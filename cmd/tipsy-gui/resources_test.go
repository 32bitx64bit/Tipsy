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

func TestDesktopEntryTargetsGUIAndThemeIcon(t *testing.T) {
	path := filepath.Join("..", "..", "share", "applications", "io.github.tipsy_linux.Tipsy.desktop")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, line := range []string{"Type=Application", "Exec=tipsy-gui", "Icon=tipsy", "Terminal=false"} {
		if !strings.Contains(text, line+"\n") {
			t.Errorf("desktop entry missing %q", line)
		}
	}
}

func TestThemeUsesTipsyBrandPalette(t *testing.T) {
	for _, color := range []string{"#091329", "#126cf3", "#dff515"} {
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
