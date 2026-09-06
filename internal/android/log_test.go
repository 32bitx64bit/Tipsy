// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

func captureAndroidSlog(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	logging.RefreshDebugEnabled()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		logging.RefreshDebugEnabled()
	})
	return &buf
}

func TestAndroidLogPrintSkipsDisabledDebug(t *testing.T) {
	t.Setenv("TIPSY_LOG", "all")
	t.Setenv("TIPSY_LOG_LEVEL", "info")
	logging.Init()
	buf := captureAndroidSlog(t, slog.LevelInfo)
	resetTestLogCounters()

	if testAndroidLogPrint(3, "rbx", "debug-noise") != 1 {
		t.Fatal("print should still report success when skipped")
	}
	if testAndroidLogVPrint(2, "rbx", "verbose-noise") != 1 {
		t.Fatal("vprint should still report success when skipped")
	}
	if testAndroidLogWrite(3, "rbx", "write-noise") != 1 {
		t.Fatal("write should still report success when skipped")
	}
	if testAndroidLogBufWrite(0, 3, "rbx", "buf-noise") != 1 {
		t.Fatal("buf_write should still report success when skipped")
	}
	if got := testAndroidLogSkipCount(); got != 4 {
		t.Fatalf("C skip count=%d want 4", got)
	}
	if got := testLogWriteCalls(); got != 0 {
		t.Fatalf("GoAndroid_LogWrite entered %d times for skipped DEBUG/VERBOSE", got)
	}
	out := buf.String()
	for _, noise := range []string{"debug-noise", "verbose-noise", "write-noise", "buf-noise"} {
		if strings.Contains(out, noise) {
			t.Fatalf("disabled Debug leaked %q: %s", noise, out)
		}
	}

	resetTestLogCounters()
	testAndroidLogPrint(4, "rbx", "info-keep")
	testAndroidLogPrint(5, "rbx", "warn-keep")
	testAndroidLogPrint(6, "rbx", "error-keep")
	testAndroidLogAssert("cond", "rbx", "fatal-keep")
	if got := testAndroidLogSkipCount(); got != 0 {
		t.Fatalf("INFO/WARN/ERROR/FATAL skip count=%d want 0", got)
	}
	if got := testLogWriteCalls(); got != 4 {
		t.Fatalf("GoAndroid_LogWrite calls=%d want 4", got)
	}
	out = buf.String()
	for _, want := range []string{"info-keep", "warn-keep", "error-keep", "fatal-keep"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
}

func TestAndroidLogJNIRobloxSettingsDebugStaysInfo(t *testing.T) {
	t.Setenv("TIPSY_LOG", "all")
	t.Setenv("TIPSY_LOG_LEVEL", "info")
	logging.Init()
	buf := captureAndroidSlog(t, slog.LevelInfo)
	resetTestLogCounters()

	testAndroidLogPrint(3, "rbx.JNIRobloxSettings", "setting-debug")
	if got := testAndroidLogSkipCount(); got != 0 {
		t.Fatalf("JNIRobloxSettings DEBUG skip count=%d want 0", got)
	}
	if got := testLogWriteCalls(); got != 1 {
		t.Fatalf("JNIRobloxSettings DEBUG must enter Go, calls=%d", got)
	}
	out := buf.String()
	if !strings.Contains(out, "setting-debug") {
		t.Fatalf("JNIRobloxSettings DEBUG not delivered: %s", out)
	}
	if strings.Contains(out, "level=DEBUG") {
		t.Fatalf("JNIRobloxSettings DEBUG must stay Info: %s", out)
	}
	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("JNIRobloxSettings DEBUG expected Info: %s", out)
	}
}

func TestAndroidLogDebugEnabledStillLogs(t *testing.T) {
	buf := captureAndroidSlog(t, slog.LevelDebug)
	resetTestLogCounters()

	testAndroidLogPrint(3, "rbx", "debug-on")
	if got := testAndroidLogSkipCount(); got != 0 {
		t.Fatalf("enabled Debug skip count=%d want 0", got)
	}
	if got := testLogWriteCalls(); got != 1 {
		t.Fatalf("enabled Debug GoAndroid_LogWrite calls=%d want 1", got)
	}
	out := buf.String()
	if !strings.Contains(out, "debug-on") {
		t.Fatalf("enabled Debug did not log: %s", out)
	}
}

func TestAndroidLogWriteNotifiesObserver(t *testing.T) {
	t.Cleanup(func() { SetLogTextObserver(nil) })
	logging.Init()
	var got string
	SetLogTextObserver(func(text string) { got = text })
	if testAndroidLogPrint(4, "rbx", "Info [FLog::DataModelBindings] onGameLoaded: placeId:1818.") != 1 {
		t.Fatal("print")
	}
	if got != "Info [FLog::DataModelBindings] onGameLoaded: placeId:1818." {
		t.Fatalf("observer=%q", got)
	}
}
