package main

import (
	"syscall"
	"unsafe"
)

type processStats struct {
	WorkingSet   uint64  `json:"workingSet"`
	PrivateBytes uint64  `json:"privateBytes"`
	CPUSeconds   float64 `json:"cpuSeconds"`
}

func readProcessStats(pid int) processStats {
	k := syscall.NewLazyDLL("kernel32.dll")
	p := syscall.NewLazyDLL("psapi.dll")
	h, _, _ := k.NewProc("OpenProcess").Call(0x410, 0, uintptr(pid))
	if h == 0 {
		return processStats{}
	}
	defer k.NewProc("CloseHandle").Call(h)
	var mem struct {
		CB, Faults                                                                                            uint32
		PeakWS, WS, PeakPoolPaged, PoolPaged, PeakPoolNonPaged, PoolNonPaged, Pagefile, PeakPagefile, Private uintptr
	}
	mem.CB = uint32(unsafe.Sizeof(mem))
	p.NewProc("GetProcessMemoryInfo").Call(h, uintptr(unsafe.Pointer(&mem)), uintptr(mem.CB))
	var created, exited, kernel, user syscall.Filetime
	k.NewProc("GetProcessTimes").Call(h, uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	ticks := func(t syscall.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }
	return processStats{uint64(mem.WS), uint64(mem.Private), float64(ticks(kernel)+ticks(user)) / 1e7}
}
