// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSignalExitCode(t *testing.T) {
	for _, tc := range []struct {
		sig  os.Signal
		want int
	}{
		{os.Interrupt, 130},
		{syscall.SIGINT, 130},
		{syscall.SIGTERM, 143},
	} {
		if got := signalExitCode(tc.sig); got != tc.want {
			t.Errorf("signalExitCode(%v) = %d, want %d", tc.sig, got, tc.want)
		}
	}
}

// TestAwaitSignalEscalationSecondSignalExits verifies the first signal cancels
// the app context without forcing an exit and the second one force-exits with
// the conventional status for that signal.
func TestAwaitSignalEscalationSecondSignalExits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  os.Signal
		second os.Signal
		want   int
	}{
		{name: "interrupt twice", first: syscall.SIGINT, second: syscall.SIGINT, want: 130},
		{name: "terminate twice", first: syscall.SIGTERM, second: syscall.SIGTERM, want: 143},
		{name: "interrupt then terminate", first: syscall.SIGINT, second: syscall.SIGTERM, want: 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			signals := make(chan os.Signal, 2)
			forced := make(chan int, 1)
			done := make(chan struct{})
			exited := make(chan struct{})
			go func() {
				awaitSignalEscalation(signals, cancel, func(code int) { forced <- code }, done)
				close(exited)
			}()

			signals <- tc.first
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("first signal did not cancel the app context")
			}
			select {
			case code := <-forced:
				t.Fatalf("first signal force-exited with code %d", code)
			case <-time.After(20 * time.Millisecond):
			}

			signals <- tc.second
			select {
			case code := <-forced:
				if code != tc.want {
					t.Fatalf("second signal exit code = %d, want %d", code, tc.want)
				}
			case <-time.After(time.Second):
				t.Fatal("second signal did not force an immediate exit")
			}
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Fatal("signal listener did not return after force-exit")
			}
			close(done)
		})
	}
}

func TestAwaitSignalEscalationRetiresWithoutSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	forced := make(chan int, 1)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		awaitSignalEscalation(signals, cancel, func(code int) { forced <- code }, done)
		close(exited)
	}()
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("signal listener did not retire after shutdown")
	}
	select {
	case code := <-forced:
		t.Fatalf("retired listener force-exited with code %d", code)
	default:
	}
	if ctx.Err() != nil {
		t.Fatal("retiring an unused listener cancelled the app context")
	}
}

// TestSignalChannelNotifierStopsCleanly exercises production registration
// without sending a real process signal, and checks stop is idempotent.
func TestSignalChannelNotifierStopsCleanly(t *testing.T) {
	ctx, stop := signalChannelNotifier(func(int) {})(context.Background(), os.Interrupt, syscall.SIGTERM)
	if ctx.Err() != nil {
		t.Fatal("notifier cancelled before any signal")
	}
	stop()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("notifier stop did not cancel the app context")
	}
}
