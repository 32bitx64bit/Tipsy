package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBrandIconIsRealHighResolutionPNG(t *testing.T) {
	path := filepath.Join("..", "..", "tipsy.png")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if config.Width < 256 || config.Height < 256 {
		t.Fatalf("icon is only %dx%d", config.Width, config.Height)
	}
}

func TestBrandIconCandidatesIncludeFlatpakExportAndKeepOverrideFirst(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom.png")
	t.Setenv("TIPSY_ICON_PATH", override)
	exeDir := filepath.Join(t.TempDir(), "app", "bin")

	candidates := brandIconCandidates(exeDir)
	if len(candidates) == 0 || candidates[0] != override {
		t.Fatalf("TIPSY_ICON_PATH must stay the first candidate, got %q", candidates)
	}
	for _, want := range []string{
		filepath.Join(exeDir, "..", "share", "icons", "hicolor", "512x512", "apps", "tipsy.png"),
		filepath.Join(exeDir, "..", "share", "icons", "hicolor", "512x512", "apps", "io.github.tipsy_linux.Tipsy.png"),
		filepath.Join(exeDir, "..", "share", "icons", "hicolor", "256x256", "apps", "io.github.tipsy_linux.Tipsy.png"),
	} {
		if !slices.Contains(candidates, want) {
			t.Errorf("brandIconCandidates(%q) missing %q: %q", exeDir, want, candidates)
		}
	}
}

// A Flatpak install exposes only the app-id filename to the GUI; resolution
// must find it without QApplication or a real executable path.
func TestBrandIconResolutionFindsFlatpakExportedIcon(t *testing.T) {
	for _, size := range []string{"512x512", "256x256"} {
		t.Run(size, func(t *testing.T) {
			root := t.TempDir()
			exeDir := filepath.Join(root, "app", "bin")
			if err := os.MkdirAll(exeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(root, "app", "share", "icons", "hicolor", size, "apps", "io.github.tipsy_linux.Tipsy.png")
			if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(want, []byte("png"), 0o644); err != nil {
				t.Fatal(err)
			}

			t.Setenv("TIPSY_ICON_PATH", "")
			if got := firstExistingIcon(brandIconCandidates(exeDir)); filepath.Clean(got) != filepath.Clean(want) {
				t.Fatalf("resolved %q, want %q", got, want)
			}
		})
	}
}

func TestBrandIconFallbacksPreferTipsyOverNeutralIcon(t *testing.T) {
	names := brandIconFallbackNames()
	if len(names) < 2 {
		t.Fatalf("expected at least two theme fallbacks, got %q", names)
	}
	if names[0] != "tipsy" {
		t.Errorf("first fallback must be the installed tipsy icon, got %q", names[0])
	}
	if names[len(names)-1] != "application-x-executable" {
		t.Errorf("last fallback must be neutral, got %q", names[len(names)-1])
	}
	for _, name := range names {
		if name == "applications-games" {
			t.Fatal("fallbacks must never use the generic game controller icon")
		}
	}
}

func TestDesktopEntriesTargetPlayAndSettings(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		lines []string
	}{
		{
			name: "play",
			file: "io.github.tipsy_linux.Tipsy.Play.desktop",
			lines: []string{
				"Name=Tipsy - Play\n",
				"Exec=tipsy-gui --play %u\n",
				"Icon=tipsy\n",
				"Terminal=false\n",
				"MimeType=x-scheme-handler/roblox;x-scheme-handler/roblox-player;\n",
				"StartupWMClass=roblox\n",
				"X-AppImage-Integrate=false\n",
			},
		},
		{
			name: "settings",
			file: "io.github.tipsy_linux.Tipsy.Settings.desktop",
			lines: []string{
				"Name=Tipsy - Settings\n",
				"Exec=tipsy-gui %u\n",
				"Icon=tipsy\n",
				"Terminal=false\n",
				"StartupWMClass=tipsy-gui\n",
				"Categories=Game;\n",
				"X-AppImage-Integrate=false\n",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join("..", "..", "share", "applications", test.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, line := range test.lines {
				if !strings.Contains(text, line) {
					t.Errorf("desktop entry missing %q", strings.TrimSuffix(line, "\n"))
				}
			}
		})
	}
}

func TestThemeUsesTipsyBrandPalette(t *testing.T) {
	for _, color := range []string{"#141820", "#2d6bff", "#ffffff"} {
		if !strings.Contains(appStyleSheet, color) {
			t.Errorf("theme missing brand color %s", color)
		}
	}
	for _, selector := range []string{
		"QPushButton:focus", "QRadioButton:focus", "QRadioButton:checked", "QScrollBar:vertical", "QWidget#wizardPage",
		"QCheckBox#vsyncToggle::indicator", "QCheckBox#vsyncToggle::indicator:checked", "QCheckBox#vsyncToggle:hover:enabled",
		"QCheckBox#vsyncToggle:focus", "QCheckBox#vsyncToggle:disabled", "QCheckBox#vsyncToggle:disabled:focus",
	} {
		if !strings.Contains(appStyleSheet, selector) {
			t.Errorf("theme missing interaction/layout selector %s", selector)
		}
	}
}
