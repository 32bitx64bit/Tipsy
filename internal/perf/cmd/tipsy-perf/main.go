// tipsy-perf standardizes reproducible Tipsy microbenchmark runs. It only
// invokes benchmark entries in internal/perf's catalog; every other subsystem
// is represented in the JSON result with its explicit availability boundary.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tipsy-linux/tipsy/internal/perf"
)

const schemaVersion = 1

type policy struct {
	Warmups          int    `json:"warmups"`
	Samples          int    `json:"samples"`
	BenchTime        string `json:"benchtime"`
	ProfileBenchTime string `json:"profile_benchtime"`
	Timeout          string `json:"timeout"`
	GOMAXPROCS       int    `json:"gomaxprocs"`
	ParallelPackages int    `json:"parallel_packages"`
	Profiles         bool   `json:"profiles"`
}

type host struct {
	GOOS        string            `json:"goos"`
	GOARCH      string            `json:"goarch"`
	GoVersion   string            `json:"go_version"`
	Kernel      string            `json:"kernel,omitempty"`
	CPUModel    string            `json:"cpu_model,omitempty"`
	Governor    string            `json:"cpu_governor,omitempty"`
	Environment map[string]string `json:"environment"`
}

type runMeta struct {
	Mode        string `json:"mode"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at,omitempty"`
	Root        string `json:"root"`
	Revision    string `json:"revision,omitempty"`
	Dirty       bool   `json:"dirty"`
}

// runnerBuild identifies the exact direct-test-binary runner that produced a
// report. It is build provenance only, not evidence that any client binary
// shares the runner's timing or debug symbols.
type runnerBuild struct {
	Executable string `json:"executable,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	GoVersion  string `json:"go_version"`
}

type metric struct {
	Name                       string                   `json:"name"`
	Iterations                 int64                    `json:"iterations"`
	NsPerOp                    *float64                 `json:"ns_per_op,omitempty"`
	NativeNsPerOp              *float64                 `json:"native_ns_per_op,omitempty"`
	BytesPerOp                 *float64                 `json:"bytes_per_op,omitempty"`
	AllocsPerOp                *float64                 `json:"allocs_per_op,omitempty"`
	ControllerIdleReadinessPer *controllerIdleReadiness `json:"controller_idle_readiness_per_op,omitempty"`
	AudioQueueOwnershipPer     []fixtureMetric          `json:"audio_queue_ownership_per_op,omitempty"`
	MutedCaptureCadencePer     []fixtureMetric          `json:"muted_capture_cadence_per_op,omitempty"`
}

// fixtureMetric is an exact fixed-value aggregate emitted by one bounded
// fixture invocation. Its value is never normalized into a process metric:
// the owning fixture defines the counter and its boundary.
type fixtureMetric struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// controllerIdleReadiness is the exact bounded ReadyPump empty-watch
// aggregate emitted by BenchmarkControllerIdleReadiness. It is deliberately
// separate from process rusage context switches and from physical input
// latency; the latter cannot be derived from an empty private directory.
type controllerIdleReadiness struct {
	InitialRescans  *float64 `json:"initial_rescans,omitempty"`
	HotplugRescans  *float64 `json:"hotplug_rescans,omitempty"`
	RecoveryRescans *float64 `json:"recovery_rescans,omitempty"`
	EvdevReady      *float64 `json:"evdev_ready,omitempty"`
	InotifyReady    *float64 `json:"inotify_ready,omitempty"`
	ShutdownWakes   *float64 `json:"shutdown_wakes,omitempty"`
	FrameHandoffs   *float64 `json:"frame_handoffs,omitempty"`
	ReadyToFrameNS  *float64 `json:"ready_to_frame_ns,omitempty"`
}

type sample struct {
	Ordinal   int           `json:"ordinal"`
	ExitOK    bool          `json:"exit_ok"`
	Metrics   []metric      `json:"metrics,omitempty"`
	Resources *processUsage `json:"resources,omitempty"`
	Output    string        `json:"output,omitempty"`
}

// processUsage is direct benchmark-test-binary rusage. It intentionally does
// not call context switches wakeups: a context switch cannot identify an
// evdev, inotify, audio, or input readiness source.
type processUsage struct {
	Scope                      string `json:"scope"`
	UserCPUNanoseconds         int64  `json:"user_cpu_nanoseconds"`
	SystemCPUNanoseconds       int64  `json:"system_cpu_nanoseconds"`
	MaxRSSBytes                int64  `json:"max_rss_bytes,omitempty"`
	VoluntaryContextSwitches   int64  `json:"voluntary_context_switches,omitempty"`
	InvoluntaryContextSwitches int64  `json:"involuntary_context_switches,omitempty"`
}

type profile struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

type result struct {
	ID       string    `json:"id"`
	Package  string    `json:"package"`
	Status   string    `json:"status"`
	Warmup   *sample   `json:"warmup,omitempty"`
	Samples  []sample  `json:"samples,omitempty"`
	Profiles []profile `json:"profiles,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type unavailable struct {
	ID      string `json:"id"`
	Package string `json:"package"`
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
}

type comparison struct {
	ID          string  `json:"id"`
	Benchmark   string  `json:"benchmark"`
	BaselineNs  float64 `json:"baseline_ns_per_op"`
	CandidateNs float64 `json:"candidate_ns_per_op"`
	ChangePct   float64 `json:"change_pct"`
}

type report struct {
	SchemaVersion       int                          `json:"schema_version"`
	Run                 runMeta                      `json:"run"`
	RunnerBuild         runnerBuild                  `json:"runner_build"`
	Host                host                         `json:"host"`
	Policy              policy                       `json:"policy"`
	CapturePlan         perf.CapturePlan             `json:"capture_plan"`
	StartupBuildCapture perf.StartupBuildCapturePlan `json:"startup_build_capture"`
	Matrix              []perf.Entry                 `json:"matrix"`
	Results             []result                     `json:"results"`
	Unavailable         []unavailable                `json:"unavailable"`
	Controls            []controlResult              `json:"controls,omitempty"`
	Comparison          []comparison                 `json:"comparison,omitempty"`
}

// controlResult retains only an allowlisted command's identity, timing,
// direct-process rusage, status, and output digest. It deliberately omits
// command output, which keeps this generic runner away from user state.
type controlResult struct {
	ID              string        `json:"id"`
	Command         string        `json:"command"`
	Scope           string        `json:"scope"`
	Environment     string        `json:"environment"`
	ExitOK          bool          `json:"exit_ok"`
	ExitCode        int           `json:"exit_code"`
	WallNanoseconds int64         `json:"wall_nanoseconds"`
	Resources       *processUsage `json:"resources,omitempty"`
	OutputSHA256    string        `json:"output_sha256,omitempty"`
	OutputBytes     int64         `json:"output_bytes,omitempty"`
	Error           string        `json:"error,omitempty"`
}

type controlSpec struct {
	ID      string
	Command string
	Args    []string
	GUI     bool
}

var controlCatalog = map[string]controlSpec{
	"cli-version": {
		ID:      "cli-version",
		Command: "tipsy version",
		Args:    []string{"version"},
	},
	"gui-help": {
		ID:      "gui-help",
		Command: "tipsy-gui --help",
		Args:    []string{"--help"},
		GUI:     true,
	},
}

type options struct {
	root             string
	out              string
	compare          string
	mode             string
	only             map[string]bool
	warmups          int
	samples          int
	benchTime        string
	profileBenchTime string
	profiles         bool
	list             bool
	resume           bool
	controls         []string
	controlsOnly     bool
	cliBin           string
	guiBin           string
	timeout          time.Duration
}

func main() {
	var only string
	var controls string
	opt := options{}
	flag.StringVar(&opt.root, "root", ".", "module root")
	flag.StringVar(&opt.out, "out", "-", "output JSON path, or - for stdout (profiles disabled)")
	flag.StringVar(&opt.compare, "compare", "", "prior schema-v1 JSON result to compare")
	flag.StringVar(&opt.mode, "mode", "baseline", "run label: baseline, candidate, diagnostic, or clean-control")
	flag.StringVar(&only, "only", "", "comma-separated matrix IDs; default is every benchmarkable entry")
	flag.IntVar(&opt.warmups, "warmups", 1, "unreported warmup runs per entry")
	flag.IntVar(&opt.samples, "samples", 5, "reported samples per entry")
	flag.StringVar(&opt.benchTime, "benchtime", "1s", "duration for each benchmark sample")
	flag.StringVar(&opt.profileBenchTime, "profile-benchtime", "3s", "duration for separate profile run")
	flag.BoolVar(&opt.profiles, "profiles", true, "collect CPU, alloc, block, and mutex profiles separately from samples")
	flag.BoolVar(&opt.list, "list", false, "write the matrix and exit without running benchmarks")
	flag.BoolVar(&opt.resume, "resume", false, "preserve completed entries in -out and rerun only failed or missing entries")
	flag.StringVar(&controls, "controls", "", "comma-separated clean-control IDs: cli-version,gui-help")
	flag.BoolVar(&opt.controlsOnly, "controls-only", false, "run only -controls, not benchmark entries")
	flag.StringVar(&opt.cliBin, "cli-bin", "", "built tipsy CLI binary used by cli-version control")
	flag.StringVar(&opt.guiBin, "gui-bin", "", "built tipsy GUI binary used by gui-help control")
	flag.DurationVar(&opt.timeout, "timeout", 15*time.Minute, "timeout per go test invocation")
	flag.Parse()
	opt.only = splitSet(only)
	opt.controls = splitList(controls)

	if err := validateOptions(&opt); err != nil {
		fatal(err)
	}
	if opt.out == "-" && opt.profiles {
		opt.profiles = false
		fmt.Fprintln(os.Stderr, "tipsy-perf: profiles disabled because -out=- has no durable profile directory")
	}
	if opt.mode == "clean-control" && opt.profiles {
		opt.profiles = false
		fmt.Fprintln(os.Stderr, "tipsy-perf: profiles disabled for clean-control mode")
	}

	root, err := filepath.Abs(opt.root)
	if err != nil {
		fatal(err)
	}
	opt.root = root
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		fatal(fmt.Errorf("module root: %w", err))
	}

	rep := report{
		SchemaVersion:       schemaVersion,
		Run:                 runMeta{Mode: opt.mode, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Root: root, Revision: git(root, "rev-parse", "HEAD"), Dirty: git(root, "status", "--porcelain") != ""},
		RunnerBuild:         captureRunnerBuild(),
		Host:                captureHost(),
		Policy:              policy{Warmups: opt.warmups, Samples: opt.samples, BenchTime: opt.benchTime, ProfileBenchTime: opt.profileBenchTime, Timeout: opt.timeout.String(), GOMAXPROCS: 1, ParallelPackages: 1, Profiles: opt.profiles},
		CapturePlan:         perf.HostOverheadCapturePlan(),
		StartupBuildCapture: perf.StartupBuildTimingPlan(),
		Matrix:              perf.Catalog(),
	}
	if opt.resume {
		if opt.out == "-" {
			fatal(errors.New("-resume requires a file output path"))
		}
		previous, err := readReport(opt.out)
		if err != nil {
			fatal(fmt.Errorf("resume report: %w", err))
		}
		for _, prior := range previous.Results {
			if benchmarkable(rep.Matrix, prior.ID) {
				rep.Results = append(rep.Results, prior)
			}
		}
		rep.Unavailable = previous.Unavailable
	}
	if opt.list {
		finish(&rep, opt.out)
		return
	}
	for _, id := range opt.controls {
		res := runControl(context.Background(), opt, id)
		rep.Controls = append(rep.Controls, res)
		if opt.out != "-" {
			writeReport(opt.out, rep)
		}
	}
	if opt.controlsOnly {
		finish(&rep, opt.out)
		return
	}

	for _, entry := range rep.Matrix {
		if len(opt.only) != 0 && !opt.only[entry.ID] {
			continue
		}
		if opt.resume && len(opt.only) == 0 && completed(rep.Results, entry.ID) {
			continue
		}
		if entry.Kind != perf.Microbenchmark {
			if !hasUnavailable(rep.Unavailable, entry.ID) {
				rep.Unavailable = append(rep.Unavailable, unavailable{ID: entry.ID, Package: entry.Package, Kind: string(entry.Kind), Reason: entry.Reason})
			}
			continue
		}
		rep.Results = withoutResult(rep.Results, entry.ID)
		res := runEntry(context.Background(), opt, entry)
		rep.Results = append(rep.Results, res)
		if opt.out != "-" {
			writeReport(opt.out, rep)
		}
	}
	if opt.compare != "" {
		prior, err := readReport(opt.compare)
		if err != nil {
			fatal(err)
		}
		rep.Comparison = compareResults(prior, rep)
	}
	finish(&rep, opt.out)
}

func validateOptions(opt *options) error {
	if opt.mode != "baseline" && opt.mode != "candidate" && opt.mode != "diagnostic" && opt.mode != "clean-control" {
		return fmt.Errorf("-mode must be baseline, candidate, diagnostic, or clean-control")
	}
	if len(opt.controls) != 0 && opt.mode != "clean-control" {
		return fmt.Errorf("-controls requires -mode=clean-control so it is not compared with diagnostic data")
	}
	if len(opt.controls) != 0 && !opt.controlsOnly {
		return errors.New("-controls requires -controls-only to keep clean controls separate from benchmark diagnostics")
	}
	if opt.controlsOnly && len(opt.controls) == 0 {
		return errors.New("-controls-only requires at least one -controls ID")
	}
	for _, id := range opt.controls {
		if _, ok := controlCatalog[id]; !ok {
			return fmt.Errorf("unknown clean control %q", id)
		}
	}
	if opt.warmups < 0 || opt.samples < 1 {
		return fmt.Errorf("-warmups must be non-negative and -samples must be positive")
	}
	if _, err := time.ParseDuration(opt.benchTime); err != nil {
		return fmt.Errorf("-benchtime: %w", err)
	}
	if _, err := time.ParseDuration(opt.profileBenchTime); err != nil {
		return fmt.Errorf("-profile-benchtime: %w", err)
	}
	return nil
}

func runEntry(ctx context.Context, opt options, entry perf.Entry) result {
	res := result{ID: entry.ID, Package: entry.Package, Status: "ok"}
	binary, cleanup, err := buildBench(ctx, opt, entry)
	if err != nil {
		res.Status, res.Error = "build-failed", err.Error()
		return res
	}
	defer cleanup()
	for i := 0; i < opt.warmups; i++ {
		s := runBench(ctx, opt, entry, binary, opt.benchTime, nil)
		res.Warmup = &s
		if !s.ExitOK {
			res.Status, res.Error = "failed", s.Output
			return res
		}
	}
	for i := 0; i < opt.samples; i++ {
		s := runBench(ctx, opt, entry, binary, opt.benchTime, nil)
		s.Ordinal = i + 1
		res.Samples = append(res.Samples, s)
		if !s.ExitOK {
			res.Status, res.Error = "failed", s.Output
			return res
		}
	}
	if opt.profiles {
		profiles, err := profilePaths(opt.out, entry.ID)
		if err != nil {
			res.Status, res.Error = "failed", err.Error()
			return res
		}
		s := runBench(ctx, opt, entry, binary, opt.profileBenchTime, profiles)
		if !s.ExitOK {
			res.Status, res.Error = "profile-failed", s.Output
			return res
		}
		for kind, path := range profiles {
			info, err := os.Stat(path)
			if err != nil {
				res.Profiles = append(res.Profiles, profile{Kind: kind, Path: path})
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				res.Profiles = append(res.Profiles, profile{Kind: kind, Path: path, Bytes: info.Size()})
				continue
			}
			digest := sha256.Sum256(data)
			res.Profiles = append(res.Profiles, profile{Kind: kind, Path: path, Bytes: info.Size(), SHA256: hex.EncodeToString(digest[:])})
		}
		sort.Slice(res.Profiles, func(i, j int) bool { return res.Profiles[i].Kind < res.Profiles[j].Kind })
	}
	return res
}

func buildBench(parent context.Context, opt options, entry perf.Entry) (string, func(), error) {
	ctx, cancel := context.WithTimeout(parent, opt.timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "tipsy-perf-"+entry.ID+"-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "benchmark.test")
	args := []string{"test", "-p=1", "-c", "-o", binary}
	if len(entry.Tags) != 0 {
		args = append(args, "-tags="+strings.Join(entry.Tags, ","))
	}
	pkg := entry.Package
	if entry.RunnerPackage != "" {
		pkg = entry.RunnerPackage
	}
	args = append(args, pkg)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = opt.root
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("build benchmark test binary: %w\n%s", err, trimOutput(output.String()))
	}
	if ctx.Err() != nil {
		cleanup()
		return "", func() {}, ctx.Err()
	}
	return binary, cleanup, nil
}

func runBench(parent context.Context, opt options, entry perf.Entry, binary, benchTime string, profiles map[string]string) sample {
	ctx, cancel := context.WithTimeout(parent, opt.timeout)
	defer cancel()
	args := []string{"-test.run=^$", "-test.bench=" + entry.Benchmark, "-test.benchmem", "-test.count=1", "-test.cpu=1", "-test.benchtime=" + benchTime}
	if profiles != nil {
		args = append(args,
			"-test.cpuprofile="+profiles["cpu"],
			"-test.memprofile="+profiles["alloc"],
			"-test.blockprofile="+profiles["block"],
			"-test.blockprofilerate=1",
			"-test.mutexprofile="+profiles["mutex"],
			"-test.mutexprofilefraction=1",
		)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = opt.root
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	s := sample{ExitOK: err == nil, Output: trimOutput(output.String()), Resources: usage(cmd.ProcessState)}
	if s.ExitOK {
		s.Metrics = parseMetrics(output.String())
		if len(s.Metrics) == 0 {
			s.ExitOK = false
			s.Output = "benchmark command completed without benchmark output\n" + s.Output
		}
	}
	if ctx.Err() != nil {
		s.ExitOK = false
		s.Output = ctx.Err().Error() + "\n" + s.Output
	}
	return s
}

// runControl executes an explicitly allowlisted binary command with a private
// HOME/XDG tree. It is intentionally a process-control measurement, rather
// than GUI visibility or client-startup measurement, and retains no output.
func runControl(parent context.Context, opt options, id string) controlResult {
	spec, ok := controlCatalog[id]
	if !ok {
		return controlResult{ID: id, Scope: "ephemeral-xdg-uninstrumented-control-not-visible-client-acceptance", Environment: "fresh-private-home-and-xdg", Error: "unknown control"}
	}
	binary := opt.cliBin
	if spec.GUI {
		binary = opt.guiBin
	}
	result := controlResult{
		ID:          spec.ID,
		Command:     spec.Command,
		Scope:       "ephemeral-xdg-uninstrumented-control-not-visible-client-acceptance",
		Environment: "fresh-private-home-and-xdg",
	}
	if binary == "" {
		result.Error = "required control binary was not supplied"
		return result
	}
	info, err := os.Stat(binary)
	if err != nil {
		result.Error = fmt.Sprintf("control binary: %v", err)
		return result
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		result.Error = "control binary is not an executable regular file"
		return result
	}
	dir, err := os.MkdirTemp("", "tipsy-perf-clean-control-")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"home", "config", "data", "cache"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			result.Error = err.Error()
			return result
		}
	}
	ctx, cancel := context.WithTimeout(parent, opt.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, spec.Args...)
	cmd.Dir = opt.root
	cmd.Env = cleanControlEnv(dir)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	started := time.Now()
	err = cmd.Run()
	result.WallNanoseconds = time.Since(started).Nanoseconds()
	result.Resources = controlUsage(cmd.ProcessState)
	result.OutputBytes = int64(output.Len())
	digest := sha256.Sum256(output.Bytes())
	result.OutputSHA256 = hex.EncodeToString(digest[:])
	result.ExitOK = err == nil && ctx.Err() == nil
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		result.Error = err.Error()
	}
	if ctx.Err() != nil {
		result.Error = ctx.Err().Error()
	}
	return result
}

// cleanControlEnv drops user-specific runtime variables while retaining only
// the normal process environment required to find and start the already-built
// control binaries. HOME and all XDG roots are private to this invocation.
func cleanControlEnv(root string) []string {
	filtered := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "HOME" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "TIPSY_") || strings.HasPrefix(key, "ROBLOX_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered,
		"GOMAXPROCS=1",
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
	)
	return filtered
}

func usage(state *os.ProcessState) *processUsage {
	if state == nil {
		return nil
	}
	ru, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return nil
	}
	return usageFromRusage(ru)
}

func controlUsage(state *os.ProcessState) *processUsage {
	u := usage(state)
	if u != nil {
		u.Scope = "direct-clean-control-process-rusage"
	}
	return u
}

func usageFromRusage(ru *syscall.Rusage) *processUsage {
	if ru == nil {
		return nil
	}
	u := &processUsage{
		Scope:                      "direct-benchmark-test-binary-rusage",
		UserCPUNanoseconds:         timevalNanoseconds(ru.Utime),
		SystemCPUNanoseconds:       timevalNanoseconds(ru.Stime),
		VoluntaryContextSwitches:   ru.Nvcsw,
		InvoluntaryContextSwitches: ru.Nivcsw,
	}
	// Linux ru_maxrss is KiB. Tipsy's baseline runner is Linux-only; keeping
	// the conversion next to the scope prevents a host RSS claim from being
	// silently copied to a platform with different rusage semantics.
	if runtime.GOOS == "linux" && ru.Maxrss > 0 {
		u.MaxRSSBytes = ru.Maxrss * 1024
	}
	return u
}

func timevalNanoseconds(tv syscall.Timeval) int64 {
	return tv.Sec*int64(time.Second) + tv.Usec*int64(time.Microsecond)
}

func profilePaths(out, id string) (map[string]string, error) {
	if out == "-" {
		return nil, errors.New("profiles require a file output path")
	}
	base := strings.TrimSuffix(filepath.Base(out), filepath.Ext(out)) + ".profiles"
	dir := filepath.Join(filepath.Dir(out), base, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return map[string]string{
		"cpu": filepath.Join(dir, "cpu.pprof"), "alloc": filepath.Join(dir, "alloc.pprof"), "block": filepath.Join(dir, "block.pprof"), "mutex": filepath.Join(dir, "mutex.pprof"),
	}, nil
}

var (
	benchmarkHead     = regexp.MustCompile(`^(Benchmark\S+)\s+(\d+)\s+`)
	nsPerOp           = regexp.MustCompile(`([0-9.]+)\s+ns/op`)
	nativeNsPerOp     = regexp.MustCompile(`([0-9.]+)\s+native-ns/op`)
	bytesPerOp        = regexp.MustCompile(`([0-9.]+)\s+B/op`)
	allocsPerOp       = regexp.MustCompile(`([0-9.]+)\s+allocs/op`)
	readinessPerOp    = regexp.MustCompile(`([0-9.]+)\s+(initial-rescans|hotplug-rescans|recovery-rescans|evdev-ready|inotify-ready|shutdown-wakes|frame-handoffs|ready-to-frame-ns)/op`)
	audioQueuePerOp   = regexp.MustCompile(`([0-9.]+)\s+(audio-queue-(?:capacity|resident-nodes|free-nodes|queued|inflight|callbacks|node-allocations|payload-allocations|payload-bytes|copy-operations|copy-bytes|node-reclamations|payload-reclamations|capacity-rejections|caller-copy-preserved))/op`)
	mutedCadencePerOp = regexp.MustCompile(`([0-9.]+)\s+(audio-muted-(?:callbacks|interval-p50-ns|interval-p95-ns|interval-p99-ns|requeue-p50-ns|requeue-p95-ns|requeue-p99-ns|scheduled-interval-ns|deadline-waits|missed-deadline-clamps))/op`)
)

func parseMetrics(out string) []metric {
	var metrics []metric
	for _, line := range strings.Split(out, "\n") {
		head := benchmarkHead.FindStringSubmatch(line)
		if head == nil {
			continue
		}
		nsMatch := nsPerOp.FindStringSubmatch(line)
		if nsMatch == nil {
			continue
		}
		iterations, _ := strconv.ParseInt(head[2], 10, 64)
		ns, _ := strconv.ParseFloat(nsMatch[1], 64)
		m := metric{Name: normalizeBenchmarkName(head[1]), Iterations: iterations, NsPerOp: &ns}
		if match := nativeNsPerOp.FindStringSubmatch(line); match != nil {
			native, _ := strconv.ParseFloat(match[1], 64)
			m.NativeNsPerOp = &native
		}
		if match := bytesPerOp.FindStringSubmatch(line); match != nil {
			bytes, _ := strconv.ParseFloat(match[1], 64)
			m.BytesPerOp = &bytes
		}
		if match := allocsPerOp.FindStringSubmatch(line); match != nil {
			allocs, _ := strconv.ParseFloat(match[1], 64)
			m.AllocsPerOp = &allocs
		}
		m.ControllerIdleReadinessPer = parseControllerIdleReadiness(line)
		m.AudioQueueOwnershipPer = parseFixtureMetrics(line, audioQueuePerOp)
		m.MutedCaptureCadencePer = parseFixtureMetrics(line, mutedCadencePerOp)
		metrics = append(metrics, m)
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
	return metrics
}

func parseFixtureMetrics(line string, pattern *regexp.Regexp) []fixtureMetric {
	matches := pattern.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return nil
	}
	metrics := make([]fixtureMetric, 0, len(matches))
	for _, match := range matches {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		metrics = append(metrics, fixtureMetric{Name: match[2], Value: value, Unit: "per-fixture-invocation"})
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Name < metrics[j].Name })
	return metrics
}

func parseControllerIdleReadiness(line string) *controllerIdleReadiness {
	var out controllerIdleReadiness
	for _, match := range readinessPerOp.FindAllStringSubmatch(line, -1) {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		switch match[2] {
		case "initial-rescans":
			out.InitialRescans = &value
		case "hotplug-rescans":
			out.HotplugRescans = &value
		case "recovery-rescans":
			out.RecoveryRescans = &value
		case "evdev-ready":
			out.EvdevReady = &value
		case "inotify-ready":
			out.InotifyReady = &value
		case "shutdown-wakes":
			out.ShutdownWakes = &value
		case "frame-handoffs":
			out.FrameHandoffs = &value
		case "ready-to-frame-ns":
			out.ReadyToFrameNS = &value
		}
	}
	if out.InitialRescans == nil && out.HotplugRescans == nil && out.RecoveryRescans == nil && out.EvdevReady == nil && out.InotifyReady == nil && out.ShutdownWakes == nil && out.FrameHandoffs == nil && out.ReadyToFrameNS == nil {
		return nil
	}
	return &out
}

var gomaxprocsSuffix = regexp.MustCompile(`-\d+$`)

func normalizeBenchmarkName(name string) string { return gomaxprocsSuffix.ReplaceAllString(name, "") }

func captureHost() host {
	env := make(map[string]string)
	for _, key := range []string{"CGO_ENABLED", "CGO_CFLAGS", "CGO_LDFLAGS", "GOAMD64", "GOARCH", "GOEXPERIMENT", "GOFLAGS", "GOOS", "GOVERSION"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	h := host{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), CPUModel: cpuModel(), Governor: cpuGovernor(), Environment: env}
	var uname syscall.Utsname
	if syscall.Uname(&uname) == nil {
		h.Kernel = chars(uname.Release[:])
	}
	return h
}

func captureRunnerBuild() runnerBuild {
	meta := runnerBuild{GoVersion: runtime.Version()}
	path, err := os.Executable()
	if err != nil {
		return meta
	}
	meta.Executable = path
	info, err := os.Stat(path)
	if err != nil {
		return meta
	}
	meta.Bytes = info.Size()
	data, err := os.ReadFile(path)
	if err != nil {
		return meta
	}
	digest := sha256.Sum256(data)
	meta.SHA256 = hex.EncodeToString(digest[:])
	return meta
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "model name") {
			_, value, _ := strings.Cut(line, ":")
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cpuGovernor() string {
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func chars(in []int8) string {
	buf := make([]byte, 0, len(in))
	for _, c := range in {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	return string(buf)
}

func git(root string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readReport(path string) (report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return report{}, err
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return report{}, err
	}
	if rep.SchemaVersion != schemaVersion {
		return report{}, fmt.Errorf("compare schema %d, need %d", rep.SchemaVersion, schemaVersion)
	}
	return rep, nil
}

func compareResults(baseline, candidate report) []comparison {
	prior := medians(baseline.Results)
	next := medians(candidate.Results)
	var compared []comparison
	for key, old := range prior {
		if now, ok := next[key]; ok && old > 0 {
			id, name, _ := strings.Cut(key, "\x00")
			compared = append(compared, comparison{ID: id, Benchmark: name, BaselineNs: old, CandidateNs: now, ChangePct: (now - old) * 100 / old})
		}
	}
	sort.Slice(compared, func(i, j int) bool {
		if compared[i].ID == compared[j].ID {
			return compared[i].Benchmark < compared[j].Benchmark
		}
		return compared[i].ID < compared[j].ID
	})
	return compared
}

func medians(results []result) map[string]float64 {
	values := make(map[string][]float64)
	for _, result := range results {
		if result.Status != "ok" {
			continue
		}
		for _, sample := range result.Samples {
			for _, m := range sample.Metrics {
				if m.NsPerOp != nil {
					values[result.ID+"\x00"+m.Name] = append(values[result.ID+"\x00"+m.Name], *m.NsPerOp)
				}
			}
		}
	}
	medians := make(map[string]float64, len(values))
	for key, samples := range values {
		sort.Float64s(samples)
		medians[key] = samples[len(samples)/2]
	}
	return medians
}

func splitSet(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	set := make(map[string]bool)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	return set
}

func splitList(raw string) []string {
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	var values []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	return values
}

func completed(results []result, id string) bool {
	for _, result := range results {
		if result.ID == id && result.Status == "ok" {
			return true
		}
	}
	return false
}

func withoutResult(results []result, id string) []result {
	filtered := results[:0]
	for _, result := range results {
		if result.ID != id {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func hasUnavailable(entries []unavailable, id string) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}

func benchmarkable(entries []perf.Entry, id string) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return entry.Kind == perf.Microbenchmark
		}
	}
	return false
}

func trimOutput(out string) string {
	const max = 16 << 10
	if len(out) <= max {
		return out
	}
	return out[:max] + "\n[truncated]\n"
}

func writeReport(path string, rep report) {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fatal(err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fatal(err)
	}
	temp, err := os.CreateTemp(dir, ".tipsy-perf-*.tmp")
	if err != nil {
		fatal(err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		fatal(err)
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		fatal(err)
	}
	if err := temp.Close(); err != nil {
		fatal(err)
	}
	if err := os.Rename(tempName, path); err != nil {
		fatal(err)
	}
}

func finish(rep *report, out string) {
	rep.Run.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if out == "-" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(data))
		return
	}
	writeReport(out, *rep)
	fmt.Printf("tipsy-perf: wrote %s\n", out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "tipsy-perf:", err)
	os.Exit(2)
}
