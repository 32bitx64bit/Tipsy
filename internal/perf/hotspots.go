package perf

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Hotspot is one function in a pprof -top listing. Flat/Cumulative use Unit
// ("nanoseconds" for CPU, "bytes" for heap/alloc). Percents are 0–100.
type Hotspot struct {
	Name              string  `json:"name"`
	Flat              float64 `json:"flat"`
	FlatPercent       float64 `json:"flat_percent"`
	Cumulative        float64 `json:"cumulative"`
	CumulativePercent float64 `json:"cumulative_percent"`
	Unit              string  `json:"unit"`
}

// ProfileSummary is the ranked whole-process view of one pprof artifact.
type ProfileSummary struct {
	Kind     string    `json:"kind"`
	Path     string    `json:"path"`
	Type     string    `json:"type,omitempty"`
	Duration string    `json:"duration,omitempty"`
	Total    float64   `json:"total,omitempty"`
	Unit     string    `json:"unit,omitempty"`
	Hotspots []Hotspot `json:"hotspots,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// SummarizeProfile ranks functions in a Go pprof file with `go tool pprof -top`.
// The listing is function names only; it is not a gameplay or FPS result.
func SummarizeProfile(path string, limit int) ProfileSummary {
	summary := ProfileSummary{Path: path, Kind: profileKindFromPath(path)}
	if strings.TrimSpace(path) == "" {
		summary.Error = "profile path is empty"
		return summary
	}
	if limit < 1 {
		limit = 40
	}
	cmd := exec.Command("go", "tool", "pprof", "-top", fmt.Sprintf("-nodecount=%d", limit), path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		summary.Error = strings.TrimSpace(err.Error() + "\n" + stderr.String())
		return summary
	}
	parsed, err := ParsePprofTop(stdout.String())
	if err != nil {
		summary.Error = err.Error()
		return summary
	}
	parsed.Kind = summary.Kind
	parsed.Path = path
	return parsed
}

// ParsePprofTop converts `go tool pprof -top` text into ranked hotspots.
func ParsePprofTop(text string) (ProfileSummary, error) {
	var summary ProfileSummary
	lines := strings.Split(text, "\n")
	headerDone := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Type:") {
			summary.Type = strings.TrimSpace(strings.TrimPrefix(trim, "Type:"))
			summary.Unit = unitForPprofType(summary.Type)
			continue
		}
		if strings.HasPrefix(trim, "Duration:") {
			summary.Duration = strings.TrimSpace(strings.TrimPrefix(trim, "Duration:"))
			if total, ok := parseTotalSamples(summary.Duration); ok {
				summary.Total = total
			}
			continue
		}
		if strings.Contains(line, "flat%") && strings.Contains(line, "cum%") {
			headerDone = true
			continue
		}
		if !headerDone || trim == "" || strings.HasPrefix(trim, "Dropped ") || strings.HasPrefix(trim, "Showing nodes") {
			continue
		}
		spot, ok := parsePprofTopRow(line, summary.Unit)
		if !ok {
			continue
		}
		summary.Hotspots = append(summary.Hotspots, spot)
	}
	if summary.Unit == "" {
		summary.Unit = "nanoseconds"
	}
	for i := range summary.Hotspots {
		if summary.Hotspots[i].Unit == "" {
			summary.Hotspots[i].Unit = summary.Unit
		}
	}
	if len(summary.Hotspots) == 0 {
		return summary, fmt.Errorf("pprof top listing contained no function rows")
	}
	return summary, nil
}

func profileKindFromPath(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "cpu"):
		return "cpu"
	case strings.Contains(base, "alloc"):
		return "alloc"
	case strings.Contains(base, "heap"):
		return "heap"
	case strings.Contains(base, "mutex"):
		return "mutex"
	case strings.Contains(base, "block"):
		return "block"
	case strings.Contains(base, "goroutine"):
		return "goroutine"
	case strings.Contains(base, "threadcreate"):
		return "threadcreate"
	default:
		return "profile"
	}
}

func unitForPprofType(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.Contains(lower, "alloc") || strings.Contains(lower, "heap") || strings.Contains(lower, "inuse_space") || strings.Contains(lower, "alloc_space"):
		return "bytes"
	case strings.Contains(lower, "inuse_objects") || strings.Contains(lower, "alloc_objects") || strings.Contains(lower, "goroutine") || strings.Contains(lower, "threadcreate"):
		return "count"
	default:
		return "nanoseconds"
	}
}

func parseTotalSamples(durationLine string) (float64, bool) {
	// "200.51ms, Total samples = 180ms (89.77%)"
	_, rest, ok := strings.Cut(durationLine, "Total samples =")
	if !ok {
		return 0, false
	}
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, " "); i > 0 {
		rest = rest[:i]
	}
	value, err := parsePprofValue(rest)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parsePprofTopRow(line, unit string) (Hotspot, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return Hotspot{}, false
	}
	flat, err := parsePprofValue(fields[0])
	if err != nil {
		return Hotspot{}, false
	}
	flatPct, err := parsePercent(fields[1])
	if err != nil {
		return Hotspot{}, false
	}
	if _, err := parsePercent(fields[2]); err != nil {
		return Hotspot{}, false
	}
	cum, err := parsePprofValue(fields[3])
	if err != nil {
		return Hotspot{}, false
	}
	cumPct, err := parsePercent(fields[4])
	if err != nil {
		return Hotspot{}, false
	}
	name := strings.Join(fields[5:], " ")
	if name == "" || name == "flat" {
		return Hotspot{}, false
	}
	return Hotspot{
		Name:              name,
		Flat:              flat,
		FlatPercent:       flatPct,
		Cumulative:        cum,
		CumulativePercent: cumPct,
		Unit:              unit,
	}, true
}

func parsePercent(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, "%")
	if strings.HasPrefix(raw, "<") {
		return 0, nil
	}
	return strconv.ParseFloat(raw, 64)
}

func parsePprofValue(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty pprof value")
	}
	if raw == "0" {
		return 0, nil
	}
	units := []struct {
		suffix string
		scale  float64
	}{
		{"ns", 1},
		{"µs", 1e3},
		{"μs", 1e3},
		{"us", 1e3},
		{"ms", 1e6},
		{"s", 1e9},
		{"GB", 1e9},
		{"MB", 1e6},
		{"kB", 1e3},
		{"KB", 1e3},
		{"B", 1},
	}
	for _, unit := range units {
		if strings.HasSuffix(raw, unit.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(raw, unit.suffix), 64)
			if err != nil {
				return 0, err
			}
			return n * unit.scale, nil
		}
	}
	return strconv.ParseFloat(raw, 64)
}
