package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

func startChromeProcess(cmd *exec.Cmd) (stopTree func() error, err error) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	job, _, callErr := kernel.NewProc("CreateJobObjectW").Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("CreateJobObjectW: %w", callErr)
	}
	closeJob := func() error { return syscall.CloseHandle(syscall.Handle(job)) }
	defer func() {
		if err != nil {
			_ = closeJob()
		}
	}()

	// JOBOBJECT_EXTENDED_LIMIT_INFORMATION, with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
	var limits struct {
		PerProcessUserTimeLimit, PerJobUserTimeLimit int64
		LimitFlags                                   uint32
		MinimumWorkingSetSize, MaximumWorkingSetSize uintptr
		ActiveProcessLimit                           uint32
		Affinity                                     uintptr
		PriorityClass, SchedulingClass               uint32
		IOCounters                                   [6]uint64
		ProcessMemoryLimit, JobMemoryLimit           uintptr
		PeakProcessMemoryUsed, PeakJobMemoryUsed     uintptr
	}
	limits.LimitFlags = 0x2000
	if ok, _, callErr := kernel.NewProc("SetInformationJobObject").Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		return nil, fmt.Errorf("SetInformationJobObject: %w", callErr)
	}
	// Assign Chrome to the job before its first instruction, so even the first
	// Crashpad process inherits the job. os/exec closes the primary thread handle.
	const createSuspended = 0x0004
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	const processSetQuota = 0x0100
	process, err := syscall.OpenProcess(processSetQuota|syscall.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("open Chrome process: %w", err)
	}
	defer syscall.CloseHandle(process)
	if ok, _, callErr := kernel.NewProc("AssignProcessToJobObject").Call(job, uintptr(process)); ok == 0 {
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", callErr)
	}
	if err := resumeChromeProcess(kernel, uint32(cmd.Process.Pid)); err != nil {
		return nil, err
	}
	return func() error {
		// Explicitly terminate the job even if another handle references it.
		ok, _, callErr := kernel.NewProc("TerminateJobObject").Call(job, 1)
		closeErr := closeJob()
		if ok == 0 {
			return errors.Join(fmt.Errorf("TerminateJobObject: %w", callErr), closeErr)
		}
		return closeErr
	}, nil
}

func resumeChromeProcess(kernel *syscall.LazyDLL, pid uint32) error {
	const snapshotThreads = 0x0004
	snapshot, err := syscall.CreateToolhelp32Snapshot(snapshotThreads, 0)
	if err != nil {
		return fmt.Errorf("snapshot Chrome startup thread: %w", err)
	}
	defer syscall.CloseHandle(snapshot)
	// THREADENTRY32. A newly created suspended process has one thread.
	var entry struct {
		Size, Usage, ID, OwnerPID, BasePriority, DeltaPriority, Flags uint32
	}
	entry.Size = uint32(unsafe.Sizeof(entry))
	next := kernel.NewProc("Thread32Next")
	ok, _, callErr := kernel.NewProc("Thread32First").Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.OwnerPID == pid {
			const threadSuspendResume = 0x0002
			thread, _, callErr := kernel.NewProc("OpenThread").Call(threadSuspendResume, 0, uintptr(entry.ID))
			if thread == 0 {
				return fmt.Errorf("open Chrome startup thread: %w", callErr)
			}
			defer syscall.CloseHandle(syscall.Handle(thread))
			if count, _, callErr := kernel.NewProc("ResumeThread").Call(thread); uint32(count) == ^uint32(0) {
				return fmt.Errorf("resume Chrome startup thread: %w", callErr)
			}
			return nil
		}
		ok, _, callErr = next.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("find Chrome startup thread for process %d: %w", pid, callErr)
}

func chromeProfileLocked(err error) bool {
	const sharingViolation, lockViolation = syscall.Errno(32), syscall.Errno(33)
	return errors.Is(err, os.ErrPermission) || errors.Is(err, sharingViolation) || errors.Is(err, lockViolation)
}
