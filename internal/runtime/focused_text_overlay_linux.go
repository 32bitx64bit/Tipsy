// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

const (
	// focusedTextOverlayPollInterval is the retired always-on launch-loop
	// period (62 Hz). The loop now uses a nil channel while no textbox is
	// focused and focusedTextOverlayRepaint (4 Hz) only while one is.
	focusedTextOverlayPollInterval = 16 * time.Millisecond
	focusedTextOverlayRepaint      = 250 * time.Millisecond
	rbxFallbackFontRatio           = float32(0.795)
	rbxBoldFallbackLetterSpacing   = float32(0.04)
)

// focusedTextOverlayWake is a launch-loop ticker that is nil while the
// overlay is inactive and ticks at focusedTextOverlayRepaint while focused.
type focusedTextOverlayWake struct {
	ticker *time.Ticker
}

func (o *focusedTextOverlayWake) C() <-chan time.Time {
	if o == nil || o.ticker == nil {
		return nil
	}
	return o.ticker.C
}

func (o *focusedTextOverlayWake) sync(active bool) {
	if o == nil {
		return
	}
	if active {
		if o.ticker == nil {
			o.ticker = time.NewTicker(focusedTextOverlayRepaint)
		}
		return
	}
	o.stop()
}

func (o *focusedTextOverlayWake) stop() {
	if o == nil || o.ticker == nil {
		return
	}
	o.ticker.Stop()
	o.ticker = nil
}

type rbxFontMappingRecord struct {
	Enum             int32   `json:"enum"`
	Font             string  `json:"font"`
	FromRbxFontRatio float32 `json:"fromRbxFontRatio"`
}

type focusedTextFontContract struct {
	file            string
	ratio           float32
	letterSpacingEm float32
}

// focusedTextFontResolver mirrors RbxKeyboard's APK asset lookup. Its map and
// paths contain style metadata only; editor content never enters it.
type focusedTextFontResolver struct {
	assetsDir string
	mapped    map[int32]focusedTextFontContract
}

func newFocusedTextFontResolver(assetsDir string) focusedTextFontResolver {
	r := focusedTextFontResolver{assetsDir: assetsDir, mapped: make(map[int32]focusedTextFontContract)}
	raw, err := os.ReadFile(filepath.Join(assetsDir, "android", "fonts", "font-mappings.json"))
	if err != nil {
		return r
	}
	var records []rbxFontMappingRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return r
	}
	for _, record := range records {
		// Font names are untrusted package data at this boundary. The official
		// helper only resolves a leaf below content/fonts; never let a malformed
		// mapping escape that asset directory.
		if record.Font == "" || filepath.Base(record.Font) != record.Font ||
			!finitePositiveFloat32(record.FromRbxFontRatio) {
			continue
		}
		fontFile := filepath.Join(assetsDir, "content", "fonts", record.Font)
		info, err := os.Lstat(fontFile)
		if err != nil || !info.Mode().IsRegular() {
			// RbxKeyboard uses the mapped contract only when Typeface loading
			// succeeds. A missing/non-file asset is therefore a fallback, not a
			// request for a similarly named host font.
			continue
		}
		r.mapped[record.Enum] = focusedTextFontContract{
			file:  fontFile,
			ratio: record.FromRbxFontRatio,
		}
	}
	return r
}

func (r focusedTextFontResolver) resolve(font int32) focusedTextFontContract {
	if contract, ok := r.mapped[font]; ok {
		return contract
	}
	name := "SourceSansPro-Regular.ttf"
	spacing := float32(0)
	switch font {
	case 4:
		name = "SourceSansPro-Bold.ttf"
		spacing = rbxBoldFallbackLetterSpacing
	case 5:
		name = "SourceSansPro-Light.ttf"
	}
	return focusedTextFontContract{
		file:            filepath.Join(r.assetsDir, "fonts", name),
		ratio:           rbxFallbackFontRatio,
		letterSpacingEm: spacing,
	}
}

func finitePositiveFloat32(value float32) bool {
	f := float64(value)
	return !math.IsNaN(f) && !math.IsInf(f, 0) && value > 0
}

type focusedTextOverlaySink interface {
	Update(x11.FocusedTextSnapshot) error
}

// focusedTextOverlaySync stores only content-free invalidation state. The
// source's sensitive snapshot is requested solely when it will be painted and
// falls out of scope immediately after sink.Update returns.
type focusedTextOverlaySync struct {
	sink       focusedTextOverlaySink
	version    func() uint64
	snapshot   func() jni.RbxTextOverlaySnapshot
	seen       uint64
	active     bool
	lastRender time.Time
	fonts      focusedTextFontResolver
}

func newFocusedTextOverlaySync(sink focusedTextOverlaySink, assetsDir string) *focusedTextOverlaySync {
	return &focusedTextOverlaySync{
		sink:     sink,
		version:  jni.RbxTextOverlayVersion,
		snapshot: jni.CurrentRbxTextOverlay,
		fonts:    newFocusedTextFontResolver(assetsDir),
	}
}

func (s *focusedTextOverlaySync) refresh(now time.Time) (bool, error) {
	if s == nil || s.sink == nil || s.version == nil || s.snapshot == nil {
		return false, nil
	}
	// The engine->Java property callback has already returned before Runtime
	// reaches this loop. Complete the APK's UI-thread half now: call the named
	// nativeGetTextBoxInfo export on dedicated Main, then consume the resulting
	// version. JNI coalesces requests and rejects stale focus sessions.
	jni.RefreshRbxTextOverlayInfo()
	version := s.version()
	periodic := s.active && (s.lastRender.IsZero() || now.Sub(s.lastRender) >= focusedTextOverlayRepaint)
	if version == s.seen && !periodic {
		return false, nil
	}
	snapshot := s.snapshot()
	text := snapshot.Text
	if !snapshot.Active {
		// The JNI contract already guarantees this; keep the compositor's
		// inactive boundary fail-closed if a future source regresses.
		text = ""
	}
	font := s.fonts.resolve(snapshot.Font)
	fontSizePx := snapshot.FontSize * snapshot.Density * font.ratio
	frame := x11.FocusedTextSnapshot{
		Version:            snapshot.Version,
		Active:             snapshot.Active,
		Configured:         snapshot.Configured,
		Editable:           snapshot.Editable,
		Multiline:          snapshot.Multiline,
		TextWrapped:        snapshot.TextWrapped,
		Text:               text,
		CursorUTF16:        snapshot.SelectionEndUTF16,
		Density:            snapshot.Density,
		X:                  snapshot.X,
		Y:                  snapshot.Y,
		Width:              snapshot.Width,
		Height:             snapshot.Height,
		FontSize:           fontSizePx,
		FontFile:           font.file,
		LetterSpacing:      font.letterSpacingEm,
		TextColor:          snapshot.TextColor,
		Font:               snapshot.Font,
		TextInputType:      snapshot.TextInputType,
		XAlignment:         snapshot.XAlignment,
		YAlignment:         snapshot.YAlignment,
		ReturnKeyType:      snapshot.ReturnKeyType,
		PaddingLeft:        snapshot.PaddingLeftPx,
		PaddingTop:         snapshot.PaddingTopPx,
		PaddingRight:       snapshot.PaddingRightPx,
		PaddingBottom:      snapshot.PaddingBottomPx,
		CursorVisible:      snapshot.CursorVisible,
		IncludeFontPadding: snapshot.IncludeFontPadding,
	}
	// Consume the snapshot's own generation so a concurrent edit between the
	// cheap version read and snapshot copy is never accidentally skipped.
	s.seen = snapshot.Version
	s.active = snapshot.Active && snapshot.Configured
	s.lastRender = now
	return true, s.sink.Update(frame)
}
