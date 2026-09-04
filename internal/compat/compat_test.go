package compat

import (
	"context"
	"testing"
)

func TestBuildNoPaths(t *testing.T) {
	t.Parallel()
	_, err := Build(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for empty paths")
	}
}

func TestBuildCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Build(ctx, []string{"/no/such.apk"})
	if err == nil {
		t.Fatal("expected canceled context error")
	}
}
