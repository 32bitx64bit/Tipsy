// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import qt "github.com/mappu/miqt/qt6"

// These setters make the hot state paths idempotent. Besides avoiding needless
// Qt layout/style work, they avoid re-emitting widget signals during hydration.
func (w *mainWindow) setLabelText(label *qt.QLabel, text string) bool {
	if label == nil || label.Text() == text {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	label.SetText(text)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setButtonText(button *qt.QPushButton, text string) bool {
	if button == nil || button.Text() == text {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	button.SetText(text)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setButtonEnabled(button *qt.QPushButton, enabled bool) bool {
	if button == nil || button.IsEnabled() == enabled {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	button.SetEnabled(enabled)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setCheckBoxChecked(box *qt.QCheckBox, checked bool) bool {
	if box == nil || box.IsChecked() == checked {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	box.SetChecked(checked)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setCheckBoxText(box *qt.QCheckBox, text string) bool {
	if box == nil || box.Text() == text {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	box.SetText(text)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setComboIndex(combo *qt.QComboBox, index int) bool {
	if combo == nil || combo.CurrentIndex() == index {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	combo.SetCurrentIndex(index)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setSpinValue(spin *qt.QSpinBox, value int) bool {
	if spin == nil || spin.Value() == value {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	spin.SetValue(value)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setWidgetEnabled(widget *qt.QWidget, enabled bool) bool {
	if widget == nil || widget.IsEnabled() == enabled {
		w.metrics.widgetSkipped.Add(1)
		return false
	}
	widget.SetEnabled(enabled)
	w.metrics.widgetWrites.Add(1)
	return true
}

func (w *mainWindow) setObjectName(object *qt.QObject, name string) bool {
	if object == nil || object.ObjectName() == name {
		return false
	}
	setObjectName(object, name)
	return true
}
