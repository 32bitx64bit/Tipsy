// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/tipsy-linux/tipsy/internal/app"
	"github.com/tipsy-linux/tipsy/internal/graphics"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/perf"
)

type contextNotifier func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
type appRunner func(context.Context, []string, io.Writer, io.Writer) int

func main() {
	os.Exit(tipsyMain(os.Args[1:]))
}

func tipsyMain(args []string) int {
	// Isolated anti-lag driver probe. It runs before logging or profiling setup
	// so the helper sees the user's environment verbatim and writes only its
	// key=value report.
	if len(args) > 0 && args[0] == graphics.AntiLagProbeCommand {
		return graphics.RunAntiLagProbeMain()
	}
	logging.Init()
	stop, err := perf.StartFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tipsy:", err)
		return 2
	}
	defer stop()

	runner := app.Run
	if perf.CLISurfaceRequested() {
		runner = runCLISurface
		args = nil
	}
	return run(signalChannelNotifier(os.Exit), runner, args, os.Stdout, os.Stderr)
}

// signalExitCode is the conventional shell status for a signal-terminated
// process: 130 for SIGINT, 143 for SIGTERM.
func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

// awaitSignalEscalation cancels the app on the first signal and force-exits on
// the next, so a second Ctrl+C terminates a client whose first shutdown is
// wedged. done retires the listener once run returns.
func awaitSignalEscalation(signals <-chan os.Signal, cancel context.CancelFunc, forceExit func(int), done <-chan struct{}) {
	select {
	case <-done:
		return
	case _, ok := <-signals:
		if !ok {
			return
		}
		cancel()
	}
	select {
	case <-done:
	case sig, ok := <-signals:
		if ok {
			forceExit(signalExitCode(sig))
		}
	}
}

// signalChannelNotifier is the production notifier: the first SIGINT/SIGTERM
// cancels the app context cleanly, and a second one exits immediately instead
// of re-entering the same shutdown path. forceExit is injectable for tests.
func signalChannelNotifier(forceExit func(int)) contextNotifier {
	return func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, signals...)
		done := make(chan struct{})
		go awaitSignalEscalation(ch, cancel, forceExit, done)
		var once sync.Once
		stop := func() {
			once.Do(func() {
				signal.Stop(ch)
				close(done)
				cancel()
			})
		}
		return ctx, stop
	}
}

func run(notify contextNotifier, runApp appRunner, args []string, stdout, stderr io.Writer) int {
	ctx, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runApp(ctx, args, stdout, stderr)
}

// runCLISurface repeatedly dispatches the checkout-safe command table in this
// process so one CPU/heap profile ranks real CLI and host-probe cost. Output is
// discarded; the report keeps only pprof artifacts and a digest of this process.
func runCLISurface(ctx context.Context, _ []string, _, stderr io.Writer) int {
	commands := perf.WholeProgramCLICommands()
	for {
		if ctx.Err() != nil {
			return 0
		}
		for _, args := range commands {
			if ctx.Err() != nil {
				return 0
			}
			code := app.Run(ctx, args, io.Discard, io.Discard)
			if code > 2 {
				fmt.Fprintf(stderr, "tipsy: cli-surface %v exit %d\n", args, code)
				return code
			}
		}
	}
}
