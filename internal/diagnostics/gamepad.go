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
// usually empty.
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
	Enabled      bool   `json:"enabled"`
	PathSelector string `json:"pathSelector"`
	// DisabledBy names the gate that is off when Enabled is false:
	// "kill-switch" (TIPSY_GAMEPAD=0|off) or "config" (the persisted
	// gamepad.enabled switch). The kill-switch wins when both are off.
	DisabledBy     string            `json:"disabledBy,omitempty"`
	PadCount       int               `json:"padCount"`
	Pads           []GamepadPad      `json:"pads,omitempty"`
	Denied         []string          `json:"denied,omitempty"`
	PermissionHint string            `json:"permissionHint,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Note           string            `json:"note,omitempty"`
}

// gamepadScanFunc enumerates evdev nodes. It never synthesizes a device.
var gamepadScanFunc = gamepad.Scan

func gamepadDir() string { return gamepad.InputNodeDir }

// The gates that can switch the engine's pads off. Mirrors the JNI side.
const (
	gamepadDisabledByKillSwitch = "kill-switch"
	gamepadDisabledByConfig     = "config"
)

// gamepadEnabledLive reports whether pads may reach the engine right now: the
// TIPSY_GAMEPAD kill-switch AND the persisted gamepad.enabled switch must both
// allow it. Both are read fresh from the environment and the settings file on
// every call (no cache) because the JNI pump re-evaluates the same two inputs
// at every launch, at window-focus gain, and on its frame path; a cached answer
// here would report a toggle the engine has already acted on (or the reverse).
// It returns the raw TIPSY_GAMEPAD value and the gate that is off ("" when
// enabled). An unreadable settings file reads as defaults (enabled), as on a
// first launch; a running session keeps its last good value instead.
func gamepadEnabledLive() (enabled bool, raw string, disabledBy string) {
	raw = os.Getenv("TIPSY_GAMEPAD")
	if gamepad.KillSwitchOff(os.LookupEnv) {
		return false, raw, gamepadDisabledByKillSwitch
	}
	if !effectiveGamepadConfig().Enabled {
		return false, raw, gamepadDisabledByConfig
	}
	return true, raw, ""
}

// gamepadPathLive reports the effective feed-in arm: pads are direct-only, so
// any TIPSY_GAMEPAD_PATH value is parsed-but-ignored.
func gamepadPathLive() (string, string) {
	return "direct", os.Getenv("TIPSY_GAMEPAD_PATH")
}

// gamepadEnvKeys lists the TIPSY_GAMEPAD* variables the diagnose facts report.
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
	enabled, _, disabledBy := gamepadEnabledLive()
	pathSel, _ := gamepadPathLive()
	info := GamepadInfo{
		Enabled:      enabled,
		DisabledBy:   disabledBy,
		PathSelector: pathSel,
		Env:          gamepadEnv(),
	}
	if !enabled {
		// Nothing opens /dev/input while pads are off, whichever gate is off.
		if disabledBy == gamepadDisabledByConfig {
			info.Note = "disabled by Settings > Controller (gamepad.enabled=false); no pads are opened"
		} else {
			info.Note = "disabled by TIPSY_GAMEPAD=0|off kill-switch; no pads are opened"
		}
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
// and controller canonicalize here. One caps line per pad.
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
	case !info.Enabled && info.DisabledBy == gamepadDisabledByConfig:
		msg = "Gamepad input is switched off in Settings > Controller (gamepad.enabled=false); no pads are opened and the engine sees zero pads. A running game picks the change up on the next controller input or when its window regains focus."
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
	// The TIPSY_GAMEPAD line reports the kill-switch alone; the persisted
	// switch is reported on the Effective line below.
	killOff := info.DisabledBy == gamepadDisabledByKillSwitch || (!info.Enabled && info.DisabledBy == "")
	enabledWord := "enabled"
	if killOff {
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
		eff.EffectiveDeadzone(), effectiveEnabledWord(!killOff, eff.Enabled)))
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
// caps line (topology + advertised Android sets). Detail stays empty for
// formatter/GUI shape compatibility.
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
