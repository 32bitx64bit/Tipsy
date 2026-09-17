package perf

// ProgramCapturePlan describes whole-process profiling of a real tipsy
// binary. Isolated package microbenchmarks cannot rank live hotspots because
// they never share the production process, CGO boundary, or guest present path.
type ProgramCapturePlan struct {
	Instrumented CaptureMode    `json:"instrumented"`
	Clean        CaptureMode    `json:"clean_acceptance"`
	Measurements []Measurement  `json:"measurements,omitempty"`
	Workloads    []WorkloadCase `json:"workloads"`
}

// NativeCPUProfileMeasurementID is the ranked userspace perf-record listing.
// It is not displayed FPS and must not be compared to a perf-off arm.
const NativeCPUProfileMeasurementID = "native-cpu-profile-hotspots"

// NativeCPUProfileMeasurement ranks sampled userspace CPU by symbol. Go
// pprof reports native/CGO/guest time as runtime._LostExternalCode; this
// measurement is the follow-up, not an FPS or optimization result.
func NativeCPUProfileMeasurement() Measurement {
	return Measurement{
		ID:           NativeCPUProfileMeasurementID,
		Unit:         "percent of sampled userspace CPU by symbol",
		Definition:   "Ranked userspace perf record samples of one linked tipsy process, reported as function names and percentages only. Present-call samples are not displayed-frame durations. This is not FPS, 1% lows, or a perf-on versus perf-off optimization result.",
		Availability: "live-workload-required",
		Reason:       "Go runtime/pprof attributes native/CGO/guest time as runtime._LostExternalCode. Ranking those symbols requires host perf_event access during a live launch. Missing perf or a blocking perf_event_paranoid value is recorded as unavailable, not as zeros.",
	}
}

// WholeProgramCLICommands is the bounded argv set exercised by
// BenchmarkWholeProgramCLI. It never inspects APKs, launches the client,
// writes user configuration, or opens a microphone.
func WholeProgramCLICommands() [][]string {
	commands := [][]string{
		{"version"},
		{"help"},
		{"logs"},
		{"config", "path"},
		{"doctor"},
		{"diagnose"},
	}
	for _, subsystem := range []string{"x11", "graphics", "audio", "jni", "loader", "roblox", "auth", "gamepad"} {
		commands = append(commands, []string{"diagnose", subsystem})
	}
	return commands
}

// WholeProgramCapturePlan is immutable runner metadata. Enabling
// TIPSY_PPROF_DIR is an instrumented arm and must not be compared to an
// unprofiled launch as an FPS result.
func WholeProgramCapturePlan() ProgramCapturePlan {
	return ProgramCapturePlan{
		Instrumented: CaptureMode{
			ID:      "whole-program-instrumented",
			Purpose: "Rank CPU, allocation, and goroutine cost inside one real tipsy process while privacy-safe pprof files are written.",
			Required: []string{
				"the linked production binary, not a per-package test binary",
				"TIPSY_PPROF_DIR process-wide CPU plus heap/alloc dumps",
				"ranked pprof -top hotspots with function names only",
				"userspace perf record call-graph when -program-perf is set",
			},
			Prohibited: []string{
				"treating a doctor/diagnose profile as client or gameplay hotspots",
				"comparing a profiled launch to an unprofiled launch as an FPS or frame-time result",
				"claiming displayed FPS or 1% lows from perf record or pprof samples",
				"comparing a perf-record arm to a perf-off arm as an optimization result",
				"retaining stdout/stderr, cookies, account, path payloads, instruction dumps, or microphone samples in the report",
			},
		},
		Clean: CaptureMode{
			ID:      "whole-program-clean",
			Purpose: "Confirm the same argv still behaves correctly with TIPSY_PPROF_DIR unset.",
			Required: []string{
				"same binary and argv as the instrumented arm",
				"no process-wide pprof rates",
			},
			Prohibited: []string{
				"using the clean arm's wall time as a hotspot ranking",
			},
		},
		Measurements: []Measurement{NativeCPUProfileMeasurement()},
		Workloads: []WorkloadCase{
			{
				ID:             "whole-program-cli-surface",
				Subsystem:      "linked CLI dispatch, doctor, and diagnose",
				Availability:   "runner",
				Reason:         "With no extra argv, tipsy-perf -program loops WholeProgramCLICommands inside the linked tipsy binary for -program-duration (default 5s) so CPU/heap profiles have samples. Explicit argv such as doctor still profiles that one command. This does not load libroblox or present a frame.",
				RequiredMetric: []string{"process-cpu-time", "go-allocations", "go-cpu-profile-hotspots"},
				OwnerHandoff:   "tipsy-perf -program -out report.json  # default 5s linked-binary CLI surface; add -- doctor for one command; add -- launch with -program-duration and DISPLAY for the client",
			},
			{
				ID:             "whole-program-client-launch",
				Subsystem:      "tipsy launch",
				Availability:   "live-workload-required",
				Reason:         "Accurate client hotspots require the official x86-64 client on X11. tipsy-perf -program -- launch records process-wide pprof for a bounded duration. -program-perf adds a userspace perf record of the same process. Neither is an FPS result.",
				RequiredMetric: []string{"process-cpu-time", "rss", "go-cpu-profile-hotspots", NativeCPUProfileMeasurementID},
				OwnerHandoff:   "Run DISPLAY=:0 tipsy-perf -program -program-perf -program-duration=180s -out report.json -- launch https://www.roblox.com/games/920587237/Natural-Disaster-Survival against an already-installed official client with saved unlimited FPS. Close any extra Tipsy GUI first so the client lock is free. Missing perf is recorded as unavailable, not zeros.",
			},
		},
	}
}
