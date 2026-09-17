package perf

import (
	"strings"
	"testing"
)

const sampleCPUTop = `File: tipsy
Type: cpu
Time: Sep 16, 2026 at 9:00pm (CDT)
Duration: 200.51ms, Total samples = 180ms (89.77%)
Showing nodes accounting for 180ms, 100% of 180ms total
      flat  flat%   sum%        cum   cum%
     120ms 66.67% 66.67%      180ms   100%  github.com/tipsy-linux/tipsy/internal/perf.burn
      60ms 33.33%   100%       60ms 33.33%  runtime.mallocgc
Dropped 3 nodes (cum <= 0.90ms)
`

func TestParsePprofTopRanksCPUFunctions(t *testing.T) {
	summary, err := ParsePprofTop(sampleCPUTop)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Type != "cpu" || summary.Unit != "nanoseconds" || summary.Total != 180e6 {
		t.Fatalf("summary=%#v", summary)
	}
	if len(summary.Hotspots) != 2 {
		t.Fatalf("hotspots=%#v", summary.Hotspots)
	}
	if summary.Hotspots[0].Name != "github.com/tipsy-linux/tipsy/internal/perf.burn" || summary.Hotspots[0].Flat != 120e6 || summary.Hotspots[0].FlatPercent != 66.67 {
		t.Fatalf("first hotspot=%#v", summary.Hotspots[0])
	}
	if summary.Hotspots[1].Name != "runtime.mallocgc" || summary.Hotspots[1].CumulativePercent != 33.33 {
		t.Fatalf("second hotspot=%#v", summary.Hotspots[1])
	}
}

func TestParsePprofTopAllocBytes(t *testing.T) {
	text := strings.Join([]string{
		"Type: alloc_space",
		"      flat  flat%   sum%        cum   cum%",
		"      2.50MB 62.50% 62.50%      4.00MB 100%  github.com/tipsy-linux/tipsy/internal/jni.(*VM).internString",
		"      1.50MB 37.50%   100%      1.50MB 37.50%  runtime.mallocgc",
	}, "\n")
	summary, err := ParsePprofTop(text)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Unit != "bytes" || summary.Hotspots[0].Flat != 2.5e6 {
		t.Fatalf("summary=%#v", summary)
	}
}

func TestParsePprofTopRejectsEmptyListing(t *testing.T) {
	if _, err := ParsePprofTop("Type: cpu\n"); err == nil {
		t.Fatal("expected empty listing error")
	}
}

func TestParsePercentLessThan(t *testing.T) {
	got, err := parsePercent("<0.01%")
	if err != nil || got != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}
