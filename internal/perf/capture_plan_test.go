package perf

import (
	"strings"
	"testing"
)

func TestHostOverheadCapturePlanCoversRequestedCases(t *testing.T) {
	plan := HostOverheadCapturePlan()
	if plan.Instrumented.ID != "instrumented-capture" || plan.Clean.ID != "clean-acceptance" {
		t.Fatalf("capture modes = %#v / %#v", plan.Instrumented, plan.Clean)
	}
	if !contains(plan.Instrumented.Prohibited, "comparing observer-enabled data to clean data as an optimization result") {
		t.Fatalf("instrumented plan must forbid clean/instrumented comparison: %#v", plan.Instrumented.Prohibited)
	}

	want := map[string]string{
		"jni-mutf-reference-correctness-baseline": "runner-fixture",
		"muted-audio-cadence":                     "runner-fixture",
		"audio-queue-storage":                     "runner-fixture",
		"audio-retry-interruption":                "owner-seam-not-exported",
		"controller-idle-wakeup":                  "runner-fixture",
		"controller-translation-allocation":       "runner",
		"concurrent-asset-read":                   "owner-seam-required",
		"long-session-cache-retention":            "owner-seam-required",
	}
	got := make(map[string]WorkloadCase, len(plan.Cases))
	for _, workload := range plan.Cases {
		if _, duplicate := got[workload.ID]; duplicate {
			t.Fatalf("duplicate workload ID %q", workload.ID)
		}
		got[workload.ID] = workload
	}
	for id, availability := range want {
		workload, ok := got[id]
		if !ok {
			t.Errorf("missing workload %q", id)
			continue
		}
		if workload.Availability != availability || len(workload.RequiredMetric) == 0 {
			t.Errorf("workload %q = %#v", id, workload)
		}
		if (availability == "owner-seam-required" || availability == "runner-fixture" || availability == "owner-seam-not-exported") && workload.OwnerHandoff == "" {
			t.Errorf("workload %q lacks a minimal owner handoff", id)
		}
	}
}

func TestHostOverheadCapturePlanKeepsAudioFixtureBoundaries(t *testing.T) {
	plan := HostOverheadCapturePlan()
	measurements := make(map[string]Measurement, len(plan.Measurements))
	for _, measurement := range plan.Measurements {
		measurements[measurement.ID] = measurement
	}
	for _, tc := range []struct {
		id           string
		availability string
		mustContain  string
	}{
		{"audio-queue-ownership", "runner-fixture", "not native allocator CPU"},
		{"muted-capture-cadence", "runner-fixture", "not end-to-end audio latency"},
		{"audio-retry-interruptions", "owner-seam-not-exported", "must not parse test logs"},
	} {
		measurement, ok := measurements[tc.id]
		if !ok || measurement.Availability != tc.availability || !strings.Contains(measurement.Reason, tc.mustContain) {
			t.Errorf("audio measurement %q = %#v", tc.id, measurement)
		}
	}
}

func TestHostOverheadCapturePlanKeepsJNISyntheticBoundary(t *testing.T) {
	plan := HostOverheadCapturePlan()
	for _, measurement := range plan.Measurements {
		if measurement.ID != "jni-synthetic-mutf-reference-boundary" {
			continue
		}
		if measurement.Availability != "runner-fixture" || !strings.Contains(measurement.Definition, "Global-reference multiplicity is correctness state") || !strings.Contains(measurement.Reason, "live string/reference lifetime, retained bytes, call frequency") || !strings.Contains(measurement.Reason, "not native retained-memory accounting") {
			t.Fatalf("jni synthetic boundary = %#v", measurement)
		}
		return
	}
	t.Fatal("missing JNI synthetic boundary")
}

func TestHostOverheadCapturePlanDefinesOnePercentLow(t *testing.T) {
	plan := HostOverheadCapturePlan()
	for _, measurement := range plan.Measurements {
		if measurement.ID != "one-percent-low" {
			continue
		}
		if measurement.Unit != "frames per second" || !strings.Contains(measurement.Definition, "1e9 / nearest-rank p99") {
			t.Fatalf("1%% low definition = %#v", measurement)
		}
		return
	}
	t.Fatal("missing one-percent-low definition")
}

func TestHostOverheadCapturePlanKeepsReadyPumpFixtureBoundary(t *testing.T) {
	plan := HostOverheadCapturePlan()
	var measurement *Measurement
	for i := range plan.Measurements {
		if plan.Measurements[i].ID == "controller-idle-readiness-counts" {
			measurement = &plan.Measurements[i]
			break
		}
	}
	if measurement == nil || measurement.Availability != "runner-fixture" || !strings.Contains(measurement.Definition, "initial=1, shutdown=1") || !strings.Contains(measurement.Reason, "not client CPU") {
		t.Fatalf("readiness measurement = %#v", measurement)
	}
	for _, workload := range plan.Cases {
		if workload.ID != "controller-idle-wakeup" {
			continue
		}
		if workload.Availability != "runner-fixture" || !contains(workload.RequiredMetric, "controller-idle-readiness-counts") || !strings.Contains(workload.Reason, "causal prepatch ticker comparison") {
			t.Fatalf("readiness workload = %#v", workload)
		}
		return
	}
	t.Fatal("missing controller-idle-wakeup workload")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
