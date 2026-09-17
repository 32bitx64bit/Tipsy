package perf

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/app"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

func BenchmarkWholeProgramCLI(b *testing.B) {
	root := b.TempDir()
	b.Setenv("HOME", filepath.Join(root, "home"))
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	b.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	b.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	for _, name := range []string{"home", "config", "data", "cache"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			b.Fatal(err)
		}
	}
	logging.Init()
	commands := WholeProgramCLICommands()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, args := range commands {
			code := app.Run(ctx, args, io.Discard, io.Discard)
			if code > 2 {
				b.Fatalf("tipsy %v exit %d", args, code)
			}
		}
	}
}

func TestWholeProgramCLICommandsRun(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	for _, name := range []string{"home", "config", "data", "cache"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	logging.Init()
	var stdout, stderr bytes.Buffer
	for _, args := range WholeProgramCLICommands() {
		stdout.Reset()
		stderr.Reset()
		code := app.Run(context.Background(), args, &stdout, &stderr)
		if code > 2 {
			t.Fatalf("tipsy %v exit %d stderr=%s", args, code, stderr.String())
		}
	}
}
