// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/elfinspect"
	"github.com/tipsy-linux/tipsy/internal/integrity"
)

func validClientELFReport() *elfinspect.Report {
	var exports []string
	for _, capability := range requiredClientCapabilities {
		exports = append(exports, capability.exports...)
	}
	return &elfinspect.Report{
		Class: "ELF64", Machine: "EM_X86_64", Type: "ET_DYN",
		JNIEntryPoints: exports,
	}
}

func TestClientCompatibilityUsesArchitectureTypeAndNamedCapabilitiesNotVersion(t *testing.T) {
	valid := validClientELFReport()
	if err := validateClientELFReport(valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*elfinspect.Report)
	}{
		{name: "ELF32", edit: func(report *elfinspect.Report) { report.Class = "ELF32" }},
		{name: "arm64", edit: func(report *elfinspect.Report) { report.Machine = "EM_AARCH64" }},
		{name: "executable", edit: func(report *elfinspect.Report) { report.Type = "ET_EXEC" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *valid
			copy.JNIEntryPoints = slices.Clone(valid.JNIEntryPoints)
			tc.edit(&copy)
			if err := validateClientELFReport(&copy); err == nil {
				t.Fatal("incompatible ELF identity was accepted")
			}
		})
	}
	for _, capability := range requiredClientCapabilities {
		t.Run("missing "+capability.name, func(t *testing.T) {
			copy := *valid
			copy.JNIEntryPoints = slices.Clone(valid.JNIEntryPoints)
			missing := capability.exports[0]
			copy.JNIEntryPoints = slices.DeleteFunc(copy.JNIEntryPoints, func(name string) bool { return name == missing })
			err := validateClientELFReport(&copy)
			if err == nil || !strings.Contains(err.Error(), capability.name) || strings.Contains(err.Error(), missing) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidateExtractedClientCompatibilityReturnsTypedFailure(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "lib", "x86_64")
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libroblox.so"), []byte("not an ELF or package payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ValidateExtractedClientCompatibility(context.Background(), root)
	if ErrorKindOf(err) != ErrCompatibility || !strings.Contains(err.Error(), "readable ELF") {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
}

func TestSnapshotDoesNotTrustInventoryOnlyAndLogsContentFreeFailure(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root := t.TempDir()
	source := filepath.Join(root, "source")
	storeRoot := filepath.Join(root, "store")
	t.Cleanup(func() { makeGenerationTreeWritable(storeRoot) })
	report := writeGenerationSource(t, source, cert)
	id, err := PrepareGeneration(context.Background(), source, storeRoot, report, testTrust(cert))
	if err != nil {
		t.Fatal(err)
	}
	if err := (integrity.Store{Root: storeRoot}).Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	service := New()
	service.GenerationStoreRoot = storeRoot
	service.Trust = testTrust(cert)
	snapshot, err := service.Snapshot(context.Background())
	if err == nil || snapshot.Installed || snapshot.Readiness != ReadinessRejected {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if ErrorKindOf(err) != ErrIntegrity {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	logText := logs.String()
	if !strings.Contains(logText, "installed Roblox client readiness verification failed") ||
		!strings.Contains(logText, "kind=integrity") || strings.Contains(logText, "selected-secret-path") || strings.Contains(logText, source) {
		t.Fatalf("unsafe or incomplete readiness log: %q", logText)
	}
}

func TestSnapshotStatesAreExplicitAndIncompleteVerdictsFailClosed(t *testing.T) {
	service := New()
	service.GenerationStoreRoot = filepath.Join(t.TempDir(), "absent-store")
	absent, err := service.Snapshot(context.Background())
	if err != nil || absent.Installed || absent.Readiness != ReadinessNotInstalled {
		t.Fatalf("absent=%+v err=%v", absent, err)
	}

	service.snapshot = func(context.Context, string, TrustPolicy) (InstallSnapshot, error) {
		return InstallSnapshot{Installed: true}, nil
	}
	incomplete, err := service.Snapshot(context.Background())
	if ErrorKindOf(err) != ErrIntegrity || incomplete.Installed || incomplete.Readiness != ReadinessRejected {
		t.Fatalf("incomplete=%+v err=%v", incomplete, err)
	}
}
