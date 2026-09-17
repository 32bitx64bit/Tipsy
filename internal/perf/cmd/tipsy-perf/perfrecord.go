package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/perf"
)

const (
	perfDataFileName    = "perf.data"
	perfReportTimeout   = 2 * time.Minute
	perfProbeTimeout    = 5 * time.Second
	perfHelpTimeout     = 3 * time.Second
	perfPercentLimit    = 1.0
	perfCallGraph       = "fp"
	perfUserspaceEvent  = "cycles:u"
	perfParanoidBlocked = 3
	perfUnavailable     = "unavailable"
	perfStatusOK        = "ok"
	perfStatusFailed    = "failed"
)

// nativeCPUCapture is a privacy-safe userspace perf ranking. It stores
// function names and percentages only: no instruction dumps, disassembly,
// cookies, tickets, or stdout/stderr payloads.
type nativeCPUCapture struct {
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	Reason        string          `json:"reason,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	Event         string          `json:"event,omitempty"`
	CallGraph     string          `json:"call_graph,omitempty"`
	UserCallGraph bool            `json:"user_call_graph,omitempty"`
	PercentLimit  float64         `json:"percent_limit,omitempty"`
	DataPath      string          `json:"data_path,omitempty"`
	Hotspots      []nativeHotspot `json:"hotspots,omitempty"`
}

type nativeHotspot struct {
	Name    string  `json:"name"`
	Percent float64 `json:"percent"`
}

type perfRecordMode struct {
	event         string
	userCallGraph bool
}

type perfHost struct {
	lookPath          func(string) (string, error)
	paranoid          func() ([]byte, error)
	userCallGraphHelp func(string) bool
	recordProbe       func(string, perfRecordMode) error
	report            func(ctx context.Context, perfBin, dataPath string, extra []string) (string, error)
}

func defaultPerfHost() perfHost {
	return perfHost{
		lookPath: exec.LookPath,
		paranoid: func() ([]byte, error) {
			return os.ReadFile("/proc/sys/kernel/perf_event_paranoid")
		},
		userCallGraphHelp: perfRecordHelpHasUserCallGraph,
		recordProbe:       probePerfRecordMode,
		report:            runPerfReport,
	}
}

var hostPerf = defaultPerfHost()

func preferredPerfRecordModes() []perfRecordMode {
	return []perfRecordMode{
		{event: perfUserspaceEvent, userCallGraph: true},
		{event: perfUserspaceEvent, userCallGraph: false},
		{event: "", userCallGraph: true},
		{event: "", userCallGraph: false},
	}
}

func emptyNativeCPUCapture() nativeCPUCapture {
	return nativeCPUCapture{
		ID:           perf.NativeCPUProfileMeasurementID,
		CallGraph:    perfCallGraph,
		PercentLimit: perfPercentLimit,
	}
}

func probeNativeCPU(profileDir string) nativeCPUCapture {
	out := emptyNativeCPUCapture()
	out.DataPath = filepath.Join(profileDir, perfDataFileName)
	path, err := hostPerf.lookPath("perf")
	if err != nil {
		out.Status = perfUnavailable
		out.Reason = "perf is not installed on PATH"
		out.DataPath = ""
		return out
	}
	out.Tool = path
	if runtime.GOOS != "linux" {
		out.Status = perfUnavailable
		out.Reason = "userspace perf record is Linux-only"
		out.DataPath = ""
		return out
	}
	if raw, err := hostPerf.paranoid(); err == nil {
		n, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if perr == nil && n >= perfParanoidBlocked {
			out.Status = perfUnavailable
			out.Reason = fmt.Sprintf("kernel.perf_event_paranoid=%d blocks unprivileged perf_event sampling", n)
			out.DataPath = ""
			return out
		}
	}
	mode, err := hostPerf.selectMode(path)
	if err != nil {
		out.Status = perfUnavailable
		out.Reason = err.Error()
		out.DataPath = ""
		return out
	}
	out.Event = mode.event
	out.UserCallGraph = mode.userCallGraph
	out.Status = perfStatusOK
	return out
}

func (h perfHost) selectMode(perfBin string) (perfRecordMode, error) {
	helpHasUser := true
	if h.userCallGraphHelp != nil {
		helpHasUser = h.userCallGraphHelp(perfBin)
	}
	var last error
	for _, mode := range preferredPerfRecordModes() {
		if mode.userCallGraph && !helpHasUser {
			continue
		}
		if h.recordProbe == nil {
			return mode, nil
		}
		if err := h.recordProbe(perfBin, mode); err != nil {
			last = err
			continue
		}
		return mode, nil
	}
	if last == nil {
		last = errors.New("perf record probe failed")
	}
	return perfRecordMode{}, last
}

func wrapPerfRecord(perfBin, dataPath string, mode perfRecordMode, binary string, args []string) (string, []string) {
	return perfBin, perfRecordArgs(dataPath, mode, binary, args)
}

func perfRecordArgs(dataPath string, mode perfRecordMode, binary string, args []string) []string {
	out := []string{"record", "-o", dataPath}
	if mode.event != "" {
		out = append(out, "-e", mode.event)
	}
	out = append(out, "--call-graph", perfCallGraph)
	if mode.userCallGraph {
		out = append(out, "--user-call-graph")
	}
	out = append(out, "--", binary)
	return append(out, args...)
}

func perfRecordModeFromCapture(n nativeCPUCapture) perfRecordMode {
	return perfRecordMode{event: n.Event, userCallGraph: n.UserCallGraph}
}

func finalizeNativeCPU(capture nativeCPUCapture, profileDir string) nativeCPUCapture {
	if capture.Status != perfStatusOK {
		capture.Hotspots = nil
		return capture
	}
	if capture.DataPath == "" {
		capture.DataPath = filepath.Join(profileDir, perfDataFileName)
	}
	info, err := os.Stat(capture.DataPath)
	if err != nil || info.Size() == 0 {
		capture.Status = perfUnavailable
		capture.Reason = "perf.data was not written"
		capture.Hotspots = nil
		if err != nil {
			capture.DataPath = ""
		}
		return capture
	}
	text, err := collectPerfReport(capture.Tool, capture.DataPath)
	if err != nil {
		capture.Status = perfStatusFailed
		capture.Reason = err.Error()
		capture.Hotspots = nil
		return capture
	}
	spots := parsePerfReportStdio(text)
	capture.Hotspots = spots
	if len(spots) == 0 {
		capture.Reason = "no userspace symbols at or above 1 percent"
	}
	return capture
}

func collectPerfReport(perfBin, dataPath string) (string, error) {
	if perfBin == "" {
		return "", errors.New("perf report missing tool path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), perfReportTimeout)
	defer cancel()
	text, err := hostPerf.report(ctx, perfBin, dataPath, []string{"--sort", "symbol"})
	if err != nil {
		text, err = hostPerf.report(ctx, perfBin, dataPath, nil)
	}
	if err != nil {
		return "", errors.New("perf report failed")
	}
	return text, nil
}

func runPerfReport(ctx context.Context, perfBin, dataPath string, extra []string) (string, error) {
	args := []string{"report", "-i", dataPath, "--stdio", "--no-children", "-n", "--percent-limit", "1"}
	args = append(args, extra...)
	cmd := exec.CommandContext(ctx, perfBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func parsePerfReportStdio(text string) []nativeHotspot {
	var out []nativeHotspot
	for _, line := range strings.Split(text, "\n") {
		if hotspot, ok := parsePerfReportRow(line); ok {
			out = append(out, hotspot)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Percent > out[j].Percent })
	return out
}

func parsePerfReportRow(line string) (nativeHotspot, bool) {
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "#") {
		return nativeHotspot{}, false
	}
	lower := strings.ToLower(trim)
	if strings.Contains(lower, "overhead") && strings.Contains(lower, "symbol") {
		return nativeHotspot{}, false
	}
	if looksLikeDisassemblyLine(trim) {
		return nativeHotspot{}, false
	}
	if strings.Contains(trim, "[k]") || strings.Contains(lower, "kallsyms") {
		return nativeHotspot{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return nativeHotspot{}, false
	}
	pctIdx := -1
	for i, field := range fields {
		if strings.HasSuffix(field, "%") {
			pctIdx = i
			break
		}
	}
	if pctIdx < 0 || pctIdx+1 >= len(fields) {
		return nativeHotspot{}, false
	}
	pct, err := strconv.ParseFloat(strings.TrimSuffix(fields[pctIdx], "%"), 64)
	if err != nil || pct < perfPercentLimit {
		return nativeHotspot{}, false
	}
	rest := fields[pctIdx+1:]
	if len(rest) > 1 {
		if _, err := strconv.ParseInt(strings.ReplaceAll(rest[0], ",", ""), 10, 64); err == nil {
			rest = rest[1:]
		}
	}
	joined := strings.Join(rest, " ")
	name := joined
	for _, marker := range []string{"[.]", "[+]", "[u]"} {
		if i := strings.LastIndex(joined, marker); i >= 0 {
			name = strings.TrimSpace(joined[i+len(marker):])
			break
		}
	}
	clean, ok := sanitizeSymbolName(name)
	if !ok {
		return nativeHotspot{}, false
	}
	return nativeHotspot{Name: clean, Percent: pct}, true
}

func sanitizeSymbolName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"[.]", "[+]", "[u]", "[k]"} {
		name = strings.TrimSpace(strings.TrimPrefix(name, prefix))
	}
	if name == "" || name == "Symbol" {
		return "", false
	}
	lower := strings.ToLower(name)
	for _, banned := range []string{"roblosecurity", ".roblosecurity", "cookie", "ticket="} {
		if strings.Contains(lower, banned) {
			return "", false
		}
	}
	if strings.ContainsAny(name, "/\\") {
		name = filepath.Base(name)
	}
	if looksLikeDisassemblyLine(name) {
		return "", false
	}
	return name, true
}

func looksLikeDisassemblyLine(line string) bool {
	trim := strings.TrimSpace(line)
	if trim == "" {
		return false
	}
	if i := strings.IndexByte(trim, ':'); i > 0 {
		before := strings.TrimSpace(trim[:i])
		if isHexToken(before) {
			return true
		}
	}
	lower := strings.ToLower(trim)
	if strings.Contains(trim, "%") && (strings.Contains(lower, "mov ") || strings.Contains(lower, "jmp ") || strings.Contains(lower, "call ") || strings.Contains(lower, "lea ")) {
		return true
	}
	return false
}

func isHexToken(raw string) bool {
	raw = strings.TrimPrefix(strings.ToLower(raw), "0x")
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func classifyPerfFailure(err error, output string) string {
	combined := ""
	if err != nil {
		combined = err.Error()
	}
	combined += "\n" + output
	lower := strings.ToLower(combined)
	switch {
	case strings.Contains(lower, "paranoid"):
		return "perf_event_paranoid blocked the probe record"
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "operation not permitted"):
		return "perf record probe was denied permission"
	case strings.Contains(lower, "not supported") && strings.Contains(lower, "user-call-graph"):
		return "perf record does not support --user-call-graph"
	case strings.Contains(lower, "not found") && strings.Contains(lower, "perf"):
		return "perf is not installed on PATH"
	default:
		return "perf record probe failed"
	}
}

func perfRecordHelpHasUserCallGraph(perfBin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), perfHelpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, perfBin, "record", "-h")
	out, _ := cmd.CombinedOutput()
	return strings.Contains(string(out), "user-call-graph")
}

func probePerfRecordMode(perfBin string, mode perfRecordMode) error {
	dir, err := os.MkdirTemp("", "tipsy-perf-probe-")
	if err != nil {
		return errors.New("perf record probe failed")
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), perfProbeTimeout)
	defer cancel()
	args := perfRecordArgs(filepath.Join(dir, perfDataFileName), mode, "true", nil)
	cmd := exec.CommandContext(ctx, perfBin, args...)
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return errors.New(classifyPerfFailure(err, output.String()))
	}
	return nil
}
