// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package logging

import (
	"regexp"
	"sync"
)

const redacted = "[REDACTED]"

// Patterns keep a leading capture so JSON quotes and key prefixes survive.
// They stay uncompiled until the first line that needs redaction; startup and
// hot diagnostic-only paths never pay the compilation cost.
var redactPatterns = []struct {
	pattern string
	repl    string
}{
	// JSON string values for known secret keys.
	{
		pattern: `(?i)("(?:password|passwd|pwd|pass|cookie|cookies|authorization|access[_-]?token|refresh[_-]?token|id[_-]?token|oauth[_-]?token|\.ROBLOSECURITY)"\s*:\s*")[^"]*(")`,
		repl:    `${1}` + redacted + `${2}`,
	},
	// .ROBLOSECURITY=value or .ROBLOSECURITY: value
	{
		pattern: `(?i)(\.ROBLOSECURITY\s*[=:]\s*)[^\s"'&,;]+`,
		repl:    `${1}` + redacted,
	},
	// Cookie / Set-Cookie headers (colon form; JSON keys are quoted and skipped).
	{
		pattern: `(?i)(\b(?:set-)?cookie\s*:\s*)[^\r\n]+`,
		repl:    `${1}` + redacted,
	},
	// Authorization: Bearer … / Authorization: …
	{
		pattern: `(?i)(authorization\s*[=:]\s*)[^\s,;]+(?:\s+[^\s,;]+)?`,
		repl:    `${1}` + redacted,
	},
	// Standalone Bearer tokens.
	{
		pattern: `(?i)(\bbearer\s+)[A-Za-z0-9._\-+/=]+`,
		repl:    `${1}` + redacted,
	},
	// OAuth / refresh / access tokens as key=value.
	{
		pattern: `(?i)(\b(?:access[_-]?token|refresh[_-]?token|id[_-]?token|oauth[_-]?token)\s*[=:]\s*)[^\s"'&,;]+`,
		repl:    `${1}` + redacted,
	},
	// Passwords as key=value. Require = or : so "password policy" is kept.
	{
		pattern: `(?i)(\b(?:password|passwd|pwd|pass)\s*[=:]\s*)[^\s"'&,;]+`,
		repl:    `${1}` + redacted,
	},
	// Website protocol-handler authentication tickets.
	{
		pattern: `(?i)(gameinfo:)[^+\s]+`,
		repl:    `${1}` + redacted,
	},
	{
		pattern: `(?i)(rbx-authentication-ticket\s*[:=]\s*)[^\s,;]+`,
		repl:    `${1}` + redacted,
	},
}

type redactRule struct {
	re   *regexp.Regexp
	repl string
}

// compileRedactRules runs once, on the first line that passes the needle
// prefilter.
var compileRedactRules = sync.OnceValue(func() []redactRule {
	rules := make([]redactRule, len(redactPatterns))
	for i, p := range redactPatterns {
		rules[i] = redactRule{re: regexp.MustCompile(p.pattern), repl: p.repl}
	}
	return rules
})

// ASCII-insensitive needles that must catch every secret form. "pwd" is extra
// so the password regex cannot be skipped. Fail-closed: if a protected line
// might not contain one of these, do not skip the regexes.
var redactNeedles = []string{
	"pass",
	"pwd",
	"cookie",
	"token",
	"authorization",
	"bearer",
	"roblosecurity",
	"gameinfo:",
	"rbx-authentication",
}

func asciiFold(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func asciiContainsFold(s, sub string) bool {
	n, m := len(s), len(sub)
	if m == 0 {
		return true
	}
	if m > n {
		return false
	}
	for i := 0; i+m <= n; i++ {
		ok := true
		for j := 0; j < m; j++ {
			if asciiFold(s[i+j]) != asciiFold(sub[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func mayContainSecret(s string) bool {
	for _, n := range redactNeedles {
		if asciiContainsFold(s, n) {
			return true
		}
	}
	return false
}

// Redact strips cookies, tokens, and passwords.
func Redact(s string) string {
	if s == "" || !mayContainSecret(s) {
		return s
	}
	for _, p := range compileRedactRules() {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
