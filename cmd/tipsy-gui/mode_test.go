package main

import (
	"reflect"
	"testing"
)

func TestSplitGUIArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		mode string
		uri  string
		qt   []string
	}{
		{name: "default settings", in: []string{"tipsy-gui"}, mode: guiModeSettings, qt: []string{"tipsy-gui"}},
		{name: "play", in: []string{"tipsy-gui", "--play"}, mode: guiModePlay, qt: []string{"tipsy-gui"}},
		{name: "settings flag", in: []string{"tipsy-gui", "--settings"}, mode: guiModeSettings, qt: []string{"tipsy-gui"}},
		{name: "play then qt arg", in: []string{"tipsy-gui", "--play", "-platform", "offscreen"}, mode: guiModePlay, qt: []string{"tipsy-gui", "-platform", "offscreen"}},
		{name: "last mode wins", in: []string{"tipsy-gui", "--play", "--settings"}, mode: guiModeSettings, qt: []string{"tipsy-gui"}},
		{name: "website uri forces play", in: []string{"tipsy-gui", "roblox://experiences/start?placeId=1818", "-platform", "offscreen"}, mode: guiModePlay, uri: "roblox://experiences/start?placeId=1818", qt: []string{"tipsy-gui", "-platform", "offscreen"}},
		{name: "settings plus website uri still plays", in: []string{"tipsy-gui", "--settings", "roblox://experiences/start?placeId=1818"}, mode: guiModePlay, uri: "roblox://experiences/start?placeId=1818", qt: []string{"tipsy-gui"}},
		{name: "empty", in: nil, mode: guiModeSettings, qt: nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mode, uri, qtArgs := splitGUIArgs(test.in)
			if mode != test.mode {
				t.Fatalf("mode=%q, want %q", mode, test.mode)
			}
			if uri != test.uri {
				t.Fatalf("uri=%q, want %q", uri, test.uri)
			}
			if !reflect.DeepEqual(qtArgs, test.qt) {
				t.Fatalf("qt args=%q, want %q", qtArgs, test.qt)
			}
		})
	}
}
