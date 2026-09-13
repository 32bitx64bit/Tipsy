/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * WebKitGTK 4.1 + GTK3 child overlay. Own Display/GTK loop; the Roblox X11
 * pump is not stolen. The GtkWindow is override-redirect and reparented
 * into the Roblox xid so it is not a second WM client.
 */

#include "webview_overlay.h"

#include <gdk/gdkx.h>
#include <gtk/gtk.h>
#include <jsc/jsc.h>
#include <libsoup/soup.h>
#include <webkit2/webkit2.h>

#include <X11/Xlib.h>

#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

struct tipsy_webview_state {
	GtkWidget *window;
	GtkWidget *chrome;
	GtkWidget *title;
	GdkWindow *parent_window;
	GdkCursor *cursors[3]; /* default, pointer, text from the active APK */
	GdkCursor *standard_cursors[6];
	WebKitWebView *view;
	Window parent;
	int mapped;
	int width;
	int height;
	int ox;
	int oy;
	unsigned long load_serial;
};

static pthread_t g_thread;
static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t g_ready = PTHREAD_COND_INITIALIZER;
static int g_started;
static int g_ready_flag;
static int g_init_ok;
static GMainLoop *g_loop;
static struct tipsy_webview_state g_ov;

static const char kHybridJS[] =
	"(function(){"
	"var R=window.Roblox=window.Roblox||{};"
	"var H=R.Hybrid=R.Hybrid||{};"
	"H.Bridge=H.Bridge||{};"
	"if(typeof H.Bridge.nativeCallback!=='function'){"
	"H.Bridge.nativeCallback=function(){};}"
	"if(typeof H.Bridge.emitEvent!=='function'){"
	"H.Bridge.emitEvent=function(){};}"
	"function post(u){"
	"try{window.webkit.messageHandlers.tipsyJoin.postMessage(String(u||''));}"
	"catch(e){}"
	"}"
	"H.openUrl=function(u){post(u);};"
	"H.Launch=H.Launch||{};"
	"H.Launch.launch=function(u){post(u);};"
	"var B=window.__globalRobloxAndroidBridge__=window.__globalRobloxAndroidBridge__||{};"
	"B.executeRoblox=function(s){post(s);};"
	"})();";

static const char kDarkSchemeJS[] =
	"(function(){try{"
	"var m=document.querySelector('meta[name=color-scheme]');"
	"if(!m){m=document.createElement('meta');m.name='color-scheme';"
	"(document.head||document.documentElement).appendChild(m);}"
	"m.content='dark';"
	"document.documentElement.style.colorScheme='dark';"
	"}catch(e){}})();";

static const char kLightSchemeJS[] =
	"(function(){try{"
	"var m=document.querySelector('meta[name=color-scheme]');"
	"if(!m){m=document.createElement('meta');m.name='color-scheme';"
	"(document.head||document.documentElement).appendChild(m);}"
	"m.content='light';"
	"document.documentElement.style.colorScheme='light';"
	"}catch(e){}})();";

struct tipsy_open_job {
	unsigned long parent;
	int width;
	int height;
	char *title;
	char *assets;
	char *url;
	char *theme;
	char **names;
	char **values;
	char **domains;
	char **paths;
	int *secure;
	int *http_only;
	int n;
};

static void free_open_job(struct tipsy_open_job *job)
{
	int i;

	if (job == NULL) {
		return;
	}
	free(job->url);
	free(job->theme);
	free(job->title);
	free(job->assets);
	for (i = 0; i < job->n; i++) {
		if (job->names != NULL) {
			free(job->names[i]);
		}
		if (job->values != NULL) {
			if (job->values[i] != NULL) {
				memset(job->values[i], 0, strlen(job->values[i]));
			}
			free(job->values[i]);
		}
		if (job->domains != NULL) {
			free(job->domains[i]);
		}
		if (job->paths != NULL) {
			free(job->paths[i]);
		}
	}
	free(job->names);
	free(job->values);
	free(job->domains);
	free(job->paths);
	free(job->secure);
	free(job->http_only);
	free(job);
}

static Display *overlay_dpy(void)
{
	GdkDisplay *gd;

	if (g_ov.window == NULL) {
		return NULL;
	}
	gd = gtk_widget_get_display(g_ov.window);
	if (gd == NULL || !GDK_IS_X11_DISPLAY(gd)) {
		return NULL;
	}
	return gdk_x11_display_get_xdisplay(gd);
}

static Window overlay_xid(void)
{
	GdkWindow *gdk;

	if (g_ov.window == NULL) {
		return None;
	}
	gdk = gtk_widget_get_window(g_ov.window);
	if (gdk == NULL) {
		return None;
	}
	return gdk_x11_window_get_xid(gdk);
}

struct tipsy_cookie_load {
	char *url;
	unsigned long serial;
	int remaining;
	int queuing;
};

struct tipsy_cookie_add {
	struct tipsy_cookie_load *load;
	SoupCookie *cookie;
};

static void cookie_load_finish(struct tipsy_cookie_load *load)
{
	if (load == NULL) {
		return;
	}
	if (g_ov.view != NULL && g_ov.load_serial == load->serial &&
		g_ov.mapped && load->url != NULL && load->url[0] != 0) {
		webkit_web_view_load_uri(g_ov.view, load->url);
	}
	g_free(load->url);
	g_free(load);
}

static void on_cookie_added(GObject *src, GAsyncResult *res, gpointer data)
{
	GError *err = NULL;
	struct tipsy_cookie_add *add = data;

	webkit_cookie_manager_add_cookie_finish(WEBKIT_COOKIE_MANAGER(src), res, &err);
	if (err != NULL) {
		g_error_free(err);
	}
	if (add == NULL) {
		return;
	}
	if (add->cookie != NULL) {
		soup_cookie_free(add->cookie);
	}
	if (add->load != NULL) {
		add->load->remaining--;
		if (!add->load->queuing && add->load->remaining <= 0) {
			cookie_load_finish(add->load);
		}
	}
	g_free(add);
}

static void apply_cookies_then_load(WebKitWebView *view, struct tipsy_open_job *job)
{
	WebKitWebContext *ctx;
	WebKitCookieManager *mgr;
	struct tipsy_cookie_load *load;
	int i;

	if (view == NULL || job == NULL) {
		return;
	}
	load = g_malloc0(sizeof *load);
	load->url = g_strdup(job->url);
	load->serial = g_ov.load_serial;
	if (job->n <= 0) {
		cookie_load_finish(load);
		return;
	}
	ctx = webkit_web_view_get_context(view);
	if (ctx == NULL) {
		cookie_load_finish(load);
		return;
	}
	mgr = webkit_web_context_get_cookie_manager(ctx);
	if (mgr == NULL) {
		cookie_load_finish(load);
		return;
	}
	/* Programmatic inject has no first-party document yet; ACCEPT_ALWAYS
	 * is required or .roblox.com session cookies are dropped. */
	webkit_cookie_manager_set_accept_policy(mgr, WEBKIT_COOKIE_POLICY_ACCEPT_ALWAYS);
	load->queuing = 1;
	for (i = 0; i < job->n; i++) {
		SoupCookie *c;
		struct tipsy_cookie_add *add;
		const char *name = job->names[i];
		const char *value = job->values[i];
		const char *domain = job->domains[i];
		const char *path = job->paths[i];

		if (name == NULL || value == NULL || domain == NULL || path == NULL) {
			continue;
		}
		c = soup_cookie_new(name, value, domain, path, -1);
		if (c == NULL) {
			continue;
		}
		soup_cookie_set_secure(c, job->secure[i] != 0);
		soup_cookie_set_http_only(c, job->http_only[i] != 0);
		add = g_malloc0(sizeof *add);
		add->load = load;
		add->cookie = c;
		load->remaining++;
		webkit_cookie_manager_add_cookie(mgr, c, NULL, on_cookie_added, add);
	}
	load->queuing = 0;
	if (load->remaining <= 0) {
		cookie_load_finish(load);
	}
}

static gboolean idle_hide(gpointer data);

/* Android's adapter ignores windowType; slideInFromRight is not a width.
 * GenericWebPage already owns the native route, so its host fills that route.
 * Keep the minimum at 1: pinning it to the last allocation prevents shrink. */
static void place_overlay(int parent_w, int parent_h)
{
	Display *dpy;
	Window child;
	int scale;
	int w = parent_w > 0 ? parent_w : 1;
	int h = parent_h > 0 ? parent_h : 1;

	g_ov.ox = 0;
	g_ov.oy = 0;
	g_ov.width = w;
	g_ov.height = h;
	if (g_ov.window != NULL) {
		scale = gtk_widget_get_scale_factor(g_ov.window);
		if (scale < 1) scale = 1;
		gtk_window_resize(GTK_WINDOW(g_ov.window),
			(w + scale - 1) / scale, (h + scale - 1) / scale);
	}
	dpy = overlay_dpy();
	child = overlay_xid();
	if (dpy != NULL && child != None) {
		XMoveResizeWindow(dpy, child, 0, 0, (unsigned)w, (unsigned)h);
	}
}

static void apply_overlay_theme(const char *theme)
{
	GtkSettings *s;
	gboolean dark = TRUE;
	GdkRGBA bg;

	if (theme != NULL && g_ascii_strcasecmp(theme, "Light") == 0) {
		dark = FALSE;
	}
	s = gtk_settings_get_default();
	if (s != NULL) {
		g_object_set(s, "gtk-application-prefer-dark-theme", dark, NULL);
	}
	if (g_ov.window != NULL) {
		GtkStyleContext *style = gtk_widget_get_style_context(g_ov.window);
		gtk_style_context_remove_class(style, dark ? "light" : "dark");
		gtk_style_context_add_class(style, dark ? "dark" : "light");
	}
	if (g_ov.view == NULL) {
		return;
	}
	if (dark) {
		bg.red = 0.102;
		bg.green = 0.102;
		bg.blue = 0.102;
		bg.alpha = 1.0;
	} else {
		bg.red = 1.0;
		bg.green = 1.0;
		bg.blue = 1.0;
		bg.alpha = 1.0;
	}
	webkit_web_view_set_background_color(g_ov.view, &bg);
}

static void dismiss_overlay_user(void)
{
	if (!g_ov.mapped) {
		return;
	}
	idle_hide(NULL);
	tipsy_go_webview_closed();
}

static gboolean on_delete(GtkWidget *widget, GdkEvent *event, gpointer data)
{
	(void)widget;
	(void)event;
	(void)data;
	dismiss_overlay_user();
	return TRUE;
}

static void on_close_clicked(GtkButton *btn, gpointer data)
{
	(void)btn;
	(void)data;
	dismiss_overlay_user();
}

static gboolean on_key_press(GtkWidget *w, GdkEventKey *ev, gpointer data)
{
	(void)w;
	(void)data;
	if (ev != NULL && ev->keyval == GDK_KEY_Escape) {
		dismiss_overlay_user();
		return TRUE;
	}
	return FALSE;
}

static gboolean on_decide_policy(WebKitWebView *view, WebKitPolicyDecision *decision,
	WebKitPolicyDecisionType type, gpointer data)
{
	WebKitNavigationPolicyDecision *nd;
	WebKitNavigationAction *act;
	WebKitURIRequest *req;
	const char *uri;

	(void)view;
	(void)data;
	if (type != WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION &&
		type != WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
		return FALSE;
	}
	nd = WEBKIT_NAVIGATION_POLICY_DECISION(decision);
	act = webkit_navigation_policy_decision_get_navigation_action(nd);
	if (act == NULL) {
		return FALSE;
	}
	req = webkit_navigation_action_get_request(act);
	if (req == NULL) {
		return FALSE;
	}
	uri = webkit_uri_request_get_uri(req);
	if (uri != NULL && tipsy_go_webview_policy((char *)uri) != 0) {
		webkit_policy_decision_ignore(decision);
		return TRUE;
	}
	return FALSE;
}

static void on_script_message(WebKitUserContentManager *mgr, JSCValue *value, gpointer data)
{
	char *s;

	(void)mgr;
	(void)data;
	if (value == NULL || !jsc_value_is_string(value)) {
		return;
	}
	s = jsc_value_to_string(value);
	if (s != NULL) {
		(void)tipsy_go_webview_policy(s);
		g_free(s);
	}
}

static int follow_parent(Display *dpy, Window parent, int *out_w, int *out_h)
{
	Window root = None, p = None, *children = NULL;
	unsigned n = 0;
	int x = 0, y = 0;
	unsigned w = 0, h = 0, bw = 0, depth = 0;

	if (dpy == NULL || parent == None) {
		return -1;
	}
	if (XGetGeometry(dpy, parent, &root, &x, &y, &w, &h, &bw, &depth) == 0) {
		return -1;
	}
	(void)p;
	(void)children;
	(void)n;
	if (w < 1 || h < 1) {
		return -1;
	}
	*out_w = (int)w;
	*out_h = (int)h;
	return 0;
}

static GdkFilterReturn parent_filter(GdkXEvent *gxev, GdkEvent *gev, gpointer data)
{
	XEvent *ev = (XEvent *)gxev;

	(void)gev;
	(void)data;
	if (ev == NULL) {
		return GDK_FILTER_CONTINUE;
	}
	if (ev->type == ConfigureNotify && ev->xconfigure.window == g_ov.parent) {
		int w = ev->xconfigure.width;
		int h = ev->xconfigure.height;
		if (w > 0 && h > 0 && g_ov.window != NULL) {
			place_overlay(w, h);
		}
	} else if (ev->type == DestroyNotify && ev->xdestroywindow.window == g_ov.parent) {
		tipsy_webview_overlay_close();
	}
	/* Parent unmap includes minimize. X11 keeps the child unviewable until
	 * restore; it is not a user dismissal and must not pop the Lua route. */
	return GDK_FILTER_CONTINUE;
}

static void select_parent_events(Display *dpy, Window parent)
{
	GdkWindow *foreign;
	GdkDisplay *gd;

	if (dpy == NULL || parent == None) {
		return;
	}
	XSelectInput(dpy, parent, StructureNotifyMask);
	gd = gdk_display_get_default();
	if (gd == NULL) {
		return;
	}
	foreign = gdk_x11_window_foreign_new_for_display(gd, parent);
	if (foreign != NULL) {
		g_ov.parent_window = foreign;
		gdk_window_add_filter(foreign, parent_filter, NULL);
	}
}

static void restore_parent_focus(void)
{
	Display *dpy = overlay_dpy();
	Window focus = None, child = overlay_xid();
	int revert;
	XWindowAttributes attrs;
	if (dpy == NULL || child == None || g_ov.parent == None) return;
	XGetInputFocus(dpy, &focus, &revert);
	/* The GTK toplevel owns X keyboard focus; don't steal it back if the
	 * user has already switched to another application. */
	if (focus == child && XGetWindowAttributes(dpy, g_ov.parent, &attrs) &&
		attrs.map_state == IsViewable) {
		XSetInputFocus(dpy, g_ov.parent, RevertToParent, CurrentTime);
		XFlush(dpy);
	}
}

static void destroy_overlay_widgets(void)
{
	unsigned long serial = g_ov.load_serial + 1;
	if (g_ov.parent_window != NULL) {
		gdk_window_remove_filter(g_ov.parent_window, parent_filter, NULL);
		g_object_unref(g_ov.parent_window);
		g_ov.parent_window = NULL;
	}
	if (g_ov.window != NULL) {
		gtk_widget_destroy(g_ov.window);
	}
	for (unsigned i = 0; i < G_N_ELEMENTS(g_ov.cursors); i++) {
		g_clear_object(&g_ov.cursors[i]);
	}
	for (unsigned i = 0; i < G_N_ELEMENTS(g_ov.standard_cursors); i++) {
		g_clear_object(&g_ov.standard_cursors[i]);
	}
	memset(&g_ov, 0, sizeof g_ov);
	g_ov.load_serial = serial;
}

static gboolean idle_hide(gpointer data)
{
	(void)data;
	if (g_ov.window != NULL && g_ov.mapped) {
		restore_parent_focus();
		gtk_widget_hide(g_ov.window);
		g_ov.mapped = 0;
		g_ov.load_serial++;
		webkit_web_view_stop_loading(g_ov.view);
	}
	return G_SOURCE_REMOVE;
}

static gboolean idle_close(gpointer data)
{
	(void)data;
	destroy_overlay_widgets();
	return G_SOURCE_REMOVE;
}

static void apply_chrome_css(GtkWidget *window)
{
	GtkCssProvider *provider = gtk_css_provider_new();
	gtk_css_provider_load_from_data(provider,
		"#tipsy-webview { background-color: #111216; }"
		"#tipsy-webview-chrome { padding: 8px 16px; border-bottom: 1px solid #35373d; }"
		".dark #tipsy-webview-chrome { background-color: #252730; color: #f7f7f8; }"
		".light #tipsy-webview-chrome { background-color: #f7f7f8; color: #202227; border-color: #dedee3; }"
		"#tipsy-webview-chrome button { background-image: none; background-color: transparent;"
		" color: inherit; border: 0; border-radius: 8px; box-shadow: none; text-shadow: none;"
		" min-height: 32px; padding: 2px 10px; font-weight: 600; }"
		".dark #tipsy-webview-chrome button:hover { background-color: #393b45; }"
		".light #tipsy-webview-chrome button:hover { background-color: #e5e5eb; }"
		"#tipsy-webview-chrome button:focus { outline: 2px solid #7191ff; outline-offset: -2px; }",
		-1, NULL);
	gtk_style_context_add_provider_for_screen(gtk_widget_get_screen(window),
		GTK_STYLE_PROVIDER(provider), GTK_STYLE_PROVIDER_PRIORITY_APPLICATION);
	g_object_unref(provider);
}

/* GDK's X11 backend caches named cursor objects per display. Match those
 * public objects (and legacy cursor types), preserving WebKit's own choice
 * of arrow, hand, text, resize, busy, hidden, or page-provided custom cursor.
 * Only these three familiar shapes use the active APK's artwork. */
static GdkCursor *overlay_cursor_for(GdkCursor *cursor)
{
	GdkCursorType type = cursor ? gdk_cursor_get_cursor_type(cursor) : GDK_LEFT_PTR;
	int kind = -1;
	if (cursor == g_ov.cursors[0] || cursor == g_ov.cursors[1] || cursor == g_ov.cursors[2]) {
		if (cursor != NULL) return cursor;
	}
	if (type == GDK_LEFT_PTR || type == GDK_ARROW) kind = 0;
	else if (type == GDK_HAND1 || type == GDK_HAND2) kind = 1;
	else if (type == GDK_XTERM) kind = 2;
	for (unsigned i = 0; cursor != NULL && i < G_N_ELEMENTS(g_ov.standard_cursors); i++) {
		if (cursor == g_ov.standard_cursors[i]) kind = (int)i / 2;
	}
	if (kind >= 0 && g_ov.cursors[kind] != NULL) return g_ov.cursors[kind];
	return cursor;
}

static void on_cursor_changed(GObject *object, GParamSpec *spec, gpointer data)
{
	GdkWindow *window = GDK_WINDOW(object);
	GdkCursor *current = gdk_window_get_cursor(window);
	GdkCursor *replacement = overlay_cursor_for(current);
	(void)spec;
	(void)data;
	if (replacement != current) gdk_window_set_cursor(window, replacement);
}

static void on_view_realized(GtkWidget *widget, gpointer data)
{
	GdkWindow *window = gtk_widget_get_window(widget);
	(void)data;
	if (window == NULL) return;
	g_signal_connect(window, "notify::cursor", G_CALLBACK(on_cursor_changed), NULL);
	gdk_window_set_cursor(window, g_ov.cursors[0] != NULL ? g_ov.cursors[0] : g_ov.standard_cursors[0]);
}

static void load_overlay_cursors(const char *assets)
{
	const char *names[] = { "default", "left_ptr", "pointer", "hand2", "text", "xterm" };
	const char *files[] = { "ArrowFarCursor.png", "ArrowCursor.png", "IBeamCursor.png" };
	GdkDisplay *display = gtk_widget_get_display(g_ov.window);
	for (unsigned i = 0; i < G_N_ELEMENTS(names); i++) {
		g_ov.standard_cursors[i] = gdk_cursor_new_from_name(display, names[i]);
	}
	if (assets == NULL || assets[0] == 0) return;
	for (unsigned i = 0; i < G_N_ELEMENTS(files); i++) {
		char *path = g_build_filename(assets, "content", "textures", "Cursors", "KeyboardMouse", files[i], NULL);
		GdkPixbuf *pixbuf = gdk_pixbuf_new_from_file(path, NULL);
		g_free(path);
		if (pixbuf == NULL) continue;
		int w = gdk_pixbuf_get_width(pixbuf), h = gdk_pixbuf_get_height(pixbuf);
		/* Roblox's cursor sprites are centered on the pointer. Preserve the
		 * transparent padding and hotspot rather than shifting click targets. */
		if (w > 0 && h > 0 && w <= 256 && h <= 256) {
			g_ov.cursors[i] = gdk_cursor_new_from_pixbuf(display, pixbuf, w / 2, h / 2);
		}
		g_object_unref(pixbuf);
	}
}

static gboolean on_back_cursor(GtkWidget *widget, GdkEventCrossing *event, gpointer data)
{
	GdkWindow *window = gtk_widget_get_window(widget);
	unsigned kind = event->type == GDK_ENTER_NOTIFY ? 1 : 0;
	(void)data;
	if (window != NULL) {
		gdk_window_set_cursor(window, g_ov.cursors[kind] != NULL ? g_ov.cursors[kind] : g_ov.standard_cursors[kind * 2]);
	}
	return FALSE;
}

static GtkWidget *build_close_chrome(void)
{
	GtkWidget *bar = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 16);
	GtkWidget *button = gtk_button_new_with_label("Back to Roblox");
	GtkWidget *arrow = gtk_image_new_from_icon_name("go-previous-symbolic", GTK_ICON_SIZE_BUTTON);
	gtk_widget_set_name(bar, "tipsy-webview-chrome");
	gtk_button_set_image(GTK_BUTTON(button), arrow);
	gtk_button_set_always_show_image(GTK_BUTTON(button), TRUE);
	gtk_widget_set_tooltip_text(button, "Return to Roblox (Esc)");
	g_signal_connect(button, "clicked", G_CALLBACK(on_close_clicked), NULL);
	g_signal_connect(button, "enter-notify-event", G_CALLBACK(on_back_cursor), NULL);
	g_signal_connect(button, "leave-notify-event", G_CALLBACK(on_back_cursor), NULL);
	gtk_box_pack_start(GTK_BOX(bar), button, FALSE, FALSE, 0);
	g_ov.title = gtk_label_new("");
	gtk_label_set_ellipsize(GTK_LABEL(g_ov.title), PANGO_ELLIPSIZE_END);
	gtk_label_set_xalign(GTK_LABEL(g_ov.title), 0.0);
	gtk_box_pack_start(GTK_BOX(bar), g_ov.title, TRUE, TRUE, 0);
	g_ov.chrome = bar;
	return bar;
}

static void apply_document_scripts(const char *theme)
{
	WebKitUserContentManager *manager = webkit_web_view_get_user_content_manager(g_ov.view);
	const char *scripts[] = { kHybridJS,
		theme != NULL && g_ascii_strcasecmp(theme, "Light") == 0 ? kLightSchemeJS : kDarkSchemeJS };
	webkit_user_content_manager_remove_all_scripts(manager);
	for (unsigned i = 0; i < G_N_ELEMENTS(scripts); i++) {
		WebKitUserScript *script = webkit_user_script_new(scripts[i],
			WEBKIT_USER_CONTENT_INJECT_ALL_FRAMES, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START,
			NULL, NULL);
		webkit_user_content_manager_add_script(manager, script);
		webkit_user_script_unref(script);
	}
}

static void show_overlay(struct tipsy_open_job *job)
{
	Display *dpy = overlay_dpy();
	Window child = overlay_xid();
	gtk_label_set_text(GTK_LABEL(g_ov.title), job->title != NULL ? job->title : "");
	apply_overlay_theme(job->theme);
	apply_document_scripts(job->theme);
	gtk_widget_show_all(g_ov.window);
	place_overlay(job->width, job->height);
	if (dpy != NULL && child != None) {
		XMapRaised(dpy, child);
		XSetInputFocus(dpy, child, RevertToParent, CurrentTime);
		XFlush(dpy);
	}
	gtk_widget_grab_focus(GTK_WIDGET(g_ov.view));
	g_ov.mapped = 1;
	g_ov.load_serial++;
	apply_cookies_then_load(g_ov.view, job);
}

static void build_overlay(struct tipsy_open_job *job)
{
	WebKitUserContentManager *ucm;
	WebKitSettings *settings;
	GtkWidget *vbox;
	Display *dpy;
	Window child;
	int w, h;

	w = job->width;
	h = job->height;
	dpy = gdk_x11_display_get_xdisplay(GDK_X11_DISPLAY(gdk_display_get_default()));
	if (dpy != NULL && follow_parent(dpy, (Window)job->parent, &w, &h) == 0) {
		job->width = w;
		job->height = h;
	}
	if (g_ov.window != NULL && g_ov.parent == (Window)job->parent) {
		show_overlay(job);
		return;
	}
	destroy_overlay_widgets();

	g_ov.window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	gtk_widget_set_name(g_ov.window, "tipsy-webview");
	load_overlay_cursors(job->assets);
	gtk_window_set_decorated(GTK_WINDOW(g_ov.window), FALSE);
	gtk_window_set_skip_taskbar_hint(GTK_WINDOW(g_ov.window), TRUE);
	gtk_window_set_skip_pager_hint(GTK_WINDOW(g_ov.window), TRUE);
	gtk_window_set_accept_focus(GTK_WINDOW(g_ov.window), TRUE);
	gtk_window_set_resizable(GTK_WINDOW(g_ov.window), TRUE);
	gtk_window_set_type_hint(GTK_WINDOW(g_ov.window), GDK_WINDOW_TYPE_HINT_UTILITY);
	place_overlay(job->width, job->height);
	gtk_widget_set_size_request(g_ov.window, 1, 1);
	gtk_window_set_default_size(GTK_WINDOW(g_ov.window), g_ov.width, g_ov.height);
	g_signal_connect(g_ov.window, "key-press-event", G_CALLBACK(on_key_press), NULL);
	g_signal_connect(g_ov.window, "delete-event", G_CALLBACK(on_delete), NULL);
	apply_chrome_css(g_ov.window);

	ucm = webkit_user_content_manager_new();
	g_signal_connect(ucm, "script-message-received::tipsyJoin",
		G_CALLBACK(on_script_message), NULL);
	webkit_user_content_manager_register_script_message_handler(ucm, "tipsyJoin");

	g_ov.view = WEBKIT_WEB_VIEW(webkit_web_view_new_with_user_content_manager(ucm));
	g_signal_connect(g_ov.view, "realize", G_CALLBACK(on_view_realized), NULL);
	g_object_unref(ucm);
	settings = webkit_web_view_get_settings(g_ov.view);
	webkit_settings_set_javascript_can_open_windows_automatically(settings, FALSE);
	webkit_settings_set_user_agent(settings,
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko) ROBLOX Android App Hybrid");
	g_signal_connect(g_ov.view, "decide-policy", G_CALLBACK(on_decide_policy), NULL);
	apply_overlay_theme(job->theme);

	vbox = gtk_box_new(GTK_ORIENTATION_VERTICAL, 0);
	gtk_box_pack_start(GTK_BOX(vbox), build_close_chrome(), FALSE, FALSE, 0);
	gtk_box_pack_start(GTK_BOX(vbox), GTK_WIDGET(g_ov.view), TRUE, TRUE, 0);
	gtk_container_add(GTK_CONTAINER(g_ov.window), vbox);

	gtk_widget_realize(g_ov.window);
	{
		GdkWindow *gdk = gtk_widget_get_window(g_ov.window);
		if (gdk != NULL) {
			gdk_window_set_override_redirect(gdk, TRUE);
			gdk_window_set_cursor(gdk, g_ov.cursors[0] != NULL ? g_ov.cursors[0] : g_ov.standard_cursors[0]);
		}
	}
	dpy = overlay_dpy();
	child = overlay_xid();
	g_ov.parent = (Window)job->parent;
	if (dpy != NULL && child != None && job->parent != 0) {
		XReparentWindow(dpy, child, (Window)job->parent, g_ov.ox, g_ov.oy);
		XMoveResizeWindow(dpy, child, g_ov.ox, g_ov.oy,
			(unsigned)g_ov.width, (unsigned)g_ov.height);
		select_parent_events(dpy, g_ov.parent);
	}
	show_overlay(job);
}

static gboolean idle_open(gpointer data)
{
	struct tipsy_open_job *job = data;

	if (job != NULL) {
		build_overlay(job);
		free_open_job(job);
	}
	return G_SOURCE_REMOVE;
}

static void *gtk_thread_main(void *arg)
{
	(void)arg;
	gdk_set_allowed_backends("x11");
	g_init_ok = gtk_init_check(NULL, NULL) ? 1 : 0;
	if (g_init_ok) {
		g_loop = g_main_loop_new(NULL, FALSE);
	}
	pthread_mutex_lock(&g_mu);
	g_ready_flag = 1;
	pthread_cond_broadcast(&g_ready);
	pthread_mutex_unlock(&g_mu);
	if (g_init_ok && g_loop != NULL) {
		g_main_loop_run(g_loop);
	}
	return NULL;
}

int tipsy_webview_overlay_start(void)
{
	pthread_mutex_lock(&g_mu);
	if (g_started) {
		int ok = g_init_ok;
		pthread_mutex_unlock(&g_mu);
		return ok ? 0 : -1;
	}
	g_started = 1;
	if (pthread_create(&g_thread, NULL, gtk_thread_main, NULL) != 0) {
		g_started = 0;
		pthread_mutex_unlock(&g_mu);
		return -1;
	}
	while (!g_ready_flag) {
		pthread_cond_wait(&g_ready, &g_mu);
	}
	pthread_mutex_unlock(&g_mu);
	return g_init_ok ? 0 : -1;
}

static char *dup_str(const char *s)
{
	if (s == NULL) {
		return NULL;
	}
	return strdup(s);
}

int tipsy_webview_overlay_open(unsigned long parent, int width, int height,
	const char *url, const char *theme, const char *title, const char *assets, char **names,
	char **values, char **domains, char **paths, int *secure,
	int *http_only, int n_cookies)
{
	struct tipsy_open_job *job;
	int i;

	if (tipsy_webview_overlay_start() != 0) {
		return -1;
	}
	if (parent == 0 || url == NULL || url[0] == 0) {
		return -1;
	}
	if (width < 1) {
		width = 1280;
	}
	if (height < 1) {
		height = 720;
	}
	job = calloc(1, sizeof *job);
	if (job == NULL) {
		return -1;
	}
	job->parent = parent;
	job->width = width;
	job->height = height;
	job->title = dup_str(title);
	job->assets = dup_str(assets);
	job->url = dup_str(url);
	job->theme = dup_str(theme);
	job->n = n_cookies;
	if (n_cookies > 0) {
		job->names = calloc((size_t)n_cookies, sizeof(char *));
		job->values = calloc((size_t)n_cookies, sizeof(char *));
		job->domains = calloc((size_t)n_cookies, sizeof(char *));
		job->paths = calloc((size_t)n_cookies, sizeof(char *));
		job->secure = calloc((size_t)n_cookies, sizeof(int));
		job->http_only = calloc((size_t)n_cookies, sizeof(int));
		for (i = 0; i < n_cookies; i++) {
			job->names[i] = dup_str(names[i]);
			job->values[i] = dup_str(values[i]);
			job->domains[i] = dup_str(domains[i]);
			job->paths[i] = dup_str(paths[i]);
			job->secure[i] = secure[i];
			job->http_only[i] = http_only[i];
		}
	}
	g_idle_add(idle_open, job);
	return 0;
}

void tipsy_webview_overlay_hide(void)
{
	if (!g_init_ok) {
		return;
	}
	g_idle_add(idle_hide, NULL);
}

void tipsy_webview_overlay_close(void)
{
	if (!g_init_ok) {
		return;
	}
	g_idle_add(idle_close, NULL);
}

int tipsy_webview_overlay_visible(void)
{
	return g_ov.mapped != 0;
}

struct tipsy_cursor_check {
	pthread_mutex_t mutex;
	pthread_cond_t done;
	int kind;
	int result;
	int complete;
};

static gboolean idle_cursor_check(gpointer data)
{
	struct tipsy_cursor_check *check = data;
	int result = 0;
	if (g_ov.mapped && g_ov.view != NULL && check->kind >= 0 && check->kind < 3) {
		GdkWindow *window = gtk_widget_get_window(GTK_WIDGET(g_ov.view));
		GdkCursor *standard = g_ov.standard_cursors[check->kind * 2];
		if (window != NULL && standard != NULL) {
			gdk_window_set_cursor(window, standard);
			GdkCursor *expected = g_ov.cursors[check->kind] != NULL ? g_ov.cursors[check->kind] : standard;
			if (gdk_window_get_cursor(window) == expected) result = g_ov.cursors[check->kind] != NULL ? 2 : 1;
		}
	} else if (!g_ov.mapped && check->kind == -1) {
		result = 1;
	}
	pthread_mutex_lock(&check->mutex);
	check->result = result;
	check->complete = 1;
	pthread_cond_signal(&check->done);
	pthread_mutex_unlock(&check->mutex);
	return G_SOURCE_REMOVE;
}

int tipsy_webview_overlay_test_cursor(int kind)
{
	struct tipsy_cursor_check check = {
		.mutex = PTHREAD_MUTEX_INITIALIZER,
		.done = PTHREAD_COND_INITIALIZER,
		.kind = kind,
	};
	if (!g_init_ok) return 0;
	pthread_mutex_lock(&check.mutex);
	g_idle_add(idle_cursor_check, &check);
	while (!check.complete) pthread_cond_wait(&check.done, &check.mutex);
	pthread_mutex_unlock(&check.mutex);
	pthread_cond_destroy(&check.done);
	pthread_mutex_destroy(&check.mutex);
	return check.result;
}
