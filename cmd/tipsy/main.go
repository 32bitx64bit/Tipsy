// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/tipsy-linux/tipsy/internal/app"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

type contextNotifier func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
type appRunner func(context.Context, []string, io.Writer, io.Writer) int

func main() {
	logging.Init()
	os.Exit(run(signal.NotifyContext, app.Run, os.Args[1:], os.Stdout, os.Stderr))
}

func run(notify contextNotifier, runApp appRunner, args []string, stdout, stderr io.Writer) int {
	ctx, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runApp(ctx, args, stdout, stderr)
}
