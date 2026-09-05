package main

import "github.com/tipsy-linux/tipsy/internal/rbxuri"

const (
	guiModeSettings = "settings"
	guiModePlay     = "play"
)

func splitGUIArgs(args []string) (mode, uri string, qtArgs []string) {
	mode = guiModeSettings
	if len(args) == 0 {
		return mode, "", args
	}
	qtArgs = []string{args[0]}
	for _, arg := range args[1:] {
		switch {
		case arg == "--play":
			mode = guiModePlay
		case arg == "--settings":
			mode = guiModeSettings
		case rbxuri.LooksLike(arg):
			uri = arg
			mode = guiModePlay
		default:
			qtArgs = append(qtArgs, arg)
		}
	}
	return mode, uri, qtArgs
}
