// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package authfiles holds the plain data a verified generation hands to the
// launcher. It has no dependencies so the setup service can produce it without
// importing the runtime package that consumes it.
package authfiles

// Files is the same-generation, authenticated file view for Android assets and
// launch metadata. Paths are usable only while the owning authorized
// generation remains open.
type Files struct {
	GenerationID string
	RootDir      string
	AssetsDir    string
	BaseAPKPath  string
	VersionName  string
}
