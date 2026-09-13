// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Gamepad permission guidance. Content-free: never carries input values.
const gamepadPermissionHint = "add user to input group and relogin; check udev rule / logind ACL; Flatpak needs --device=all"

// GamepadPad is one accessible pad: name/vendor/product/capabilities only.
// Field shapes are kept for the doctor formatter and GUI card; Detail is
// now usually empty (mapping verbosity deleted, see below).
type GamepadPad struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor"`
	Product string `json:"product"`
	Mapping string `json:"mapping"`
	Caps    string `json:"caps"`
	Detail  string `json:"detail,omitempty"`
}

// GamepadInfo is the honest pad enumeration for diagnose/doctor.
type GamepadInfo struct {
	Enabled        bool              `json:"enabled"`
	PathSelector   string            `json:"pathSelector"`
	PadCount       int               `json:"padCount"`
	Pads           []GamepadPad      `json:"pads,omitempty"`
	Denied         []string          `json:"denied,omitempty"`
	PermissionHint string            `json:"permissionHint,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Note           string            `json:"note,omitempty"`
}

// gamepadScanFunc enumerates evdev nodes. Test seam; production always calls
// gamepad.Scan. It never synthesizes a device.
var gamepadScanFunc = gamepad.Scan

func gamepadDir() string { return gamepad.InputNodeDir }

// gamepadEnabledLive mirrors the JNI kill-switch without caching.
func gamepadEnabledLive() (bool, string) {
	raw := os.Getenv("TIPSY_GAMEPAD")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "off", "false", "no":
		return false, raw
	default:
		return true, raw
	}
}

// gamepadPathLive reports the effective feed-in arm. Pads are direct-only in
// the lean build: any TIPSY_GAMEPAD_PATH value is parsed-but-ignored and the
// effective arm is always direct.
func gamepadPathLive() (string, string) {
	return "direct", os.Getenv("TIPSY_GAMEPAD_PATH")
}

// gamepadEnvKeys lists the lean TIPSY_GAMEPAD* variables the diagnose facts
// report. Removed v1 keys (per-stick deadzone/invert, rumble) are ignored
// everywhere and no longer reported.
var gamepadEnvKeys = []string{
	"TIPSY_GAMEPAD", "TIPSY_GAMEPAD_PATH", "TIPSY_GAMEPAD_DEBUG",
	"TIPSY_GAMEPAD_DEADZONE",
}

// gamepadEnv reports the TIPSY_GAMEPAD* environment, redacted. Only set
// variables are included.
func gamepadEnv() map[string]string {
	out := make(map[string]string)
	for _, k := range gamepadEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = logging.Redact(v)
		}
	}
	return out
}

// effectiveGamepadConfig resolves the live calibration the JNI pump uses.
func effectiveGamepadConfig() gamepad.GamepadConfig {
	cfg, err := gamepad.LoadGamepadConfigFile(config.Paths().ConfigFile)
	if err != nil {
		cfg = gamepad.DefaultGamepadConfig()
	}
	return cfg.WithEnv(os.LookupEnv)
}

// probeGamepad enumerates accessible pads without grabbing anything.
func probeGamepad() GamepadInfo {
	enabled, _ := gamepadEnabledLive()
	pathSel, _ := gamepadPathLive()
	info := GamepadInfo{
		Enabled:      enabled,
		PathSelector: pathSel,
		Env:          gamepadEnv(),
	}
	if !enabled {
		info.Note = "disabled by TIPSY_GAMEPAD=0|off kill-switch; no pads are opened"
		return info
	}
	res, err := gamepadScanFunc(gamepadDir())
	if err != nil {
		info.Note = "scan error: " + logging.Redact(err.Error())
		return info
	}
	info.Denied = append([]string(nil), res.Denied...)
	sort.Strings(info.Denied)
	for _, p := range res.Pads {
		info.Pads = append(info.Pads, describeGamepadPad(p))
	}
	info.PadCount = len(info.Pads)
	if len(info.Denied) > 0 {
		info.PermissionHint = gamepadPermissionHint
	}
	switch {
	case len(info.Pads) == 0 && len(info.Denied) > 0:
		info.Note = fmt.Sprintf("no accessible gamepad: permission denied on %d node(s) (%s); zero pads is the honest state, never a fake pad",
			len(info.Denied), info.Denied[0])
	case len(info.Pads) == 0:
		info.Note = "no gamepad found (zero devices is the honest state); the engine sees zero pads"
	default:
		info.Note = fmt.Sprintf("%d gamepad(s) accessible; names/caps below are content-free (no input values, secrets never logged)", len(info.Pads))
	}
	return info
}

// gamepadDiagnoseStatus is the honest subsystem state: disabled under the
// kill-switch, degraded on EACCES, active otherwise (pads or honest empty).
func gamepadDiagnoseStatus(info GamepadInfo) string {
	switch {
	case !info.Enabled:
		return "disabled"
	case info.PadCount == 0 && len(info.Denied) > 0:
		return "degraded"
	default:
		return "active"
	}
}

// diagnoseGamepad builds the `tipsy diagnose gamepad` report. Aliases pad
// and controller canonicalize here. One caps line per pad; per-pad
// mapping-verbosity and per-axis dumps are deleted vs v1.
func diagnoseGamepad() *SubsystemReport {
	info := probeGamepad()
	status := gamepadDiagnoseStatus(info)
	var facts []string
	facts = append(facts, gamepadEnvFacts(info)...)
	for i, p := range info.Pads {
		facts = append(facts,
			fmt.Sprintf("pad%d: %s %q vendor=%s product=%s mapping=%s caps=%s", i, p.Path, p.Name, p.Vendor, p.Product, p.Mapping, p.Caps))
	}
	if len(info.Denied) > 0 {
		eg := strings.Join(info.Denied, ", ")
		if len(info.Denied) > 3 {
			eg = strings.Join(info.Denied[:3], ", ") + ", ..."
		}
		facts = append(facts, fmt.Sprintf("denied: %d node(s) EACCES (%s): %s", len(info.Denied), eg, info.PermissionHint))
	}
	facts = append(facts,
		"Deadzone: honest per-axis flat from EVIOCGABS (small 0.08 fallback when flat=0) plus one global TIPSY_GAMEPAD_DEADZONE floor for both sticks",
		"Feed: direct-only single-pad evdev pump bypassing X11; TIPSY_GAMEPAD_PATH is parsed-but-ignored",
	)
	var msg string
	switch {
	case !info.Enabled:
		msg = "Gamepad input is disabled by the TIPSY_GAMEPAD=0|off kill-switch; no pads are opened and the engine sees zero pads."
	case info.PadCount == 0 && len(info.Denied) > 0:
		msg = "No accessible gamepad: permission denied on " + strconv.Itoa(len(info.Denied)) + " node(s). " + info.PermissionHint + ". Zero pads is the honest state; no fake pad is synthesized."
	case info.PadCount == 0:
		msg = "No gamepad found (zero devices is the honest state); the engine sees zero pads. Plug in a USB pad or check permissions if one is connected."
	default:
		msg = "Accessible gamepads are listed with content-free names/caps (no input values; secrets never logged). " + info.Note
	}
	return &SubsystemReport{
		Subsystem: "gamepad",
		Status:    status,
		Facts:     facts,
		Message:   msg,
	}
}

// gamepadEnvFacts reports enable state, the lean calibration override, and
// the effective floor the pump enforces (persisted file overlaid with env:
// file < env).
func gamepadEnvFacts(info GamepadInfo) []string {
	enabledWord := "enabled"
	if !info.Enabled {
		enabledWord = "disabled"
	}
	rawEnable, ok := info.Env["TIPSY_GAMEPAD"]
	if !ok {
		rawEnable = "unset"
	} else if strings.TrimSpace(rawEnable) == "" {
		rawEnable = "set-empty"
	}
	out := []string{
		fmt.Sprintf("TIPSY_GAMEPAD=%s (%s)", rawEnable, enabledWord),
		fmt.Sprintf("TIPSY_GAMEPAD_PATH=parsed-but-ignored (effective: %s)", info.PathSelector),
	}
	if v, ok := info.Env["TIPSY_GAMEPAD_DEADZONE"]; ok {
		out = append(out, fmt.Sprintf("TIPSY_GAMEPAD_DEADZONE=%s (default: device flat, 0.08 fallback when flat=0)", v))
	} else {
		out = append(out, "TIPSY_GAMEPAD_DEADZONE=unset (default: device flat, 0.08 fallback when flat=0)")
	}
	eff := effectiveGamepadConfig()
	out = append(out, fmt.Sprintf("Effective: file < env → stick floor=%.2f; subsystem=%s (missing JSON = defaults)",
		eff.EffectiveDeadzone(), effectiveEnabledWord(info.Enabled, eff.Enabled)))
	return out
}

// effectiveEnabledWord names the subsystem gate the pump enforces: the
// TIPSY_GAMEPAD kill-switch and the persisted gamepad.enabled switch must
// both agree, otherwise the engine sees zero pads.
func effectiveEnabledWord(killSwitchOn, fileOn bool) string {
	switch {
	case !killSwitchOn:
		return "off (kill-switch)"
	case !fileOn:
		return "off (config file)"
	default:
		return "on"
	}
}

// describeGamepadPad renders one connect-time snapshot content-free: the one
// caps line (topology + advertised Android sets). Mapping detail verbosity
// is deleted; Detail stays empty for formatter/GUI shape compatibility.
func describeGamepadPad(p gamepad.DeviceInfo) GamepadPad {
	m := p.Mapping
	if m.Name == "" {
		m, _ = gamepad.ResolveMappingForDevice(p)
	}
	mapping := m.Name
	if mapping == "" {
		mapping = "spec-default"
	}
	name := sanitizeGamepadName(p.Name)
	return GamepadPad{
		Path:    p.Path,
		Name:    name,
		Vendor:  fmt.Sprintf("%04x", p.ID.Vendor),
		Product: fmt.Sprintf("%04x", p.ID.Product),
		Mapping: mapping,
		Caps:    gamepadCaps(p),
	}
}

// gamepadCaps summarizes honest capabilities in one line: present ABS
// topology, present pad-button count, and the advertised Android key/motion
// sets. Unreported axes are absent, never zero-filled.
func gamepadCaps(p gamepad.DeviceInfo) string {
	var abs []string
	for _, c := range p.SortedAbsCodes() {
		abs = append(abs, gamepadAbsName(c))
	}
	absStr := "-"
	if len(abs) > 0 {
		absStr = strings.Join(abs, ",")
	}
	nBtn := 0
	for _, c := range gamepadButtonCodes {
		if p.HasKey[c] {
			nBtn++
		}
	}
	keys := gamepad.SupportedKeysForDevice(p)
	m := p.Mapping
	if m.Name == "" {
		m, _ = gamepad.ResolveMappingForDevice(p)
	}
	motions := gamepad.SupportedMotions(m, p.HasAbs)
	return fmt.Sprintf("abs=%s buttons=%d androidKeys=%s androidAxes=%s",
		absStr, nBtn, intsString(keys), intsString(motions))
}

// gamepadButtonCodes are the evdev buttons counted as pad capabilities.
var gamepadButtonCodes = []uint16{
	gamepad.BtnSouth, gamepad.BtnEast, gamepad.BtnC, gamepad.BtnNorth,
	gamepad.BtnWest, gamepad.BtnZ, gamepad.BtnTL, gamepad.BtnTR,
	gamepad.BtnTL2, gamepad.BtnTR2, gamepad.BtnSelect, gamepad.BtnStart,
	gamepad.BtnMode, gamepad.BtnThumbl, gamepad.BtnThumbr,
	gamepad.BtnDpadUp, gamepad.BtnDpadDown, gamepad.BtnDpadLeft,
	gamepad.BtnDpadRight, gamepad.KeyMenu, gamepad.KeyBack,
}

func gamepadAbsName(c uint16) string {
	switch c {
	case gamepad.NoAxis:
		return "-"
	case gamepad.AbsX:
		return "X"
	case gamepad.AbsY:
		return "Y"
	case gamepad.AbsZ:
		return "Z"
	case gamepad.AbsRX:
		return "RX"
	case gamepad.AbsRY:
		return "RY"
	case gamepad.AbsRZ:
		return "RZ"
	case gamepad.AbsHat0X:
		return "HAT0X"
	case gamepad.AbsHat0Y:
		return "HAT0Y"
	case gamepad.AbsHat2X:
		return "HAT2X"
	case gamepad.AbsHat2Y:
		return "HAT2Y"
	}
	return fmt.Sprintf("ABS_%d", c)
}

func intsString(ints []int) string {
	if len(ints) == 0 {
		return "[]"
	}
	parts := make([]string, len(ints))
	for i, v := range ints {
		parts[i] = strconv.Itoa(v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// sanitizeGamepadName mirrors the gamepad package's connect-time rule:
// trim, cap at 64 bytes, strip control characters.
func sanitizeGamepadName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}
