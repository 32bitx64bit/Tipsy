// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		in         string
		notContain string
		contain    string
	}{
		{
			name:       "password equals",
			in:         "login failed password=hunter2 for user",
			notContain: "hunter2",
			contain:    redacted,
		},
		{
			name:       "password colon",
			in:         "password: supersecret",
			notContain: "supersecret",
			contain:    redacted,
		},
		{
			name:       "json password",
			in:         `{"password":"hunter2","user":"x"}`,
			notContain: "hunter2",
			contain:    redacted,
		},
		{
			name:       "roblo security cookie",
			in:         ".ROBLOSECURITY=_|WARNING:-DO-NOT-SHARE-THIS.--abc",
			notContain: "WARNING:-DO-NOT-SHARE-THIS.--abc",
			contain:    redacted,
		},
		{
			name:       "cookie header",
			in:         "Cookie: session=abc; .ROBLOSECURITY=xyz",
			notContain: "xyz",
			contain:    redacted,
		},
		{
			name:       "set-cookie header",
			in:         "Set-Cookie: .ROBLOSECURITY=tok123; Path=/",
			notContain: "tok123",
			contain:    redacted,
		},
		{
			name:       "authorization bearer",
			in:         "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aa",
			notContain: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aa",
			contain:    redacted,
		},
		{
			name:       "authorization header",
			in:         "authorization: Basic dXNlcjpwYXNz",
			notContain: "dXNlcjpwYXNz",
			contain:    redacted,
		},
		{
			name:       "standalone bearer",
			in:         "token Bearer abcdef123456",
			notContain: "abcdef123456",
			contain:    redacted,
		},
		{
			name:       "refresh token",
			in:         "refresh_token=rrrr-secret",
			notContain: "rrrr-secret",
			contain:    redacted,
		},
		{
			name:       "access token",
			in:         "access_token=aaaa-secret",
			notContain: "aaaa-secret",
			contain:    redacted,
		},
		{
			name:       "oauth token",
			in:         "oauth_token=oooo-secret",
			notContain: "oooo-secret",
			contain:    redacted,
		},
		{
			name:       "json refresh token",
			in:         `{"refresh_token":"keep-me-secret"}`,
			notContain: "keep-me-secret",
			contain:    redacted,
		},
		{
			name:       "innocent password word",
			in:         "the password policy is strict",
			notContain: redacted,
			contain:    "the password policy is strict",
		},
		{
			name:       "empty",
			in:         "",
			notContain: redacted,
			contain:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Redact(tt.in)
			if tt.notContain != "" && strings.Contains(got, tt.notContain) {
				t.Fatalf("Redact(%q) leaked %q: %q", tt.in, tt.notContain, got)
			}
			if tt.contain != "" && !strings.Contains(got, tt.contain) {
				t.Fatalf("Redact(%q) = %q, want to contain %q", tt.in, got, tt.contain)
			}
		})
	}
}

func TestLoggerRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("TIPSY_LOG", "all")
	t.Setenv("TIPSY_LOG_LEVEL", "debug")
	initWithWriter(&buf)

	Logger(CatAuth).Info("login", "cookie", ".ROBLOSECURITY=super-secret-cookie")
	out := buf.String()
	if strings.Contains(out, "super-secret-cookie") {
		t.Fatalf("log leaked cookie: %s", out)
	}
	if !strings.Contains(out, redacted) {
		t.Fatalf("expected redacted marker in log: %s", out)
	}
}

func TestCategoryFilter(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("TIPSY_LOG", "apk,elf")
	t.Setenv("TIPSY_LOG_LEVEL", "info")
	initWithWriter(&buf)

	Logger(CatAPK).Info("apk-msg")
	Logger(CatELF).Info("elf-msg")
	Logger(CatJNI).Info("jni-msg")

	out := buf.String()
	if !strings.Contains(out, "apk-msg") {
		t.Fatalf("expected apk log, got %s", out)
	}
	if !strings.Contains(out, "elf-msg") {
		t.Fatalf("expected elf log, got %s", out)
	}
	if strings.Contains(out, "jni-msg") {
		t.Fatalf("jni category should be filtered: %s", out)
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	if parseLevel("debug") != slog.LevelDebug {
		t.Fatal("debug")
	}
	if parseLevel("WARN") != slog.LevelWarn {
		t.Fatal("warn")
	}
	if parseLevel("error") != slog.LevelError {
		t.Fatal("error")
	}
	if parseLevel("") != slog.LevelInfo {
		t.Fatal("default info")
	}
}

func TestParseCategories(t *testing.T) {
	t.Parallel()
	all, cats := parseCategories("")
	if !all || cats != nil {
		t.Fatalf("empty should be all, got all=%v cats=%v", all, cats)
	}
	all, cats = parseCategories("apk, ELF ,jni")
	if all {
		t.Fatal("expected filtered")
	}
	for _, c := range []string{"apk", "elf", "jni"} {
		if _, ok := cats[c]; !ok {
			t.Fatalf("missing %s", c)
		}
	}
}
