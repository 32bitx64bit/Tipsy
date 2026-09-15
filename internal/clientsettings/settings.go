// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package clientsettings owns the validated Roblox settings surface exposed by
// Tipsy, including user-owned custom Fast Flags that are supplied to the
// official Android client-settings initialization on its next launch.
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
	"unicode"
	"unicode/utf8"

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
	// Texture quality uses the named LowTextureMode mapping. The current
	// Android client's TM2 repeatedly completes finer-mip requests with the
	// same coarse image, even with the high quality and memory settings active.
	// High uses the official TM1 path, verified with close-up avatar textures.
	// Both manager gates are required: NewRenderUseTextureManager2 otherwise
	// reenables the version-24 RenderUseTextureManager2 gate during startup.
	// Low restores both gates so the previous high launch cannot linger in
	// the engine flag cache. The 3/1 quality override pair remains high/low.
	flagTextureQualityOverrideEnabled = "DFFlagTextureQualityOverrideEnabled"
	intTextureQualityOverride         = "DFIntTextureQualityOverride"
	flagUITextureCompressionDesktop   = "FFlagUITextureCompressionDesktop"
	flagTCTextureCompressionDesktop   = "FFlagTCTextureCompressionDesktop"
	intRenderTextureTotalBudgetMB     = "FIntRenderTextureTotalBudgetMB"
	intRenderTextureMipBias           = "FIntRenderTextureMipBias"
	intRenderForceVideoMemorySize     = "FIntRenderForceVideoMemorySize"
	// Clothing composites (TextureCompositor) live under their own byte
	// budget: min(max(videoMemorySize/3, 8 MiB), this DFInt). The client
	// default cap is 48 MiB, which a 45-player server can exceed, and over
	// budget the compositor re-bakes clothing at 1/LowResFactor width
	// (measured 229 of 916). High raises the cap so the measured populated
	// server keeps full-width composites; low restores the default cap
	// explicitly.
	intDebugTc1MaxAllowedMemoryBudget     = "DFIntDebugTc1MaxAllowedMemoryBudget"
	flagTM2RuntimeTextureDisableStreaming = "FFlagTM2RuntimeTextureDisableStreaming"
	flagTM2SkipMipsForUnstreamable2       = "FFlagTM2SkipMipsForUnstreamable2"
	flagUseTM1LegacyMipPackForDecal       = "FFlagUseTM1PropsetUseLegacyMipPackForDecal"
	flagRenderUseTextureManager2          = "FFlagRenderUseTextureManager224"
	flagNewRenderUseTextureManager2       = "FFlagNewRenderUseTextureManager2"
	textureQualityHigh                    = "3"
	textureQualityLow                     = "1"
	textureBudgetHighMB                   = "128"
	textureBudgetLowMB                    = "64"
	textureMipBiasHigh                    = "0"
	textureMipBiasLow                     = "2"
	// 256 MiB / 48 MiB (client-default cap) in bytes.
	compositorBudgetHighBytes = "268435456"
	compositorBudgetLowBytes  = "50331648"
	// 1 GiB / 64 MiB in bytes. INT32-safe; matches caps.videoMemory units.
	videoMemoryHighBytes = "1073741824"
	videoMemoryLowBytes  = "67108864"

	maxSettingsBytes    = 64 << 10
	maxRobloxXMLBytes   = 4 << 20
	maxFastFlags        = 128
	maxFastFlagNameLen  = 128
	maxFastFlagValueLen = 4 << 10
	maxFastFlagBytes    = 32 << 10
	unlimitedFPSValue   = "9999"
	// engineDefaultFramerateCap is Roblox's stored "no Tipsy override"
	// value (Auto / client-owned). Used when Tipsy must write a working
	// UserGameSettings document without having observed a prior cap.
	engineDefaultFramerateCap = "-1"
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

type Settings struct {
	Renderer  Renderer  `json:"renderer"`
	FrameRate FrameRate `json:"frameRate"`
	VSync     bool      `json:"vsync"`
	// LowTextureMode requests the memory-saving texture mapping (override 1,
	// 64 MiB video-memory cap, 48 MiB clothing-compositor cap, TM2 skip-mips,
	// no desktop DXT). That pair yields about 21.3 MiB of effective compositor
	// budget. The zero value / missing JSON field is false, so existing configs
	// and Default() emit the high-quality mapping (override 3, 1 GiB
	// video-memory cap, 256 MiB effective compositor budget, desktop DXT,
	// official TM1).
	LowTextureMode bool `json:"lowTextureMode"`
	// Display selects where Tipsy maps the launcher and Roblox windows.
	// "primary" (default) pins them to the current main monitor, "pointer"
	// restores window-manager mouse placement, and any other value is an
	// XRandR/Qt output name. A missing output falls back to primary at spawn.
	Display string `json:"display,omitempty"`
	// StartFullscreen is a Tipsy-owned window preference. When enabled, Tipsy
	// asks the desktop window manager to fullscreen a newly created Roblox
	// window. It does not read, change, or mirror Roblox's in-app fullscreen
	// setting. The missing JSON field keeps existing installations windowed.
	StartFullscreen bool `json:"startFullscreen"`
	// DiscordRichPresence shows the current experience on Discord. Missing
	// JSON and Default() leave it off. DiscordJoinButton stays off unless
	// explicitly enabled.
	DiscordRichPresence bool `json:"discordRichPresence"`
	DiscordJoinButton   bool `json:"discordJoinButton"`
}

// FastFlag is one user-owned Roblox Fast Flag override. Name identifies a
// supported Fast Flag type and Value is its serialized client-settings value.
// Values are intentionally strings because that is the representation used by
// Roblox's applicationSettings JSON surface.
type FastFlag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FastFlagConflict identifies a custom Fast Flag that takes precedence over a
// normal Tipsy Settings control. It is informational: custom Fast Flags remain
// valid so an informed user can intentionally override a Settings choice.
type FastFlagConflict struct {
	Name    string
	Setting string
}

var fastFlagPrefixes = []string{"FFlag", "DFFlag", "FInt", "DFInt", "FString", "DFString"}

var fastFlagControlLabels = map[string]string{
	flagPreferOpenGL:                      "Renderer",
	flagPreferVulkan:                      "Renderer",
	flagGameBasicSettingsFramerateCap:     "Frame rate",
	flagTaskSchedulerLimitFPS240:          "Frame rate",
	intTaskSchedulerTargetFPS:             "Frame rate",
	flagTextureQualityOverrideEnabled:     "Texture quality",
	intTextureQualityOverride:             "Texture quality",
	flagUITextureCompressionDesktop:       "Texture quality",
	flagTCTextureCompressionDesktop:       "Texture quality",
	intRenderTextureTotalBudgetMB:         "Texture quality",
	intRenderTextureMipBias:               "Texture quality",
	intDebugTc1MaxAllowedMemoryBudget:     "Texture quality",
	intRenderForceVideoMemorySize:         "Texture quality",
	flagTM2RuntimeTextureDisableStreaming: "Texture quality",
	flagTM2SkipMipsForUnstreamable2:       "Texture quality",
	flagUseTM1LegacyMipPackForDecal:       "Texture quality",
	flagRenderUseTextureManager2:          "Texture quality",
	flagNewRenderUseTextureManager2:       "Texture quality",
}

// ValidateFastFlags checks the bounded, typed custom-Fast-Flag surface.
// It permits the flag classes accepted by the Android client-settings JSON:
// boolean FFlag/DFFlag, signed 32-bit FInt/DFInt, and printable FString/
// DFString values. Duplicate names are rejected so the persisted list always
// has one unambiguous final value for each flag.
func ValidateFastFlags(flags []FastFlag) error {
	if len(flags) > maxFastFlags {
		return fmt.Errorf("at most %d custom Fast Flags are allowed", maxFastFlags)
	}
	seen := make(map[string]struct{}, len(flags))
	total := 0
	for _, flag := range flags {
		kind, ok := fastFlagKind(flag.Name)
		if !ok {
			return fmt.Errorf("custom Fast Flag name is invalid")
		}
		if _, duplicate := seen[flag.Name]; duplicate {
			return fmt.Errorf("custom Fast Flag names must be unique")
		}
		seen[flag.Name] = struct{}{}
		if err := validateFastFlagValue(kind, flag.Value); err != nil {
			return err
		}
		total += len(flag.Name) + len(flag.Value)
		if total > maxFastFlagBytes {
			return fmt.Errorf("custom Fast Flag data exceeds %d bytes", maxFastFlagBytes)
		}
	}
	return nil
}

func fastFlagKind(name string) (string, bool) {
	if name == "" || len(name) > maxFastFlagNameLen {
		return "", false
	}
	for _, prefix := range fastFlagPrefixes {
		if suffix, found := strings.CutPrefix(name, prefix); found {
			if suffix == "" {
				return "", false
			}
			for _, r := range suffix {
				if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
					return "", false
				}
			}
			return prefix, true
		}
	}
	return "", false
}

func validateFastFlagValue(kind, value string) error {
	if value == "" || len(value) > maxFastFlagValueLen || !utf8.ValidString(value) {
		return fmt.Errorf("custom Fast Flag value is invalid")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("custom Fast Flag value contains a control character")
		}
	}
	switch kind {
	case "FFlag", "DFFlag":
		if value != "True" && value != "False" {
			return fmt.Errorf("boolean Fast Flags require True or False")
		}
	case "FInt", "DFInt":
		if _, err := strconv.ParseInt(value, 10, 32); err != nil {
			return fmt.Errorf("integer Fast Flags require a signed 32-bit integer")
		}
	}
	return nil
}

// FastFlagConflicts returns the custom entries that will override one of
// Tipsy's regular Settings controls at launch. The result retains the input
// order for direct display in an editor.
func FastFlagConflicts(flags []FastFlag) []FastFlagConflict {
	var conflicts []FastFlagConflict
	for _, flag := range flags {
		if setting, ok := fastFlagControlLabels[flag.Name]; ok {
			conflicts = append(conflicts, FastFlagConflict{Name: flag.Name, Setting: setting})
		}
	}
	return conflicts
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
	FastFlags   []FastFlag `json:"fastFlags,omitempty"`
	FPSOwned    bool       `json:"fpsOwned,omitempty"`
	FPSOriginal string     `json:"fpsOriginal,omitempty"`
	FPSApplied  string     `json:"fpsApplied,omitempty"`
	unknown     map[string]json.RawMessage
}

func equalFastFlags(a, b []FastFlag) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneFastFlags(flags []FastFlag) []FastFlag {
	if flags == nil {
		return nil
	}
	return append([]FastFlag(nil), flags...)
}

func samePersistedSettings(a, b persistedSettings) bool {
	return a.Settings == b.Settings &&
		equalFastFlags(a.FastFlags, b.FastFlags) &&
		a.FPSOwned == b.FPSOwned &&
		a.FPSOriginal == b.FPSOriginal &&
		a.FPSApplied == b.FPSApplied
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
	return Settings{
		Renderer:  RendererAuto,
		FrameRate: FrameRate{Mode: FrameRateAuto},
		Display:   DisplayPrimary,
	}
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

// settingsDocumentOnlyChange reports a draft that differs only in host-side
// startup preferences or Discord Rich Presence. Neither changes Roblox's XML
// nor a live client, so it is safe to save while Roblox holds the client lock.
func settingsDocumentOnlyChange(draft, saved Settings) bool {
	draft = normalized(draft)
	saved = normalized(saved)
	if draft.DiscordRichPresence == saved.DiscordRichPresence &&
		draft.DiscordJoinButton == saved.DiscordJoinButton &&
		draft.StartFullscreen == saved.StartFullscreen {
		return false
	}
	draft.DiscordRichPresence, draft.DiscordJoinButton = false, false
	saved.DiscordRichPresence, saved.DiscordJoinButton = false, false
	draft.StartFullscreen, saved.StartFullscreen = false, false
	return draft == saved
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
	release, err := AcquireSettingsDocumentLock()
	if err != nil {
		return Settings{}, err
	}
	defer release()
	doc, err := s.loadDocument(ctx)
	return doc.Settings, err
}

func (s *Service) Apply(ctx context.Context, wanted Settings) (ApplyResult, error) {
	wanted = normalized(wanted)
	if err := wanted.Validate(); err != nil {
		return ApplyResult{}, err
	}
	oldDoc, err := s.loadDocument(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if settingsDocumentOnlyChange(wanted, oldDoc.Settings) {
		release, err := AcquireSettingsDocumentLock()
		if err != nil {
			return ApplyResult{}, err
		}
		defer release()
		return s.applyLocked(ctx, wanted)
	}
	release, err := AcquireClientLock()
	if err != nil {
		return ApplyResult{}, err
	}
	defer release()
	releaseDocument, err := AcquireSettingsDocumentLock()
	if err != nil {
		return ApplyResult{}, err
	}
	defer releaseDocument()
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
	diskXML, readErr := readRegularFile(xmlPath, maxRobloxXMLBytes)
	xmlExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ApplyResult{}, readErr
	}
	seed := diskXML
	createdWorking := false
	note := ""
	frameRateApplied := false
	if xmlExists {
		_, _, err := updateFramerateCap(diskXML, "")
		if err != nil {
			// Roblox's settings document is user data, never a launch gate.
			// An unclean shutdown can truncate it to zero bytes or cut it
			// mid-tag. Preserve those bytes, then write a working
			// UserGameSettings document so this launch (and older Tipsy
			// builds that still parse the file strictly) have a real XML
			// instead of an empty path.
			s.backupMalformedXML(diskXML)
			var parseErr xmlParseError
			if errors.As(err, &parseErr) {
				seed = workingUserGameSettingsXML(xmlFramerateCap(wanted.FrameRate))
				createdWorking = true
				note = "Roblox settings XML was malformed and has been preserved; Tipsy wrote a working settings document so this launch can use it."
			} else {
				note = "Roblox settings XML cannot take a frame-rate override yet; the choice is saved and will be applied on a later launch."
			}
		}
	} else if wanted.FrameRate.Mode != FrameRateAuto {
		seed = workingUserGameSettingsXML(xmlFramerateCap(wanted.FrameRate))
		createdWorking = true
	}

	newXML := diskXML
	xmlChanged := false
	if len(seed) > 0 {
		_, current, err := updateFramerateCap(seed, "")
		if err != nil {
			if createdWorking {
				return ApplyResult{}, fmt.Errorf("write working Roblox settings XML: %w", err)
			}
		} else {
			switch wanted.FrameRate.Mode {
			case FrameRateAuto:
				newXML = seed
				if oldDoc.FPSOwned && current == oldDoc.FPSApplied {
					newXML, _, err = updateFramerateCap(seed, oldDoc.FPSOriginal)
					if err != nil {
						return ApplyResult{}, err
					}
				}
				newDoc.FPSOwned = false
				newDoc.FPSOriginal = ""
				newDoc.FPSApplied = ""
				xmlChanged = !bytes.Equal(diskXML, newXML)
				frameRateApplied = xmlExists || createdWorking
			case FrameRateLimited, FrameRateUnlimited:
				value := fpsValue(wanted.FrameRate)
				if !oldDoc.FPSOwned {
					if createdWorking {
						newDoc.FPSOriginal = engineDefaultFramerateCap
					} else {
						newDoc.FPSOriginal = current
					}
				}
				newDoc.FPSOwned = true
				newDoc.FPSApplied = value
				newXML, _, err = updateFramerateCap(seed, value)
				if err != nil {
					return ApplyResult{}, err
				}
				xmlChanged = !bytes.Equal(diskXML, newXML)
				frameRateApplied = true
			}
		}
	}

	graphicsChanged := oldDoc.Renderer != wanted.Renderer || oldDoc.FrameRate != wanted.FrameRate || oldDoc.VSync != wanted.VSync || oldDoc.LowTextureMode != wanted.LowTextureMode
	placementChanged := oldDoc.Display != wanted.Display
	fullscreenChanged := oldDoc.StartFullscreen != wanted.StartFullscreen
	discordChanged := oldDoc.DiscordRichPresence != wanted.DiscordRichPresence || oldDoc.DiscordJoinButton != wanted.DiscordJoinButton
	docChanged := !samePersistedSettings(oldDoc, newDoc)
	if xmlChanged {
		if err := config.AtomicWriteFile(xmlPath, newXML, 0o600); err != nil {
			return ApplyResult{}, fmt.Errorf("write Roblox frame-rate setting: %w", err)
		}
	}
	if docChanged {
		if err := s.writeDocument(newDoc); err != nil {
			if xmlChanged && !createdWorking {
				_ = config.AtomicWriteFile(xmlPath, diskXML, 0o600)
			}
			return ApplyResult{}, err
		}
	}
	applyNote := noteForFrameRate(wanted.FrameRate, note)
	if placementChanged && !graphicsChanged && !xmlChanged {
		applyNote = noteForDisplay(wanted.Display)
	}
	if fullscreenChanged && !graphicsChanged && !xmlChanged && !placementChanged {
		applyNote = noteForStartFullscreen(wanted.StartFullscreen)
	}
	if discordChanged && !graphicsChanged && !xmlChanged && !placementChanged && !fullscreenChanged {
		applyNote = noteForDiscord(wanted)
	}
	return ApplyResult{
		Settings:         wanted,
		RestartRequired:  graphicsChanged || xmlChanged,
		FrameRateApplied: frameRateApplied,
		FrameRateNote:    applyNote,
	}, nil
}

// ReconcileWhileClientLocked writes a working Roblox settings document when
// the on-disk XML is missing or unparseable, then reapplies an explicit
// frame-rate choice. Auto still leaves a healthy document's cap under
// client ownership. The caller must hold the client lock for the full launch.
func (s *Service) ReconcileWhileClientLocked(ctx context.Context) error {
	release, err := AcquireSettingsDocumentLock()
	if err != nil {
		return err
	}
	defer release()
	doc, err := s.loadDocument(ctx)
	if err != nil {
		return err
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
// downloaded-policy/client ownership. High texture quality selects the
// official TM1 path because TM2 does not retain finer completed mips on the
// current Android client. Low restores TM2 and the memory-saving mapping.
func Overrides(s Settings) (map[string]any, error) {
	s = normalized(s)
	if err := s.Validate(); err != nil {
		return nil, err
	}
	out := map[string]any{
		flagGameBasicSettingsFramerateCap: "True",
	}
	applyTextureQualityOverrides(out, s.LowTextureMode)
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

// OverridesWithFastFlags returns the official ClientAppSettings overrides for
// normal Tipsy Settings plus validated user Fast Flags. User entries are added
// last by design, so an explicitly confirmed custom value wins over a normal
// Settings control for the next Roblox launch.
func OverridesWithFastFlags(s Settings, flags []FastFlag) (map[string]any, error) {
	out, err := Overrides(s)
	if err != nil {
		return nil, err
	}
	if err := ValidateFastFlags(flags); err != nil {
		return nil, err
	}
	for _, flag := range flags {
		out[flag.Name] = flag.Value
	}
	return out, nil
}

// applyTextureQualityOverrides writes Tipsy's named LowTextureMode mapping.
// Desktop compression, legacy-decal, video-memory, and compositor-budget keys
// are explicit so a previous high launch cannot linger in the engine flag
// cache when Low texture mode is on.
//
// FIntTextureCompositorLowResFactor is deliberately not emitted. Roblox ships
// 4 on every platform's CDN table (desktop included); Tipsy's former 1 did not
// produce full-resolution output and triggered pathological rebake/upsample
// loops (one NDS job reached ~1300 requests/s; one Blacksite Zeta job queued
// ~4000 in 90 seconds).
// FIntAvatarTextureMemoryMax is not emitted either: changing the attempted key
// did not move the measured compositor budget, and bounded registration
// inspection found only the same name as a memory-tracker label, not a
// consumed FastInt control.
func applyTextureQualityOverrides(out map[string]any, low bool) {
	out[flagTextureQualityOverrideEnabled] = "True"
	if low {
		out[intTextureQualityOverride] = textureQualityLow
		out[flagUITextureCompressionDesktop] = "False"
		out[flagTCTextureCompressionDesktop] = "False"
		out[intRenderTextureTotalBudgetMB] = textureBudgetLowMB
		out[intRenderTextureMipBias] = textureMipBiasLow
		out[intDebugTc1MaxAllowedMemoryBudget] = compositorBudgetLowBytes
		out[intRenderForceVideoMemorySize] = videoMemoryLowBytes
		out[flagTM2RuntimeTextureDisableStreaming] = "True"
		out[flagTM2SkipMipsForUnstreamable2] = "True"
		out[flagUseTM1LegacyMipPackForDecal] = "True"
		out[flagRenderUseTextureManager2] = "True"
		out[flagNewRenderUseTextureManager2] = "True"
		return
	}
	out[intTextureQualityOverride] = textureQualityHigh
	out[flagRenderUseTextureManager2] = "False"
	out[flagNewRenderUseTextureManager2] = "False"
	out[flagUITextureCompressionDesktop] = "True"
	out[flagTCTextureCompressionDesktop] = "True"
	out[intRenderTextureTotalBudgetMB] = textureBudgetHighMB
	out[intRenderTextureMipBias] = textureMipBiasHigh
	out[intDebugTc1MaxAllowedMemoryBudget] = compositorBudgetHighBytes
	out[intRenderForceVideoMemorySize] = videoMemoryHighBytes
	out[flagTM2RuntimeTextureDisableStreaming] = "False"
	out[flagTM2SkipMipsForUnstreamable2] = "False"
	out[flagUseTM1LegacyMipPackForDecal] = "False"
}

func (s *Service) LoadOverrides(ctx context.Context) (map[string]any, error) {
	release, err := AcquireSettingsDocumentLock()
	if err != nil {
		return nil, err
	}
	defer release()
	doc, err := s.loadDocument(ctx)
	if err != nil {
		return nil, err
	}
	return OverridesWithFastFlags(doc.Settings, doc.FastFlags)
}

// LoadFastFlags returns a copy of the persisted custom Fast Flag list. The
// list is independent of Settings so an ordinary renderer/FPS save cannot
// erase it.
func (s *Service) LoadFastFlags(ctx context.Context) ([]FastFlag, error) {
	release, err := AcquireSettingsDocumentLock()
	if err != nil {
		return nil, err
	}
	defer release()
	doc, err := s.loadDocument(ctx)
	if err != nil {
		return nil, err
	}
	return cloneFastFlags(doc.FastFlags), nil
}

// SaveFastFlags atomically replaces the custom Fast Flag list. It refuses to
// change a list while Roblox is running because its values are passed into the
// official Android client-settings initialization only at launch.
func (s *Service) SaveFastFlags(ctx context.Context, flags []FastFlag) error {
	if err := ValidateFastFlags(flags); err != nil {
		return err
	}
	releaseClient, err := AcquireClientLock()
	if err != nil {
		return err
	}
	defer releaseClient()
	releaseDocument, err := AcquireSettingsDocumentLock()
	if err != nil {
		return err
	}
	defer releaseDocument()
	doc, err := s.loadDocument(ctx)
	if err != nil {
		return err
	}
	if equalFastFlags(doc.FastFlags, flags) {
		return nil
	}
	doc.FastFlags = cloneFastFlags(flags)
	return s.writeDocument(doc)
}

func fpsValue(f FrameRate) string {
	if f.Mode == FrameRateUnlimited {
		return unlimitedFPSValue
	}
	return strconv.Itoa(f.Limit)
}

func xmlFramerateCap(f FrameRate) string {
	switch f.Mode {
	case FrameRateLimited, FrameRateUnlimited:
		return fpsValue(f)
	default:
		return engineDefaultFramerateCap
	}
}

// workingUserGameSettingsXML is a minimal official-shaped rbx settings
// document: version-4 roblox wrapper, the stock External sentinels, and one
// UserGameSettings FramerateCap. Missing properties stay absent so Roblox
// fills class defaults on load instead of Tipsy inventing graphics, camera,
// or volume values. cap must be a decimal integer.
func workingUserGameSettingsXML(cap string) []byte {
	if _, err := strconv.Atoi(cap); err != nil {
		cap = engineDefaultFramerateCap
	}
	return []byte(`<roblox xmlns:xmime="http://www.w3.org/2005/05/xmlmime" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:noNamespaceSchemaLocation="http://www.roblox.com/roblox.xsd" version="4">
	<External>null</External>
	<External>nil</External>
	<Item class="UserGameSettings">
		<Properties>
			<int name="FramerateCap">` + cap + `</int>
		</Properties>
	</Item>
</roblox>
`)
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

func noteForStartFullscreen(enabled bool) string {
	if enabled {
		return "The next Roblox window will ask the desktop to start fullscreen."
	}
	return "The next Roblox window will start windowed."
}

func noteForDiscord(s Settings) string {
	if !s.DiscordRichPresence {
		return "Discord Rich Presence is off. The change applies while Roblox is running."
	}
	if s.DiscordJoinButton {
		return "Discord Rich Presence will show a public Roblox Join button to friends. Discord does not show it on your own status. The change applies while Roblox is running."
	}
	return "Discord Rich Presence updates while Roblox is running."
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
	if err := decodePersisted(data, &got); err != nil {
		return s.recoverMalformed(ctx, path)
	}
	got.Settings = normalized(got.Settings)
	if err := got.Settings.validateShape(); err != nil {
		return s.recoverMalformed(ctx, path)
	}
	if got.FPSOwned && (got.FPSOriginal == "" || got.FPSApplied == "") {
		return s.recoverMalformed(ctx, path)
	}
	if err := ValidateFastFlags(got.FastFlags); err != nil {
		return s.recoverMalformed(ctx, path)
	}
	return got, nil
}

type persistedWire struct {
	Renderer            Renderer   `json:"renderer"`
	FrameRate           FrameRate  `json:"frameRate"`
	VSync               bool       `json:"vsync"`
	LowTextureMode      bool       `json:"lowTextureMode"`
	Display             string     `json:"display,omitempty"`
	StartFullscreen     bool       `json:"startFullscreen"`
	DiscordRichPresence *bool      `json:"discordRichPresence"`
	DiscordJoinButton   bool       `json:"discordJoinButton"`
	FastFlags           []FastFlag `json:"fastFlags"`
	FPSOwned            bool       `json:"fpsOwned,omitempty"`
	FPSOriginal         string     `json:"fpsOriginal,omitempty"`
	FPSApplied          string     `json:"fpsApplied,omitempty"`
}

type persistedWriteWire struct {
	Renderer            Renderer   `json:"renderer"`
	FrameRate           FrameRate  `json:"frameRate"`
	VSync               bool       `json:"vsync"`
	LowTextureMode      bool       `json:"lowTextureMode"`
	Display             string     `json:"display,omitempty"`
	StartFullscreen     bool       `json:"startFullscreen"`
	DiscordRichPresence bool       `json:"discordRichPresence"`
	DiscordJoinButton   bool       `json:"discordJoinButton"`
	FastFlags           []FastFlag `json:"fastFlags,omitempty"`
	FPSOwned            bool       `json:"fpsOwned,omitempty"`
	FPSOriginal         string     `json:"fpsOriginal,omitempty"`
	FPSApplied          string     `json:"fpsApplied,omitempty"`
}

var persistedSettingKeys = map[string]struct{}{
	"renderer": {}, "frameRate": {}, "vsync": {}, "lowTextureMode": {},
	"display": {}, "startFullscreen": {}, "discordRichPresence": {},
	"discordJoinButton": {}, "fastFlags": {}, "fpsOwned": {},
	"fpsOriginal": {}, "fpsApplied": {},
}

func decodePersisted(data []byte, got *persistedSettings) error {
	if got == nil {
		return fmt.Errorf("persisted settings destination is nil")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("persisted settings must be a JSON object")
	}
	var wire persistedWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	unknown := make(map[string]json.RawMessage)
	for key, value := range raw {
		if _, known := persistedSettingKeys[key]; !known {
			unknown[key] = append(json.RawMessage(nil), value...)
		}
	}
	presence := false
	if wire.DiscordRichPresence != nil {
		presence = *wire.DiscordRichPresence
	}
	*got = persistedSettings{
		Settings: Settings{
			Renderer:            wire.Renderer,
			FrameRate:           wire.FrameRate,
			VSync:               wire.VSync,
			LowTextureMode:      wire.LowTextureMode,
			Display:             wire.Display,
			StartFullscreen:     wire.StartFullscreen,
			DiscordRichPresence: presence,
			DiscordJoinButton:   wire.DiscordJoinButton,
		},
		FastFlags:   cloneFastFlags(wire.FastFlags),
		FPSOwned:    wire.FPSOwned,
		FPSOriginal: wire.FPSOriginal,
		FPSApplied:  wire.FPSApplied,
		unknown:     unknown,
	}
	return nil
}

// MarshalJSON preserves top-level fields that this version does not model so
// a narrow settings or Fast Flag save does not discard a future owner's data.
func (doc persistedSettings) MarshalJSON() ([]byte, error) {
	known, err := json.Marshal(persistedWriteWire{
		Renderer:            doc.Renderer,
		FrameRate:           doc.FrameRate,
		VSync:               doc.VSync,
		LowTextureMode:      doc.LowTextureMode,
		Display:             doc.Display,
		StartFullscreen:     doc.StartFullscreen,
		DiscordRichPresence: doc.DiscordRichPresence,
		DiscordJoinButton:   doc.DiscordJoinButton,
		FastFlags:           doc.FastFlags,
		FPSOwned:            doc.FPSOwned,
		FPSOriginal:         doc.FPSOriginal,
		FPSApplied:          doc.FPSApplied,
	})
	if err != nil {
		return nil, err
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(known, &encoded); err != nil {
		return nil, err
	}
	result := make(map[string]json.RawMessage, len(doc.unknown)+len(encoded))
	for key, value := range doc.unknown {
		result[key] = append(json.RawMessage(nil), value...)
	}
	for key := range persistedSettingKeys {
		delete(result, key)
	}
	for key, value := range encoded {
		result[key] = value
	}
	return json.Marshal(result)
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

// xmlParseError marks bytes the XML decoder could not parse at all: an empty
// or whitespace-only file left by an unclean shutdown, or a document cut
// mid-tag. It is distinct from a parseable document whose frame-rate field is
// absent or invalid, which must be left in place.
type xmlParseError struct{ err error }

func (e xmlParseError) Error() string { return e.err.Error() }
func (e xmlParseError) Unwrap() error { return e.err }

func updateFramerateCap(data []byte, replacement string) ([]byte, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, "", xmlParseError{io.ErrUnexpectedEOF}
	}
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
			return nil, "", xmlParseError{err}
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
