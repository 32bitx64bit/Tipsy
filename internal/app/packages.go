// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"io"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/compat"
	"github.com/tipsy-linux/tipsy/internal/elfinspect"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/runtime"
)

func cmdSetup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, setupHelp)
		return 0
	}
	if len(f.rest) == 0 {
		fmt.Fprintln(stderr, "pass official APK/dir")
		fmt.Fprint(stderr, setupHelp)
		return 2
	}
	res, err := runtime.Setup(ctx, f.rest)
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	fmt.Fprintf(stdout, "Extracted to %s\n", res.DestDir)
	if res.Report != nil && res.Report.Merged != nil {
		m := res.Report.Merged
		if m.PackageName != "" {
			fmt.Fprintf(stdout, "Package: %s %s (%d)\n", m.PackageName, m.VersionName, m.VersionCode)
		}
	}
	fmt.Fprintf(stdout, "Libraries: %d\n", len(res.Libraries))
	return 0
}

func cmdLaunch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	probe := false
	rest := args
	for len(rest) > 0 {
		switch rest[0] {
		case "-h", "--help":
			fmt.Fprint(stdout, launchHelp)
			return 0
		case "--probe":
			probe = true
			rest = rest[1:]
		default:
			fmt.Fprintf(stderr, "launch: unexpected argument %q\n", rest[0])
			fmt.Fprint(stderr, launchHelp)
			return 2
		}
	}
	err := runtime.Launch(ctx, runtime.LaunchOptions{Probe: probe})
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	if probe {
		fmt.Fprintln(stdout, "probe: libroblox.so loaded, JNI_OnLoad returned")
	}
	return 0
}

func cmdInspect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, inspectHelp)
		return 0
	}
	if len(f.rest) == 0 {
		fmt.Fprintln(stderr, "inspect: at least one APK, directory, or package archive is required")
		fmt.Fprint(stderr, inspectHelp)
		return 2
	}
	rep, err := apk.Inspect(ctx, f.rest)
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	if f.json {
		b, err := apk.FormatJSON(rep)
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		writeJSON(stdout, b)
		return 0
	}
	fmt.Fprint(stdout, apk.FormatText(rep))
	return 0
}

func cmdDiagnoseNative(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	_ = ctx
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, diagnoseNativeHelp)
		return 0
	}
	if len(f.rest) != 1 {
		fmt.Fprintln(stderr, "diagnose-native: exactly one .so path is required")
		fmt.Fprint(stderr, diagnoseNativeHelp)
		return 2
	}
	rep, err := elfinspect.Analyze(f.rest[0])
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	if f.json {
		b, err := elfinspect.FormatJSON(rep)
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		writeJSON(stdout, b)
		return 0
	}
	fmt.Fprint(stdout, elfinspect.FormatText(rep))
	return 0
}

func cmdCompareRoblox(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, compareHelp)
		return 0
	}
	if len(f.rest) != 2 {
		fmt.Fprintln(stderr, "compare-roblox: two package paths are required (old, new)")
		fmt.Fprint(stderr, compareHelp)
		return 2
	}
	oldRep, err := apk.Inspect(ctx, []string{f.rest[0]})
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	newRep, err := apk.Inspect(ctx, []string{f.rest[1]})
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	diff := apk.Compare(oldRep, newRep)
	if f.json {
		b, err := apk.FormatDiffJSON(diff)
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		writeJSON(stdout, b)
		return 0
	}
	fmt.Fprint(stdout, apk.FormatDiff(diff))
	return 0
}

func cmdReport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, reportHelp)
		return 0
	}
	if len(f.rest) == 0 {
		fmt.Fprintln(stderr, "report: at least one APK, directory, or package archive is required")
		fmt.Fprint(stderr, reportHelp)
		return 2
	}
	rep, err := compat.Build(ctx, f.rest)
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	if f.json {
		b, err := compat.FormatJSON(rep)
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		writeJSON(stdout, b)
		return 0
	}
	fmt.Fprint(stdout, compat.FormatText(rep))
	return 0
}
