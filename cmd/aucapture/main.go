package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"amongus-capture-v2/pkg/bot"
	"amongus-capture-v2/pkg/config"
	"amongus-capture-v2/pkg/memory"
	"amongus-capture-v2/pkg/web"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	AppVersion = "2.2.0-SelfHosted"
	Banner     = `
=====================================================================
   🚀 AmongUsCapture v2 (Dedicated Client & Self-Hosted Discord Bot)
   Standalone App • Memory ~7MB • Instant Launch • All-in-One
=====================================================================
`
)

var (
	globalDiscordBot  *bot.DiscordBot
	globalGameReader  *memory.GameReader
	globalCfgMgr      *config.ConfigManager
	globalHWND        uintptr
	globalMutexHandle uintptr
	origWndProc       uintptr
)

func init() {
	// Lock main OS thread for Win32 / COM / WebView2 message loop requirements
	runtime.LockOSThread()
}

func main() {
	fmt.Print(Banner)

	// Command line flags
	portFlag := flag.Int("port", 0, "WebUI dashboard port (0 = auto select free port)")
	browserModeFlag := flag.Bool("browser", false, "Open in external web browser instead of dedicated native window")
	headlessFlag := flag.Bool("headless", false, "Run in background headless mode (no GUI window)")
	versionFlag := flag.Bool("v", false, "Show version")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("AmongUsCapture v%s (%s/%s)\n", AppVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	// 1. Enable Kill-On-Close Job Object on Windows (Kills all child processes like WebView2 automatically)
	enableKillOnCloseJob()

	// 2. Clean up any lingering old instances if present
	killDuplicateInstances()

	// 3. Single Instance Mutex on Windows
	if runtime.GOOS == "windows" {
		modKernel32 := windows.NewLazySystemDLL("kernel32.dll")
		procCreateMutexW := modKernel32.NewProc("CreateMutexW")
		mutexName, _ := windows.UTF16PtrFromString("Local\\AmongUsCaptureV2_SingleInstance_Mutex")
		hMutex, _, _ := procCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(mutexName)))
		globalMutexHandle = hMutex
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 0. Clean legacy storage once to reset previous cache/state as requested
	config.CleanLegacyStorage()

	// 1. Initialize Configuration
	cfgMgr := config.NewConfigManager()
	globalCfgMgr = cfgMgr
	cfg := cfgMgr.Get()

	// Determine Port
	targetPort := *portFlag
	if targetPort == 0 {
		if cfg.WebUIPort != 0 {
			targetPort = cfg.WebUIPort
		} else {
			// Find free port
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err == nil {
				targetPort = l.Addr().(*net.TCPAddr).Port
				l.Close()
			} else {
				targetPort = 8123
			}
		}
	}

	// 2. Initialize Offset Manager
	offsetMgr := memory.NewOffsetManager(cfg.OffsetsURL)

	// 3. Initialize Game Memory Reader in background
	gameReader := memory.NewGameReader(offsetMgr)
	globalGameReader = gameReader
	go gameReader.Start(ctx)

	// 4. Initialize Self-Hosted Discord Bot
	discordBot := bot.NewDiscordBot(cfgMgr, gameReader)
	globalDiscordBot = discordBot

	// Handle OS Signals (SIGINT, SIGTERM)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down AmongUsCapture v2 gracefully...")
		cancel()
		cleanupAndExit()
	}()

	// 5. Initialize Web Dashboard Server in background
	webServer := web.NewWebServer(targetPort, gameReader, discordBot, cfgMgr, offsetMgr)
	go func() {
		if err := webServer.Start(ctx); err != nil {
			log.Printf("Web server error: %v", err)
		}
	}()

	dashboardURL := fmt.Sprintf("http://localhost:%d", targetPort)
	log.Printf("Dedicated Client GUI loading from: %s", dashboardURL)

	// Isolate WebView2 User Data Directory to TEMP folder
	tempDir := os.TempDir()
	wvUserData := fmt.Sprintf("%s\\AmongUsCaptureV2_WebView2", tempDir)
	os.Setenv("WEBVIEW2_USER_DATA_FOLDER", wvUserData)

	// Launch Mode: Native Window (Default) on Main Thread vs Browser vs Headless
	if !*headlessFlag {
		if *browserModeFlag || runtime.GOOS != "windows" {
			// Fallback to browser
			go func() {
				time.Sleep(500 * time.Millisecond)
				openBrowser(dashboardURL)
			}()
			<-ctx.Done()
		} else {
			// Dedicated Native Windows Desktop GUI via WebView2 (Ran directly on Main Thread)
			time.Sleep(100 * time.Millisecond)

			winWidth := 820
			winHeight := 640
			if cfg.WindowWidth >= 400 {
				winWidth = cfg.WindowWidth
			}
			if cfg.WindowHeight >= 300 {
				winHeight = cfg.WindowHeight
			}

			// Center by default if position was never saved
			isCenter := (cfg.WindowX == -1 && cfg.WindowY == -1)

			w := webview2.NewWithOptions(webview2.WebViewOptions{
				Debug:     cfg.Debug,
				AutoFocus: true,
				DataPath:  wvUserData,
				WindowOptions: webview2.WindowOptions{
					Title:  "AmongUsCapture v2 - Dedicated Client",
					Width:  uint(winWidth),
					Height: uint(winHeight),
					IconId: 0,
					Center: isCenter,
				},
			})
			if w != nil {
				hwnd := uintptr(w.Window())
				globalHWND = hwnd

				// Restore exact position and maximized state across multiple monitors
				if !isCenter && cfg.WindowX != -1 && cfg.WindowY != -1 {
					restoreWindowPos(hwnd, cfg.WindowX, cfg.WindowY, winWidth, winHeight, cfg.WindowMaximized)
				} else if cfg.WindowMaximized {
					user32 := windows.NewLazySystemDLL("user32.dll")
					procShowWindow := user32.NewProc("ShowWindow")
					procShowWindow.Call(hwnd, 3) // SW_SHOWMAXIMIZED = 3
				}

				// Hook Win32 Window Procedure directly to catch WM_CLOSE instantly
				hookWindowProc(hwnd)

				// Bind explicit exit function to JS
				_ = w.Bind("appExit", func() {
					log.Println("[Lifecycle] appExit called from frontend. Exiting immediately...")
					saveWindowPlacement()
					cleanupAndExit()
				})

				// Active Safety Watcher: Check if HWND still exists every 100ms
				go func() {
					modUser32 := windows.NewLazySystemDLL("user32.dll")
					procIsWindow := modUser32.NewProc("IsWindow")

					time.Sleep(500 * time.Millisecond)
					for {
						time.Sleep(100 * time.Millisecond)
						ret, _, _ := procIsWindow.Call(hwnd)
						if ret == 0 {
							log.Println("[Lifecycle] Window destroyed detected by watcher. Exiting process now...")
							saveWindowPlacement()
							cleanupAndExit()
							return
						}
					}
				}()

				w.SetSize(winWidth, winHeight, webview2.HintNone)
				w.Navigate(dashboardURL)
				w.Run() // Blocks on main thread until window is closed
				w.Destroy()
			} else {
				log.Println("WebView2 not available, falling back to external browser.")
				openBrowser(dashboardURL)
				<-ctx.Done()
			}
		}
	} else {
		<-ctx.Done()
	}

	// Clean shutdown & Instant termination
	cancel()
	cleanupAndExit()
}

type rectStruct struct {
	Left, Top, Right, Bottom int32
}

type pointStruct struct {
	X, Y int32
}

type windowPlacementStruct struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    pointStruct
	PtMaxPosition    pointStruct
	RcNormalPosition rectStruct
}

func saveWindowPlacement() {
	if globalHWND == 0 || globalCfgMgr == nil || runtime.GOOS != "windows" {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	procGetWindowPlacement := user32.NewProc("GetWindowPlacement")
	procIsZoomed := user32.NewProc("IsZoomed")

	var wp windowPlacementStruct
	wp.Length = uint32(unsafe.Sizeof(wp))
	ret, _, _ := procGetWindowPlacement.Call(globalHWND, uintptr(unsafe.Pointer(&wp)))

	isMaximized := false
	maxRet, _, _ := procIsZoomed.Call(globalHWND)
	if maxRet != 0 {
		isMaximized = true
	}

	if ret != 0 {
		r := wp.RcNormalPosition
		w := int(r.Right - r.Left)
		h := int(r.Bottom - r.Top)
		x := int(r.Left)
		y := int(r.Top)

		if w >= 400 && h >= 300 {
			_ = globalCfgMgr.Update(func(c *config.AppConfig) {
				c.WindowX = x
				c.WindowY = y
				c.WindowWidth = w
				c.WindowHeight = h
				c.WindowMaximized = isMaximized
			})
			_ = globalCfgMgr.Save()
		}
	}
}

func restoreWindowPos(hwnd uintptr, x, y, width, height int, maximized bool) {
	if hwnd == 0 || runtime.GOOS != "windows" {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	procSetWindowPos := user32.NewProc("SetWindowPos")
	procMonitorFromRect := user32.NewProc("MonitorFromRect")
	procShowWindow := user32.NewProc("ShowWindow")

	// Multi-Monitor check: verify rect intersects with any active connected monitor
	r := rectStruct{
		Left:   int32(x),
		Top:    int32(y),
		Right:  int32(x + width),
		Bottom: int32(y + height),
	}

	const MONITOR_DEFAULTTONULL = 0
	hMonitor, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), MONITOR_DEFAULTTONULL)
	if hMonitor == 0 {
		// Target monitor was disconnected or coordinates are offscreen -> Fallback to primary screen
		procGetSystemMetrics := user32.NewProc("GetSystemMetrics")
		screenWidth, _, _ := procGetSystemMetrics.Call(0)
		screenHeight, _, _ := procGetSystemMetrics.Call(1)
		x = (int(screenWidth) - width) / 2
		y = (int(screenHeight) - height) / 2
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
	}

	const SWP_NOZORDER = 0x0004
	const SWP_NOACTIVATE = 0x0010
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(width), uintptr(height), SWP_NOZORDER|SWP_NOACTIVATE)

	if maximized {
		const SW_SHOWMAXIMIZED = 3
		procShowWindow.Call(hwnd, SW_SHOWMAXIMIZED)
	}
}

var exitOnce sync.Once

func customWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	const (
		WM_CLOSE     = 0x0010
		WM_DESTROY   = 0x0002
		WM_NCDESTROY = 0x0082
		WM_QUIT      = 0x0012
	)

	if msg == WM_CLOSE {
		saveWindowPlacement()

		// 1. Instantly hide window so UI never hangs for the user
		user32 := windows.NewLazySystemDLL("user32.dll")
		procShowWindow := user32.NewProc("ShowWindow")
		procShowWindow.Call(hwnd, 0) // SW_HIDE = 0

		cleanupAndExitAsync()
		return 0
	} else if msg == WM_DESTROY || msg == WM_NCDESTROY || msg == WM_QUIT {
		saveWindowPlacement()
		cleanupAndExitAsync()
		return 0
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	procCallWindowProcW := user32.NewProc("CallWindowProcW")
	ret, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func hookWindowProc(hwnd uintptr) {
	if hwnd == 0 || runtime.GOOS != "windows" {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	procSetWindowLongPtrW := user32.NewProc("SetWindowLongPtrW")
	if procSetWindowLongPtrW.Find() != nil {
		procSetWindowLongPtrW = user32.NewProc("SetWindowLongW")
	}

	cb := windows.NewCallback(customWndProc)
	ret, _, _ := procSetWindowLongPtrW.Call(hwnd, ^uintptr(3), cb)
	origWndProc = ret
}

func cleanupAndExitAsync() {
	exitOnce.Do(func() {
		saveWindowPlacement()

		go func() {
			if globalDiscordBot != nil {
				globalDiscordBot.DeleteStatusEmbed()
			}
			if globalMutexHandle != 0 {
				windows.CloseHandle(windows.Handle(globalMutexHandle))
			}
			time.Sleep(40 * time.Millisecond)
			exitProcessImmediate(0)
		}()

		// Hard deadline: terminate unconditionally after 250ms
		go func() {
			time.Sleep(250 * time.Millisecond)
			exitProcessImmediate(0)
		}()
	})
}

func cleanupAndExit() {
	saveWindowPlacement()
	cleanupAndExitAsync()
	time.Sleep(50 * time.Millisecond)
	exitProcessImmediate(0)
}

func exitProcessImmediate(code uint32) {
	if runtime.GOOS == "windows" {
		windows.ExitProcess(code)
	}
	os.Exit(int(code))
}

func killDuplicateInstances() {
	if runtime.GOOS != "windows" {
		return
	}
	myPID := os.Getpid()
	execPath, err := os.Executable()
	if err != nil {
		return
	}
	baseName := filepath.Base(execPath)

	modKernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procCreateToolhelp32Snapshot := modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW := modKernel32.NewProc("Process32FirstW")
	procProcess32NextW := modKernel32.NewProc("Process32NextW")
	procOpenProcess := modKernel32.NewProc("OpenProcess")
	procTerminateProcess := modKernel32.NewProc("TerminateProcess")
	procCloseHandle := modKernel32.NewProc("CloseHandle")

	const TH32CS_SNAPPROCESS = 0x00000002
	const PROCESS_TERMINATE = 0x0001

	type PROCESSENTRY32W struct {
		Size            uint32
		Usage           uint32
		ProcessID       uint32
		DefaultHeapID   uintptr
		ModuleID        uint32
		Threads         uint32
		ParentProcessID uint32
		PriClassBase    int32
		Flags           uint32
		ExeFile         [windows.MAX_PATH]uint16
	}

	hSnap, _, _ := procCreateToolhelp32Snapshot.Call(TH32CS_SNAPPROCESS, 0)
	if hSnap == 0 || hSnap == uintptr(windows.InvalidHandle) {
		return
	}
	defer procCloseHandle.Call(hSnap)

	var entry PROCESSENTRY32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(hSnap, uintptr(unsafe.Pointer(&entry)))
	for ret != 0 {
		if entry.ProcessID != uint32(myPID) {
			pName := windows.UTF16ToString(entry.ExeFile[:])
			if strings.EqualFold(pName, baseName) {
				hProc, _, _ := procOpenProcess.Call(PROCESS_TERMINATE, 0, uintptr(entry.ProcessID))
				if hProc != 0 {
					procTerminateProcess.Call(hProc, 0)
					procCloseHandle.Call(hProc)
					log.Printf("[Lifecycle] Cleaned up lingering instance (PID: %d)", entry.ProcessID)
				}
			}
		}
		ret, _, _ = procProcess32NextW.Call(hSnap, uintptr(unsafe.Pointer(&entry)))
	}
}

func enableKillOnCloseJob() {
	if runtime.GOOS != "windows" {
		return
	}
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procCreateJobObjectW := kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject := kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject := kernel32.NewProc("AssignProcessToJobObject")
	procGetCurrentProcess := kernel32.NewProc("GetCurrentProcess")

	hJob, _, _ := procCreateJobObjectW.Call(0, 0)
	if hJob == 0 {
		return
	}

	const JobObjectExtendedLimitInformation = 9
	const JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000

	type JOBOBJECT_BASIC_LIMIT_INFORMATION struct {
		PerProcessUserTimeLimit int64
		PerJobUserTimeLimit     int64
		LimitFlags              uint32
		MinimumWorkingSetSize   uintptr
		MaximumWorkingSetSize   uintptr
		ActiveProcessLimit      uint32
		Affinity                uintptr
		PriorityClass           uint32
		SchedulingClass         uint32
	}

	type IO_COUNTERS struct {
		ReadOperationCount  uint64
		WriteOperationCount uint64
		OtherOperationCount uint64
		ReadTransferCount   uint64
		WriteTransferCount  uint64
		OtherTransferCount  uint64
	}

	type JOBOBJECT_EXTENDED_LIMIT_INFORMATION struct {
		BasicLimitInformation JOBOBJECT_BASIC_LIMIT_INFORMATION
		IoInfo                IO_COUNTERS
		ProcessMemoryLimit    uintptr
		JobMemoryLimit        uintptr
		PeakProcessMemoryLimit uintptr
		PeakJobMemoryLimit    uintptr
	}

	var info JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	ret, _, _ := procSetInformationJobObject.Call(
		hJob,
		JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ret == 0 {
		return
	}

	hProcess, _, _ := procGetCurrentProcess.Call()
	procAssignProcessToJobObject.Call(hJob, hProcess)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

