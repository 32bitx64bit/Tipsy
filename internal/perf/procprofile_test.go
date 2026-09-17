package perf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartFromEnvIsNoopWhenUnset(t *testing.T) {
	t.Setenv(EnvProfileDir, "")
	stop, err := StartFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

func TestStartProcessProfileWritesCPUAndHeap(t *testing.T) {
	dir := t.TempDir()
	stop, err := StartProcessProfile(dir, ProfileOptions{CPU: true, Heap: true, Allocs: true, Goroutine: true})
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(200 * time.Millisecond)
	sink := 0
	for time.Now().Before(end) {
		sink += time.Now().Nanosecond()
	}
	if sink == 0 {
		t.Fatal("cpu burn produced no work")
	}
	stop()
	for _, name := range []string{"cpu.pprof", "heap.pprof", "alloc.pprof", "goroutine.pprof"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mutex.pprof")); !os.IsNotExist(err) {
		t.Fatalf("mutex profile written without contention option: %v", err)
	}
	summary := SummarizeProfile(filepath.Join(dir, "cpu.pprof"), 10)
	if summary.Error != "" && !strings.Contains(summary.Error, "no function rows") {
		t.Fatalf("summarize cpu profile: %s", summary.Error)
	}
}

func TestCLISurfaceRequested(t *testing.T) {
	t.Setenv(EnvCLISurface, "")
	if CLISurfaceRequested() {
		t.Fatal("CLI surface default on")
	}
	t.Setenv(EnvCLISurface, "1")
	if !CLISurfaceRequested() {
		t.Fatal("expected CLI surface")
	}
}

func TestProfileOptionsFromEnvEnableContention(t *testing.T) {
	t.Setenv(EnvProfileContention, "1")
	if !ProfileOptionsFromEnv().Contention {
		t.Fatal("expected contention profiles")
	}
	t.Setenv(EnvProfileContention, "")
	if ProfileOptionsFromEnv().Contention {
		t.Fatal("contention profiles default on")
	}
}
