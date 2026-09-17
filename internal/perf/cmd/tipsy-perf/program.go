package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tipsy-linux/tipsy/internal/perf"
)

const defaultCLISurfaceDuration = 5 * time.Second

type programResult struct {
	ID              string                `json:"id"`
	Status          string                `json:"status"`
	Command         []string              `json:"command"`
	Workload        string                `json:"workload,omitempty"`
	Scope           string                `json:"scope"`
	Environment     string                `json:"environment"`
	ExitOK          bool                  `json:"exit_ok"`
	ExitCode        int                   `json:"exit_code"`
	Signaled        bool                  `json:"signaled,omitempty"`
	WallNanoseconds int64                 `json:"wall_nanoseconds"`
	Resources       *processUsage         `json:"resources,omitempty"`
	OutputSHA256    string                `json:"output_sha256,omitempty"`
	OutputBytes     int64                 `json:"output_bytes,omitempty"`
	Profiles        []profile             `json:"profiles,omitempty"`
	Hotspots        []perf.ProfileSummary `json:"hotspots,omitempty"`
	ProfileFlush    string                `json:"profile_flush,omitempty"`
	NativeCPU       *nativeCPUCapture     `json:"native_cpu,omitempty"`
	Error           string                `json:"error,omitempty"`
}

const (
	defaultCLISurfaceFlush = 10 * time.Second
	defaultProgramFlush    = 30 * time.Second
	programBackstopSlack   = 15 * time.Second
)

func runProgramCapture(parent context.Context, opt options) programResult {
	args := opt.programArgs
	cliSurface := len(args) == 0
	duration := opt.programDuration
	if cliSurface && duration == 0 {
		duration = defaultCLISurfaceDuration
	}
	result := programResult{
		ID:          "whole-program",
		Status:      "ok",
		Command:     programCommand(args, cliSurface),
		Workload:    programWorkload(args, cliSurface),
		Scope:       programScope(args, cliSurface),
		Environment: programEnvironmentLabel(args, cliSurface),
	}
	if err := validateProgramArgs(args, duration); err != nil {
		result.Status, result.Error = "failed", err.Error()
		return result
	}
	if opt.out == "-" {
		result.Status, result.Error = "failed", "whole-program profiles require a file -out path"
		return result
	}

	binary, cleanup, err := resolveProgramBinary(parent, opt)
	if err != nil {
		result.Status, result.Error = "build-failed", err.Error()
		return result
	}
	defer cleanup()

	profileDir, err := programProfileDir(opt.out)
	if err != nil {
		result.Status, result.Error = "failed", err.Error()
		return result
	}
	result.Profiles = []profile{{Kind: "dir", Path: profileDir}}

	flush := programFlushGrace(opt, cliSurface)
	result.ProfileFlush = flush.String()
	var native nativeCPUCapture
	wrapped := false
	runBin := binary
	runArgs := append([]string{}, args...)
	if opt.programPerf {
		native = probeNativeCPU(profileDir)
		if native.Status == perfStatusOK {
			runBin, runArgs = wrapPerfRecord(native.Tool, native.DataPath, perfRecordModeFromCapture(native), binary, args)
			wrapped = true
		}
	}

	backstop := programBackstop(opt.timeout, duration, flush)
	ctx, cancel := context.WithTimeout(parent, backstop)
	defer cancel()

	cmd := exec.CommandContext(ctx, runBin, runArgs...)
	cmd.Dir = opt.root
	cmd.Env = programEnv(opt, args, profileDir, cliSurface)
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	started := time.Now()
	terminated, err := runBounded(cmd, duration, flush)
	if wrapped && startFailed(err) {
		native.Status = perfUnavailable
		native.Reason = classifyPerfFailure(err, "")
		native.Hotspots = nil
		native.DataPath = ""
		wrapped = false
		cmd = exec.CommandContext(ctx, binary, args...)
		cmd.Dir = opt.root
		cmd.Env = programEnv(opt, args, profileDir, cliSurface)
		output.Reset()
		cmd.Stdout = &output
		cmd.Stderr = &output
		started = time.Now()
		terminated, err = runBounded(cmd, duration, flush)
	}
	result.WallNanoseconds = time.Since(started).Nanoseconds()
	result.Resources = programUsage(cmd.ProcessState)
	if wrapped && result.Resources != nil {
		result.Resources.Scope = "perf-record-wrapper-rusage-not-direct-tipsy-child"
	}
	result.OutputBytes = int64(output.Len())
	digest := sha256.Sum256([]byte(output.String()))
	result.OutputSHA256 = hex.EncodeToString(digest[:])
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		result.Signaled = terminated || !cmd.ProcessState.Exited()
	}
	if terminated {
		result.ExitOK = true
		result.Signaled = true
	} else {
		result.ExitOK = err == nil && ctx.Err() == nil && result.ExitCode == 0
		if err != nil {
			result.Error = err.Error()
		}
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
		}
	}
	if result.Error != "" && result.Status == "ok" {
		result.Status = "failed"
	}

	result.Profiles = append(result.Profiles, listProgramProfiles(profileDir)...)
	if opt.programPerf {
		native = finalizeNativeCPU(native, profileDir)
		result.NativeCPU = &native
		if native.DataPath != "" {
			if info, statErr := os.Stat(native.DataPath); statErr == nil {
				result.Profiles = append(result.Profiles, profile{Kind: "perf-data", Path: native.DataPath, Bytes: info.Size()})
			}
		}
	}
	for _, kind := range []string{"cpu", "alloc", "heap", "goroutine"} {
		path := filepath.Join(profileDir, kind+".pprof")
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Size() == 0 {
			result.Hotspots = append(result.Hotspots, perf.ProfileSummary{Kind: kind, Path: path, Error: "profile file is empty"})
			continue
		}
		result.Hotspots = append(result.Hotspots, perf.SummarizeProfile(path, opt.programHotspots))
	}
	return result
}

func validateProgramArgs(args []string, duration time.Duration) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "setup", "repair":
		return fmt.Errorf("%s mutates install state and is not a whole-program capture argv", args[0])
	case "launch":
		if duration <= 0 {
			return fmt.Errorf("launch requires -program-duration so the capture is bounded")
		}
		if strings.TrimSpace(os.Getenv("DISPLAY")) == "" {
			return fmt.Errorf("launch requires DISPLAY for an X11 client capture")
		}
	}
	return nil
}

func programCommand(args []string, cliSurface bool) []string {
	if cliSurface {
		return []string{"tipsy"}
	}
	return append([]string{"tipsy"}, args...)
}

func programWorkload(args []string, cliSurface bool) string {
	if cliSurface {
		return "cli-surface"
	}
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func programScope(args []string, cliSurface bool) string {
	if len(args) > 0 && args[0] == "launch" {
		return "instrumented-linked-binary-live-client-not-fps"
	}
	if cliSurface {
		return "instrumented-linked-binary-cli-surface-not-client-hotspots"
	}
	return "instrumented-linked-binary-cli-not-client-hotspots"
}

func programEnvironmentLabel(args []string, cliSurface bool) string {
	if len(args) > 0 && args[0] == "launch" {
		return "user-runtime-xdg-plus-tipsy-pprof-dir"
	}
	if cliSurface {
		return "fresh-private-home-and-xdg-plus-tipsy-pprof-cli-surface"
	}
	return "fresh-private-home-and-xdg-plus-tipsy-pprof-dir"
}

func resolveProgramBinary(parent context.Context, opt options) (string, func(), error) {
	if opt.cliBin != "" {
		info, err := os.Stat(opt.cliBin)
		if err != nil {
			return "", func() {}, fmt.Errorf("program binary: %w", err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return "", func() {}, fmt.Errorf("program binary is not an executable regular file")
		}
		return opt.cliBin, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "tipsy-perf-program-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "tipsy")
	ctx, cancel := context.WithTimeout(parent, opt.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/tipsy")
	cmd.Dir = opt.root
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("build ./cmd/tipsy: %w\n%s", err, trimOutput(string(out)))
	}
	return binary, cleanup, nil
}

func programProfileDir(out string) (string, error) {
	base := strings.TrimSuffix(filepath.Base(out), filepath.Ext(out)) + ".profiles"
	dir := filepath.Join(filepath.Dir(out), base, "whole-program")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func programEnv(opt options, args []string, profileDir string, cliSurface bool) []string {
	launch := len(args) > 0 && args[0] == "launch"
	var env []string
	if launch {
		env = append([]string{}, os.Environ()...)
	} else {
		xdg := filepath.Join(profileDir, "xdg")
		env = programPrivateEnv(xdg)
	}
	filtered := env[:0]
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == perf.EnvProfileDir || key == perf.EnvProfileContention || key == perf.EnvCLISurface {
			continue
		}
		if !launch && key == "GOMAXPROCS" {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, perf.EnvProfileDir+"="+profileDir)
	if opt.programContention {
		filtered = append(filtered, perf.EnvProfileContention+"=1")
	}
	if cliSurface {
		filtered = append(filtered, perf.EnvCLISurface+"=1")
	}
	return filtered
}

func programPrivateEnv(xdg string) []string {
	for _, name := range []string{"home", "config", "data", "cache"} {
		_ = os.MkdirAll(filepath.Join(xdg, name), 0o700)
	}
	filtered := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "HOME" || key == "GOMAXPROCS" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "TIPSY_") || strings.HasPrefix(key, "ROBLOX_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"HOME="+filepath.Join(xdg, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(xdg, "config"),
		"XDG_DATA_HOME="+filepath.Join(xdg, "data"),
		"XDG_CACHE_HOME="+filepath.Join(xdg, "cache"),
	)
}

func programFlushGrace(opt options, cliSurface bool) time.Duration {
	if opt.programFlush > 0 {
		return opt.programFlush
	}
	if cliSurface {
		return defaultCLISurfaceFlush
	}
	return defaultProgramFlush
}

func programBackstop(timeout, duration, flush time.Duration) time.Duration {
	if duration <= 0 {
		return timeout
	}
	if flush <= 0 {
		flush = defaultProgramFlush
	}
	needed := duration + flush + programBackstopSlack
	if needed > timeout {
		return needed
	}
	return timeout
}

func startFailed(err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}

func runBounded(cmd *exec.Cmd, duration, flush time.Duration) (bool, error) {
	if duration <= 0 {
		return false, cmd.Run()
	}
	if flush <= 0 {
		flush = defaultProgramFlush
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case err := <-done:
		return false, err
	case <-timer.C:
		signalProcessGroup(cmd.Process, syscall.SIGTERM)
		select {
		case <-done:
			return true, nil
		case <-time.After(flush):
			signalProcessGroup(cmd.Process, syscall.SIGKILL)
			<-done
			return true, nil
		}
	}
}

func signalProcessGroup(p *os.Process, sig syscall.Signal) {
	if p == nil || p.Pid <= 1 {
		return
	}
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil || pgid <= 1 || pgid != p.Pid {
		_ = p.Signal(sig)
		return
	}
	_ = syscall.Kill(-pgid, sig)
}

func listProgramProfiles(dir string) []profile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []profile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pprof") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		item := profile{Kind: strings.TrimSuffix(entry.Name(), ".pprof"), Path: path}
		if err == nil {
			item.Bytes = info.Size()
			if data, err := os.ReadFile(path); err == nil {
				digest := sha256.Sum256(data)
				item.SHA256 = hex.EncodeToString(digest[:])
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func programUsage(state *os.ProcessState) *processUsage {
	u := usage(state)
	if u != nil {
		u.Scope = "direct-whole-program-process-rusage"
	}
	return u
}
