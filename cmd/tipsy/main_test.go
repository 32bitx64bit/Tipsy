// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMainSourceExists(t *testing.T) {
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal(err)
	}
}

func TestRunWiresInterruptContextAndPreservesExitCode(t *testing.T) {
	t.Parallel()

	args := []string{"launch", "--probe"}
	stdin := bytes.NewBufferString("input")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stopCalled := false
	runnerCalled := false

	notify := func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		if parent == nil {
			t.Fatal("notify parent context is nil")
		}
		if len(signals) != 2 || signals[0] != os.Interrupt || signals[1] != syscall.SIGTERM {
			t.Fatalf("notify signals = %v, want [%v %v]", signals, os.Interrupt, syscall.SIGTERM)
		}
		ctx, cancel := context.WithCancel(parent)
		return ctx, func() {
			stopCalled = true
			cancel()
		}
	}
	runApp := func(ctx context.Context, gotArgs []string, gotStdin io.Reader, gotStdout, gotStderr io.Writer) int {
		runnerCalled = true
		if ctx == nil {
			t.Fatal("runner context is nil")
		}
		if ctx.Err() != nil {
			t.Fatalf("runner context unexpectedly cancelled: %v", ctx.Err())
		}
		if len(gotArgs) != len(args) || gotArgs[0] != args[0] || gotArgs[1] != args[1] {
			t.Fatalf("runner args = %v, want %v", gotArgs, args)
		}
		if gotStdin != stdin || gotStdout != &stdout || gotStderr != &stderr {
			t.Fatal("runner streams were not passed through unchanged")
		}
		return 37
	}

	if got := run(notify, runApp, args, stdin, &stdout, &stderr); got != 37 {
		t.Fatalf("run exit code = %d, want 37", got)
	}
	if !runnerCalled {
		t.Fatal("runner was not called")
	}
	if !stopCalled {
		t.Fatal("signal notification cleanup was not called")
	}
}

func TestRunPropagatesNotifierCancellation(t *testing.T) {
	t.Parallel()

	registered := make(chan context.CancelFunc, 1)
	stopped := make(chan struct{}, 1)
	notify := func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		registered <- cancel
		return ctx, func() {
			cancel()
			stopped <- struct{}{}
		}
	}
	runApp := func(ctx context.Context, _ []string, _ io.Reader, _, _ io.Writer) int {
		<-ctx.Done()
		return 19
	}

	exited := make(chan int, 1)
	go func() {
		exited <- run(notify, runApp, nil, bytes.NewReader(nil), io.Discard, io.Discard)
	}()
	(<-registered)()

	if got := <-exited; got != 19 {
		t.Fatalf("run exit code after cancellation = %d, want 19", got)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("signal notification cleanup was not called after cancellation")
	}
}
