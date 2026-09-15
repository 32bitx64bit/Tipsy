// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
)

func TestPrepareFastFlagsForSave(t *testing.T) {
	t.Parallel()

	want := []clientsettings.FastFlag{
		{Name: "FFlagFirst", Value: "True"},
		{Name: "DFIntSecond", Value: " 37 "},
	}
	got, err := prepareFastFlagsForSave([]clientsettings.FastFlag{
		{Name: " FFlagFirst ", Value: "True"},
		{Name: "DFIntSecond", Value: " 37 "},
	})
	if err != nil {
		t.Fatalf("prepare valid flags: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepared flags=%+v want=%+v", got, want)
	}

	for _, tc := range []struct {
		name  string
		flags []clientsettings.FastFlag
		want  string
	}{
		{name: "missing name", flags: []clientsettings.FastFlag{{Value: "True"}}, want: "name"},
		{name: "missing value", flags: []clientsettings.FastFlag{{Name: "FFlagFirst"}}, want: "value"},
		{name: "duplicate name after trimming", flags: []clientsettings.FastFlag{{Name: "FFlagFirst", Value: "True"}, {Name: " FFlagFirst ", Value: "False"}}, want: "only once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepareFastFlagsForSave(tc.flags); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("prepare(%+v) error=%v, want %q", tc.flags, err, tc.want)
			}
		})
	}
}

func TestFastFlagEditorUsesDedicatedRowsAndExplicitConfirmation(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	app := qt.NewQApplication([]string{"tipsy-fast-flags-test"})
	defer app.Delete()
	qt.QApplication_SetStyleWithStyle("Fusion")
	app.SetStyleSheet(appStyleSheet)

	var saved []clientsettings.FastFlag
	editor := newFastFlagEditor(nil, []clientsettings.FastFlag{{Name: "FFlagFirst", Value: "True"}}, func(_ context.Context, flags []clientsettings.FastFlag) error {
		saved = append([]clientsettings.FastFlag(nil), flags...)
		return nil
	})
	defer editor.dialog.Delete()
	if len(editor.rows) != 1 || editor.rows[0].container == nil {
		t.Fatalf("initial editor rows=%+v", editor.rows)
	}
	editor.addRow(clientsettings.FastFlag{Name: "DFIntSecond", Value: "37"})
	if len(editor.rows) != 2 || editor.rows[0].container.UnsafePointer() == editor.rows[1].container.UnsafePointer() {
		t.Fatalf("entries did not receive dedicated containers: %+v", editor.rows)
	}
	if editor.rows[1].name.Text() != "DFIntSecond" || editor.rows[1].value.Text() != "37" {
		t.Fatalf("second editor row=%q=%q", editor.rows[1].name.Text(), editor.rows[1].value.Text())
	}
	editor.addRow(clientsettings.FastFlag{Name: "FFlagDebugGraphicsPreferOpenGL", Value: "True"})
	if notice := editor.notice.Text(); !strings.Contains(notice, "FFlagDebugGraphicsPreferOpenGL") || !strings.Contains(notice, "Renderer") {
		t.Fatalf("custom-override warning=%q", notice)
	}
	if len(saved) != 0 {
		t.Fatalf("editor saved before confirmation: %+v", saved)
	}
	editor.confirmSave()
	qt.QCoreApplication_ProcessEvents()
	want := []clientsettings.FastFlag{{Name: "FFlagFirst", Value: "True"}, {Name: "DFIntSecond", Value: "37"}, {Name: "FFlagDebugGraphicsPreferOpenGL", Value: "True"}}
	if !reflect.DeepEqual(saved, want) || editor.dialog.Result() != int(qt.QDialog__Accepted) {
		t.Fatalf("confirmation saved=%+v result=%d want=%+v", saved, editor.dialog.Result(), want)
	}
}
