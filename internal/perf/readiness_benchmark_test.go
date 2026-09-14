package perf

import (
	"runtime"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/gamepad"
)

const controllerIdleReadinessDuration = 75 * time.Millisecond

// BenchmarkControllerIdleReadiness runs the bounded, content-free ReadyPump
// empty-watch fixture. The aggregate proves only the fixture's owner-side
// startup/rescan/readiness/shutdown counts; it is not physical input latency,
// client-process CPU, a legacy-ticker comparison, or gameplay evidence.
func BenchmarkControllerIdleReadiness(b *testing.B) {
	if runtime.GOOS != "linux" {
		b.Skip("ReadyPump empty-watch fixture is Linux-only")
	}
	b.ReportAllocs()
	for range b.N {
		snapshot, err := gamepad.ControllerIdleReadinessFixture(controllerIdleReadinessDuration)
		if err != nil {
			b.Fatal(err)
		}
		if snapshot.InitialRescans != 1 || snapshot.HotplugRescans != 0 || snapshot.RecoveryRescans != 0 ||
			snapshot.EvdevReady != 0 || snapshot.InotifyReady != 0 || snapshot.ShutdownWake != 1 ||
			snapshot.FrameHandoffs != 0 || snapshot.ReadyToFrameMinNS != 0 || snapshot.ReadyToFrameMaxNS != 0 || snapshot.ReadyToFrameSumNS != 0 {
			b.Fatalf("unexpected empty-watch readiness aggregate: %+v", snapshot)
		}
	}
	// These are per-fixture-invocation counts, emitted as benchmark metrics so
	// the runner records all zero and non-zero fields rather than inferring a
	// readiness wake from process scheduler context switches.
	b.ReportMetric(1, "initial-rescans/op")
	b.ReportMetric(0, "hotplug-rescans/op")
	b.ReportMetric(0, "recovery-rescans/op")
	b.ReportMetric(0, "evdev-ready/op")
	b.ReportMetric(0, "inotify-ready/op")
	b.ReportMetric(1, "shutdown-wakes/op")
	b.ReportMetric(0, "frame-handoffs/op")
	b.ReportMetric(0, "ready-to-frame-ns/op")
}
