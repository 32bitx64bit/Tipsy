// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// Linux UAPI values absent from the frozen syscall package.
const (
	androidAppRlimitRTPrio = 14
	linuxCapabilityV3      = 0x20080522
	linuxCapSysNice        = uint32(1 << 23)
)

type schedulingCapabilityHeader struct {
	version uint32
	pid     int32
}

type schedulingCapabilityData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

// initializeAndroidAppScheduling establishes the ordinary Android app's
// real-time privilege limit in this client process. Call before guest loading
// or any guest thread creation; concurrent scheduler/capability changes are
// outside this startup contract. The hard-limit reduction lasts until exit.
//
// AOSP bionic pthread.h documents that ordinary apps cannot promote themselves
// to a real-time policy. Using the real resource limit preserves libc/kernel
// validation and EPERM, without changing nice, affinity, or priority queries.
// Existing real-time threads and CAP_SYS_NICE would bypass this boundary, so
// incompatible startup is reported before changing the limit.
func initializeAndroidAppScheduling() error {
	if err := checkAndroidAppSchedulingThreads(); err != nil {
		return fmt.Errorf("initialize Android app scheduling: %w", err)
	}
	if err := syscall.Setrlimit(androidAppRlimitRTPrio, &syscall.Rlimit{}); err != nil {
		return fmt.Errorf("initialize Android app scheduling: set RLIMIT_RTPRIO to zero: %w", err)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(androidAppRlimitRTPrio, &limit); err != nil {
		return fmt.Errorf("initialize Android app scheduling: verify RLIMIT_RTPRIO: %w", err)
	}
	if limit.Cur != 0 || limit.Max != 0 {
		return fmt.Errorf("initialize Android app scheduling: RLIMIT_RTPRIO remained %d/%d", limit.Cur, limit.Max)
	}
	return nil
}

func checkAndroidAppSchedulingThreads() error {
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return fmt.Errorf("enumerate startup threads: %w", err)
	}
	for _, thread := range threads {
		tid, err := strconv.ParseInt(thread.Name(), 10, 32)
		if err != nil {
			return fmt.Errorf("invalid startup thread ID %q: %w", thread.Name(), err)
		}
		policy, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETSCHEDULER, uintptr(tid), 0, 0)
		if errno == syscall.ESRCH {
			continue // A Go/runtime thread may exit during enumeration.
		}
		if errno != 0 {
			return fmt.Errorf("read thread %d scheduler: %w", tid, errno)
		}
		header := schedulingCapabilityHeader{version: linuxCapabilityV3, pid: int32(tid)}
		var caps [2]schedulingCapabilityData
		_, _, errno = syscall.RawSyscall(syscall.SYS_CAPGET,
			uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&caps[0])), 0)
		if errno == syscall.ESRCH {
			continue
		}
		if errno != 0 {
			return fmt.Errorf("read thread %d scheduling capabilities: %w", tid, errno)
		}
		if err := validateAndroidAppSchedulingThread(policy, caps[0]); err != nil {
			return fmt.Errorf("thread %d: %w", tid, err)
		}
	}
	return nil
}

func validateAndroidAppSchedulingThread(policy uintptr, caps schedulingCapabilityData) error {
	// SCHED_RESET_ON_FORK is returned alongside the policy and is compatible.
	policy &^= 0x40000000
	switch policy {
	case 0, 3, 5, 7: // OTHER, BATCH, IDLE, EXT: ordinary scheduling classes.
	case 1, 2, 6: // FIFO, RR, DEADLINE: lowering a limit does not demote these.
		return fmt.Errorf("real-time scheduler policy %d is incompatible with Android app startup", policy)
	default:
		return fmt.Errorf("unsupported startup scheduler policy %d", policy)
	}
	// Permitted SYS_NICE can be made effective without gaining a new privilege.
	if (caps.effective|caps.permitted)&linuxCapSysNice != 0 {
		return fmt.Errorf("CAP_SYS_NICE is incompatible with Android app startup")
	}
	return nil
}
