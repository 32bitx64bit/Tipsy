// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package config

func acquireConfigLock() (func(), error) { return func() {}, nil }
