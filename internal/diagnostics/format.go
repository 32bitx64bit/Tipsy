// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

func FormatDoctor(r *DoctorReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	w("Tipsy Doctor (%s)", nz(r.Version, "unknown"))
	w("")
	w("System")
	w("  OS: %s", nz(r.System.OS, "unknown"))
	w("  Kernel: %s", nz(r.System.Kernel, "unknown"))
	w("  Architecture: %s", nz(r.System.Architecture, "unknown"))
	w("")
	w("Display")
	w("  Session: %s", nz(r.Display.Session, "unknown"))
	w("  DISPLAY: %s", nz(r.Display.DISPLAY, "unset"))
	w("  XRandR: %s", nz(r.Display.XRandR, "unknown"))
	w("  XInput2: %s", nz(r.Display.XInput2, "unknown"))
	w("")
	w("GPU")
	w("  Vendor: %s", nz(r.GPU.Vendor, "unknown"))
	w("  Driver: %s", nz(r.GPU.Driver, "unknown"))
	w("  EGL: %s", nz(r.GPU.EGL, "unknown"))
	w("  OpenGL ES: %s", nz(r.GPU.GLES, "unknown"))
	w("  Vulkan: %s", nz(r.GPU.Vulkan, "unknown"))
	w("")
	w("Audio")
	w("  PipeWire: %s", nz(r.Audio.PipeWire, "unknown"))
	w("  Pulse: %s", nz(r.Audio.Pulse, "unknown"))
	w("")
	w("Qt")
	w("  Widgets: %s", nz(r.Qt.Widgets, "unknown"))
	w("")
	w("Roblox")
	w("  Data dir: %s", nz(r.Roblox.DataDir, "unknown"))
	w("  Runtime files: %s", nz(r.Roblox.RuntimeFiles, "unknown"))
	if r.Roblox.Note != "" {
		w("  Note: %s", r.Roblox.Note)
	}
	w("")
	w("Runtime")
	w("  Native symbols: %s", nz(r.Runtime.NativeSymbols, "unknown"))
	w("  JNI methods: %s", nz(r.Runtime.JNIMethods, "unknown"))
	if r.Runtime.Note != "" {
		w("  Note: %s", r.Runtime.Note)
	}
	if len(r.Env) > 0 {
		w("")
		w("Environment")
		keys := make([]string, 0, len(r.Env))
		for k := range r.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w("  %s: %s", k, r.Env[k])
		}
	}
	if r.Paths.ConfigDir != "" {
		w("")
		w("Paths")
		w("  Config: %s", r.Paths.ConfigDir)
		w("  Data: %s", r.Paths.DataDir)
		w("  Cache: %s", r.Paths.CacheDir)
		w("  State: %s", r.Paths.StateDir)
		w("  Logs: %s", r.Paths.LogDir)
	}
	w("")
	w("Result")
	if len(r.Issues) == 0 {
		w("  %s", nz(r.Summary, "ok"))
	} else {
		w("  %s", nz(r.Summary, fmt.Sprintf("%d issues found.", len(r.Issues))))
		for _, issue := range r.Issues {
			w("  - %s", issue)
		}
	}
	return logging.Redact(b.String())
}

func FormatDoctorJSON(r *DoctorReport) ([]byte, error) {
	if r == nil {
		return []byte("null\n"), nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	return []byte(logging.Redact(string(b))), nil
}

func FormatSubsystem(r *SubsystemReport) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	w("Tipsy diagnose %s", nz(r.Subsystem, "unknown"))
	w("  Status: %s", nz(r.Status, "unknown"))
	if r.Milestone != "" {
		w("  Milestone: %s", r.Milestone)
	}
	if r.Message != "" {
		w("  %s", r.Message)
	}
	for _, f := range r.Facts {
		w("  - %s", f)
	}
	if r.Error != "" {
		w("  Error: %s", r.Error)
	}
	return logging.Redact(b.String())
}

func FormatSubsystemJSON(r *SubsystemReport) ([]byte, error) {
	if r == nil {
		return []byte("null\n"), nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	return []byte(logging.Redact(string(b))), nil
}

func nz(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
