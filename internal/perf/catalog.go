// Package perf defines Tipsy's reproducible project-performance matrix.
//
// It deliberately describes only host-owned work.  A record marked
// unavailable is evidence that a workload needs a controlled fixture or
// user-driven gameplay capture; it is not a zero-cost claim.
package perf

import "sort"

// Kind describes how a subsystem can be measured without changing account,
// game, hardware, or desktop state.
type Kind string

const (
	// Microbenchmark is a bounded, deterministic benchmark the runner executes.
	Microbenchmark Kind = "microbenchmark"
	// ProgramProfile is a whole-process capture of a linked tipsy binary.
	// The default runner records it as unavailable; tipsy-perf -program runs it.
	ProgramProfile Kind = "program-profile"
	// FixtureRequired needs an input artifact or host resource that is not part
	// of a standard, privacy-safe checkout.
	FixtureRequired Kind = "fixture-required"
	// LiveRequired is meaningful only in an actual, matched user-driven game.
	LiveRequired Kind = "live-workload-required"
	// ExternalBoundary is dominated by an OS, driver, remote peer, or desktop
	// component and is not a stable Go microbenchmark.
	ExternalBoundary Kind = "external-boundary"
	// BuildOnly is not resident in the running client.
	BuildOnly Kind = "build-only"
	// SupportTool is project tooling, not a client workload.
	SupportTool Kind = "support-tool"
)

// Entry is a stable subsystem identifier. Benchmark is a Go regular
// expression matching the benchmarks run for a Microbenchmark entry. Tags are
// required build tags, if any.
type Entry struct {
	ID            string   `json:"id"`
	Package       string   `json:"package"`
	RunnerPackage string   `json:"runner_package,omitempty"`
	Subsystem     string   `json:"subsystem"`
	Kind          Kind     `json:"kind"`
	Benchmark     string   `json:"benchmark,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

// Catalog is intentionally complete for `go list ./...`. Keep IDs stable: a
// result comparison uses ID plus benchmark name, not package order.
func Catalog() []Entry {
	entries := []Entry{
		{ID: "cli", Package: "./cmd/tipsy", Subsystem: "headless command wiring", Kind: LiveRequired, Reason: "Launch, inspect, and setup perform user-selected filesystem or client work; a synthetic loop would not model a user session."},
		{ID: "whole-program", Package: "./cmd/tipsy", Subsystem: "linked tipsy process CPU/heap profile", Kind: ProgramProfile, Reason: "Accurate hotspots require one real tipsy process. tipsy-perf -program profiles the linked binary: no extra argv loops the checkout-safe CLI surface; launch is live-workload-required and needs DISPLAY plus -program-duration."},
		{ID: "whole-program-cli", Package: "./internal/app", RunnerPackage: "./internal/perf", Subsystem: "whole CLI dispatch in one process", Kind: Microbenchmark, Benchmark: "^BenchmarkWholeProgramCLI$", Reason: "Runs version/help/logs/config path/doctor/diagnose through app.Run in one process so a CPU profile ranks CLI and host-probe cost. It excludes APK inspect, setup, launch, and gameplay."},
		{ID: "gui", Package: "./cmd/tipsy-gui", Subsystem: "Qt entrypoint", Kind: ExternalBoundary, Reason: "Qt event-loop and compositor timing require a visible X11 session and must not be driven by the baseline runner."},
		{ID: "android", Package: "./internal/android", Subsystem: "Android ABI, EGL, Vulkan, asset, and thread shims", Kind: Microbenchmark, Benchmark: "^(BenchmarkVulkanPresentTiming|BenchmarkVulkanPresentStats|BenchmarkEGLSwapStats|BenchmarkOpenAssetBytes|BenchmarkAssetStartupOpenClose|BenchmarkGettid)$"},
		{ID: "audio-opensl-queue", Package: "./internal/android", RunnerPackage: "./internal/perf", Subsystem: "bounded OpenSL fake-player queue ownership", Kind: Microbenchmark, Benchmark: "^BenchmarkOpenSLQueueOwnership$", Reason: "Reports fixed C bridge queue-storage/copy/callback counters only; it excludes device I/O, allocator CPU, RSS, OS wakeups, audio latency, and FPS."},
		{ID: "audio-muted-cadence", Package: "./internal/android", RunnerPackage: "./internal/perf", Subsystem: "bounded OpenSL fake-muted recorder cadence", Kind: Microbenchmark, Benchmark: "^BenchmarkMutedCaptureCadence$", Reason: "Reports fake-host callback cadence and synchronous callback-to-requeue duration only; it excludes microphone I/O, CPU attribution, RSS, OS wakeups, end-to-end latency, and FPS."},
		{ID: "apk", Package: "./internal/apk", Subsystem: "APK inspection and signature verification", Kind: FixtureRequired, Reason: "Meaningful cost depends on an APK's size and signing blocks; the official APK is not a test fixture in the checkout."},
		{ID: "app", Package: "./internal/app", Subsystem: "CLI command dispatch", Kind: LiveRequired, Reason: "Dispatch is cold control-plane work whose meaningful cost includes the selected command's real side effects."},
		{ID: "clientsettings", Package: "./internal/clientsettings", Subsystem: "Roblox client-settings persistence", Kind: FixtureRequired, Reason: "Uses user-owned ClientSettings XML and atomic files; benchmark only with a redacted representative fixture."},
		{ID: "compat", Package: "./internal/compat", Subsystem: "compatibility report assembly", Kind: FixtureRequired, Reason: "Cost is driven by APK and ELF report cardinality; it needs a versioned synthetic fixture."},
		{ID: "config", Package: "./internal/config", Subsystem: "shared JSON persistence", Kind: FixtureRequired, Reason: "UpdateJSON deliberately takes an interprocess lock and writes a private file; never benchmark against a user's live config."},
		{ID: "desktop", Package: "./internal/desktop", Subsystem: "desktop integration", Kind: ExternalBoundary, Reason: "xdg-mime, icon caches, and desktop files are installation actions, not a running-client hot path."},
		{ID: "diagnostics", Package: "./internal/diagnostics", Subsystem: "doctor and diagnostics", Kind: ExternalBoundary, Reason: "Reads host services and proc state; results are host-health diagnostics rather than client-frame work."},
		{ID: "discord", Package: "./internal/discord", Subsystem: "Discord rich presence", Kind: ExternalBoundary, Reason: "IPC and catalog lookup are opt-in network/socket work; benchmark only with a controlled local peer."},
		{ID: "elfinspect", Package: "./internal/elfinspect", Subsystem: "ELF inspection", Kind: FixtureRequired, Reason: "Parsing cost is proportional to a supplied ELF; use a redacted fixed ELF fixture, never libroblox.so in source."},
		{ID: "gamepad", Package: "./internal/gamepad", RunnerPackage: "./internal/perf", Subsystem: "evdev frame to Android-frame translation", Kind: Microbenchmark, Benchmark: "^BenchmarkGamepadTranslation$", Reason: "Uses the fixed public xpad-shaped fixture in internal/perf; it excludes device discovery, readiness wakeups, and gameplay latency."},
		{ID: "gamepad-ready-pump", Package: "./internal/gamepad", RunnerPackage: "./internal/perf", Subsystem: "bounded ReadyPump healthy empty-watch aggregate", Kind: Microbenchmark, Benchmark: "^BenchmarkControllerIdleReadiness$", Reason: "Runs a private empty watched directory for 75ms and records only owner-side readiness counts; it does not measure physical input latency, live client CPU, or legacy-ticker causality."},
		{ID: "graphics", Package: "./internal/graphics", Subsystem: "renderer capability policy", Kind: ExternalBoundary, Reason: "Renderer availability is a driver/GL/Vulkan query and is not a stable host-independent loop."},
		{ID: "graphics-flickercap", Package: "./internal/graphics/flickercap", Subsystem: "present/flicker capture", Kind: LiveRequired, Reason: "Requires a visible X11 drawable and matched render cadence; Home or synthetic pixels do not represent gameplay."},
		{ID: "gui-model", Package: "./internal/gui", Subsystem: "Qt GUI model and widgets", Kind: ExternalBoundary, Reason: "Widget layout, MIQT callbacks, and compositor work require a visible desktop; an offscreen loop would not represent interactive cost."},
		{ID: "integrity", Package: "./internal/integrity", Subsystem: "trusted artifact store", Kind: FixtureRequired, Reason: "Hashing and atomic promotion depend on artifact sizes and storage; use sealed synthetic artifacts for a future fixture suite."},
		{ID: "jni", Package: "./internal/jni", Subsystem: "synthetic JNI vtable/MUTF-8 and reference fixture boundary", Kind: Microbenchmark, Benchmark: "^(BenchmarkJNINative.*|BenchmarkExceptionCheck|BenchmarkJNILocalRef|BenchmarkDispatchCoreHit|BenchmarkInternedStringHit)$", Tags: []string{"tipsy_perfbench"}, Reason: "Runs corrected synthetic MUTF-8 construction and direct-vtable fixtures; global references are setup/teardown outside timed loops. It excludes live string/reference lifetime, retained bytes, call frequency, CPU/RSS/wakeups, and gameplay/FPS attribution."},
		{ID: "loader", Package: "./internal/loader", Subsystem: "APS2 packed relocation application", Kind: Microbenchmark, Benchmark: "^BenchmarkAPS2PackedRelocApply$"},
		{ID: "logging", Package: "./internal/logging", Subsystem: "privacy redaction", Kind: Microbenchmark, Benchmark: "^BenchmarkRedact$"},
		{ID: "mic", Package: "./internal/mic", Subsystem: "microphone capture", Kind: ExternalBoundary, Reason: "PipeWire/Pulse device and scheduling behavior require a user-selected host source; baseline runner must not open capture devices."},
		{ID: "perf", Package: "./internal/perf", Subsystem: "performance catalog and controlled fixtures", Kind: SupportTool, Reason: "Owns the runner and synthetic fixtures; it is not itself a client subsystem."},
		{ID: "perf-runner", Package: "./internal/perf/cmd/tipsy-perf", Subsystem: "performance runner", Kind: SupportTool, Reason: "Orchestration overhead is recorded per command but is not client work."},
		{ID: "rbxuri", Package: "./internal/rbxuri", RunnerPackage: "./internal/perf", Subsystem: "Roblox URI parsing and handoff", Kind: Microbenchmark, Benchmark: "^BenchmarkRbxURIParse$", Reason: "Executed by the internal/perf control benchmark to avoid creating a second ad-hoc package runner."},
		{ID: "runtime", Package: "./internal/runtime", Subsystem: "GameActivity display-refresh publication", Kind: Microbenchmark, Benchmark: "^BenchmarkDisplayRefreshPublicationUnchanged$"},
		{ID: "securitypolicy", Package: "./internal/securitypolicy", Subsystem: "signed policy validation", Kind: FixtureRequired, Reason: "Security policy is setup/control-plane work; benchmark only against fixed signed policy fixtures, never weaken validation for a loop."},
		{ID: "setupsvc", Package: "./internal/setupsvc", Subsystem: "official package setup", Kind: FixtureRequired, Reason: "Uses user-authorized APK inputs and storage; a benchmark requires a sealed synthetic package corpus."},
		{ID: "version", Package: "./internal/version", Subsystem: "build-version selection", Kind: SupportTool, Reason: "Constant-time build metadata access is not a client hot path."},
		{ID: "x11", Package: "./internal/x11", Subsystem: "X11 input queue drain and callback publication", Kind: Microbenchmark, Benchmark: "^(BenchmarkDrainInputLocked|BenchmarkDrainInputLockedPointers|BenchmarkNotifyInput|BenchmarkDrainInputLockedBatch|BenchmarkDrainInputLockedText|BenchmarkInputDrainDiagnosticsDisabled|BenchmarkInputDrainDiagnosticsEnabled)$"},
		{ID: "x11probe", Package: "./internal/x11/x11probe", Subsystem: "X11 integration probe", Kind: ExternalBoundary, Reason: "Purpose-built test client sends real X11 requests and must run only in a visible integration test."},
		{ID: "appimage", Package: "./packaging/appimage", Subsystem: "AppImage assembly and guards", Kind: BuildOnly, Reason: "Packaging is offline build work, not resident client execution."},
		{ID: "arch-package", Package: "./packaging/arch", Subsystem: "Arch package rendering", Kind: BuildOnly, Reason: "Packaging is offline build work, not resident client execution."},
		{ID: "flatpak", Package: "./packaging/flatpak", Subsystem: "Flatpak manifest rendering", Kind: BuildOnly, Reason: "Packaging is offline build work, not resident client execution."},
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries
}
