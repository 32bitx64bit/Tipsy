// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

type Category string

const (
	CatAPK          Category = "apk"
	CatLoader       Category = "loader"
	CatELF          Category = "elf"
	CatAndroid      Category = "android"
	CatJNI          Category = "jni"
	CatGameActivity Category = "gameactivity"
	CatX11          Category = "x11"
	CatGraphics     Category = "graphics"
	CatInput        Category = "input"
	CatAudio        Category = "audio"
	CatNetwork      Category = "network"
	CatAuth         Category = "auth"
	CatFilesystem   Category = "filesystem"
	CatQt           Category = "qt"
	CatRuntime      Category = "runtime"
)

const categoryKey = "category"

var knownCategories = map[Category]struct{}{
	CatAPK: {}, CatLoader: {}, CatELF: {}, CatAndroid: {}, CatJNI: {},
	CatGameActivity: {}, CatX11: {}, CatGraphics: {}, CatInput: {},
	CatAudio: {}, CatNetwork: {}, CatAuth: {}, CatFilesystem: {},
	CatQt: {}, CatRuntime: {},
}

var initMu sync.Mutex

var debugEnabled atomic.Bool
var lastDebugDefault atomic.Pointer[slog.Logger]
var debugListener atomic.Value // func(bool)

// DebugEnabled reports whether Android/slog Debug records are accepted.
// Updated from Init and when Logger sees a replaced slog default.
func DebugEnabled() bool {
	return debugEnabled.Load()
}

// SetDebugChangeListener is invoked whenever DebugEnabled changes and once
// with the current value. The android package uses this to publish a C-visible
// flag so liblog can skip VERBOSE/DEBUG without a Go call.
func SetDebugChangeListener(fn func(bool)) {
	debugListener.Store(fn)
	if fn != nil {
		fn(debugEnabled.Load())
	}
}

// RefreshDebugEnabled recomputes DebugEnabled from the current slog default's
// Android-category Debug gate. Tests that replace slog.Default() should call
// this (or Logger) so the C skip flag stays aligned.
func RefreshDebugEnabled() {
	refreshDebugEnabled()
}

func refreshDebugEnabled() {
	def := slog.Default()
	enabled := def.With(categoryKey, string(CatAndroid)).Enabled(context.Background(), slog.LevelDebug)
	lastDebugDefault.Store(def)
	prev := debugEnabled.Swap(enabled)
	if prev == enabled {
		return
	}
	if v := debugListener.Load(); v != nil {
		if fn, ok := v.(func(bool)); ok && fn != nil {
			fn(enabled)
		}
	}
}

// Init reads TIPSY_LOG (comma categories or "all") and TIPSY_LOG_LEVEL.
func Init() {
	initWithWriter(os.Stderr)
}

func initWithWriter(w io.Writer) {
	initMu.Lock()
	defer initMu.Unlock()

	level := parseLevel(os.Getenv("TIPSY_LOG_LEVEL"))
	all, cats := parseCategories(os.Getenv("TIPSY_LOG"))

	inner := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			return redactAttr(a)
		},
	})
	h := &filterHandler{
		inner: inner,
		all:   all,
		cats:  cats,
	}
	slog.SetDefault(slog.New(h))
	refreshDebugEnabled()
}

type cachedLogger struct {
	def *slog.Logger
	log *slog.Logger
}

var loggerCache sync.Map // Category -> *cachedLogger

// Logger returns a slog.Logger tagged with the given category.
// The tagged logger is reused for the current slog default so hot paths
// do not allocate a With() wrapper on every call. Tests that replace
// slog.Default() still get a fresh logger because the default pointer is
// part of the cache key.
func Logger(cat Category) *slog.Logger {
	def := slog.Default()
	if lastDebugDefault.Load() != def {
		refreshDebugEnabled()
	}
	if v, ok := loggerCache.Load(cat); ok {
		c := v.(*cachedLogger)
		if c.def == def {
			return c.log
		}
	}
	l := def.With(categoryKey, string(cat))
	loggerCache.Store(cat, &cachedLogger{def: def, log: l})
	return l
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseCategories(s string) (all bool, cats map[string]struct{}) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "all") {
		return true, nil
	}
	cats = make(map[string]struct{})
	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if part == "all" {
			return true, nil
		}
		if _, ok := knownCategories[Category(part)]; ok {
			cats[part] = struct{}{}
		}
	}
	if len(cats) == 0 {
		return true, nil
	}
	return false, cats
}

type filterHandler struct {
	inner slog.Handler
	all   bool
	cats  map[string]struct{}
	cat   string
}

func (h *filterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if !h.inner.Enabled(ctx, level) {
		return false
	}
	if h.all || h.cat == "" {
		return true
	}
	_, ok := h.cats[h.cat]
	return ok
}

func (h *filterHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Message = Redact(r.Message)
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

func (h *filterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
		if a.Key == categoryKey {
			nh.cat = strings.ToLower(a.Value.String())
		}
	}
	nh.inner = h.inner.WithAttrs(redacted)
	return &nh
}

func (h *filterHandler) WithGroup(name string) slog.Handler {
	nh := *h
	nh.inner = h.inner.WithGroup(name)
	return &nh
}

func redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Redact(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		for i, ga := range group {
			out[i] = redactAttr(ga)
		}
		a.Value = slog.GroupValue(out...)
	}
	return a
}
