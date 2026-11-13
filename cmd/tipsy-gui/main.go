// Command tipsy-gui is the Qt 6 desktop application for Tipsy. It contains
// presentation and workflow code only; package provenance, extraction, client
// settings and launch behavior are delegated to the shared backend.
package main

import (
	"fmt"
	"os"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/graphics"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/perf"
	"github.com/tipsy-linux/tipsy/internal/version"
)

func main() {
	// The Settings card's isolated anti-lag probe re-executes this binary.
	// Answer it before Qt starts so it cannot open a second window.
	if len(os.Args) > 1 && os.Args[1] == graphics.AntiLagProbeCommand {
		os.Exit(graphics.RunAntiLagProbeMain())
	}
	logging.Init()
	logging.Logger(logging.CatRuntime).Info("tipsy-gui startup",
		"version", version.String(),
		"channel", version.Channel,
		"provenance", version.Provenance(),
		"sourceCommit", version.SourceCommit)
	if !version.IsOfficialBuild() {
		fmt.Fprintln(os.Stderr, "tipsy-gui: "+version.UnofficialBuildLabel)
	}
	stop, err := perf.StartFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tipsy-gui:", err)
		os.Exit(2)
	}
	defer stop()

	mode, uri, qtArgs := splitGUIArgs(os.Args)
	app := qt.NewQApplication(qtArgs)
	defer app.Delete()
	qt.QCoreApplication_SetOrganizationName("tipsy-linux")
	qt.QCoreApplication_SetOrganizationDomain("tipsy-linux.github.io")
	qt.QCoreApplication_SetApplicationName("tipsy-gui")
	qt.QCoreApplication_SetApplicationVersion(version.String())
	qt.QGuiApplication_SetApplicationDisplayName(guiTitle("Tipsy"))
	qt.QGuiApplication_SetDesktopFileName("io.github.tipsy_linux.Tipsy.Settings")
	qt.QGuiApplication_SetQuitOnLastWindowClosed(true)
	qt.QApplication_SetStyleWithStyle("Fusion")

	icon := brandIcon()
	qt.QGuiApplication_SetWindowIcon(icon)

	service := newProductionService()
	win := newWindowBase(service, icon)
	if mode == guiModePlay || uri != "" {
		win.startExternalInitialization(uri)
	} else {
		win.startSettingsInitialization()
	}
	qt.QApplication_Exec()
}

// guiTitle marks window chrome of unofficial builds so a local or modified
// binary cannot present the plain official Tipsy title. Official releases
// keep their titles unchanged.
func guiTitle(title string) string {
	if version.IsOfficialBuild() {
		return title
	}
	return title + " (" + version.UnofficialBuildLabel + ")"
}
