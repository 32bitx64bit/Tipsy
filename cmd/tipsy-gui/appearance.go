package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	qt "github.com/mappu/miqt/qt6"
)

type appearanceMode string

const (
	appearanceSystem appearanceMode = "system"
	appearanceLight  appearanceMode = "light"
	appearanceDark   appearanceMode = "dark"
)

func validAppearance(mode appearanceMode) bool {
	return mode == appearanceSystem || mode == appearanceLight || mode == appearanceDark
}

// GUI appearance is independent of the shared Roblox settings and their Apply
// transaction. Atomic replacement preserves the last preference on write errors.
func appearancePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tipsy", "appearance.json"), nil
}
func loadAppearance() appearanceMode {
	path, err := appearancePath()
	if err != nil {
		return appearanceSystem
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return appearanceSystem
	}
	var value struct {
		Mode appearanceMode `json:"mode"`
	}
	if json.Unmarshal(data, &value) != nil || !validAppearance(value.Mode) {
		return appearanceSystem
	}
	return value.Mode
}
func saveAppearance(mode appearanceMode) error {
	path, err := appearancePath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".appearance-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	data, _ := json.Marshal(struct {
		Mode appearanceMode `json:"mode"`
	}{mode})
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (w *mainWindow) initAppearance() {
	w.appearance = loadAppearance()
	// Read the platform palette before applying our own. Qt < 6.5 and platforms
	// without a color-scheme preference use this native palette as their fallback.
	palette := qt.QGuiApplication_Palette()
	color := palette.ColorWithCr(qt.QPalette__Window)
	w.systemDarkFallback = color.Lightness() < 128
	runtime.KeepAlive(palette) // ColorWithCr borrows from this auto-finalized value.
	w.applyAppearance()
	timer := qt.NewQTimer2(w.win.QObject)
	// MIQT targets Qt 6.2; querying the Qt 6.5+ property avoids a new native shim
	// while still following desktop theme changes on newer Qt installations.
	timer.OnTimeout(func() {
		if w.appearance == appearanceSystem && w.systemDark() != w.appearanceIsDark {
			w.applyAppearance()
		}
	})
	timer.Start(1000)
}
func (w *mainWindow) systemDark() bool {
	value := qt.QGuiApplication_StyleHints().Property("colorScheme")
	if value.IsValid() {
		switch value.ToInt() {
		case 1:
			return false
		case 2:
			return true
		}
	}
	return w.systemDarkFallback
}
func (w *mainWindow) chooseAppearance(mode appearanceMode) {
	if !validAppearance(mode) {
		return
	}
	if err := saveAppearance(mode); err != nil {
		qt.QMessageBox_Warning(w.win.QWidget, "Appearance could not be saved", "Check that your Tipsy configuration folder is writable, then try again.")
		w.syncAppearanceButtons()
		return
	}
	w.appearance = mode
	w.applyAppearance()
}
func (w *mainWindow) syncAppearanceButtons() {
	for mode, button := range w.appearanceButtons {
		button.SetChecked(mode == w.appearance)
	}
}
func (w *mainWindow) applyAppearance() {
	dark := w.appearance == appearanceDark || w.appearance == appearanceSystem && w.systemDark()
	w.appearanceIsDark = dark
	palette := qt.NewQPalette()
	colors := map[qt.QPalette__ColorRole]string{
		qt.QPalette__Window: "#fcfcfd", qt.QPalette__Base: "#ffffff", qt.QPalette__AlternateBase: "#eef0f4",
		qt.QPalette__Button: "#ffffff", qt.QPalette__WindowText: "#141820", qt.QPalette__Text: "#141820", qt.QPalette__ButtonText: "#141820",
		qt.QPalette__Highlight: "#2d6bff", qt.QPalette__HighlightedText: "#ffffff", qt.QPalette__ToolTipBase: "#141820", qt.QPalette__ToolTipText: "#fcfcfd",
	}
	if dark {
		colors[qt.QPalette__Window] = "#1c1f24"
		colors[qt.QPalette__Base] = "#23272e"
		colors[qt.QPalette__Button] = "#23272e"
		colors[qt.QPalette__AlternateBase] = "#2d333d"
		for _, role := range []qt.QPalette__ColorRole{qt.QPalette__WindowText, qt.QPalette__Text, qt.QPalette__ButtonText} {
			colors[role] = "#f3f5f7"
		}
		colors[qt.QPalette__ToolTipBase] = "#f3f5f7"
		colors[qt.QPalette__ToolTipText] = "#1c1f24"
	}
	for role, hex := range colors {
		color := qt.NewQColor6(hex)
		palette.SetColor2(role, color)
		color.Delete()
	}
	disabled := qt.NewQColor6("#8390a2")
	for _, role := range []qt.QPalette__ColorRole{qt.QPalette__WindowText, qt.QPalette__Text, qt.QPalette__ButtonText} {
		palette.SetColor(qt.QPalette__Disabled, role, disabled)
	}
	disabled.Delete()
	qt.QApplication_SetPalette(palette)
	palette.Delete()
	app := qt.UnsafeNewQApplication(qt.QCoreApplication_Instance().UnsafePointer())
	app.SetStyleSheet(themeStyleSheet(dark))
	w.syncAppearanceButtons()
	if w.stack != nil && w.stack.CurrentIndex() == 3 {
		w.runDoctor()
	}
}
