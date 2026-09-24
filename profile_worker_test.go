package main

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

type profileWorkerTracker struct {
	mu   sync.Mutex
	last map[string]any
}

func (p *profileWorkerTracker) observe(cmd *exec.Cmd) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	var workerWS, workerPrivate, combinedWS, hostWS uint64
	at := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			w, h := readProcessStats(cmd.Process.Pid), readProcessStats(os.Getpid())
			workerWS = max(workerWS, w.WorkingSet)
			workerPrivate = max(workerPrivate, w.PrivateBytes)
			combinedWS = max(combinedWS, w.WorkingSet+h.WorkingSet)
			hostWS = max(hostWS, h.WorkingSet)
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		cpu := 0.0
		if cmd.ProcessState != nil {
			cpu = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.last = map[string]any{"seconds": time.Since(at).Seconds(), "cpuSeconds": cpu, "workerPeakWorkingSet": workerWS, "workerPeakPrivateBytes": workerPrivate, "combinedPeakWorkingSet": combinedWS, "hostPeakWorkingSet": hostWS, "samplingMs": 5}
	}
}

func (p *profileWorkerTracker) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]any)
	for k, v := range p.last {
		out[k] = v
	}
	return out
}
