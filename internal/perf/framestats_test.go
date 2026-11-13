package perf

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
)

// stampsNS builds n+1 stamps. The first n gaps between successive stamps are
// 1 ms, except `slow` gaps of slowUS microseconds. The stamps are returned in
// arrival order with the slow gaps inserted second, so callers reading them
// as a gap sequence get an average tick of 1 ms.
func stampsNS(n, slow int, slowUS int64) []uint64 {
	out := make([]uint64, 0, n+1) // first stamp plus one per gap
	now := uint64(1_000_000_000)
	out = append(out, now)
	fast := n - slow
	half := fast / 2
	for i := 0; i < half; i++ {
		now += 1000 * 1000
		out = append(out, now)
	}
	for i := 0; i < slow; i++ {
		now += uint64(slowUS) * 1000
		out = append(out, now)
	}
	for i := 0; i < fast-half; i++ {
		now += 1000 * 1000
		out = append(out, now)
	}
	return out
}

// bucketLower is the reported percentile value for a bucket that holds one
// interval of wantUS microseconds; a 1000 us interval lands on 1000 exactly.
func bucketLower(wantUS int64) int64 { return wantUS / frameTimeBucketMicros * frameTimeBucketMicros }

func TestFrameTimeAggregatorPercentiles(t *testing.T) {
	a := NewFrameTimeAggregator()
	// 197 intervals in the 1 ms bucket and 3 in the 5 ms bucket: p50 and p95
	// report the 1 ms bucket floor; p99 lands on the 5 ms frame because
	// ceil(0.99 * 200) = 198 is the first slow rank.
	a.Add(0, stampsNS(200, 3, 5000))
	s := a.Summary()
	if s.Metric != FrameTimeMetric {
		t.Fatalf("metric = %q, want %q", s.Metric, FrameTimeMetric)
	}
	if s.Presents != 200 {
		t.Fatalf("presents = %d, want 200", s.Presents)
	}
	if s.Percentiles.P50 != bucketLower(1000) {
		t.Fatalf("p50 = %d us, want %d", s.Percentiles.P50, bucketLower(1000))
	}
	if s.Percentiles.P95 != bucketLower(1000) {
		t.Fatalf("p95 = %d us, want %d", s.Percentiles.P95, bucketLower(1000))
	}
	if s.Percentiles.P99 != bucketLower(5000) {
		t.Fatalf("p99 = %d us, want %d", s.Percentiles.P99, bucketLower(5000))
	}
	if s.Percentiles.Max != bucketLower(5000) {
		t.Fatalf("max = %d us, want %d", s.Percentiles.Max, bucketLower(5000))
	}
}

// TestFrameTimeAggregatorSteadyRefreshAccuracy pins R4: the reported p50/p99
// must be within 0.1 ms of the true present interval for a steady 60, 144 or
// 240 Hz stream, with the fixed-size histogram.
func TestFrameTimeAggregatorSteadyRefreshAccuracy(t *testing.T) {
	const samples = 1000
	for _, hz := range []uint64{60, 144, 240} {
		step := 1_000_000_000 / hz
		stamps := make([]uint64, samples+1)
		stamps[0] = 1_000_000_000
		for i := 1; i <= samples; i++ {
			stamps[i] = stamps[i-1] + step
		}
		a := NewFrameTimeAggregator()
		a.Add(0, stamps)
		s := a.Summary()
		wantMS := float64(step) / 1e6
		for name, gotUS := range map[string]int64{"p50": s.Percentiles.P50, "p99": s.Percentiles.P99} {
			gotMS := float64(gotUS) / 1000
			if diff := math.Abs(gotMS - wantMS); diff > 0.1 {
				t.Fatalf("%d Hz %s = %.3f ms, want %.3f ms (diff %.3f ms > 0.1 ms)",
					hz, name, gotMS, wantMS, diff)
			}
		}
		if s.Presents != samples {
			t.Fatalf("%d Hz presents = %d, want %d", hz, s.Presents, samples)
		}
	}
}

func TestFrameTimeAggregatorOnePercentLowDefinition(t *testing.T) {
	a := NewFrameTimeAggregator()
	a.Add(0, stampsNS(200, 3, 5000))
	s := a.Summary()
	// ceil(0.01 * 200) = 2 slowest intervals, both in the 5 ms bucket whose
	// midpoint is 5050 us, so the tail mean is 1000/5.05 FPS.
	if s.OnePercentLow.Frames != 2 {
		t.Fatalf("1%% low frames = %d, want 2", s.OnePercentLow.Frames)
	}
	want := 1000.0 / 5.05
	if math.Abs(s.OnePercentLow.MeanFPS-want) > 0.001 {
		t.Fatalf("1%% low = %f FPS, want %f", s.OnePercentLow.MeanFPS, want)
	}
	if !strings.Contains(s.OnePercentLow.Definition, "slowest 1%") {
		t.Fatalf("1%% low definition missing: %q", s.OnePercentLow.Definition)
	}
}

func TestFrameTimeAggregatorDropsOverflowGap(t *testing.T) {
	a := NewFrameTimeAggregator()
	// Every gap is 1 ms. The middle poll reports 7 samples lost before
	// 11000000 ns was read, so the 4 ms -> 11 ms jump must not become one long
	// interval.
	a.Add(0, []uint64{1000000, 2000000, 3000000, 4000000})
	a.Add(7, []uint64{11000000})
	// The ring reports a cumulative total, so the next poll still says 7.
	a.Add(7, []uint64{12000000})
	s := a.Summary()
	if s.Overflow != 7 {
		t.Fatalf("overflow = %d, want 7", s.Overflow)
	}
	if s.Presents != 4 {
		t.Fatalf("presents = %d, want 4 (the three pre-gap and one post-gap gaps)", s.Presents)
	}
	if s.Percentiles.P99 != bucketLower(1000) || s.Percentiles.Max > bucketLower(1000)+frameTimeBucketMicros-1 {
		t.Fatalf("percentiles = %+v, want the 1 ms bucket (overflow gap must be dropped)", s.Percentiles)
	}
}

// TestFrameTimeAggregatorCumulativeOverwrittenContract pins the drop contract
// across two snapshots: out_overwritten is a running total for the capture, so
// the second poll that repeats the same count must not add it again or drop the
// interval between the two windows.
func TestFrameTimeAggregatorCumulativeOverwrittenContract(t *testing.T) {
	a := NewFrameTimeAggregator()
	a.Add(0, []uint64{3_000_000})
	// One sample was lost before this window. The gap 3 ms -> 9 ms is not an
	// interval, but the next two stamps are.
	a.Add(1, []uint64{9_000_000, 10_000_000})
	// Same cumulative total: nothing new was lost, so the 10 ms -> 11 ms
	// interval must still be counted.
	a.Add(1, []uint64{11_000_000})
	s := a.Summary()
	if s.Overflow != 1 {
		t.Fatalf("overflow = %d, want 1 (a repeated cumulative count is not a new gap)", s.Overflow)
	}
	if s.Presents != 2 {
		t.Fatalf("presents = %d, want 2 (1 ms and 1 ms intervals after the gap)", s.Presents)
	}
	if s.Percentiles.P50 != bucketLower(1000) {
		t.Fatalf("p50 = %d, want %d", s.Percentiles.P50, bucketLower(1000))
	}
	// A backwards count is a re-arm, not a new gap, and the next sample starts
	// a fresh origin.
	a.Add(0, []uint64{20_000_000, 21_000_000})
	if s := a.Summary(); s.Overflow != 1 || s.Resets != 0 {
		t.Fatalf("after re-arm baseline: overflow = %d, want 1 (no invented gap)", s.Overflow)
	}
}

func TestFrameTimeAggregatorEmptyAndNonMonotonic(t *testing.T) {
	a := NewFrameTimeAggregator()
	s := a.Summary()
	if s.Presents != 0 || s.Percentiles.P50 != 0 || len(s.Notes) == 0 {
		t.Fatalf("empty capture = %+v, want zero values plus a note", s)
	}
	b := NewFrameTimeAggregator()
	// A concurrent presenter may append out of order; a negative delta is not
	// a frame and must not become a zero-length interval. The 3000000 stamp
	// restarts the origin, so only 3000000->7000000 (4 ms) survives.
	b.Add(0, []uint64{7000000, 3000000, 7000000})
	s = b.Summary()
	if s.Presents != 1 || s.Percentiles.P50 != bucketLower(4000) || s.OutOfOrder != 1 {
		t.Fatalf("non-monotonic capture = %+v, want one 4000 us interval", s)
	}
}

func TestFrameTimeAggregatorRepeatedLossAndClamp(t *testing.T) {
	a := NewFrameTimeAggregator()
	a.Add(0, []uint64{1_000_000, 2_000_000})
	a.Add(3, []uint64{10_000_000, 11_000_000})
	a.Add(6, []uint64{20_000_000, 21_000_000})
	a.Add(6, []uint64{21_000_000 + uint64(frameTimeBucketLimitUS)*1000})
	s := a.Summary()
	if s.Overflow != 6 || s.Presents != 4 || s.Clamped != 1 || s.Percentiles.Max != frameTimeBucketLimitUS-1 {
		t.Fatalf("summary = %+v, want six lost, four intervals and one clamped maximum", s)
	}
	// Rounding an extremely large interval must not wrap into a small bucket.
	b := NewFrameTimeAggregator()
	b.Add(0, []uint64{1, ^uint64(0)})
	if s := b.Summary(); s.Clamped != 1 {
		t.Fatalf("huge interval clamped = %d, want 1", s.Clamped)
	}
}

func TestWriteFrameTimeSummaryFiles(t *testing.T) {
	dir := t.TempDir()
	a := NewFrameTimeAggregator()
	a.Add(0, stampsNS(100, 1, 3000))
	if err := writeFrameTimeSummary(dir, a.Summary()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "framestats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("framestats.json: %v", err)
	}
	for _, key := range []string{"metric", "definition", "source", "percentiles_us", "one_percent_low", "overflow_samples", "contended_samples", "out_of_order_samples", "clamped_samples", "attempts"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("framestats.json missing %q: %s", key, raw)
		}
	}
	low, ok := doc["one_percent_low"].(map[string]any)
	if !ok || low["definition"] == "" {
		t.Fatalf("framestats.json 1%% low definition missing: %s", raw)
	}
	text, err := os.ReadFile(filepath.Join(dir, "framestats.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"present-return interval", "p50:", "p95:", "p99:", "1% low:", "overflow_samples:", "contended_samples:", "attempts:"} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("framestats.txt missing %q:\n%s", want, text)
		}
	}
}

func TestFrameStatsCompareRejectsInvalidSamples(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "framestats-compare.py"))
	if err != nil {
		t.Fatal(err)
	}
	makeCapture := func(key string, text bool) string {
		t.Helper()
		a := NewFrameTimeAggregator()
		a.Add(0, []uint64{1_000_000, 2_000_000})
		s := a.Summary()
		s.Source, s.Path = FrameTimeSourceNative, FrameTimePresentPath
		switch key {
		case "overflow_samples":
			s.Overflow = 3
		case "out_of_order_samples":
			s.OutOfOrder = 1
		case "clamped_samples":
			s.Clamped = 1
		}
		dir := t.TempDir()
		if err := writeFrameTimeSummary(dir, s); err != nil {
			t.Fatal(err)
		}
		if text {
			if err := os.Remove(filepath.Join(dir, "framestats.json")); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	for _, text := range []bool{false, true} {
		clean := makeCapture("", text)
		for _, key := range []string{"overflow_samples", "out_of_order_samples", "clamped_samples"} {
			bad := makeCapture(key, text)
			for _, paths := range [][2]string{{clean, bad}, {bad, clean}} {
				output, err := exec.Command("python3", script, paths[0], paths[1]).CombinedOutput()
				if err == nil || !strings.Contains(string(output), key) {
					t.Errorf("text=%v key=%s: err=%v output=%s", text, key, err, output)
				}
			}
		}
	}
}

func TestFrameTimeCaptureWritesDropTelemetry(t *testing.T) {
	dir := t.TempDir()
	contended, attempts := uint64(8), uint64(110)
	c := &FrameTimeCapture{
		dir:             dir,
		source:          FrameTimeSourceNative,
		path:            FrameTimePresentPath,
		agg:             NewFrameTimeAggregator(),
		done:            make(chan struct{}),
		stop:            func() {},
		contendedReader: func() uint64 { return contended },
		attemptsReader:  func() uint64 { return attempts },
		contendedBase:   3,
		attemptsBase:    100,
		snapshotter: func(uint64) (uint64, uint64, []uint64) {
			return 3, 0, []uint64{1_000_000, 2_000_000, 3_000_000}
		},
	}
	var written FrameTimeSummary
	c.writes = func(s FrameTimeSummary) { written = s }
	c.stopCapture()
	if written.ContendedSamples != 5 || written.Attempts != 10 {
		t.Fatalf("drop telemetry = contended %d, attempts %d; want 5 and 10", written.ContendedSamples, written.Attempts)
	}
	if !strings.Contains(strings.Join(written.Notes, "\n"), "following interval includes its time") {
		t.Fatalf("drop note missing: %v", written.Notes)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "framestats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("framestats.json: %v", err)
	}
	if doc["contended_samples"] != float64(5) || doc["attempts"] != float64(10) {
		t.Fatalf("framestats.json drop telemetry = %v, want contended_samples 5 and attempts 10", doc)
	}
	text, err := os.ReadFile(filepath.Join(dir, "framestats.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"contended_samples: 5", "attempts: 10", "following interval includes its time"} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("framestats.txt missing %q:\n%s", want, text)
		}
	}
}

func TestFrameStatsCompareRejectsContendedCaptures(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "framestats-compare.py"))
	if err != nil {
		t.Fatal(err)
	}
	makeCapture := func(name string, contended *int) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		doc := map[string]any{
			"metric":           FrameTimeMetric,
			"definition":       frameTimeDefinition,
			"source":           FrameTimeSourceNative,
			"path":             FrameTimePresentPath,
			"presents":         2,
			"overflow_samples": 0,
			"percentiles_us":   map[string]int{"p50": 1000, "p95": 1000, "p99": 1000, "max": 1000},
			"one_percent_low":  map[string]any{"definition": frameTimeLowDefinition, "mean_fps": 1000.0},
		}
		if contended != nil {
			doc["contended_samples"] = *contended
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "framestats.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	run := func(baseline, candidate string) error {
		t.Helper()
		cmd := exec.Command("python3", script, baseline, candidate)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("framestats-compare output: %s", output)
		}
		return err
	}
	zero := 0
	missing := makeCapture("missing", nil)
	clean := makeCapture("clean", &zero)
	if err := run(missing, clean); err != nil {
		t.Fatalf("missing contended_samples should default to zero: %v", err)
	}
	for _, side := range []string{"baseline", "candidate"} {
		contended := 1
		bad := makeCapture("contended-"+side, &contended)
		baseline, candidate := clean, bad
		if side == "baseline" {
			baseline, candidate = bad, clean
		}
		if err := run(baseline, candidate); err == nil {
			t.Errorf("comparison accepted contended capture on %s side", side)
		}
	}
}

// TestFrameStatsCompareTextFallback pins R6: a capture directory with only
// framestats.txt is comparable. The text writer emits path, so the fallback
// parser must carry it into the same validation the JSON path uses.
func TestFrameStatsCompareTextFallback(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "framestats-compare.py"))
	if err != nil {
		t.Fatal(err)
	}
	makeText := func() string {
		dir := t.TempDir()
		summary := FrameTimeSummary{
			Metric:     FrameTimeMetric,
			Definition: frameTimeDefinition,
			Source:     FrameTimeSourceNative,
			Path:       FrameTimePresentPath,
			Percentiles: FrameTimeValues{
				P50: 1000, P95: 1000, P99: 1000, Max: 1000,
			},
			Presents: 2,
			OnePercentLow: FrameTimeOnePercentLow{
				Definition: frameTimeLowDefinition,
				Frames:     2,
				MeanFPS:    1000,
			},
		}
		var text strings.Builder
		FormatFrameTimeText(&text, summary)
		if err := os.WriteFile(filepath.Join(dir, "framestats.txt"), []byte(text.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cmd := exec.Command("python3", script, makeText(), makeText())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("text-only comparison failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "p50") {
		t.Fatalf("text-only comparison produced no table:\n%s", output)
	}
}

// TestPGOMergeSelectsCPUProfilesOnly pins R6: the capture directory also holds
// heap dumps with a .pprof suffix, and merging one of those makes pprof reject
// the set. The script must select CPU profiles by period type.
func TestPGOMergeSelectsCPUProfilesOnly(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "pgo-merge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	capture := t.TempDir()
	cpuPath := filepath.Join(capture, "cpu.pprof")
	cpu, err := os.Create(cpuPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.StartCPUProfile(cpu); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(50 * time.Millisecond)
	sink := 0
	for time.Now().Before(end) {
		sink += time.Now().Nanosecond()
	}
	pprof.StopCPUProfile()
	if err := cpu.Close(); err != nil {
		t.Fatal(err)
	}
	if sink == 0 {
		t.Fatal("cpu profile captured no work")
	}
	heapPath := filepath.Join(capture, "heap.pprof")
	heap, err := os.Create(heapPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.WriteHeapProfile(heap); err != nil {
		t.Fatal(err)
	}
	if err := heap.Close(); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "default.pgo")
	cmd := exec.Command("bash", script, capture)
	cmd.Env = append(os.Environ(), "TIPSY_PGO_OUTPUT="+output)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pgo-merge failed with a heap dump in the capture: %v\n%s", err, combined)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("merged profile: %v\n%s", err, combined)
	}
	if info.Size() == 0 {
		t.Fatal("merged profile is empty")
	}
}

// TestFrameTimeSummaryNamesPresentPath pins the disclosure that makes a capture
// honest: the ring is fed only by the Vulkan present path, so the summary says
// path=vulkan-present and states that GLES presents are not captured.
func TestFrameTimeSummaryNamesPresentPath(t *testing.T) {
	dir := t.TempDir()
	c := &FrameTimeCapture{
		dir:         dir,
		source:      FrameTimeSourceNative,
		path:        FrameTimePresentPath,
		agg:         NewFrameTimeAggregator(),
		done:        make(chan struct{}),
		stop:        func() {},
		snapshotter: android.FrameTimeSnapshot,
	}
	c.agg.notes = append(c.agg.notes, FrameTimeGLESNote)
	c.stopCapture()
	raw, err := os.ReadFile(filepath.Join(dir, "framestats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("framestats.json: %v", err)
	}
	if got := doc["path"]; got != "vulkan-present" {
		t.Fatalf("path = %v, want vulkan-present: %s", got, raw)
	}
	if def, _ := doc["definition"].(string); !strings.Contains(def, "GLES present path is not captured") {
		t.Fatalf("definition does not disclose GLES exclusion: %q", def)
	}
	notes, _ := doc["notes"].([]any)
	found := false
	for _, note := range notes {
		if s, _ := note.(string); strings.Contains(s, "GLES present path not captured") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes lack the GLES disclosure: %s", raw)
	}
	text, err := os.ReadFile(filepath.Join(dir, "framestats.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"path: vulkan-present", "GLES present path not captured"} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("framestats.txt missing %q:\n%s", want, text)
		}
	}
}

// TestFrameTimeCaptureStopWaitsForPoller pins the stop order: the final
// snapshot is taken only after the poll goroutine has exited, exactly once,
// and a second stop writes nothing.
// TestFrameTimeCaptureWritesPeriodically pins R5: the poller publishes
// framestats.json and framestats.txt while the process is still running, using
// tmp + rename, so a crash or _exit does not lose the whole capture.
func TestFrameTimeCaptureWritesPeriodically(t *testing.T) {
	dir := t.TempDir()
	c := &FrameTimeCapture{
		dir:    dir,
		source: FrameTimeSourceNative,
		path:   FrameTimePresentPath,
		agg:    NewFrameTimeAggregator(),
		done:   make(chan struct{}),
		poller: make(chan struct{}),
		stop:   func() {},
		snapshotter: func(after uint64) (uint64, uint64, []uint64) {
			if after == 0 {
				return 2, 0, []uint64{1_000_000, 2_000_000, 3_000_000}
			}
			return after, 0, nil
		},
	}
	go c.pollNative()
	defer c.stopCapture()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "framestats.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			c.stopCapture()
			t.Fatal("framestats.json was not written while the capture was running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "framestats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("periodic framestats.json: %v", err)
	}
	if doc["source"] != FrameTimeSourceNative {
		t.Fatalf("periodic summary source = %v, want %s", doc["source"], FrameTimeSourceNative)
	}
	c.stopCapture()
	if _, err := os.Stat(filepath.Join(dir, "framestats.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("tmp file left after atomic write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "framestats.txt")); err != nil {
		t.Fatalf("framestats.txt: %v", err)
	}
}

func TestFrameTimeCaptureStopWaitsForPoller(t *testing.T) {
	dir := t.TempDir()
	c := &FrameTimeCapture{
		dir:    dir,
		source: FrameTimeSourceNative,
		path:   FrameTimePresentPath,
		agg:    NewFrameTimeAggregator(),
		done:   make(chan struct{}),
		poller: make(chan struct{}),
		stop:   func() {},
	}
	// Reads are only meaningful once the poller has exited: a read before that
	// would share the cursor with a poll in flight. pollNative closes c.poller
	// as it returns, so the final snapshot can check it directly.
	var reads int
	c.snapshotter = func(after uint64) (uint64, uint64, []uint64) {
		select {
		case <-c.poller:
		default:
			t.Errorf("final snapshot taken while the poll goroutine was still running")
		}
		reads++
		if reads == 1 {
			return 3, 0, []uint64{1_000_000, 2_000_000, 3_000_000}
		}
		return after, 0, nil
	}
	var written []FrameTimeSummary
	c.writes = func(s FrameTimeSummary) { written = append(written, s) }
	go c.pollNative()

	// Let the poller enter its select loop, then stop. The poll interval is far
	// longer than this test, so only the join can end it.
	time.Sleep(10 * time.Millisecond)
	c.stopCapture()
	c.stopCapture()

	if len(written) != 1 {
		t.Fatalf("summaries written = %d, want 1", len(written))
	}
	if reads != 1 {
		t.Fatalf("ring reads at stop = %d, want 1", reads)
	}
	if written[0].Presents != 2 || written[0].Path != FrameTimePresentPath {
		t.Fatalf("summary = %+v, want 2 intervals on %s", written[0], FrameTimePresentPath)
	}
	select {
	case <-c.poller:
	default:
		t.Fatal("stopCapture returned before the poll goroutine exited")
	}
}

// TestFrameTimeCaptureStopJoinsRealPoller runs the real poll goroutine through
// a start, a stop, and a second stop under -race. The final snapshot reads the
// real ring after the join, so a cursor shared with a live poller would be a
// data race here.
func TestFrameTimeCaptureStopJoinsRealPoller(t *testing.T) {
	c := startFrameTimeCapture(t.TempDir())
	c.stopCapture()
	c.stopCapture()
	raw, err := os.ReadFile(filepath.Join(c.dir, "framestats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("framestats.json: %v", err)
	}
	if c.source == FrameTimeSourceNative {
		if doc["path"] != FrameTimePresentPath {
			t.Fatalf("path = %v, want %s: %s", doc["path"], FrameTimePresentPath, raw)
		}
	} else if doc["source"] != FrameTimeSourceUnavailable {
		t.Fatalf("source = %v, want %s", doc["source"], FrameTimeSourceUnavailable)
	}
}

func TestStartProcessProfileWritesFrameStats(t *testing.T) {
	dir := t.TempDir()
	stop, err := StartProcessProfile(dir, ProfileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	for _, name := range []string{"framestats.json", "framestats.txt"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

// TestWriteAtomicIgnoresPlantedTempSymlink pins the O_EXCL temp-file behaviour:
// a symlink left at the old predictable "<path>.tmp" location must not receive
// the write, and the real output must still be produced.
func TestWriteAtomicIgnoresPlantedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "framestats.json")
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := writeFrameTimeJSON(path, FrameTimeSummary{Metric: "present"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "sentinel" {
		t.Fatalf("victim = %q, %v; want unchanged sentinel", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("framestats.json mode = %v, want 0600", info.Mode().Perm())
	}
	if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), `"present"`) {
		t.Fatalf("framestats.json = %q, %v", raw, err)
	}
	if target, err := os.Readlink(path + ".tmp"); err != nil || target != victim {
		t.Fatalf("planted symlink changed: %q, %v", target, err)
	}
}

// TestWriteProfileFileIgnoresPlantedTempSymlink covers the profiler writer,
// which shares writeAtomic but writes pprof bodies instead of frame stats.
func TestWriteProfileFileIgnoresPlantedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "heap.pprof")
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	write := func(w io.Writer) error {
		_, err := io.WriteString(w, "profile-body")
		return err
	}
	if err := writeProfileFile(path, "", write); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "sentinel" {
		t.Fatalf("victim = %q, %v; want unchanged sentinel", got, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "profile-body" {
		t.Fatalf("heap.pprof = %q, %v", got, err)
	}
}
