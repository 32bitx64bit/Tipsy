// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

// Package android provides in-process symbol resolvers for official Android
// x86-64 DT_NEEDED sonames (libc, libandroid, libEGL, libvulkan, …). It is not an emulator.
package android
