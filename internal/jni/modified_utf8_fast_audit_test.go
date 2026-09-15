// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func checkAuditMUTF8(t *testing.T, s string) {
	t.Helper()
	units := utf16.Encode([]rune(s))
	wantLen, _ := modifiedUTF8Length(units)
	gotLen, ok := modifiedUTF8StringLength(s)
	if !ok || gotLen != wantLen {
		t.Fatalf("length: %q got %d want %d", s, gotLen, wantLen)
	}
	want, got := make([]byte, wantLen), make([]byte, gotLen)
	encodeModifiedUTF8To(want, units)
	if encodeModifiedUTF8StringTo(got, s) != gotLen || !bytes.Equal(got, want) {
		t.Fatalf("encoding mismatch: %q", s)
	}
}
func TestAuditMUTF8BoundaryCases(t *testing.T) {
	for _, s := range []string{"", "ascii", "a\x00b", "é", "漢字", "😀", "\xff\xfe", "\xed\xa0\x80", "A😀\x00é"} {
		checkAuditMUTF8(t, s)
	}
}
func TestAuditMUTF8EveryUnicodeScalar(t *testing.T) {
	// Chunked strings avoid a million tiny allocations in this exhaustive test.
	var chunk strings.Builder
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		chunk.WriteRune(r)
		if chunk.Len() > 8192 {
			checkAuditMUTF8(t, chunk.String())
			chunk.Reset()
		}
	}
	checkAuditMUTF8(t, chunk.String())
}
func TestAuditSurrogateDetection(t *testing.T) {
	cases := [][]uint16{nil, {0}, {0xd800}, {0xdc00}, {0xd800, 0xdc00}, {0xd800, 0xd800}, {0xdc00, 0xd800}, {0xd800, 0xdc00, 0xdfff}}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 10000; i++ {
		u := make([]uint16, rng.Intn(40))
		for j := range u {
			u[j] = uint16(rng.Uint32())
		}
		cases = append(cases, u)
	}
	for _, u := range cases {
		roundtrip := utf16.Encode(utf16.Decode(u))
		if got, want := hasUnpairedSurrogate(u), !sameUTF16(u, roundtrip); got != want {
			t.Fatalf("%x got %v want %v", u, got, want)
		}
	}
}

var auditMUTF8LengthSink int

func BenchmarkAuditMUTF8DirectLength(b *testing.B) {
	s := strings.Repeat("Roblox 😀 \x00", 64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		auditMUTF8LengthSink, _ = modifiedUTF8StringLength(s)
	}
}
func BenchmarkAuditMUTF8LegacyLength(b *testing.B) {
	s := strings.Repeat("Roblox 😀 \x00", 64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		auditMUTF8LengthSink, _ = modifiedUTF8Length(utf16.Encode([]rune(s)))
	}
}
