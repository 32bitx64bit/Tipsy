package perf

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCatalogCoversProjectPackages(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate catalog test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	covered := make(map[string]bool)
	ids := make(map[string]bool)
	for _, entry := range Catalog() {
		if ids[entry.ID] {
			t.Fatalf("duplicate catalog ID: %s", entry.ID)
		}
		ids[entry.ID] = true
		covered[entry.Package] = true
		if entry.ID == "" || entry.Subsystem == "" || entry.Kind == "" {
			t.Fatalf("incomplete catalog entry: %#v", entry)
		}
		if entry.Kind == Microbenchmark && entry.Benchmark == "" {
			t.Fatalf("microbenchmark lacks expression: %#v", entry)
		}
		if entry.Kind != Microbenchmark && entry.Reason == "" {
			t.Fatalf("non-benchmarkable entry lacks reason: %#v", entry)
		}
	}
	for _, importPath := range strings.Fields(string(out)) {
		packagePath := "./" + strings.TrimPrefix(importPath, "github.com/tipsy-linux/tipsy/")
		if !covered[packagePath] {
			t.Errorf("go list package has no performance-matrix entry: %s", packagePath)
		}
	}
}

func TestCatalogIncludesBoundedReadyPumpFixture(t *testing.T) {
	for _, entry := range Catalog() {
		if entry.ID != "gamepad-ready-pump" {
			continue
		}
		if entry.Kind != Microbenchmark || entry.Package != "./internal/gamepad" || entry.RunnerPackage != "./internal/perf" || entry.Benchmark != "^BenchmarkControllerIdleReadiness$" {
			t.Fatalf("ready-pump entry = %#v", entry)
		}
		return
	}
	t.Fatal("missing gamepad-ready-pump catalog entry")
}

func TestCatalogIncludesAudioFixtures(t *testing.T) {
	want := map[string]string{
		"audio-opensl-queue":  "^BenchmarkOpenSLQueueOwnership$",
		"audio-muted-cadence": "^BenchmarkMutedCaptureCadence$",
	}
	for _, entry := range Catalog() {
		benchmark, ok := want[entry.ID]
		if !ok {
			continue
		}
		if entry.Kind != Microbenchmark || entry.Package != "./internal/android" || entry.RunnerPackage != "./internal/perf" || entry.Benchmark != benchmark {
			t.Errorf("audio entry = %#v", entry)
		}
		delete(want, entry.ID)
	}
	for id := range want {
		t.Errorf("missing audio fixture entry %q", id)
	}
}

func TestCatalogIncludesWholeProgramEntries(t *testing.T) {
	got := map[string]Entry{}
	for _, entry := range Catalog() {
		got[entry.ID] = entry
	}
	cli := got["whole-program-cli"]
	if cli.Kind != Microbenchmark || cli.Package != "./internal/app" || cli.RunnerPackage != "./internal/perf" || cli.Benchmark != "^BenchmarkWholeProgramCLI$" {
		t.Fatalf("whole-program-cli = %#v", cli)
	}
	program := got["whole-program"]
	if program.Kind != ProgramProfile || program.Package != "./cmd/tipsy" || program.Reason == "" || program.Benchmark != "" {
		t.Fatalf("whole-program = %#v", program)
	}
}

func TestCatalogKeepsJNISyntheticBoundaryExplicit(t *testing.T) {
	for _, entry := range Catalog() {
		if entry.ID != "jni" {
			continue
		}
		if entry.Kind != Microbenchmark || entry.Package != "./internal/jni" || !strings.Contains(entry.Reason, "global references are setup/teardown outside timed loops") || !strings.Contains(entry.Reason, "excludes live string/reference lifetime, retained bytes, call frequency") {
			t.Fatalf("jni entry = %#v", entry)
		}
		return
	}
	t.Fatal("missing jni catalog entry")
}
