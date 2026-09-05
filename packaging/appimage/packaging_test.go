// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package appimage_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate packaging test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestAppRunUsesOnlyItsAppDir(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "appimage", "AppRun"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"$appdir/usr/bin/tipsy-gui",
		"$appdir/usr/bin/tipsy",
		"$appdir/usr/lib",
		"$appdir/usr/plugins",
		"--play",
		"--settings",
		"integrate_pin_entries",
		"remove_foreign_launchers",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("AppRun is missing %q", required)
		}
	}
	for _, forbidden := range []string{".tipsy-private", "libroblox.so", "/home/", "ensure_nodisplay", "hide_foreign_play_pins"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("AppRun contains forbidden release coupling %q", forbidden)
		}
	}
}

func TestQtConfigurationIsRelative(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "appimage", "qt.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"Prefix=..", "Plugins=plugins", "Libraries=lib"} {
		if !strings.Contains(text, required) {
			t.Errorf("qt.conf is missing %q", required)
		}
	}
	if strings.Contains(text, "/usr") || strings.Contains(text, "/home") {
		t.Fatal("qt.conf contains an absolute host path")
	}
}

func TestDesktopEntriesAreDistinctPinTargets(t *testing.T) {
	repo := repoRoot(t)
	play, err := os.ReadFile(filepath.Join(repo, "share", "applications", "io.github.tipsy_linux.Tipsy.Play.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(repo, "share", "applications", "io.github.tipsy_linux.Tipsy.Settings.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	playText := string(play)
	settingsText := string(settings)
	for _, required := range []string{"Name=Tipsy - Play", "Exec=tipsy launch %u", "StartupWMClass=roblox", "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;", "X-AppImage-Integrate=false"} {
		if !strings.Contains(playText, required+"\n") {
			t.Errorf("Play desktop entry missing %q", required)
		}
	}
	for _, required := range []string{"Name=Tipsy - Settings", "Exec=tipsy-gui %u", "StartupWMClass=tipsy-gui", "Categories=Game;", "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;", "X-AppImage-Integrate=false"} {
		if !strings.Contains(settingsText, required+"\n") {
			t.Errorf("Settings desktop entry missing %q", required)
		}
	}
	if strings.Contains(playText, "StartupWMClass=tipsy-gui") {
		t.Fatal("Play desktop entry must not share the Settings window class")
	}
	if strings.Contains(settingsText, "StartupWMClass=roblox") {
		t.Fatal("Settings desktop entry must not share the Play window class")
	}
}

func TestAppDirBuilderPinsAppImageVersionBeforeManifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "build-appdir.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"inject_appimage_version",
		`print "X-AppImage-Version=" ver`,
		`inject_appimage_version "$appdir/io.github.tipsy_linux.Tipsy.Play.desktop"`,
		`manifest="$appdir/usr/share/tipsy/manifest.sha256"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("build-appdir.sh is missing %q", required)
		}
	}
	injectAt := strings.Index(text, `inject_appimage_version "$appdir/io.github.tipsy_linux.Tipsy.Play.desktop"`)
	manifestAt := strings.Index(text, `manifest="$appdir/usr/share/tipsy/manifest.sha256"`)
	if injectAt < 0 || manifestAt < 0 || injectAt > manifestAt {
		t.Fatal("X-AppImage-Version must be written before the payload manifest")
	}
}

func TestAppDirBuilderRequiresFocusedTextNativeStack(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "build-appdir.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"required_pkg_modules=(Qt6Widgets Qt6Gui Qt6Core x11 xext pangocairo pangoft2 cairo-xlib)",
		`pkg-config --exists "${required_pkg_modules[@]}"`,
		`queue+=("$library")`,
		`copy_package_license "$library"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("build-appdir.sh is missing %q", required)
		}
	}
	for _, hostLibrary := range []string{
		"libpangocairo-1.0.so.*",
		"libpango-1.0.so.*",
		"libpangoft2-1.0.so.*",
		"libcairo.so.*",
		"libglib-2.0.so.*",
		"libgobject-2.0.so.*",
		"libfontconfig.so.*",
		"libfreetype.so.*",
	} {
		if strings.Contains(text, hostLibrary) {
			t.Errorf("focused-text runtime dependency must not be excluded as host-provided: %s", hostLibrary)
		}
	}
}

func TestAppImageBuilderRejectsUnpinnedTool(t *testing.T) {
	repo := repoRoot(t)
	appdir := t.TempDir()
	tool := filepath.Join(t.TempDir(), "appimagetool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(repo, "scripts", "build-appimage.sh"),
		"--appdir", appdir,
		"--version", "test",
		"--tool", tool,
		"--tool-sha256", strings.Repeat("0", 64),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("builder accepted an incorrect tool digest: %s", output)
	}
	if !strings.Contains(string(output), "SHA-256 mismatch") {
		t.Fatalf("unexpected error: %s", output)
	}
}

func TestAppImageBuilderRejectsUnpinnedRuntime(t *testing.T) {
	repo := repoRoot(t)
	appdir := t.TempDir()
	tool := filepath.Join(t.TempDir(), "appimagetool")
	runtime := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime, []byte("runtime-fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	toolHash := sha256File(t, tool)
	command := exec.Command(filepath.Join(repo, "scripts", "build-appimage.sh"),
		"--appdir", appdir,
		"--version", "test",
		"--tool", tool,
		"--tool-sha256", toolHash,
		"--runtime-file", runtime,
		"--runtime-sha256", strings.Repeat("0", 64),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("builder accepted an incorrect runtime digest: %s", output)
	}
	if !strings.Contains(string(output), "runtime SHA-256 mismatch") {
		t.Fatalf("unexpected error: %s", output)
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	sum, err := exec.Command("sha256sum", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(sum))[0]
}

func TestReleaseGuardNamesForbiddenPayloads(t *testing.T) {
	repo := repoRoot(t)
	for _, test := range []struct {
		name     string
		relative string
	}{
		{name: "Roblox library", relative: "usr/lib/libroblox.so"},
		{name: "private docs", relative: ".tipsy-private/docs/note.txt"},
		{name: "account data", relative: "usr/share/app-data/session.bin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			appdir := minimalAppDir(t)
			path := filepath.Join(appdir, filepath.FromSlash(test.relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("forbidden fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(filepath.Join(repo, "scripts", "check-release-tree.sh"), appdir)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("guard accepted %s", test.relative)
			}
			if !strings.Contains(string(output), "forbidden") && !strings.Contains(string(output), "unexpected top-level") {
				t.Fatalf("guard did not name forbidden content: %s", output)
			}
		})
	}
}

func TestAppRunDispatchesPlayAndSettings(t *testing.T) {
	appdir := fakeRunnableAppDir(t)

	t.Run("settings", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings", "--no-integrate")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, "")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("settings: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy-gui")
	})

	t.Run("play without runtime opens settings", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--play", "--no-integrate")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, "")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("play fallback: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy-gui")
	})

	t.Run("play with runtime launches client", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "tipsy", "runtime", "lib", "x86_64"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--play", "--no-integrate")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, xdg)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("play: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy launch")
	})

	t.Run("rewritten launch argument plays", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "tipsy", "runtime", "lib", "x86_64"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(filepath.Join(appdir, "AppRun"), "launch", "--no-integrate")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, xdg)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("launch: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy launch")
	})

	t.Run("website uri launches client", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "tipsy", "runtime", "lib", "x86_64"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--no-integrate", "roblox://experiences/start?placeId=1818")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, xdg)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("uri: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy launch roblox://experiences/start?placeId=1818")
	})

	t.Run("settings plus website uri still launches client", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "tipsy", "runtime", "lib", "x86_64"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings", "--no-integrate", "roblox://experiences/start?placeId=1818")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, xdg)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("settings uri: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy launch roblox://experiences/start?placeId=1818")
	})

	t.Run("settings invocation name", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--no-integrate")
		command.Dir = appdir
		command.Env = append(stubEnv(t, logPath, ""), "ARGV0=Tipsy-Settings.AppImage")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("argv0 settings: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy-gui")
	})
}

func TestAppRunWritesPinEntries(t *testing.T) {
	appdir := fakeRunnableAppDir(t)
	home := t.TempDir()
	xdg := filepath.Join(home, ".local", "share")
	appImage := filepath.Join(home, "Tipsy-0.0.0-dev-x86_64.AppImage")
	logPath := filepath.Join(t.TempDir(), "stub.log")
	command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings")
	command.Dir = appdir
	command.Env = append(stubEnv(t, logPath, xdg),
		"HOME="+home,
		"APPIMAGE="+appImage,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("integrate: %v\n%s", err, output)
	}

	playPath := filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Play.desktop")
	settingsPath := filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Settings.desktop")
	play, err := os.ReadFile(playPath)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	playText := string(play)
	settingsText := string(settings)
	quoted := `Exec="` + appImage + `" --play %u`
	if !strings.Contains(playText, quoted+"\n") {
		t.Fatalf("Play pin Exec=%q, want %q", playText, quoted)
	}
	if !strings.Contains(playText, "StartupWMClass=roblox\n") || !strings.Contains(playText, "Name=Tipsy - Play\n") {
		t.Fatalf("Play pin entry is missing identity fields: %s", playText)
	}
	if !strings.Contains(playText, "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n") {
		t.Fatalf("Play pin entry is missing Roblox URI handlers: %s", playText)
	}
	if !strings.Contains(settingsText, `Exec="`+appImage+`" --settings %u`+"\n") {
		t.Fatalf("Settings pin Exec is wrong: %s", settingsText)
	}
	if !strings.Contains(settingsText, "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n") {
		t.Fatalf("Settings pin is missing Roblox URI handlers: %s", settingsText)
	}
	if !strings.Contains(playText, "[Desktop Action Settings]\n") {
		t.Fatalf("Play pin is missing a Settings action: %s", playText)
	}
	if !strings.Contains(playText, `Exec="`+appImage+`" --settings %u`+"\n") {
		t.Fatalf("Play Settings action Exec is wrong: %s", playText)
	}
	if strings.Count(playText, `Exec="`+appImage+`" --play %u`+"\n") != 1 {
		t.Fatalf("Play pin should keep one Play Exec: %s", playText)
	}
	if strings.Contains(playText, "NoDisplay=true") {
		t.Fatalf("solo Play pin should stay visible: %s", playText)
	}
	if !strings.Contains(playText, "X-AppImage-Integrate=false\n") {
		t.Fatalf("Play pin dropped X-AppImage-Integrate=false: %s", playText)
	}
	if !strings.Contains(settingsText, "X-AppImage-Integrate=false\n") {
		t.Fatalf("Settings pin dropped X-AppImage-Integrate=false: %s", settingsText)
	}
	if !strings.Contains(settingsText, "StartupWMClass=tipsy-gui\n") || !strings.Contains(settingsText, "Name=Tipsy - Settings\n") {
		t.Fatalf("Settings pin entry is missing identity fields: %s", settingsText)
	}
	icon := filepath.Join(xdg, "icons", "hicolor", "256x256", "apps", "io.github.tipsy_linux.Tipsy.png")
	if _, err := os.Stat(icon); err != nil {
		t.Fatalf("pin icon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "icons", "hicolor", "512x512", "apps", "io.github.tipsy_linux.Tipsy.png")); err != nil {
		t.Fatalf("512 pin icon: %v", err)
	}
	if !strings.Contains(playText, "Icon="+icon+"\n") {
		t.Fatalf("Play pin Icon is not the installed PNG: %s", playText)
	}
	if !strings.Contains(settingsText, "Icon="+icon+"\n") {
		t.Fatalf("Settings pin Icon is not the installed PNG: %s", settingsText)
	}
}

func TestAppRunKeepsPlayVisibleForProtocolHandlers(t *testing.T) {
	appdir := fakeRunnableAppDir(t)
	home := t.TempDir()
	xdg := filepath.Join(home, ".local", "share")
	apps := filepath.Join(xdg, "applications")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	appImage := filepath.Join(home, "Applications", "Tipsy-0.0.0-test-x86_64.AppImage")
	manager := "[Desktop Entry]\nName=Tipsy - Play\nComment=Managed by AppImage Manager\nExec=" + appImage + " launch %u\nIcon=appimage_tipsy_fake\n"
	if err := os.WriteFile(filepath.Join(apps, "appimage_tipsy_fake.desktop"), []byte(manager), 0o644); err != nil {
		t.Fatal(err)
	}
	other := "[Desktop Entry]\nName=Other\nExec=" + filepath.Join(home, "Applications", "Other.AppImage") + "\n"
	if err := os.WriteFile(filepath.Join(apps, "appimage_other.desktop"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	iconDir := filepath.Join(xdg, "icons", "hicolor", "256x256", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconDir, "appimage_tipsy_fake.png"), []byte("manager-icon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "stub.log")
	command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings")
	command.Dir = appdir
	command.Env = append(stubEnv(t, logPath, xdg),
		"HOME="+home,
		"APPIMAGE="+appImage,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("integrate: %v\n%s", err, output)
	}
	play, err := os.ReadFile(filepath.Join(apps, "io.github.tipsy_linux.Tipsy.Play.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(apps, "io.github.tipsy_linux.Tipsy.Settings.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(play), "NoDisplay=true") {
		t.Fatalf("Play pin must stay visible so protocol choosers can offer it: %s", play)
	}
	if strings.Contains(string(settings), "NoDisplay=true") {
		t.Fatalf("Settings pin was hidden: %s", settings)
	}
	if _, err := os.Stat(filepath.Join(apps, "appimage_tipsy_fake.desktop")); !os.IsNotExist(err) {
		t.Fatalf("AppImage Manager Play duplicate should be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(iconDir, "appimage_tipsy_fake.png")); !os.IsNotExist(err) {
		t.Fatalf("AppImage Manager icon should be deleted: %v", err)
	}
	otherText, err := os.ReadFile(filepath.Join(apps, "appimage_other.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if gotOther := string(otherText); gotOther != other {
		t.Fatalf("unrelated AppImage launcher was changed: %s", gotOther)
	}
	if !strings.Contains(string(play), "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n") {
		t.Fatalf("Play pin lost URI handlers: %s", play)
	}
	if !strings.Contains(string(settings), "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n") {
		t.Fatalf("Settings pin lost URI handlers: %s", settings)
	}
	icon := filepath.Join(xdg, "icons", "hicolor", "256x256", "apps", "io.github.tipsy_linux.Tipsy.png")
	if !strings.Contains(string(settings), "Icon="+icon+"\n") {
		t.Fatalf("Settings pin Icon=%s", settings)
	}
}

func TestAppRunRemovesLeftoverTipsyManagerPins(t *testing.T) {
	appdir := fakeRunnableAppDir(t)
	home := t.TempDir()
	xdg := filepath.Join(home, ".local", "share")
	apps := filepath.Join(xdg, "applications")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(home, "Applications", "Tipsy-0.0.0-test10-x86_64.AppImage")
	previous := filepath.Join(home, "Applications", "Tipsy-0.0.0-test8-x86_64.AppImage")
	leftover := "[Desktop Entry]\nName=Tipsy - Play\nComment=Managed by AppImage Manager\nExec=" + previous + " launch %u\nIcon=appimage_tipsy_old\n"
	if err := os.WriteFile(filepath.Join(apps, "appimage_tipsy_old.desktop"), []byte(leftover), 0o644); err != nil {
		t.Fatal(err)
	}
	other := "[Desktop Entry]\nName=Other\nComment=Managed by AppImage Manager\nExec=" + filepath.Join(home, "Applications", "Other.AppImage") + "\nIcon=appimage_other\n"
	if err := os.WriteFile(filepath.Join(apps, "appimage_other.desktop"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	iconDir := filepath.Join(xdg, "icons", "hicolor", "256x256", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconDir, "appimage_tipsy_old.png"), []byte("old-icon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iconDir, "appimage_other.png"), []byte("other-icon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "stub.log")
	command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings")
	command.Dir = appdir
	command.Env = append(stubEnv(t, logPath, xdg),
		"HOME="+home,
		"APPIMAGE="+current,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("integrate: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(apps, "appimage_tipsy_old.desktop")); !os.IsNotExist(err) {
		t.Fatalf("leftover Tipsy Manager pin should be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(iconDir, "appimage_tipsy_old.png")); !os.IsNotExist(err) {
		t.Fatalf("leftover Tipsy Manager icon should be deleted: %v", err)
	}
	gotOther, err := os.ReadFile(filepath.Join(apps, "appimage_other.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOther) != other {
		t.Fatalf("non-Tipsy AppImage launcher was changed: %s", gotOther)
	}
	if _, err := os.Stat(filepath.Join(iconDir, "appimage_other.png")); err != nil {
		t.Fatalf("non-Tipsy AppImage icon was deleted: %v", err)
	}
	brand := filepath.Join(iconDir, "io.github.tipsy_linux.Tipsy.png")
	if _, err := os.Stat(brand); err != nil {
		t.Fatalf("brand icon: %v", err)
	}
	play, err := os.ReadFile(filepath.Join(apps, "io.github.tipsy_linux.Tipsy.Play.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(play), "NoDisplay=true") {
		t.Fatalf("Play pin must stay visible: %s", play)
	}
	if !strings.Contains(string(play), `Exec="`+current+`" --play %u`+"\n") {
		t.Fatalf("Play pin Exec is not the current AppImage: %s", play)
	}
}

func TestAppRunNoIntegrateSkipsPinEntries(t *testing.T) {
	appdir := fakeRunnableAppDir(t)
	home := t.TempDir()
	xdg := filepath.Join(home, ".local", "share")
	logPath := filepath.Join(t.TempDir(), "stub.log")
	command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings", "--no-integrate")
	command.Dir = appdir
	command.Env = append(stubEnv(t, logPath, xdg),
		"HOME="+home,
		"APPIMAGE="+filepath.Join(home, "Tipsy.AppImage"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("no-integrate: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Play.desktop")); !os.IsNotExist(err) {
		t.Fatalf("wrote Play pin entry despite --no-integrate: %v", err)
	}
}

func stubEnv(t *testing.T, logPath, xdgDataHome string) []string {
	t.Helper()
	env := []string{
		"PATH=/usr/bin:/bin",
		"TIPSY_STUB_LOG=" + logPath,
		"HOME=" + t.TempDir(),
		"XDG_CONFIG_HOME=" + t.TempDir(),
	}
	if xdgDataHome != "" {
		env = append(env, "XDG_DATA_HOME="+xdgDataHome)
	} else {
		env = append(env, "XDG_DATA_HOME="+t.TempDir())
	}
	return env
}

func assertStubLog(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(data))
	if got != want {
		t.Fatalf("stub log=%q, want %q", got, want)
	}
}

func fakeRunnableAppDir(t *testing.T) string {
	t.Helper()
	repo := repoRoot(t)
	root := t.TempDir()
	for _, dir := range []string{"usr/bin", "usr/lib", "usr/plugins", "usr/share/applications"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	appRun, err := os.ReadFile(filepath.Join(repo, "packaging", "appimage", "AppRun"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AppRun"), appRun, 0o755); err != nil {
		t.Fatal(err)
	}
	icon := []byte("png-fixture\n")
	if err := os.WriteFile(filepath.Join(root, "tipsy.png"), icon, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"io.github.tipsy_linux.Tipsy.Play.desktop",
		"io.github.tipsy_linux.Tipsy.Settings.desktop",
	} {
		data, err := os.ReadFile(filepath.Join(repo, "share", "applications", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "usr/share/applications", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stub := "#!/bin/sh\nprintf '%s %s\\n' \"$(basename -- \"$0\")\" \"$*\" > \"$TIPSY_STUB_LOG\"\n"
	if err := os.WriteFile(filepath.Join(root, "usr/bin/tipsy"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/bin/tipsy-gui"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func minimalAppDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"AppRun",
		".DirIcon",
		"io.github.tipsy_linux.Tipsy.Play.desktop",
		"tipsy.png",
		"usr/bin/tipsy",
		"usr/bin/tipsy-gui",
		"usr/bin/qt.conf",
		"usr/plugins/platforms/libqoffscreen.so",
		"usr/plugins/platforms/libqxcb.so",
		"usr/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop",
		"usr/share/applications/io.github.tipsy_linux.Tipsy.Settings.desktop",
		"usr/share/icons/hicolor/512x512/apps/tipsy.png",
		"usr/share/licenses/tipsy/LICENSE",
		"usr/share/licenses/tipsy/NOTICE",
		"usr/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml",
		"usr/share/tipsy/build-info",
		"usr/share/tipsy/manifest.sha256",
	}
	for _, relative := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if relative == "AppRun" || relative == "usr/bin/tipsy" || relative == "usr/bin/tipsy-gui" {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte("packaging fixture\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
