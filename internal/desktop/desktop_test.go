// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package desktop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// The shared templates in share/applications are what deb, rpm, Flatpak and
// the AppDir install verbatim. They must stay identical to what this package
// renders for a stable system install, so there is one source of truth.
func TestSystemRenderingMatchesSharedTemplates(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, entry := range Render(RenderOptions{Identity: Stable, Launcher: SystemLauncher()}) {
		want, err := os.ReadFile(filepath.Join(root, "share", "applications", entry.Name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(entry.Body, want) {
			t.Errorf("%s differs from share/applications template:\n--- rendered\n%s\n--- template\n%s", entry.Name, entry.Body, want)
		}
	}
}

func TestDevelopmentIdentityIsDistinct(t *testing.T) {
	t.Parallel()
	entries := Render(RenderOptions{Identity: Development, Launcher: AppImageLauncher("/home/dev/Tipsy-0.0.0-dev-x86_64.AppImage")})
	if entries[0].Name != "io.github.tipsy_linux.Tipsy.Dev.Play.desktop" || entries[1].Name != "io.github.tipsy_linux.Tipsy.Dev.Settings.desktop" {
		t.Fatalf("dev entry names = %q, %q", entries[0].Name, entries[1].Name)
	}
	play, settings := string(entries[0].Body), string(entries[1].Body)
	for _, want := range []string{
		"Name=Tipsy-Dev - Play\n",
		"Comment=Launch Roblox with Tipsy (development build)\n",
		`Exec="/home/dev/Tipsy-0.0.0-dev-x86_64.AppImage" --play %u` + "\n",
		"X-Tipsy-Medium=appimage\n",
		"X-Tipsy-Origin=/home/dev/Tipsy-0.0.0-dev-x86_64.AppImage\n",
		"X-Tipsy-Channel=dev\n",
		"[Desktop Action Settings]\nName=Tipsy-Dev - Settings\n",
	} {
		if !strings.Contains(play, want) {
			t.Errorf("dev Play entry missing %q:\n%s", want, play)
		}
	}
	for _, want := range []string{"Name=Tipsy-Dev - Settings\n", "GenericName=Tipsy-Dev settings\n", `Exec="/home/dev/Tipsy-0.0.0-dev-x86_64.AppImage" --settings %u` + "\n"} {
		if !strings.Contains(settings, want) {
			t.Errorf("dev Settings entry missing %q:\n%s", want, settings)
		}
	}
	if Stable.File(Play) == Development.File(Play) {
		t.Fatal("stable and dev identities must not share a desktop-file ID")
	}
	if !strings.Contains(play, "MimeType=x-scheme-handler/roblox;") {
		t.Fatal("dev Play must still advertise the roblox schemes so a developer can pick it in a chooser")
	}
}

func TestQuoteExecArgumentRoundTrips(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/home/g/Applications/Tipsy-1.2.0-x86_64.AppImage",
		"/home/g/My Apps/Tipsy.AppImage",
		`/odd/"quote"/Tipsy.AppImage`,
		"/odd/$HOME/`tick`/Tipsy.AppImage",
	} {
		quoted := QuoteExecArgument(path)
		if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
			t.Errorf("%q not quoted: %s", path, quoted)
		}
		if got := firstExecArgument(quoted + " --play %u"); got != path {
			t.Errorf("firstExecArgument(%s) = %q, want %q", quoted, got, path)
		}
	}
	if got := firstExecArgument("tipsy-gui --play %u"); got != "tipsy-gui" {
		t.Errorf("unquoted program = %q", got)
	}
	if got := firstExecArgument(`/usr/bin/flatpak run --command=tipsy-gui io.github.tipsy_linux.Tipsy`); got != "/usr/bin/flatpak" {
		t.Errorf("flatpak program = %q", got)
	}
}

type fixture struct {
	env      Env
	flatpak  string // a XDG_DATA_DIRS entry mimicking /var/lib/flatpak/exports/share
	usrShare string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		flatpak:  filepath.Join(root, "var", "lib", "flatpak", "exports", "share"),
		usrShare: filepath.Join(root, "usr", "share"),
	}
	f.env = Env{
		Home:       filepath.Join(root, "home"),
		DataHome:   filepath.Join(root, "home", ".local", "share"),
		ConfigHome: filepath.Join(root, "home", ".config"),
		DataDirs:   []string{filepath.Join(root, "usr", "local", "share"), f.usrShare, f.flatpak},
	}
	return f
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const flatpakExport = "[Desktop Entry]\nType=Application\nName=Tipsy - Play\nExec=/usr/bin/flatpak run --branch=master --arch=x86_64 --command=tipsy-gui --file-forwarding io.github.tipsy_linux.Tipsy --play @@u %u @@\nIcon=io.github.tipsy_linux.Tipsy\nX-Flatpak=io.github.tipsy_linux.Tipsy\n"

const legacyAppImagePin = "[Desktop Entry]\nType=Application\nName=Tipsy - Play\nExec=\"/home/g/Applications/Tipsy-0.0.0-dev.perf2-x86_64.AppImage\" --play %u\nIcon=/home/g/.local/share/icons/hicolor/256x256/apps/io.github.tipsy_linux.Tipsy.png\n"

type recorder struct{ calls []string }

func (r *recorder) run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	return nil
}

// The situation that motivated this package: a Flatpak is installed, but a
// user-scope entry an older AppImage wrote still wins by XDG precedence.
func TestResolveUserScopeAppImageShadowsFlatpak(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	writeFile(t, filepath.Join(f.flatpak, "applications", Stable.File(Play)), flatpakExport)
	writeFile(t, filepath.Join(f.env.ApplicationsDir(), Stable.File(Play)), legacyAppImagePin)
	writeFile(t, filepath.Join(f.env.ConfigHome, "mimeapps.list"), "[Default Applications]\nx-scheme-handler/roblox=io.github.tipsy_linux.Tipsy.Play.desktop;\n")

	status := Resolve(f.env, Stable)
	if status.Owner == nil || status.Owner.Scope != ScopeUser || status.Owner.Medium != MediumAppImage {
		t.Fatalf("owner = %+v, want the user-scope AppImage pin", status.Owner)
	}
	if status.Owner.Origin != "/home/g/Applications/Tipsy-0.0.0-dev.perf2-x86_64.AppImage" {
		t.Fatalf("owner origin = %q", status.Owner.Origin)
	}
	if len(status.Providers) != 2 || status.Providers[1].Medium != MediumFlatpak || status.Providers[1].Scope != ScopeSystem {
		t.Fatalf("providers = %+v", status.Providers)
	}
	if !status.HandledByIdentity || status.Handler != Stable.File(Play) {
		t.Fatalf("handler = %q (byIdentity=%v)", status.Handler, status.HandledByIdentity)
	}
	if got := Plan(status, MediumFlatpak, ""); got != ActionRelease {
		t.Fatalf("Flatpak plan = %s, want release", got)
	}
	if got := Plan(status, MediumAppImage, "/home/g/Applications/Tipsy-0.0.0-dev.perf2-x86_64.AppImage"); got != ActionNone {
		t.Fatalf("owning AppImage plan = %s, want none", got)
	}
	if got := Plan(status, MediumAppImage, "/home/g/Downloads/Tipsy-1.3.0-x86_64.AppImage"); got != ActionAdopt {
		t.Fatalf("other AppImage plan = %s, want adopt", got)
	}

	// The developer's fix: the Flatpak releases the shadow.
	rec := &recorder{}
	result, err := Release(context.Background(), f.env, Stable, rec.run)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || len(result.Kept) != 0 {
		t.Fatalf("release result = %+v", result)
	}
	after := Resolve(f.env, Stable)
	if after.Owner == nil || after.Owner.Medium != MediumFlatpak {
		t.Fatalf("after release owner = %+v, want the Flatpak", after.Owner)
	}
	if !after.HandledByIdentity {
		t.Fatal("the roblox:// handler registration must survive release; it names the ID, not the file")
	}
	if len(rec.calls) != 1 || !strings.HasPrefix(rec.calls[0], "update-desktop-database ") {
		t.Fatalf("helper calls = %q", rec.calls)
	}
}

func TestAdoptIfUnownedDefersToPackageInstall(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	writeFile(t, filepath.Join(f.usrShare, "applications", Stable.File(Play)), "[Desktop Entry]\nType=Application\nName=Tipsy - Play\nExec=tipsy-gui --play %u\n")
	rec := &recorder{}
	_, err := Adopt(context.Background(), f.env, AdoptOptions{
		Identity:  Stable,
		Launcher:  AppImageLauncher("/home/g/Tipsy-1.2.0-x86_64.AppImage"),
		IfUnowned: true,
		Handler:   true,
		Run:       rec.run,
	})
	if !errors.Is(err, ErrOwnedElsewhere) {
		t.Fatalf("err = %v, want ErrOwnedElsewhere", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.env.ApplicationsDir(), Stable.File(Play))); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("IfUnowned adopt must not write user-scope entries over a package install")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("no helpers may run when deferring, got %q", rec.calls)
	}

	// A development build never collides: its identity is separate.
	result, err := Adopt(context.Background(), f.env, AdoptOptions{
		Identity:  Development,
		Launcher:  AppImageLauncher("/home/g/Tipsy-0.0.0-dev-x86_64.AppImage"),
		IfUnowned: true,
		Run:       rec.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) != 2 || result.Handler {
		t.Fatalf("dev adopt result = %+v", result)
	}
	for _, call := range rec.calls {
		if strings.HasPrefix(call, "xdg-mime") {
			t.Fatalf("dev adopt without Handler must not touch xdg-mime: %q", rec.calls)
		}
	}
	if stable := Resolve(f.env, Stable); stable.Owner == nil || stable.Owner.Medium != MediumSystem {
		t.Fatalf("stable owner after dev adopt = %+v; dev must not disturb it", stable.Owner)
	}
}

func TestAdoptWritesEntriesIconsHandlerAndCleansDuplicates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	image := "/home/g/Applications/Tipsy-1.2.0-x86_64.AppImage"
	appsDir := f.env.ApplicationsDir()
	// Duplicates an AppImage manager left behind for the same file.
	writeFile(t, filepath.Join(appsDir, "appimage_tipsy_9f3.desktop"), "[Desktop Entry]\nType=Application\nName=Tipsy\nExec="+image+" --play %u\nIcon=appimage_tipsy_9f3\n")
	writeFile(t, filepath.Join(f.env.IconDir("128x128"), "appimage_tipsy_9f3.png"), "png")
	writeFile(t, filepath.Join(appsDir, "Tipsy Play (manager).desktop"), "[Desktop Entry]\nType=Application\nName=Tipsy\nExec=\""+image+"\"\n")
	// Something unrelated must survive.
	writeFile(t, filepath.Join(appsDir, "org.example.Other.desktop"), "[Desktop Entry]\nType=Application\nName=Other\nExec=other\n")
	iconSource := filepath.Join(t.TempDir(), "tipsy.png")
	writeFile(t, iconSource, "png-bytes")

	rec := &recorder{}
	result, err := Adopt(context.Background(), f.env, AdoptOptions{
		Identity:   Stable,
		Launcher:   AppImageLauncher(image),
		IconSource: iconSource,
		Handler:    true,
		Run:        rec.run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) != 2 || !result.Handler {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Removed) != 2 {
		t.Fatalf("removed = %q, want both manager duplicates", result.Removed)
	}
	if _, err := os.Stat(filepath.Join(appsDir, "org.example.Other.desktop")); err != nil {
		t.Fatal("unrelated entry was removed")
	}
	if _, err := os.Stat(filepath.Join(f.env.IconDir("128x128"), "appimage_tipsy_9f3.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manager icon was not removed")
	}
	play, err := os.ReadFile(filepath.Join(appsDir, Stable.File(Play)))
	if err != nil {
		t.Fatal(err)
	}
	iconPath := filepath.Join(f.env.IconDir("256x256"), "io.github.tipsy_linux.Tipsy.png")
	for _, want := range []string{
		"Exec=\"" + image + "\" --play %u\n",
		"Icon=" + iconPath + "\n",
		"X-Tipsy-Medium=appimage\n",
		"X-Tipsy-Channel=stable\n",
	} {
		if !strings.Contains(string(play), want) {
			t.Errorf("adopted Play entry missing %q:\n%s", want, play)
		}
	}
	for _, size := range iconSizes {
		if _, err := os.Stat(filepath.Join(f.env.IconDir(size), "io.github.tipsy_linux.Tipsy.png")); err != nil {
			t.Errorf("icon %s not installed: %v", size, err)
		}
	}
	wantCalls := []string{
		"update-desktop-database " + appsDir,
		"xdg-mime default io.github.tipsy_linux.Tipsy.Play.desktop x-scheme-handler/roblox-player",
		"xdg-mime default io.github.tipsy_linux.Tipsy.Play.desktop x-scheme-handler/roblox",
	}
	if strings.Join(rec.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("helper calls = %q, want %q", rec.calls, wantCalls)
	}

	status := Resolve(f.env, Stable)
	if status.Owner == nil || !status.Owner.Marked || status.Owner.Medium != MediumAppImage || status.Owner.Origin != image {
		t.Fatalf("owner after adopt = %+v", status.Owner)
	}
	if got := Plan(status, MediumAppImage, image); got != ActionNone {
		t.Fatalf("plan for the adopting AppImage = %s, want none", got)
	}

	// Adopting again (every AppImage start) is a no-op: nothing rewritten,
	// no helper processes.
	writeFile(t, filepath.Join(f.env.ConfigHome, "mimeapps.list"), "[Default Applications]\nx-scheme-handler/roblox=io.github.tipsy_linux.Tipsy.Play.desktop;\n")
	rec.calls = nil
	again, err := Adopt(context.Background(), f.env, AdoptOptions{Identity: Stable, Launcher: AppImageLauncher(image), IconSource: iconSource, Handler: true, Run: rec.run})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Written) != 0 || len(again.Removed) != 0 || len(rec.calls) != 0 {
		t.Fatalf("repeat adopt was not a no-op: written=%q removed=%q calls=%q", again.Written, again.Removed, rec.calls)
	}
}

func TestReleaseKeepsEntriesTipsyDidNotWrite(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	handWritten := filepath.Join(f.env.ApplicationsDir(), Stable.File(Play))
	writeFile(t, handWritten, "[Desktop Entry]\nType=Application\nName=Tipsy - Play\nExec=tipsy-gui --play %u\n")
	result, err := Release(context.Background(), f.env, Stable, (&recorder{}).run)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 || len(result.Kept) != 1 || result.Kept[0] != handWritten {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(handWritten); err != nil {
		t.Fatal("hand-written entry must be left alone")
	}
}

func TestPlanForPackageInstallsWithoutShadow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	writeFile(t, filepath.Join(f.usrShare, "applications", Stable.File(Play)), "[Desktop Entry]\nType=Application\nName=Tipsy - Play\nExec=tipsy-gui --play %u\n")
	status := Resolve(f.env, Stable)
	if got := Plan(status, MediumSystem, "/usr/bin/tipsy-gui"); got != ActionNone {
		t.Fatalf("system plan = %s", got)
	}
	if got := Plan(status, MediumFlatpak, ""); got != ActionNone {
		t.Fatalf("flatpak plan = %s", got)
	}
	if got := Plan(status, MediumAppImage, "/x/Tipsy.AppImage"); got != ActionAdopt {
		t.Fatalf("appimage plan = %s (adopt is offered, but only as an explicit takeover)", got)
	}
	empty := Resolve(f.env, Development)
	if empty.Owned() {
		t.Fatal("dev identity must be unowned in a stable-only fixture")
	}
	if got := Plan(empty, MediumSource, "/home/g/go/bin/tipsy-gui"); got != ActionAdopt {
		t.Fatalf("source plan = %s", got)
	}
}

// A terminal opened from an AppImage editor exports that editor's APPIMAGE;
// a source-built tipsy running there is not an AppImage.
func TestCurrentMediumIgnoresInheritedAppImageVariables(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("APPIMAGE", "/home/x/Applications/Editor.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_EditorXYZ")
	medium, origin := CurrentMedium()
	if medium == MediumAppImage {
		t.Fatalf("medium = %s (%s); the test binary is not inside %s", medium, origin, os.Getenv("APPDIR"))
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// When APPDIR really contains the executable, the AppImage is trusted.
	t.Setenv("APPDIR", filepath.Dir(filepath.Dir(exe)))
	medium, origin = CurrentMedium()
	if medium != MediumAppImage || origin != "/home/x/Applications/Editor.AppImage" {
		t.Fatalf("medium = %s (%s), want appimage with the APPIMAGE path", medium, origin)
	}
}

func TestEnvFromOSHonoursXDGVariables(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("HOME", "/home/x")
	t.Setenv("XDG_DATA_HOME", "/data/home")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	t.Setenv("XDG_DATA_DIRS", "/a/share:/b/share/:")
	env := EnvFromOS()
	if env.DataHome != "/data/home" || env.ConfigHome != "/cfg" || env.Home != "/home/x" {
		t.Fatalf("env = %+v", env)
	}
	if strings.Join(env.DataDirs, ",") != "/a/share,/b/share" {
		t.Fatalf("DataDirs = %q", env.DataDirs)
	}
	t.Setenv("XDG_DATA_DIRS", "")
	if env := EnvFromOS(); strings.Join(env.DataDirs, ",") != strings.Join(DefaultDataDirs, ",") {
		t.Fatalf("default DataDirs = %q", env.DataDirs)
	}
}
