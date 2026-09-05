// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package logging

import "regexp"

const redacted = "[REDACTED]"

// Patterns keep a leading capture so JSON quotes and key prefixes survive.
var redactPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// JSON string values for known secret keys.
	{
		re:   regexp.MustCompile(`(?i)("(?:password|passwd|pwd|pass|cookie|cookies|authorization|access[_-]?token|refresh[_-]?token|id[_-]?token|oauth[_-]?token|\.ROBLOSECURITY)"\s*:\s*")[^"]*(")`),
		repl: `${1}` + redacted + `${2}`,
	},
	// .ROBLOSECURITY=value or .ROBLOSECURITY: value
	{
		re:   regexp.MustCompile(`(?i)(\.ROBLOSECURITY\s*[=:]\s*)[^\s"'&,;]+`),
		repl: `${1}` + redacted,
	},
	// Cookie / Set-Cookie headers (colon form; JSON keys are quoted and skipped).
	{
		re:   regexp.MustCompile(`(?i)(\b(?:set-)?cookie\s*:\s*)[^\r\n]+`),
		repl: `${1}` + redacted,
	},
	// Authorization: Bearer … / Authorization: …
	{
		re:   regexp.MustCompile(`(?i)(authorization\s*[=:]\s*)[^\s,;]+(?:\s+[^\s,;]+)?`),
		repl: `${1}` + redacted,
	},
	// Standalone Bearer tokens.
	{
		re:   regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._\-+/=]+`),
		repl: `${1}` + redacted,
	},
	// OAuth / refresh / access tokens as key=value.
	{
		re:   regexp.MustCompile(`(?i)(\b(?:access[_-]?token|refresh[_-]?token|id[_-]?token|oauth[_-]?token)\s*[=:]\s*)[^\s"'&,;]+`),
		repl: `${1}` + redacted,
	},
	// Passwords as key=value. Require = or : so "password policy" is kept.
	{
		re:   regexp.MustCompile(`(?i)(\b(?:password|passwd|pwd|pass)\s*[=:]\s*)[^\s"'&,;]+`),
		repl: `${1}` + redacted,
	},
	// Website protocol-handler authentication tickets.
	{
		re:   regexp.MustCompile(`(?i)(gameinfo:)[^+\s]+`),
		repl: `${1}` + redacted,
	},
	{
		re:   regexp.MustCompile(`(?i)(rbx-authentication-ticket\s*[:=]\s*)[^\s,;]+`),
		repl: `${1}` + redacted,
	},
}

// Redact never leak cookies/tokens/passwords.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, p := range redactPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
