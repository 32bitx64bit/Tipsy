/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * X11-first WebKitGTK child overlay. The widget is reparented into the
 * Roblox client window (no WM toplevel, no taskbar). URLs, cookies, and
 * job ids never enter logs from this shim.
 */
#ifndef TIPSY_WEBVIEW_OVERLAY_H
#define TIPSY_WEBVIEW_OVERLAY_H

#ifdef __cplusplus
extern "C" {
#endif

int tipsy_webview_overlay_start(void);
int tipsy_webview_overlay_open(unsigned long parent, int width, int height,
	const char *url, const char *theme, const char *title, const char *assets, char **names,
	char **values, char **domains, char **paths, int *secure,
	int *http_only, int n_cookies);
void tipsy_webview_overlay_hide(void);
void tipsy_webview_overlay_close(void);
int tipsy_webview_overlay_visible(void);
/* Test synchronization and cursor-contract inspection on the GTK thread. */
int tipsy_webview_overlay_test_cursor(int kind);
int tipsy_webview_overlay_test_policy(const char *uri);

/* Implemented in Go (webview_overlay_linux.go). Returns 1 if the URI was
 * handled as a join or close command (navigation should be ignored). */
int tipsy_go_webview_policy(char *uri);

/* User dismissed the overlay (Back to Roblox / Escape). */
void tipsy_go_webview_closed(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_WEBVIEW_OVERLAY_H */
