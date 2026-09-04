// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !(linux && amd64)

package runtime

import (
	"context"
	"fmt"
)

// LaunchOptions controls tipsy launch.
type LaunchOptions struct {
	Probe  bool
	Width  int
	Height int
}

// Launch is only implemented on Linux x86-64 with cgo.
func Launch(ctx context.Context, opt LaunchOptions) error {
	_ = ctx
	_ = opt
	return fmt.Errorf("launch requires Linux x86-64")
}
