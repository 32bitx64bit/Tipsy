package perf

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"
)

// processProfileSnapshotInterval writes heap/alloc/goroutine dumps while the
// process is still running. Final SIGTERM often lets in-process native code
// _exit during StopCPUProfile/GC, which previously left empty heap files.
const processProfileSnapshotInterval = 15 * time.Second

const (
	// EnvProfileDir enables process-wide profiles on the real tipsy binary.
	// The directory is created with mode 0700. Profiles contain function
	// names and addresses only; they must not be treated as a log of user
	// input, cookies, or account state.
	EnvProfileDir = "TIPSY_PPROF_DIR"
	// EnvProfileContention enables mutex and block profiles. Those rates
	// add observer work and belong only on an instrumented capture arm.
	EnvProfileContention = "TIPSY_PPROF_CONTENTION"
	// EnvCLISurface makes the linked tipsy binary loop the checkout-safe
	// CLI command table until SIGTERM or context cancel. tipsy-perf sets
	// this when -program is given with no extra argv.
	EnvCLISurface = "TIPSY_PPROF_CLI_SURFACE"
)

// ProfileOptions selects which runtime/pprof artifacts a process capture writes.
type ProfileOptions struct {
	CPU          bool
	Heap         bool
	Allocs       bool
	Goroutine    bool
	ThreadCreate bool
	Contention   bool
}

// DefaultProcessProfileOptions is the instrumented whole-program set: CPU,
// heap, allocs, and goroutine dumps. Mutex/block profiles stay opt-in because
// they change scheduling cost.
func DefaultProcessProfileOptions() ProfileOptions {
	return ProfileOptions{CPU: true, Heap: true, Allocs: true, Goroutine: true, ThreadCreate: true}
}

// ProfileOptionsFromEnv reads TIPSY_PPROF_CONTENTION. CPU/heap dumps are
// always on when a profile directory is configured.
func ProfileOptionsFromEnv() ProfileOptions {
	opt := DefaultProcessProfileOptions()
	opt.Contention = strings.TrimSpace(os.Getenv(EnvProfileContention)) == "1"
	return opt
}

// StartFromEnv starts process-wide profiling when TIPSY_PPROF_DIR is set.
// A missing directory variable is a no-op so production runs stay uninstrumented.
func StartFromEnv() (func(), error) {
	dir := strings.TrimSpace(os.Getenv(EnvProfileDir))
	if dir == "" {
		return func() {}, nil
	}
	return StartProcessProfile(dir, ProfileOptionsFromEnv())
}

// CLISurfaceRequested is true when the linked binary should loop the
// checkout-safe CLI command table instead of dispatching os.Args.
func CLISurfaceRequested() bool {
	return strings.TrimSpace(os.Getenv(EnvCLISurface)) == "1"
}

// StartProcessProfile writes pprof artifacts into dir. Stop must be called
// exactly once; it is safe to defer. It restores mutex/block rates even if a
// later write fails.
func StartProcessProfile(dir string, opt ProfileOptions) (func(), error) {
	if strings.TrimSpace(dir) == "" {
		return func() {}, fmt.Errorf("performance profile directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return func() {}, fmt.Errorf("performance profile directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return func() {}, fmt.Errorf("performance profile directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return func() {}, err
	}
	if !info.IsDir() {
		return func() {}, fmt.Errorf("performance profile path is not a directory")
	}

	state := &processProfile{dir: abs, opt: opt}
	if opt.Contention {
		state.prevMutex = runtime.SetMutexProfileFraction(1)
		runtime.SetBlockProfileRate(1)
		state.contentionArmed = true
	}
	if opt.CPU {
		cpu, err := os.OpenFile(filepath.Join(abs, "cpu.pprof"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			state.restoreRates()
			return func() {}, err
		}
		if err := pprof.StartCPUProfile(cpu); err != nil {
			cpu.Close()
			state.restoreRates()
			return func() {}, err
		}
		state.cpu = cpu
	}

	var once sync.Once
	done := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	stop := func() {
		once.Do(func() {
			signal.Stop(sigs)
			close(done)
			state.stop()
		})
	}
	go func() {
		select {
		case <-sigs:
			stop()
		case <-done:
		}
	}()
	go func() {
		ticker := time.NewTicker(processProfileSnapshotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				state.snapshot()
			case <-done:
				return
			}
		}
	}()
	return stop, nil
}

type processProfile struct {
	mu              sync.Mutex
	dir             string
	opt             ProfileOptions
	cpu             *os.File
	prevMutex       int
	contentionArmed bool
}

func (p *processProfile) restoreRates() {
	if !p.contentionArmed {
		return
	}
	runtime.SetMutexProfileFraction(p.prevMutex)
	runtime.SetBlockProfileRate(0)
	p.contentionArmed = false
}

func (p *processProfile) snapshot() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeProfilesLocked()
}

func (p *processProfile) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cpu != nil {
		pprof.StopCPUProfile()
		_ = p.cpu.Close()
		p.cpu = nil
	}
	// Dump before runtime.GC(). A process-group SIGTERM can make in-process
	// native code _exit during GC on a multi-gigabyte RSS, which used to
	// leave heap.pprof truncated to 0 bytes and skip alloc/goroutine dumps.
	p.writeProfilesLocked()
	runtime.GC()
	if p.opt.Heap {
		if err := writeProfileFile(filepath.Join(p.dir, "heap.pprof"), "", pprof.WriteHeapProfile); err != nil {
			fmt.Fprintf(os.Stderr, "tipsy: write heap.pprof: %v\n", err)
		}
	}
	p.restoreRates()
}

func (p *processProfile) writeProfilesLocked() {
	writes := []struct {
		enabled bool
		name    string
		lookup  string
		direct  func(io.Writer) error
	}{
		{p.opt.Heap, "heap.pprof", "", pprof.WriteHeapProfile},
		{p.opt.Allocs, "alloc.pprof", "allocs", nil},
		{p.opt.Goroutine, "goroutine.pprof", "goroutine", nil},
		{p.opt.ThreadCreate, "threadcreate.pprof", "threadcreate", nil},
		{p.opt.Contention, "mutex.pprof", "mutex", nil},
		{p.opt.Contention, "block.pprof", "block", nil},
	}
	for _, write := range writes {
		if !write.enabled {
			continue
		}
		if err := writeProfileFile(filepath.Join(p.dir, write.name), write.lookup, write.direct); err != nil {
			fmt.Fprintf(os.Stderr, "tipsy: write %s: %v\n", write.name, err)
		}
	}
}

func writeProfileFile(path, lookup string, direct func(io.Writer) error) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var writeErr error
	if direct != nil {
		writeErr = direct(f)
	} else {
		prof := pprof.Lookup(lookup)
		if prof == nil {
			writeErr = fmt.Errorf("unknown pprof profile %q", lookup)
		} else {
			writeErr = prof.WriteTo(f, 0)
		}
	}
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, path)
}
