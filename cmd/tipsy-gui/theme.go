package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	qt "github.com/mappu/miqt/qt6"
)

// A single semantic stylesheet covers pages, setup, menus and dialogs. Colors
// are resolved together so changing appearance never leaves a light-only control.
var appStyleSheet = themeStyleSheet(false)

const themeTemplate = `
* { font-family: "Inter", "Noto Sans", "DejaVu Sans", sans-serif; font-size: 14px; color: @text; }
QMainWindow, QDialog, QWizard, QWidget#appRoot, QWidget#contentStack,
QWidget#wizardPage, QWidget#wizardPageContent, QWizard > QWidget,
QScrollArea#wizardPageScroll > QWidget, QScrollArea#wizardPageScroll > QWidget > QWidget { background: @bg; }
QWidget#topNavigation, QWidget#appearanceBar { background: @surface; }
QWidget#topNavigation { border-bottom: 1px solid @border; }
QWidget#appearanceBar { border-top: 1px solid @border; }
QMenuBar, QMenu { background: @surface; color: @text; }
QMenuBar::item:selected, QMenu::item:selected { background: @selected; color: @link; }
QWidget#wizardSide { background: @surface; border-right: 1px solid @border; }
QLabel#wordmark { font-size: 26px; font-weight: 800; }
QLabel#eyebrowOnDark, QLabel#sidebarNote, QLabel#sidebarVersion, QLabel#wizardSideFoot { color: @muted; font-size: 11px; }
QLabel#wizardSideTitle { font-size: 23px; font-weight: 750; }
QLabel#wizardStep, QLabel#wizardStepDone, QLabel#wizardStepActive { color: @muted; padding: 8px 10px; border: 1px solid transparent; border-radius: 6px; }
QLabel#wizardStepDone { color: @text; }
QLabel#wizardStepActive { color: @link; background: @selected; border-left: 3px solid @accent; }
QPushButton { min-height: 20px; border: 1px solid @border; border-radius: 6px; padding: 8px 14px; background: @surface; font-weight: 600; }
QPushButton:hover { background: @hover; }
QPushButton:focus { border: 2px solid @focus; padding: 7px 13px; }
QPushButton#navButton { border: 0; border-bottom: 3px solid transparent; border-radius: 0; padding: 17px 13px 14px; background: transparent; font-weight: 500; }
QPushButton#navButton:hover { background: @hover; }
QPushButton#navButton:checked { border-bottom: 3px solid @accent; color: @text; font-weight: 650; }
QPushButton#navButton:focus { background: @selected; outline: none; border-bottom: 3px solid @focus; }
QPushButton#appearanceButton { border-radius: 4px; min-width: 56px; padding: 7px 10px; font-weight: 500; }
QPushButton#appearanceButton:checked { background: @selected; color: @link; border-color: @accent; }
QPushButton#primaryButton, QPushButton#playButton { background: @accent; border-color: @accent; color: #ffffff; }
QPushButton#primaryButton:hover, QPushButton#playButton:hover { background: #215de1; border-color: #215de1; }
QPushButton#primaryButton:focus, QPushButton#playButton:focus { border: 2px solid @focus; }
QPushButton#playButton { font-size: 20px; font-weight: 600; padding: 13px 22px; }
QPushButton#playButton:focus { padding: 12px 21px; }
QPushButton#secondaryButton { background: @surface; }
QPushButton#linkButton { color: @link; border: 1px solid transparent; background: transparent; padding: 5px 9px; }
QPushButton#linkButton:focus { border-color: @focus; }
QPushButton:disabled, QPushButton#primaryButton:disabled, QPushButton#playButton:disabled { color: @disabled; background: @hover; border-color: @border; }
QScrollArea { border: 0; background: transparent; }
QScrollArea > QWidget > QWidget { background: transparent; }
QScrollBar:vertical { background: transparent; width: 10px; margin: 3px 2px; }
QScrollBar:horizontal { background: transparent; height: 10px; margin: 2px; }
QScrollBar::handle:vertical, QScrollBar::handle:horizontal { background: @disabled; min-height: 36px; min-width: 36px; border-radius: 4px; }
QScrollBar::add-line, QScrollBar::sub-line, QScrollBar::add-page, QScrollBar::sub-page { width: 0; height: 0; background: transparent; }
QLabel#pageTitle, QLabel#wizardPageTitle { font-size: 28px; font-weight: 750; }
QLabel#progressTitle { font-size: 23px; font-weight: 750; }
QLabel#launchTitle { font-size: 32px; font-weight: 800; }
QLabel#launchSubtitle { font-size: 18px; color: @muted; }
QLabel#pageSubtitle, QLabel#wizardPageSubtitle, QLabel#mutedText, QLabel#pathValue, QLabel#wizardOptionDetail { color: @muted; }
QLabel#sectionLabel { font-size: 16px; font-weight: 650; }
QLabel#metricValue { font-size: 26px; font-weight: 750; }
QLabel#metricValueSmall, QLabel#wizardProgressTitle { font-size: 20px; font-weight: 650; }
QLabel#formLabel { font-weight: 600; }
QLabel#wizardLead { font-size: 16px; }
QFrame#card, QFrame#subtleCard { background: @surface; border: 1px solid @border; border-radius: 8px; }
QFrame#preferenceRow { border: 0; border-top: 1px solid @border; }
QLabel#sourceRow, QLabel#wizardInfoCard, QLabel#wizardCheck { background: @surface; border: 1px solid @border; border-radius: 7px; padding: 14px; }
QLabel#statusReady, QLabel#statusWarning, QLabel#statusRejected, QLabel#statusNeutral { border-radius: 11px; padding: 4px 12px; font-size: 11px; font-weight: 600; }
QLabel#clientReadyIcon { background: @success; color: @bg; border-radius: 12px; font-weight: 700; }
QLabel#statusReady { background: @successBg; color: @success; border: 1px solid @success; }
QLabel#statusWarning { background: @warningBg; color: @warning; border: 1px solid @warning; }
QLabel#statusRejected { background: @errorBg; color: @error; border: 1px solid @error; }
QLabel#statusNeutral { background: @hover; color: @muted; border: 1px solid @border; }
QLabel#noticeInfo, QLabel#noticeSuccess, QLabel#noticeWarning, QLabel#noticeError { border-radius: 6px; padding: 10px 12px; }
QLabel#noticeInfo { background: @selected; color: @link; }
QLabel#noticeSuccess { background: @successBg; color: @success; }
QLabel#noticeWarning { background: @warningBg; color: @warning; }
QLabel#noticeError { background: @errorBg; color: @error; }
QComboBox, QSpinBox, QLineEdit, QPlainTextEdit { background: @surface; color: @text; border: 1px solid @border; border-radius: 6px; padding: 8px 10px; selection-background-color: @accent; selection-color: #ffffff; }
QComboBox:focus, QSpinBox:focus, QLineEdit:focus, QPlainTextEdit:focus { border: 2px solid @focus; }
QComboBox:disabled, QSpinBox:disabled, QLineEdit:disabled, QPlainTextEdit:disabled { background: @hover; color: @disabled; }
QComboBox QAbstractItemView { background: @surface; color: @text; border: 1px solid @border; selection-background-color: @selected; selection-color: @link; }
QComboBox QAbstractItemView::item { min-height: 30px; padding: 4px 8px; }
QComboBox QAbstractItemView::item:disabled { color: @disabled; }
QRadioButton { spacing: 9px; padding: 5px 7px; border: 2px solid transparent; border-radius: 6px; }
QRadioButton:focus { border-color: @focus; }
QRadioButton:checked { background: @selected; color: @link; border-color: @accent; }
QRadioButton:disabled { color: @disabled; }
QCheckBox#vsyncToggle { spacing: 10px; padding: 8px 10px; border: 2px solid transparent; border-radius: 6px; background: @surface; }
QCheckBox#vsyncToggle:hover:enabled { background: @hover; }
QCheckBox#vsyncToggle:focus { border-color: @focus; }
QCheckBox#vsyncToggle:checked { background: @selected; color: @link; border-color: @accent; }
QCheckBox#vsyncToggle:disabled { color: @disabled; background: @hover; }
QCheckBox#vsyncToggle:disabled:focus { border-color: @border; }
QCheckBox#vsyncToggle::indicator { width: 18px; height: 18px; border: 2px solid @muted; border-radius: 4px; background: @surface; }
QCheckBox#vsyncToggle::indicator:checked { background: @accent; border-color: @accent; }
QCheckBox#vsyncToggle::indicator:disabled { background: @hover; border-color: @disabled; }
QCheckBox#vsyncToggle::indicator:checked:disabled { background: @disabled; }
QProgressBar { min-height: 12px; border: 0; border-radius: 6px; background: @hover; text-align: center; color: transparent; }
QProgressBar::chunk { background: @accent; border-radius: 6px; }
QStatusBar { background: @surface; color: @muted; border-top: 1px solid @border; font-size: 11px; }
QStatusBar::item { border: 0; }
QToolTip { background: @text; color: @bg; border: 0; padding: 6px; }
`

func themeStyleSheet(dark bool) string {
	colors := []string{"@bg", "#fcfcfd", "@surface", "#ffffff", "@text", "#141820", "@muted", "#68717e", "@border", "#d9dde3", "@hover", "#eef0f4", "@selected", "#e5eeff", "@link", "#2466df", "@accent", "#2d6bff", "@focus", "#1b55c6", "@disabled", "#85909e", "@successBg", "#edf8f1", "@success", "#227d48", "@warningBg", "#fff4d8", "@warning", "#805c10", "@errorBg", "#ffebee", "@error", "#ac2447"}
	if dark {
		colors = []string{"@bg", "#1c1f24", "@surface", "#23272e", "@text", "#f3f5f7", "@muted", "#a8b1bf", "@border", "#424a56", "@hover", "#2d333d", "@selected", "#203b68", "@link", "#87b5ff", "@accent", "#2d6bff", "@focus", "#aacbff", "@disabled", "#8390a2", "@successBg", "#183528", "@success", "#7ade9d", "@warningBg", "#3d321b", "@warning", "#f0cb78", "@errorBg", "#40222c", "@error", "#ffa6bc"}
	}
	return strings.NewReplacer(colors...).Replace(themeTemplate)
}

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
