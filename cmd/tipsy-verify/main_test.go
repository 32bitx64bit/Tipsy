package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNoEmbeddedRootIsDevelopmentUnrestricted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "DevelopmentUnrestricted") || !strings.Contains(stderr.String(), "release_bootstrap_missing") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "tipsy-verify 0.1.0\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
