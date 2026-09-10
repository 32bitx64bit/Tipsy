// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scope is where a desktop entry lives.
type Scope string

const (
	// ScopeUser is $XDG_DATA_HOME/applications; it outranks every data dir.
	ScopeUser Scope = "user"
	// ScopeSystem is any $XDG_DATA_DIRS/applications entry (distro packages,
	// Flatpak exports).
	ScopeSystem Scope = "system"
)

// Provider is one installed copy of a launcher identity found on the search
// path.
type Provider struct {
	// Path is the desktop file that was found.
	Path string `json:"path"`
	// Scope is user or system.
	Scope Scope `json:"scope"`
	// Medium is the packaging form that installed it, as far as it can be told.
	Medium Medium `json:"medium"`
	// Origin is the AppImage file or binary the entry starts, when known.
	Origin string `json:"origin,omitempty"`
	// Exec is the raw Exec= value.
	Exec string `json:"exec"`
	// Marked is true when the entry carries Tipsy's own X-Tipsy-* keys.
	Marked bool `json:"marked"`
}

// Status describes who provides one launcher identity on this machine.
type Status struct {
	Identity Identity `json:"identity"`
	// Owner is the provider the desktop actually resolves the identity to
	// (first hit in precedence order), or nil when nothing provides it.
	Owner *Provider `json:"owner,omitempty"`
	// Providers lists every copy found, in precedence order (Owner first).
	Providers []Provider `json:"providers"`
	// Handler is the desktop-file ID registered for roblox:// URIs, or "".
	Handler string `json:"handler,omitempty"`
	// HandledByIdentity is true when Handler is this identity's Play entry.
	HandledByIdentity bool `json:"handledByIdentity"`
}

// Owned reports whether anything provides the identity.
func (s Status) Owned() bool { return s.Owner != nil }

// OwnedElsewhere reports whether the identity resolves to a system-scope
// provider (distro package or Flatpak): an install that Tipsy must not shadow
// with user-scope entries unless the user asks for it.
func (s Status) OwnedElsewhere() bool {
	return s.Owner != nil && s.Owner.Scope == ScopeSystem
}

// SystemProviders lists the system-scope copies (package/Flatpak installs)
// regardless of whether a user-scope entry currently shadows them.
func (s Status) SystemProviders() []Provider {
	var out []Provider
	for _, p := range s.Providers {
		if p.Scope == ScopeSystem {
			out = append(out, p)
		}
	}
	return out
}

// Resolve looks the identity up the way the desktop does: the user-scope
// applications directory first, then each XDG_DATA_DIRS entry in order.
func Resolve(env Env, id Identity) Status {
	status := Status{Identity: id}
	name := id.File(Play)
	for _, dir := range env.searchDirs() {
		path := filepath.Join(dir.path, name)
		parsed, err := parseDesktopFile(path)
		if err != nil {
			continue
		}
		provider := classify(parsed, dir.scope)
		status.Providers = append(status.Providers, provider)
	}
	if len(status.Providers) > 0 {
		owner := status.Providers[0]
		status.Owner = &owner
	}
	status.Handler = env.defaultHandler("x-scheme-handler/roblox")
	if status.Handler == "" {
		status.Handler = env.defaultHandler("x-scheme-handler/roblox-player")
	}
	status.HandledByIdentity = status.Handler == name
	return status
}

func classify(parsed *desktopFile, scope Scope) Provider {
	provider := Provider{
		Path:  parsed.path,
		Scope: scope,
		Exec:  parsed.main["Exec"],
	}
	if medium := Medium(parsed.main[markerMedium]); medium != MediumUnknown {
		provider.Marked = true
		provider.Medium = medium
		provider.Origin = parsed.main[markerOrigin]
		return provider
	}
	program := parsed.execProgram()
	switch {
	case strings.Contains(parsed.path, "/flatpak/exports/"),
		program == "flatpak" || strings.HasSuffix(program, "/flatpak"):
		provider.Medium = MediumFlatpak
	case strings.HasSuffix(strings.ToLower(program), ".appimage"):
		// Entries written by AppRun before ownership markers existed.
		provider.Medium = MediumAppImage
		provider.Origin = program
	case scope == ScopeSystem:
		provider.Medium = MediumSystem
	case filepath.IsAbs(program):
		provider.Medium = MediumSource
		provider.Origin = program
	default:
		// A prefix install copied into ~/.local/share/applications
		// (scripts/install-desktop.sh) or a hand-written entry.
		provider.Medium = MediumSystem
	}
	return provider
}

// DescribeProvider renders one provider for people ("Flatpak (system scope)").
func DescribeProvider(p Provider) string {
	var what string
	switch p.Medium {
	case MediumAppImage:
		what = "AppImage " + p.Origin
	case MediumFlatpak:
		what = "Flatpak"
	case MediumSource:
		what = "source build " + p.Origin
	case MediumSystem:
		if p.Scope == ScopeUser {
			what = "user-level install (" + p.Exec + ")"
		} else {
			what = "system package"
		}
	default:
		what = "unknown install"
	}
	return fmt.Sprintf("%s [%s]", what, p.Path)
}

// Action is what the running install can do about the launcher.
type Action string

const (
	// ActionNone: the running install already provides the launcher.
	ActionNone Action = "none"
	// ActionAdopt: write user-scope entries so this install provides it.
	ActionAdopt Action = "adopt"
	// ActionRelease: remove this identity's user-scope entries so the
	// package/Flatpak install underneath becomes the launcher again.
	ActionRelease Action = "release"
)

// Plan decides which action makes sense for a process running from medium
// (with origin = its AppImage path or binary) given the resolved status.
func Plan(status Status, medium Medium, origin string) Action {
	owner := status.Owner
	switch medium {
	case MediumFlatpak, MediumSystem:
		// Package-style installs are provided by their own exported files;
		// the only thing that can hide them is a user-scope entry.
		if owner != nil && owner.Scope == ScopeUser {
			return ActionRelease
		}
		return ActionNone
	default:
		if owner != nil && owner.Scope == ScopeUser && owner.Medium == medium && sameOrigin(owner.Origin, origin) {
			return ActionNone
		}
		return ActionAdopt
	}
}

func insideDir(path, dir string) bool {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func sameOrigin(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// ErrOwnedElsewhere is returned by Adopt when IfUnowned is set and a
// package or Flatpak install already provides the identity.
var ErrOwnedElsewhere = errors.New("desktop launcher is provided by another installation")

// CurrentMedium detects the packaging form of the running process.
//
// APPIMAGE is inherited by every child of any AppImage (a terminal started
// from an AppImage editor carries the editor's path), so it only counts when
// the running executable really lives inside that AppImage's mount (APPDIR).
func CurrentMedium() (Medium, string) {
	if InFlatpak() {
		return MediumFlatpak, ""
	}
	exe, err := os.Executable()
	if err != nil {
		return MediumUnknown, ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if image := strings.TrimSpace(os.Getenv("APPIMAGE")); filepath.IsAbs(image) {
		if appDir := strings.TrimSpace(os.Getenv("APPDIR")); filepath.IsAbs(appDir) && insideDir(exe, appDir) {
			return MediumAppImage, image
		}
	}
	gui := filepath.Join(filepath.Dir(exe), "tipsy-gui")
	for _, prefix := range []string{"/usr/bin/", "/usr/local/bin/", "/opt/"} {
		if strings.HasPrefix(exe, prefix) {
			return MediumSystem, gui
		}
	}
	return MediumSource, gui
}

// IconSource finds the PNG an AppImage should install for its user-scope
// entries: the tipsy.png at the payload root (APPDIR, exported by the
// AppImage runtime and AppRun). Other media rely on the theme icon their
// package installed, so this returns "" for them.
func IconSource(medium Medium) string {
	if medium != MediumAppImage {
		return ""
	}
	for _, key := range []string{"APPDIR", "TIPSY_RELEASE_APPDIR"} {
		dir := strings.TrimSpace(os.Getenv(key))
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, "tipsy.png")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

// LauncherFor builds the launcher a process from medium/origin should
// register: the AppImage file, or the tipsy-gui binary next to the running
// executable.
func LauncherFor(medium Medium, origin string) (Launcher, error) {
	switch medium {
	case MediumAppImage:
		if !filepath.IsAbs(origin) {
			return Launcher{}, errors.New("AppImage path must be absolute")
		}
		return AppImageLauncher(origin), nil
	case MediumSource, MediumSystem:
		if !filepath.IsAbs(origin) {
			return Launcher{}, errors.New("tipsy-gui path must be absolute")
		}
		if _, err := os.Stat(origin); err != nil {
			return Launcher{}, err
		}
		return ExecutableLauncher(origin, medium), nil
	case MediumFlatpak:
		return Launcher{}, errors.New("a Flatpak is integrated by its own exported entries")
	default:
		return Launcher{}, errors.New("cannot tell how this Tipsy was installed")
	}
}
