// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package clientsettings owns the small, validated Roblox settings surface
// exposed by Tipsy. It deliberately does not expose arbitrary fast flags.
package clientsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/graphics"
)

type Renderer string

const (
	RendererAuto   Renderer = "auto"
	RendererOpenGL Renderer = "opengl"
	RendererVulkan Renderer = "vulkan"

	flagPreferOpenGL = "FFlagDebugGraphicsPreferOpenGL"
	flagPreferVulkan = "FFlagDebugGraphicsPreferVulkan"
	// The 2.734.917 client resolves GameBasicSettingsFramerateCap through its
	// versioned flag lookup with version 5. Fast-variable ingestion supplies
	// the FFlag type prefix used here.
	flagGameBasicSettingsFramerateCap = "FFlagGameBasicSettingsFramerateCap5"
	flagTaskSchedulerLimitFPS240      = "FFlagTaskSchedulerLimitTargetFpsTo2402"
	intTaskSchedulerTargetFPS         = "DFIntTaskSchedulerTargetFps"
	// Official allowlisted ClientAppSettings keys. Android Roblox otherwise
	// loads mobile-phone texture quality. 3 is high; 1 is the documented
	// memory-saving low (not last-resort 0).
	flagTextureQualityOverrideEnabled = "DFFlagTextureQualityOverrideEnabled"
	intTextureQualityOverride         = "DFIntTextureQualityOverride"
	textureQualityHigh                = "3"
	textureQualityLow                 = "1"

	maxSettingsBytes  = 64 << 10
	maxRobloxXMLBytes = 4 << 20
	unlimitedFPSValue = "9999"
)

type RendererOption struct {
	Renderer  Renderer `json:"renderer"`
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
}

func RendererOptions() []RendererOption {
	caps := graphics.ProbeRendererCapabilities()
	auto := RendererOption{Renderer: RendererAuto, Available: caps.OpenGL.Available || caps.Vulkan.Available}
	switch {
	case caps.Vulkan.Available:
		auto.Reason = "Auto selects Vulkan when the Android WSI adapter and a host device are available"
	case caps.OpenGL.Available:
		auto.Reason = caps.OpenGL.Reason
	default:
		auto.Reason = caps.OpenGL.Reason
	}
	return []RendererOption{
		auto,
		{Renderer: RendererOpenGL, Available: caps.OpenGL.Available, Reason: caps.OpenGL.Reason},
		{Renderer: RendererVulkan, Available: caps.Vulkan.Available, Reason: caps.Vulkan.Reason},
	}
}

type UnsupportedRendererError = graphics.UnsupportedRendererError

type FrameRateMode string

const (
	FrameRateAuto      FrameRateMode = "auto"
	FrameRateLimited   FrameRateMode = "limited"
	FrameRateUnlimited FrameRateMode = "unlimited"
	MinFrameRate                     = 30
	MaxFrameRate                     = 240

	DisplayPrimary  = "primary"
	DisplayPointer  = "pointer"
	maxDisplayBytes = 128
)

type FrameRate struct {
	Mode  FrameRateMode `json:"mode"`
	Limit int           `json:"limit,omitempty"`
}

// NeedsUnthrottledPresentation is the superseded FPS-coupled policy retained
// only until the Runtime integration switches to Settings' VSync policy. New
// callers must use Settings.NeedsUnthrottledPresentation so FPS and VSync stay
// independent.
func (f FrameRate) NeedsUnthrottledPresentation(refreshHz float64) bool {
	switch f.Mode {
	case FrameRateUnlimited:
		return true
	case FrameRateLimited:
		if refreshHz <= 0 {
			refreshHz = 60
		}
		// Accommodate nominal modes such as 59.94 and 143.98 Hz without
		// treating matching integer limits as requests above refresh.
		return float64(f.Limit) > refreshHz+1
	default:
		return false
	}
}

type Settings struct {
	Renderer  Renderer  `json:"renderer"`
	FrameRate FrameRate `json:"frameRate"`
	VSync     bool      `json:"vsync"`
	// LowTextureMode requests Roblox's documented memory-saving texture
	// quality (override 1). The zero value / missing JSON field is false, so
	// existing configs and Default() emit high quality (override 3).
	LowTextureMode bool `json:"lowTextureMode"`
	// Display selects where Tipsy maps the launcher and Roblox windows.
	// "primary" (default) pins them to the current main monitor, "pointer"
	// restores window-manager mouse placement, and any other value is an
	// XRandR/Qt output name. A missing output falls back to primary at spawn.
	Display string `json:"display,omitempty"`
}

// NeedsUnthrottledPresentation reports the inverse of the user's independent
// VSync choice. VSync defaults off, so a missing field in an older persisted
// config requests unthrottled presentation without changing its FPS target.
func (s Settings) NeedsUnthrottledPresentation() bool {
	return !s.VSync
}

type ApplyResult struct {
	Settings         Settings `json:"settings"`
	RestartRequired  bool     `json:"restartRequired"`
	FrameRateApplied bool     `json:"frameRateApplied"`
	FrameRateNote    string   `json:"frameRateNote,omitempty"`
}

type persistedSettings struct {
	Settings
	FPSOwned    bool   `json:"fpsOwned,omitempty"`
	FPSOriginal string `json:"fpsOriginal,omitempty"`
	FPSApplied  string `json:"fpsApplied,omitempty"`
}

type Service struct {
	Path    string
	XMLPath string
	now     func() time.Time
}

func New() *Service {
	return &Service{
		Path:    config.Paths().ClientSettingsFile,
		XMLPath: RobloxSettingsPath(),
		now:     time.Now,
	}
}

func RobloxSettingsPath() string {
	return filepath.Join(config.Paths().DataDir, "app-data", "com.roblox.client", "files", "appData", "GlobalBasicSettings_13.xml")
}

func Default() Settings {
	return Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateAuto}, Display: DisplayPrimary}
}

func (s Settings) Validate() error {
	s = normalized(s)
	if err := s.validateShape(); err != nil {
		return err
	}
	return graphics.RequireRenderer(graphics.Renderer(s.Renderer))
}

// validateShape checks durable data independently of host capabilities. A
// renderer choice can become temporarily unavailable without making the
// persisted settings document corrupt.
func (s Settings) validateShape() error {
	switch s.Renderer {
	case RendererAuto, RendererOpenGL, RendererVulkan:
	default:
		return fmt.Errorf("renderer must be auto, opengl, or vulkan")
	}
	switch s.FrameRate.Mode {
	case FrameRateAuto, FrameRateUnlimited:
		if s.FrameRate.Limit != 0 {
			return fmt.Errorf("automatic and unlimited frame-rate modes do not accept a numeric limit")
		}
	case FrameRateLimited:
		if s.FrameRate.Limit < MinFrameRate || s.FrameRate.Limit > MaxFrameRate {
			return fmt.Errorf("limited frame rate must be between %d and %d", MinFrameRate, MaxFrameRate)
		}
	default:
		return fmt.Errorf("frame-rate mode must be auto, limited, or unlimited")
	}
	return validateDisplay(s.Display)
}

func validateDisplay(display string) error {
	display = NormalizeDisplay(display)
	if len(display) > maxDisplayBytes {
		return fmt.Errorf("monitor name is too long")
	}
	if strings.ContainsAny(display, "/\\\x00") {
		return fmt.Errorf("monitor name is invalid")
	}
	return nil
}

// NormalizeDisplay maps a missing value to the primary-monitor default.
func NormalizeDisplay(display string) string {
	if display == "" {
		return DisplayPrimary
	}
	return display
}

func normalized(s Settings) Settings {
	if s.Renderer == "" {
		s.Renderer = RendererAuto
	}
	if s.FrameRate.Mode == "" {
		s.FrameRate.Mode = FrameRateAuto
	}
	s.Display = NormalizeDisplay(s.Display)
	return s
}

func (s *Service) Load(ctx context.Context) (Settings, error) {
	doc, err := s.loadDocument(ctx)
	return doc.Settings, err
}

func (s *Service) Apply(ctx context.Context, wanted Settings) (ApplyResult, error) {
	wanted = normalized(wanted)
	if err := wanted.Validate(); err != nil {
		return ApplyResult{}, err
	}
	release, err := AcquireClientLock()
	if err != nil {
		return ApplyResult{}, err
	}
	defer release()
	return s.applyLocked(ctx, wanted)
}

func (s *Service) applyLocked(ctx context.Context, wanted Settings) (ApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	oldDoc, err := s.loadDocument(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	newDoc := oldDoc
	newDoc.Settings = wanted

	xmlPath := s.xmlPath()
	oldXML, readErr := readRegularFile(xmlPath, maxRobloxXMLBytes)
	xmlExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ApplyResult{}, readErr
	}
	newXML := oldXML
	xmlChanged := false
	note := ""
	if xmlExists {
		_, current, err := updateFramerateCap(oldXML, "")
		if err != nil {
			s.backupMalformedXML(oldXML)
			return ApplyResult{}, fmt.Errorf("Roblox settings XML is malformed; the original was preserved: %w", err)
		}
		switch wanted.FrameRate.Mode {
		case FrameRateAuto:
			if oldDoc.FPSOwned && current == oldDoc.FPSApplied {
				newXML, _, err = updateFramerateCap(oldXML, oldDoc.FPSOriginal)
				if err != nil {
					return ApplyResult{}, err
				}
				xmlChanged = !bytes.Equal(oldXML, newXML)
			}
			newDoc.FPSOwned = false
			newDoc.FPSOriginal = ""
			newDoc.FPSApplied = ""
		case FrameRateLimited, FrameRateUnlimited:
			value := fpsValue(wanted.FrameRate)
			if !oldDoc.FPSOwned {
				newDoc.FPSOriginal = current
			}
			newDoc.FPSOwned = true
			newDoc.FPSApplied = value
			newXML, _, err = updateFramerateCap(oldXML, value)
			if err != nil {
				return ApplyResult{}, err
			}
			xmlChanged = !bytes.Equal(oldXML, newXML)
		}
	} else if wanted.FrameRate.Mode != FrameRateAuto {
		note = "Roblox has not created GlobalBasicSettings_13.xml yet; the choice is saved and will be applied on a later launch."
	}

	graphicsChanged := oldDoc.Renderer != wanted.Renderer || oldDoc.FrameRate != wanted.FrameRate || oldDoc.VSync != wanted.VSync || oldDoc.LowTextureMode != wanted.LowTextureMode
	placementChanged := oldDoc.Display != wanted.Display
	docChanged := oldDoc != newDoc
	if xmlChanged {
		if err := config.AtomicWriteFile(xmlPath, newXML, 0o600); err != nil {
			return ApplyResult{}, fmt.Errorf("write Roblox frame-rate setting: %w", err)
		}
	}
	if docChanged {
		if err := s.writeDocument(newDoc); err != nil {
			if xmlChanged {
				_ = config.AtomicWriteFile(xmlPath, oldXML, 0o600)
			}
			return ApplyResult{}, err
		}
	}
	applyNote := noteForFrameRate(wanted.FrameRate, note)
	if placementChanged && !graphicsChanged && !xmlChanged {
		applyNote = noteForDisplay(wanted.Display)
	}
	return ApplyResult{
		Settings:         wanted,
		RestartRequired:  graphicsChanged || xmlChanged,
		FrameRateApplied: xmlExists,
		FrameRateNote:    applyNote,
	}, nil
}

// ReconcileWhileClientLocked reapplies an explicit XML frame-rate choice just
// before launch. The caller must hold the client lock for the full launch.
func (s *Service) ReconcileWhileClientLocked(ctx context.Context) error {
	doc, err := s.loadDocument(ctx)
	if err != nil {
		return err
	}
	if doc.FrameRate.Mode == FrameRateAuto {
		return nil
	}
	_, err = s.applyLocked(ctx, doc.Settings)
	return err
}

func (s *Service) Reset(ctx context.Context) (Settings, error) {
	want := Default()
	_, err := s.Apply(ctx, want)
	return want, err
}

// Overrides converts renderer, FPS, and texture-quality choices into Roblox
// ClientAppSettings strings. The feature gate exposes the current client's
// official GameBasicSettings frame-rate surface. Limited mode sets the current
// client's legacy scheduler target; Unlimited opts out of the current 240
// limiter and keeps its high finite target in GlobalBasicSettings_13.xml.
// Automatic mode deliberately leaves both FPS controls under
// downloaded-policy/client ownership. Texture quality is always overridden:
// high (3) by default, or low (1) when LowTextureMode is on.
func Overrides(s Settings) (map[string]any, error) {
	s = normalized(s)
	if err := s.Validate(); err != nil {
		return nil, err
	}
	out := map[string]any{
		flagGameBasicSettingsFramerateCap: "True",
		flagTextureQualityOverrideEnabled: "True",
		intTextureQualityOverride:         textureQualityHigh,
	}
	if s.LowTextureMode {
		out[intTextureQualityOverride] = textureQualityLow
	}
	switch s.Renderer {
	case RendererOpenGL:
		out[flagPreferOpenGL] = "True"
	case RendererVulkan:
		out[flagPreferVulkan] = "True"
	case RendererAuto:
		resolved, err := graphics.ProbeRendererCapabilities().Resolve(graphics.RendererAuto)
		if err != nil {
			return nil, err
		}
		if resolved == graphics.RendererVulkan {
			out[flagPreferVulkan] = "True"
		}
	}
	switch s.FrameRate.Mode {
	case FrameRateLimited:
		out[intTaskSchedulerTargetFPS] = strconv.Itoa(s.FrameRate.Limit)
	case FrameRateUnlimited:
		// The current downloaded Android policy enables this exact 240-FPS
		// limiter. Unlimited alone opts out; its high finite 9999 request is
		// owned by GlobalBasicSettings_13.xml. Do not also feed 9999 through the
		// legacy TaskSchedulerTargetFps registry: this client unconditionally
		// clamps that integer to 240 during post-settings initialization. Limited
		// stays within 30..240, and Auto leaves both settings under
		// downloaded-policy/client ownership.
		out[flagTaskSchedulerLimitFPS240] = "False"
	}
	return out, nil
}

func (s *Service) LoadOverrides(ctx context.Context) (map[string]any, error) {
	settings, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	return Overrides(settings)
}

func fpsValue(f FrameRate) string {
	if f.Mode == FrameRateUnlimited {
		return unlimitedFPSValue
	}
	return strconv.Itoa(f.Limit)
}

func noteForFrameRate(f FrameRate, prior string) string {
	if prior != "" {
		return prior
	}
	if f.Mode == FrameRateUnlimited {
		return "Experimental: Tipsy requests a high finite 9999 FPS target; Roblox, the graphics driver, or the hardware may impose another limit."
	}
	return ""
}

func noteForDisplay(display string) string {
	switch NormalizeDisplay(display) {
	case DisplayPointer:
		return "The next Tipsy and Roblox windows follow the mouse, the same way the window manager used to place them."
	case DisplayPrimary:
		return "The next Tipsy and Roblox windows open on the main monitor."
	default:
		return "The next Tipsy and Roblox windows open on the selected monitor."
	}
}

func (s *Service) loadDocument(ctx context.Context) (persistedSettings, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return persistedSettings{}, err
	}
	path := s.settingsPath()
	data, err := readRegularFile(path, maxSettingsBytes)
	if errors.Is(err, os.ErrNotExist) {
		return persistedSettings{Settings: Default()}, nil
	}
	if err != nil {
		return persistedSettings{}, err
	}
	var got persistedSettings
	if err := json.Unmarshal(data, &got); err != nil {
		return s.recoverMalformed(ctx, path)
	}
	got.Settings = normalized(got.Settings)
	if err := got.Settings.validateShape(); err != nil {
		return s.recoverMalformed(ctx, path)
	}
	if got.FPSOwned && (got.FPSOriginal == "" || got.FPSApplied == "") {
		return s.recoverMalformed(ctx, path)
	}
	return got, nil
}

func (s *Service) writeDocument(doc persistedSettings) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return config.AtomicWriteFile(s.settingsPath(), data, 0o600)
}

func (s *Service) settingsPath() string {
	if s != nil && s.Path != "" {
		return s.Path
	}
	return config.Paths().ClientSettingsFile
}

func (s *Service) xmlPath() string {
	if s != nil && s.XMLPath != "" {
		return s.XMLPath
	}
	return RobloxSettingsPath()
}

func (s *Service) recoverMalformed(ctx context.Context, path string) (persistedSettings, error) {
	if err := ctx.Err(); err != nil {
		return persistedSettings{}, err
	}
	backup := s.invalidPath(path)
	if err := os.Rename(path, backup); err != nil {
		return persistedSettings{}, fmt.Errorf("recover malformed settings: %w", err)
	}
	doc := persistedSettings{Settings: Default()}
	if err := s.writeDocument(doc); err != nil {
		_ = os.Rename(backup, path)
		return persistedSettings{}, fmt.Errorf("recover malformed settings: %w", err)
	}
	return doc, nil
}

func (s *Service) backupMalformedXML(data []byte) {
	_ = config.AtomicWriteFile(s.invalidPath(s.xmlPath()), data, 0o600)
}

func (s *Service) invalidPath(path string) string {
	stamp := time.Now().UTC()
	if s != nil && s.now != nil {
		stamp = s.now().UTC()
	}
	base := fmt.Sprintf("%s.invalid-%s", path, stamp.Format("20060102T150405.000000000Z"))
	for i := 0; ; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Lstat(candidate); err != nil {
			// A permission or I/O error will be reported by the subsequent
			// rename/write. Do not spin forever while choosing a backup name.
			return candidate
		}
	}
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("settings path is not a regular file")
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("settings file exceeds %d bytes", limit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("settings file exceeds %d bytes", limit)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	return data, nil
}

func updateFramerateCap(data []byte, replacement string) ([]byte, string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	type scope struct{ name, class string }
	var stack []scope
	start, end := -1, -1
	value := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			class, attrName := "", ""
			for _, a := range t.Attr {
				if a.Name.Local == "class" {
					class = a.Value
				}
				if a.Name.Local == "name" {
					attrName = a.Value
				}
			}
			inSettings, inProperties := false, false
			for _, sc := range stack {
				if sc.name == "Item" && sc.class == "UserGameSettings" {
					inSettings = true
				}
				if inSettings && sc.name == "Properties" {
					inProperties = true
				}
			}
			stack = append(stack, scope{name: t.Name.Local, class: class})
			if t.Name.Local == "int" && attrName == "FramerateCap" && inSettings && inProperties {
				if start >= 0 {
					return nil, "", fmt.Errorf("multiple UserGameSettings FramerateCap fields")
				}
				start = int(dec.InputOffset())
			}
		case xml.CharData:
			if start >= 0 && end < start && len(stack) > 0 && stack[len(stack)-1].name == "int" {
				end = int(dec.InputOffset())
				value = strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if start < 0 || end < start || value == "" {
		return nil, "", fmt.Errorf("unique UserGameSettings FramerateCap field not found")
	}
	if _, err := strconv.Atoi(value); err != nil {
		return nil, "", fmt.Errorf("FramerateCap is not an integer")
	}
	if replacement == "" {
		return append([]byte(nil), data...), value, nil
	}
	span := data[start:end]
	prefixLen := len(span) - len(bytes.TrimLeft(span, " \t\r\n"))
	suffixLen := len(span) - len(bytes.TrimRight(span, " \t\r\n"))
	var out []byte
	out = append(out, data[:start+prefixLen]...)
	out = append(out, replacement...)
	out = append(out, data[end-suffixLen:]...)
	return out, value, nil
}
