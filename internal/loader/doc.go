// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

// Package loader maps official Android x86-64 ET_DYN objects and applies
// standard plus APS2 packed RELA. It is a compatibility loader, not a
// debugger, injector, or anti-cheat bypass.
package loader
