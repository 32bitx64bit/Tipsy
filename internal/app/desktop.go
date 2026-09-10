// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/desktop"
	"github.com/tipsy-linux/tipsy/internal/version"
)

const desktopHelp = `Usage: tipsy desktop <status|adopt|release|render> [options]

Desktop launcher integration. One desktop-file ID is one identity: stable
builds are "Tipsy" (io.github.tipsy_linux.Tipsy), development builds are
"Tipsy-Dev" (io.github.tipsy_linux.Tipsy.Dev), so a developer's daily
install and work-in-progress builds never fight over the same launcher.

  status [--json]        Who provides this build's launcher and the roblox://
                         handler (user-scope entry, Flatpak, distro package).
  adopt [options]        Write user-scope launcher entries for this install
                         (AppImage, or the tipsy-gui next to this binary).
      --if-unowned       Do nothing when a package or Flatpak already provides
                         the identity (what AppRun does on every start).
      --appimage PATH    AppImage to start (default: $APPIMAGE).
      --icon PATH        PNG to install as the launcher icon.
      --handler          Also register as the roblox:// handler
      --no-handler       (default: yes for stable builds, no for dev builds).
  release                Remove the user-scope entries Tipsy wrote so the
                         package or Flatpak install becomes the launcher.
  render [--channel stable|dev] [--out DIR]
                         Print (or write) the system-style entries packaging
                         ships; share/applications must match the stable set.
`

func cmdDesktop(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, desktopHelp)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, desktopHelp)
		return 0
	case "status":
		return desktopStatus(rest, stdout, stderr)
	case "adopt":
		return desktopAdopt(ctx, rest, stdout, stderr)
	case "release":
		return desktopRelease(ctx, rest, stdout, stderr)
	case "render":
		return desktopRender(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tipsy desktop: unknown subcommand %q\n", sub)
		fmt.Fprint(stderr, desktopHelp)
		return 2
	}
}

func desktopStatus(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Fprint(stdout, desktopHelp)
			return 0
		default:
			fmt.Fprintf(stderr, "tipsy desktop status: unexpected argument %q\n", arg)
			return 2
		}
	}
	env := desktop.EnvFromOS()
	id := desktop.Current()
	status := desktop.Resolve(env, id)
	medium, origin := desktop.CurrentMedium()
	plan := desktop.Plan(status, medium, origin)
	if asJSON {
		out, err := marshalIndent(struct {
			desktop.Status
			Medium  desktop.Medium `json:"medium"`
			Origin  string         `json:"origin,omitempty"`
			Version string         `json:"version"`
			Channel string         `json:"channel"`
			Plan    desktop.Action `json:"plan"`
		}{status, medium, origin, version.String(), version.Channel, plan})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = stdout.Write(out)
		return 0
	}
	fmt.Fprintf(stdout, "This build:   %s %s (%s channel, %s)\n", id.Name, version.String(), version.Channel, describeMedium(medium, origin))
	fmt.Fprintf(stdout, "Identity:     %s\n", id.File(desktop.Play))
	if status.Owner == nil {
		fmt.Fprintln(stdout, "Launcher:     not provided by any installation")
	} else {
		fmt.Fprintf(stdout, "Launcher:     %s\n", desktop.DescribeProvider(*status.Owner))
	}
	for _, p := range status.Providers[min(1, len(status.Providers)):] {
		fmt.Fprintf(stdout, "  shadowed:   %s\n", desktop.DescribeProvider(p))
	}
	switch {
	case status.Handler == "":
		fmt.Fprintln(stdout, "roblox:// →   no default handler registered")
	case status.HandledByIdentity:
		fmt.Fprintf(stdout, "roblox:// →   %s (this identity)\n", status.Handler)
	default:
		fmt.Fprintf(stdout, "roblox:// →   %s\n", status.Handler)
	}
	fmt.Fprintf(stdout, "Suggested:    %s\n", describePlan(plan))
	return 0
}

func describeMedium(medium desktop.Medium, origin string) string {
	switch medium {
	case desktop.MediumAppImage:
		return "AppImage " + origin
	case desktop.MediumFlatpak:
		return "Flatpak"
	case desktop.MediumSystem:
		return "system install " + origin
	case desktop.MediumSource:
		return "source build " + origin
	default:
		return "unknown install"
	}
}

func describePlan(plan desktop.Action) string {
	switch plan {
	case desktop.ActionAdopt:
		return "tipsy desktop adopt   (make this install the launcher)"
	case desktop.ActionRelease:
		return "tipsy desktop release (remove the user-scope entry shadowing this install)"
	default:
		return "nothing; this install provides the launcher"
	}
}

func desktopAdopt(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	medium, origin := desktop.CurrentMedium()
	id := desktop.Current()
	opts := desktop.AdoptOptions{Identity: id, Handler: !id.Development()}
	appimage := ""
	icon := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--if-unowned":
			opts.IfUnowned = true
		case "--handler":
			opts.Handler = true
		case "--no-handler":
			opts.Handler = false
		case "--appimage", "--icon":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "tipsy desktop adopt: %s needs a path\n", args[i])
				return 2
			}
			if args[i] == "--appimage" {
				appimage = args[i+1]
			} else {
				icon = args[i+1]
			}
			i++
		case "-h", "--help":
			fmt.Fprint(stdout, desktopHelp)
			return 0
		default:
			fmt.Fprintf(stderr, "tipsy desktop adopt: unexpected argument %q\n", args[i])
			return 2
		}
	}
	if appimage != "" {
		medium, origin = desktop.MediumAppImage, appimage
	}
	launcher, err := desktop.LauncherFor(medium, origin)
	if err != nil {
		fmt.Fprintf(stderr, "tipsy desktop adopt: %v\n", err)
		return 1
	}
	opts.Launcher = launcher
	if icon != "" {
		if info, err := os.Stat(icon); err != nil || !info.Mode().IsRegular() {
			fmt.Fprintf(stderr, "tipsy desktop adopt: --icon %s is not a readable file\n", icon)
			return 2
		}
		opts.IconSource = icon
	} else {
		opts.IconSource = desktop.IconSource(medium)
	}
	result, err := desktop.Adopt(ctx, desktop.EnvFromOS(), opts)
	if err != nil {
		if errors.Is(err, desktop.ErrOwnedElsewhere) {
			fmt.Fprintf(stdout, "%s is provided by %s; leaving it alone (run without --if-unowned to take over).\n",
				id.Name, desktop.DescribeProvider(*result.Before.Owner))
			return 0
		}
		fmt.Fprintf(stderr, "tipsy desktop adopt: %v\n", err)
		return 1
	}
	for _, path := range result.Written {
		fmt.Fprintf(stdout, "wrote   %s\n", path)
	}
	for _, path := range result.Removed {
		fmt.Fprintf(stdout, "removed %s (duplicate launcher)\n", path)
	}
	if result.Handler {
		fmt.Fprintf(stdout, "%s now handles roblox:// links.\n", id.Name)
	}
	return 0
}

func desktopRelease(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		switch arg {
		case "-h", "--help":
			fmt.Fprint(stdout, desktopHelp)
			return 0
		default:
			fmt.Fprintf(stderr, "tipsy desktop release: unexpected argument %q\n", arg)
			return 2
		}
	}
	id := desktop.Current()
	result, err := desktop.Release(ctx, desktop.EnvFromOS(), id, nil)
	if err != nil {
		fmt.Fprintf(stderr, "tipsy desktop release: %v\n", err)
		return 1
	}
	for _, path := range result.Removed {
		fmt.Fprintf(stdout, "removed %s\n", path)
	}
	for _, path := range result.Kept {
		fmt.Fprintf(stdout, "kept    %s (not written by Tipsy)\n", path)
	}
	if len(result.Removed) == 0 && len(result.Kept) == 0 {
		fmt.Fprintf(stdout, "%s has no user-scope launcher entries to remove.\n", id.Name)
	}
	return 0
}

func desktopRender(args []string, stdout, stderr io.Writer) int {
	channel := version.ChannelStable
	out := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--channel", "--out":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "tipsy desktop render: %s needs a value\n", args[i])
				return 2
			}
			if args[i] == "--channel" {
				channel = args[i+1]
			} else {
				out = args[i+1]
			}
			i++
		case "-h", "--help":
			fmt.Fprint(stdout, desktopHelp)
			return 0
		default:
			fmt.Fprintf(stderr, "tipsy desktop render: unexpected argument %q\n", args[i])
			return 2
		}
	}
	if channel != version.ChannelStable && channel != version.ChannelDevelopment {
		fmt.Fprintf(stderr, "tipsy desktop render: channel must be %s or %s\n", version.ChannelStable, version.ChannelDevelopment)
		return 2
	}
	entries := desktop.Render(desktop.RenderOptions{Identity: desktop.ForChannel(channel), Launcher: desktop.SystemLauncher()})
	if out == "" {
		for i, entry := range entries {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			fmt.Fprintf(stdout, "# %s\n", entry.Name)
			_, _ = stdout.Write(entry.Body)
		}
		return 0
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintf(stderr, "tipsy desktop render: %v\n", err)
		return 1
	}
	for _, entry := range entries {
		path := filepath.Join(out, entry.Name)
		if err := os.WriteFile(path, entry.Body, 0o644); err != nil {
			fmt.Fprintf(stderr, "tipsy desktop render: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", path)
	}
	return 0
}
