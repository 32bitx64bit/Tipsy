// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Schemes are the URI schemes the Play launcher handles.
var Schemes = []string{"x-scheme-handler/roblox-player", "x-scheme-handler/roblox"}

// Runner executes a desktop helper (update-desktop-database, xdg-mime).
// A missing helper is not an error; integration is best-effort there.
type Runner func(ctx context.Context, name string, args ...string) error

// ExecRunner runs helpers from PATH with a short timeout and no output.
func ExecRunner(ctx context.Context, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// AdoptOptions controls Adopt.
type AdoptOptions struct {
	Identity Identity
	Launcher Launcher
	// IconSource is a PNG copied into the user icon theme so the entries have
	// an icon even when no package installed one. Optional.
	IconSource string
	// IfUnowned makes Adopt a no-op (ErrOwnedElsewhere) when a package or
	// Flatpak install already provides the identity. AppRun uses this on
	// every start so an AppImage never silently takes over a real install.
	IfUnowned bool
	// Handler registers the Play entry as the default roblox:// handler.
	Handler bool
	Run     Runner
}

// AdoptResult reports what Adopt did.
type AdoptResult struct {
	Written []string
	Removed []string
	Handler bool
	Before  Status
}

// Adopt writes user-scope Play and Settings entries for the identity so the
// desktop resolves it to this install, cleans up duplicate launchers other
// tools created for the same origin, refreshes the desktop database, and
// optionally claims the roblox:// handler.
func Adopt(ctx context.Context, env Env, opts AdoptOptions) (AdoptResult, error) {
	result := AdoptResult{Before: Resolve(env, opts.Identity)}
	if opts.IfUnowned && result.Before.OwnedElsewhere() {
		return result, fmt.Errorf("%w: %s", ErrOwnedElsewhere, result.Before.Owner.Path)
	}
	if opts.Launcher.Play == "" || opts.Launcher.Settings == "" {
		return result, errors.New("launcher has no Exec commands")
	}
	run := opts.Run
	if run == nil {
		run = ExecRunner
	}
	appsDir := env.ApplicationsDir()
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return result, err
	}

	iconRef := ""
	if opts.IconSource != "" {
		ref, err := installIcons(env, opts.Identity, opts.IconSource)
		if err != nil {
			return result, fmt.Errorf("install icon: %w", err)
		}
		iconRef = ref
	}
	// AppRun adopts on every start; only touch the desktop when something
	// actually differs so a launch is not a menu-cache rebuild.
	changed := false
	for _, entry := range Render(RenderOptions{Identity: opts.Identity, Launcher: opts.Launcher, Icon: iconRef}) {
		path := filepath.Join(appsDir, entry.Name)
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, entry.Body) {
			continue
		}
		if err := writeAtomic(path, entry.Body); err != nil {
			return result, err
		}
		result.Written = append(result.Written, path)
		changed = true
	}
	if opts.Launcher.Origin != "" {
		result.Removed = removeForeignLaunchers(env, opts.Identity, opts.Launcher.Origin)
		changed = changed || len(result.Removed) > 0
	}
	if changed {
		_ = run(ctx, "update-desktop-database", appsDir)
	}
	if opts.Handler {
		if changed || !result.Before.HandledByIdentity {
			for _, scheme := range Schemes {
				if err := run(ctx, "xdg-mime", "default", opts.Identity.File(Play), scheme); err != nil {
					return result, fmt.Errorf("register %s handler: %w", scheme, err)
				}
			}
		}
		result.Handler = true
	}
	return result, nil
}

// ReleaseResult reports what Release removed.
type ReleaseResult struct {
	Removed []string
	// Kept lists user-scope entries for the identity that Tipsy did not write
	// and therefore left alone.
	Kept   []string
	Before Status
}

// Release removes the identity's user-scope entries (and icons) that Tipsy
// wrote, so the package or Flatpak copy underneath becomes the launcher. The
// roblox:// handler registration is left as it is: it names the desktop-file
// ID, which now resolves to that copy.
func Release(ctx context.Context, env Env, id Identity, run Runner) (ReleaseResult, error) {
	result := ReleaseResult{Before: Resolve(env, id)}
	if run == nil {
		run = ExecRunner
	}
	appsDir := env.ApplicationsDir()
	for _, kind := range Kinds {
		path := filepath.Join(appsDir, id.File(kind))
		parsed, err := parseDesktopFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, err
		}
		if !writtenByTipsy(parsed) {
			result.Kept = append(result.Kept, path)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		result.Removed = append(result.Removed, path)
	}
	for _, size := range iconSizes {
		icon := filepath.Join(env.IconDir(size), id.IconName()+".png")
		if err := os.Remove(icon); err == nil {
			result.Removed = append(result.Removed, icon)
		}
	}
	if len(result.Removed) > 0 {
		_ = run(ctx, "update-desktop-database", appsDir)
	}
	return result, nil
}

// writtenByTipsy recognizes entries Adopt wrote (markers) and the entries the
// pre-marker AppRun wrote (Exec pointing at an AppImage).
func writtenByTipsy(parsed *desktopFile) bool {
	if _, ok := parsed.main[markerMedium]; ok {
		return true
	}
	return strings.HasSuffix(strings.ToLower(parsed.execProgram()), ".appimage")
}

var iconSizes = []string{"256x256", "512x512"}

// installIcons copies the PNG into the user hicolor theme under the
// identity's icon name and returns the absolute path entries should
// reference (menus refresh unreliably on theme-name lookups for new icons).
func installIcons(env Env, id Identity, source string) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	ref := ""
	for _, size := range iconSizes {
		dir := env.IconDir(size)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		path := filepath.Join(dir, id.IconName()+".png")
		if err := writeAtomic(path, data); err != nil {
			return "", err
		}
		if ref == "" {
			ref = path
		}
	}
	return ref, nil
}

// removeForeignLaunchers deletes other user-scope entries that start the
// same origin (AppImage managers write a second "Play" row for the same file
// and can leave an older appimage_tipsy_*.desktop behind after an update).
func removeForeignLaunchers(env Env, id Identity, origin string) []string {
	appsDir := env.ApplicationsDir()
	matches, err := filepath.Glob(filepath.Join(appsDir, "*.desktop"))
	if err != nil {
		return nil
	}
	var removed []string
	for _, path := range matches {
		base := filepath.Base(path)
		if base == id.File(Play) || base == id.File(Settings) {
			continue
		}
		parsed, err := parseDesktopFile(path)
		if err != nil {
			continue
		}
		foreign := strings.HasPrefix(base, "appimage_tipsy_") || sameOrigin(parsed.execProgram(), origin)
		if !foreign {
			continue
		}
		removeManagerIcons(env, parsed.main["Icon"])
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
		}
	}
	return removed
}

// removeManagerIcons drops the appimage_tipsy_* theme icons AppImage
// managers install alongside their own entries.
func removeManagerIcons(env Env, icon string) {
	if !strings.HasPrefix(icon, "appimage_tipsy_") || strings.ContainsAny(icon, "/\\") {
		return
	}
	pattern := filepath.Join(env.DataHome, "icons", "hicolor", "*", "apps", icon+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
