package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/perf"
)

const samplePerfReport = `# Overhead       Samples  Symbol
# ........  ............  ......
    45.00%           450  [.] vkQueuePresentKHR
    12.50%           125  [.] runtime.mallocgc
     5.25%            52  [.] mesa_flush
     0.50%             5  [.] skipped_below_limit
     3.00%            30  [k] __schedule
    2.10%             21  [.] /usr/lib/libvulkan.so.1
`

func TestPerfRecordArgsUsesFramePointersAndUserspace(t *testing.T) {
	got := perfRecordArgs("/tmp/perf.data", perfRecordMode{event: "cycles:u", userCallGraph: true}, "/tmp/tipsy", []string{"launch"})
	want := []string{"record", "-o", "/tmp/perf.data", "-e", "cycles:u", "--call-graph", "fp", "--user-call-graph", "--", "/tmp/tipsy", "launch"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

func TestParsePerfReportStdioKeepsNamesAndPercentsOnly(t *testing.T) {
	got := parsePerfReportStdio(samplePerfReport)
	if len(got) != 4 {
		t.Fatalf("hotspots=%#v", got)
	}
	if got[0].Name != "vkQueuePresentKHR" || got[0].Percent != 45 {
		t.Fatalf("first=%#v", got[0])
	}
	if got[3].Name != "libvulkan.so.1" {
		t.Fatalf("path was not reduced to basename: %#v", got[3])
	}
	for _, spot := range got {
		if spot.Name == "skipped_below_limit" || spot.Name == "__schedule" || strings.Contains(spot.Name, "/") {
			t.Fatalf("kept filtered row: %#v", spot)
		}
	}
}

func TestParsePerfReportStdioDropsDisassemblyAndSecrets(t *testing.T) {
	text := strings.Join([]string{
		"    20.00%  20  [.] honest_symbol",
		"    401234:  48 89 c3  mov %rax,%rbx",
		"    15.00%  15  [.] cookie=.ROBLOSECURITY",
		"    10.00%  10  [.] https://example/?ticket=abc",
	}, "\n")
	got := parsePerfReportStdio(text)
	if len(got) != 1 || got[0].Name != "honest_symbol" {
		t.Fatalf("hotspots=%#v", got)
	}
}

func TestProbeNativeCPUUnavailableWithoutPerf(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	orig := hostPerf
	t.Cleanup(func() { hostPerf = orig })
	hostPerf = defaultPerfHost()
	got := probeNativeCPU(t.TempDir())
	if got.Status != perfUnavailable || !strings.Contains(got.Reason, "not installed") {
		t.Fatalf("probe=%#v", got)
	}
	if got.Hotspots != nil || got.ID != perf.NativeCPUProfileMeasurementID {
		t.Fatalf("unavailable probe leaked hotspots: %#v", got)
	}
}

func TestProbeNativeCPUUnavailableWhenParanoidBlocks(t *testing.T) {
	orig := hostPerf
	t.Cleanup(func() { hostPerf = orig })
	hostPerf = perfHost{
		lookPath: func(string) (string, error) { return "/usr/bin/perf", nil },
		paranoid: func() ([]byte, error) { return []byte("4\n"), nil },
		recordProbe: func(string, perfRecordMode) error {
			t.Fatal("blocked paranoid still probed perf record")
			return nil
		},
	}
	got := probeNativeCPU(t.TempDir())
	if got.Status != perfUnavailable || !strings.Contains(got.Reason, "perf_event_paranoid=4") {
		t.Fatalf("probe=%#v", got)
	}
	if got.Hotspots != nil {
		t.Fatalf("blocked probe faked zeros: %#v", got)
	}
}

func TestSelectPerfRecordModePrefersUserspaceFP(t *testing.T) {
	host := perfHost{
		userCallGraphHelp: func(string) bool { return true },
		recordProbe:       func(string, perfRecordMode) error { return nil },
	}
	mode, err := host.selectMode("/usr/bin/perf")
	if err != nil || mode.event != perfUserspaceEvent || !mode.userCallGraph {
		t.Fatalf("mode=%#v err=%v", mode, err)
	}
}

func TestSelectPerfRecordModeFallsBackWithoutUserCallGraph(t *testing.T) {
	var tried []bool
	host := perfHost{
		userCallGraphHelp: func(string) bool { return true },
		recordProbe: func(_ string, mode perfRecordMode) error {
			tried = append(tried, mode.userCallGraph)
			if mode.userCallGraph {
				return errors.New("perf record does not support --user-call-graph")
			}
			return nil
		},
	}
	mode, err := host.selectMode("/usr/bin/perf")
	if err != nil || mode.userCallGraph || mode.event != perfUserspaceEvent {
		t.Fatalf("mode=%#v err=%v tried=%v", mode, err, tried)
	}
}

func TestClassifyPerfFailureOmitsPayloads(t *testing.T) {
	reason := classifyPerfFailure(errors.New("exit status 1"), "Cookie: abc .ROBLOSECURITY=xyz ticket=1 Permission denied")
	if strings.Contains(reason, "ROBLOSECURITY") || strings.Contains(reason, "Cookie") || strings.Contains(reason, "ticket=1") {
		t.Fatalf("leaked payload: %q", reason)
	}
	if reason != "perf record probe was denied permission" {
		t.Fatalf("reason=%q", reason)
	}
}

func TestFinalizeNativeCPUKeepsUnavailableWithoutZeros(t *testing.T) {
	got := finalizeNativeCPU(nativeCPUCapture{
		ID:     perf.NativeCPUProfileMeasurementID,
		Status: perfUnavailable,
		Reason: "perf is not installed on PATH",
	}, t.TempDir())
	if got.Status != perfUnavailable || got.Hotspots != nil || got.Reason == "" {
		t.Fatalf("finalize=%#v", got)
	}
}

func TestFinalizeNativeCPUReportsMissingData(t *testing.T) {
	dir := t.TempDir()
	got := finalizeNativeCPU(nativeCPUCapture{
		ID:       perf.NativeCPUProfileMeasurementID,
		Status:   perfStatusOK,
		Tool:     "/usr/bin/perf",
		DataPath: filepath.Join(dir, "perf.data"),
	}, dir)
	if got.Status != perfUnavailable || got.Reason != "perf.data was not written" || got.Hotspots != nil {
		t.Fatalf("finalize=%#v", got)
	}
}

func TestCollectPerfReportDoesNotStoreStderr(t *testing.T) {
	orig := hostPerf
	t.Cleanup(func() { hostPerf = orig })
	hostPerf.report = func(ctx context.Context, perfBin, dataPath string, extra []string) (string, error) {
		if strings.Contains(strings.Join(extra, " "), "symbol") {
			return "", errors.New("no sort")
		}
		return samplePerfReport, nil
	}
	text, err := collectPerfReport("/usr/bin/perf", filepath.Join(t.TempDir(), "perf.data"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "vkQueuePresentKHR") {
		t.Fatalf("report text missing symbols: %q", text)
	}
}

func TestWrapPerfRecordKeepsChildArgvAfterSeparator(t *testing.T) {
	bin, args := wrapPerfRecord("/usr/bin/perf", "/tmp/p.data", perfRecordMode{event: "cycles:u", userCallGraph: true}, "/tmp/tipsy", []string{"version"})
	if bin != "/usr/bin/perf" {
		t.Fatalf("bin=%s", bin)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-- /tmp/tipsy version") {
		t.Fatalf("args=%q", joined)
	}
	if strings.Contains(joined, "GOMAXPROCS") {
		t.Fatalf("wrapper pinned GOMAXPROCS: %q", joined)
	}
}

func TestProbeNativeCPUUsesSelectedMode(t *testing.T) {
	orig := hostPerf
	t.Cleanup(func() { hostPerf = orig })
	hostPerf = perfHost{
		lookPath:          func(string) (string, error) { return "/usr/bin/perf", nil },
		paranoid:          func() ([]byte, error) { return []byte("2\n"), nil },
		userCallGraphHelp: func(string) bool { return true },
		recordProbe:       func(string, perfRecordMode) error { return nil },
	}
	got := probeNativeCPU(t.TempDir())
	if got.Status != perfStatusOK || !got.UserCallGraph || got.Event != perfUserspaceEvent || got.CallGraph != "fp" {
		t.Fatalf("probe=%#v", got)
	}
	if _, err := os.Stat(got.DataPath); !os.IsNotExist(err) {
		t.Fatal("probe wrote a live perf.data")
	}
}
