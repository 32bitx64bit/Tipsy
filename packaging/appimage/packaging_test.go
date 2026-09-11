// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package appimage_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
		"TIPSY_RELEASE_ARTIFACT",
		"TIPSY_RELEASE_APPDIR",
		"--play",
		"--settings",
		"integrate_pin_entries",
		`"$appdir/usr/bin/tipsy" desktop adopt --if-unowned`,
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
	for _, required := range []string{"Name=Tipsy - Play", "Exec=tipsy-gui --play %u", "StartupWMClass=roblox", "MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;", "X-AppImage-Integrate=false"} {
		if !strings.Contains(playText, required+"\n") {
			t.Errorf("Play desktop entry missing %q", required)
		}
	}
	for _, required := range []string{"Name=Tipsy - Settings", "Exec=tipsy-gui %u", "StartupWMClass=tipsy-gui", "Categories=Game;", "X-AppImage-Integrate=false"} {
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
	if strings.Contains(settingsText, "MimeType=x-scheme-handler/") {
		t.Fatal("Settings desktop entry must not compete with Play for Roblox URI handling")
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
		"required_pkg_modules=(Qt6Widgets Qt6Gui Qt6Core x11 xext pangocairo pangoft2 cairo-xlib libpulse libpulse-simple)",
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

func TestReleaseGuardAcceptsCanonicalMinimalTree(t *testing.T) {
	appdir := minimalAppDir(t)
	command := exec.Command(filepath.Join(repoRoot(t), "scripts", "check-release-tree.sh"), appdir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("guard rejected canonical fixture: %v\n%s", err, output)
	}
}

func TestReleaseGuardRejectsUnsafeFilesystemAndContent(t *testing.T) {
	repo := repoRoot(t)
	guard := filepath.Join(repo, "scripts", "check-release-tree.sh")
	tests := []struct {
		name    string
		want    string
		mutate  func(*testing.T, string)
		prepare bool
	}{
		{
			name: "symbolic link",
			want: "symbolic links",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				mustSymlink(t, "build-info", filepath.Join(root, "usr/share/tipsy/linked"))
			},
		},
		{
			name: "hard link",
			want: "hard-linked",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Link(filepath.Join(root, "usr/share/tipsy/build-info"), filepath.Join(root, "usr/share/tipsy/linked")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			want: "special filesystem",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if output, err := exec.Command("mkfifo", filepath.Join(root, "usr/share/tipsy/pipe")).CombinedOutput(); err != nil {
					t.Fatalf("mkfifo: %v: %s", err, output)
				}
			},
		},
		{
			name: "group writable",
			want: "group/world-writable",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				mustChmod(t, filepath.Join(root, "usr/share/tipsy/build-info"), 0o664)
			},
		},
		{
			name: "setuid",
			want: "setuid/setgid",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if output, err := exec.Command("chmod", "4755", filepath.Join(root, "AppRun")).CombinedOutput(); err != nil {
					t.Fatalf("chmod: %v: %s", err, output)
				}
			},
		},
		{
			name: "unexpected executable",
			want: "unexpected executable",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				mustWrite(t, filepath.Join(root, "usr/share/tipsy/run-me"), []byte("#!/bin/sh\n"), 0o755)
			},
		},
		{
			name: "unsafe path spelling",
			want: "unsafe release path spelling",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				mustWrite(t, filepath.Join(root, "usr/share/tipsy/bad name"), []byte("data\n"), 0o644)
			},
		},
		{
			name: "private key marker",
			want: "credential",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				mustWrite(t, filepath.Join(root, "usr/share/tipsy/metadata"), []byte("-----BEGIN PRIVATE KEY-----\nfixture\n"), 0o644)
			},
		},
		{
			name: "renamed zip",
			want: "ZIP/APK-like",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				path := filepath.Join(root, "usr/share/tipsy/payload.bin")
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				archive := zip.NewWriter(file)
				part, err := archive.Create("lib/x86_64/libroblox.so")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write([]byte("proprietary-fixture")); err != nil {
					t.Fatal(err)
				}
				if err := archive.Close(); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsafe rpath",
			want: "RPATH/RUNPATH",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				data, err := os.ReadFile("/bin/true")
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "usr/bin/tipsy")
				mustWrite(t, path, data, 0o755)
				if output, err := exec.Command("patchelf", "--set-rpath", "/opt/host", path).CombinedOutput(); err != nil {
					t.Fatalf("patchelf: %v: %s", err, output)
				}
			},
		},
		{
			name:    "unmanifested file",
			want:    "manifest covers",
			prepare: true,
			mutate: func(t *testing.T, root string) {
				t.Helper()
				mustWrite(t, filepath.Join(root, "usr/share/tipsy/unmanifested"), []byte("data\n"), 0o644)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appdir := minimalAppDir(t)
			if test.prepare {
				refreshManifest(t, appdir)
			}
			test.mutate(t, appdir)
			command := exec.Command(guard, appdir)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("guard accepted unsafe fixture\n%s", output)
			}
			if !strings.Contains(string(output), test.want) {
				t.Fatalf("guard output %q does not contain %q", output, test.want)
			}
		})
	}
}

func TestReleaseGuardRejectsFileCapabilities(t *testing.T) {
	repo := repoRoot(t)
	appdir := minimalAppDir(t)
	tools := t.TempDir()
	stub := "#!/bin/sh\ncase \"$*\" in *usr/bin/tipsy) printf '%s cap_net_bind_service=ep\\n' \"$3\";; esac\n"
	mustWrite(t, filepath.Join(tools, "getcap"), []byte(stub), 0o755)
	command := exec.Command(filepath.Join(repo, "scripts", "check-release-tree.sh"), appdir)
	command.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "file capabilities") {
		t.Fatalf("guard did not reject reported file capability: %v\n%s", err, output)
	}
}

func TestReleaseInputLockSeparatesGitHubSignedAndOfficial(t *testing.T) {
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "release-lock.py")
	lock := filepath.Join(repo, "scripts", "release-inputs.lock.json")
	if output, err := exec.Command(script, "--lock", lock, "--mode", "developer").CombinedOutput(); err != nil {
		t.Fatalf("developer lock validation: %v\n%s", err, output)
	}
	if output, err := exec.Command(script, "--lock", lock, "--mode", "github-signed").CombinedOutput(); err != nil {
		t.Fatalf("GitHub-signed lock validation: %v\n%s", err, output)
	}
	output, err := exec.Command(script, "--lock", lock, "--mode", "official").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "pinned builder image digest") {
		t.Fatalf("GitHub-signed lock did not block the stricter official build: %v\n%s", err, output)
	}
	digest, err := exec.Command(script, "--lock", lock, "--mode", "developer", "--digest").Output()
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{64}\n$`).Match(digest) {
		t.Fatalf("lock digest is not canonical SHA-256: %v %q", err, digest)
	}

	raw, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	var mutated map[string]any
	if err := json.Unmarshal(raw, &mutated); err != nil {
		t.Fatal(err)
	}
	mutated["builder"].(map[string]any)["unexpected"] = true
	canonical, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	badLock := filepath.Join(t.TempDir(), "release-inputs.lock.json")
	mustWrite(t, badLock, append(canonical, '\n'), 0o644)
	output, err = exec.Command(script, "--lock", badLock, "--mode", "developer").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "unknown fields") {
		t.Fatalf("nested unknown lock field was accepted: %v\n%s", err, output)
	}
}

func TestReleaseEvidenceIsDeterministicAndPrivatePathFree(t *testing.T) {
	repo := repoRoot(t)
	appdir := minimalAppDir(t)
	artifact := filepath.Join(t.TempDir(), "Tipsy-test-x86_64.AppDir.tar.gz")
	mustWrite(t, artifact, []byte("deterministic-artifact\n"), 0o644)
	lock := filepath.Join(repo, "scripts", "release-inputs.lock.json")
	outputs := []string{"artifact-manifest.json", "build-materials.json", "provenance-input.json", "release-hashes.sha256", "tipsy.spdx.json"}
	var baseline map[string][]byte
	for pass := 0; pass < 2; pass++ {
		out := filepath.Join(t.TempDir(), "evidence")
		command := exec.Command(filepath.Join(repo, "scripts", "release-evidence.py"),
			"evidence",
			"--version", "0.0.0-test",
			"--source-commit", strings.Repeat("a", 40),
			"--source-date-epoch", "1700000000",
			"--release-lock", lock,
			"--mode", "developer",
			"--appdir", appdir,
			"--artifact", artifact,
			"--output-dir", out,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("evidence pass %d: %v\n%s", pass, err, output)
		}
		current := make(map[string][]byte)
		for _, name := range outputs {
			data, err := os.ReadFile(filepath.Join(out, name))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("/home/")) || bytes.Contains(data, []byte(".tipsy-private")) {
				t.Fatalf("%s leaked a private/local path: %s", name, data)
			}
			current[name] = data
		}
		if pass == 0 {
			baseline = current
			continue
		}
		for _, name := range outputs {
			if !bytes.Equal(baseline[name], current[name]) {
				t.Fatalf("%s is not deterministic", name)
			}
		}
	}
	var document map[string]any
	if err := json.Unmarshal(baseline["tipsy.spdx.json"], &document); err != nil {
		t.Fatal(err)
	}
	if document["spdxVersion"] != "SPDX-2.3" {
		t.Fatalf("unexpected SPDX document: %v", document["spdxVersion"])
	}
}

func TestBuildInfoMarksDeveloperArtifactUnrestricted(t *testing.T) {
	repo := repoRoot(t)
	output := filepath.Join(t.TempDir(), "build-info.json")
	command := exec.Command(filepath.Join(repo, "scripts", "release-evidence.py"),
		"build-info",
		"--version", "test",
		"--source-commit", strings.Repeat("b", 40),
		"--source-date-epoch", "1700000000",
		"--release-lock", filepath.Join(repo, "scripts", "release-inputs.lock.json"),
		"--mode", "developer",
		"--source-dirty",
		"--output", output,
	)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build info: %v\n%s", err, data)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"releaseKind":"development-unrestricted"`)) || !bytes.Contains(data, []byte(`"tree":"dirty"`)) {
		t.Fatalf("developer label is not explicit: %s", data)
	}
}

func TestWorkflowDependenciesAreImmutableAndLeastPrivilege(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	uses := regexp.MustCompile(`(?m)^\s*- uses: [^@\s]+@([^\s]+)`).FindAllStringSubmatch(text, -1)
	if len(uses) == 0 {
		t.Fatal("workflow has no action dependencies")
	}
	for _, match := range uses {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(match[1]) {
			t.Errorf("workflow action is not pinned to a full SHA: %s", match[0])
		}
	}
	for _, forbidden := range []string{"ubuntu-latest", "continue-on-error: true", "permissions: write-all", `go-version: "`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("workflow retains unsafe/mutable setting %q", forbidden)
		}
	}
	for _, required := range []string{
		"permissions:\n  contents: read",
		"persist-credentials: false",
		`go-version-file: go.mod`,
		"scripts/ci-install-native.sh",
		"scripts/ci-test-build.sh",
		"ubuntu-22.04",
		"ubuntu-24.04",
		"debian:bookworm-slim",
		"fedora:43",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("workflow is missing %q", required)
		}
	}
	install, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "ci-install-native.sh"))
	if err != nil {
		t.Fatal(err)
	}
	installText := string(install)
	for _, required := range []string{"libpulse-dev", "libxi-dev", "xvfb", "pulseaudio-libs-devel", "libXi-devel", "pkg-config --exists", "qmake6", "Qt6Widgets.pc"} {
		if !strings.Contains(installText, required) {
			t.Errorf("ci-install-native.sh is missing %q", required)
		}
	}
	testBuild, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "ci-test-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	testText := string(testBuild)
	for _, required := range []string{"go vet -unsafeptr=false ./...", "go test", "-race", "go build -o bin/tipsy ./cmd/tipsy", "go build -o bin/tipsy-gui ./cmd/tipsy-gui", "Xvfb", "/tmp/tipsy-ci-pkgconfig"} {
		if !strings.Contains(testText, required) {
			t.Errorf("ci-test-build.sh is missing %q", required)
		}
	}
}

func TestReleaseWorkflowSecurityIfPresent(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "release.yml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "push:") || !strings.Contains(text, "workflow_dispatch:") || !strings.Contains(text, "version:") || !strings.Contains(text, "tags:") || !strings.Contains(text, "v*") {
		t.Fatal("release workflow must support automatic tags and manual versioned releases")
	}
	for _, forbidden := range []string{"pull_request:", "pull_request_target:", "workflow_run:", "secrets.", "permissions: write-all", "persist-credentials: true", "ubuntu-latest"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("release workflow retains unsafe setting %q", forbidden)
		}
	}
	uses := regexp.MustCompile(`(?m)^\s*uses:\s+[^@\s]+@([^\s]+)`).FindAllStringSubmatch(text, -1)
	if len(uses) == 0 {
		t.Fatal("release workflow has no pinned action dependencies")
	}
	for _, match := range uses {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(match[1]) {
			t.Errorf("release workflow action is not pinned to a full SHA: %s", match[0])
		}
	}
	for _, required := range []string{
		"permissions:\n  contents: read",
		"  build:\n",
		"  publish:\n",
		"needs: build",
		"contents: write",
		"id-token: write",
		"attestations: write",
		"environment: production",
		"github.event_name == 'workflow_dispatch'",
		"refs/heads/main",
		"MANUAL_VERSION",
		"--target '${{ steps.preflight.outputs.commit }}'",
		"persist-credentials: false",
		"scripts/release-build.sh",
		"--mode github-signed",
		"release-candidate-keyless",
		"libcap2-bin",
		"libpulse-dev",
		"libxi-dev",
		"libpulse-simple",
		"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02",
		"actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093",
		"sigstore/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6",
		"cosign sign-blob --yes --bundle",
		"cosign verify-blob",
		"--certificate-oidc-issuer",
		"gh release create",
		"GH_TOKEN: ${{ github.token }}",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{"offline TUF signing ceremony", "release-candidate-unsigned", "Upload release-candidate material only"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("release workflow retains the wrong publication model %q", forbidden)
		}
	}
}

func TestPublicSourceAdmissionAllowsExplicitSyntheticFixtures(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	writeRepoFile(t, repo, "redaction.go", "package redaction\n\nconst marker = \"-----BEGIN PRIVATE KEY-----\"\n")
	writeRepoFile(t, repo, "testdata/apk/synthetic-base.apk", "synthetic APK fixture\n")
	writeRepoFile(t, repo, "testdata/native/synthetic-libroblox.so", "synthetic SO fixture\n")
	writeRepoFile(t, repo, "testdata/apk/synthetic-bundle.zip", "synthetic bundle fixture\n")
	gitCommitAll(t, repo, "safe synthetic fixtures")
	if output, err := runPublicSourceAdmission(repo, "HEAD"); err != nil {
		t.Fatalf("source admission rejected explicit synthetic fixtures: %v\n%s", err, output)
	}
}

func TestPublicSourceAdmissionAcceptsCleanCurrentCommit(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	writeRepoFile(t, repo, "README.md", "public source\n")
	commit := gitCommitAll(t, repo, "clean current source")

	output, err := runPublicSourceAdmission(repo, commit)
	if err != nil {
		t.Fatalf("source admission rejected clean current source: %v\n%s", err, output)
	}
	if !strings.Contains(output, "admitted "+commit) {
		t.Fatalf("source admission did not identify its admitted commit: %s", output)
	}
}

func TestPublicSourceAdmissionRequiresCleanWorktreeForSelectedCommit(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	writeRepoFile(t, repo, "README.md", "public source\n")
	commit := gitCommitAll(t, repo, "clean base")
	writeRepoFile(t, repo, "uncommitted.txt", "not admitted\n")

	output, err := runPublicSourceAdmission(repo, commit)
	if err == nil || !strings.Contains(output, "work tree is not clean for selected commit") {
		t.Fatalf("source admission accepted a dirty worktree: %v\n%s", err, output)
	}
}

func TestPublicSourceAdmissionRejectsReachableHistoricalLeak(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	writeRepoFile(t, repo, "README.md", "safe\n")
	gitCommitAll(t, repo, "safe base")
	writeRepoFile(t, repo, "archive/release.key", "not-a-real-key\n")
	leakCommit := gitCommitAll(t, repo, "historical filename leak")
	if err := os.Remove(filepath.Join(repo, "archive", "release.key")); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, repo, "remove historical filename leak")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil {
		t.Fatalf("source admission accepted a reachable historical leak: %s", output)
	}
	if !strings.Contains(output, leakCommit) || !strings.Contains(output, "archive/release.key") {
		t.Fatalf("historical leak diagnostic must name only its commit/path: %s", output)
	}
	if strings.Contains(output, "not-a-real-key") {
		t.Fatalf("historical leak diagnostic exposed file contents: %s", output)
	}
}

func TestPublicSourceAdmissionRejectsEncodedPEMWithoutEchoingIt(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	encoded := strings.Repeat("QUJD", 16)
	block := strings.Join([]string{"-----BEGIN PRIVATE KEY-----", encoded, "-----END PRIVATE KEY-----", ""}, "\n")
	writeRepoFile(t, repo, "source.txt", block)
	commit := gitCommitAll(t, repo, "encoded pem fixture")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil {
		t.Fatalf("source admission accepted an encoded PEM block: %s", output)
	}
	if !strings.Contains(output, commit) || !strings.Contains(output, "source.txt") || !strings.Contains(output, "encoded PEM private key") {
		t.Fatalf("encoded PEM diagnostic is incomplete: %s", output)
	}
	if strings.Contains(output, encoded) {
		t.Fatalf("encoded PEM diagnostic exposed its body: %s", output)
	}
}

func TestPublicSourceAdmissionRejectsHighConfidenceCredentialWithoutEchoingIt(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	credential := "ghp_" + strings.Repeat("Ab1", 12)
	writeRepoFile(t, repo, "config.txt", "api_key = "+credential+"\n")
	commit := gitCommitAll(t, repo, "credential fixture")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil {
		t.Fatalf("source admission accepted a high-confidence credential: %s", output)
	}
	if !strings.Contains(output, commit) || !strings.Contains(output, "config.txt") || !strings.Contains(output, "high-confidence credential value") {
		t.Fatalf("credential diagnostic is incomplete: %s", output)
	}
	if strings.Contains(output, credential) {
		t.Fatalf("credential diagnostic exposed its value: %s", output)
	}
}

func TestPublicSourceAdmissionAllowsOnlyReviewedGenericCredentialFixtures(t *testing.T) {
	const loggingTestPath = "internal/logging/logging_test.go"
	reviewedBlobs := []string{
		"29b2714b944bce1f3b5abaecf79db92b2c2bad5d",
		"29bcf100cb32fe66841690e688f9848f39a84025",
		"64435761862bfd222a01795875835c244e882a3e",
		"ccdfafeb8b98b32b1af4513c923489d919d21322",
	}
	for _, blobID := range reviewedBlobs {
		t.Run(blobID, func(t *testing.T) {
			repo := temporaryGitRepoWithReviewedBlob(t, blobID, loggingTestPath)
			if output, err := runPublicSourceAdmission(repo, "HEAD"); err != nil {
				t.Fatalf("source admission rejected reviewed generic-credential fixture %s: %v\n%s", blobID, err, output)
			}
		})
	}

	t.Run("reviewed blob at another path is rejected", func(t *testing.T) {
		repo := temporaryGitRepoWithReviewedBlob(t, reviewedBlobs[0], "internal/other/logging_test.go")
		output, err := runPublicSourceAdmission(repo, "HEAD")
		if err == nil || !strings.Contains(output, "high-confidence credential value") {
			t.Fatalf("source admission accepted a reviewed blob outside its allowlisted path: %v\n%s", err, output)
		}
	})

	t.Run("new generic fixture is rejected", func(t *testing.T) {
		repo := temporaryGitRepo(t)
		writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
		credential := strings.Join([]string{"N7q4Vm2X", "p8Ld5Rs1", "Zk9c3Hw6", "Tf0By7Ja"}, "")
		writeRepoFile(t, repo, loggingTestPath, "authorization: Bearer "+credential+"\n")
		gitCommitAll(t, repo, "new generic logging fixture")

		output, err := runPublicSourceAdmission(repo, "HEAD")
		if err == nil || !strings.Contains(output, "high-confidence credential value") {
			t.Fatalf("source admission accepted a new generic-credential fixture: %v\n%s", err, output)
		}
		if strings.Contains(output, credential) {
			t.Fatalf("generic-credential diagnostic exposed its value: %s", output)
		}
	})
}

func TestPublicSourceAdmissionRejectsPrivateAndProprietaryPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "tracked private directory", path: ".tipsy-private/docs/notes.txt", want: "private material tracked"},
		{name: "credential filename", path: "config/credentials.json", want: "credential filename"},
		{name: "Roblox library", path: "payload/libroblox.so", want: "Roblox or proprietary payload filename"},
		{name: "APK outside fixture", path: "payload/client.apk", want: "Roblox or proprietary payload filename"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := temporaryGitRepo(t)
			writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
			writeRepoFile(t, repo, test.path, "fixture\n")
			var commit string
			if strings.HasPrefix(test.path, ".tipsy-private/") {
				gitRun(t, repo, "add", "--all")
				gitRun(t, repo, "add", "--force", test.path)
				gitRun(t, repo, "commit", "--quiet", "-m", test.name)
				commit = strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
			} else {
				commit = gitCommitAll(t, repo, test.name)
			}

			output, err := runPublicSourceAdmission(repo, "HEAD")
			if err == nil {
				t.Fatalf("source admission accepted %s: %s", test.path, output)
			}
			if !strings.Contains(output, commit) || !strings.Contains(output, test.path) || !strings.Contains(output, test.want) {
				t.Fatalf("unsafe path diagnostic is incomplete: %s", output)
			}
		})
	}
}

func TestPublicSourceAdmissionRequiresIgnoredPrivateDirectory(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, "README.md", "safe\n")
	gitCommitAll(t, repo, "missing private ignore")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil || !strings.Contains(output, ".tipsy-private/ must be ignored") {
		t.Fatalf("source admission accepted a repository without private ignore: %v\n%s", err, output)
	}
}

func TestPublicSourceAdmissionRejectsGitSymlinkWithoutEchoingTarget(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	writeRepoFile(t, repo, "README.md", "safe\n")
	linkTarget := "/private/release-authority"
	if err := os.Symlink(linkTarget, filepath.Join(repo, "linked-source")); err != nil {
		t.Fatal(err)
	}
	commit := gitCommitAll(t, repo, "symlink fixture")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil {
		t.Fatalf("source admission accepted a Git symlink: %s", output)
	}
	if !strings.Contains(output, commit) || !strings.Contains(output, "linked-source") || !strings.Contains(output, "Git symlink entry") {
		t.Fatalf("Git symlink diagnostic is incomplete: %s", output)
	}
	if strings.Contains(output, linkTarget) {
		t.Fatalf("Git symlink diagnostic exposed its target: %s", output)
	}
}

func TestPublicSourceAdmissionRejectsGitlink(t *testing.T) {
	child := temporaryGitRepo(t)
	writeRepoFile(t, child, "README.md", "dependency\n")
	gitCommitAll(t, child, "dependency")

	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	gitRun(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", child, "vendor/dependency")
	commit := gitCommitAll(t, repo, "gitlink fixture")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil {
		t.Fatalf("source admission accepted a Gitlink: %s", output)
	}
	if !strings.Contains(output, commit) || !strings.Contains(output, "vendor/dependency") || !strings.Contains(output, "non-regular Git entry") {
		t.Fatalf("Gitlink diagnostic is incomplete: %s", output)
	}
}

func TestPublicSourceAdmissionRejectsOversizedBlobBeforeContentInspection(t *testing.T) {
	repo := temporaryGitRepo(t)
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	path := filepath.Join(repo, "large-source.dat")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8*1024*1024 + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	commit := gitCommitAll(t, repo, "oversized blob fixture")

	output, err := runPublicSourceAdmission(repo, "HEAD")
	if err == nil {
		t.Fatalf("source admission accepted an oversized blob: %s", output)
	}
	if !strings.Contains(output, commit) || !strings.Contains(output, "large-source.dat") || !strings.Contains(output, "tracked blob exceeds 8388608-byte inspection limit") {
		t.Fatalf("oversized blob diagnostic is incomplete: %s", output)
	}
}

func TestReleaseBuilderRunsSourceAdmissionBeforeCreatingOutput(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	guard := strings.Index(text, `check-public-source.sh" --repo "$repo" --commit "$source_commit"`)
	outputCreation := strings.Index(text, `mkdir -p -- "$output_dir"`)
	if guard < 0 || outputCreation < 0 || guard > outputCreation {
		t.Fatal("release-build must admit source before creating its output directory")
	}
}

func temporaryGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "--quiet")
	gitRun(t, repo, "config", "user.name", "Tipsy Packaging Test")
	gitRun(t, repo, "config", "user.email", "packaging-test@example.invalid")
	return repo
}

func temporaryGitRepoWithReviewedBlob(t *testing.T, blobID, relativePath string) string {
	t.Helper()
	repo := temporaryGitRepo(t)
	objectsPath := strings.TrimSpace(gitRun(t, repoRoot(t), "rev-parse", "--path-format=absolute", "--git-path", "objects"))
	if err := os.MkdirAll(filepath.Join(repo, ".git", "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "objects", "info", "alternates"), []byte(objectsPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "cat-file", "-e", blobID+"^{blob}").Run(); err != nil {
		t.Fatalf("reviewed fixture blob %s is unavailable: %v", blobID, err)
	}
	writeRepoFile(t, repo, ".gitignore", "/.tipsy-private/\n")
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "update-index", "--add", "--cacheinfo", "100644,"+blobID+","+relativePath)
	tree := strings.TrimSpace(gitRun(t, repo, "write-tree"))
	commit := strings.TrimSpace(gitRun(t, repo, "commit-tree", tree, "-m", "reviewed logging fixture"))
	gitRun(t, repo, "update-ref", "refs/heads/master", commit)
	gitRun(t, repo, "checkout-index", "--all")
	return repo
}

func writeRepoFile(t *testing.T, repo, relative, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitCommitAll(t *testing.T, repo, message string) string {
	t.Helper()
	gitRun(t, repo, "add", "--all")
	gitRun(t, repo, "commit", "--quiet", "-m", message)
	return strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
}

func gitRun(t *testing.T, repo string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func runPublicSourceAdmission(repo, commit string) (string, error) {
	command := exec.Command(filepath.Join(repoRootForTestBinary(), "scripts", "check-public-source.sh"), "--repo", repo, "--commit", commit)
	output, err := command.CombinedOutput()
	return string(output), err
}

func repoRootForTestBinary() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestReleaseBuilderUsesUnprivilegedCredentialScrubbedBuilds(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"bwrap", "unshare", "systemd-run", "sudo -n", "TIPSY_RELEASE_SOURCE_READONLY"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("release builder retains a privileged or unsupported isolation dependency: %q", forbidden)
		}
	}
	for _, required := range []string{
		"env -i",
		`PATH="$go_bin_dir:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"`,
		"GOPROXY=off",
		"GOSUMDB=off",
		"assert_clean_source",
		"release build modified the checked-out source tree",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("release builder is missing unprivileged-build invariant %q", required)
		}
	}
}

func TestAttestationScaffoldPinsVerifierAndFailsBeforeH0(t *testing.T) {
	repo := repoRoot(t)
	policy := filepath.Join(repo, "scripts", "attestation-policy.json")
	data, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["status"] != "reviewed" || parsed["builderEnvironment"] != "github-hosted" {
		t.Fatalf("unsafe GitHub-signed attestation policy: %s", data)
	}
	script, err := os.ReadFile(filepath.Join(repo, "scripts", "verify-release-attestation.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"gh_sha256", "--bundle", "--unshare-net", "--deny-self-hosted-runners", "reviewed"} {
		if !bytes.Contains(script, []byte(required)) {
			t.Errorf("attestation verifier scaffold is missing %q", required)
		}
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

	t.Run("play with runtime opens progress", func(t *testing.T) {
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
		assertStubLog(t, logPath, "tipsy-gui --play")
	})

	t.Run("explicit launch keeps cli behavior", func(t *testing.T) {
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
		assertStubLog(t, logPath, "tipsy-gui --play roblox://experiences/start?placeId=1818")
	})

	t.Run("website uri without runtime reaches gui setup", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "stub.log")
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--no-integrate", "roblox://experiences/start?placeId=1818")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, "")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("first-run uri: %v\n%s", err, output)
		}
		assertStubLog(t, logPath, "tipsy-gui roblox://experiences/start?placeId=1818")
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
		assertStubLog(t, logPath, "tipsy-gui --play roblox://experiences/start?placeId=1818")
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

	t.Run("missing gui fails without cli fallback", func(t *testing.T) {
		appdir := fakeRunnableAppDir(t)
		if err := os.Remove(filepath.Join(appdir, "usr", "bin", "tipsy-gui")); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(t.TempDir(), "stub.log")
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "tipsy", "runtime", "lib", "x86_64"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(filepath.Join(appdir, "AppRun"), "--play", "--no-integrate")
		command.Dir = appdir
		command.Env = stubEnv(t, logPath, xdg)
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("missing GUI unexpectedly succeeded: %s", output)
		}
		if got := strings.TrimSpace(string(output)); got != "Tipsy: bundled graphical launcher is unavailable." {
			t.Fatalf("missing GUI output=%q", got)
		}
		if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
			t.Fatalf("CLI fallback ran or wrote a log: %v", statErr)
		}
	})
}

func TestAppRunHandsOuterAppImageToReleaseVerifier(t *testing.T) {
	appdir := fakeRunnableAppDir(t)
	output := filepath.Join(t.TempDir(), "release-artifact")
	stub := "#!/bin/sh\nprintf '%s\\n%s' \"$TIPSY_RELEASE_ARTIFACT\" \"$TIPSY_RELEASE_APPDIR\" > \"$TIPSY_RELEASE_TEST_OUTPUT\"\n"
	if err := os.WriteFile(filepath.Join(appdir, "usr", "bin", "tipsy-gui"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "Tipsy-1.2.3-x86_64.AppImage")
	command := exec.Command(filepath.Join(appdir, "AppRun"), "--settings", "--no-integrate")
	command.Dir = appdir
	command.Env = append(stubEnv(t, filepath.Join(t.TempDir(), "stub.log"), ""), "APPIMAGE="+artifact, "TIPSY_RELEASE_TEST_OUTPUT="+output)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("AppRun: %v\n%s", err, data)
	}
	got, err := os.ReadFile(output)
	want := artifact + "\n" + appdir
	if err != nil || string(got) != want {
		t.Fatalf("release artifact=%q err=%v", got, err)
	}
}

// AppRun delegates launcher integration to the real `tipsy desktop adopt`.
// A developer build integrates under the separate Tipsy-Dev identity, so it
// never shadows the developer's real (Flatpak/package) Tipsy install, and it
// still cleans up the duplicate entries AppImage managers leave behind.
func TestAppRunIntegratesDeveloperBuildAsTipsyDev(t *testing.T) {
	appdir := realCLIAppDir(t, "dev")
	home := t.TempDir()
	xdg := filepath.Join(home, ".local", "share")
	apps := filepath.Join(xdg, "applications")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(home, "Applications", "Tipsy-0.0.0-dev-x86_64.AppImage")
	previous := filepath.Join(home, "Applications", "Tipsy-0.0.0-test8-x86_64.AppImage")
	leftover := "[Desktop Entry]\nName=Tipsy - Play\nComment=Managed by AppImage Manager\nExec=" + previous + " launch %u\nIcon=appimage_tipsy_old\n"
	if err := os.WriteFile(filepath.Join(apps, "appimage_tipsy_old.desktop"), []byte(leftover), 0o644); err != nil {
		t.Fatal(err)
	}
	other := "[Desktop Entry]\nName=Other\nExec=" + filepath.Join(home, "Applications", "Other.AppImage") + "\nIcon=appimage_other\n"
	if err := os.WriteFile(filepath.Join(apps, "appimage_other.desktop"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	iconDir := filepath.Join(xdg, "icons", "hicolor", "256x256", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"appimage_tipsy_old.png", "appimage_other.png"} {
		if err := os.WriteFile(filepath.Join(iconDir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runAppRun(t, appdir, xdg, home, current, "--settings")

	if _, err := os.Stat(filepath.Join(apps, "io.github.tipsy_linux.Tipsy.Play.desktop")); !os.IsNotExist(err) {
		t.Fatalf("developer build must not write the stable Tipsy identity: %v", err)
	}
	play := readFile(t, filepath.Join(apps, "io.github.tipsy_linux.Tipsy.Dev.Play.desktop"))
	settings := readFile(t, filepath.Join(apps, "io.github.tipsy_linux.Tipsy.Dev.Settings.desktop"))
	icon := filepath.Join(iconDir, "io.github.tipsy_linux.Tipsy.Dev.png")
	for _, want := range []string{
		"Name=Tipsy-Dev - Play\n",
		`Exec="` + current + `" --play %u` + "\n",
		"MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n",
		"StartupWMClass=roblox\n",
		"X-AppImage-Integrate=false\n",
		"X-Tipsy-Medium=appimage\n",
		"X-Tipsy-Origin=" + current + "\n",
		"X-Tipsy-Channel=dev\n",
		"Icon=" + icon + "\n",
		"[Desktop Action Settings]\nName=Tipsy-Dev - Settings\n",
	} {
		if !strings.Contains(play, want) {
			t.Errorf("Dev Play pin missing %q:\n%s", want, play)
		}
	}
	if strings.Count(play, `Exec="`+current+`" --play %u`+"\n") != 1 || strings.Contains(play, "NoDisplay=true") {
		t.Fatalf("Dev Play pin must have one visible Play Exec:\n%s", play)
	}
	for _, want := range []string{"Name=Tipsy-Dev - Settings\n", `Exec="` + current + `" --settings %u` + "\n", "StartupWMClass=tipsy-gui\n", "Icon=" + icon + "\n"} {
		if !strings.Contains(settings, want) {
			t.Errorf("Dev Settings pin missing %q:\n%s", want, settings)
		}
	}
	if strings.Contains(settings, "MimeType=x-scheme-handler/") {
		t.Fatalf("Settings pin must not compete for Roblox URI handling: %s", settings)
	}
	for _, size := range []string{"256x256", "512x512"} {
		if _, err := os.Stat(filepath.Join(xdg, "icons", "hicolor", size, "apps", "io.github.tipsy_linux.Tipsy.Dev.png")); err != nil {
			t.Errorf("%s pin icon: %v", size, err)
		}
	}
	if _, err := os.Stat(filepath.Join(apps, "appimage_tipsy_old.desktop")); !os.IsNotExist(err) {
		t.Fatalf("leftover AppImage manager pin should be deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(iconDir, "appimage_tipsy_old.png")); !os.IsNotExist(err) {
		t.Fatalf("leftover AppImage manager icon should be deleted: %v", err)
	}
	if got := readFile(t, filepath.Join(apps, "appimage_other.desktop")); got != other {
		t.Fatalf("unrelated AppImage launcher was changed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(iconDir, "appimage_other.png")); err != nil {
		t.Fatalf("unrelated AppImage icon was deleted: %v", err)
	}
	// The dev build must not claim the roblox:// handler by default.
	if data, err := os.ReadFile(filepath.Join(configHomeOf(t, appdir), "mimeapps.list")); err == nil && strings.Contains(string(data), "Tipsy.Dev") {
		t.Fatalf("developer build registered itself as URI handler:\n%s", data)
	}
}

// A stable AppImage only integrates when nothing else provides the Tipsy
// identity; an installed package or Flatpak is left as the launcher.
func TestAppRunStableBuildDefersToInstalledPackage(t *testing.T) {
	appdir := realCLIAppDir(t, "stable")
	appImage := "Tipsy-1.2.0-x86_64.AppImage"

	t.Run("package installed", func(t *testing.T) {
		home := t.TempDir()
		xdg := filepath.Join(home, ".local", "share")
		system := filepath.Join(t.TempDir(), "usr", "share")
		installed := filepath.Join(system, "applications", "io.github.tipsy_linux.Tipsy.Play.desktop")
		if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(installed, []byte("[Desktop Entry]\nType=Application\nName=Tipsy - Play\nExec=tipsy-gui --play %u\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runAppRun(t, appdir, xdg, home, filepath.Join(home, appImage), "--settings", "XDG_DATA_DIRS="+system)
		if _, err := os.Stat(filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Play.desktop")); !os.IsNotExist(err) {
			t.Fatalf("AppImage shadowed an installed package with a user-scope entry: %v", err)
		}
		if _, err := os.Stat(filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Dev.Play.desktop")); !os.IsNotExist(err) {
			t.Fatalf("stable build wrote the dev identity: %v", err)
		}
	})

	t.Run("nothing installed", func(t *testing.T) {
		home := t.TempDir()
		xdg := filepath.Join(home, ".local", "share")
		empty := filepath.Join(t.TempDir(), "share")
		current := filepath.Join(home, appImage)
		runAppRun(t, appdir, xdg, home, current, "--settings", "XDG_DATA_DIRS="+empty)
		play := readFile(t, filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Play.desktop"))
		for _, want := range []string{"Name=Tipsy - Play\n", `Exec="` + current + `" --play %u` + "\n", "X-Tipsy-Channel=stable\n"} {
			if !strings.Contains(play, want) {
				t.Errorf("stable Play pin missing %q:\n%s", want, play)
			}
		}
		if _, err := os.Stat(filepath.Join(xdg, "applications", "io.github.tipsy_linux.Tipsy.Settings.desktop")); err != nil {
			t.Fatalf("stable Settings pin: %v", err)
		}
	})
}

func runAppRun(t *testing.T, appdir, xdg, home, appImage string, arg string, extraEnv ...string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "stub.log")
	command := exec.Command(filepath.Join(appdir, "AppRun"), arg)
	command.Dir = appdir
	command.Env = append(stubEnv(t, logPath, xdg),
		"HOME="+home,
		"APPIMAGE="+appImage,
		"XDG_CONFIG_HOME="+configHomeOf(t, appdir),
	)
	command.Env = append(command.Env, extraEnv...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("AppRun %s: %v\n%s", arg, err, output)
	}
}

// configHomeOf gives each fixture one XDG_CONFIG_HOME so a test can inspect
// the mimeapps.list that xdg-mime (when present on the host) writes.
func configHomeOf(t *testing.T, appdir string) string {
	t.Helper()
	dir := filepath.Join(appdir, ".xdg-config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// realCLIAppDir is fakeRunnableAppDir with the real tipsy CLI (built for the
// given release channel) so AppRun's `tipsy desktop adopt` really runs.
// tipsy-gui stays a stub. Builds are cached by the go tool.
func realCLIAppDir(t *testing.T, channel string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the tipsy CLI")
	}
	appdir := fakeRunnableAppDir(t)
	repo := repoRoot(t)
	cli := filepath.Join(appdir, "usr", "bin", "tipsy")
	if err := os.Remove(cli); err != nil { // replace the shell stub
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/tipsy-linux/tipsy/internal/version.Channel="+channel,
		"-o", cli, "./cmd/tipsy")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tipsy CLI: %v\n%s", err, output)
	}
	return appdir
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
		"usr/share/tipsy/build-info.json",
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
	refreshManifest(t, root)
	return root
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func mustChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func refreshManifest(t *testing.T, root string) {
	t.Helper()
	manifest := filepath.Join(root, "usr/share/tipsy/manifest.sha256")
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == manifest || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var contents strings.Builder
	for _, relative := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		fmt.Fprintf(&contents, "%x  %s\n", digest, relative)
	}
	if err := os.WriteFile(manifest, []byte(contents.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
