// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !amd64

package runtime

func desktopAppPolicyOverride(string) (string, error) { return "", nil }
