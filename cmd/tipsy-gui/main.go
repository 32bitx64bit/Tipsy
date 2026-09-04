// Command tipsy-gui is the Qt 6 desktop application for Tipsy.
//
// It contains presentation and workflow code only. Package provenance,
// extraction, client settings and launch behavior are delegated to the shared
// backend used by the CLI.
package main

import (
	"os"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/version"
)

func main() {
	logging.Init()

	app := qt.NewQApplication(os.Args)
	qt.QCoreApplication_SetOrganizationName("tipsy-linux")
	qt.QCoreApplication_SetOrganizationDomain("tipsy-linux.github.io")
	qt.QCoreApplication_SetApplicationName("tipsy-gui")
	qt.QCoreApplication_SetApplicationVersion(version.String())
	qt.QGuiApplication_SetApplicationDisplayName("Tipsy")
	qt.QGuiApplication_SetQuitOnLastWindowClosed(true)
	qt.QApplication_SetStyleWithStyle("Fusion")
	app.SetStyleSheet(appStyleSheet)

	icon := brandIcon()
	qt.QGuiApplication_SetWindowIcon(icon)

	win := newMainWindow(newProductionService(), icon)
	win.Show()
	if win.FirstRun() {
		win.ShowSetupWizard(true)
	}
	qt.QApplication_Exec()
}
