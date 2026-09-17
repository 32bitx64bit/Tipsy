// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/tipsy-linux/tipsy/internal/app"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/perf"
)

type contextNotifier func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
type appRunner func(context.Context, []string, io.Writer, io.Writer) int

func main() {
	os.Exit(tipsyMain(os.Args[1:]))
}

func tipsyMain(args []string) int {
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
	return run(signal.NotifyContext, runner, args, os.Stdout, os.Stderr)
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
