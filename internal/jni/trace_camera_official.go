// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && official_release

package jni

const cameraTraceCompiledIn = false

func cameraTraceLogger() func(string, ...any) {
	return noopCameraTraceLog
}
