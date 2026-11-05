package perf

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
)

// FrameTimeMetric names the captured interval. It is deliberately not called
// "frame time" alone: neighbours are two returns from a successful present
// call, so the value is the wall time between present returns, not display
// scan-out and not input-to-photon latency.
const FrameTimeMetric = "present-return interval"

// FrameTimeSourceNative is the native frame-time ring fed by the Vulkan
// present-return path, the presenter Tipsy drives for gameplay. Presents
// happen on native threads Go never sees, so the ring is the only seam.
const FrameTimeSourceNative = "native-ring"

// FrameTimePresentPath names which present path the native ring observes. Only
// the Vulkan adapter records into it, so an EGL/GLES-presented frame is not in
// the capture; the summary says so instead of reporting GLES as zero.
const FrameTimePresentPath = "vulkan-present"

// FrameTimeGLESNote discloses that the GLES present path is outside this
// capture. It is written into every native summary and its framestats.txt.
const FrameTimeGLESNote = "GLES present path not captured: framestats records Vulkan vkQueuePresentKHR returns only, so frames presented through eglSwapBuffers are absent from these percentiles"

// FrameTimeSourceUnavailable marks a build with no native ring; the summary
// then says so instead of reporting zero-frame statistics as a result.
const FrameTimeSourceUnavailable = "unavailable"

// FrameTimePollInterval is the bounded poll cadence. It must stay well below
// the time it takes one ring length of presents to wrap (8192 frames at
// 60 FPS is about 136 s), so a first poll is also allowed immediately.
const FrameTimePollInterval = 200 * time.Millisecond

const (
	// frameTimeBucketMicros is the fixed histogram bucket width, in
	// microseconds. 100 us keeps the nearest-rank floor within 0.1 ms of the
	// true interval for every steady refresh rate (60/144/240 Hz), while the
	// fixed bin count keeps memory bounded.
	frameTimeBucketMicros = 100
	// frameTimePercentileBins keeps 16384 buckets, covering intervals up to
	// about 1.6 s. A longer interval is clamped into the last bucket and
	// counted in clamped_samples.
	frameTimePercentileBins = 16384
	frameTimeBucketLimitUS  = frameTimePercentileBins * frameTimeBucketMicros
	// frameTimeLowFPSPercent is the "1% low" tail share.
	frameTimeLowFPSPercent = 1.0
)

// FrameTimeSummary is the machine-readable capture summary. Values are
// integer microseconds so a capture file stays comparable byte for byte.
type FrameTimeSummary struct {
	Metric           string                 `json:"metric"`
	Definition       string                 `json:"definition"`
	Source           string                 `json:"source"`
	Path             string                 `json:"path,omitempty"`
	Percentiles      FrameTimeValues        `json:"percentiles_us"`
	OnePercentLow    FrameTimeOnePercentLow `json:"one_percent_low"`
	Polls            int                    `json:"polls"`
	Presents         uint64                 `json:"presents"`
	Overflow         uint64                 `json:"overflow_samples"`
	ContendedSamples uint64                 `json:"contended_samples"`
	Attempts         uint64                 `json:"attempts"`
	Clamped          uint64                 `json:"clamped_samples"`
	OutOfOrder       uint64                 `json:"out_of_order_samples"`
	Resets           uint64                 `json:"ring_resets"`
	ReadErrors       int                    `json:"read_errors"`
	Notes            []string               `json:"notes,omitempty"`
}

// FrameTimeValues holds nearest-rank percentiles in microseconds.
type FrameTimeValues struct {
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
	Max int64 `json:"max"`
}

// FrameTimeOnePercentLow records the slowest-tail mean and the exact
// definition used, because "1% low" is otherwise ambiguous.
type FrameTimeOnePercentLow struct {
	Definition  string  `json:"definition"`
	Frames      int     `json:"frames"`
	MeanFPS     float64 `json:"mean_fps"`
	MeanFrameMS float64 `json:"mean_interval_ms"`
}

const frameTimeDefinition = "Time between two successive returns from a successful Vulkan vkQueuePresentKHR call in the Tipsy Vulkan adapter, measured on host CLOCK_MONOTONIC. The GLES present path is not captured. It is not display scan-out, compositor latency, or input-to-photon latency."

const frameTimeLowDefinition = "Mean FPS of the slowest 1% of captured intervals: ceil(0.01*n) longest intervals are selected, and the value is 1000 / (their mean length in ms)."

// FrameTimeAggregator accumulates interval buckets from a frame-time ring.
// Intervals are bucketed at a fixed width so memory stays constant and the
// result does not depend on when a poll arrived. A sample whose predecessor
// was lost to ring overflow, or that belongs to a different ring reset, is
// dropped rather than turned into a fictitious interval.
type FrameTimeAggregator struct {
	buckets    [frameTimePercentileBins]uint64
	count      uint64
	maxUS      int64
	clamped    uint64
	outOfOrder uint64
	overflow   uint64
	resets     uint64
	polls      int
	readErrors int
	prev       uint64
	havePrev   bool
	// overwrittenSeen is the last cumulative drop count folded in. The ring
	// reports a running total, so only an increase is a new gap.
	overwrittenSeen uint64
	notes           []string
}

// NewFrameTimeAggregator returns an empty aggregator.
func NewFrameTimeAggregator() *FrameTimeAggregator {
	return &FrameTimeAggregator{}
}

// Add appends timestamps copied from one ring snapshot. overwritten is the
// ring's cumulative lost-sample count for the capture (late-claim drops plus
// samples wrapped past before the retained window); only an increase over the
// last value is a new gap. A gap is counted once and the interval across it is
// not invented. A count that moves backwards means the ring was re-armed, so
// the baseline is reset without inventing a gap.
func (a *FrameTimeAggregator) Add(overwritten uint64, samples []uint64) {
	a.polls++
	if overwritten > a.overwrittenSeen {
		a.overflow += overwritten - a.overwrittenSeen
		a.havePrev = false
	}
	if overwritten < a.overwrittenSeen {
		a.havePrev = false
		a.overflow += overwritten
	}
	a.overwrittenSeen = overwritten
	for _, now := range samples {
		if !a.havePrev {
			// First retained stamp after arming or after an overflow gap: its
			// predecessor is unknown, so no interval is invented from it.
			a.prev = now
			a.havePrev = true
			continue
		}
		prev := a.prev
		a.prev = now
		if now <= prev {
			// A non-monotonic or concurrent-reader gap cannot be a frame
			// interval; keep the newer stamp as the next origin.
			a.notes = appendNote(a.notes, "skipped non-positive present-return interval")
			a.outOfOrder++
			continue
		}
		a.observe(now - prev)
	}
}

// MarkReset records a ring reset observed between polls (for example a
// reachable Vulkan ring cursor that moved backwards).
func (a *FrameTimeAggregator) MarkReset() {
	a.resets++
	a.havePrev = false
}

// MarkReadError records one failed ring read.
func (a *FrameTimeAggregator) MarkReadError() {
	a.readErrors++
}

func (a *FrameTimeAggregator) observe(deltaNS uint64) {
	// Round a sub-microsecond remainder up so a 0.4 ms tick cannot fall into
	// the 0 us bucket.
	us := deltaNS / 1000
	if deltaNS%1000 != 0 {
		us++
	}
	if us >= frameTimeBucketLimitUS {
		a.clamped++
		us = frameTimeBucketLimitUS - 1
	}
	a.buckets[us/frameTimeBucketMicros]++
	a.count++
	if int64(us) > a.maxUS {
		a.maxUS = int64(us)
	}
}

// Summary computes nearest-rank p50/p95/p99 over bucket floors and the 1% low
// tail mean. An empty capture reports zero values with a note rather than
// inventing a frame time.
func (a *FrameTimeAggregator) Summary() FrameTimeSummary {
	sum := FrameTimeSummary{
		Metric:     FrameTimeMetric,
		Definition: frameTimeDefinition,
		Presents:   a.count,
		Overflow:   a.overflow,
		Clamped:    a.clamped,
		OutOfOrder: a.outOfOrder,
		Resets:     a.resets,
		Polls:      a.polls,
		ReadErrors: a.readErrors,
		Notes:      append([]string(nil), a.notes...),
	}
	if a.count == 0 {
		sum.Notes = appendNote(sum.Notes, "no present-return intervals captured; the client may not have presented through the Vulkan adapter")
		return sum
	}
	sum.Percentiles = FrameTimeValues{
		P50: a.percentile(50),
		P95: a.percentile(95),
		P99: a.percentile(99),
		Max: a.maxUS,
	}
	low := a.slowestMeanUS()
	sum.OnePercentLow = FrameTimeOnePercentLow{
		Definition:  frameTimeLowDefinition,
		Frames:      low.frames,
		MeanFPS:     fpsFromMicros(low.meanUS),
		MeanFrameMS: float64(low.meanUS) / 1000,
	}
	return sum
}

type tailMean struct {
	frames int
	meanUS float64
}

// slowestMeanUS walks buckets from the slowest end, summing whole buckets
// until the tail is complete. Bucket midpoints fix the sign of any rounding,
// so the result is stable across runs of the same capture.
func (a *FrameTimeAggregator) slowestMeanUS() tailMean {
	target := int64(math.Ceil(float64(a.count) * frameTimeLowFPSPercent / 100))
	if target < 1 {
		target = 1
	}
	if target > int64(a.count) {
		target = int64(a.count)
	}
	var taken int64
	var sumUS float64
	for i := len(a.buckets) - 1; i >= 0 && taken < target; i-- {
		n := int64(a.buckets[i])
		if n == 0 {
			continue
		}
		mid := bucketMidpointUS(i)
		if n <= target-taken {
			taken += n
			sumUS += float64(n) * float64(mid)
			continue
		}
		part := target - taken
		taken += part
		sumUS += float64(part) * float64(mid)
	}
	return tailMean{frames: int(taken), meanUS: sumUS / float64(taken)}
}

// percentile is nearest-rank over bucket lower edges. A whole bucket shares
// one value, so a rank inside a bucket reports where the bucket starts.
func (a *FrameTimeAggregator) percentile(rank float64) int64 {
	if a.count == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(a.count) * rank / 100))
	if target < 1 {
		target = 1
	}
	var seen int64
	for i := range a.buckets {
		seen += int64(a.buckets[i])
		if seen >= target {
			return bucketLowerUS(i)
		}
	}
	return a.maxUS
}

// bucketLowerUS is the smallest interval in bucket i; bucketMidpointUS is its
// centre, used for the 1% low so a bucket's contribution is unbiased.
func bucketLowerUS(i int) int64 {
	return int64(i) * frameTimeBucketMicros
}

func bucketMidpointUS(i int) int64 {
	return int64(i)*frameTimeBucketMicros + frameTimeBucketMicros/2
}

func fpsFromMicros(meanUS float64) float64 {
	if meanUS <= 0 {
		return 0
	}
	return roundTo(1e6/meanUS, 0.001)
}

// roundTo rounds to the nearest step and avoids float dust in JSON.
func roundTo(v, step float64) float64 {
	return math.Round(v/step) * step
}

func appendNote(notes []string, note string) []string {
	for _, existing := range notes {
		if existing == note {
			return notes
		}
	}
	return append(notes, note)
}

// FrameTimeCapture arms the native frame-time ring for one capture lifetime.
// It exists only while a process profile directory is configured.
//
// The ring cursor, the aggregator, and the final write are single-threaded by
// contract: pollNative runs on one goroutine, stopCapture joins it, and takes
// the last snapshot itself, so a sample is folded in exactly once.
type FrameTimeCapture struct {
	dir    string
	source string
	path   string
	stop   func()
	agg    *FrameTimeAggregator
	done   chan struct{}
	// poller closes when the poll goroutine has returned. It is nil in a
	// build with no native ring.
	poller chan struct{}
	// finalOnce makes the stop-time snapshot and write happen once even if
	// stopCapture is called twice.
	finalOnce sync.Once
	// stopOnce makes closing done and joining the poller happen once, so a
	// repeated stop does not close a closed channel.
	stopOnce sync.Once
	// native is the retained ring cursor.
	native uint64
	// snapshotter reads the ring; tests replace it to observe the final read.
	snapshotter func(after uint64) (cursor, overwritten uint64, samples []uint64)
	// Counter readers and arm-time baselines make drop telemetry capture-local.
	contendedReader func() uint64
	attemptsReader  func() uint64
	contendedBase   uint64
	attemptsBase    uint64
	// writes records each summary write; tests count them.
	writes func(FrameTimeSummary)
}

// startFrameTimeCapture arms the native ring when the build has a Vulkan
// adapter. A build without
// one still writes a summary that says so, never invented intervals.
func startFrameTimeCapture(dir string) *FrameTimeCapture {
	c := &FrameTimeCapture{
		dir:             dir,
		agg:             NewFrameTimeAggregator(),
		done:            make(chan struct{}),
		snapshotter:     android.FrameTimeSnapshot,
		contendedReader: android.FrameTimeContended,
		attemptsReader:  android.FrameTimeAttempts,
	}
	if android.VulkanPresentTimingSupported() {
		// The native ring is independent of the opt-in diagnostic timing ring.
		c.source = FrameTimeSourceNative
		c.path = FrameTimePresentPath
		c.agg.notes = append(c.agg.notes, FrameTimeGLESNote)
		c.native = android.SetFrameTimeArmed(true)
		// Arming clears both native counters before enabling presenters; zero is
		// the baseline, avoiding a read-after-arm race that could skip early events.
		c.contendedBase = 0
		c.attemptsBase = 0
		c.stop = func() {
			android.SetFrameTimeArmed(false)
		}
		c.poller = make(chan struct{})
		go c.pollNative()
		return c
	}
	c.source = FrameTimeSourceUnavailable
	c.stop = func() {}
	c.agg.MarkReadError()
	c.agg.notes = append(c.agg.notes,
		"this build has no Vulkan adapter, so it records no present-return intervals")
	return c
}

// stopCapture stops the poller, disarms the source, takes one final snapshot,
// then writes framestats.json and framestats.txt. Joining the poller before
// the final read keeps the cursor single-owner: a poll already in flight cannot
// race the last snapshot or fold the same samples twice.
func (c *FrameTimeCapture) stopCapture() {
	c.stopOnce.Do(func() {
		close(c.done)
		if c.poller != nil {
			<-c.poller
		}
		if c.stop != nil {
			c.stop()
		}
	})
	c.finalOnce.Do(func() {
		if c.source == FrameTimeSourceNative {
			c.pollNativeOnce()
		}
		c.writeSummary(c.currentSummary())
	})
}

// currentSummary adds the capture-local counters and path to the histogram.
func (c *FrameTimeCapture) currentSummary() FrameTimeSummary {
	summary := c.agg.Summary()
	if summary.Source == "" {
		summary.Source = c.source
	}
	if c.source == FrameTimeSourceNative {
		summary.ContendedSamples = frameTimeCounterDelta(c.contendedReader, c.contendedBase)
		summary.Attempts = frameTimeCounterDelta(c.attemptsReader, c.attemptsBase)
	}
	if summary.ContendedSamples != 0 {
		summary.Notes = appendNote(summary.Notes,
			"a dropped present is not timestamped, so the following interval includes its time")
	}
	if summary.Path == "" {
		summary.Path = c.path
	}
	return summary
}

// writeSummary publishes one snapshot atomically (tmp + rename). The poller
// calls it every interval so a crash or _exit loses at most one interval, and
// stopCapture calls it once more for the final totals.
func (c *FrameTimeCapture) writeSummary(summary FrameTimeSummary) {
	if c.writes != nil {
		c.writes(summary)
	}
	if err := writeFrameTimeSummary(c.dir, summary); err != nil {
		fmt.Fprintf(os.Stderr, "tipsy: write framestats: %v\n", err)
	}
}

func frameTimeCounterDelta(read func() uint64, baseline uint64) uint64 {
	if read == nil {
		return 0
	}
	current := read()
	if current < baseline {
		// A ring re-arm during capture resets counters; report the current epoch
		// rather than wrapping an unsigned subtraction into a false huge count.
		return current
	}
	return current - baseline
}

func (c *FrameTimeCapture) pollNative() {
	defer close(c.poller)
	ticker := time.NewTicker(FrameTimePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.pollNativeOnce()
			c.writeSummary(c.currentSummary())
		case <-c.done:
			return
		}
	}
}

// pollNativeOnce folds one ring snapshot into the histogram. The ring never
// reports a cursor behind its samples, so a cursor that moved backwards means
// the ring was reset or re-armed under this capture; the gap is recorded as a
// reset and not used as an interval.
func (c *FrameTimeCapture) pollNativeOnce() {
	cursor, overwritten, samples := c.snapshotter(c.native)
	if cursor < c.native {
		c.agg.MarkReset()
	}
	c.native = cursor
	c.agg.Add(overwritten, samples)
}

// writeFrameTimeSummary writes framestats.json and framestats.txt atomically
// into dir.
func writeFrameTimeSummary(dir string, summary FrameTimeSummary) error {
	if err := writeFrameTimeJSON(filepath.Join(dir, "framestats.json"), summary); err != nil {
		return err
	}
	var text strings.Builder
	FormatFrameTimeText(&text, summary)
	return writeFrameTimeText(filepath.Join(dir, "framestats.txt"), text.String())
}

func writeFrameTimeJSON(path string, summary FrameTimeSummary) error {
	return writeAtomic(path, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	})
}

func writeFrameTimeText(path, text string) error {
	return writeAtomic(path, func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func writeAtomic(path string, write func(io.Writer) error) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := write(f)
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, path)
}

// FormatFrameTimeText renders the human-readable framestats.txt body.
func FormatFrameTimeText(w io.Writer, s FrameTimeSummary) {
	fmt.Fprintf(w, "metric: %s\n", s.Metric)
	fmt.Fprintf(w, "definition: %s\n", s.Definition)
	fmt.Fprintf(w, "source: %s\n", s.Source)
	if s.Path != "" {
		fmt.Fprintf(w, "path: %s\n", s.Path)
	}
	fmt.Fprintf(w, "present-return intervals: %d over %d polls\n", s.Presents, s.Polls)
	fmt.Fprintf(w, "p50: %s\n", formatMicros(s.Percentiles.P50))
	fmt.Fprintf(w, "p95: %s\n", formatMicros(s.Percentiles.P95))
	fmt.Fprintf(w, "p99: %s\n", formatMicros(s.Percentiles.P99))
	fmt.Fprintf(w, "max: %s\n", formatMicros(s.Percentiles.Max))
	fmt.Fprintf(w, "1%% low: %.3f FPS over %d slowest frame(s) (mean interval %.3f ms)\n",
		s.OnePercentLow.MeanFPS, s.OnePercentLow.Frames, s.OnePercentLow.MeanFrameMS)
	fmt.Fprintf(w, "1%% low definition: %s\n", s.OnePercentLow.Definition)
	fmt.Fprintf(w, "overflow_samples: %d (samples dropped before a poll read them; nonzero is an error)\n", s.Overflow)
	fmt.Fprintf(w, "contended_samples: %d (present drops because no timestamp was available; nonzero is an error)\n", s.ContendedSamples)
	fmt.Fprintf(w, "out_of_order_samples: %d (non-positive intervals; nonzero is an error)\n", s.OutOfOrder)
	fmt.Fprintf(w, "attempts: %d\n", s.Attempts)
	fmt.Fprintf(w, "clamped_samples: %d (at least %.1f ms; max and slow tail are clamped)\n", s.Clamped, float64(frameTimeBucketLimitUS)/1000)
	if s.Resets != 0 {
		fmt.Fprintf(w, "ring_resets: %d\n", s.Resets)
	}
	if s.ReadErrors != 0 {
		fmt.Fprintf(w, "read_errors: %d\n", s.ReadErrors)
	}
	for _, note := range s.Notes {
		fmt.Fprintf(w, "note: %s\n", note)
	}
}

func formatMicros(us int64) string {
	if us == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3f ms", float64(us)/1000)
}
