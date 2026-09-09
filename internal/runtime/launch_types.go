// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

type LaunchOptions struct {
	Probe bool
	// StartFullscreen asks the host X11 window manager for standard EWMH
	// fullscreen immediately after the window maps. It is a Tipsy-owned launch
	// policy, not a guessed Roblox Android preference.
	StartFullscreen      bool
	Width                int
	Height               int
	Started              func()
	Request              rbxuri.Request
	AuthorizedGeneration AuthorizedGeneration
}

// AuthorizedRuntimeFiles is the same-generation, already authenticated file
// view used by Android assets and launch metadata. Paths are usable only while
// the AuthorizedGeneration owner remains open.
type AuthorizedRuntimeFiles struct {
	GenerationID string
	RootDir      string
	AssetsDir    string
	BaseAPKPath  string
	VersionName  string
}

// AuthorizedGeneration is the narrow launch-side view implemented by
// setupsvc.AuthorizedGeneration. Keeping the interface here avoids a package
// cycle: setupsvc owns installation and currently depends on runtime setup
// helpers. Official callers must keep the generation open until Launch returns.
type AuthorizedGeneration interface {
	NativeDescriptorSet(context.Context) (*integrity.NativeDescriptorSet, error)
	AuthorizedRuntimeFiles(context.Context) (AuthorizedRuntimeFiles, error)
}

var ErrAuthorizedGenerationRequired = errors.New("runtime: authenticated runtime generation is required")

func authorizedNativeDescriptorSet(ctx context.Context, generation AuthorizedGeneration) (*integrity.NativeDescriptorSet, error) {
	if generation == nil {
		return nil, ErrAuthorizedGenerationRequired
	}
	set, err := generation.NativeDescriptorSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime: authenticated runtime generation: %w", err)
	}
	if set == nil {
		return nil, fmt.Errorf("runtime: authenticated runtime generation has no native descriptor set")
	}
	if err := set.Validate(); err != nil {
		return nil, fmt.Errorf("runtime: authenticated runtime generation descriptor set: %w", err)
	}
	return set, nil
}

func authorizedRuntimeFiles(ctx context.Context, generation AuthorizedGeneration, generationID string) (AuthorizedRuntimeFiles, error) {
	if generation == nil {
		return AuthorizedRuntimeFiles{}, ErrAuthorizedGenerationRequired
	}
	files, err := generation.AuthorizedRuntimeFiles(ctx)
	if err != nil {
		return AuthorizedRuntimeFiles{}, fmt.Errorf("runtime: authenticated runtime files: %w", err)
	}
	if files.GenerationID == "" || files.GenerationID != generationID || files.RootDir == "" ||
		files.AssetsDir == "" || files.BaseAPKPath == "" || strings.TrimSpace(files.VersionName) == "" {
		return AuthorizedRuntimeFiles{}, fmt.Errorf("runtime: authenticated runtime files are incomplete or from a different generation")
	}
	if !filepath.IsAbs(files.RootDir) || files.AssetsDir != filepath.Join(files.RootDir, "assets") ||
		files.BaseAPKPath != filepath.Join(files.RootDir, "apk", "base.apk") {
		return AuthorizedRuntimeFiles{}, fmt.Errorf("runtime: authenticated runtime file layout is not canonical")
	}
	return files, nil
}
