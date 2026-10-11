package metrics

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modpsapi    = windows.NewLazySystemDLL("psapi.dll")

	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
	procGetProcessTimes      = modkernel32.NewProc("GetProcessTimes")
	procGetCurrentProcess    = modkernel32.NewProc("GetCurrentProcess")
)

type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

type fileTime struct {
	lowDateTime  uint32
	highDateTime uint32
}

func (ft fileTime) toNanoseconds() int64 {
	// 100-nanosecond intervals since Jan 1, 1601
	total := (int64(ft.highDateTime) << 32) | int64(ft.lowDateTime)
	return total * 100
}

// ProcessMetrics represents real-time performance and resource utilization of this application
type ProcessMetrics struct {
	WorkingSetMB float64 `json:"working_set_mb"` // OS Physical RAM (matches Task Manager)
	AllocMB      float64 `json:"alloc_mb"`       // Go Runtime Allocated Heap RAM
	SysMB        float64 `json:"sys_mb"`         // Go Runtime System Memory
	CPUPercent   float64 `json:"cpu_percent"`    // Process CPU Usage (0.0% - 100.0%)
	Goroutines   int     `json:"goroutines"`     // Current active Go routines
	Summary      string  `json:"summary"`        // e.g. "8.4 MB (0.1%)"
	Detail       string  `json:"detail"`         // e.g. "RAM: 8.4 MB | CPU: 0.1% | Go: 14"
}

// Monitor tracks system resource consumption over time
type Monitor struct {
	mu           sync.Mutex
	lastSample   time.Time
	lastProcTime int64
	numCPU       int
	cached       ProcessMetrics
}

var (
	globalMonitor     *Monitor
	globalMonitorOnce sync.Once
)

// GetMonitor returns the singleton resource monitor
func GetMonitor() *Monitor {
	globalMonitorOnce.Do(func() {
		globalMonitor = &Monitor{
			numCPU: runtime.NumCPU(),
		}
		if globalMonitor.numCPU < 1 {
			globalMonitor.numCPU = 1
		}
		// Initial sample
		globalMonitor.Sample()
	})
	return globalMonitor
}

// Sample calculates current metrics and returns an up-to-date snapshot
func (m *Monitor) Sample() ProcessMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var wsBytes uintptr
	var currentProcTime int64

	if runtime.GOOS == "windows" {
		hProc, _, _ := procGetCurrentProcess.Call()
		if hProc != 0 {
			// 1. Memory Working Set (Task Manager's Working Set Memory)
			var pmc processMemoryCounters
			pmc.cb = uint32(unsafe.Sizeof(pmc))
			ret, _, _ := procGetProcessMemoryInfo.Call(hProc, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb))
			if ret != 0 {
				wsBytes = pmc.workingSetSize
			}

			// 2. CPU Usage
			var ftCreation, ftExit, ftKernel, ftUser fileTime
			retTime, _, _ := procGetProcessTimes.Call(
				hProc,
				uintptr(unsafe.Pointer(&ftCreation)),
				uintptr(unsafe.Pointer(&ftExit)),
				uintptr(unsafe.Pointer(&ftKernel)),
				uintptr(unsafe.Pointer(&ftUser)),
			)
			if retTime != 0 {
				currentProcTime = ftKernel.toNanoseconds() + ftUser.toNanoseconds()
			}
		}
	}

	// Read Go memory stats as fallback/detail
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	allocMB := float64(ms.Alloc) / (1024 * 1024)
	sysMB := float64(ms.Sys) / (1024 * 1024)

	var wsMB float64
	if wsBytes > 0 {
		wsMB = float64(wsBytes) / (1024 * 1024)
	} else {
		wsMB = sysMB
	}

	// Calculate CPU usage
	var cpuPercent float64
	if !m.lastSample.IsZero() && currentProcTime > 0 && m.lastProcTime > 0 {
		elapsedWallNS := now.Sub(m.lastSample).Nanoseconds()
		elapsedProcNS := currentProcTime - m.lastProcTime

		if elapsedWallNS > 0 && elapsedProcNS >= 0 {
			totalCapacityNS := elapsedWallNS * int64(m.numCPU)
			cpuPercent = (float64(elapsedProcNS) / float64(totalCapacityNS)) * 100.0
			if cpuPercent > 100.0 {
				cpuPercent = 100.0
			}
			if math.IsNaN(cpuPercent) || math.IsInf(cpuPercent, 0) || cpuPercent < 0 {
				cpuPercent = 0.0
			}
		}
	}

	m.lastSample = now
	m.lastProcTime = currentProcTime

	numGoroutines := runtime.NumGoroutine()

	summary := fmt.Sprintf("%.1f MB (%.1f%%)", wsMB, cpuPercent)
	detail := fmt.Sprintf("RAM: %.1f MB | CPU: %.1f%% | Goroutines: %d", wsMB, cpuPercent, numGoroutines)

	m.cached = ProcessMetrics{
		WorkingSetMB: math.Round(wsMB*10) / 10,
		AllocMB:      math.Round(allocMB*10) / 10,
		SysMB:        math.Round(sysMB*10) / 10,
		CPUPercent:   math.Round(cpuPercent*10) / 10,
		Goroutines:   numGoroutines,
		Summary:      summary,
		Detail:       detail,
	}

	return m.cached
}

// Current returns the most recently cached metrics without forcing a resample
func (m *Monitor) Current() ProcessMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cached
}
