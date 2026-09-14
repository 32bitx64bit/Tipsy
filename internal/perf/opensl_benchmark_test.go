package perf

import (
	"io"
	"log/slog"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// BenchmarkOpenSLQueueOwnership executes Audio's bounded fake-player queue
// fixture. Its reported custom metrics are C bridge storage/copy counters,
// not allocator CPU, RSS, OS wakeups, device latency, or FPS measurements.
func BenchmarkOpenSLQueueOwnership(b *testing.B) {
	silenceAudioFixtureLogs(b)
	b.ReportAllocs()
	var result android.OpenSLQueueOwnership
	for range b.N {
		var ok bool
		result, ok = android.OpenSLQueueOwnershipFixture()
		if !ok || result.Capacity != 2 || result.NodesOwned != 2 || result.NodesFree != 2 ||
			result.Queued != 0 || result.Inflight != 0 || result.Callbacks != 4 ||
			result.NodeAllocations != 2 || result.PayloadAllocations != 2 || result.PayloadBytesAllocated != 3_840 ||
			result.CopyOperations != 6 || result.CopyBytes != 11_520 || result.NodeReclamations != 6 ||
			result.PayloadReclamations != 6 || result.CapacityRejections != 1 || !result.CallerBufferCopiesPreserved {
			b.Fatalf("queue ownership fixture ok=%t result=%+v", ok, result)
		}
	}
	b.ReportMetric(float64(result.Capacity), "audio-queue-capacity/op")
	b.ReportMetric(float64(result.NodesOwned), "audio-queue-resident-nodes/op")
	b.ReportMetric(float64(result.NodesFree), "audio-queue-free-nodes/op")
	b.ReportMetric(float64(result.Queued), "audio-queue-queued/op")
	b.ReportMetric(float64(result.Inflight), "audio-queue-inflight/op")
	b.ReportMetric(float64(result.Callbacks), "audio-queue-callbacks/op")
	b.ReportMetric(float64(result.NodeAllocations), "audio-queue-node-allocations/op")
	b.ReportMetric(float64(result.PayloadAllocations), "audio-queue-payload-allocations/op")
	b.ReportMetric(float64(result.PayloadBytesAllocated), "audio-queue-payload-bytes/op")
	b.ReportMetric(float64(result.CopyOperations), "audio-queue-copy-operations/op")
	b.ReportMetric(float64(result.CopyBytes), "audio-queue-copy-bytes/op")
	b.ReportMetric(float64(result.NodeReclamations), "audio-queue-node-reclamations/op")
	b.ReportMetric(float64(result.PayloadReclamations), "audio-queue-payload-reclamations/op")
	b.ReportMetric(float64(result.CapacityRejections), "audio-queue-capacity-rejections/op")
	b.ReportMetric(1, "audio-queue-caller-copy-preserved/op")
}

// BenchmarkMutedCaptureCadence runs only Audio's fake-muted recorder fixture.
// Callback interval and callback-to-requeue data describe that fixture's host
// cadence and synchronous re-enqueue work, not microphone/device latency or
// OS wakeups; no microphone is opened or read.
func BenchmarkMutedCaptureCadence(b *testing.B) {
	silenceAudioFixtureLogs(b)
	if android.MicrophoneDisabled() {
		b.Skip("microphone kill-switch changes the fake recorder contract")
	}
	b.ReportAllocs()
	var result android.MutedCaptureCadence
	for range b.N {
		var ok bool
		result, ok = android.MutedCaptureCadenceFixture()
		if !ok || result.Callbacks != 8 || result.ScheduledIntervalNS != 10_000_000 ||
			result.CallbackIntervalP50NS < 5_000_000 || result.CallbackIntervalP99NS > 100_000_000 ||
			result.CallbackToRequeueP99NS == 0 || result.DeadlineWaits+result.MissedDeadlineClamps != 7 {
			b.Fatalf("muted cadence fixture ok=%t result=%+v", ok, result)
		}
	}
	b.ReportMetric(float64(result.Callbacks), "audio-muted-callbacks/op")
	b.ReportMetric(float64(result.CallbackIntervalP50NS), "audio-muted-interval-p50-ns/op")
	b.ReportMetric(float64(result.CallbackIntervalP95NS), "audio-muted-interval-p95-ns/op")
	b.ReportMetric(float64(result.CallbackIntervalP99NS), "audio-muted-interval-p99-ns/op")
	b.ReportMetric(float64(result.CallbackToRequeueP50NS), "audio-muted-requeue-p50-ns/op")
	b.ReportMetric(float64(result.CallbackToRequeueP95NS), "audio-muted-requeue-p95-ns/op")
	b.ReportMetric(float64(result.CallbackToRequeueP99NS), "audio-muted-requeue-p99-ns/op")
	b.ReportMetric(float64(result.ScheduledIntervalNS), "audio-muted-scheduled-interval-ns/op")
	b.ReportMetric(float64(result.DeadlineWaits), "audio-muted-deadline-waits/op")
	b.ReportMetric(float64(result.MissedDeadlineClamps), "audio-muted-missed-deadline-clamps/op")
}

// silenceAudioFixtureLogs keeps a high-iteration synthetic fixture from
// turning its intentional content-free lifecycle diagnostics into benchmark
// I/O. It is confined to this direct test binary and restores the prior logger
// before the package process exits.
func silenceAudioFixtureLogs(b *testing.B) {
	b.Helper()
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1})))
	logging.RefreshDebugEnabled()
	b.Cleanup(func() {
		slog.SetDefault(prior)
		logging.RefreshDebugEnabled()
	})
}
