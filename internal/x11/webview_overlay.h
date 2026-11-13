/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: AGPL-3.0-or-later
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

#include <stddef.h>

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
/* Pure join-policy evaluation against supplied facts (origin URI and input
 * age in seconds; a negative age means no user input). Reads and mutates no
 * overlay state. */
int tipsy_webview_overlay_test_join_gate(const char *uri, double input_age_seconds);
/* Live-WebKit host-test support. Loads a fixed local first-party page, sends
 * a real GTK key event, and reports the live document. None of these grant a
 * join-gate bypass. */
int tipsy_webview_overlay_test_load_roblox_page(void);
int tipsy_webview_overlay_test_send_key(void);
int tipsy_webview_overlay_test_document(char *uri, size_t uri_len, int *loading);
#ifdef TIPSY_WEBVIEW_ALLOW_TEST_SEAMS
/* Development-only bypass of the join origin/gesture gate for host tests
 * on local data: pages. Compiled out of official_release builds. */
void tipsy_webview_overlay_test_set_join_gate_bypass(int bypass);
#endif

/* Implemented in Go (webview_overlay_linux.go). Returns 1 if the URI was
 * handled as a join or close command (navigation should be ignored). */
int tipsy_go_webview_policy(char *uri);

/* User dismissed the overlay (Back to Roblox / Escape). */
void tipsy_go_webview_closed(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_WEBVIEW_OVERLAY_H */
