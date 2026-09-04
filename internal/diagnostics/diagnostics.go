// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/version"
)

type SystemInfo struct {
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	Architecture string `json:"architecture"`
}

type DisplayInfo struct {
	Session string `json:"session"`
	DISPLAY string `json:"display"`
	XRandR  string `json:"xrandr"`
	XInput2 string `json:"xinput2"`
}

type GPUInfo struct {
	Vendor string `json:"vendor"`
	Driver string `json:"driver,omitempty"`
	EGL    string `json:"egl"`
	GLES   string `json:"gles"`
	Vulkan string `json:"vulkan,omitempty"`
}

type AudioInfo struct {
	PipeWire string `json:"pipewire"`
	Pulse    string `json:"pulse"`
}

type QtInfo struct {
	Widgets string `json:"widgets"`
	Version string `json:"version,omitempty"`
}

type RobloxInfo struct {
	DataDir        string `json:"dataDir"`
	DataDirPresent bool   `json:"dataDirPresent"`
	RuntimeFiles   string `json:"runtimeFiles"`
	Note           string `json:"note,omitempty"`
}

type RuntimeInfo struct {
	NativeSymbols string `json:"nativeSymbols"`
	JNIMethods    string `json:"jniMethods"`
	Note          string `json:"note,omitempty"`
}

type DoctorReport struct {
	Version string            `json:"version"`
	System  SystemInfo        `json:"system"`
	Display DisplayInfo       `json:"display"`
	GPU     GPUInfo           `json:"gpu"`
	Audio   AudioInfo         `json:"audio"`
	Qt      QtInfo            `json:"qt"`
	Roblox  RobloxInfo        `json:"roblox"`
	Runtime RuntimeInfo       `json:"runtime"`
	Paths   config.Layout     `json:"paths"`
	Issues  []string          `json:"issues"`
	Summary string            `json:"summary"`
	Env     map[string]string `json:"env,omitempty"`
}

type SubsystemReport struct {
	Subsystem string   `json:"subsystem"`
	Status    string   `json:"status"`
	Milestone string   `json:"milestone,omitempty"`
	Facts     []string `json:"facts,omitempty"`
	Message   string   `json:"message"`
	Error     string   `json:"error,omitempty"`
}

var diagnoseSubsystems = []string{"x11", "graphics", "audio", "jni", "loader", "roblox", "auth"}

func Doctor(ctx context.Context) *DoctorReport {
	if ctx == nil {
		ctx = context.Background()
	}
	r := &DoctorReport{
		Version: version.String(),
		Paths:   config.Paths(),
	}
	r.System = probeSystem()
	r.Display = probeDisplay()
	r.GPU = probeGPU(ctx)
	r.Audio = probeAudio()
	r.Qt = probeQt(ctx)
	r.Roblox = probeRoblox()
	r.Runtime = RuntimeInfo{
		NativeSymbols: "not implemented yet (milestone 5)",
		JNIMethods:    "not implemented yet (milestone 6)",
		Note:          "Runtime loader is not started (milestones 4+).",
	}
	r.Issues = collectIssues(r)
	switch n := len(r.Issues); {
	case n == 0:
		r.Summary = "No host issues found. Runtime is not implemented yet (milestones 4+)."
	case n == 1:
		r.Summary = "1 issue found."
	default:
		r.Summary = strconv.Itoa(n) + " issues found."
	}
	r.Env = reportEnv()
	return r
}

func reportEnv() map[string]string {
	keys := []string{
		"DISPLAY", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP",
		"WAYLAND_DISPLAY", "TIPSY_LOG", "TIPSY_LOG_LEVEL",
	}
	out := make(map[string]string)
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = logging.Redact(v)
		}
	}
	return out
}

func Diagnose(ctx context.Context, subsystem string) *SubsystemReport {
	if ctx == nil {
		ctx = context.Background()
	}
	sub := strings.ToLower(strings.TrimSpace(subsystem))
	switch sub {
	case "", "all":
		return &SubsystemReport{
			Subsystem: "all",
			Status:    "ok",
			Facts:     diagnoseAllFacts(ctx),
			Message:   "Specify a subsystem: " + strings.Join(diagnoseSubsystems, ", "),
		}
	case "x11":
		d := probeDisplay()
		return &SubsystemReport{
			Subsystem: "x11",
			Status:    "not implemented yet (milestone 9)",
			Milestone: "9",
			Facts: []string{
				"Session: " + d.Session,
				"DISPLAY: " + d.DISPLAY,
				"XRandR: " + d.XRandR,
				"XInput2: " + d.XInput2,
				"XDG_SESSION_TYPE=" + getenv("XDG_SESSION_TYPE", "unset"),
			},
			Message: "Native X11 windowing is not implemented yet (milestone 9).",
		}
	case "graphics":
		g := probeGPU(ctx)
		return &SubsystemReport{
			Subsystem: "graphics",
			Status:    "not implemented yet (milestone 10)",
			Milestone: "10",
			Facts: []string{
				"Vendor: " + g.Vendor,
				"Driver: " + g.Driver,
				"EGL: " + g.EGL,
				"OpenGL ES: " + g.GLES,
				"Vulkan: " + g.Vulkan,
			},
			Message: "EGL/GLES on X11 is not implemented yet (milestone 10).",
		}
	case "audio":
		a := probeAudio()
		mic := "opens on demand when Roblox starts recording"
		if disabled, _ := strconv.ParseBool(os.Getenv("TIPSY_DISABLE_MICROPHONE")); disabled {
			mic = "disabled by TIPSY_DISABLE_MICROPHONE"
		}
		return &SubsystemReport{
			Subsystem: "audio",
			Status:    "ready",
			Milestone: "15",
			Facts: []string{
				"Client API: OpenSL ES buffer queues",
				"Host bridge: PulseAudio / PipeWire Pulse server",
				"Playback: asynchronous worker with reconnect",
				"Microphone: " + mic,
				"PipeWire: " + a.PipeWire,
				"Pulse: " + a.Pulse,
			},
			Message: "OpenSL ES playback and capture are bridged to the host. Device access is verified when Roblox starts a stream.",
		}
	case "jni":
		return &SubsystemReport{
			Subsystem: "jni",
			Status:    "not implemented yet (milestone 6)",
			Milestone: "6",
			Facts: []string{
				"Architecture: " + goArch(),
			},
			Message: "JavaVM/JNIEnv is not implemented yet (milestone 6).",
		}
	case "loader":
		return &SubsystemReport{
			Subsystem: "loader",
			Status:    "not implemented yet (milestone 4)",
			Milestone: "4",
			Facts: []string{
				"Architecture: " + goArch(),
				"OS: " + probeSystem().OS,
			},
			Message: "Android native loader is not implemented yet (milestone 4).",
		}
	case "roblox":
		rb := probeRoblox()
		return &SubsystemReport{
			Subsystem: "roblox",
			Status:    "not implemented yet (milestone 11)",
			Milestone: "11",
			Facts: []string{
				"Data dir: " + rb.DataDir,
				"Present: " + boolString(rb.DataDirPresent),
				"Runtime files: " + rb.RuntimeFiles,
			},
			Message: "Roblox launch is not implemented yet (milestone 11). Use `tipsy inspect` for packages (milestones 1–3).",
		}
	case "auth":
		return &SubsystemReport{
			Subsystem: "auth",
			Status:    "not implemented yet (milestone 13)",
			Milestone: "13",
			Facts: []string{
				"Official login only; secrets are never logged.",
				"Data dir: " + config.Paths().DataDir,
			},
			Message: "Authentication persistence is not implemented yet (milestones 13–14).",
		}
	default:
		return &SubsystemReport{
			Subsystem: sub,
			Status:    "unknown",
			Error:     "unknown subsystem",
			Message:   "Unknown subsystem " + sub + ". Choose: " + strings.Join(diagnoseSubsystems, ", "),
		}
	}
}

func collectIssues(r *DoctorReport) []string {
	var issues []string
	if r.System.Architecture != "x86_64" && r.System.Architecture != "amd64" {
		issues = append(issues, "Architecture is "+r.System.Architecture+" (Tipsy requires x86_64)")
	}
	if r.Display.DISPLAY == "unset" || r.Display.DISPLAY == "" {
		issues = append(issues, "DISPLAY is unset")
	}
	sess := strings.ToLower(r.Display.Session)
	if sess != "" && sess != "x11" && sess != "unknown" && !strings.Contains(sess, "x11") {
		issues = append(issues, "Session is "+r.Display.Session+" (X11 is the primary Tipsy target)")
	}
	if strings.EqualFold(r.GPU.EGL, "not found") {
		issues = append(issues, "EGL libraries not detected")
	}
	if !r.Roblox.DataDirPresent {
		issues = append(issues, "Roblox data directory is empty or missing (setup is not implemented; milestone 17)")
	}
	issues = append(issues, "Runtime not implemented yet (milestones 4+)")
	return issues
}

func diagnoseAllFacts(ctx context.Context) []string {
	var facts []string
	for _, s := range diagnoseSubsystems {
		rep := Diagnose(ctx, s)
		facts = append(facts, s+": "+rep.Status)
	}
	return facts
}

func goArch() string {
	if runtime.GOARCH == "amd64" {
		return "x86_64"
	}
	return runtime.GOARCH
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
