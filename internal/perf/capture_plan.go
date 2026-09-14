package perf

// CapturePlan describes the privacy-safe evidence required before a
// host-runtime microbenchmark can be connected to user-visible performance.
// It is emitted with every runner report so raw baseline data cannot be
// mistaken for a gameplay result.
type CapturePlan struct {
	Instrumented CaptureMode    `json:"instrumented"`
	Clean        CaptureMode    `json:"clean_acceptance"`
	Measurements []Measurement  `json:"measurements"`
	Cases        []WorkloadCase `json:"cases"`
}

// CaptureMode is one deliberately separate kind of live evidence. A clean
// acceptance run is not a control for an instrumented run: it answers whether
// the client still behaves correctly without diagnostics, while like-for-like
// instrumented arms answer a performance comparison.
type CaptureMode struct {
	ID         string   `json:"id"`
	Purpose    string   `json:"purpose"`
	Required   []string `json:"required"`
	Prohibited []string `json:"prohibited"`
}

// Measurement makes both a unit and an availability boundary explicit. The
// baseline runner records Go allocations from the testing package and direct
// test-binary resource usage. Metrics marked live-workload-required must come
// from the named owner seam during matched, user-driven gameplay.
type Measurement struct {
	ID           string `json:"id"`
	Unit         string `json:"unit"`
	Definition   string `json:"definition"`
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
}

// WorkloadCase is a requested host-overhead workload that cannot necessarily
// be run by a privacy-safe checkout. It gives the next subsystem owner the
// smallest required seam without turning unavailable work into a zero result.
type WorkloadCase struct {
	ID             string   `json:"id"`
	Subsystem      string   `json:"subsystem"`
	Availability   string   `json:"availability"`
	Reason         string   `json:"reason"`
	RequiredMetric []string `json:"required_metrics"`
	OwnerHandoff   string   `json:"owner_handoff,omitempty"`
}

// HostOverheadCapturePlan is stable report metadata, not live capture data.
// The two modes must never be compared directly: diagnostics intentionally
// add work, and clean runs intentionally omit it.
func HostOverheadCapturePlan() CapturePlan {
	return CapturePlan{
		Instrumented: CaptureMode{
			ID:      "instrumented-capture",
			Purpose: "Compare equal-duration warmed A/B/A gameplay arms while the same privacy-safe observer set is enabled in every arm.",
			Required: []string{
				"same scene, route, renderer, resolution, quality, frame cap, and compositor state",
				"same observer configuration in every compared arm",
				"redacted aggregate-only diagnostics and frame summary metrics",
			},
			Prohibited: []string{
				"comparing observer-enabled data to clean data as an optimization result",
				"raw input, text, account, cookie, URL, or microphone payload capture",
			},
		},
		Clean: CaptureMode{
			ID:      "clean-acceptance",
			Purpose: "Confirm visible input, audio, shutdown, and frame pacing after a candidate with diagnostics disabled.",
			Required: []string{
				"same warmed scene and settings as the associated instrumented capture",
				"visible input, audio, and shutdown acceptance recorded separately",
			},
			Prohibited: []string{
				"using clean results as the baseline or candidate side of an instrumented comparison",
				"claiming an FPS gain without matched instrumented frame-tail evidence",
			},
		},
		Measurements: []Measurement{
			{
				ID:           "frame-time-percentiles",
				Unit:         "nanoseconds per displayed frame",
				Definition:   "After warm-up, sort complete displayed-frame durations ascending and report nearest-rank p50=ceil(0.50*N), p95=ceil(0.95*N), and p99=ceil(0.99*N). Present-call duration is not a displayed-frame duration.",
				Availability: "live-workload-required",
				Reason:       "Requires a matched user-driven scene and a display-frame timing source.",
			},
			{
				ID:           "one-percent-low",
				Unit:         "frames per second",
				Definition:   "1% low FPS = 1e9 / nearest-rank p99 displayed-frame duration from the same warmed arm; retain the p99 duration and sample count so this derived value is auditable.",
				Availability: "live-workload-required",
				Reason:       "A synthetic benchmark and present-call timing cannot define displayed-frame 1% lows.",
			},
			{
				ID:           "process-cpu-time",
				Unit:         "nanoseconds user and system CPU time",
				Definition:   "For runner samples, direct benchmark-test-binary getrusage user/system CPU. For live arms, use the profiled client process over the recorded interval.",
				Availability: "runner-and-live",
			},
			{
				ID:           "go-allocations",
				Unit:         "bytes per operation and allocations per operation",
				Definition:   "testing.B ReportAllocs output for each bounded benchmark; it excludes native allocator activity.",
				Availability: "runner",
			},
			{
				ID:           "native-allocations",
				Unit:         "bytes and allocations by native allocation domain",
				Definition:   "Owner-provided aggregate allocation counters around the exact native boundary, reset and read only at arm boundaries.",
				Availability: "owner-seam-required",
				Reason:       "Go benchmark B/op must not be relabeled as native allocation data.",
			},
			{
				ID:           "jni-synthetic-mutf-reference-boundary",
				Unit:         "synthetic direct-vtable timing and fixture lifecycle only",
				Definition:   "The tagged JNI fixture creates global references before each timed native-pthread loop, exercises corrected MUTF-8 construction and related synthetic vtable calls, then releases fixture globals during teardown. Global-reference multiplicity is correctness state, not a timed live lifetime metric.",
				Availability: "runner-fixture",
				Reason:       "It cannot attribute live string/reference lifetime, retained bytes, call frequency, CPU/RSS/wakeups, frame tails, or FPS. Go B/op is fixture allocation output, not native retained-memory accounting.",
			},
			{
				ID:           "audio-queue-ownership",
				Unit:         "content-free C bridge event counts and retained bytes per fixture invocation",
				Definition:   "The bounded fake-player fixture records capacity, resident/free nodes, queued/in-flight entries, callbacks, C bridge node/payload allocation events, payload/copy bytes, reclamations, full-queue rejections, and caller-copy preservation.",
				Availability: "runner-fixture",
				Reason:       "These are queue ownership counters, not native allocator CPU, process RSS, OS wakeups, device latency, or FPS.",
			},
			{
				ID:           "muted-capture-cadence",
				Unit:         "content-free callback count and monotonic nanoseconds per fixture invocation",
				Definition:   "The fake-muted recorder fixture records callback interval p50/p95/p99, synchronous callback-to-requeue p50/p95/p99, its scheduled interval, deadline waits, and missed-deadline clamps without opening or reading a microphone.",
				Availability: "runner-fixture",
				Reason:       "Callback-to-requeue duration and fake cadence are not end-to-end audio latency, host wakeups, CPU attribution, RSS, or FPS.",
			},
			{
				ID:           "audio-retry-interruptions",
				Unit:         "content-free retry state counts per fixture invocation",
				Definition:   "The Audio-owned fake retry fixture reports retry waits, retry interruptions, rate-limited error emissions/suppressions, callbacks, queued, and in-flight entries.",
				Availability: "owner-seam-not-exported",
				Reason:       "The result is currently package-private; internal/perf must not parse test logs or bypass Audio ownership to manufacture a runner metric.",
			},
			{
				ID:           "rss",
				Unit:         "bytes",
				Definition:   "For runner samples, Linux direct benchmark-test-binary ru_maxrss converted from KiB to bytes. For live arms, record process RSS high-water and interval snapshots separately from evictable cache bytes.",
				Availability: "runner-and-live",
			},
			{
				ID:           "wakeups",
				Unit:         "count per interval",
				Definition:   "Record owner readiness wake events and timeout/recovery wakes separately. Runner rusage context-switch counts are included only as scheduler context-switch metadata and are not wakeup counts.",
				Availability: "owner-seam-required",
				Reason:       "A scheduler context switch does not prove an evdev, inotify, audio, or input wakeup.",
			},
			{
				ID:           "controller-idle-readiness-counts",
				Unit:         "aggregate counts per bounded empty-watch fixture invocation",
				Definition:   "The Linux ReadyPump fixture records initial/hotplug/recovery rescans, evdev-ready, inotify-ready, shutdown-wake, frame-handoff, and ready-to-frame host-work fields. In an empty healthy watch it must be initial=1, shutdown=1, and every other field=0.",
				Availability: "runner-fixture",
				Reason:       "This is an owner-side synthetic readiness aggregate, not client CPU, a physical-input latency, or a legacy-ticker comparison.",
			},
			{
				ID:           "input-latency",
				Unit:         "nanoseconds",
				Definition:   "Monotonic elapsed time from an input readiness/read boundary to completion of the corresponding host-to-guest dispatch, summarized as count/p50/p95/p99/max without input payloads.",
				Availability: "owner-seam-required",
				Reason:       "Requires the Input owner to mark real dispatch boundaries without changing SYN_REPORT ordering.",
			},
			{
				ID:           "audio-latency",
				Unit:         "nanoseconds",
				Definition:   "Monotonic elapsed time from a completed audio queue callback to its successful requeue or intentional stop, summarized as count/p50/p95/p99/max without PCM capture.",
				Availability: "owner-seam-required",
				Reason:       "The available fake callback-to-requeue aggregate is synchronous bridge work, not device or end-to-end latency.",
			},
		},
		Cases: []WorkloadCase{
			{
				ID:             "jni-mutf-reference-correctness-baseline",
				Subsystem:      "corrected JNI MUTF-8 and global-reference fixture",
				Availability:   "runner-fixture",
				Reason:         "The direct native-pthread fixture rebaselines corrected synthetic MUTF-8 construction; its global references are pre-loop setup/teardown, not a measurement of a production reference lifetime or call rate.",
				RequiredMetric: []string{"jni-synthetic-mutf-reference-boundary", "process-cpu-time", "go-allocations"},
				OwnerHandoff:   "JNI: keep the corrected lifetime semantics unchanged. A cache or lifetime candidate requires a privacy-filtered matched live aggregate for call frequency, reference lifetime, and retained bytes, plus separate clean acceptance.",
			},
			{
				ID:             "muted-audio-cadence",
				Subsystem:      "OpenSL capture mute path",
				Availability:   "runner-fixture",
				Reason:         "The public fake-muted fixture reports cadence and synchronous requeue fields, but not CPU, RSS, OS wakeups, native allocation, device latency, or FPS.",
				RequiredMetric: []string{"muted-capture-cadence", "go-allocations"},
				OwnerHandoff:   "Audio fixture is available through internal/android/opensles_pacing.go. A visible claim still needs matched live capture and separate clean audible/mute/unmute/shutdown acceptance.",
			},
			{
				ID:             "audio-queue-storage",
				Subsystem:      "OpenSL player queue bridge",
				Availability:   "runner-fixture",
				Reason:         "The public bounded fake-player fixture exposes only queue ownership, bridge allocation/copy events, retained capacity, callbacks, and full-queue handling.",
				RequiredMetric: []string{"audio-queue-ownership", "go-allocations"},
				OwnerHandoff:   "Audio fixture is available through internal/android/opensles_pacing.go. Do not turn bridge counter changes into allocator CPU, RSS, audio latency, or FPS claims.",
			},
			{
				ID:             "audio-retry-interruption",
				Subsystem:      "OpenSL retry deadline and error-rate limiting",
				Availability:   "owner-seam-not-exported",
				Reason:         "Audio has a deterministic fake retry result, but its Go wrapper is package-private and cannot be reported by internal/perf without an Audio-owned export.",
				RequiredMetric: []string{"audio-retry-interruptions"},
				OwnerHandoff:   "Audio: export only the existing fixed-size retry fixture result; preserve its fake endpoint, content-free fields, and no-device boundary. Do not add a timing or cgo redesign.",
			},
			{
				ID:             "controller-idle-wakeup",
				Subsystem:      "evdev/inotify controller manager",
				Availability:   "runner-fixture",
				Reason:         "A bounded Linux empty-watch fixture records its ReadyPump owner-side aggregate. It cannot establish live-client CPU, physical-input latency, or a causal prepatch ticker comparison.",
				RequiredMetric: []string{"controller-idle-readiness-counts", "process-cpu-time", "go-allocations", "rss"},
				OwnerHandoff:   "Input: the fixture is available in internal/gamepad/readiness_linux.go. Any visible controller benefit still needs matched live aggregate/frame-tail capture plus a separate clean connect/input/hotplug/shutdown acceptance run.",
			},
			{
				ID:             "controller-translation-allocation",
				Subsystem:      "evdev frame to Android frame translation",
				Availability:   "runner",
				Reason:         "A fixed public xpad-like capability/frame fixture can measure current translation cost and Go allocations without a device or gameplay claim.",
				RequiredMetric: []string{"go-allocations", "process-cpu-time"},
			},
			{
				ID:             "concurrent-asset-read",
				Subsystem:      "asset lookup/load/publication",
				Availability:   "owner-seam-required",
				Reason:         "The current asset benchmark measures first and cached serial opens only; it cannot validly stage a blocked cold load beside an unrelated hot hit from another package.",
				RequiredMetric: []string{"process-cpu-time", "go-allocations", "native-allocations", "input-latency"},
				OwnerHandoff:   "Filesystem: add a test-only controlled reader/decompress barrier plus content-free cache snapshot in internal/android/asset.go and asset_test.go, then benchmark cold-key stall versus unrelated hot-key completion without changing archive lifetime semantics.",
			},
			{
				ID:             "long-session-cache-retention",
				Subsystem:      "asset cache and native asset pin lifetimes",
				Availability:   "owner-seam-required",
				Reason:         "The cache has no public byte/active-handle/in-flight/pin snapshot, so RSS alone cannot distinguish live working set from evictable retention.",
				RequiredMetric: []string{"rss", "native-allocations"},
				OwnerHandoff:   "Filesystem: expose a test-only aggregate snapshot of live bytes, evictable cached bytes, pinned/borrowed bytes, active handles, and in-flight loads; drive a deterministic multi-generation fixture with no asset names or payloads in output.",
			},
		},
	}
}
