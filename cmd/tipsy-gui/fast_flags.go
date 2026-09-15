// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
)

// fastFlagSettingsStore is deliberately separate from the ordinary Settings
// model. Fast Flags are an explicit advanced override surface; opening or
// cancelling this dialog must not modify the graphics/settings draft.
type fastFlagSettingsStore interface {
	LoadFastFlags(context.Context) ([]clientsettings.FastFlag, error)
	SaveFastFlags(context.Context, []clientsettings.FastFlag) error
}

// ownerNotifyingService keeps the optional advanced-settings boundary visible
// through the GUI's existing service wrapper without expanding guimodel.Service.
func (s ownerNotifyingService) LoadFastFlags(ctx context.Context) ([]clientsettings.FastFlag, error) {
	store, ok := s.Service.(fastFlagSettingsStore)
	if !ok {
		return nil, errors.New("custom Fast Flags are unavailable in this build")
	}
	return store.LoadFastFlags(ctx)
}

func (s ownerNotifyingService) SaveFastFlags(ctx context.Context, flags []clientsettings.FastFlag) error {
	store, ok := s.Service.(fastFlagSettingsStore)
	if !ok {
		return errors.New("custom Fast Flags are unavailable in this build")
	}
	return store.SaveFastFlags(ctx, flags)
}

// LoadFastFlags and SaveFastFlags are thin presentation adapters. The shared
// client-settings service owns validation, locking, atomic persistence, and
// launch-time override policy.
func (s *productionService) LoadFastFlags(ctx context.Context) ([]clientsettings.FastFlag, error) {
	if s == nil || s.settings == nil {
		return nil, errors.New("custom Fast Flag settings service is unavailable")
	}
	return s.settings.LoadFastFlags(ctx)
}

func (s *productionService) SaveFastFlags(ctx context.Context, flags []clientsettings.FastFlag) error {
	if s == nil || s.settings == nil {
		return errors.New("custom Fast Flag settings service is unavailable")
	}
	return s.settings.SaveFastFlags(ctx, flags)
}

type fastFlagEditorRow struct {
	container *qt.QFrame
	name      *qt.QLineEdit
	value     *qt.QLineEdit
}

type fastFlagEditor struct {
	dialog  *qt.QDialog
	rows    []*fastFlagEditorRow
	rowList *qt.QVBoxLayout
	notice  *qt.QLabel
	add     *qt.QPushButton
	confirm *qt.QPushButton
	save    func(context.Context, []clientsettings.FastFlag) error
}

func (w *mainWindow) showFastFlagEditor() {
	store, ok := w.service.(fastFlagSettingsStore)
	if !ok {
		qt.QMessageBox_Warning(w.win.QWidget, "Custom Fast Flags unavailable", "This build cannot open the custom Fast Flag editor.")
		return
	}
	flags, err := store.LoadFastFlags(context.Background())
	if err != nil {
		// Do not echo a filesystem or validation error in a UI that contains
		// user-entered settings. The backing service keeps the detailed local
		// diagnostic; this surface needs only an actionable, content-free error.
		qt.QMessageBox_Warning(w.win.QWidget, "Could not load custom Fast Flags", "Custom Fast Flags could not be loaded. Check that Tipsy's settings folder is writable, then try again.")
		return
	}
	editor := newFastFlagEditor(w.win.QWidget, flags, store.SaveFastFlags)
	editor.dialog.Exec()
	editor.dialog.Delete()
}

func newFastFlagEditor(parent *qt.QWidget, flags []clientsettings.FastFlag, save func(context.Context, []clientsettings.FastFlag) error) *fastFlagEditor {
	editor := &fastFlagEditor{
		dialog: qt.NewQDialog(parent),
		save:   save,
	}
	editor.dialog.SetWindowTitle("Custom Fast Flags")
	editor.dialog.SetMinimumSize2(520, 420)
	editor.dialog.Resize(680, 620)
	editor.dialog.SetModal(true)

	layout := qt.NewQVBoxLayout(editor.dialog.QWidget)
	layout.SetContentsMargins(28, 24, 28, 24)
	layout.SetSpacing(14)
	title := qt.NewQLabel3("Custom Fast Flags")
	setObjectName(title.QObject, "pageTitle")
	title.SetAccessibleName("Custom Fast Flags editor")
	layout.AddWidget(title.QWidget)
	intro := qt.NewQLabel3("Add one flag and value per entry. Changes are saved only when you choose Confirm and save, then take effect after Roblox is restarted.")
	intro.SetWordWrap(true)
	setObjectName(intro.QObject, "bodyText")
	layout.AddWidget(intro.QWidget)

	editor.notice = qt.NewQLabel2()
	editor.notice.SetWordWrap(true)
	editor.notice.SetAccessibleName("Fast Flag validation and conflict notice")
	layout.AddWidget(editor.notice.QWidget)

	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	scroll.SetFrameShape(qt.QFrame__NoFrame)
	scroll.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	listContent := qt.NewQWidget2()
	editor.rowList = qt.NewQVBoxLayout(listContent)
	editor.rowList.SetContentsMargins(0, 0, 0, 0)
	editor.rowList.SetSpacing(10)
	editor.rowList.AddStretch()
	scroll.SetWidget(listContent)
	layout.AddWidget2(scroll.QWidget, 1)

	addRow := qt.NewQHBoxLayout2()
	editor.add = qt.NewQPushButton3("Add Fast Flag")
	setObjectName(editor.add.QObject, "secondaryButton")
	editor.add.SetAccessibleName("Add Fast Flag entry")
	editor.add.SetAccessibleDescription("Add a separate Fast Flag and value entry")
	editor.add.OnClicked(func() { editor.addRow(clientsettings.FastFlag{}) })
	addRow.AddWidget(editor.add.QWidget)
	addRow.AddStretch()
	layout.AddLayout(addRow.QLayout)

	actions := qt.NewQHBoxLayout2()
	actions.AddStretch()
	cancel := qt.NewQPushButton3("Cancel")
	setObjectName(cancel.QObject, "secondaryButton")
	cancel.SetAccessibleName("Cancel custom Fast Flag changes")
	cancel.OnClicked(editor.dialog.Reject)
	actions.AddWidget(cancel.QWidget)
	editor.confirm = qt.NewQPushButton3("Confirm and save")
	setObjectName(editor.confirm.QObject, "primaryButton")
	editor.confirm.SetDefault(true)
	editor.confirm.SetAccessibleName("Confirm and save custom Fast Flags")
	editor.confirm.SetAccessibleDescription("Validate and save every custom Fast Flag entry. Roblox must be restarted for changes to apply.")
	editor.confirm.OnClicked(editor.confirmSave)
	actions.AddWidget(editor.confirm.QWidget)
	layout.AddLayout(actions.QLayout)

	for _, flag := range flags {
		editor.addRow(flag)
	}
	editor.refreshNotice()
	return editor
}

func (editor *fastFlagEditor) addRow(flag clientsettings.FastFlag) {
	row := &fastFlagEditorRow{}
	container, content := newVerticalCard("subtleCard")
	row.container = container
	row.container.SetAccessibleName("Custom Fast Flag entry")

	heading := qt.NewQHBoxLayout2()
	heading.AddWidget(sectionLabel("Fast Flag").QWidget)
	heading.AddStretch()
	remove := qt.NewQPushButton3("Remove")
	setObjectName(remove.QObject, "secondaryButton")
	remove.SetAccessibleName("Remove Fast Flag entry")
	remove.OnClicked(func() { editor.removeRow(row) })
	heading.AddWidget(remove.QWidget)
	content.AddLayout(heading.QLayout)

	form := qt.NewQFormLayout2()
	form.SetHorizontalSpacing(20)
	form.SetVerticalSpacing(10)
	form.SetRowWrapPolicy(qt.QFormLayout__WrapLongRows)
	form.SetFieldGrowthPolicy(qt.QFormLayout__AllNonFixedFieldsGrow)
	row.name = qt.NewQLineEdit3(flag.Name)
	row.name.SetPlaceholderText("FFlagExample")
	row.name.SetClearButtonEnabled(true)
	row.name.SetAccessibleName("Fast Flag name")
	row.name.SetAccessibleDescription("The custom Fast Flag identifier")
	row.name.OnTextChanged(func(string) { editor.refreshNotice() })
	form.AddRow3("Flag", row.name.QWidget)
	row.value = qt.NewQLineEdit3(flag.Value)
	row.value.SetPlaceholderText("True, False, a number, or text")
	row.value.SetClearButtonEnabled(true)
	row.value.SetAccessibleName("Fast Flag value")
	row.value.SetAccessibleDescription("The value for this custom Fast Flag")
	row.value.OnTextChanged(func(string) { editor.refreshNotice() })
	form.AddRow3("Value", row.value.QWidget)
	content.AddLayout(form.QLayout)

	editor.rows = append(editor.rows, row)
	// Keep the stretch at the bottom so every entry remains its own visible
	// card instead of spreading out as entries are added or removed.
	editor.rowList.InsertWidget(editor.rowList.Count()-1, row.container.QWidget)
	editor.refreshNotice()
}

func (editor *fastFlagEditor) removeRow(row *fastFlagEditorRow) {
	if row == nil {
		return
	}
	for i, candidate := range editor.rows {
		if candidate != row {
			continue
		}
		editor.rowList.RemoveWidget(row.container.QWidget)
		row.container.Delete()
		editor.rows = append(editor.rows[:i], editor.rows[i+1:]...)
		break
	}
	editor.refreshNotice()
}

func (editor *fastFlagEditor) entries() []clientsettings.FastFlag {
	flags := make([]clientsettings.FastFlag, 0, len(editor.rows))
	for _, row := range editor.rows {
		flags = append(flags, clientsettings.FastFlag{Name: row.name.Text(), Value: row.value.Text()})
	}
	return flags
}

func prepareFastFlagsForSave(flags []clientsettings.FastFlag) ([]clientsettings.FastFlag, error) {
	prepared := make([]clientsettings.FastFlag, 0, len(flags))
	seen := make(map[string]struct{}, len(flags))
	for _, flag := range flags {
		name := strings.TrimSpace(flag.Name)
		if name == "" {
			return nil, errors.New("enter a Fast Flag name for every entry")
		}
		if strings.TrimSpace(flag.Value) == "" {
			return nil, errors.New("enter a Fast Flag value for every entry")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("each Fast Flag name can appear only once")
		}
		seen[name] = struct{}{}
		// Values remain byte-for-byte as entered. This is important for string
		// flags; the shared service validates their printable, bounded shape.
		prepared = append(prepared, clientsettings.FastFlag{Name: name, Value: flag.Value})
	}
	return prepared, nil
}

func (editor *fastFlagEditor) refreshNotice() {
	if editor.notice == nil {
		return
	}
	flags, err := prepareFastFlagsForSave(editor.entries())
	switch {
	case err != nil:
		editor.notice.SetText(err.Error())
		setObjectName(editor.notice.QObject, "noticeWarning")
	case clientsettings.ValidateFastFlags(flags) != nil:
		editor.notice.SetText("Invalid custom Fast Flag entry. Use True or False for FFlag/DFFlag, a signed 32-bit integer for FInt/DFInt, or nonempty printable text for FString/DFString.")
		setObjectName(editor.notice.QObject, "noticeWarning")
	case len(clientsettings.FastFlagConflicts(flags)) > 0:
		conflicts := clientsettings.FastFlagConflicts(flags)
		parts := make([]string, 0, len(conflicts))
		for _, conflict := range conflicts {
			parts = append(parts, fmt.Sprintf("%s overrides the normal %s setting", conflict.Name, conflict.Setting))
		}
		editor.notice.SetText("Warning: custom Fast Flags take precedence. " + strings.Join(parts, "; ") + ".")
		setObjectName(editor.notice.QObject, "noticeWarning")
	case len(flags) == 0:
		editor.notice.SetText("No custom Fast Flags are configured. Confirm and save clears any existing custom entries.")
		setObjectName(editor.notice.QObject, "noticeInfo")
	default:
		editor.notice.SetText("Custom Fast Flags take precedence over matching normal Settings controls. Confirm and save to apply these entries on the next Roblox restart.")
		setObjectName(editor.notice.QObject, "noticeInfo")
	}
	refreshStyle(editor.notice.QWidget)
}

func (editor *fastFlagEditor) confirmSave() {
	flags, err := prepareFastFlagsForSave(editor.entries())
	if err != nil {
		editor.refreshNotice()
		return
	}
	if err := clientsettings.ValidateFastFlags(flags); err != nil {
		editor.refreshNotice()
		return
	}
	if editor.save == nil || editor.save(context.Background(), flags) != nil {
		// Save failures can include paths and implementation details. Keep user
		// input opaque on this surface while preserving the entries for retry.
		editor.notice.SetText("Could not save custom Fast Flags. Review the entries and check that Tipsy's settings folder is writable, then try again.")
		setObjectName(editor.notice.QObject, "noticeError")
		refreshStyle(editor.notice.QWidget)
		return
	}
	editor.dialog.Accept()
}
