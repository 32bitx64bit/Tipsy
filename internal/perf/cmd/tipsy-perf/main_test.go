package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseMetricsKeepsNativeAndGoMeasurements(t *testing.T) {
	metrics := parseMetrics("BenchmarkNative/Short-1  1000000  73.23 ns/op  73.19 native-ns/op  0 B/op  0 allocs/op\n")
	if len(metrics) != 1 {
		t.Fatalf("metrics=%#v", metrics)
	}
	m := metrics[0]
	if m.Name != "BenchmarkNative/Short" || m.Iterations != 1_000_000 || m.NsPerOp == nil || *m.NsPerOp != 73.23 || m.NativeNsPerOp == nil || *m.NativeNsPerOp != 73.19 || m.BytesPerOp == nil || *m.BytesPerOp != 0 || m.AllocsPerOp == nil || *m.AllocsPerOp != 0 {
		t.Fatalf("metric=%#v", m)
	}
}

func TestParseMetricsOrdinaryGoBenchmark(t *testing.T) {
	metrics := parseMetrics("BenchmarkPure-1  1000000  5.50 ns/op  16 B/op  1 allocs/op\n")
	if len(metrics) != 1 || metrics[0].NativeNsPerOp != nil {
		t.Fatalf("metrics=%#v", metrics)
	}
}

func TestParseMetricsKeepsReadyPumpEmptyWatchAggregate(t *testing.T) {
	metrics := parseMetrics("BenchmarkControllerIdleReadiness-1  16  75000000 ns/op  1.000 initial-rescans/op  0 hotplug-rescans/op  0 recovery-rescans/op  0 evdev-ready/op  0 inotify-ready/op  1.000 shutdown-wakes/op  0 frame-handoffs/op  0 ready-to-frame-ns/op  320 B/op  4 allocs/op\n")
	if len(metrics) != 1 || metrics[0].ControllerIdleReadinessPer == nil {
		t.Fatalf("metrics=%#v", metrics)
	}
	got := metrics[0].ControllerIdleReadinessPer
	if got.InitialRescans == nil || *got.InitialRescans != 1 || got.ShutdownWakes == nil || *got.ShutdownWakes != 1 || got.HotplugRescans == nil || *got.HotplugRescans != 0 || got.RecoveryRescans == nil || *got.RecoveryRescans != 0 || got.EvdevReady == nil || *got.EvdevReady != 0 || got.InotifyReady == nil || *got.InotifyReady != 0 || got.FrameHandoffs == nil || *got.FrameHandoffs != 0 || got.ReadyToFrameNS == nil || *got.ReadyToFrameNS != 0 {
		t.Fatalf("readiness=%#v", got)
	}
}

func TestParseMetricsKeepsAudioFixtureAggregates(t *testing.T) {
	metrics := parseMetrics("BenchmarkOpenSLQueueOwnership-1  20  100 ns/op  2.000 audio-queue-capacity/op  2.000 audio-queue-resident-nodes/op  6.000 audio-queue-copy-operations/op  11520 audio-queue-copy-bytes/op  1.000 audio-queue-caller-copy-preserved/op  8 B/op  1 allocs/op\nBenchmarkMutedCaptureCadence-1  2  70000000 ns/op  8.000 audio-muted-callbacks/op  10000000 audio-muted-scheduled-interval-ns/op  7.000 audio-muted-deadline-waits/op  0 audio-muted-missed-deadline-clamps/op  64 B/op  1 allocs/op\n")
	if len(metrics) != 2 {
		t.Fatalf("metrics=%#v", metrics)
	}
	cadence, queue := metrics[0], metrics[1]
	if cadence.Name != "BenchmarkMutedCaptureCadence" || fixtureMetricValue(cadence.MutedCaptureCadencePer, "audio-muted-callbacks") != 8 || fixtureMetricValue(cadence.MutedCaptureCadencePer, "audio-muted-scheduled-interval-ns") != 10_000_000 || fixtureMetricValue(cadence.MutedCaptureCadencePer, "audio-muted-missed-deadline-clamps") != 0 {
		t.Fatalf("cadence=%#v", cadence)
	}
	if queue.Name != "BenchmarkOpenSLQueueOwnership" || fixtureMetricValue(queue.AudioQueueOwnershipPer, "audio-queue-capacity") != 2 || fixtureMetricValue(queue.AudioQueueOwnershipPer, "audio-queue-copy-operations") != 6 || fixtureMetricValue(queue.AudioQueueOwnershipPer, "audio-queue-caller-copy-preserved") != 1 {
		t.Fatalf("queue=%#v", queue)
	}
}

func fixtureMetricValue(metrics []fixtureMetric, name string) float64 {
	for _, metric := range metrics {
		if metric.Name == name {
			return metric.Value
		}
	}
	return -1
}

func TestUsageFromRusageKeepsContextSwitchesDistinctFromWakeups(t *testing.T) {
	got := usageFromRusage(&syscall.Rusage{
		Utime:  syscall.Timeval{Sec: 2, Usec: 500},
		Stime:  syscall.Timeval{Sec: 1, Usec: 250},
		Maxrss: 123,
		Nvcsw:  7,
		Nivcsw: 3,
	})
	if got == nil || got.Scope != "direct-benchmark-test-binary-rusage" || got.UserCPUNanoseconds != 2_000_500_000 || got.SystemCPUNanoseconds != 1_000_250_000 || got.VoluntaryContextSwitches != 7 || got.InvoluntaryContextSwitches != 3 {
		t.Fatalf("usage = %#v", got)
	}
	if runtime.GOOS == "linux" && got.MaxRSSBytes != 123*1024 {
		t.Fatalf("linux max RSS = %d, want %d", got.MaxRSSBytes, 123*1024)
	}
}

func TestCleanControlsRequireTheirOwnEvidenceLane(t *testing.T) {
	valid := options{
		mode:             "clean-control",
		controls:         []string{"cli-version", "gui-help"},
		controlsOnly:     true,
		warmups:          0,
		samples:          1,
		benchTime:        time.Second.String(),
		profileBenchTime: time.Second.String(),
	}
	if err := validateOptions(&valid); err != nil {
		t.Fatalf("clean-control validation: %v", err)
	}
	invalid := valid
	invalid.controlsOnly = false
	if err := validateOptions(&invalid); err == nil {
		t.Fatal("mixed clean-control and diagnostic benchmark execution was accepted")
	}
	invalid = valid
	invalid.mode = "diagnostic"
	if err := validateOptions(&invalid); err == nil {
		t.Fatal("diagnostic mode with clean controls was accepted")
	}
}

func TestCleanControlEnvironmentUsesPrivateHomeAndXDG(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	env := strings.Join(cleanControlEnv(root), "\n")
	for _, want := range []string{
		"HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"GOMAXPROCS=1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("clean environment missing %q: %s", want, env)
		}
	}
}

func TestControlUsageHasSeparateRusageScope(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("true: %v", err)
	}
	usage := controlUsage(cmd.ProcessState)
	if usage == nil || usage.Scope != "direct-clean-control-process-rusage" {
		t.Fatalf("control usage = %#v", usage)
	}
}

func TestCaptureRunnerBuildIncludesDirectBinaryDigest(t *testing.T) {
	got := captureRunnerBuild()
	if got.Executable == "" || got.Bytes <= 0 || len(got.SHA256) != 64 || got.GoVersion == "" {
		t.Fatalf("runner build = %#v", got)
	}
}
