package main

import (
	"os"
	"path/filepath"
	"runtime"

	qt "github.com/mappu/miqt/qt6"
)

const appStyleSheet = `
* {
  font-family: "Inter", "Noto Sans", "DejaVu Sans", sans-serif;
  font-size: 14px;
  color: #15213b;
}
QMainWindow, QDialog, QWizard, QWidget#appRoot, QWidget#contentStack {
  background: #f3f6fb;
}
QWizard > QWidget { background: #f3f6fb; }
QWidget#wizardPage, QScrollArea#wizardPageScroll,
QScrollArea#wizardPageScroll > QWidget,
QScrollArea#wizardPageScroll > QWidget > QWidget,
QWidget#wizardPageContent { background: #f3f6fb; }
QMenuBar, QMenu { background: #ffffff; color: #15213b; }
QMenuBar::item:selected, QMenu::item:selected { background: #e9f1ff; color: #0f62e7; }
QWidget#sidebar { background: #091329; border-right: 1px solid #172442; }
QWidget#wizardSide { background: #091329; }
QLabel#wordmark { color: #ffffff; font-size: 27px; font-weight: 800; }
QLabel#eyebrowOnDark { color: #dff515; font-size: 10px; font-weight: 800; letter-spacing: 2px; }
QLabel#sidebarNote { color: #c8d2e4; font-size: 11px; }
QLabel#sidebarVersion { color: #9eabc2; font-size: 11px; }
QLabel#wizardSideTitle { color: #ffffff; font-size: 24px; font-weight: 800; }
QLabel#wizardSideFoot { color: #8e9bb2; font-size: 11px; }
QLabel#wizardPageTitle { color: #101b33; font-size: 26px; font-weight: 800; }
QLabel#wizardPageSubtitle { color: #66758f; font-size: 14px; }
QLabel#wizardStep, QLabel#wizardStepDone, QLabel#wizardStepActive {
  color: #9eabc2; font-size: 13px; font-weight: 650; padding: 8px 10px;
  border: 1px solid transparent; border-radius: 8px;
}
QLabel#wizardStepDone { color: #c8d2e4; }
QLabel#wizardStepActive {
  color: #ffffff; background: #18325f; border-left: 3px solid #dff515; padding-left: 8px;
}
QPushButton#navButton {
  color: #bec8da; text-align: left; font-size: 14px; font-weight: 650;
  padding: 13px 14px; border: 1px solid transparent; border-radius: 10px; background: transparent;
}
QPushButton#navButton:hover { background: #111f3a; color: #ffffff; }
QPushButton#navButton:focus { border: 2px solid #78aaf7; padding: 12px 13px; color: #ffffff; }
QPushButton#navButton:checked {
  background: #18325f; color: #ffffff; border-left: 4px solid #dff515; padding-left: 11px;
}
QPushButton#navButton:checked:focus { border: 2px solid #78aaf7; border-left: 4px solid #dff515; padding-left: 11px; }
QScrollArea { border: 0; background: transparent; }
QScrollArea > QWidget > QWidget { background: transparent; }
QScrollBar:vertical {
  background: transparent; width: 10px; margin: 3px 2px 3px 0;
}
QScrollBar::handle:vertical { background: #aebbd0; min-height: 38px; border-radius: 4px; }
QScrollBar::handle:vertical:hover { background: #8293ae; }
QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical,
QScrollBar::add-page:vertical, QScrollBar::sub-page:vertical { height: 0; background: transparent; }
QScrollBar:horizontal { background: transparent; height: 10px; margin: 2px; }
QScrollBar::handle:horizontal { background: #aebbd0; min-width: 38px; border-radius: 4px; }
QScrollBar::add-line:horizontal, QScrollBar::sub-line:horizontal,
QScrollBar::add-page:horizontal, QScrollBar::sub-page:horizontal { width: 0; background: transparent; }
QLabel#pageTitle { color: #101b33; font-size: 30px; font-weight: 800; }
QLabel#pageSubtitle { color: #66758f; font-size: 14px; }
QFrame#card, QFrame#subtleCard, QFrame#heroCard {
  background: #ffffff; border: 1px solid #dfe6f1; border-radius: 15px;
}
QFrame#subtleCard { background: #edf3fb; border-color: #d9e3f1; }
QFrame#heroCard {
  background: #12366c; border: 1px solid #1f5198;
}
QLabel#heroEyebrow { color: #dff515; font-size: 11px; font-weight: 800; letter-spacing: 2px; }
QLabel#heroTitle { color: #ffffff; font-size: 29px; font-weight: 850; }
QLabel#heroBody { color: #d6e2f5; font-size: 13px; }
QLabel#sectionLabel { color: #12203a; font-size: 16px; font-weight: 750; }
QLabel#metricValue { color: #0e1d38; font-size: 27px; font-weight: 800; }
QLabel#metricValueSmall { color: #0e1d38; font-size: 20px; font-weight: 750; }
QLabel#bodyText { color: #34415c; }
QLabel#mutedText, QLabel#pathValue { color: #66758f; }
QLabel#formLabel { color: #34415c; font-weight: 700; }
QLabel#sourceRow {
  background: #f7f9fd; color: #34415c; border: 1px solid #dfe6f1; border-radius: 10px; padding: 14px;
}
QLabel#wizardLead { color: #24324d; font-size: 16px; }
QLabel#wizardInfoCard, QLabel#wizardCheck {
  background: #ffffff; color: #34415c; border: 1px solid #dfe6f1; border-radius: 10px; padding: 14px;
}
QLabel#wizardOptionDetail { color: #66758f; margin-left: 24px; }
QLabel#wizardProgressTitle { color: #10203f; font-size: 22px; font-weight: 800; }
QLabel#statusReady, QLabel#statusWarning, QLabel#statusNeutral {
  border-radius: 8px; padding: 5px 9px; font-size: 10px; font-weight: 800;
}
QLabel#statusReady { background: #def7e9; color: #137353; }
QLabel#statusWarning { background: #fff0c9; color: #805800; }
QLabel#statusNeutral { background: #e8edf5; color: #576681; }
QLabel#noticeInfo, QLabel#noticeSuccess, QLabel#noticeWarning, QLabel#noticeError {
  border-radius: 9px; padding: 11px 13px;
}
QLabel#noticeInfo { background: #e8f1ff; color: #1858a8; }
QLabel#noticeSuccess { background: #def7e9; color: #126647; }
QLabel#noticeWarning { background: #fff2d1; color: #805800; }
QLabel#noticeError { background: #ffe3ea; color: #a52647; }
QPushButton { min-height: 20px; border-radius: 9px; padding: 9px 16px; font-weight: 700; }
QPushButton:focus { border: 2px solid #126cf3; padding: 8px 15px; }
QPushButton#primaryButton, QPushButton#playButton {
  color: #ffffff; background: #126cf3; border: 1px solid #126cf3;
}
QPushButton#playButton {
  color: #0b1730; background: #dff515; border-color: #dff515; font-size: 15px; padding: 12px 22px;
}
QPushButton#primaryButton:hover { background: #0f5fd7; }
QPushButton#playButton:hover { background: #efff58; }
QPushButton#secondaryButton { color: #174a94; background: #eef4fd; border: 1px solid #cdddf3; }
QPushButton#secondaryButton:hover { background: #dfeafa; }
QPushButton#primaryButton:focus, QPushButton#secondaryButton:focus, QPushButton#playButton:focus {
  border: 2px solid #092c65; padding: 8px 15px;
}
QPushButton#playButton:focus { border-color: #ffffff; }
QPushButton:disabled { color: #8792a6; background: #e7ebf1; border-color: #d8dee8; }
QPushButton#primaryButton:disabled, QPushButton#playButton:disabled {
  color: #8792a6; background: #e7ebf1; border-color: #d8dee8;
}
QComboBox, QSpinBox, QLineEdit, QPlainTextEdit {
  background: #ffffff; color: #15213b; border: 1px solid #cad5e5; border-radius: 8px;
  padding: 8px 10px; selection-background-color: #126cf3;
}
QComboBox:focus, QSpinBox:focus, QLineEdit:focus, QPlainTextEdit:focus { border: 2px solid #126cf3; }
QComboBox:disabled, QSpinBox:disabled, QLineEdit:disabled, QPlainTextEdit:disabled {
  background: #edf1f6; color: #7b879c; border-color: #d8dee8;
}
QComboBox QAbstractItemView { background: #ffffff; border: 1px solid #cad5e5; selection-background-color: #e8f1ff; selection-color: #10203f; }
QComboBox QAbstractItemView::item { min-height: 32px; padding: 4px 8px; }
QComboBox QAbstractItemView::item:disabled { color: #8b96a9; background: #f3f5f8; }
QRadioButton { spacing: 9px; padding: 5px 7px; border: 2px solid transparent; border-radius: 7px; }
QRadioButton:hover:enabled { background: #eaf2ff; }
QRadioButton:focus { border-color: #126cf3; background: #eaf2ff; }
QRadioButton:disabled { color: #8792a6; }
QRadioButton:checked { color: #0f5bb8; background: #e8f1ff; border-color: #b9d4fa; font-weight: 700; }
QProgressBar {
  min-height: 12px; border: 0; border-radius: 6px; background: #dfe6f1;
  text-align: center; color: transparent;
}
QProgressBar::chunk { border-radius: 6px; background: #126cf3; }
QStatusBar { background: #ffffff; color: #66758f; border-top: 1px solid #dfe6f1; }
QStatusBar::item { border: 0; }
QToolTip { background: #101b33; color: #ffffff; border: 0; padding: 6px; }
`

func brandIcon() *qt.QIcon {
	if path := brandIconPath(); path != "" {
		return qt.NewQIcon4(path)
	}
	return qt.QIcon_FromTheme("applications-games")
}

func brandIconPath() string {
	var candidates []string
	if override := os.Getenv("TIPSY_ICON_PATH"); override != "" {
		candidates = append(candidates, override)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "tipsy.png"),
			filepath.Join(dir, "..", "share", "icons", "hicolor", "512x512", "apps", "tipsy.png"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "tipsy.png"))
	}
	if _, sourceFile, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(sourceFile), "..", "..", "tipsy.png"))
	}
	candidates = append(candidates,
		"/usr/local/share/icons/hicolor/512x512/apps/tipsy.png",
		"/usr/share/icons/hicolor/512x512/apps/tipsy.png",
	)
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func setObjectName(object *qt.QObject, name string) {
	value := qt.NewQAnyStringView3(name)
	object.SetObjectName(*value)
	value.Delete()
}

func refreshStyle(widget *qt.QWidget) {
	style := widget.Style()
	style.Unpolish(widget)
	style.Polish(widget)
	widget.Update()
}

func newPage(title, subtitle string) (*qt.QWidget, *qt.QVBoxLayout, *qt.QScrollArea) {
	content := qt.NewQWidget2()
	setObjectName(content.QObject, "pageContent")
	content.SetMaximumWidth(1160)
	layout := qt.NewQVBoxLayout(content)
	layout.SetContentsMargins(30, 30, 30, 34)
	layout.SetSpacing(16)

	titleLabel := qt.NewQLabel3(title)
	setObjectName(titleLabel.QObject, "pageTitle")
	layout.AddWidget(titleLabel.QWidget)
	subtitleLabel := qt.NewQLabel3(subtitle)
	setObjectName(subtitleLabel.QObject, "pageSubtitle")
	subtitleLabel.SetWordWrap(true)
	layout.AddWidget(subtitleLabel.QWidget)
	layout.AddSpacing(4)

	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	scroll.SetFrameShape(qt.QFrame__NoFrame)
	scroll.SetFocusPolicy(qt.NoFocus)
	scroll.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	scroll.SetAlignment(qt.AlignHCenter | qt.AlignTop)
	scroll.SetWidget(content)
	return scroll.QWidget, layout, scroll
}

func newCard(name string) (*qt.QFrame, *qt.QHBoxLayout) {
	card := qt.NewQFrame2()
	setObjectName(card.QObject, name)
	layout := qt.NewQHBoxLayout(card.QWidget)
	layout.SetContentsMargins(22, 21, 22, 21)
	layout.SetSpacing(16)
	return card, layout
}

func newVerticalCard(name string) (*qt.QFrame, *qt.QVBoxLayout) {
	card := qt.NewQFrame2()
	setObjectName(card.QObject, name)
	layout := qt.NewQVBoxLayout(card.QWidget)
	layout.SetContentsMargins(22, 21, 22, 21)
	layout.SetSpacing(12)
	return card, layout
}

func sectionLabel(text string) *qt.QLabel {
	label := qt.NewQLabel3(text)
	setObjectName(label.QObject, "sectionLabel")
	return label
}

func applyMonoFont(edit *qt.QPlainTextEdit) {
	font := qt.NewQFont()
	font.SetStyleHint(qt.QFont__TypeWriter)
	font.SetFixedPitch(true)
	edit.SetFont(font)
}
