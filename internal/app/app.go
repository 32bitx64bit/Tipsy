// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/diagnostics"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/version"
)

// Run dispatches a CLI invocation. args does not include the program name.
// The returned int is the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}

	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "doctor":
		return cmdDoctor(ctx, rest, stdout, stderr)
	case "inspect":
		return cmdInspect(ctx, rest, stdout, stderr)
	case "diagnose-native":
		return cmdDiagnoseNative(ctx, rest, stdout, stderr)
	case "compare-roblox":
		return cmdCompareRoblox(ctx, rest, stdout, stderr)
	case "report":
		return cmdReport(ctx, rest, stdout, stderr)
	case "diagnose":
		return cmdDiagnose(ctx, rest, stdout, stderr)
	case "config":
		return cmdConfig(rest, stdout, stderr)
	case "logs":
		return cmdLogs(stdout)
	case "setup":
		return cmdSetup(ctx, rest, stdout, stderr)
	case "launch":
		return cmdLaunch(ctx, rest, stdout, stderr)
	case "desktop":
		return cmdDesktop(ctx, rest, stdout, stderr)
	case "repair":
		fmt.Fprintf(stderr, "tipsy %s: %s\n", cmd, "not implemented; use tipsy setup to re-extract")
		return 1
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", cmd)
		printUsage(stderr)
		return 2
	}
}

func cmdDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, doctorHelp)
		return 0
	}
	if len(f.rest) != 0 {
		fmt.Fprintf(stderr, "doctor: unexpected argument %q\n", f.rest[0])
		return 2
	}
	rep := diagnostics.Doctor(ctx)
	if f.json {
		b, err := diagnostics.FormatDoctorJSON(rep)
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		_, _ = stdout.Write(b)
		return 0
	}
	fmt.Fprint(stdout, diagnostics.FormatDoctor(rep))
	return 0
}

func cmdDiagnose(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if f.help {
		fmt.Fprint(stdout, diagnoseHelp)
		return 0
	}
	sub := ""
	if len(f.rest) > 0 {
		sub = f.rest[0]
	}
	if len(f.rest) > 1 {
		fmt.Fprintf(stderr, "diagnose: unexpected extra argument %q\n", f.rest[1])
		return 2
	}
	if sub == "" {
		for i, name := range []string{"x11", "graphics", "audio", "jni", "loader", "roblox", "auth", "gamepad"} {
			rep := diagnostics.Diagnose(ctx, name)
			if f.json {
				b, err := diagnostics.FormatSubsystemJSON(rep)
				if err != nil {
					fmt.Fprintln(stderr, logging.Redact(err.Error()))
					return 1
				}
				_, _ = stdout.Write(b)
			} else {
				if i > 0 {
					fmt.Fprintln(stdout)
				}
				fmt.Fprint(stdout, diagnostics.FormatSubsystem(rep))
			}
		}
		return 0
	}
	rep := diagnostics.Diagnose(ctx, sub)
	if rep.Status == "unknown" {
		fmt.Fprintln(stderr, logging.Redact(rep.Message))
		return 2
	}
	if f.json {
		b, err := diagnostics.FormatSubsystemJSON(rep)
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		_, _ = stdout.Write(b)
		return 0
	}
	fmt.Fprint(stdout, diagnostics.FormatSubsystem(rep))
	return 0
}

func cmdConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, configHelp)
		return 0
	}
	p := config.Paths()
	if len(args) == 0 {
		c, err := config.Load()
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		fmt.Fprintf(stdout, "config file: %s\n", p.ConfigFile)
		b, err := marshalIndent(c)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = stdout.Write(b)
		return 0
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(stdout, p.ConfigFile)
		return 0
	case "get":
		c, err := config.Load()
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		if len(args) == 1 {
			b, err := marshalIndent(c)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			_, _ = stdout.Write(b)
			return 0
		}
		val, err := configGet(c, args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		fmt.Fprintln(stdout, val)
		return 0
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "usage: tipsy config set <key> <value>")
			return 2
		}
		var setErr error
		err := config.Update(func(c *config.Config) error {
			setErr = configSet(c, args[1], strings.Join(args[2:], " "))
			return setErr
		})
		if setErr != nil {
			fmt.Fprintln(stderr, setErr)
			return 2
		}
		if err != nil {
			fmt.Fprintln(stderr, logging.Redact(err.Error()))
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "config: unknown subcommand %q\n", args[0])
		fmt.Fprint(stderr, configHelp)
		return 2
	}
}

func cmdLogs(stdout io.Writer) int {
	p := config.Paths()
	fmt.Fprintf(stdout, "log directory: %s\n", p.LogDir)
	fmt.Fprintln(stdout, "Set TIPSY_LOG to a comma-separated category list or \"all\".")
	fmt.Fprintln(stdout, "Set TIPSY_LOG_LEVEL to debug, info, warn, or error.")
	fmt.Fprintln(stdout, "Example: TIPSY_LOG=apk,elf,loader TIPSY_LOG_LEVEL=debug tipsy inspect Roblox.apk")
	return 0
}

func configGet(c *config.Config, key string) (string, error) {
	switch strings.ToLower(key) {
	case "datadir", "dataDir":
		return c.DataDir, nil
	case "loglevel", "logLevel":
		return c.LogLevel, nil
	case "logcategories", "logCategories":
		return strings.Join(c.LogCategories, ","), nil
	default:
		return "", fmt.Errorf("unknown config key %q (dataDir, logLevel, logCategories)", key)
	}
}

func configSet(c *config.Config, key, value string) error {
	switch strings.ToLower(key) {
	case "datadir", "dataDir":
		c.DataDir = value
	case "loglevel", "logLevel":
		c.LogLevel = value
	case "logcategories", "logCategories":
		var cats []string
		for _, p := range strings.Split(value, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				cats = append(cats, p)
			}
		}
		c.LogCategories = cats
	default:
		return fmt.Errorf("unknown config key %q (dataDir, logLevel, logCategories)", key)
	}
	return nil
}

type flags struct {
	json bool
	help bool
	rest []string
}

func parseFlags(args []string) (flags, error) {
	var f flags
	for _, a := range args {
		switch a {
		case "--json":
			f.json = true
		case "-h", "--help":
			f.help = true
		default:
			if strings.HasPrefix(a, "-") {
				return f, fmt.Errorf("unknown flag %s", a)
			}
			f.rest = append(f.rest, a)
		}
	}
	return f, nil
}

func writeJSON(stdout io.Writer, data []byte) {
	_, _ = stdout.Write(data)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
}
