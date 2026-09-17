package perf

import (
	"strings"
	"testing"
)

func TestWholeProgramCLICommandsStayBounded(t *testing.T) {
	seen := map[string]bool{}
	for _, args := range WholeProgramCLICommands() {
		if len(args) == 0 {
			t.Fatal("empty argv")
		}
		switch args[0] {
		case "version", "help", "logs", "doctor", "diagnose":
		case "config":
			if len(args) != 2 || args[1] != "path" {
				t.Fatalf("config argv must be path-only: %v", args)
			}
		default:
			t.Fatalf("command %v is not a bounded whole-program CLI argv", args)
		}
		for _, arg := range args {
			switch arg {
			case "launch", "setup", "inspect", "compare-roblox", "report", "set":
				t.Fatalf("forbidden argv %v", args)
			}
		}
		seen[args[0]] = true
	}
	if !seen["doctor"] || !seen["diagnose"] || !seen["version"] {
		t.Fatalf("missing core commands: %v", seen)
	}
}

func TestWholeProgramCapturePlanKeepsLaunchLive(t *testing.T) {
	plan := WholeProgramCapturePlan()
	if plan.Instrumented.ID != "whole-program-instrumented" || plan.Clean.ID != "whole-program-clean" {
		t.Fatalf("modes=%#v", plan)
	}
	got := map[string]WorkloadCase{}
	for _, workload := range plan.Workloads {
		got[workload.ID] = workload
	}
	cli := got["whole-program-cli-surface"]
	if cli.Availability != "runner" || len(cli.RequiredMetric) == 0 {
		t.Fatalf("cli workload=%#v", cli)
	}
	launch := got["whole-program-client-launch"]
	if launch.Availability != "live-workload-required" || launch.OwnerHandoff == "" {
		t.Fatalf("launch workload=%#v", launch)
	}
	if !containsString(launch.RequiredMetric, NativeCPUProfileMeasurementID) {
		t.Fatalf("launch missing native CPU metric: %#v", launch.RequiredMetric)
	}
	if !strings.Contains(launch.OwnerHandoff, "-program-perf") {
		t.Fatalf("launch handoff must name -program-perf: %s", launch.OwnerHandoff)
	}
	if !strings.Contains(cli.Reason, "linked tipsy binary") {
		t.Fatalf("cli surface must name the linked binary: %s", cli.Reason)
	}
	if len(plan.Measurements) != 1 || plan.Measurements[0].ID != NativeCPUProfileMeasurementID {
		t.Fatalf("measurements=%#v", plan.Measurements)
	}
	if plan.Measurements[0].Availability != "live-workload-required" || plan.Measurements[0].Unit != "percent of sampled userspace CPU by symbol" {
		t.Fatalf("native CPU measurement=%#v", plan.Measurements[0])
	}
	if !containsString(plan.Instrumented.Prohibited, "comparing a perf-record arm to a perf-off arm as an optimization result") || !containsString(plan.Instrumented.Prohibited, "claiming displayed FPS or 1% lows from perf record or pprof samples") {
		t.Fatalf("instrumented prohibited=%#v", plan.Instrumented.Prohibited)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
