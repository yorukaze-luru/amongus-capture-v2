package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessInfo holds information about the hooked Among Us process
type ProcessInfo struct {
	PID              uint32
	Handle           windows.Handle
	ExePath          string
	Is64Bit          bool
	GameAssemblyBase uintptr
	GameAssemblySize uint32
	GameAssemblyPath string
	UnityPlayerBase  uintptr
	UnityPlayerSize  uint32
	GameHash         string
}

// FindAmongUsProcess searches for a running Among Us process and extracts module info
func FindAmongUsProcess() (*ProcessInfo, error) {
	snap, _, err := procCreateToolhelp32Snap.Call(TH32CS_SNAPPROCESS, 0)
	if snap == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("failed to create process snapshot: %w", err)
	}
	defer procCloseHandle.Call(snap)

	var entry ProcessEntry32W
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil, fmt.Errorf("no processes found")
	}

	var targetPID uint32
	for {
		exeName := syscall.UTF16ToString(entry.ExeFile[:])
		lowerName := strings.ToLower(exeName)
		if strings.Contains(lowerName, "among us") || strings.Contains(lowerName, "amongus") {
			targetPID = entry.ProcessID
			break
		}
		ret, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}

	if targetPID == 0 {
		return nil, fmt.Errorf("Among Us process not found")
	}

	return HookProcess(targetPID)
}

// HookProcess opens a handle to the process and inspects its modules
func HookProcess(pid uint32) (*ProcessInfo, error) {
	hProcess, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_OPERATION, false, pid)
	if err != nil {
		hProcess, err = windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open process (PID %d): %w", pid, err)
	}

	// Check if process is 64-bit or 32-bit
	var isWow64 bool
	procIsWow64Process.Call(uintptr(hProcess), uintptr(unsafe.Pointer(&isWow64)))
	// If Wow64 is true, the process is 32-bit running on 64-bit Windows.
	// If Wow64 is false on 64-bit Windows, process is 64-bit native.
	is64Bit := !isWow64

	pInfo := &ProcessInfo{
		PID:     pid,
		Handle:  hProcess,
		Is64Bit: is64Bit,
	}

	// Inspect modules using CreateToolhelp32Snapshot
	modSnap, _, _ := procCreateToolhelp32Snap.Call(TH32CS_SNAPMODULE|TH32CS_SNAPMODULE32, uintptr(pid))
	if modSnap == uintptr(windows.InvalidHandle) {
		windows.CloseHandle(hProcess)
		return nil, fmt.Errorf("failed to snapshot modules for PID %d", pid)
	}
	defer procCloseHandle.Call(modSnap)

	var modEntry ModuleEntry32W
	modEntry.Size = uint32(unsafe.Sizeof(modEntry))

	ret, _, _ := procModule32FirstW.Call(modSnap, uintptr(unsafe.Pointer(&modEntry)))
	if ret == 0 {
		windows.CloseHandle(hProcess)
		return nil, fmt.Errorf("failed to enumerate modules")
	}

	for {
		modName := strings.ToLower(syscall.UTF16ToString(modEntry.ModuleName[:]))
		modPath := syscall.UTF16ToString(modEntry.ExePath[:])

		if pInfo.ExePath == "" && (strings.HasSuffix(modName, ".exe")) {
			pInfo.ExePath = modPath
		}

		if modName == "gameassembly.dll" {
			pInfo.GameAssemblyBase = modEntry.ModBaseAddr
			pInfo.GameAssemblySize = modEntry.ModBaseSize
			pInfo.GameAssemblyPath = modPath
		} else if modName == "unityplayer.dll" {
			pInfo.UnityPlayerBase = modEntry.ModBaseAddr
			pInfo.UnityPlayerSize = modEntry.ModBaseSize
		}

		ret, _, _ = procModule32NextW.Call(modSnap, uintptr(unsafe.Pointer(&modEntry)))
		if ret == 0 {
			break
		}
	}

	if pInfo.GameAssemblyBase == 0 {
		windows.CloseHandle(hProcess)
		return nil, fmt.Errorf("GameAssembly.dll not found in process PID %d (still initializing?)", pid)
	}

	// Calculate SHA256 of GameAssembly.dll file if accessible
	if pInfo.GameAssemblyPath != "" {
		hash, err := calculateFileHash(pInfo.GameAssemblyPath)
		if err == nil {
			pInfo.GameHash = hash
		}
	}

	return pInfo, nil
}

func calculateFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	buf := make([]byte, 1024*1024) // 1MB buffer for ultra-fast disk reads
	if _, err := io.CopyBuffer(hasher, file, buf); err != nil {
		return "", err
	}

	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}
