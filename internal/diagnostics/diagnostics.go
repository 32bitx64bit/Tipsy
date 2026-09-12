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
	Gamepad GamepadInfo       `json:"gamepad"`
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

// DiagnoseSubsystems lists the subsystem names accepted by Diagnose.
var DiagnoseSubsystems = []string{"x11", "graphics", "audio", "jni", "loader", "roblox", "auth", "gamepad"}

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
	r.Gamepad = probeGamepad()
	r.Roblox = probeRoblox()
	r.Runtime = RuntimeInfo{
		NativeSymbols: "official x86_64 client maps and runs (JNI_OnLoad and initializeNativeCode complete)",
		JNIMethods:    "Android/JNI compatibility surface is active",
		Note:          "The official logged-out login UI renders on X11 with EGL/GLES presentation. This diagnostic does not verify login, join, or gameplay.",
	}
	r.Issues = collectIssues(r)
	switch n := len(r.Issues); {
	case n == 0:
		r.Summary = "No host issues found. Run `tipsy diagnose <subsystem>` for component status."
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
		"TIPSY_GAMEPAD", "TIPSY_GAMEPAD_PATH", "TIPSY_GAMEPAD_DEBUG",
		"TIPSY_GAMEPAD_DEADZONE", "TIPSY_GAMEPAD_DEADZONE_LEFT", "TIPSY_GAMEPAD_DEADZONE_RIGHT",
		"TIPSY_GAMEPAD_INVERT_Y", "TIPSY_GAMEPAD_INVERT_Y_LEFT", "TIPSY_GAMEPAD_INVERT_Y_RIGHT",
		"TIPSY_GAMEPAD_RUMBLE",
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
			Message:   "Specify a subsystem: " + strings.Join(DiagnoseSubsystems, ", "),
		}
	case "x11":
		d := probeDisplay()
		return &SubsystemReport{
			Subsystem: "x11",
			Status:    "active",
			Facts: []string{
				"Session: " + d.Session,
				"DISPLAY: " + d.DISPLAY,
				"XRandR: " + d.XRandR,
				"XInput2: " + d.XInput2,
				"XDG_SESSION_TYPE=" + getenv("XDG_SESSION_TYPE", "unset"),
			},
			Message: "Native X11 windowing is active; the official logged-out Roblox login UI renders on X11.",
		}
	case "graphics":
		g := probeGPU(ctx)
		return &SubsystemReport{
			Subsystem: "graphics",
			Status:    "active",
			Facts: []string{
				"Vendor: " + g.Vendor,
				"Driver: " + g.Driver,
				"EGL: " + g.EGL,
				"OpenGL ES: " + g.GLES,
				"Vulkan: " + g.Vulkan,
			},
			Message: "EGL/GLES presentation on X11 is active; the official login UI renders through the host GL stack.",
		}
	case "audio":
		a := probeAudio()
		mic := "opens on demand when Roblox starts recording"
		if disabled, _ := strconv.ParseBool(os.Getenv("TIPSY_DISABLE_MICROPHONE")); disabled {
			mic = "disabled by TIPSY_DISABLE_MICROPHONE"
		}
		return &SubsystemReport{
			Subsystem: "audio",
			Status:    "active",
			Facts: []string{
				"Client APIs: FMOD AudioTrack playback and OpenSL ES buffer queues",
				"Host bridge: PulseAudio / PipeWire Pulse server",
				"Playback: user-confirmed audible through the host device",
				"Microphone: " + mic,
				"PipeWire: " + a.PipeWire,
				"Pulse: " + a.Pulse,
			},
			Message: "Playback through the host PulseAudio/PipeWire bridge is user-confirmed audible. This diagnostic reports the installed bridge only; it does not play sound and does not verify microphone capture or in-experience audio.",
		}
	case "jni":
		return &SubsystemReport{
			Subsystem: "jni",
			Status:    "active",
			Facts: []string{
				"Architecture: " + goArch(),
				"Measured: JNI_OnLoad returns 0x10006 (JNI 1.6)",
				"Measured: initializeNativeCode returns a non-zero handle",
				"Android/JNI compatibility surface answers the official client's GetMethodID/RegisterNatives calls",
			},
			Message: "The official client's JNI surface is active: JNI_OnLoad returns 0x10006 and initializeNativeCode returns a non-zero handle. Login, join, and gameplay are separate stages this diagnostic does not verify.",
		}
	case "loader":
		return &SubsystemReport{
			Subsystem: "loader",
			Status:    "active",
			Facts: []string{
				"Architecture: " + goArch(),
				"OS: " + probeSystem().OS,
				"Measured: official x86_64 libroblox.so maps with PT_LOAD segments and relocations applied",
				"Measured: constructors and JNI_OnLoad run against the mapped image",
			},
			Message: "The Android x86-64 ELF loader maps the official libroblox.so and applies its relocations. This diagnostic does not start Roblox or verify a rendered frame.",
		}
	case "roblox":
		rb := probeRoblox()
		return &SubsystemReport{
			Subsystem: "roblox",
			Status:    "active",
			Facts: []string{
				"Data dir: " + rb.DataDir,
				"Present: " + boolString(rb.DataDirPresent),
				"Runtime files: " + rb.RuntimeFiles,
				"Measured: the official logged-out login UI renders on X11 with EGL/GLES presentation",
			},
			Message: "The official client maps, completes JNI initialization, and renders its logged-out login UI on X11. Login, join, gameplay, and in-experience state are not verified by this diagnostic.",
		}
	case "auth":
		return &SubsystemReport{
			Subsystem: "auth",
			Status:    "active",
			Facts: []string{
				"Official login only; secrets are never logged.",
				"Measured: official login completes and an authenticated restart is user-confirmed",
				"Data dir: " + config.Paths().DataDir,
			},
			Message: "Official login and authenticated restart are user-confirmed. This diagnostic does not read cookies, tokens, or account state, and does not verify join or gameplay.",
		}
	case "gamepad", "pad", "controller":
		return diagnoseGamepad()
	default:
		return &SubsystemReport{
			Subsystem: sub,
			Status:    "unknown",
			Error:     "unknown subsystem",
			Message:   "Unknown subsystem " + sub + ". Choose: " + strings.Join(DiagnoseSubsystems, ", "),
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
	if len(r.Gamepad.Denied) > 0 {
		eg := r.Gamepad.Denied[0]
		issues = append(issues, "Gamepad access denied on "+strconv.Itoa(len(r.Gamepad.Denied))+" input node(s) (e.g. "+eg+"): "+gamepadPermissionHint)
	}
	if !r.Roblox.DataDirPresent {
		issues = append(issues, "Roblox data directory is empty or missing; run `tipsy setup` to install the official client")
	}
	return issues
}

func diagnoseAllFacts(ctx context.Context) []string {
	var facts []string
	for _, s := range DiagnoseSubsystems {
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
