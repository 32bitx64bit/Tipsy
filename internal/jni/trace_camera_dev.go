// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && !official_release

package jni

import (
	"fmt"
	"os"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const cameraTraceCompiledIn = true

func cameraTraceLogger() func(string, ...any) {
	if os.Getenv("TIPSY_TRACE_CAMERA") == "1" {
		return func(format string, args ...any) {
			logging.Logger(logging.CatJNI).Info("[jni] camera trace: " + fmt.Sprintf(format, args...))
		}
	}
	return noopCameraTraceLog
}
