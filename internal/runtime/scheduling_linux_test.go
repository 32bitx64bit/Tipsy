// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"os"
	"os/exec"
	goruntime "runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestAndroidAppSchedulingIsolatedProcess(t *testing.T) {
	const childEnv = "TIPSY_TEST_SCHEDULING_CHILD"
	if os.Getenv(childEnv) == "1" {
		testAndroidAppSchedulingChild(t)
		return
	}
	var before syscall.Rlimit
	if err := syscall.Getrlimit(androidAppRlimitRTPrio, &before); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAndroidAppSchedulingIsolatedProcess$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated scheduling test: %v\n%s", err, output)
	}
	var after syscall.Rlimit
	if err := syscall.Getrlimit(androidAppRlimitRTPrio, &after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("child altered parent RLIMIT_RTPRIO: before=%+v after=%+v", before, after)
	}
	t.Logf("parent limits unchanged (%d/%d); child output:\n%s", before.Cur, before.Max, output)
}

func testAndroidAppSchedulingChild(t *testing.T) {
	// Keep the policy/nice checks on the same OS thread. Hard-limit changes
	// occur only in this subprocess, never in the package test runner.
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	policyBefore, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETSCHEDULER, 0, 0, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	niceBefore, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	var niceLimitBefore syscall.Rlimit
	if err := syscall.Getrlimit(13, &niceLimitBefore); err != nil { // RLIMIT_NICE
		t.Fatal(err)
	}
	if err := initializeAndroidAppScheduling(); err != nil {
		t.Fatal(err)
	}
	if err := initializeAndroidAppScheduling(); err != nil {
		t.Fatalf("repeated initialization: %v", err)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(androidAppRlimitRTPrio, &limit); err != nil {
		t.Fatal(err)
	}
	if limit.Cur != 0 || limit.Max != 0 {
		t.Fatalf("actual RLIMIT_RTPRIO = %+v, want zero", limit)
	}
	for _, policy := range []uintptr{1, 2} { // Actual FIFO and RR requests.
		param := int32(1)
		_, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0, policy, uintptr(unsafe.Pointer(&param)))
		if errno != syscall.EPERM {
			t.Fatalf("real scheduler policy %d request: %v, want EPERM", policy, errno)
		}
	}
	// Invalid input retains kernel validation; this is not a blanket EPERM shim.
	param := int32(0)
	_, _, errno = syscall.RawSyscall(syscall.SYS_SCHED_SETSCHEDULER, 0, 42, uintptr(unsafe.Pointer(&param)))
	if errno != syscall.EINVAL {
		t.Fatalf("invalid real scheduler policy: %v, want EINVAL", errno)
	}
	policyAfter, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETSCHEDULER, 0, 0, 0)
	if errno != 0 || policyAfter != policyBefore {
		t.Fatalf("scheduler changed: before=%d after=%d error=%v", policyBefore, policyAfter, errno)
	}
	niceAfter, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil || niceAfter != niceBefore {
		t.Fatalf("nice changed: before=%d after=%d error=%v", niceBefore, niceAfter, err)
	}
	var niceLimitAfter syscall.Rlimit
	if err := syscall.Getrlimit(13, &niceLimitAfter); err != nil {
		t.Fatal(err)
	}
	if niceLimitAfter != niceLimitBefore {
		t.Fatalf("RLIMIT_NICE changed: before=%+v after=%+v", niceLimitBefore, niceLimitAfter)
	}
	t.Log("actual RTPRIO 0/0; FIFO/RR returned EPERM; invalid policy returned EINVAL; scheduler and nice unchanged")
}

func TestAndroidAppSchedulingStartupPreconditions(t *testing.T) {
	for _, policy := range []uintptr{0, 3, 5, 7, 0x40000000} {
		if err := validateAndroidAppSchedulingThread(policy, schedulingCapabilityData{}); err != nil {
			t.Errorf("ordinary policy %d rejected: %v", policy, err)
		}
	}
	for _, policy := range []uintptr{1, 2, 6, 0x40000001, 42} {
		if err := validateAndroidAppSchedulingThread(policy, schedulingCapabilityData{}); err == nil {
			t.Errorf("incompatible policy %d accepted", policy)
		}
	}
	for _, caps := range []schedulingCapabilityData{
		{effective: linuxCapSysNice},
		{permitted: linuxCapSysNice},
	} {
		if err := validateAndroidAppSchedulingThread(0, caps); err == nil {
			t.Errorf("scheduling capability bypass accepted: %+v", caps)
		}
	}
}
