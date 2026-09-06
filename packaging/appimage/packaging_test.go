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

func TestReleaseInputLockIsCanonicalAndFailClosed(t *testing.T) {
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "release-lock.py")
	lock := filepath.Join(repo, "scripts", "release-inputs.lock.json")
	if output, err := exec.Command(script, "--lock", lock, "--mode", "developer").CombinedOutput(); err != nil {
		t.Fatalf("developer lock validation: %v\n%s", err, output)
	}
	output, err := exec.Command(script, "--lock", lock, "--mode", "official").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "reviewed release input lock") {
		t.Fatalf("bootstrap lock did not block official build: %v\n%s", err, output)
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
	for _, forbidden := range []string{"ubuntu-latest", "continue-on-error: true", "permissions: write-all"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("workflow retains unsafe/mutable setting %q", forbidden)
		}
	}
	for _, required := range []string{"permissions:\n  contents: read", "persist-credentials: false", `go-version: "1.27.1"`} {
		if !strings.Contains(text, required) {
			t.Errorf("workflow is missing %q", required)
		}
	}
}

func TestReleaseBuilderUsesExistingWritableMountpoints(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "release-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"--dir /release-out", `--bind "$destination" /release-out`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("release builder creates a mountpoint after the read-only root bind: %q", forbidden)
		}
	}
	for _, required := range []string{
		`--bind "$destination" "$destination"`,
		`--dir /tmp/home`,
		`--setenv PATH "$go_bin_dir:/usr/local/bin:/usr/bin:/bin"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("release builder is missing isolated-build invariant %q", required)
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
	if parsed["status"] != "bootstrap-unverified" || parsed["builderEnvironment"] != "github-hosted" {
		t.Fatalf("unsafe bootstrap attestation policy: %s", data)
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
