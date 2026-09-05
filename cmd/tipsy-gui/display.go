package main

import (
	"fmt"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

type displayChoice struct {
	key   string
	label string
}

type displayScreen struct {
	name          string
	width, height int
	primary       bool
}

func desktopDisplayChoices(selected string) []displayChoice {
	primary := qt.QGuiApplication_PrimaryScreen()
	var screens []displayScreen
	for _, screen := range qt.QGuiApplication_Screens() {
		if screen == nil || screen.Name() == "" {
			continue
		}
		info := displayScreen{name: screen.Name()}
		if size := screen.Size(); size != nil {
			info.width, info.height = size.Width(), size.Height()
		}
		if primary != nil {
			info.primary = screen.Name() == primary.Name()
		}
		screens = append(screens, info)
	}
	return displayChoices(selected, screens)
}

func displayChoices(selected string, screens []displayScreen) []displayChoice {
	selected = guimodel.NormalizeDisplay(selected)
	choices := []displayChoice{
		{guimodel.DisplayPrimary, "Main monitor (default)"},
		{guimodel.DisplayPointer, "Follow mouse"},
	}
	seen := map[string]bool{guimodel.DisplayPrimary: true, guimodel.DisplayPointer: true}
	for _, screen := range screens {
		if screen.name == "" || seen[screen.name] {
			continue
		}
		seen[screen.name] = true
		label := screen.name
		if screen.width > 0 && screen.height > 0 {
			label = fmt.Sprintf("%s · %d×%d", screen.name, screen.width, screen.height)
		}
		if screen.primary {
			label += " (main)"
		}
		choices = append(choices, displayChoice{screen.name, label})
	}
	if !seen[selected] {
		choices = append(choices, displayChoice{selected, selected + " (not connected)"})
	}
	return choices
}

func screenForDisplay(display string) *qt.QScreen {
	display = guimodel.NormalizeDisplay(display)
	if guimodel.IsPointerDisplay(display) {
		return nil
	}
	screens := qt.QGuiApplication_Screens()
	if display != guimodel.DisplayPrimary {
		for _, screen := range screens {
			if screen != nil && screen.Name() == display {
				return screen
			}
		}
	}
	if primary := qt.QGuiApplication_PrimaryScreen(); primary != nil {
		return primary
	}
	if len(screens) > 0 {
		return screens[0]
	}
	return nil
}

func centerWindowOnRect(areaX, areaY, areaW, areaH, winW, winH int) (x, y int) {
	x, y = areaX, areaY
	if areaW > winW {
		x += (areaW - winW) / 2
	}
	if areaH > winH {
		y += (areaH - winH) / 2
	}
	return x, y
}

func placeWidgetOnDisplay(widget *qt.QWidget, display string) {
	if widget == nil || guimodel.IsPointerDisplay(display) {
		return
	}
	screen := screenForDisplay(display)
	if screen == nil {
		return
	}
	area := screen.AvailableGeometry()
	if area == nil {
		return
	}
	size := widget.FrameSize()
	if size == nil || size.Width() < 1 || size.Height() < 1 {
		size = widget.Size()
	}
	if size == nil || size.Width() < 1 || size.Height() < 1 {
		return
	}
	x, y := centerWindowOnRect(area.X(), area.Y(), area.Width(), area.Height(), size.Width(), size.Height())
	widget.Move(x, y)
}

func configuredDisplay(w *mainWindow) string {
	if w == nil || w.settings == nil {
		return guimodel.DisplayPrimary
	}
	return guimodel.NormalizeDisplay(w.settings.View().Saved.Display)
}
