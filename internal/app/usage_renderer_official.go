// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package app

// launchTestRendererHelp is empty in official_release builds: the
// TIPSY_TEST_RENDERER visual-test hook is not compiled in.
const launchTestRendererHelp = ""
