// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/compat"
	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/elfinspect"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func cmdSetup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, development, err := parseSetupFlags(args)
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
	cfg, err := loadConfigWithDevelopmentConsent(development)
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	authority, err := resolveAuthority(ctx, cfg, defaultAuthorityDependencies())
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	if authority.Mode == setupsvc.DevelopmentUnrestricted {
		printDevelopmentWarning(stderr)
	}
	service := setupsvc.New()
	service.Trust = authority.Trust
	res, err := service.Install(ctx, setupsvc.InstallRequest{Mode: setupsvc.InstallLocal, LocalPaths: f.rest}, nil)
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	snapshot := res.Snapshot
	fmt.Fprintf(stdout, "Extracted to %s\n", snapshot.RuntimeDir)
	if snapshot.PackageName != "" {
		fmt.Fprintf(stdout, "Package: %s %s (%d)\n", snapshot.PackageName, snapshot.VersionName, snapshot.VersionCode)
	}
	fmt.Fprintln(stdout, "Architecture: x86_64")
	return 0
}

func cmdLaunch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return cmdLaunchWithDependencies(ctx, args, stdout, stderr, defaultAuthorityDependencies(), defaultGenerationDependencies())
}

func cmdLaunchWithDependencies(ctx context.Context, args []string, stdout, stderr io.Writer, authorityDeps authorityDependencies, generationDeps generationDependencies) int {
	probe := false
	development := false
	uri := ""
	rest := args
	for len(rest) > 0 {
		switch {
		case rest[0] == "-h" || rest[0] == "--help":
			fmt.Fprint(stdout, launchHelp)
			return 0
		case rest[0] == "--probe":
			probe = true
			rest = rest[1:]
		case rest[0] == "--development":
			development = true
			rest = rest[1:]
		case rest[0] == "--":
			rest = rest[1:]
			if len(rest) == 0 {
				fmt.Fprintln(stderr, "launch: missing URI after --")
				fmt.Fprint(stderr, launchHelp)
				return 2
			}
			uri = rest[0]
			rest = rest[1:]
		case rbxuri.LooksLike(rest[0]):
			uri = rest[0]
			rest = rest[1:]
		default:
			fmt.Fprintf(stderr, "launch: unexpected argument %q\n", rest[0])
			fmt.Fprint(stderr, launchHelp)
			return 2
		}
	}
	if probe && uri != "" {
		fmt.Fprintln(stderr, "launch: --probe cannot take a website URI")
		return 2
	}
	req, err := rbxuri.Parse(uri)
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 2
	}
	session, err := openAuthorizedLaunchSession(ctx, development, authorityDeps, generationDeps)
	if err != nil {
		var sessionErr *LaunchSessionError
		if errors.As(err, &sessionErr) && sessionErr.Authority.Warning != "" {
			fmt.Fprintln(stderr, sessionErr.Authority.Warning)
		}
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	defer session.Close()
	if warning := session.State().Warning; warning != "" {
		fmt.Fprintln(stderr, warning)
	}
	err = session.Launch(ctx, runtime.LaunchOptions{Probe: probe, Request: req})
	if err != nil {
		fmt.Fprintln(stderr, logging.Redact(err.Error()))
		return 1
	}
	if probe {
		fmt.Fprintln(stdout, "probe: libroblox.so loaded, JNI_OnLoad returned")
	}
	return 0
}

func parseSetupFlags(args []string) (flags, bool, error) {
	var ordinary []string
	development := false
	for _, arg := range args {
		if arg == "--development" {
			development = true
			continue
		}
		ordinary = append(ordinary, arg)
	}
	f, err := parseFlags(ordinary)
	return f, development, err
}

func loadConfigWithDevelopmentConsent(requested bool) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if requested && !cfg.DevelopmentApproved() {
		cfg.ApproveDevelopment()
		if err := config.Save(cfg); err != nil {
			return nil, fmt.Errorf("record explicit development authorization: %w", err)
		}
	}
	return cfg, nil
}

func printDevelopmentWarning(w io.Writer) {
	fmt.Fprintln(w, DevelopmentUnrestrictedWarning)
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
