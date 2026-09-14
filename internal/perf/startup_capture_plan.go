package perf

// StartupBuildCapturePlan keeps startup/build timing separate from gameplay
// evidence. It describes exactly which phases are runnable from a checkout
// without opening user-owned package/account state and which need an owning
// subsystem to supply a bounded content-free seam.
type StartupBuildCapturePlan struct {
	Diagnostic   StartupCaptureMode `json:"diagnostic"`
	CleanControl StartupCaptureMode `json:"clean_control"`
	Phases       []StartupPhase     `json:"phases"`
	PGO          PGOReadiness       `json:"pgo"`
}

// StartupCaptureMode is a separate evidence lane. Diagnostic results cannot
// be compared to an uninstrumented control/acceptance record.
type StartupCaptureMode struct {
	ID         string   `json:"id"`
	Purpose    string   `json:"purpose"`
	Required   []string `json:"required"`
	Prohibited []string `json:"prohibited"`
}

// StartupPhase states the smallest safe measurement surface for one startup
// stage. An unavailable phase is an intentional result, not a zero-cost
// estimate.
type StartupPhase struct {
	ID           string `json:"id"`
	Subsystem    string `json:"subsystem"`
	Availability string `json:"availability"`
	SafeSeam     string `json:"safe_seam,omitempty"`
	Reason       string `json:"reason"`
	OwnerHandoff string `json:"owner_handoff,omitempty"`
}

// PGOReadiness records the inputs required before changing build settings. A
// microbenchmark or a launcher-control profile is never representative PGO
// data for a client/gameplay workload.
type PGOReadiness struct {
	RepresentativeProfile bool   `json:"representative_profile"`
	MatchingDebugArtifact bool   `json:"matching_debug_artifact"`
	Reason                string `json:"reason"`
}

// StartupBuildTimingPlan is immutable runner metadata. It does not enable a
// diagnostic, change durable-write policy, or choose compiler flags.
func StartupBuildTimingPlan() StartupBuildCapturePlan {
	return StartupBuildCapturePlan{
		Diagnostic: StartupCaptureMode{
			ID:      "startup-diagnostic",
			Purpose: "Measure a bounded content-free fixture with diagnostic/profiling artifacts, separately from clean control or visible acceptance.",
			Required: []string{
				"same source revision, runner policy, host metadata, and raw artifact for every arm",
				"only an existing public or package-owned deterministic fixture",
			},
			Prohibited: []string{
				"using a diagnostic result as a clean acceptance result",
				"opening user account, cookie, package, or configuration state from a generic runner",
				"claiming client startup, frame-time, FPS, or steady-state CPU from a fixture",
			},
		},
		CleanControl: StartupCaptureMode{
			ID:      "startup-clean-control",
			Purpose: "Record uninstrumented, ephemeral-XDG CLI/GUI control commands separately from diagnostics; it is not visible client acceptance.",
			Required: []string{
				"exact allowlisted control command and fresh private XDG roots",
				"wall time, direct-process rusage, exit status, and output digest without retained output",
			},
			Prohibited: []string{
				"using a clean control as one side of a diagnostic comparison",
				"claiming GUI visibility, Login, client startup, account isolation, or gameplay acceptance",
			},
		},
		Phases: []StartupPhase{
			{
				ID:           "startup-verification",
				Subsystem:    "APK/package verification",
				Availability: "fixture-required",
				Reason:       "Signature verification requires an explicitly authorized representative APK and signing blocks; the checkout contains no safe corpus for a generic runner.",
				OwnerHandoff: "Packaging/APK: provide a sealed, non-secret representative fixture or a content-free owner benchmark that preserves full verification and records corpus identity/digest metadata.",
			},
			{
				ID:           "startup-extraction",
				Subsystem:    "APK x86-64 extraction",
				Availability: "fixture-required",
				Reason:       "apk.Extract requires real authorized APK inputs and a disposable destination; a generic runner must not substitute a smaller corpus or mutate a user runtime.",
				OwnerHandoff: "APK/Packaging: supply a sealed representative extraction corpus and private disposable destination protocol that preserves hash and durable-write checks.",
			},
			{
				ID:           "startup-relocation",
				Subsystem:    "ELF APS2 packed relocation",
				Availability: "runner-fixture",
				SafeSeam:     "internal/loader BenchmarkAPS2PackedRelocApply (65536 synthetic RELATIVE records)",
				Reason:       "The existing loader-owned benchmark can be sampled directly; it is synthetic relocation work, not guest loading or client startup.",
			},
			{
				ID:           "startup-storage-hardening",
				Subsystem:    "generation/integrity durable storage",
				Availability: "owner-seam-required",
				Reason:       "Existing integrity operations require trusted generation files and durable filesystem transitions but expose no bounded public content-free stage/open fixture.",
				OwnerHandoff: "Integrity/Packaging: expose an owner-controlled fixture around the exact verification, fsync, rename, and descriptor-pinning contract; do not weaken durable writes for timing.",
			},
			{
				ID:           "startup-jni-table-publication",
				Subsystem:    "initial JNI native-interface table publication",
				Availability: "owner-seam-required",
				Reason:       "jni.NewVM also seeds process-global VM/X11 bridge state and has no resettable publish-only public fixture, so a loop would measure unrelated lifecycle state.",
				OwnerHandoff: "JNI: add only a bounded, content-free publish/reset fixture that proves no production VM behavior changes; include no caller, handle, class, string, or account data.",
			},
			{
				ID:           "startup-client",
				Subsystem:    "actual client startup",
				Availability: "live-workload-required",
				Reason:       "Representative startup needs an isolated approved runtime and visible lifecycle acceptance; a timeout or probe cannot represent startup cost or usable UI.",
			},
			{
				ID:           "startup-gui-clean-control",
				Subsystem:    "Qt GUI executable",
				Availability: "unavailable-no-finite-safe-command",
				Reason:       "tipsy-gui constructs QApplication before application workflow and has no built-in bounded command that exits after a representative GUI initialization. A timeout, --help abort, or unobserved window is not startup or visibility timing.",
				OwnerHandoff: "Qt GUI: if needed, provide a content-free explicit finite readiness/control seam plus an X11 observer contract; it must not read user settings or imply visible acceptance.",
			},
		},
		PGO: PGOReadiness{
			RepresentativeProfile: false,
			MatchingDebugArtifact: false,
			Reason:                "No matched warmed gameplay Go CPU profile and no same-build debug-artifact pairing exist. Fixture or control-command profiles must not drive PGO or build-setting edits.",
		},
	}
}
