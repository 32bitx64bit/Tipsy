// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/mic"
)

// Honest JNI leftover this slice: hasSystemFeature(microphone) already
// follows the OpenSL env door (§269), not mic.Allowed() / the config file.
// Doctor must not treat that split as a host failure.
const microphoneFeatureNote = "JNI feature follows the env mic door (not yet mic.Allowed(); config file is CLI-canonical)"

const microphoneDisabledHint = "unset TIPSY_MICROPHONE / TIPSY_DISABLE_MICROPHONE, or enable the microphone section in the config file"
const noCaptureSourceHint = "no capture source found: unmute in pavucontrol/wpctl; Flatpak needs Pulse visible (see packaging/microphone-input.md)"

// captureSourceProbe enumerates Pulse/PipeWire capture sources. Test seam;
// production always calls probeCaptureSourcesLive. Never opens a stream.
var captureSourceProbe = probeCaptureSourcesLive

func attachMicrophone(ctx context.Context, info *AudioInfo) {
	if info == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path := config.Paths().ConfigFile
	data, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		info.MicrophoneNote = "config read error: " + logging.Redact(readErr.Error())
		data = nil
	}
	cfg, parseErr := mic.ParseMicSection(data)
	if parseErr != nil {
		info.MicrophoneNote = "config parse error (corrupt file not applied): " + logging.Redact(parseErr.Error())
		cfg = mic.DefaultMicrophoneConfig()
	}
	lookup := os.LookupEnv
	cfg = cfg.WithEnv(lookup)
	enabled := cfg.Allowed()
	info.MicrophoneEnabled = &enabled
	info.MicrophoneControl = mic.EnabledControl(data, lookup)
	info.MicrophoneFeature = microphoneFeatureNote
	pinned := cfg.SourcePinned()
	info.CaptureSourcePinned = &pinned
	n, probe, ok := captureSourceProbe(ctx)
	info.CaptureProbe = probe
	if ok {
		info.CaptureSources = &n
	}
}

func diagnoseAudio(ctx context.Context) *SubsystemReport {
	a := probeAudio(ctx)
	facts := []string{
		"Client APIs: FMOD AudioTrack playback and OpenSL ES buffer queues",
		"Host bridge: PulseAudio / PipeWire Pulse server",
		"Playback: user-confirmed audible through the host device",
		microphoneDoorFact(a),
		microphoneFeatureNote,
		captureSourceFact(a),
		sourcePinFact(a),
		"Last-capture telemetry: not exported from OpenSL this slice (no Go counters)",
		"PipeWire: " + a.PipeWire,
		"Pulse: " + a.Pulse,
	}
	if a.MicrophoneNote != "" {
		facts = append(facts, "Microphone config: "+a.MicrophoneNote)
	}
	return &SubsystemReport{
		Subsystem: "audio",
		Status:    "active",
		Facts:     facts,
		Message:   audioDiagnoseMessage(a),
	}
}

func microphoneDoorFact(a AudioInfo) string {
	enabled := true
	if a.MicrophoneEnabled != nil {
		enabled = *a.MicrophoneEnabled
	}
	word := "enabled"
	if !enabled {
		word = "disabled"
	}
	control := nz(a.MicrophoneControl, mic.MicrophoneControlDefault)
	return fmt.Sprintf("Microphone: %s (%s)", word, control)
}

func captureSourceFact(a AudioInfo) string {
	probe := nz(a.CaptureProbe, "unavailable")
	if a.CaptureSources == nil {
		return "Capture sources: unavailable (" + probe + "; presence probe only, never opens a stream)"
	}
	return fmt.Sprintf("Capture sources: %d (%s; presence probe only, names not reported)", *a.CaptureSources, probe)
}

func sourcePinFact(a AudioInfo) string {
	if a.CaptureSourcePinned != nil && *a.CaptureSourcePinned {
		return "Source pin: pinned (name not reported)"
	}
	return "Source pin: default"
}

func audioDiagnoseMessage(a AudioInfo) string {
	msg := "Playback through the host PulseAudio/PipeWire bridge is user-confirmed audible. Host capture is implemented and host-verified; in-experience voice still needs an eligible session. This diagnostic does not play sound and never reports PCM. " + microphoneFeatureNote + "."
	if a.MicrophoneEnabled != nil && !*a.MicrophoneEnabled {
		msg += " Microphone door is closed (" + nz(a.MicrophoneControl, mic.MicrophoneControlDefault) + ")."
	}
	if a.CaptureSources != nil && *a.CaptureSources == 0 {
		msg += " " + noCaptureSourceHint + "."
	}
	return msg
}

func collectAudioIssues(r *DoctorReport) []string {
	if r == nil {
		return nil
	}
	var issues []string
	if r.Audio.MicrophoneEnabled != nil && !*r.Audio.MicrophoneEnabled {
		control := nz(r.Audio.MicrophoneControl, mic.MicrophoneControlDefault)
		issues = append(issues, "Microphone input is disabled ("+control+"): "+microphoneDisabledHint)
	}
	if r.Audio.CaptureSources != nil && *r.Audio.CaptureSources == 0 {
		issues = append(issues, noCaptureSourceHint)
	}
	return issues
}

func formatAudioMicLines(a AudioInfo) string {
	if a.MicrophoneEnabled == nil && a.MicrophoneControl == "" && a.MicrophoneFeature == "" &&
		a.CaptureSources == nil && a.CaptureProbe == "" && a.CaptureSourcePinned == nil {
		return ""
	}
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	w("  Microphone: %s", strings.TrimPrefix(microphoneDoorFact(a), "Microphone: "))
	if a.MicrophoneFeature != "" {
		w("  %s", a.MicrophoneFeature)
	}
	w("  %s", captureSourceFact(a))
	w("  %s", sourcePinFact(a))
	if a.MicrophoneNote != "" {
		w("  Note: %s", a.MicrophoneNote)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func probeCaptureSourcesLive(ctx context.Context) (count int, probe string, ok bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if commandExists("pactl") {
		out, err := exec.CommandContext(ctx, "pactl", "list", "sources", "short").Output()
		if err != nil {
			return 0, "pactl", false
		}
		return countPulseSourcesShort(string(out)), "pactl", true
	}
	if commandExists("pw-cli") {
		out, err := exec.CommandContext(ctx, "pw-cli", "ls", "Node").Output()
		if err != nil {
			return 0, "pw-cli", false
		}
		return countPipeWireAudioSources(string(out)), "pw-cli", true
	}
	return 0, "unavailable", false
}

// countPulseSourcesShort counts pactl "list sources short" rows that are not
// sink monitors. Names are parsed only to exclude ".monitor"; they are never
// returned.
func countPulseSourcesShort(out string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.Contains(fields[1], ".monitor") {
			continue
		}
		n++
	}
	return n
}

// countPipeWireAudioSources counts pw-cli Node listings whose media.class is
// Audio/Source. Monitor nodes are skipped when the class or name marks them.
func countPipeWireAudioSources(out string) int {
	n := 0
	class := ""
	monitor := false
	flush := func() {
		if class == "Audio/Source" && !monitor {
			n++
		}
		class = ""
		monitor = false
	}
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "id ") {
			flush()
			continue
		}
		low := strings.ToLower(trim)
		if strings.Contains(low, "media.class") && strings.Contains(trim, "Audio/Source") {
			if strings.Contains(low, "monitor") {
				monitor = true
			}
			class = "Audio/Source"
		}
		if strings.Contains(low, ".monitor") || strings.Contains(low, "stream.monitor") {
			monitor = true
		}
	}
	flush()
	return n
}
