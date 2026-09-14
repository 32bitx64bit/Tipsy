package perf

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

func BenchmarkRbxURIParse(b *testing.B) {
	const raw = "https://www.roblox.com/games/12345/example?gameInstanceId=SYNTHETIC-JOB-ID"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		request, err := rbxuri.Parse(raw)
		if err != nil || request.PlaceID != 12345 {
			b.Fatalf("parse: request=%+v err=%v", request, err)
		}
	}
}
