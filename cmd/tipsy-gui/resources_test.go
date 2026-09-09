package main

import (
	"image/png"
	"os"
	"path/filepath"
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
