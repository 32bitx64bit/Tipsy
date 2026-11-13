// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

// TestDiagnoseAllProbesRobloxOnce proves the "all" listing (loader and roblox
// share one report) inspects the active generation exactly once.
func TestDiagnoseAllProbesRobloxOnce(t *testing.T) {
	old := inspectActiveImports
	t.Cleanup(func() { inspectActiveImports = old })
	calls := 0
	inspectActiveImports = func(context.Context, string, setupsvc.TrustPolicy) (setupsvc.ActiveClientImportReport, error) {
		calls++
		return setupsvc.ActiveClientImportReport{}, errors.New("no active generation")
	}

	report := DiagnoseForTrust(context.Background(), "all", setupsvc.OfficialTrustPolicy())
	if report.Status != "ok" {
		t.Fatalf("status = %q", report.Status)
	}
	if calls != 1 {
		t.Fatalf("Roblox generation probe ran %d times for one report, want 1", calls)
	}
}

// TestDoctorForTrustCancelledContextSkipsRobloxProbe proves a cancelled
// context stops the probes before they touch the disk.
func TestDoctorForTrustCancelledContextSkipsRobloxProbe(t *testing.T) {
	old := inspectActiveImports
	t.Cleanup(func() { inspectActiveImports = old })
	called := false
	inspectActiveImports = func(context.Context, string, setupsvc.TrustPolicy) (setupsvc.ActiveClientImportReport, error) {
		called = true
		return setupsvc.ActiveClientImportReport{}, errors.New("unexpected probe")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := DoctorForTrust(ctx, setupsvc.OfficialTrustPolicy())
	if report == nil {
		t.Fatal("DoctorForTrust returned nil")
	}
	if called {
		t.Fatal("Roblox generation probe ran on a cancelled context")
	}
	if report.Summary == "" {
		t.Fatal("cancelled report lost its summary")
	}
}

// TestDoctorForTrustCancelledSkipsSetupIssue proves an already-cancelled
// report with a present data dir does not turn the skipped probe into a
// "tipsy setup" issue.
func TestDoctorForTrustCancelledSkipsSetupIssue(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	dataDir := filepath.Join(dataHome, "tipsy")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "runtime"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := DoctorForTrust(ctx, setupsvc.OfficialTrustPolicy())
	if !report.cancelled {
		t.Fatal("cancelled context did not mark the report cancelled")
	}
	if len(report.Issues) != 0 {
		t.Fatalf("cancelled report kept issues: %v", report.Issues)
	}
	if hasSetupIssue(report.Issues) {
		t.Fatalf("cancelled report emitted a setup issue: %v", report.Issues)
	}
	const want = "Diagnostics were cancelled before every check completed."
	if report.Summary != want {
		t.Fatalf("summary = %q, want %q", report.Summary, want)
	}
}

// TestDoctorForTrustMissingDataDirReportsSetupIssue proves an uncancelled
// report still reports the missing Roblox data directory.
func TestDoctorForTrustMissingDataDirReportsSetupIssue(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	report := DoctorForTrust(context.Background(), setupsvc.OfficialTrustPolicy())
	if report.cancelled {
		t.Fatal("uncancelled context marked the report cancelled")
	}
	if !hasSetupIssue(report.Issues) {
		t.Fatalf("missing data dir did not emit a setup issue: %v", report.Issues)
	}
}

func hasSetupIssue(issues []string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, "`tipsy setup`") {
			return true
		}
	}
	return false
}

// TestDiagnoseRobloxStopsOnCancellation proves an in-flight probe unblocks with
// its context instead of running to completion.
func TestDiagnoseRobloxStopsOnCancellation(t *testing.T) {
	old := inspectActiveImports
	t.Cleanup(func() { inspectActiveImports = old })
	started := make(chan struct{})
	inspectActiveImports = func(ctx context.Context, _ string, _ setupsvc.TrustPolicy) (setupsvc.ActiveClientImportReport, error) {
		close(started)
		<-ctx.Done()
		return setupsvc.ActiveClientImportReport{}, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	done := make(chan *SubsystemReport, 1)
	go func() { done <- DiagnoseForTrust(ctx, "roblox", setupsvc.OfficialTrustPolicy()) }()
	select {
	case report := <-done:
		if report.Status != "active" {
			t.Fatalf("status = %q", report.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Roblox probe did not stop after context cancellation")
	}
}
