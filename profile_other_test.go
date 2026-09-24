//go:build !windows

package main

type processStats struct {
	WorkingSet   uint64  `json:"workingSet"`
	PrivateBytes uint64  `json:"privateBytes"`
	CPUSeconds   float64 `json:"cpuSeconds"`
}

func readProcessStats(pid int) processStats { return processStats{} }
