// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package graphics

/*
#include "vulkan_latency_probe.h"
*/
import "C"

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AntiLagProbeCommand is the argument the tipsy binary uses to run the isolated
// driver probe. The helper exits 0 when an eligible RADV device exposes
// VK_AMD_anti_lag, 1 otherwise, and prints a small key=value report.
const AntiLagProbeCommand = "antilag-probe"

// AntiLagProbeTimeout bounds the helper so a wedged loader cannot stall
// startup. Two instance creations plus an extension enumeration are cheap.
const AntiLagProbeTimeout = 15 * time.Second

var antiLagProbeOnce sync.Once
var antiLagProbeResult AntiLagSupport

// RunAntiLagProbeMain runs the in-process driver probe and returns the process
// exit code. It is the helper entry point for AntiLagProbeCommand.
func RunAntiLagProbeMain() int {
	return int(C.tipsy_vulkan_antilag_probe())
}

// ProbeAntiLagSupport runs the VK_AMD_anti_lag probe in an isolated helper
// process with the user's environment verbatim. A missing or failing helper is
// reported as unavailable, never as a guess.
func ProbeAntiLagSupport() AntiLagSupport {
	return ProbeAntiLagSupportContext(context.Background())
}

// ProbeAntiLagSupportContext is ProbeAntiLagSupport for a caller that already
// has a deadline for settings load.
func ProbeAntiLagSupportContext(ctx context.Context) AntiLagSupport {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return AntiLagSupport{Reason: "probe deadline already passed"}
	}
	antiLagProbeOnce.Do(func() { antiLagProbeResult = runAntiLagProbeProcessContext(ctx) })
	return antiLagProbeResult
}

func runAntiLagProbeProcess() AntiLagSupport {
	return runAntiLagProbeProcessContext(context.Background())
}

func runAntiLagProbeProcessContext(ctx context.Context) AntiLagSupport {
	exe, err := os.Executable()
	if err != nil {
		return AntiLagSupport{Reason: "helper executable could not be located: " + err.Error()}
	}
	timeout := AntiLagProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return AntiLagSupport{Reason: "probe deadline already passed"}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, AntiLagProbeCommand)
	// The user's environment verbatim: a Mesa anti-lag layer must be seen
	// exactly as the real client would see it.
	cmd.Env = os.Environ()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	err = cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return AntiLagSupport{Reason: "probe timed out"}
	}
	return ParseAntiLagProbeReport(stdout.String(), err)
}

// ParseAntiLagProbeReport turns the helper's key=value report into support.
// runErr is the helper exit status; child and run are both used so callers
// with a captured report and manual status agree on the same policy.
func ParseAntiLagProbeReport(report string, runErr error) AntiLagSupport {
	fields := make(map[string]string, 6)
	for _, line := range strings.Split(report, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	support := AntiLagSupport{
		Driver: fields["driver"],
		Reason: fields["reason"],
	}
	available := fields["available"] == "1" && runErr == nil
	needsLayer := fields["needs_layer_env"] == "1"
	vendor, _ := strconv.ParseUint(strings.TrimPrefix(fields["vendor"], "0x"), 16, 32)
	if !available {
		if support.Reason == "" {
			support.Reason = probeFailureReason(runErr)
		}
		return support
	}
	// The C helper already rejects everything except RADV on AMD; re-check the
	// report here so a truncated or hand-edited helper cannot enable anti-lag.
	if !strings.EqualFold(support.Driver, "") && !radvDriverName(support.Driver) {
		support.Reason = "driver is not VK_DRIVER_ID_MESA_RADV"
		return support
	}
	if vendor != 0x1002 {
		support.Reason = "vendor is not AMD"
		return support
	}
	if fields["listed"] != "1" {
		support.Reason = "VK_AMD_anti_lag is not exposed"
		return support
	}
	support.Available = true
	support.NeedsLayerEnv = needsLayer
	return support
}

func probeFailureReason(err error) string {
	if err == nil {
		return "probe reported no eligible device"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "probe found no RADV device with VK_AMD_anti_lag"
	}
	return "probe failed: " + err.Error()
}

// radvDriverName recognises the RADV device name prefix the helper reports on
// drivers that predate VkPhysicalDeviceProperties.driverID.
func radvDriverName(name string) bool {
	return strings.HasPrefix(name, "RADV") || strings.Contains(name, "RADV ")
}
