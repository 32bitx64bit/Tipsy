package perf

import "testing"

func TestStartupBuildTimingPlanKeepsSeparatedEvidenceAndUnavailableSpans(t *testing.T) {
	plan := StartupBuildTimingPlan()
	if plan.Diagnostic.ID != "startup-diagnostic" || plan.CleanControl.ID != "startup-clean-control" || plan.PGO.RepresentativeProfile || plan.PGO.MatchingDebugArtifact {
		t.Fatalf("startup plan modes/readiness = %#v", plan)
	}
	want := map[string]string{
		"startup-verification":          "fixture-required",
		"startup-extraction":            "fixture-required",
		"startup-relocation":            "runner-fixture",
		"startup-storage-hardening":     "owner-seam-required",
		"startup-jni-table-publication": "owner-seam-required",
		"startup-client":                "live-workload-required",
		"startup-gui-clean-control":     "unavailable-no-finite-safe-command",
	}
	got := make(map[string]StartupPhase, len(plan.Phases))
	for _, phase := range plan.Phases {
		got[phase.ID] = phase
	}
	for id, availability := range want {
		phase, ok := got[id]
		if !ok || phase.Availability != availability || phase.Reason == "" {
			t.Errorf("phase %q = %#v", id, phase)
		}
	}
	if phase := got["startup-relocation"]; phase.SafeSeam == "" {
		t.Fatalf("relocation safe seam = %#v", phase)
	}
	if phase := got["startup-jni-table-publication"]; phase.OwnerHandoff == "" {
		t.Fatalf("JNI owner handoff = %#v", phase)
	}
	if phase := got["startup-gui-clean-control"]; phase.OwnerHandoff == "" {
		t.Fatalf("GUI owner handoff = %#v", phase)
	}
}
