// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package desktop

import (
	"fmt"
	"strings"
)

// Medium is the packaging form a Tipsy binary runs from.
type Medium string

const (
	MediumUnknown  Medium = ""
	MediumAppImage Medium = "appimage"
	MediumFlatpak  Medium = "flatpak"
	MediumSystem   Medium = "system"
	MediumSource   Medium = "source"
)

// Launcher is how a desktop entry starts one particular install of Tipsy.
// Every field is a complete Exec= value; field codes (%u) are included where
// the entry receives a URI.
type Launcher struct {
	Play           string
	Settings       string
	SettingsAction string
	// Medium and Origin are recorded in user-scope entries (X-Tipsy-Medium,
	// X-Tipsy-Origin) so ownership can be resolved later without heuristics.
	Medium Medium
	Origin string
}

// SystemLauncher starts the tipsy-gui found on PATH; this is what distro
// packages and the Flatpak ship (Flatpak rewrites Exec on export).
func SystemLauncher() Launcher {
	return Launcher{
		Play:           "tipsy-gui --play %u",
		Settings:       "tipsy-gui %u",
		SettingsAction: "tipsy-gui",
		Medium:         MediumSystem,
	}
}

// AppImageLauncher starts a specific AppImage file through its AppRun, which
// understands --play and --settings.
func AppImageLauncher(path string) Launcher {
	quoted := QuoteExecArgument(path)
	return Launcher{
		Play:           quoted + " --play %u",
		Settings:       quoted + " --settings %u",
		SettingsAction: quoted + " --settings",
		Medium:         MediumAppImage,
		Origin:         path,
	}
}

// ExecutableLauncher starts an absolute tipsy-gui binary (source builds,
// prefix installs whose bin directory is not on the session PATH).
func ExecutableLauncher(guiPath string, medium Medium) Launcher {
	quoted := QuoteExecArgument(guiPath)
	return Launcher{
		Play:           quoted + " --play %u",
		Settings:       quoted + " %u",
		SettingsAction: quoted,
		Medium:         medium,
		Origin:         guiPath,
	}
}

// QuoteExecArgument quotes one argument for an Exec= line per the Desktop
// Entry Specification: double quotes, with `"`, "`", "$" and "\" escaped by a
// backslash. The general string escape rule is applied before the quoting
// rule, so a literal backslash needs four characters. Paths without reserved
// characters are still quoted so a space can never split the command.
func QuoteExecArgument(argument string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range argument {
		switch r {
		case '\\':
			b.WriteString(`\\\\`)
			continue
		case '"', '`', '$':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// Entry is one rendered desktop file.
type Entry struct {
	// Name is the file basename (the desktop-file ID).
	Name string
	Body []byte
}

// RenderOptions controls the rendered entries.
type RenderOptions struct {
	Identity Identity
	Launcher Launcher
	// Icon overrides the Icon= value (a theme name or absolute path). Empty
	// keeps the shared "tipsy" theme icon used by system packages.
	Icon string
}

// Render produces the Play and Settings entries for one install. The system
// rendering of the stable identity is byte-identical to the templates in
// share/applications, which packaging copies verbatim (enforced by test).
func Render(opts RenderOptions) []Entry {
	return []Entry{
		{Name: opts.Identity.File(Play), Body: []byte(renderPlay(opts))},
		{Name: opts.Identity.File(Settings), Body: []byte(renderSettings(opts))},
	}
}

func renderPlay(opts RenderOptions) string {
	id := opts.Identity
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Version=1.0\n")
	fmt.Fprintf(&b, "Name=%s\n", id.Title(Play))
	b.WriteString("GenericName=Roblox\n")
	fmt.Fprintf(&b, "Comment=%s\n", comment(id, "Launch Roblox with Tipsy"))
	fmt.Fprintf(&b, "Exec=%s\n", opts.Launcher.Play)
	fmt.Fprintf(&b, "Icon=%s\n", icon(opts))
	b.WriteString("Terminal=false\n")
	b.WriteString("Categories=Game;\n")
	fmt.Fprintf(&b, "Keywords=Roblox;Game;Linux;Launcher;Play;%s\n", keywords(id))
	b.WriteString("MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n")
	b.WriteString("StartupNotify=true\n")
	b.WriteString("StartupWMClass=roblox\n")
	b.WriteString("Actions=Settings;\n")
	b.WriteString("X-AppImage-Integrate=false\n")
	writeMarkers(&b, opts)
	b.WriteString("\n")
	b.WriteString("[Desktop Action Settings]\n")
	fmt.Fprintf(&b, "Name=%s\n", id.Title(Settings))
	fmt.Fprintf(&b, "Icon=%s\n", icon(opts))
	fmt.Fprintf(&b, "Exec=%s\n", opts.Launcher.SettingsAction)
	return b.String()
}

func renderSettings(opts RenderOptions) string {
	id := opts.Identity
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Version=1.0\n")
	fmt.Fprintf(&b, "Name=%s\n", id.Title(Settings))
	fmt.Fprintf(&b, "GenericName=%s settings\n", id.Name)
	fmt.Fprintf(&b, "Comment=%s\n", comment(id, "Open the Tipsy setup and settings menu"))
	fmt.Fprintf(&b, "Exec=%s\n", opts.Launcher.Settings)
	fmt.Fprintf(&b, "Icon=%s\n", icon(opts))
	b.WriteString("Terminal=false\n")
	b.WriteString("Categories=Game;\n")
	fmt.Fprintf(&b, "Keywords=Roblox;Game;Linux;Launcher;Settings;Setup;%s\n", keywords(id))
	b.WriteString("StartupNotify=true\n")
	b.WriteString("StartupWMClass=tipsy-gui\n")
	b.WriteString("X-AppImage-Integrate=false\n")
	writeMarkers(&b, opts)
	return b.String()
}

func icon(opts RenderOptions) string {
	if opts.Icon != "" {
		return opts.Icon
	}
	return "tipsy"
}

func comment(id Identity, base string) string {
	if id.Development() {
		return base + " (development build)"
	}
	return base
}

func keywords(id Identity) string {
	if id.Development() {
		return "Development;"
	}
	return ""
}

// Marker keys let ownership resolution identify entries Tipsy wrote itself.
const (
	markerMedium  = "X-Tipsy-Medium"
	markerOrigin  = "X-Tipsy-Origin"
	markerChannel = "X-Tipsy-Channel"
)

func writeMarkers(b *strings.Builder, opts RenderOptions) {
	if opts.Launcher.Medium == MediumUnknown || opts.Launcher.Medium == MediumSystem && opts.Launcher.Origin == "" {
		// Shared templates carry no markers; packaging installs them as-is.
		return
	}
	fmt.Fprintf(b, "%s=%s\n", markerMedium, opts.Launcher.Medium)
	if opts.Launcher.Origin != "" {
		fmt.Fprintf(b, "%s=%s\n", markerOrigin, opts.Launcher.Origin)
	}
	fmt.Fprintf(b, "%s=%s\n", markerChannel, opts.Identity.Channel)
}
