/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Host glibc/Mesa dlsym helpers and EGL window-surface translation.
 */
#include "android_bridge.h"
#include "../graphics/guest_swap.h"
#include "../x11/focused_text_foreground.h"

#include <dlfcn.h>
#include <link.h>
#include <pthread.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdio.h>
#include <stdatomic.h>
#include <time.h>

extern void GoAndroid_LogMissing(char *name);

static void *lib_libc;
static void *lib_libm;
static void *lib_libz;
static void *lib_egl;
static void *lib_gles;
static pthread_once_t host_abi_once = PTHREAD_ONCE_INIT;
static pthread_once_t host_egl_once = PTHREAD_ONCE_INIT;

typedef void *EGLDisplay;
typedef void *EGLConfig;
typedef void *EGLSurface;
typedef void *EGLContext;
typedef int32_t EGLint;
typedef uint32_t EGLBoolean;
typedef uint32_t EGLenum;
typedef intptr_t EGLAttrib;

typedef EGLDisplay (*egl_get_platform_display_fn)(EGLenum, void *, const EGLAttrib *);
typedef EGLDisplay (*egl_get_platform_display_ext_fn)(EGLenum, void *, const EGLint *);
typedef EGLDisplay (*egl_get_display_fn)(void *);
typedef EGLBoolean (*egl_initialize_fn)(EGLDisplay, EGLint *, EGLint *);
typedef EGLBoolean (*egl_choose_config_fn)(EGLDisplay, const EGLint *, EGLConfig *, EGLint, EGLint *);
typedef EGLContext (*egl_create_context_fn)(EGLDisplay, EGLConfig, EGLContext, const EGLint *);
typedef EGLSurface (*egl_create_window_surface_fn)(EGLDisplay, EGLConfig, void *, const EGLint *);
typedef EGLBoolean (*egl_make_current_fn)(EGLDisplay, EGLSurface, EGLSurface, EGLContext);
typedef void *(*egl_get_proc_address_fn)(const char *);
typedef EGLBoolean (*egl_swap_interval_fn)(EGLDisplay, EGLint);
typedef EGLBoolean (*egl_swap_buffers_fn)(EGLDisplay, EGLSurface);
typedef EGLBoolean (*egl_destroy_surface_fn)(EGLDisplay, EGLSurface);
typedef EGLBoolean (*egl_destroy_context_fn)(EGLDisplay, void *);
typedef EGLint (*egl_get_error_fn)(void);
typedef EGLDisplay (*egl_get_current_display_fn)(void);
typedef EGLSurface (*egl_get_current_surface_fn)(EGLint);
typedef void *(*egl_get_current_context_fn)(void);
typedef EGLBoolean (*egl_query_surface_fn)(EGLDisplay, EGLSurface, EGLint, EGLint *);

void *tipsy_eglGetProcAddress(const char *name);

static egl_get_platform_display_fn host_eglGetPlatformDisplay;
static egl_get_platform_display_ext_fn host_eglGetPlatformDisplayEXT;
static egl_get_display_fn host_eglGetDisplay;
static egl_initialize_fn host_eglInitialize;
static egl_choose_config_fn host_eglChooseConfig;
static egl_create_context_fn host_eglCreateContext;
static egl_create_window_surface_fn host_eglCreateWindowSurface;
static egl_make_current_fn host_eglMakeCurrent;
static egl_get_proc_address_fn host_eglGetProcAddress;
static egl_swap_interval_fn host_eglSwapInterval;
static egl_swap_buffers_fn host_eglSwapBuffers;
static egl_destroy_surface_fn host_eglDestroySurface;
static egl_destroy_context_fn host_eglDestroyContext;
static egl_get_error_fn host_eglGetError;
static egl_get_current_display_fn host_eglGetCurrentDisplay;
static egl_get_current_surface_fn host_eglGetCurrentSurface;
static egl_get_current_context_fn host_eglGetCurrentContext;
static egl_query_surface_fn host_eglQuerySurface;
static _Atomic int egl_vsync_enabled;
/* Default off. Go enables this when the 2s graphics Info logger will emit. */
static _Atomic int egl_present_stats_enabled;
static _Atomic uint64_t egl_successful_swaps;
static _Atomic uint64_t egl_first_swap_ns;
static _Atomic uint64_t egl_last_swap_ns;

#define TIPSY_EGL_FALSE ((EGLBoolean)0)
#define TIPSY_EGL_TRUE ((EGLBoolean)1)
#define TIPSY_EGL_SUCCESS ((EGLint)0x3000)
#define TIPSY_EGL_DRAW ((EGLint)0x3059)
#define TIPSY_EGL_WIDTH ((EGLint)0x3057)
#define TIPSY_EGL_HEIGHT ((EGLint)0x3056)
#define TIPSY_EGL_SWAP_BEHAVIOR ((EGLint)0x3093)
#define TIPSY_EGL_BUFFER_PRESERVED ((EGLint)0x3094)
#define TIPSY_EGL_BUFFER_DESTROYED ((EGLint)0x3095)

/* Exact-opt-in, content-free setup tracing. All emitted text is selected from
 * the fixed labels below; no EGL handles, addresses, attributes, dimensions,
 * errors, or client content are inspected or printed. */
#define TIPSY_EGL_SETUP_TRACE_ENV "TIPSY_TEST_EGL_SETUP_TRACE"
enum {
	TIPSY_EGL_SETUP_PLATFORM_DISPLAY = 1,
	TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT,
	TIPSY_EGL_SETUP_DISPLAY,
	TIPSY_EGL_SETUP_INITIALIZE,
	TIPSY_EGL_SETUP_CHOOSE_CONFIG,
	TIPSY_EGL_SETUP_CREATE_CONTEXT,
	TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE,
	TIPSY_EGL_SETUP_MAKE_CURRENT,
	TIPSY_EGL_SETUP_FIRST_SWAP,
};
enum {
	TIPSY_EGL_SETUP_SUCCESS = 1,
	TIPSY_EGL_SETUP_FAILURE,
	TIPSY_EGL_SETUP_ABSENCE,
};
typedef void (*tipsy_egl_setup_test_sink_fn)(uint32_t, uint32_t);

static pthread_once_t egl_setup_trace_once = PTHREAD_ONCE_INIT;
static int egl_setup_trace_enabled;
static _Atomic uint64_t egl_setup_trace_seen;
static _Atomic int egl_setup_first_swap_seen;
static tipsy_egl_setup_test_sink_fn egl_setup_test_sink;
static int egl_setup_fixture_suppress_missing;

static void tipsy_egl_setup_log_missing(char *name)
{
	if (!egl_setup_fixture_suppress_missing) GoAndroid_LogMissing(name);
}

static int tipsy_egl_setup_trace_value_enabled(const char *value)
{
	return value != NULL && strcmp(value, "1") == 0;
}

static void tipsy_egl_setup_trace_init(void)
{
	egl_setup_trace_enabled =
		tipsy_egl_setup_trace_value_enabled(getenv(TIPSY_EGL_SETUP_TRACE_ENV));
}

static int tipsy_egl_setup_trace_is_enabled(void)
{
	(void)pthread_once(&egl_setup_trace_once, tipsy_egl_setup_trace_init);
	return egl_setup_trace_enabled;
}

static const char *tipsy_egl_setup_marker(uint32_t stage, uint32_t outcome)
{
#define TIPSY_EGL_SETUP_MARKERS(stage_name, label) \
	case stage_name: \
		switch (outcome) { \
		case TIPSY_EGL_SETUP_SUCCESS: return label "=success"; \
		case TIPSY_EGL_SETUP_FAILURE: return label "=failure"; \
		case TIPSY_EGL_SETUP_ABSENCE: return label "=absence"; \
		default: return NULL; \
		}
	switch (stage) {
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_PLATFORM_DISPLAY, "platform_display");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT, "platform_display_ext");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_DISPLAY, "display");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_INITIALIZE, "initialize");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_CHOOSE_CONFIG, "choose_config");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_CREATE_CONTEXT, "create_context");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE, "create_window_surface");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_MAKE_CURRENT, "make_current");
	TIPSY_EGL_SETUP_MARKERS(TIPSY_EGL_SETUP_FIRST_SWAP, "first_swap");
	default: return NULL;
	}
#undef TIPSY_EGL_SETUP_MARKERS
}

static void tipsy_egl_setup_trace_result(uint32_t stage, uint32_t outcome)
{
	const char *marker;
	uint64_t bit;

	if (egl_setup_test_sink != NULL) {
		egl_setup_test_sink(stage, outcome);
	}
	if (stage == TIPSY_EGL_SETUP_FIRST_SWAP) {
		if (atomic_load_explicit(&egl_setup_first_swap_seen,
			memory_order_relaxed) != 0) return;
		if (!tipsy_egl_setup_trace_is_enabled()) {
			atomic_store_explicit(&egl_setup_first_swap_seen, 1,
				memory_order_relaxed);
			return;
		}
		if (atomic_exchange_explicit(&egl_setup_first_swap_seen, 1,
			memory_order_relaxed) != 0) return;
	} else if (!tipsy_egl_setup_trace_is_enabled()) {
		return;
	}
	if (stage < TIPSY_EGL_SETUP_PLATFORM_DISPLAY ||
		stage > TIPSY_EGL_SETUP_FIRST_SWAP ||
		outcome < TIPSY_EGL_SETUP_SUCCESS ||
		outcome > TIPSY_EGL_SETUP_ABSENCE) return;
	bit = 1ull << (((uint64_t)stage - 1ull) * 3ull +
		((uint64_t)outcome - 1ull));
	if ((atomic_fetch_or_explicit(&egl_setup_trace_seen, bit,
		memory_order_relaxed) & bit) != 0) return;
	marker = tipsy_egl_setup_marker(stage, outcome);
	if (marker != NULL) {
		fprintf(stderr, "tipsy-egl-setup: %s\n", marker);
	}
}

/* Called only from the EGL-owned resolver paths. The comparisons select fixed
 * output labels and never echo the supplied string. */
static void tipsy_egl_trace_resolver_name(const char *name)
{
	uint32_t bit = 0;
	const char *marker = NULL;

	if (name == NULL || !tipsy_egl_setup_trace_is_enabled()) return;
#define TIPSY_EGL_RESOLVER_NAME(symbol, index) \
	if (strcmp(name, symbol) == 0) { bit = (index); marker = "resolver_" symbol "=success"; }
	TIPSY_EGL_RESOLVER_NAME("eglGetProcAddress", 0u)
	else TIPSY_EGL_RESOLVER_NAME("eglGetPlatformDisplay", 1u)
	else TIPSY_EGL_RESOLVER_NAME("eglGetPlatformDisplayEXT", 2u)
	else TIPSY_EGL_RESOLVER_NAME("eglGetDisplay", 3u)
	else TIPSY_EGL_RESOLVER_NAME("eglInitialize", 4u)
	else TIPSY_EGL_RESOLVER_NAME("eglChooseConfig", 5u)
	else TIPSY_EGL_RESOLVER_NAME("eglCreateContext", 6u)
	else TIPSY_EGL_RESOLVER_NAME("eglCreateWindowSurface", 7u)
	else TIPSY_EGL_RESOLVER_NAME("eglMakeCurrent", 8u)
	else TIPSY_EGL_RESOLVER_NAME("eglSwapBuffers", 9u)
#undef TIPSY_EGL_RESOLVER_NAME
	if (marker == NULL) return;
	bit += 32u;
	if ((atomic_fetch_or_explicit(&egl_setup_trace_seen, 1ull << bit,
		memory_order_relaxed) & (1ull << bit)) == 0) {
		fprintf(stderr, "tipsy-egl-setup: %s\n", marker);
	}
}

static void tipsy_egl_trace_platform_display_ext_resolver_absence(void)
{
	uint64_t bit = 1ull << 42;

	if (!tipsy_egl_setup_trace_is_enabled()) return;
	if ((atomic_fetch_or_explicit(&egl_setup_trace_seen, bit,
		memory_order_relaxed) & bit) == 0) {
		fprintf(stderr,
			"tipsy-egl-setup: resolver_eglGetPlatformDisplayEXT=absence\n");
	}
}

/* The foreground provider lives in the X11 package. It is deliberately a
 * weak dependency: Android ABI tests and non-X11 builds retain ordinary EGL
 * behavior, while a linked X11 foreground provider offers only a short
 * premultiplied-alpha lease. No X11 drawable, root pixel, compositor, or
 * source text crosses this boundary. */
#pragma weak tipsy_focused_text_frame_acquire
#pragma weak tipsy_focused_text_frame_release
#pragma weak tipsy_focused_text_overlay_live

typedef int (*tipsy_egl_text_frame_acquire_fn)(struct tipsy_focused_text_frame *);
typedef void (*tipsy_egl_text_frame_release_fn)(uintptr_t);
typedef int (*tipsy_egl_text_overlay_live_fn)(void);

static int tipsy_egl_text_frame_acquire(struct tipsy_focused_text_frame *frame)
{
	if (tipsy_focused_text_frame_acquire == NULL) return 0;
	return tipsy_focused_text_frame_acquire(frame);
}

static void tipsy_egl_text_frame_release(uintptr_t lease)
{
	if (tipsy_focused_text_frame_release != NULL) tipsy_focused_text_frame_release(lease);
}

static int tipsy_egl_text_overlay_live_weak(void)
{
	if (tipsy_focused_text_overlay_live == NULL) return 0;
	return tipsy_focused_text_overlay_live() != 0;
}

static tipsy_egl_text_frame_acquire_fn egl_text_frame_acquire_fn =
	tipsy_egl_text_frame_acquire;
static tipsy_egl_text_frame_release_fn egl_text_frame_release_fn =
	tipsy_egl_text_frame_release;
static tipsy_egl_text_overlay_live_fn egl_text_overlay_live_fn =
	tipsy_egl_text_overlay_live_weak;

typedef unsigned int TipsyGLenum;
typedef unsigned char TipsyGLboolean;
typedef int TipsyGLint;
typedef int TipsyGLsizei;
typedef unsigned int TipsyGLuint;
typedef float TipsyGLfloat;
typedef ptrdiff_t TipsyGLsizeiptr;

#define TIPSY_GL_FALSE 0u
#define TIPSY_GL_TRUE 1u
#define TIPSY_GL_VERSION 0x1F02u
#define TIPSY_GL_EXTENSIONS 0x1F03u
#define TIPSY_GL_NUM_EXTENSIONS 0x821Du
#define TIPSY_GL_MAX_TEXTURE_SIZE 0x0D33u
#define TIPSY_GL_VIEWPORT 0x0BA2u
#define TIPSY_GL_SCISSOR_TEST 0x0C11u
#define TIPSY_GL_BLEND 0x0BE2u
#define TIPSY_GL_BLEND_SRC_RGB 0x80C9u
#define TIPSY_GL_BLEND_DST_RGB 0x80C8u
#define TIPSY_GL_BLEND_SRC_ALPHA 0x80CBu
#define TIPSY_GL_BLEND_DST_ALPHA 0x80CAu
#define TIPSY_GL_BLEND_EQUATION_RGB 0x8009u
#define TIPSY_GL_BLEND_EQUATION_ALPHA 0x883Du
#define TIPSY_GL_FUNC_ADD 0x8006u
#define TIPSY_GL_ONE 1u
#define TIPSY_GL_ONE_MINUS_SRC_ALPHA 0x0303u
#define TIPSY_GL_COLOR_WRITEMASK 0x0C23u
#define TIPSY_GL_DEPTH_TEST 0x0B71u
#define TIPSY_GL_DEPTH_WRITEMASK 0x0B72u
#define TIPSY_GL_STENCIL_TEST 0x0B90u
#define TIPSY_GL_CULL_FACE 0x0B44u
#define TIPSY_GL_FRAMEBUFFER 0x8D40u
#define TIPSY_GL_FRAMEBUFFER_BINDING 0x8CA6u
#define TIPSY_GL_READ_FRAMEBUFFER 0x8CA8u
#define TIPSY_GL_DRAW_FRAMEBUFFER 0x8CA9u
#define TIPSY_GL_READ_FRAMEBUFFER_BINDING 0x8CAAu
#define TIPSY_GL_DRAW_FRAMEBUFFER_BINDING 0x8CA6u
#define TIPSY_GL_CURRENT_PROGRAM 0x8B8Du
#define TIPSY_GL_ACTIVE_TEXTURE 0x84E0u
#define TIPSY_GL_TEXTURE0 0x84C0u
#define TIPSY_GL_TEXTURE_2D 0x0DE1u
#define TIPSY_GL_TEXTURE_BINDING_2D 0x8069u
#define TIPSY_GL_TEXTURE_MIN_FILTER 0x2801u
#define TIPSY_GL_TEXTURE_MAG_FILTER 0x2800u
#define TIPSY_GL_TEXTURE_WRAP_S 0x2802u
#define TIPSY_GL_TEXTURE_WRAP_T 0x2803u
#define TIPSY_GL_LINEAR 0x2601u
#define TIPSY_GL_CLAMP_TO_EDGE 0x812Fu
#define TIPSY_GL_UNPACK_ALIGNMENT 0x0CF5u
#define TIPSY_GL_UNPACK_ROW_LENGTH 0x0CF2u
#define TIPSY_GL_UNPACK_SKIP_ROWS 0x0CF3u
#define TIPSY_GL_UNPACK_SKIP_PIXELS 0x0CF4u
#define TIPSY_GL_UNPACK_SKIP_IMAGES 0x806Du
#define TIPSY_GL_UNPACK_IMAGE_HEIGHT 0x806Eu
#define TIPSY_GL_RGBA 0x1908u
#define TIPSY_GL_UNSIGNED_BYTE 0x1401u
#define TIPSY_GL_ARRAY_BUFFER 0x8892u
#define TIPSY_GL_ARRAY_BUFFER_BINDING 0x8894u
#define TIPSY_GL_PIXEL_UNPACK_BUFFER 0x88ECu
#define TIPSY_GL_PIXEL_UNPACK_BUFFER_BINDING 0x88EFu
#define TIPSY_GL_STATIC_DRAW 0x88E4u
#define TIPSY_GL_FLOAT 0x1406u
#define TIPSY_GL_TRIANGLE_STRIP 0x0005u
#define TIPSY_GL_VERTEX_SHADER 0x8B31u
#define TIPSY_GL_FRAGMENT_SHADER 0x8B30u
#define TIPSY_GL_COMPILE_STATUS 0x8B81u
#define TIPSY_GL_LINK_STATUS 0x8B82u
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_ENABLED 0x8622u
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_SIZE 0x8623u
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_STRIDE 0x8624u
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_TYPE 0x8625u
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_NORMALIZED 0x886Au
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_POINTER 0x8645u
#define TIPSY_GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING 0x889Fu
#define TIPSY_GL_VERTEX_ARRAY_BINDING 0x85B5u
#define TIPSY_GL_RASTERIZER_DISCARD 0x8C89u
#define TIPSY_GL_FRAMEBUFFER_SRGB 0x8DB9u
#define TIPSY_GL_SAMPLER_BINDING 0x8919u
#define TIPSY_GL_SAMPLE_ALPHA_TO_COVERAGE 0x809Eu
#define TIPSY_GL_SAMPLE_COVERAGE 0x80A0u
#define TIPSY_GL_SAMPLE_MASK 0x8E51u
#define TIPSY_GL_TRANSFORM_FEEDBACK_ACTIVE 0x8E24u

struct tipsy_gl_api {
	const unsigned char *(*GetString)(TipsyGLenum);
	const unsigned char *(*GetStringi)(TipsyGLenum, TipsyGLuint);
	void (*GetIntegerv)(TipsyGLenum, TipsyGLint *);
	void (*GetBooleanv)(TipsyGLenum, TipsyGLboolean *);
	TipsyGLboolean (*IsEnabled)(TipsyGLenum);
	void (*Enable)(TipsyGLenum);
	void (*Disable)(TipsyGLenum);
	void (*Viewport)(TipsyGLint, TipsyGLint, TipsyGLsizei, TipsyGLsizei);
	void (*ColorMask)(TipsyGLboolean, TipsyGLboolean, TipsyGLboolean, TipsyGLboolean);
	void (*BlendFuncSeparate)(TipsyGLenum, TipsyGLenum, TipsyGLenum, TipsyGLenum);
	void (*BlendEquationSeparate)(TipsyGLenum, TipsyGLenum);
	void (*DepthMask)(TipsyGLboolean);
	void (*BindFramebuffer)(TipsyGLenum, TipsyGLuint);
	void (*ActiveTexture)(TipsyGLenum);
	void (*BindTexture)(TipsyGLenum, TipsyGLuint);
	void (*TexParameteri)(TipsyGLenum, TipsyGLenum, TipsyGLint);
	void (*PixelStorei)(TipsyGLenum, TipsyGLint);
	void (*GenTextures)(TipsyGLsizei, TipsyGLuint *);
	void (*DeleteTextures)(TipsyGLsizei, const TipsyGLuint *);
	void (*TexImage2D)(TipsyGLenum, TipsyGLint, TipsyGLint, TipsyGLsizei,
		TipsyGLsizei, TipsyGLint, TipsyGLenum, TipsyGLenum, const void *);
	void (*TexSubImage2D)(TipsyGLenum, TipsyGLint, TipsyGLint, TipsyGLint,
		TipsyGLsizei, TipsyGLsizei, TipsyGLenum, TipsyGLenum, const void *);
	TipsyGLuint (*CreateShader)(TipsyGLenum);
	void (*ShaderSource)(TipsyGLuint, TipsyGLsizei, const char *const *, const TipsyGLint *);
	void (*CompileShader)(TipsyGLuint);
	void (*GetShaderiv)(TipsyGLuint, TipsyGLenum, TipsyGLint *);
	void (*DeleteShader)(TipsyGLuint);
	TipsyGLuint (*CreateProgram)(void);
	void (*AttachShader)(TipsyGLuint, TipsyGLuint);
	void (*BindAttribLocation)(TipsyGLuint, TipsyGLuint, const char *);
	void (*LinkProgram)(TipsyGLuint);
	void (*GetProgramiv)(TipsyGLuint, TipsyGLenum, TipsyGLint *);
	void (*DeleteProgram)(TipsyGLuint);
	void (*UseProgram)(TipsyGLuint);
	TipsyGLint (*GetUniformLocation)(TipsyGLuint, const char *);
	void (*Uniform1i)(TipsyGLint, TipsyGLint);
	void (*GenBuffers)(TipsyGLsizei, TipsyGLuint *);
	void (*DeleteBuffers)(TipsyGLsizei, const TipsyGLuint *);
	void (*BindBuffer)(TipsyGLenum, TipsyGLuint);
	void (*BufferData)(TipsyGLenum, TipsyGLsizeiptr, const void *, TipsyGLenum);
	void (*EnableVertexAttribArray)(TipsyGLuint);
	void (*DisableVertexAttribArray)(TipsyGLuint);
	void (*VertexAttribPointer)(TipsyGLuint, TipsyGLint, TipsyGLenum,
		TipsyGLboolean, TipsyGLsizei, const void *);
	void (*GetVertexAttribiv)(TipsyGLuint, TipsyGLenum, TipsyGLint *);
	void (*GetVertexAttribPointerv)(TipsyGLuint, TipsyGLenum, void **);
	void (*DrawArrays)(TipsyGLenum, TipsyGLint, TipsyGLsizei);
	void (*GenVertexArrays)(TipsyGLsizei, TipsyGLuint *);
	void (*DeleteVertexArrays)(TipsyGLsizei, const TipsyGLuint *);
	void (*BindVertexArray)(TipsyGLuint);
	void (*BindSampler)(TipsyGLuint, TipsyGLuint);
	int ready;
};

struct tipsy_egl_text_state {
	EGLDisplay display;
	EGLSurface surface;
	void *context;
	TipsyGLuint texture, program, vertex_buffer, vertex_array;
	TipsyGLint foreground_uniform;
	TipsyGLsizei texture_width, texture_height;
	TipsyGLint geometry_x, geometry_y, geometry_width, geometry_height;
	TipsyGLint geometry_surface_width, geometry_surface_height;
	uint64_t generation;
	int initialized, es3, srgb_write_control, geometry_valid;
	struct tipsy_egl_text_state *next;
};

struct tipsy_gl_attrib_state {
	TipsyGLint enabled, size, type, normalized, stride, buffer;
	void *pointer;
};

struct tipsy_gl_saved_state {
	TipsyGLint framebuffer, read_framebuffer, draw_framebuffer, program, array_buffer;
	TipsyGLint vertex_array, sampler_0, pixel_unpack_buffer;
	TipsyGLint viewport[4], active_texture, texture_2d, unpack_alignment;
	TipsyGLint unpack_row_length, unpack_skip_rows, unpack_skip_pixels;
	TipsyGLint unpack_skip_images, unpack_image_height;
	TipsyGLint blend_src_rgb, blend_dst_rgb, blend_src_alpha, blend_dst_alpha;
	TipsyGLint blend_equation_rgb, blend_equation_alpha;
	TipsyGLboolean color_mask[4], depth_mask;
	TipsyGLboolean scissor_enabled, blend_enabled, depth_enabled, stencil_enabled, cull_enabled;
	TipsyGLboolean rasterizer_discard_enabled, framebuffer_srgb_enabled;
	TipsyGLboolean sample_alpha_to_coverage_enabled, sample_coverage_enabled;
	TipsyGLboolean sample_mask_enabled, transform_feedback_active;
	struct tipsy_gl_attrib_state attrib[2];
};

static struct tipsy_gl_api egl_text_gl;
static pthread_mutex_t egl_text_mu = PTHREAD_MUTEX_INITIALIZER;
static struct tipsy_egl_text_state *egl_text_states;
static _Atomic uint32_t egl_text_cached_textures;
static void tipsy_egl_text_destroy_gpu_locked(struct tipsy_egl_text_state *state);

static int tipsy_egl_text_overlay_is_live(void)
{
	if (egl_text_overlay_live_fn != NULL) return egl_text_overlay_live_fn() != 0;
	return 0;
}

static void tipsy_egl_text_drop_texture_locked(struct tipsy_egl_text_state *state)
{
	if (state == NULL || state->texture == 0) return;
	if (egl_text_gl.ready && egl_text_gl.DeleteTextures != NULL) {
		egl_text_gl.DeleteTextures(1, &state->texture);
	}
	state->texture = 0;
	state->generation = 0;
	state->texture_width = state->texture_height = 0;
	atomic_fetch_sub_explicit(&egl_text_cached_textures, 1, memory_order_release);
}

/* A surface can die while its context is not current, in which case issuing
 * GL deletes would target no context or the wrong context. Retain only that
 * metadata as an orphan and reclaim its objects on the next exact context
 * presentation. EGL itself releases any remainder when the context dies. */
static void tipsy_egl_text_reap_orphans_locked(EGLDisplay display, void *context)
{
	struct tipsy_egl_text_state **cursor;
	for (cursor = &egl_text_states; *cursor != NULL;) {
		struct tipsy_egl_text_state *state = *cursor;
		if (state->surface != NULL || state->display != display ||
			state->context != context) {
			cursor = &state->next;
			continue;
		}
		*cursor = state->next;
		tipsy_egl_text_destroy_gpu_locked(state);
		free(state);
	}
}

/* guest_swap.go lives in the optional graphics package. The Android ABI layer
 * must remain independently test-linkable, so absent graphics exports are a
 * normal no-op rather than a linker error. */
#pragma weak GoEGLGuestSurfaceCreated
#pragma weak GoEGLGuestSwap
#pragma weak GoEGLGuestSurfaceDestroyed

typedef uint64_t (*tipsy_egl_guest_surface_created_fn)(uintptr_t, uintptr_t, uintptr_t);
typedef int (*tipsy_egl_guest_swap_fn)(uintptr_t, uintptr_t, uintptr_t, uint64_t);
typedef void (*tipsy_egl_guest_surface_destroyed_fn)(uintptr_t, uintptr_t, uintptr_t, uint64_t);

struct tipsy_egl_guest_surface {
	uintptr_t window;
	uintptr_t display;
	uintptr_t surface;
	uint64_t generation;
	/* A retained entry has two independent duties: a one-shot boot handoff and
	 * its eventual lifecycle destroy.  Do not discard the entry after graphics
	 * accepts the handoff, or numeric EGL handle reuse could lose the destroy
	 * generation. */
	int handoff_state;
	struct tipsy_egl_guest_surface *next;
};

enum {
	TIPSY_EGL_GUEST_HANDOFF_PENDING = 0,
	TIPSY_EGL_GUEST_HANDOFF_INFLIGHT = 1,
	TIPSY_EGL_GUEST_HANDOFF_COMPLETE = 2,
};

static pthread_mutex_t egl_guest_surfaces_mu = PTHREAD_MUTEX_INITIALIZER;
static struct tipsy_egl_guest_surface *egl_guest_surfaces;
/* The common post-boot path has no pending guest handoff.  This count lets a
 * successful host swap avoid taking the registry mutex or crossing C-to-Go
 * merely to rediscover that completed state. */
static _Atomic uint64_t egl_guest_pending_handoffs;

static uint64_t tipsy_egl_guest_surface_created(uintptr_t window, uintptr_t display,
	uintptr_t surface)
{
	if (GoEGLGuestSurfaceCreated == NULL) {
		return 0;
	}
	return GoEGLGuestSurfaceCreated(window, display, surface);
}

static int tipsy_egl_guest_swap(uintptr_t window, uintptr_t display, uintptr_t surface,
	uint64_t generation)
{
	if (GoEGLGuestSwap == NULL) {
		return 0;
	}
	return GoEGLGuestSwap(window, display, surface, generation);
}

static void tipsy_egl_guest_surface_destroyed(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation)
{
	if (GoEGLGuestSurfaceDestroyed != NULL) {
		GoEGLGuestSurfaceDestroyed(window, display, surface, generation);
	}
}

static tipsy_egl_guest_surface_created_fn egl_guest_surface_created_fn =
	tipsy_egl_guest_surface_created;
static tipsy_egl_guest_swap_fn egl_guest_swap_fn = tipsy_egl_guest_swap;
static tipsy_egl_guest_surface_destroyed_fn egl_guest_surface_destroyed_fn =
	tipsy_egl_guest_surface_destroyed;

/* All callbacks above are external (and can re-enter this shim), so the
 * registry lock only protects local identity storage. A swap/destroy takes a
 * value snapshot, drops the lock, and then calls graphics. Graphics performs
 * the final generation check, which makes a destroy racing that callback a
 * harmless rejected stale signal rather than a use-after-free. */
static struct tipsy_egl_guest_surface *tipsy_egl_guest_surface_take(uintptr_t display,
	uintptr_t surface)
{
	struct tipsy_egl_guest_surface **cursor;
	struct tipsy_egl_guest_surface *found = NULL;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (cursor = &egl_guest_surfaces; *cursor != NULL; cursor = &(*cursor)->next) {
		if ((*cursor)->display == display && (*cursor)->surface == surface) {
			found = *cursor;
			*cursor = found->next;
			found->next = NULL;
			if (found->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
				atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
					memory_order_release);
			}
			break;
		}
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	return found;
}

static void tipsy_egl_guest_surface_discard_window(uintptr_t window)
{
	struct tipsy_egl_guest_surface **cursor;
	struct tipsy_egl_guest_surface *discarded = NULL;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (cursor = &egl_guest_surfaces; *cursor != NULL;) {
		struct tipsy_egl_guest_surface *entry = *cursor;
		if (entry->window != window) {
			cursor = &entry->next;
			continue;
		}
		*cursor = entry->next;
		entry->next = discarded;
		discarded = entry;
		if (entry->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
			atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
		}
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	while (discarded != NULL) {
		struct tipsy_egl_guest_surface *next = discarded->next;
		free(discarded);
		discarded = next;
	}
}

static void tipsy_egl_guest_surface_store(struct tipsy_egl_guest_surface *entry)
{
	struct tipsy_egl_guest_surface **cursor;
	struct tipsy_egl_guest_surface *old = NULL;

	if (entry == NULL) {
		return;
	}
	/* Graphics allows one active guest surface per XID. A new surface for that
	 * window therefore invalidates all prior Android records, including a host
	 * address the driver later recycles. */
	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (cursor = &egl_guest_surfaces; *cursor != NULL;) {
		struct tipsy_egl_guest_surface *candidate = *cursor;
		if (candidate->window != entry->window) {
			cursor = &candidate->next;
			continue;
		}
		*cursor = candidate->next;
		candidate->next = old;
		old = candidate;
		if (candidate->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
			atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
		}
	}
	entry->next = egl_guest_surfaces;
	egl_guest_surfaces = entry;
	if (entry->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
		atomic_fetch_add_explicit(&egl_guest_pending_handoffs, 1,
			memory_order_release);
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	while (old != NULL) {
		struct tipsy_egl_guest_surface *next = old->next;
		free(old);
		old = next;
	}
}

/* Claim at most one pending signal before entering Go.  The in-flight state
 * suppresses a concurrent successful swap; if graphics rejects the signal,
 * finish below restores this exact still-live surface to pending. */
static int tipsy_egl_guest_surface_claim(uintptr_t display, uintptr_t surface,
	struct tipsy_egl_guest_surface *out)
{
	struct tipsy_egl_guest_surface *entry;
	int found = 0;

	if (out == NULL) {
		return 0;
	}
	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (entry = egl_guest_surfaces; entry != NULL; entry = entry->next) {
		if (entry->display == display && entry->surface == surface &&
		    entry->handoff_state == TIPSY_EGL_GUEST_HANDOFF_PENDING) {
			entry->handoff_state = TIPSY_EGL_GUEST_HANDOFF_INFLIGHT;
			atomic_fetch_sub_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
			*out = *entry;
			out->next = NULL;
			found = 1;
			break;
		}
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	return found;
}

static void tipsy_egl_guest_surface_finish(uintptr_t display, uintptr_t surface,
	uint64_t generation, int accepted)
{
	struct tipsy_egl_guest_surface *entry;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	for (entry = egl_guest_surfaces; entry != NULL; entry = entry->next) {
		if (entry->display != display || entry->surface != surface ||
		    entry->generation != generation ||
		    entry->handoff_state != TIPSY_EGL_GUEST_HANDOFF_INFLIGHT) {
			continue;
		}
		if (accepted) {
			entry->handoff_state = TIPSY_EGL_GUEST_HANDOFF_COMPLETE;
		} else {
			entry->handoff_state = TIPSY_EGL_GUEST_HANDOFF_PENDING;
			atomic_fetch_add_explicit(&egl_guest_pending_handoffs, 1,
				memory_order_release);
		}
		break;
	}
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
}

static void tipsy_egl_guest_surface_clear_all(void)
{
	struct tipsy_egl_guest_surface *entry;

	pthread_mutex_lock(&egl_guest_surfaces_mu);
	entry = egl_guest_surfaces;
	egl_guest_surfaces = NULL;
	atomic_store_explicit(&egl_guest_pending_handoffs, 0, memory_order_release);
	pthread_mutex_unlock(&egl_guest_surfaces_mu);
	while (entry != NULL) {
		struct tipsy_egl_guest_surface *next = entry->next;
		free(entry);
		entry = next;
	}
}

extern void GoAndroid_LogEGLSwapInterval(int enabled, int requested, int effective,
	int primary_ok, int primary_error, int fallback_ok, int fallback_error);

static void *open_lib(const char *name)
{
	return dlopen(name, RTLD_NOW | RTLD_LOCAL);
}

static void open_host_abi_libraries(void)
{
	lib_libc = open_lib("libc.so.6");
	lib_libm = open_lib("libm.so.6");
	lib_libz = open_lib("libz.so.1");
}

/* Resolve from one exact host ELF object. dlsym(handle, ...) also searches an
 * object's dependencies, so confirm the returned address belongs to the
 * requested object's own link_map before treating it as an owned Android ABI
 * capability. */
void *tipsy_host_dlsym_library(const char *lib, const char *name)
{
	void *handle = NULL;
	void *p;
	Dl_info info;
	struct link_map *map = NULL;

	if (lib == NULL || name == NULL) {
		return NULL;
	}
	pthread_once(&host_abi_once, open_host_abi_libraries);
	if (strcmp(lib, "libc.so") == 0) {
		handle = lib_libc;
	} else if (strcmp(lib, "libm.so") == 0) {
		handle = lib_libm;
	} else if (strcmp(lib, "libz.so") == 0) {
		handle = lib_libz;
	}
	if (handle == NULL) {
		return NULL;
	}
	p = dlsym(handle, name);
	if (p == NULL || dlinfo(handle, RTLD_DI_LINKMAP, &map) != 0 || map == NULL ||
	    dladdr(p, &info) == 0 || info.dli_fbase != (void *)map->l_addr) {
		return NULL;
	}
	return p;
}

void *tipsy_host_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	p = dlsym(RTLD_DEFAULT, name);
	if (p != NULL) {
		return p;
	}
	pthread_once(&host_abi_once, open_host_abi_libraries);
	if (lib_libc != NULL) {
		p = dlsym(lib_libc, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_libm != NULL) {
		p = dlsym(lib_libm, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_libz != NULL) {
		p = dlsym(lib_libz, name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

/* One-time EGL/GLES host library resolution. pthread_once caches both success
 * and failure, so a missing host EGL is not retried from every engine swap. */
static _Atomic int egl_init_calls;

static void open_egl_libraries(void)
{
	atomic_fetch_add_explicit(&egl_init_calls, 1, memory_order_relaxed);
	lib_egl = open_lib("libEGL.so.1");
	if (lib_egl == NULL) {
		lib_egl = open_lib("libEGL.so");
	}
	lib_gles = open_lib("libGLESv2.so.2");
	if (lib_gles == NULL) {
		lib_gles = open_lib("libGLESv2.so");
	}
	if (lib_egl != NULL) {
		host_eglGetProcAddress = (egl_get_proc_address_fn)dlsym(lib_egl, "eglGetProcAddress");
		host_eglGetPlatformDisplay = (egl_get_platform_display_fn)dlsym(lib_egl, "eglGetPlatformDisplay");
		host_eglGetPlatformDisplayEXT =
			(egl_get_platform_display_ext_fn)dlsym(lib_egl, "eglGetPlatformDisplayEXT");
		if (host_eglGetPlatformDisplayEXT == NULL && host_eglGetProcAddress != NULL) {
			host_eglGetPlatformDisplayEXT = (egl_get_platform_display_ext_fn)
				host_eglGetProcAddress("eglGetPlatformDisplayEXT");
		}
		host_eglGetDisplay = (egl_get_display_fn)dlsym(lib_egl, "eglGetDisplay");
		host_eglInitialize = (egl_initialize_fn)dlsym(lib_egl, "eglInitialize");
		host_eglChooseConfig = (egl_choose_config_fn)dlsym(lib_egl, "eglChooseConfig");
		host_eglCreateContext = (egl_create_context_fn)dlsym(lib_egl, "eglCreateContext");
		host_eglCreateWindowSurface = (egl_create_window_surface_fn)dlsym(lib_egl, "eglCreateWindowSurface");
		host_eglMakeCurrent = (egl_make_current_fn)dlsym(lib_egl, "eglMakeCurrent");
		host_eglSwapInterval = (egl_swap_interval_fn)dlsym(lib_egl, "eglSwapInterval");
		host_eglSwapBuffers = (egl_swap_buffers_fn)dlsym(lib_egl, "eglSwapBuffers");
		host_eglDestroySurface = (egl_destroy_surface_fn)dlsym(lib_egl, "eglDestroySurface");
		host_eglDestroyContext = (egl_destroy_context_fn)dlsym(lib_egl, "eglDestroyContext");
		host_eglGetError = (egl_get_error_fn)dlsym(lib_egl, "eglGetError");
		host_eglGetCurrentDisplay = (egl_get_current_display_fn)dlsym(lib_egl, "eglGetCurrentDisplay");
		host_eglGetCurrentSurface = (egl_get_current_surface_fn)dlsym(lib_egl, "eglGetCurrentSurface");
		host_eglGetCurrentContext = (egl_get_current_context_fn)dlsym(lib_egl, "eglGetCurrentContext");
		host_eglQuerySurface = (egl_query_surface_fn)dlsym(lib_egl, "eglQuerySurface");
	}
}

static void ensure_egl(void)
{
	pthread_once(&host_egl_once, open_egl_libraries);
}

static void *tipsy_egl_gl_proc(const char *name)
{
	void *p = NULL;
	if (lib_gles != NULL) {
		p = dlsym(lib_gles, name);
	}
	if (p == NULL && host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
	}
	return p;
}

/* Resolve only the GLES 2 core needed for an alpha upload and one textured
 * triangle strip.  The pre-present seam must fail closed when a host does not
 * provide the full surface; it must never replace text with an RGB/X11
 * rectangle. */
static int tipsy_egl_text_resolve_gl_locked(void)
{
	if (egl_text_gl.ready) return 1;
	ensure_egl();
	if (lib_gles == NULL) return 0;
#define TIPSY_LOAD_GL(member, type, symbol) \
	egl_text_gl.member = (type)tipsy_egl_gl_proc(symbol)
	TIPSY_LOAD_GL(GetString, const unsigned char *(*)(TipsyGLenum), "glGetString");
	TIPSY_LOAD_GL(GetStringi, const unsigned char *(*)(TipsyGLenum, TipsyGLuint), "glGetStringi");
	TIPSY_LOAD_GL(GetIntegerv, void (*)(TipsyGLenum, TipsyGLint *), "glGetIntegerv");
	TIPSY_LOAD_GL(GetBooleanv, void (*)(TipsyGLenum, TipsyGLboolean *), "glGetBooleanv");
	TIPSY_LOAD_GL(IsEnabled, TipsyGLboolean (*)(TipsyGLenum), "glIsEnabled");
	TIPSY_LOAD_GL(Enable, void (*)(TipsyGLenum), "glEnable");
	TIPSY_LOAD_GL(Disable, void (*)(TipsyGLenum), "glDisable");
	TIPSY_LOAD_GL(Viewport, void (*)(TipsyGLint, TipsyGLint, TipsyGLsizei, TipsyGLsizei), "glViewport");
	TIPSY_LOAD_GL(ColorMask, void (*)(TipsyGLboolean, TipsyGLboolean, TipsyGLboolean, TipsyGLboolean), "glColorMask");
	TIPSY_LOAD_GL(BlendFuncSeparate, void (*)(TipsyGLenum, TipsyGLenum, TipsyGLenum, TipsyGLenum), "glBlendFuncSeparate");
	TIPSY_LOAD_GL(BlendEquationSeparate, void (*)(TipsyGLenum, TipsyGLenum), "glBlendEquationSeparate");
	TIPSY_LOAD_GL(DepthMask, void (*)(TipsyGLboolean), "glDepthMask");
	TIPSY_LOAD_GL(BindFramebuffer, void (*)(TipsyGLenum, TipsyGLuint), "glBindFramebuffer");
	TIPSY_LOAD_GL(ActiveTexture, void (*)(TipsyGLenum), "glActiveTexture");
	TIPSY_LOAD_GL(BindTexture, void (*)(TipsyGLenum, TipsyGLuint), "glBindTexture");
	TIPSY_LOAD_GL(TexParameteri, void (*)(TipsyGLenum, TipsyGLenum, TipsyGLint), "glTexParameteri");
	TIPSY_LOAD_GL(PixelStorei, void (*)(TipsyGLenum, TipsyGLint), "glPixelStorei");
	TIPSY_LOAD_GL(GenTextures, void (*)(TipsyGLsizei, TipsyGLuint *), "glGenTextures");
	TIPSY_LOAD_GL(DeleteTextures, void (*)(TipsyGLsizei, const TipsyGLuint *), "glDeleteTextures");
	TIPSY_LOAD_GL(TexImage2D, void (*)(TipsyGLenum, TipsyGLint, TipsyGLint, TipsyGLsizei, TipsyGLsizei, TipsyGLint, TipsyGLenum, TipsyGLenum, const void *), "glTexImage2D");
	TIPSY_LOAD_GL(TexSubImage2D, void (*)(TipsyGLenum, TipsyGLint, TipsyGLint, TipsyGLint, TipsyGLsizei, TipsyGLsizei, TipsyGLenum, TipsyGLenum, const void *), "glTexSubImage2D");
	TIPSY_LOAD_GL(CreateShader, TipsyGLuint (*)(TipsyGLenum), "glCreateShader");
	TIPSY_LOAD_GL(ShaderSource, void (*)(TipsyGLuint, TipsyGLsizei, const char *const *, const TipsyGLint *), "glShaderSource");
	TIPSY_LOAD_GL(CompileShader, void (*)(TipsyGLuint), "glCompileShader");
	TIPSY_LOAD_GL(GetShaderiv, void (*)(TipsyGLuint, TipsyGLenum, TipsyGLint *), "glGetShaderiv");
	TIPSY_LOAD_GL(DeleteShader, void (*)(TipsyGLuint), "glDeleteShader");
	TIPSY_LOAD_GL(CreateProgram, TipsyGLuint (*)(void), "glCreateProgram");
	TIPSY_LOAD_GL(AttachShader, void (*)(TipsyGLuint, TipsyGLuint), "glAttachShader");
	TIPSY_LOAD_GL(BindAttribLocation, void (*)(TipsyGLuint, TipsyGLuint, const char *), "glBindAttribLocation");
	TIPSY_LOAD_GL(LinkProgram, void (*)(TipsyGLuint), "glLinkProgram");
	TIPSY_LOAD_GL(GetProgramiv, void (*)(TipsyGLuint, TipsyGLenum, TipsyGLint *), "glGetProgramiv");
	TIPSY_LOAD_GL(DeleteProgram, void (*)(TipsyGLuint), "glDeleteProgram");
	TIPSY_LOAD_GL(UseProgram, void (*)(TipsyGLuint), "glUseProgram");
	TIPSY_LOAD_GL(GetUniformLocation, TipsyGLint (*)(TipsyGLuint, const char *), "glGetUniformLocation");
	TIPSY_LOAD_GL(Uniform1i, void (*)(TipsyGLint, TipsyGLint), "glUniform1i");
	TIPSY_LOAD_GL(GenBuffers, void (*)(TipsyGLsizei, TipsyGLuint *), "glGenBuffers");
	TIPSY_LOAD_GL(DeleteBuffers, void (*)(TipsyGLsizei, const TipsyGLuint *), "glDeleteBuffers");
	TIPSY_LOAD_GL(BindBuffer, void (*)(TipsyGLenum, TipsyGLuint), "glBindBuffer");
	TIPSY_LOAD_GL(BufferData, void (*)(TipsyGLenum, TipsyGLsizeiptr, const void *, TipsyGLenum), "glBufferData");
	TIPSY_LOAD_GL(EnableVertexAttribArray, void (*)(TipsyGLuint), "glEnableVertexAttribArray");
	TIPSY_LOAD_GL(DisableVertexAttribArray, void (*)(TipsyGLuint), "glDisableVertexAttribArray");
	TIPSY_LOAD_GL(VertexAttribPointer, void (*)(TipsyGLuint, TipsyGLint, TipsyGLenum, TipsyGLboolean, TipsyGLsizei, const void *), "glVertexAttribPointer");
	TIPSY_LOAD_GL(GetVertexAttribiv, void (*)(TipsyGLuint, TipsyGLenum, TipsyGLint *), "glGetVertexAttribiv");
	TIPSY_LOAD_GL(GetVertexAttribPointerv, void (*)(TipsyGLuint, TipsyGLenum, void **), "glGetVertexAttribPointerv");
	TIPSY_LOAD_GL(DrawArrays, void (*)(TipsyGLenum, TipsyGLint, TipsyGLsizei), "glDrawArrays");
	TIPSY_LOAD_GL(GenVertexArrays, void (*)(TipsyGLsizei, TipsyGLuint *), "glGenVertexArrays");
	TIPSY_LOAD_GL(DeleteVertexArrays, void (*)(TipsyGLsizei, const TipsyGLuint *), "glDeleteVertexArrays");
	TIPSY_LOAD_GL(BindVertexArray, void (*)(TipsyGLuint), "glBindVertexArray");
	TIPSY_LOAD_GL(BindSampler, void (*)(TipsyGLuint, TipsyGLuint), "glBindSampler");
#undef TIPSY_LOAD_GL
	if (egl_text_gl.GetString == NULL || egl_text_gl.GetIntegerv == NULL ||
		egl_text_gl.GetBooleanv == NULL || egl_text_gl.IsEnabled == NULL ||
		egl_text_gl.Enable == NULL || egl_text_gl.Disable == NULL ||
		egl_text_gl.Viewport == NULL || egl_text_gl.ColorMask == NULL ||
		egl_text_gl.BlendFuncSeparate == NULL || egl_text_gl.BlendEquationSeparate == NULL ||
		egl_text_gl.DepthMask == NULL || egl_text_gl.BindFramebuffer == NULL ||
		egl_text_gl.ActiveTexture == NULL || egl_text_gl.BindTexture == NULL ||
		egl_text_gl.TexParameteri == NULL || egl_text_gl.PixelStorei == NULL ||
		egl_text_gl.GenTextures == NULL || egl_text_gl.DeleteTextures == NULL ||
		egl_text_gl.TexImage2D == NULL || egl_text_gl.TexSubImage2D == NULL ||
		egl_text_gl.CreateShader == NULL ||
		egl_text_gl.ShaderSource == NULL || egl_text_gl.CompileShader == NULL ||
		egl_text_gl.GetShaderiv == NULL || egl_text_gl.DeleteShader == NULL ||
		egl_text_gl.CreateProgram == NULL || egl_text_gl.AttachShader == NULL ||
		egl_text_gl.BindAttribLocation == NULL || egl_text_gl.LinkProgram == NULL ||
		egl_text_gl.GetProgramiv == NULL || egl_text_gl.DeleteProgram == NULL ||
		egl_text_gl.UseProgram == NULL || egl_text_gl.GetUniformLocation == NULL ||
		egl_text_gl.Uniform1i == NULL || egl_text_gl.GenBuffers == NULL ||
		egl_text_gl.DeleteBuffers == NULL || egl_text_gl.BindBuffer == NULL ||
		egl_text_gl.BufferData == NULL || egl_text_gl.EnableVertexAttribArray == NULL ||
		egl_text_gl.DisableVertexAttribArray == NULL || egl_text_gl.VertexAttribPointer == NULL ||
		egl_text_gl.GetVertexAttribiv == NULL || egl_text_gl.GetVertexAttribPointerv == NULL ||
		egl_text_gl.DrawArrays == NULL) {
		memset(&egl_text_gl, 0, sizeof(egl_text_gl));
		return 0;
	}
	egl_text_gl.ready = 1;
	return 1;
}

static struct tipsy_egl_text_state *tipsy_egl_text_find_locked(EGLDisplay display,
	EGLSurface surface, void *context, int create)
{
	struct tipsy_egl_text_state *state;
	tipsy_egl_text_reap_orphans_locked(display, context);
	for (state = egl_text_states; state != NULL; state = state->next) {
		if (state->display == display && state->surface == surface && state->context == context)
			return state;
	}
	if (!create) return NULL;
	state = calloc(1, sizeof(*state));
	if (state == NULL) return NULL;
	state->display = display;
	state->surface = surface;
	state->context = context;
	state->next = egl_text_states;
	egl_text_states = state;
	return state;
}

static void tipsy_egl_text_destroy_gpu_locked(struct tipsy_egl_text_state *state)
{
	if (state == NULL || !egl_text_gl.ready) return;
	tipsy_egl_text_drop_texture_locked(state);
	if (state->vertex_array != 0 && egl_text_gl.DeleteVertexArrays != NULL)
		egl_text_gl.DeleteVertexArrays(1, &state->vertex_array);
	if (state->vertex_buffer != 0) egl_text_gl.DeleteBuffers(1, &state->vertex_buffer);
	if (state->program != 0) egl_text_gl.DeleteProgram(state->program);
	state->texture = state->vertex_buffer = state->vertex_array = state->program = 0;
	state->initialized = 0;
	state->generation = 0;
	state->texture_width = state->texture_height = 0;
	state->geometry_valid = 0;
}

static void tipsy_egl_text_save_attrib(TipsyGLuint index, struct tipsy_gl_attrib_state *out)
{
	egl_text_gl.GetVertexAttribiv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_ENABLED, &out->enabled);
	egl_text_gl.GetVertexAttribiv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_SIZE, &out->size);
	egl_text_gl.GetVertexAttribiv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_TYPE, &out->type);
	egl_text_gl.GetVertexAttribiv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_NORMALIZED, &out->normalized);
	egl_text_gl.GetVertexAttribiv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_STRIDE, &out->stride);
	egl_text_gl.GetVertexAttribiv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING, &out->buffer);
	egl_text_gl.GetVertexAttribPointerv(index, TIPSY_GL_VERTEX_ATTRIB_ARRAY_POINTER, &out->pointer);
}

static void tipsy_egl_text_restore_attrib(TipsyGLuint index, const struct tipsy_gl_attrib_state *in)
{
	/* GLES 2 permits client-memory attribute arrays when ARRAY_BUFFER is zero,
	 * so both the buffer-backed and zero-buffer forms must be replayed exactly.
	 * GLES 3 never takes this path: its private VAO isolates every attribute. */
	egl_text_gl.BindBuffer(TIPSY_GL_ARRAY_BUFFER, (TipsyGLuint)in->buffer);
	egl_text_gl.VertexAttribPointer(index, in->size, (TipsyGLenum)in->type,
		(TipsyGLboolean)in->normalized, in->stride, in->pointer);
	if (in->enabled != 0) egl_text_gl.EnableVertexAttribArray(index);
	else egl_text_gl.DisableVertexAttribArray(index);
}

static int tipsy_egl_text_has_srgb_write_control(int es3)
{
	static const char extension_name[] = "GL_EXT_sRGB_write_control";
	if (es3) {
		TipsyGLint count = 0;
		TipsyGLint i;
		if (egl_text_gl.GetStringi == NULL) return 0;
		egl_text_gl.GetIntegerv(TIPSY_GL_NUM_EXTENSIONS, &count);
		if (count < 0 || count > 65536) return 0;
		for (i = 0; i < count; i++) {
			const unsigned char *extension = egl_text_gl.GetStringi(
				TIPSY_GL_EXTENSIONS, (TipsyGLuint)i);
			if (extension != NULL && strcmp((const char *)extension, extension_name) == 0)
				return 1;
		}
		return 0;
	}
	{
		const char *extensions = (const char *)egl_text_gl.GetString(TIPSY_GL_EXTENSIONS);
		const char *cursor = extensions;
		if (cursor == NULL) return 0;
		while (*cursor != '\0') {
			const char *end;
			while (*cursor == ' ') cursor++;
			end = cursor;
			while (*end != '\0' && *end != ' ') end++;
			if ((size_t)(end - cursor) == sizeof(extension_name) - 1 &&
				memcmp(cursor, extension_name, sizeof(extension_name) - 1) == 0)
				return 1;
			cursor = end;
		}
	}
	return 0;
}

static void tipsy_egl_text_save_state(struct tipsy_gl_saved_state *saved,
	const struct tipsy_egl_text_state *state)
{
	memset(saved, 0, sizeof(*saved));
	if (state->es3) {
		egl_text_gl.GetIntegerv(TIPSY_GL_READ_FRAMEBUFFER_BINDING, &saved->read_framebuffer);
		egl_text_gl.GetIntegerv(TIPSY_GL_DRAW_FRAMEBUFFER_BINDING, &saved->draw_framebuffer);
		egl_text_gl.GetIntegerv(TIPSY_GL_VERTEX_ARRAY_BINDING, &saved->vertex_array);
		egl_text_gl.GetIntegerv(TIPSY_GL_PIXEL_UNPACK_BUFFER_BINDING,
			&saved->pixel_unpack_buffer);
	} else {
		egl_text_gl.GetIntegerv(TIPSY_GL_FRAMEBUFFER_BINDING, &saved->framebuffer);
	}
	egl_text_gl.GetIntegerv(TIPSY_GL_VIEWPORT, saved->viewport);
	egl_text_gl.GetIntegerv(TIPSY_GL_CURRENT_PROGRAM, &saved->program);
	egl_text_gl.GetIntegerv(TIPSY_GL_ARRAY_BUFFER_BINDING, &saved->array_buffer);
	egl_text_gl.GetIntegerv(TIPSY_GL_ACTIVE_TEXTURE, &saved->active_texture);
	egl_text_gl.ActiveTexture(TIPSY_GL_TEXTURE0);
	egl_text_gl.GetIntegerv(TIPSY_GL_TEXTURE_BINDING_2D, &saved->texture_2d);
	if (state->es3) egl_text_gl.GetIntegerv(TIPSY_GL_SAMPLER_BINDING, &saved->sampler_0);
	egl_text_gl.GetIntegerv(TIPSY_GL_UNPACK_ALIGNMENT, &saved->unpack_alignment);
	if (state->es3) {
		egl_text_gl.GetIntegerv(TIPSY_GL_UNPACK_ROW_LENGTH, &saved->unpack_row_length);
		egl_text_gl.GetIntegerv(TIPSY_GL_UNPACK_SKIP_ROWS, &saved->unpack_skip_rows);
		egl_text_gl.GetIntegerv(TIPSY_GL_UNPACK_SKIP_PIXELS, &saved->unpack_skip_pixels);
		egl_text_gl.GetIntegerv(TIPSY_GL_UNPACK_SKIP_IMAGES, &saved->unpack_skip_images);
		egl_text_gl.GetIntegerv(TIPSY_GL_UNPACK_IMAGE_HEIGHT, &saved->unpack_image_height);
	}
	egl_text_gl.GetIntegerv(TIPSY_GL_BLEND_SRC_RGB, &saved->blend_src_rgb);
	egl_text_gl.GetIntegerv(TIPSY_GL_BLEND_DST_RGB, &saved->blend_dst_rgb);
	egl_text_gl.GetIntegerv(TIPSY_GL_BLEND_SRC_ALPHA, &saved->blend_src_alpha);
	egl_text_gl.GetIntegerv(TIPSY_GL_BLEND_DST_ALPHA, &saved->blend_dst_alpha);
	egl_text_gl.GetIntegerv(TIPSY_GL_BLEND_EQUATION_RGB, &saved->blend_equation_rgb);
	egl_text_gl.GetIntegerv(TIPSY_GL_BLEND_EQUATION_ALPHA, &saved->blend_equation_alpha);
	egl_text_gl.GetBooleanv(TIPSY_GL_COLOR_WRITEMASK, saved->color_mask);
	egl_text_gl.GetBooleanv(TIPSY_GL_DEPTH_WRITEMASK, &saved->depth_mask);
	saved->scissor_enabled = egl_text_gl.IsEnabled(TIPSY_GL_SCISSOR_TEST);
	saved->blend_enabled = egl_text_gl.IsEnabled(TIPSY_GL_BLEND);
	saved->depth_enabled = egl_text_gl.IsEnabled(TIPSY_GL_DEPTH_TEST);
	saved->stencil_enabled = egl_text_gl.IsEnabled(TIPSY_GL_STENCIL_TEST);
	saved->cull_enabled = egl_text_gl.IsEnabled(TIPSY_GL_CULL_FACE);
	if (state->es3) {
		saved->rasterizer_discard_enabled =
			egl_text_gl.IsEnabled(TIPSY_GL_RASTERIZER_DISCARD);
		saved->sample_mask_enabled = egl_text_gl.IsEnabled(TIPSY_GL_SAMPLE_MASK);
		egl_text_gl.GetBooleanv(TIPSY_GL_TRANSFORM_FEEDBACK_ACTIVE,
			&saved->transform_feedback_active);
	} else {
		tipsy_egl_text_save_attrib(0, &saved->attrib[0]);
		tipsy_egl_text_save_attrib(1, &saved->attrib[1]);
	}
	saved->sample_alpha_to_coverage_enabled =
		egl_text_gl.IsEnabled(TIPSY_GL_SAMPLE_ALPHA_TO_COVERAGE);
	saved->sample_coverage_enabled = egl_text_gl.IsEnabled(TIPSY_GL_SAMPLE_COVERAGE);
	if (state->srgb_write_control) {
		saved->framebuffer_srgb_enabled =
			egl_text_gl.IsEnabled(TIPSY_GL_FRAMEBUFFER_SRGB);
	}
}

static void tipsy_egl_text_restore_state(const struct tipsy_gl_saved_state *saved,
	const struct tipsy_egl_text_state *state)
{
	if (state->es3) {
		egl_text_gl.BindVertexArray((TipsyGLuint)saved->vertex_array);
	} else {
		tipsy_egl_text_restore_attrib(0, &saved->attrib[0]);
		tipsy_egl_text_restore_attrib(1, &saved->attrib[1]);
	}
	egl_text_gl.BindBuffer(TIPSY_GL_ARRAY_BUFFER, (TipsyGLuint)saved->array_buffer);
	if (state->es3) {
		egl_text_gl.BindBuffer(TIPSY_GL_PIXEL_UNPACK_BUFFER,
			(TipsyGLuint)saved->pixel_unpack_buffer);
	}
	if (state->es3) {
		egl_text_gl.BindFramebuffer(TIPSY_GL_READ_FRAMEBUFFER, (TipsyGLuint)saved->read_framebuffer);
		egl_text_gl.BindFramebuffer(TIPSY_GL_DRAW_FRAMEBUFFER, (TipsyGLuint)saved->draw_framebuffer);
	} else {
		egl_text_gl.BindFramebuffer(TIPSY_GL_FRAMEBUFFER, (TipsyGLuint)saved->framebuffer);
	}
	egl_text_gl.Viewport(saved->viewport[0], saved->viewport[1], saved->viewport[2], saved->viewport[3]);
	if (saved->scissor_enabled) egl_text_gl.Enable(TIPSY_GL_SCISSOR_TEST);
	else egl_text_gl.Disable(TIPSY_GL_SCISSOR_TEST);
	egl_text_gl.ColorMask(saved->color_mask[0], saved->color_mask[1], saved->color_mask[2], saved->color_mask[3]);
	if (saved->blend_enabled) egl_text_gl.Enable(TIPSY_GL_BLEND);
	else egl_text_gl.Disable(TIPSY_GL_BLEND);
	egl_text_gl.BlendFuncSeparate((TipsyGLenum)saved->blend_src_rgb,
		(TipsyGLenum)saved->blend_dst_rgb, (TipsyGLenum)saved->blend_src_alpha,
		(TipsyGLenum)saved->blend_dst_alpha);
	egl_text_gl.BlendEquationSeparate((TipsyGLenum)saved->blend_equation_rgb,
		(TipsyGLenum)saved->blend_equation_alpha);
	if (saved->depth_enabled) egl_text_gl.Enable(TIPSY_GL_DEPTH_TEST);
	else egl_text_gl.Disable(TIPSY_GL_DEPTH_TEST);
	egl_text_gl.DepthMask(saved->depth_mask);
	if (saved->stencil_enabled) egl_text_gl.Enable(TIPSY_GL_STENCIL_TEST);
	else egl_text_gl.Disable(TIPSY_GL_STENCIL_TEST);
	if (saved->cull_enabled) egl_text_gl.Enable(TIPSY_GL_CULL_FACE);
	else egl_text_gl.Disable(TIPSY_GL_CULL_FACE);
	if (state->es3) {
		if (saved->rasterizer_discard_enabled)
			egl_text_gl.Enable(TIPSY_GL_RASTERIZER_DISCARD);
		else
			egl_text_gl.Disable(TIPSY_GL_RASTERIZER_DISCARD);
		if (saved->sample_mask_enabled) egl_text_gl.Enable(TIPSY_GL_SAMPLE_MASK);
		else egl_text_gl.Disable(TIPSY_GL_SAMPLE_MASK);
	}
	if (saved->sample_alpha_to_coverage_enabled)
		egl_text_gl.Enable(TIPSY_GL_SAMPLE_ALPHA_TO_COVERAGE);
	else
		egl_text_gl.Disable(TIPSY_GL_SAMPLE_ALPHA_TO_COVERAGE);
	if (saved->sample_coverage_enabled) egl_text_gl.Enable(TIPSY_GL_SAMPLE_COVERAGE);
	else egl_text_gl.Disable(TIPSY_GL_SAMPLE_COVERAGE);
	if (state->srgb_write_control) {
		if (saved->framebuffer_srgb_enabled)
			egl_text_gl.Enable(TIPSY_GL_FRAMEBUFFER_SRGB);
		else
			egl_text_gl.Disable(TIPSY_GL_FRAMEBUFFER_SRGB);
	}
	egl_text_gl.UseProgram((TipsyGLuint)saved->program);
	egl_text_gl.ActiveTexture(TIPSY_GL_TEXTURE0);
	egl_text_gl.BindTexture(TIPSY_GL_TEXTURE_2D, (TipsyGLuint)saved->texture_2d);
	if (state->es3) egl_text_gl.BindSampler(0, (TipsyGLuint)saved->sampler_0);
	egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_ALIGNMENT, saved->unpack_alignment);
	if (state->es3) {
		egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_ROW_LENGTH, saved->unpack_row_length);
		egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_SKIP_ROWS, saved->unpack_skip_rows);
		egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_SKIP_PIXELS, saved->unpack_skip_pixels);
		egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_SKIP_IMAGES, saved->unpack_skip_images);
		egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_IMAGE_HEIGHT, saved->unpack_image_height);
	}
	egl_text_gl.ActiveTexture((TipsyGLenum)saved->active_texture);
}

static TipsyGLuint tipsy_egl_text_compile_shader(TipsyGLenum kind, const char *source)
{
	TipsyGLuint shader = egl_text_gl.CreateShader(kind);
	TipsyGLint ok = 0;
	if (shader == 0) return 0;
	egl_text_gl.ShaderSource(shader, 1, &source, NULL);
	egl_text_gl.CompileShader(shader);
	egl_text_gl.GetShaderiv(shader, TIPSY_GL_COMPILE_STATUS, &ok);
	if (ok != 0) return shader;
	egl_text_gl.DeleteShader(shader);
	return 0;
}

static int tipsy_egl_text_create_texture_locked(struct tipsy_egl_text_state *state)
{
	if (state->texture != 0) return 1;
	egl_text_gl.GenTextures(1, &state->texture);
	if (state->texture == 0) return 0;
	atomic_fetch_add_explicit(&egl_text_cached_textures, 1, memory_order_release);
	egl_text_gl.ActiveTexture(TIPSY_GL_TEXTURE0);
	egl_text_gl.BindTexture(TIPSY_GL_TEXTURE_2D, state->texture);
	egl_text_gl.TexParameteri(TIPSY_GL_TEXTURE_2D, TIPSY_GL_TEXTURE_MIN_FILTER, TIPSY_GL_LINEAR);
	egl_text_gl.TexParameteri(TIPSY_GL_TEXTURE_2D, TIPSY_GL_TEXTURE_MAG_FILTER, TIPSY_GL_LINEAR);
	egl_text_gl.TexParameteri(TIPSY_GL_TEXTURE_2D, TIPSY_GL_TEXTURE_WRAP_S, TIPSY_GL_CLAMP_TO_EDGE);
	egl_text_gl.TexParameteri(TIPSY_GL_TEXTURE_2D, TIPSY_GL_TEXTURE_WRAP_T, TIPSY_GL_CLAMP_TO_EDGE);
	state->texture_width = state->texture_height = 0;
	return 1;
}

static int tipsy_egl_text_initialize_locked(struct tipsy_egl_text_state *state)
{
	static const char vertex_source[] =
		"attribute vec2 a_position;\n"
		"attribute vec2 a_texcoord;\n"
		"varying vec2 v_texcoord;\n"
		"void main() { gl_Position = vec4(a_position, 0.0, 1.0); v_texcoord = a_texcoord; }\n";
	static const char fragment_source[] =
		"precision mediump float;\n"
		"varying vec2 v_texcoord;\n"
		"uniform sampler2D u_foreground;\n"
		"void main() { gl_FragColor = texture2D(u_foreground, v_texcoord); }\n";
	TipsyGLuint vertex = 0, fragment = 0;
	TipsyGLint linked = 0;
	if (state == NULL || !egl_text_gl.ready) return 0;
	if (state->initialized) return 1;
	if (state->es3 && (egl_text_gl.GenVertexArrays == NULL ||
		egl_text_gl.DeleteVertexArrays == NULL || egl_text_gl.BindVertexArray == NULL ||
		egl_text_gl.BindSampler == NULL))
		return 0;
	vertex = tipsy_egl_text_compile_shader(TIPSY_GL_VERTEX_SHADER, vertex_source);
	fragment = tipsy_egl_text_compile_shader(TIPSY_GL_FRAGMENT_SHADER, fragment_source);
	if (vertex == 0 || fragment == 0) goto fail;
	state->program = egl_text_gl.CreateProgram();
	if (state->program == 0) goto fail;
	egl_text_gl.AttachShader(state->program, vertex);
	egl_text_gl.AttachShader(state->program, fragment);
	egl_text_gl.BindAttribLocation(state->program, 0, "a_position");
	egl_text_gl.BindAttribLocation(state->program, 1, "a_texcoord");
	egl_text_gl.LinkProgram(state->program);
	egl_text_gl.GetProgramiv(state->program, TIPSY_GL_LINK_STATUS, &linked);
	if (linked == 0) goto fail;
	state->foreground_uniform = egl_text_gl.GetUniformLocation(state->program, "u_foreground");
	if (state->foreground_uniform < 0) goto fail;
	egl_text_gl.GenBuffers(1, &state->vertex_buffer);
	if (state->es3) egl_text_gl.GenVertexArrays(1, &state->vertex_array);
	if (!tipsy_egl_text_create_texture_locked(state) || state->vertex_buffer == 0 ||
		(state->es3 && state->vertex_array == 0)) goto fail;
	if (state->es3) {
		egl_text_gl.BindVertexArray(state->vertex_array);
		egl_text_gl.BindBuffer(TIPSY_GL_ARRAY_BUFFER, state->vertex_buffer);
		egl_text_gl.EnableVertexAttribArray(0);
		egl_text_gl.EnableVertexAttribArray(1);
		egl_text_gl.VertexAttribPointer(0, 2, TIPSY_GL_FLOAT, TIPSY_GL_FALSE,
			4 * (TipsyGLsizei)sizeof(TipsyGLfloat), (const void *)0);
		egl_text_gl.VertexAttribPointer(1, 2, TIPSY_GL_FLOAT, TIPSY_GL_FALSE,
			4 * (TipsyGLsizei)sizeof(TipsyGLfloat),
			(const void *)(2 * sizeof(TipsyGLfloat)));
	}
	egl_text_gl.DeleteShader(vertex);
	egl_text_gl.DeleteShader(fragment);
	state->initialized = 1;
	return 1;
fail:
	if (vertex != 0) egl_text_gl.DeleteShader(vertex);
	if (fragment != 0) egl_text_gl.DeleteShader(fragment);
	tipsy_egl_text_destroy_gpu_locked(state);
	return 0;
}

static int tipsy_egl_text_frame_valid(const struct tipsy_focused_text_frame *frame,
	TipsyGLint max_texture)
{
	size_t width;
	if (frame == NULL || frame->rgba == NULL || frame->lease == 0 || frame->generation == 0 ||
		frame->width < 1 || frame->height < 1 || frame->stride < 1 || max_texture < 1)
		return 0;
	if (frame->width > max_texture || frame->height > max_texture) return 0;
	width = (size_t)frame->width;
	if (width > SIZE_MAX / 4 || frame->stride != (int)(width * 4)) return 0;
	return 1;
}

/* Compose exactly one leased foreground into the current EGL draw surface.
 * The caller always releases the lease before forwarding the real swap. A
 * zero acquire means focus ended; its GPU texture is deleted the first time
 * that context next presents. */
static void tipsy_egl_compose_focused_text(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_focused_text_frame frame = {0};
	struct tipsy_egl_text_state *state;
	struct tipsy_gl_saved_state saved;
	EGLDisplay current_display;
	EGLSurface current_surface;
	void *current_context;
	EGLint surface_width = 0, surface_height = 0, swap_behavior = 0;
	TipsyGLint max_texture = 0;
	int acquired, es3;
	const unsigned char *version;
	if (egl_text_frame_acquire_fn == NULL || egl_text_frame_release_fn == NULL ||
		(egl_text_frame_acquire_fn == tipsy_egl_text_frame_acquire &&
		 (tipsy_focused_text_frame_acquire == NULL ||
		  tipsy_focused_text_frame_release == NULL))) {
		return;
	}
	if (!tipsy_egl_text_overlay_is_live() &&
		atomic_load_explicit(&egl_text_cached_textures, memory_order_acquire) == 0) {
		return;
	}
	ensure_egl();
	if (host_eglGetCurrentDisplay == NULL || host_eglGetCurrentSurface == NULL ||
		host_eglGetCurrentContext == NULL || host_eglQuerySurface == NULL) {
		return;
	}
	current_display = host_eglGetCurrentDisplay();
	current_surface = host_eglGetCurrentSurface(TIPSY_EGL_DRAW);
	current_context = host_eglGetCurrentContext();
	if (current_display != dpy || current_surface != surface || current_context == NULL) {
		return;
	}
	acquired = egl_text_frame_acquire_fn(&frame);
	if (acquired != 0 && acquired != 1) {
		return;
	}
	pthread_mutex_lock(&egl_text_mu);
	if (acquired == 0) {
		state = tipsy_egl_text_find_locked(dpy, surface, current_context, 0);
		if (state != NULL && state->texture != 0 && egl_text_gl.ready) {
			tipsy_egl_text_save_state(&saved, state);
			tipsy_egl_text_drop_texture_locked(state);
			tipsy_egl_text_restore_state(&saved, state);
		}
		goto out;
	}
	if (host_eglQuerySurface(dpy, surface, TIPSY_EGL_SWAP_BEHAVIOR,
		&swap_behavior) != TIPSY_EGL_TRUE) {
		goto out;
	}
	if (swap_behavior != TIPSY_EGL_BUFFER_DESTROYED) goto out;
	if (host_eglQuerySurface(dpy, surface, TIPSY_EGL_WIDTH, &surface_width) != TIPSY_EGL_TRUE ||
		host_eglQuerySurface(dpy, surface, TIPSY_EGL_HEIGHT, &surface_height) != TIPSY_EGL_TRUE ||
		surface_width < 1 || surface_height < 1) {
		goto out;
	}
	if (!tipsy_egl_text_resolve_gl_locked()) {
		goto out;
	}
	version = egl_text_gl.GetString(TIPSY_GL_VERSION);
	if (version == NULL || strncmp((const char *)version, "OpenGL ES ", 10) != 0 ||
		(((const char *)version)[10] != '2' && ((const char *)version)[10] != '3')) {
		goto out;
	}
	es3 = ((const char *)version)[10] == '3';
	state = tipsy_egl_text_find_locked(dpy, surface, current_context, acquired == 1);
	if (state == NULL) {
		goto out;
	}
	if (state->initialized && state->es3 != es3) {
		goto out;
	}
	state->es3 = es3;
	if (!state->initialized) {
		state->srgb_write_control = tipsy_egl_text_has_srgb_write_control(es3);
	}
	tipsy_egl_text_save_state(&saved, state);
	if (state->es3 && saved.transform_feedback_active) {
		tipsy_egl_text_restore_state(&saved, state);
		goto out;
	}
	egl_text_gl.GetIntegerv(TIPSY_GL_MAX_TEXTURE_SIZE, &max_texture);
	if (!tipsy_egl_text_frame_valid(&frame, max_texture)) {
		tipsy_egl_text_restore_state(&saved, state);
		goto out;
	}
	if (!tipsy_egl_text_initialize_locked(state)) {
		tipsy_egl_text_restore_state(&saved, state);
		goto out;
	}
	if (!tipsy_egl_text_create_texture_locked(state)) {
		tipsy_egl_text_restore_state(&saved, state);
		goto out;
	}
	egl_text_gl.ActiveTexture(TIPSY_GL_TEXTURE0);
	egl_text_gl.BindTexture(TIPSY_GL_TEXTURE_2D, state->texture);
	if (state->generation != frame.generation) {
		if (state->es3) {
			egl_text_gl.BindBuffer(TIPSY_GL_PIXEL_UNPACK_BUFFER, 0);
			egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_ROW_LENGTH, 0);
			egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_SKIP_ROWS, 0);
			egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_SKIP_PIXELS, 0);
			egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_SKIP_IMAGES, 0);
			egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_IMAGE_HEIGHT, 0);
		}
		egl_text_gl.PixelStorei(TIPSY_GL_UNPACK_ALIGNMENT, 1);
		if (state->texture_width == frame.width && state->texture_height == frame.height) {
			egl_text_gl.TexSubImage2D(TIPSY_GL_TEXTURE_2D, 0, 0, 0, frame.width,
				frame.height, TIPSY_GL_RGBA, TIPSY_GL_UNSIGNED_BYTE, frame.rgba);
		} else {
			egl_text_gl.TexImage2D(TIPSY_GL_TEXTURE_2D, 0, TIPSY_GL_RGBA, frame.width,
				frame.height, 0, TIPSY_GL_RGBA, TIPSY_GL_UNSIGNED_BYTE, frame.rgba);
			state->texture_width = frame.width;
			state->texture_height = frame.height;
		}
		state->generation = frame.generation;
	}
	if (!state->geometry_valid || state->geometry_x != frame.x ||
		state->geometry_y != frame.y || state->geometry_width != frame.width ||
		state->geometry_height != frame.height ||
		state->geometry_surface_width != surface_width ||
		state->geometry_surface_height != surface_height) {
		const TipsyGLfloat left = (TipsyGLfloat)((2.0 * (double)frame.x / surface_width) - 1.0);
		const TipsyGLfloat right = (TipsyGLfloat)((2.0 * ((double)frame.x + (double)frame.width) / surface_width) - 1.0);
		const TipsyGLfloat top = (TipsyGLfloat)(1.0 - (2.0 * (double)frame.y / surface_height));
		const TipsyGLfloat bottom = (TipsyGLfloat)(1.0 - (2.0 * ((double)frame.y + (double)frame.height) / surface_height));
		const TipsyGLfloat vertices[] = {
			left, top, 0.0f, 0.0f, right, top, 1.0f, 0.0f,
			left, bottom, 0.0f, 1.0f, right, bottom, 1.0f, 1.0f,
		};
		egl_text_gl.BindBuffer(TIPSY_GL_ARRAY_BUFFER, state->vertex_buffer);
		egl_text_gl.BufferData(TIPSY_GL_ARRAY_BUFFER, (TipsyGLsizeiptr)sizeof(vertices), vertices, TIPSY_GL_STATIC_DRAW);
		state->geometry_x = frame.x;
		state->geometry_y = frame.y;
		state->geometry_width = frame.width;
		state->geometry_height = frame.height;
		state->geometry_surface_width = surface_width;
		state->geometry_surface_height = surface_height;
		state->geometry_valid = 1;
	}
	if (state->es3) {
		egl_text_gl.BindVertexArray(state->vertex_array);
		egl_text_gl.BindSampler(0, 0);
	} else {
		egl_text_gl.BindBuffer(TIPSY_GL_ARRAY_BUFFER, state->vertex_buffer);
		egl_text_gl.EnableVertexAttribArray(0);
		egl_text_gl.EnableVertexAttribArray(1);
		egl_text_gl.VertexAttribPointer(0, 2, TIPSY_GL_FLOAT, TIPSY_GL_FALSE,
			4 * (TipsyGLsizei)sizeof(TipsyGLfloat), (const void *)0);
		egl_text_gl.VertexAttribPointer(1, 2, TIPSY_GL_FLOAT, TIPSY_GL_FALSE,
			4 * (TipsyGLsizei)sizeof(TipsyGLfloat), (const void *)(2 * sizeof(TipsyGLfloat)));
	}
	if (state->es3) egl_text_gl.BindFramebuffer(TIPSY_GL_DRAW_FRAMEBUFFER, 0);
	else egl_text_gl.BindFramebuffer(TIPSY_GL_FRAMEBUFFER, 0);
	egl_text_gl.Viewport(0, 0, surface_width, surface_height);
	egl_text_gl.Disable(TIPSY_GL_SCISSOR_TEST);
	egl_text_gl.Disable(TIPSY_GL_DEPTH_TEST);
	egl_text_gl.Disable(TIPSY_GL_STENCIL_TEST);
	egl_text_gl.Disable(TIPSY_GL_CULL_FACE);
	if (state->es3) egl_text_gl.Disable(TIPSY_GL_RASTERIZER_DISCARD);
	if (state->es3) egl_text_gl.Disable(TIPSY_GL_SAMPLE_MASK);
	egl_text_gl.Disable(TIPSY_GL_SAMPLE_ALPHA_TO_COVERAGE);
	egl_text_gl.Disable(TIPSY_GL_SAMPLE_COVERAGE);
	if (state->srgb_write_control) egl_text_gl.Disable(TIPSY_GL_FRAMEBUFFER_SRGB);
	egl_text_gl.DepthMask(TIPSY_GL_FALSE);
	egl_text_gl.ColorMask(TIPSY_GL_TRUE, TIPSY_GL_TRUE, TIPSY_GL_TRUE, TIPSY_GL_TRUE);
	egl_text_gl.Enable(TIPSY_GL_BLEND);
	egl_text_gl.BlendEquationSeparate(TIPSY_GL_FUNC_ADD, TIPSY_GL_FUNC_ADD);
	egl_text_gl.BlendFuncSeparate(TIPSY_GL_ONE, TIPSY_GL_ONE_MINUS_SRC_ALPHA,
		TIPSY_GL_ONE, TIPSY_GL_ONE_MINUS_SRC_ALPHA);
	egl_text_gl.UseProgram(state->program);
	egl_text_gl.Uniform1i(state->foreground_uniform, 0);
	egl_text_gl.DrawArrays(TIPSY_GL_TRIANGLE_STRIP, 0, 4);
	tipsy_egl_text_restore_state(&saved, state);
out:
	pthread_mutex_unlock(&egl_text_mu);
	if (acquired == 1) {
		egl_text_frame_release_fn(frame.lease);
	}
}

static void tipsy_egl_text_forget(EGLDisplay dpy, EGLSurface surface, void *context)
{
	struct tipsy_egl_text_state **cursor;
	EGLDisplay current_display = NULL;
	void *current_context = NULL;
	if (host_eglGetCurrentDisplay != NULL && host_eglGetCurrentContext != NULL) {
		current_display = host_eglGetCurrentDisplay();
		current_context = host_eglGetCurrentContext();
	}
	pthread_mutex_lock(&egl_text_mu);
	for (cursor = &egl_text_states; *cursor != NULL;) {
		struct tipsy_egl_text_state *state = *cursor;
		if (state->display != dpy || (surface != NULL && state->surface != surface) ||
			(context != NULL && state->context != context)) {
			cursor = &state->next;
			continue;
		}
		if (surface != NULL && context == NULL && state->initialized &&
			(current_display != dpy || current_context != state->context)) {
			/* Keep an unaddressable surface tombstone until this exact context is
			 * current again. It cannot match a recycled EGLSurface value. */
			state->surface = NULL;
			state->generation = 0;
			state->geometry_valid = 0;
			cursor = &state->next;
			continue;
		}
		*cursor = state->next;
		if (surface != NULL && current_display == dpy &&
			current_context == state->context) {
			tipsy_egl_text_destroy_gpu_locked(state);
		}
		/* A context destroy releases any objects that could not be deleted on
		 * an exact current-context turn. Never make another context current. */
		free(state);
	}
	pthread_mutex_unlock(&egl_text_mu);
}

static void tipsy_egl_record_successful_swap(uint64_t now_ns)
{
	uint64_t expected = 0;
	atomic_compare_exchange_strong_explicit(&egl_first_swap_ns, &expected, now_ns,
		memory_order_acq_rel, memory_order_acquire);
	atomic_store_explicit(&egl_last_swap_ns, now_ns, memory_order_release);
	atomic_fetch_add_explicit(&egl_successful_swaps, 1, memory_order_acq_rel);
}

static uint64_t tipsy_monotonic_ns(void)
{
	struct timespec ts;
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return 0;
	}
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

static void tipsy_egl_note_successful_swap(void)
{
	uint64_t now_ns;

	if (!atomic_load_explicit(&egl_present_stats_enabled, memory_order_relaxed)) {
		return;
	}
	now_ns = tipsy_monotonic_ns();
	if (now_ns != 0) {
		tipsy_egl_record_successful_swap(now_ns);
	}
}

void tipsy_egl_set_present_stats(int enabled)
{
	atomic_store_explicit(&egl_present_stats_enabled, enabled != 0, memory_order_relaxed);
}

int tipsy_egl_present_stats_enabled(void)
{
	return atomic_load_explicit(&egl_present_stats_enabled, memory_order_relaxed);
}

void tipsy_egl_reset_swap_stats(void)
{
	atomic_store_explicit(&egl_successful_swaps, 0, memory_order_release);
	atomic_store_explicit(&egl_first_swap_ns, 0, memory_order_release);
	atomic_store_explicit(&egl_last_swap_ns, 0, memory_order_release);
}

void tipsy_egl_swap_stats(uint64_t *successful_swaps, uint64_t *first_ns,
	uint64_t *last_ns)
{
	if (successful_swaps != NULL) {
		*successful_swaps = atomic_load_explicit(&egl_successful_swaps, memory_order_acquire);
	}
	if (first_ns != NULL) {
		*first_ns = atomic_load_explicit(&egl_first_swap_ns, memory_order_acquire);
	}
	if (last_ns != NULL) {
		*last_ns = atomic_load_explicit(&egl_last_swap_ns, memory_order_acquire);
	}
}

void tipsy_test_egl_record_swap(uint64_t now_ns)
{
	tipsy_egl_record_successful_swap(now_ns);
}

void tipsy_test_egl_note_successful_swap(void)
{
	tipsy_egl_note_successful_swap();
}

void tipsy_egl_set_vsync(int enabled)
{
	atomic_store_explicit(&egl_vsync_enabled, enabled != 0, memory_order_release);
}

int tipsy_egl_vsync_enabled(void)
{
	return atomic_load_explicit(&egl_vsync_enabled, memory_order_acquire);
}

EGLDisplay tipsy_eglGetPlatformDisplay(EGLenum platform, void *native_display,
	const EGLAttrib *attrib_list)
{
	EGLDisplay display;

	ensure_egl();
	if (host_eglGetPlatformDisplay == NULL) {
		tipsy_egl_setup_log_missing("eglGetPlatformDisplay");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_PLATFORM_DISPLAY,
			TIPSY_EGL_SETUP_ABSENCE);
		return NULL;
	}
	display = host_eglGetPlatformDisplay(platform, native_display, attrib_list);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_PLATFORM_DISPLAY,
		display != NULL ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return display;
}

EGLDisplay tipsy_eglGetPlatformDisplayEXT(EGLenum platform, void *native_display,
	const EGLint *attrib_list)
{
	EGLDisplay display;

	ensure_egl();
	if (host_eglGetPlatformDisplayEXT == NULL) {
		tipsy_egl_setup_log_missing("eglGetPlatformDisplayEXT");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT,
			TIPSY_EGL_SETUP_ABSENCE);
		return NULL;
	}
	display = host_eglGetPlatformDisplayEXT(platform, native_display, attrib_list);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT,
		display != NULL ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return display;
}

EGLDisplay tipsy_eglGetDisplay(void *native_display)
{
	EGLDisplay display;

	ensure_egl();
	if (host_eglGetDisplay == NULL) {
		tipsy_egl_setup_log_missing("eglGetDisplay");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_DISPLAY,
			TIPSY_EGL_SETUP_ABSENCE);
		return NULL;
	}
	display = host_eglGetDisplay(native_display);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_DISPLAY,
		display != NULL ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return display;
}

EGLBoolean tipsy_eglInitialize(EGLDisplay dpy, EGLint *major, EGLint *minor)
{
	EGLBoolean ok;

	ensure_egl();
	if (host_eglInitialize == NULL) {
		tipsy_egl_setup_log_missing("eglInitialize");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_INITIALIZE,
			TIPSY_EGL_SETUP_ABSENCE);
		return TIPSY_EGL_FALSE;
	}
	ok = host_eglInitialize(dpy, major, minor);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_INITIALIZE,
		ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return ok;
}

EGLBoolean tipsy_eglChooseConfig(EGLDisplay dpy, const EGLint *attrib_list,
	EGLConfig *configs, EGLint config_size, EGLint *num_config)
{
	EGLBoolean ok;

	ensure_egl();
	if (host_eglChooseConfig == NULL) {
		tipsy_egl_setup_log_missing("eglChooseConfig");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_CHOOSE_CONFIG,
			TIPSY_EGL_SETUP_ABSENCE);
		return TIPSY_EGL_FALSE;
	}
	ok = host_eglChooseConfig(dpy, attrib_list, configs, config_size, num_config);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_CHOOSE_CONFIG,
		ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return ok;
}

EGLContext tipsy_eglCreateContext(EGLDisplay dpy, EGLConfig config,
	EGLContext share_context, const EGLint *attrib_list)
{
	EGLContext context;

	ensure_egl();
	if (host_eglCreateContext == NULL) {
		tipsy_egl_setup_log_missing("eglCreateContext");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_CREATE_CONTEXT,
			TIPSY_EGL_SETUP_ABSENCE);
		return NULL;
	}
	context = host_eglCreateContext(dpy, config, share_context, attrib_list);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_CREATE_CONTEXT,
		context != NULL ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return context;
}

EGLBoolean tipsy_eglMakeCurrent(EGLDisplay dpy, EGLSurface draw,
	EGLSurface read, EGLContext context)
{
	EGLBoolean ok;

	ensure_egl();
	if (host_eglMakeCurrent == NULL) {
		tipsy_egl_setup_log_missing("eglMakeCurrent");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_MAKE_CURRENT,
			TIPSY_EGL_SETUP_ABSENCE);
		return TIPSY_EGL_FALSE;
	}
	ok = host_eglMakeCurrent(dpy, draw, read, context);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_MAKE_CURRENT,
		ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	return ok;
}

EGLBoolean tipsy_eglSwapInterval(EGLDisplay dpy, EGLint interval)
{
	EGLBoolean primary_ok;
	EGLBoolean fallback_ok = TIPSY_EGL_FALSE;
	EGLint primary_error;
	EGLint fallback_error = TIPSY_EGL_SUCCESS;
	int vsync;
	EGLint desired;

	ensure_egl();
	if (host_eglSwapInterval == NULL) {
		GoAndroid_LogMissing("eglSwapInterval");
		return TIPSY_EGL_FALSE;
	}
	vsync = tipsy_egl_vsync_enabled();
	desired = vsync ? 1 : 0;
	primary_ok = host_eglSwapInterval(dpy, desired);
	if (primary_ok == TIPSY_EGL_TRUE) {
		/* A new client presentation epoch (notably the transition from
		 * Landing to an experience surface) owns a fresh rate window. */
		tipsy_egl_reset_swap_stats();
		GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 1,
			TIPSY_EGL_SUCCESS, 0, TIPSY_EGL_SUCCESS);
		return TIPSY_EGL_TRUE;
	}

	if (desired == interval) {
		/* Do not consume a failed passthrough call's thread-local EGL error;
		 * the official client owns the next eglGetError. */
		GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 0, 0, 0,
			TIPSY_EGL_SUCCESS);
		return TIPSY_EGL_FALSE;
	}

	primary_error = host_eglGetError != NULL ? host_eglGetError() : 0;
	/* Preserve the official client's exact request when the host rejects the
	 * independent VSync policy. */
	fallback_ok = host_eglSwapInterval(dpy, interval);
	/* As with ordinary passthrough failure, leave fallback's EGL error for
	 * the client. Zero means deliberately unconsumed, not EGL_SUCCESS. */
	fallback_error = fallback_ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SUCCESS : 0;
	if (fallback_ok == TIPSY_EGL_TRUE) {
		tipsy_egl_reset_swap_stats();
	}
	GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 0, primary_error,
		fallback_ok == TIPSY_EGL_TRUE, fallback_error);
	return fallback_ok;
}

EGLBoolean tipsy_eglSwapBuffers(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_surface guest;
	EGLBoolean ok;

	ensure_egl();
	if (host_eglSwapBuffers == NULL) {
		tipsy_egl_setup_log_missing("eglSwapBuffers");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_FIRST_SWAP,
			TIPSY_EGL_SETUP_ABSENCE);
		return TIPSY_EGL_FALSE;
	}
	/* The host-owned, same-frame composition point. The foreground is a
	 * premultiplied-alpha glyph/caret texture, so it requires no X11 compositor
	 * and cannot synthesize a game background. */
	tipsy_egl_compose_focused_text(dpy, surface);
	ok = host_eglSwapBuffers(dpy, surface);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_FIRST_SWAP,
		ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	if (ok == TIPSY_EGL_TRUE) {
		tipsy_egl_note_successful_swap();
		if (atomic_load_explicit(&egl_guest_pending_handoffs, memory_order_acquire) != 0 &&
		    tipsy_egl_guest_surface_claim((uintptr_t)dpy, (uintptr_t)surface, &guest)) {
			/* The bridge is deliberately after the host return: a failed client
			 * present never reaches the graphics sentinel. A nonzero response is
			 * one accepted boot handoff; retain the local identity only for the
			 * later generation-checked destroy/replacement lifecycle. */
			int accepted = egl_guest_swap_fn(guest.window, guest.display, guest.surface,
				guest.generation);
			tipsy_egl_guest_surface_finish(guest.display, guest.surface,
				guest.generation, accepted != 0);
		}
	}
	return ok;
}

EGLSurface tipsy_eglCreateWindowSurface(EGLDisplay dpy, EGLConfig config, void *native_window, const EGLint *attrib_list)
{
	struct tipsy_egl_guest_surface *guest;
	EGLSurface surface;
	void *host_win = native_window;
	uintptr_t xid = 0;
	uint64_t generation;

	ensure_egl();
	if (tipsy_is_anative_window(native_window)) {
		xid = tipsy_ANativeWindow_get_handle(native_window);
		host_win = (void *)xid;
	}
	if (host_eglCreateWindowSurface == NULL) {
		tipsy_egl_setup_log_missing("eglCreateWindowSurface");
		tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE,
			TIPSY_EGL_SETUP_ABSENCE);
		return NULL;
	}
	surface = host_eglCreateWindowSurface(dpy, config, host_win, attrib_list);
	tipsy_egl_setup_trace_result(TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE,
		surface != NULL ? TIPSY_EGL_SETUP_SUCCESS : TIPSY_EGL_SETUP_FAILURE);
	if (surface == NULL || xid == 0 || dpy == NULL) {
		return surface;
	}
	/* Reserve local storage before publishing the guest surface. If allocation
	 * fails, leave graphics unnotified: a non-retainable generation must never
	 * later be signaled or destroyed as though it were tracked. */
	guest = calloc(1, sizeof(*guest));
	if (guest == NULL) {
		return surface;
	}
	/* Prevent an old Android record for the same XID from signaling while the
	 * graphics router replaces its surface mapping in the callback below. */
	tipsy_egl_guest_surface_discard_window(xid);
	generation = egl_guest_surface_created_fn(xid, (uintptr_t)dpy, (uintptr_t)surface);
	if (generation == 0) {
		free(guest);
		return surface;
	}
	guest->window = xid;
	guest->display = (uintptr_t)dpy;
	guest->surface = (uintptr_t)surface;
	guest->generation = generation;
	guest->handoff_state = TIPSY_EGL_GUEST_HANDOFF_PENDING;
	tipsy_egl_guest_surface_store(guest);
	return surface;
}

EGLBoolean tipsy_eglDestroySurface(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_surface *guest;
	EGLBoolean ok;

	ensure_egl();
	if (host_eglDestroySurface == NULL) {
		GoAndroid_LogMissing("eglDestroySurface");
		return TIPSY_EGL_FALSE;
	}
	/* Invalidate before forwarding to the host. This means a numeric host
	 * surface reuse cannot revive the old generation, even if destruction and a
	 * successful swap race on separate callers. */
	guest = tipsy_egl_guest_surface_take((uintptr_t)dpy, (uintptr_t)surface);
	if (guest != NULL) {
		egl_guest_surface_destroyed_fn(guest->window, guest->display, guest->surface,
			guest->generation);
		free(guest);
	}
	/* Drop the exact surface key before the host may recycle its numeric value.
	 * Every foreground lease is released before a host swap is entered. */
	tipsy_egl_text_forget(dpy, surface, NULL);
	ok = host_eglDestroySurface(dpy, surface);
	return ok;
}

EGLBoolean tipsy_eglDestroyContext(EGLDisplay dpy, void *context)
{
	EGLBoolean ok;

	ensure_egl();
	if (host_eglDestroyContext == NULL) {
		GoAndroid_LogMissing("eglDestroyContext");
		return TIPSY_EGL_FALSE;
	}
	/* Context addresses may be reused. This only invalidates host foreground
	 * metadata for the exact dying context; it neither makes a context current
	 * nor changes ordinary guest EGL behavior. */
	tipsy_egl_text_forget(dpy, NULL, context);
	ok = host_eglDestroyContext(dpy, context);
	return ok;
}

/* Both dlsym and eglGetProcAddress must expose the same compatibility
 * wrappers. Keep that identity table in one place so new EGL seams cannot be
 * accidentally visible through only one loader path. */
static void *tipsy_egl_wrapped_proc(const char *name)
{
	if (name == NULL) return NULL;
	if (strcmp(name, "eglGetPlatformDisplay") == 0)
		return (void *)tipsy_eglGetPlatformDisplay;
	if (strcmp(name, "eglGetPlatformDisplayEXT") == 0)
		return (void *)tipsy_eglGetPlatformDisplayEXT;
	if (strcmp(name, "eglGetDisplay") == 0) return (void *)tipsy_eglGetDisplay;
	if (strcmp(name, "eglInitialize") == 0) return (void *)tipsy_eglInitialize;
	if (strcmp(name, "eglChooseConfig") == 0) return (void *)tipsy_eglChooseConfig;
	if (strcmp(name, "eglCreateContext") == 0) return (void *)tipsy_eglCreateContext;
	if (strcmp(name, "eglCreateWindowSurface") == 0)
		return (void *)tipsy_eglCreateWindowSurface;
	if (strcmp(name, "eglMakeCurrent") == 0) return (void *)tipsy_eglMakeCurrent;
	if (strcmp(name, "eglSwapInterval") == 0) return (void *)tipsy_eglSwapInterval;
	if (strcmp(name, "eglSwapBuffers") == 0) return (void *)tipsy_eglSwapBuffers;
	if (strcmp(name, "eglDestroySurface") == 0) return (void *)tipsy_eglDestroySurface;
	if (strcmp(name, "eglDestroyContext") == 0) return (void *)tipsy_eglDestroyContext;
	if (strcmp(name, "eglGetProcAddress") == 0) return (void *)tipsy_eglGetProcAddress;
	return NULL;
}

void *tipsy_eglGetProcAddress(const char *name)
{
	void *p;

	ensure_egl();
	if (name != NULL && strcmp(name, "eglGetPlatformDisplayEXT") == 0 &&
		host_eglGetPlatformDisplayEXT == NULL) {
		tipsy_egl_trace_platform_display_ext_resolver_absence();
		return NULL;
	}
	tipsy_egl_trace_resolver_name(name);
	p = tipsy_egl_wrapped_proc(name);
	if (p != NULL) return p;
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_egl != NULL && name != NULL) {
		p = dlsym(lib_egl, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_gles != NULL && name != NULL) {
		p = dlsym(lib_gles, name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

int tipsy_test_egl_proc_is_wrapped(const char *name)
{
	void *got = tipsy_eglGetProcAddress(name);
	return got != NULL && got == tipsy_egl_wrapped_proc(name);
}

int tipsy_test_egl_init_calls(void)
{
	return atomic_load_explicit(&egl_init_calls, memory_order_relaxed);
}

int tipsy_test_egl_setup_trace_value_enabled(const char *value)
{
	return tipsy_egl_setup_trace_value_enabled(value);
}

struct tipsy_egl_setup_trace_event {
	uint32_t stage;
	uint32_t outcome;
};

struct tipsy_egl_setup_trace_fixture {
	uint32_t passed;
	uint32_t first_failure;
	uint32_t event_count;
	struct tipsy_egl_setup_trace_event events[8];
};

static uint32_t egl_setup_fixture_failure_stage;
static struct tipsy_egl_setup_trace_fixture *active_egl_setup_fixture;

static EGLDisplay tipsy_test_egl_setup_platform_display(EGLenum platform,
	void *native_display, const EGLAttrib *attrib_list)
{
	(void)platform;
	(void)native_display;
	(void)attrib_list;
	return egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_PLATFORM_DISPLAY ?
		NULL : (EGLDisplay)(uintptr_t)0x101;
}

static EGLDisplay tipsy_test_egl_setup_platform_display_ext(EGLenum platform,
	void *native_display, const EGLint *attrib_list)
{
	(void)platform;
	(void)native_display;
	(void)attrib_list;
	return egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT ?
		NULL : (EGLDisplay)(uintptr_t)0x102;
}

static EGLDisplay tipsy_test_egl_setup_display(void *native_display)
{
	(void)native_display;
	return egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_DISPLAY ?
		NULL : (EGLDisplay)(uintptr_t)0x103;
}

static EGLBoolean tipsy_test_egl_setup_initialize(EGLDisplay dpy,
	EGLint *major, EGLint *minor)
{
	(void)dpy;
	if (egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_INITIALIZE) {
		return TIPSY_EGL_FALSE;
	}
	if (major != NULL) *major = 1;
	if (minor != NULL) *minor = 5;
	return TIPSY_EGL_TRUE;
}

static EGLBoolean tipsy_test_egl_setup_choose_config(EGLDisplay dpy,
	const EGLint *attrib_list, EGLConfig *configs, EGLint config_size,
	EGLint *num_config)
{
	(void)dpy;
	(void)attrib_list;
	(void)config_size;
	if (egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_CHOOSE_CONFIG) {
		return TIPSY_EGL_FALSE;
	}
	if (configs != NULL) *configs = (EGLConfig)(uintptr_t)0x201;
	if (num_config != NULL) *num_config = 1;
	return TIPSY_EGL_TRUE;
}

static EGLContext tipsy_test_egl_setup_create_context(EGLDisplay dpy,
	EGLConfig config, EGLContext share_context, const EGLint *attrib_list)
{
	(void)dpy;
	(void)config;
	(void)share_context;
	(void)attrib_list;
	return egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_CREATE_CONTEXT ?
		NULL : (EGLContext)(uintptr_t)0x301;
}

static EGLSurface tipsy_test_egl_setup_create_window_surface(EGLDisplay dpy,
	EGLConfig config, void *native_window, const EGLint *attrib_list)
{
	(void)dpy;
	(void)config;
	(void)native_window;
	(void)attrib_list;
	return egl_setup_fixture_failure_stage ==
		TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE ?
		NULL : (EGLSurface)(uintptr_t)0x401;
}

static EGLBoolean tipsy_test_egl_setup_make_current(EGLDisplay dpy,
	EGLSurface draw, EGLSurface read, EGLContext context)
{
	(void)dpy;
	(void)draw;
	(void)read;
	(void)context;
	return egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_MAKE_CURRENT ?
		TIPSY_EGL_FALSE : TIPSY_EGL_TRUE;
}

static EGLBoolean tipsy_test_egl_setup_swap(EGLDisplay dpy, EGLSurface surface)
{
	(void)dpy;
	(void)surface;
	return egl_setup_fixture_failure_stage == TIPSY_EGL_SETUP_FIRST_SWAP ?
		TIPSY_EGL_FALSE : TIPSY_EGL_TRUE;
}

static void tipsy_test_egl_setup_sink(uint32_t stage, uint32_t outcome)
{
	struct tipsy_egl_setup_trace_fixture *fixture = active_egl_setup_fixture;
	uint32_t index;

	if (fixture == NULL) return;
	index = fixture->event_count++;
	if (index < sizeof(fixture->events) / sizeof(fixture->events[0])) {
		fixture->events[index].stage = stage;
		fixture->events[index].outcome = outcome;
	}
	if (fixture->first_failure == 0 && outcome != TIPSY_EGL_SETUP_SUCCESS) {
		fixture->first_failure = stage;
	}
}

static int tipsy_egl_setup_trace_fixture(uint32_t acquisition_stage,
	uint32_t failure_stage, uint32_t failure_outcome,
	struct tipsy_egl_setup_trace_fixture *out)
{
	egl_get_platform_display_fn saved_platform_display;
	egl_get_platform_display_ext_fn saved_platform_display_ext;
	egl_get_display_fn saved_display;
	egl_initialize_fn saved_initialize;
	egl_choose_config_fn saved_choose_config;
	egl_create_context_fn saved_create_context;
	egl_create_window_surface_fn saved_create_window_surface;
	egl_make_current_fn saved_make_current;
	egl_swap_buffers_fn saved_swap;
	tipsy_egl_setup_test_sink_fn saved_sink;
	int saved_suppress_missing;
	EGLDisplay display = NULL;
	EGLConfig config = NULL;
	EGLContext context = NULL;
	EGLSurface surface = NULL;
	EGLint major = 0;
	EGLint minor = 0;
	EGLint config_count = 0;
	uint32_t expected_events = 7;
	uint32_t last;
	int passed = 0;

	if (out == NULL || (acquisition_stage != TIPSY_EGL_SETUP_PLATFORM_DISPLAY &&
		acquisition_stage != TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT &&
		acquisition_stage != TIPSY_EGL_SETUP_DISPLAY) ||
		(failure_stage != 0 && (failure_stage < TIPSY_EGL_SETUP_PLATFORM_DISPLAY ||
		failure_stage > TIPSY_EGL_SETUP_FIRST_SWAP)) ||
		(failure_outcome != TIPSY_EGL_SETUP_FAILURE &&
		failure_outcome != TIPSY_EGL_SETUP_ABSENCE)) {
		return 0;
	}
	if (failure_stage == TIPSY_EGL_SETUP_PLATFORM_DISPLAY ||
		failure_stage == TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT ||
		failure_stage == TIPSY_EGL_SETUP_DISPLAY) {
		if (failure_stage != acquisition_stage) return 0;
	}

	ensure_egl();
	memset(out, 0, sizeof(*out));
	saved_platform_display = host_eglGetPlatformDisplay;
	saved_platform_display_ext = host_eglGetPlatformDisplayEXT;
	saved_display = host_eglGetDisplay;
	saved_initialize = host_eglInitialize;
	saved_choose_config = host_eglChooseConfig;
	saved_create_context = host_eglCreateContext;
	saved_create_window_surface = host_eglCreateWindowSurface;
	saved_make_current = host_eglMakeCurrent;
	saved_swap = host_eglSwapBuffers;
	saved_sink = egl_setup_test_sink;
	saved_suppress_missing = egl_setup_fixture_suppress_missing;

	host_eglGetPlatformDisplay = tipsy_test_egl_setup_platform_display;
	host_eglGetPlatformDisplayEXT = tipsy_test_egl_setup_platform_display_ext;
	host_eglGetDisplay = tipsy_test_egl_setup_display;
	host_eglInitialize = tipsy_test_egl_setup_initialize;
	host_eglChooseConfig = tipsy_test_egl_setup_choose_config;
	host_eglCreateContext = tipsy_test_egl_setup_create_context;
	host_eglCreateWindowSurface = tipsy_test_egl_setup_create_window_surface;
	host_eglMakeCurrent = tipsy_test_egl_setup_make_current;
	host_eglSwapBuffers = tipsy_test_egl_setup_swap;
	egl_setup_fixture_failure_stage =
		failure_outcome == TIPSY_EGL_SETUP_FAILURE ? failure_stage : 0;
	if (failure_outcome == TIPSY_EGL_SETUP_ABSENCE) {
		switch (failure_stage) {
		case TIPSY_EGL_SETUP_PLATFORM_DISPLAY: host_eglGetPlatformDisplay = NULL; break;
		case TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT: host_eglGetPlatformDisplayEXT = NULL; break;
		case TIPSY_EGL_SETUP_DISPLAY: host_eglGetDisplay = NULL; break;
		case TIPSY_EGL_SETUP_INITIALIZE: host_eglInitialize = NULL; break;
		case TIPSY_EGL_SETUP_CHOOSE_CONFIG: host_eglChooseConfig = NULL; break;
		case TIPSY_EGL_SETUP_CREATE_CONTEXT: host_eglCreateContext = NULL; break;
		case TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE: host_eglCreateWindowSurface = NULL; break;
		case TIPSY_EGL_SETUP_MAKE_CURRENT: host_eglMakeCurrent = NULL; break;
		case TIPSY_EGL_SETUP_FIRST_SWAP: host_eglSwapBuffers = NULL; break;
		default: break;
		}
	}
	active_egl_setup_fixture = out;
	egl_setup_test_sink = tipsy_test_egl_setup_sink;
	egl_setup_fixture_suppress_missing = 1;

	if (acquisition_stage == TIPSY_EGL_SETUP_PLATFORM_DISPLAY) {
		display = tipsy_eglGetPlatformDisplay(0, NULL, NULL);
	} else if (acquisition_stage == TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT) {
		display = tipsy_eglGetPlatformDisplayEXT(0, NULL, NULL);
	} else {
		display = tipsy_eglGetDisplay(NULL);
	}
	if (display == NULL) goto done;
	if (tipsy_eglInitialize(display, &major, &minor) != TIPSY_EGL_TRUE) goto done;
	if (tipsy_eglChooseConfig(display, NULL, &config, 1, &config_count) !=
		TIPSY_EGL_TRUE) goto done;
	context = tipsy_eglCreateContext(display, config, NULL, NULL);
	if (context == NULL) goto done;
	surface = tipsy_eglCreateWindowSurface(display, config, NULL, NULL);
	if (surface == NULL) goto done;
	if (tipsy_eglMakeCurrent(display, surface, surface, context) !=
		TIPSY_EGL_TRUE) goto done;
	(void)tipsy_eglSwapBuffers(display, surface);

done:
	if (failure_stage == 0) {
		passed = out->first_failure == 0 && out->event_count == expected_events &&
			out->events[expected_events - 1].stage == TIPSY_EGL_SETUP_FIRST_SWAP &&
			out->events[expected_events - 1].outcome == TIPSY_EGL_SETUP_SUCCESS;
	} else if (out->event_count > 0) {
		last = out->event_count - 1;
		passed = last < sizeof(out->events) / sizeof(out->events[0]) &&
			out->first_failure == failure_stage &&
			out->events[last].stage == failure_stage &&
			out->events[last].outcome == failure_outcome;
	}
	out->passed = passed != 0;

	egl_setup_test_sink = saved_sink;
	egl_setup_fixture_suppress_missing = saved_suppress_missing;
	active_egl_setup_fixture = NULL;
	egl_setup_fixture_failure_stage = 0;
	host_eglGetPlatformDisplay = saved_platform_display;
	host_eglGetPlatformDisplayEXT = saved_platform_display_ext;
	host_eglGetDisplay = saved_display;
	host_eglInitialize = saved_initialize;
	host_eglChooseConfig = saved_choose_config;
	host_eglCreateContext = saved_create_context;
	host_eglCreateWindowSurface = saved_create_window_surface;
	host_eglMakeCurrent = saved_make_current;
	host_eglSwapBuffers = saved_swap;
	return passed;
}

int tipsy_test_egl_setup_trace_fixture(void)
{
	static const uint32_t acquisitions[] = {
		TIPSY_EGL_SETUP_PLATFORM_DISPLAY,
		TIPSY_EGL_SETUP_PLATFORM_DISPLAY_EXT,
		TIPSY_EGL_SETUP_DISPLAY,
	};
	static const uint32_t later_stages[] = {
		TIPSY_EGL_SETUP_INITIALIZE,
		TIPSY_EGL_SETUP_CHOOSE_CONFIG,
		TIPSY_EGL_SETUP_CREATE_CONTEXT,
		TIPSY_EGL_SETUP_CREATE_WINDOW_SURFACE,
		TIPSY_EGL_SETUP_MAKE_CURRENT,
		TIPSY_EGL_SETUP_FIRST_SWAP,
	};
	static const uint32_t outcomes[] = {
		TIPSY_EGL_SETUP_FAILURE,
		TIPSY_EGL_SETUP_ABSENCE,
	};
	struct tipsy_egl_setup_trace_fixture fixture;
	uint32_t acquisition_index, stage_index, outcome_index;

	for (acquisition_index = 0;
		acquisition_index < sizeof(acquisitions) / sizeof(acquisitions[0]);
		acquisition_index++) {
		uint32_t acquisition = acquisitions[acquisition_index];
		if (!tipsy_egl_setup_trace_fixture(acquisition, 0,
			TIPSY_EGL_SETUP_FAILURE, &fixture) || fixture.first_failure != 0 ||
			fixture.event_count != 7 || fixture.events[0].stage != acquisition ||
			fixture.events[0].outcome != TIPSY_EGL_SETUP_SUCCESS) return 0;
		for (stage_index = 0;
			stage_index < sizeof(later_stages) / sizeof(later_stages[0]);
			stage_index++) {
			if (fixture.events[stage_index + 1].stage != later_stages[stage_index] ||
				fixture.events[stage_index + 1].outcome != TIPSY_EGL_SETUP_SUCCESS)
				return 0;
		}
		for (stage_index = 0;
			stage_index <= sizeof(later_stages) / sizeof(later_stages[0]);
			stage_index++) {
			uint32_t stage = stage_index == 0 ? acquisition : later_stages[stage_index - 1];
			for (outcome_index = 0;
				outcome_index < sizeof(outcomes) / sizeof(outcomes[0]);
				outcome_index++) {
				if (!tipsy_egl_setup_trace_fixture(acquisition, stage,
					outcomes[outcome_index], &fixture)) return 0;
			}
		}
	}
	return 1;
}

/* Deterministic, content-free EGL/GLES foreground fixture. The fake state is
 * intentionally stricter than Mesa: a draw is counted as invalid unless it
 * targets draw framebuffer zero through a private ES3 VAO with the exact
 * premultiplied source-over state. No glGetError call is used because a real
 * shim cannot consume and later restore the guest's GL error queue. */
struct tipsy_egl_foreground_test {
	EGLDisplay display;
	EGLSurface surface;
	EGLSurface current_surface;
	void *context;
	EGLint swap_behavior;
	int gles_major;
	int foreground_active;
	int overlay_live;
	uint64_t foreground_generation;
	uint32_t acquire_calls, release_calls, draw_calls;
	uint32_t current_display_calls;
	uint32_t texture_allocations, texture_deletions;
	uint32_t full_texture_uploads, same_size_texture_updates, geometry_uploads;
	uint32_t guest_state_restore_failures, draw_contract_failures;
	TipsyGLint read_framebuffer, draw_framebuffer, program, array_buffer, vertex_array;
	TipsyGLint sampler_0, pixel_unpack_buffer;
	TipsyGLint viewport[4], active_texture, texture_2d_0, unpack_alignment;
	TipsyGLint unpack_row_length, unpack_skip_rows, unpack_skip_pixels;
	TipsyGLint unpack_skip_images, unpack_image_height;
	TipsyGLint blend_src_rgb, blend_dst_rgb, blend_src_alpha, blend_dst_alpha;
	TipsyGLint blend_equation_rgb, blend_equation_alpha;
	TipsyGLboolean color_mask[4], depth_mask;
	TipsyGLboolean scissor_enabled, blend_enabled, depth_enabled;
	TipsyGLboolean stencil_enabled, cull_enabled, rasterizer_discard_enabled;
	TipsyGLboolean framebuffer_srgb_enabled;
	TipsyGLboolean sample_alpha_to_coverage_enabled, sample_coverage_enabled;
	TipsyGLboolean sample_mask_enabled, transform_feedback_active;
	TipsyGLuint next_shader, next_program;
	struct tipsy_gl_attrib_state attrib[2];
};

struct tipsy_egl_foreground_fixture {
	uint32_t passed;
	uint32_t acquire_calls, release_calls, draw_calls, texture_allocations;
	uint32_t full_texture_uploads, same_size_texture_updates, geometry_uploads;
	uint32_t guest_state_restore_failures, draw_contract_failures;
	uint32_t preserved_buffer_draws, context_mismatch_acquires, transform_feedback_draws;
	uint32_t cache_texture_deletions, gles2_draws, gles2_state_restore_failures;
	uint32_t live_state_records;
	uint32_t unpublished_skip_acquires, unpublished_skip_currents;
};

static struct tipsy_egl_foreground_test *active_egl_foreground_test;
static unsigned char tipsy_egl_foreground_test_pixels[32];

static const unsigned char *tipsy_test_egl_foreground_get_string(TipsyGLenum name)
{
	if (name == TIPSY_GL_VERSION) {
		if (active_egl_foreground_test != NULL &&
			active_egl_foreground_test->gles_major == 2)
			return (const unsigned char *)"OpenGL ES 2.0 Fake";
		return (const unsigned char *)"OpenGL ES 3.2 Fake";
	}
	if (name == TIPSY_GL_EXTENSIONS)
		return (const unsigned char *)"GL_EXT_sRGB_write_control";
	return NULL;
}

static const unsigned char *tipsy_test_egl_foreground_get_string_i(TipsyGLenum name,
	TipsyGLuint index)
{
	if (name == TIPSY_GL_EXTENSIONS && index == 0)
		return (const unsigned char *)"GL_EXT_sRGB_write_control";
	return NULL;
}

static void tipsy_test_egl_foreground_get_integer(TipsyGLenum name, TipsyGLint *out)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL || out == NULL) return;
	switch (name) {
	case TIPSY_GL_FRAMEBUFFER_BINDING: *out = t->draw_framebuffer; break;
	case TIPSY_GL_READ_FRAMEBUFFER_BINDING: *out = t->read_framebuffer; break;
	case TIPSY_GL_VIEWPORT: memcpy(out, t->viewport, sizeof(t->viewport)); break;
	case TIPSY_GL_CURRENT_PROGRAM: *out = t->program; break;
	case TIPSY_GL_ARRAY_BUFFER_BINDING: *out = t->array_buffer; break;
	case TIPSY_GL_VERTEX_ARRAY_BINDING: *out = t->vertex_array; break;
	case TIPSY_GL_PIXEL_UNPACK_BUFFER_BINDING: *out = t->pixel_unpack_buffer; break;
	case TIPSY_GL_ACTIVE_TEXTURE: *out = t->active_texture; break;
	case TIPSY_GL_TEXTURE_BINDING_2D: *out = t->texture_2d_0; break;
	case TIPSY_GL_SAMPLER_BINDING: *out = t->sampler_0; break;
	case TIPSY_GL_UNPACK_ALIGNMENT: *out = t->unpack_alignment; break;
	case TIPSY_GL_UNPACK_ROW_LENGTH: *out = t->unpack_row_length; break;
	case TIPSY_GL_UNPACK_SKIP_ROWS: *out = t->unpack_skip_rows; break;
	case TIPSY_GL_UNPACK_SKIP_PIXELS: *out = t->unpack_skip_pixels; break;
	case TIPSY_GL_UNPACK_SKIP_IMAGES: *out = t->unpack_skip_images; break;
	case TIPSY_GL_UNPACK_IMAGE_HEIGHT: *out = t->unpack_image_height; break;
	case TIPSY_GL_BLEND_SRC_RGB: *out = t->blend_src_rgb; break;
	case TIPSY_GL_BLEND_DST_RGB: *out = t->blend_dst_rgb; break;
	case TIPSY_GL_BLEND_SRC_ALPHA: *out = t->blend_src_alpha; break;
	case TIPSY_GL_BLEND_DST_ALPHA: *out = t->blend_dst_alpha; break;
	case TIPSY_GL_BLEND_EQUATION_RGB: *out = t->blend_equation_rgb; break;
	case TIPSY_GL_BLEND_EQUATION_ALPHA: *out = t->blend_equation_alpha; break;
	case TIPSY_GL_MAX_TEXTURE_SIZE: *out = 4096; break;
	case TIPSY_GL_NUM_EXTENSIONS: *out = 1; break;
	default: *out = 0; break;
	}
}

static void tipsy_test_egl_foreground_get_boolean(TipsyGLenum name, TipsyGLboolean *out)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL || out == NULL) return;
	if (name == TIPSY_GL_COLOR_WRITEMASK) memcpy(out, t->color_mask, sizeof(t->color_mask));
	else if (name == TIPSY_GL_DEPTH_WRITEMASK) *out = t->depth_mask;
	else if (name == TIPSY_GL_TRANSFORM_FEEDBACK_ACTIVE) *out = t->transform_feedback_active;
}

static TipsyGLboolean *tipsy_test_egl_foreground_cap(TipsyGLenum cap)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL) return NULL;
	switch (cap) {
	case TIPSY_GL_SCISSOR_TEST: return &t->scissor_enabled;
	case TIPSY_GL_BLEND: return &t->blend_enabled;
	case TIPSY_GL_DEPTH_TEST: return &t->depth_enabled;
	case TIPSY_GL_STENCIL_TEST: return &t->stencil_enabled;
	case TIPSY_GL_CULL_FACE: return &t->cull_enabled;
	case TIPSY_GL_RASTERIZER_DISCARD: return &t->rasterizer_discard_enabled;
	case TIPSY_GL_FRAMEBUFFER_SRGB: return &t->framebuffer_srgb_enabled;
	case TIPSY_GL_SAMPLE_ALPHA_TO_COVERAGE: return &t->sample_alpha_to_coverage_enabled;
	case TIPSY_GL_SAMPLE_COVERAGE: return &t->sample_coverage_enabled;
	case TIPSY_GL_SAMPLE_MASK: return &t->sample_mask_enabled;
	default: return NULL;
	}
}

static TipsyGLboolean tipsy_test_egl_foreground_is_enabled(TipsyGLenum cap)
{
	TipsyGLboolean *value = tipsy_test_egl_foreground_cap(cap);
	return value != NULL ? *value : TIPSY_GL_FALSE;
}

static void tipsy_test_egl_foreground_enable(TipsyGLenum cap)
{
	TipsyGLboolean *value = tipsy_test_egl_foreground_cap(cap);
	if (value != NULL) *value = TIPSY_GL_TRUE;
}

static void tipsy_test_egl_foreground_disable(TipsyGLenum cap)
{
	TipsyGLboolean *value = tipsy_test_egl_foreground_cap(cap);
	if (value != NULL) *value = TIPSY_GL_FALSE;
}

static void tipsy_test_egl_foreground_viewport(TipsyGLint x, TipsyGLint y,
	TipsyGLsizei width, TipsyGLsizei height)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL) {
		t->viewport[0] = x; t->viewport[1] = y;
		t->viewport[2] = width; t->viewport[3] = height;
	}
}

static void tipsy_test_egl_foreground_color_mask(TipsyGLboolean r, TipsyGLboolean g,
	TipsyGLboolean b, TipsyGLboolean a)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL) {
		t->color_mask[0] = r; t->color_mask[1] = g;
		t->color_mask[2] = b; t->color_mask[3] = a;
	}
}

static void tipsy_test_egl_foreground_blend_func(TipsyGLenum src_rgb,
	TipsyGLenum dst_rgb, TipsyGLenum src_alpha, TipsyGLenum dst_alpha)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL) {
		t->blend_src_rgb = (TipsyGLint)src_rgb; t->blend_dst_rgb = (TipsyGLint)dst_rgb;
		t->blend_src_alpha = (TipsyGLint)src_alpha;
		t->blend_dst_alpha = (TipsyGLint)dst_alpha;
	}
}

static void tipsy_test_egl_foreground_blend_equation(TipsyGLenum rgb, TipsyGLenum alpha)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL) {
		t->blend_equation_rgb = (TipsyGLint)rgb;
		t->blend_equation_alpha = (TipsyGLint)alpha;
	}
}

static void tipsy_test_egl_foreground_depth_mask(TipsyGLboolean value)
{
	if (active_egl_foreground_test != NULL) active_egl_foreground_test->depth_mask = value;
}

static void tipsy_test_egl_foreground_bind_framebuffer(TipsyGLenum target,
	TipsyGLuint framebuffer)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL) return;
	if (target == TIPSY_GL_FRAMEBUFFER || target == TIPSY_GL_DRAW_FRAMEBUFFER)
		t->draw_framebuffer = (TipsyGLint)framebuffer;
	if (target == TIPSY_GL_FRAMEBUFFER || target == TIPSY_GL_READ_FRAMEBUFFER)
		t->read_framebuffer = (TipsyGLint)framebuffer;
}

static void tipsy_test_egl_foreground_active_texture(TipsyGLenum texture)
{
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->active_texture = (TipsyGLint)texture;
}

static void tipsy_test_egl_foreground_bind_texture(TipsyGLenum target, TipsyGLuint texture)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL && target == TIPSY_GL_TEXTURE_2D &&
		t->active_texture == (TipsyGLint)TIPSY_GL_TEXTURE0)
		t->texture_2d_0 = (TipsyGLint)texture;
}

static void tipsy_test_egl_foreground_tex_parameter(TipsyGLenum target,
	TipsyGLenum name, TipsyGLint value)
{
	(void)target; (void)name; (void)value;
}

static void tipsy_test_egl_foreground_pixel_store(TipsyGLenum name, TipsyGLint value)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL) return;
	if (name == TIPSY_GL_UNPACK_ALIGNMENT) t->unpack_alignment = value;
	else if (name == TIPSY_GL_UNPACK_ROW_LENGTH) t->unpack_row_length = value;
	else if (name == TIPSY_GL_UNPACK_SKIP_ROWS) t->unpack_skip_rows = value;
	else if (name == TIPSY_GL_UNPACK_SKIP_PIXELS) t->unpack_skip_pixels = value;
	else if (name == TIPSY_GL_UNPACK_SKIP_IMAGES) t->unpack_skip_images = value;
	else if (name == TIPSY_GL_UNPACK_IMAGE_HEIGHT) t->unpack_image_height = value;
}

static void tipsy_test_egl_foreground_gen_textures(TipsyGLsizei count, TipsyGLuint *out)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL && count == 1 && out != NULL) {
		t->texture_allocations++;
		*out = 100 + t->texture_allocations;
	}
}

static void tipsy_test_egl_foreground_delete_textures(TipsyGLsizei count,
	const TipsyGLuint *textures)
{
	(void)textures;
	if (active_egl_foreground_test != NULL && count == 1)
		active_egl_foreground_test->texture_deletions++;
}

static void tipsy_test_egl_foreground_tex_image(TipsyGLenum target, TipsyGLint level,
	TipsyGLint internal_format, TipsyGLsizei width, TipsyGLsizei height,
	TipsyGLint border, TipsyGLenum format, TipsyGLenum type, const void *pixels)
{
	(void)target; (void)level; (void)internal_format; (void)width; (void)height;
	(void)border; (void)format; (void)type; (void)pixels;
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->full_texture_uploads++;
}

static void tipsy_test_egl_foreground_tex_sub_image(TipsyGLenum target, TipsyGLint level,
	TipsyGLint x, TipsyGLint y, TipsyGLsizei width, TipsyGLsizei height,
	TipsyGLenum format, TipsyGLenum type, const void *pixels)
{
	(void)target; (void)level; (void)x; (void)y; (void)width; (void)height;
	(void)format; (void)type; (void)pixels;
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->same_size_texture_updates++;
}

static TipsyGLuint tipsy_test_egl_foreground_create_shader(TipsyGLenum kind)
{
	(void)kind;
	return active_egl_foreground_test != NULL ?
		++active_egl_foreground_test->next_shader : 0;
}

static void tipsy_test_egl_foreground_shader_source(TipsyGLuint shader,
	TipsyGLsizei count, const char *const *source, const TipsyGLint *length)
{
	(void)shader; (void)count; (void)source; (void)length;
}

static void tipsy_test_egl_foreground_noop_uint(TipsyGLuint value) { (void)value; }
static void tipsy_test_egl_foreground_compile_status(TipsyGLuint shader,
	TipsyGLenum name, TipsyGLint *out)
{
	(void)shader; (void)name; if (out != NULL) *out = 1;
}
static TipsyGLuint tipsy_test_egl_foreground_create_program(void)
{
	return active_egl_foreground_test != NULL ?
		++active_egl_foreground_test->next_program : 0;
}
static void tipsy_test_egl_foreground_attach_shader(TipsyGLuint program, TipsyGLuint shader)
{ (void)program; (void)shader; }
static void tipsy_test_egl_foreground_bind_attrib_location(TipsyGLuint program,
	TipsyGLuint index, const char *name)
{ (void)program; (void)index; (void)name; }
static void tipsy_test_egl_foreground_program_status(TipsyGLuint program,
	TipsyGLenum name, TipsyGLint *out)
{ (void)program; (void)name; if (out != NULL) *out = 1; }
static void tipsy_test_egl_foreground_use_program(TipsyGLuint program)
{
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->program = (TipsyGLint)program;
}
static TipsyGLint tipsy_test_egl_foreground_uniform_location(TipsyGLuint program,
	const char *name)
{ (void)program; (void)name; return 0; }
static void tipsy_test_egl_foreground_uniform_1i(TipsyGLint location, TipsyGLint value)
{ (void)location; (void)value; }

static void tipsy_test_egl_foreground_gen_buffers(TipsyGLsizei count, TipsyGLuint *out)
{ if (count == 1 && out != NULL) *out = 202; }
static void tipsy_test_egl_foreground_delete_buffers(TipsyGLsizei count,
	const TipsyGLuint *buffers)
{ (void)count; (void)buffers; }
static void tipsy_test_egl_foreground_bind_buffer(TipsyGLenum target, TipsyGLuint buffer)
{
	if (active_egl_foreground_test == NULL) return;
	if (target == TIPSY_GL_ARRAY_BUFFER)
		active_egl_foreground_test->array_buffer = (TipsyGLint)buffer;
	else if (target == TIPSY_GL_PIXEL_UNPACK_BUFFER)
		active_egl_foreground_test->pixel_unpack_buffer = (TipsyGLint)buffer;
}
static void tipsy_test_egl_foreground_buffer_data(TipsyGLenum target,
	TipsyGLsizeiptr size, const void *data, TipsyGLenum usage)
{
	(void)target; (void)size; (void)data; (void)usage;
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->geometry_uploads++;
}
static void tipsy_test_egl_foreground_enable_attrib(TipsyGLuint index)
{
	if (active_egl_foreground_test != NULL && index < 2)
		active_egl_foreground_test->attrib[index].enabled = 1;
}
static void tipsy_test_egl_foreground_disable_attrib(TipsyGLuint index)
{
	if (active_egl_foreground_test != NULL && index < 2)
		active_egl_foreground_test->attrib[index].enabled = 0;
}
static void tipsy_test_egl_foreground_attrib_pointer(TipsyGLuint index,
	TipsyGLint size, TipsyGLenum type, TipsyGLboolean normalized,
	TipsyGLsizei stride, const void *pointer)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL || index >= 2) return;
	t->attrib[index].size = size;
	t->attrib[index].type = (TipsyGLint)type;
	t->attrib[index].normalized = normalized;
	t->attrib[index].stride = stride;
	t->attrib[index].buffer = t->array_buffer;
	t->attrib[index].pointer = (void *)pointer;
}
static void tipsy_test_egl_foreground_get_attrib(TipsyGLuint index,
	TipsyGLenum name, TipsyGLint *out)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL || index >= 2 || out == NULL) return;
	if (name == TIPSY_GL_VERTEX_ATTRIB_ARRAY_ENABLED) *out = t->attrib[index].enabled;
	else if (name == TIPSY_GL_VERTEX_ATTRIB_ARRAY_SIZE) *out = t->attrib[index].size;
	else if (name == TIPSY_GL_VERTEX_ATTRIB_ARRAY_TYPE) *out = t->attrib[index].type;
	else if (name == TIPSY_GL_VERTEX_ATTRIB_ARRAY_NORMALIZED) *out = t->attrib[index].normalized;
	else if (name == TIPSY_GL_VERTEX_ATTRIB_ARRAY_STRIDE) *out = t->attrib[index].stride;
	else if (name == TIPSY_GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING) *out = t->attrib[index].buffer;
	else *out = 0;
}
static void tipsy_test_egl_foreground_get_attrib_pointer(TipsyGLuint index,
	TipsyGLenum name, void **out)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	(void)name;
	if (t != NULL && index < 2 && out != NULL) *out = t->attrib[index].pointer;
}

static void tipsy_test_egl_foreground_draw(TipsyGLenum mode, TipsyGLint first,
	TipsyGLsizei count)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL) return;
	t->draw_calls++;
	if (mode != TIPSY_GL_TRIANGLE_STRIP || first != 0 || count != 4 ||
		t->draw_framebuffer != 0 ||
		(t->gles_major == 3 && (t->vertex_array == 0 || t->vertex_array == 23)) ||
		t->viewport[0] != 0 || t->viewport[1] != 0 ||
		t->viewport[2] != 800 || t->viewport[3] != 600 ||
		t->scissor_enabled || !t->blend_enabled || t->depth_enabled ||
		t->stencil_enabled || t->cull_enabled ||
		(t->gles_major == 3 && t->rasterizer_discard_enabled) ||
		t->framebuffer_srgb_enabled || t->sample_alpha_to_coverage_enabled ||
		t->sample_coverage_enabled || (t->gles_major == 3 && t->sample_mask_enabled) ||
		(t->gles_major == 3 && t->sampler_0 != 0) ||
		t->depth_mask ||
		!t->color_mask[0] || !t->color_mask[1] || !t->color_mask[2] || !t->color_mask[3] ||
		t->blend_src_rgb != (TipsyGLint)TIPSY_GL_ONE ||
		t->blend_dst_rgb != (TipsyGLint)TIPSY_GL_ONE_MINUS_SRC_ALPHA ||
		t->blend_src_alpha != (TipsyGLint)TIPSY_GL_ONE ||
		t->blend_dst_alpha != (TipsyGLint)TIPSY_GL_ONE_MINUS_SRC_ALPHA ||
		t->blend_equation_rgb != (TipsyGLint)TIPSY_GL_FUNC_ADD ||
		t->blend_equation_alpha != (TipsyGLint)TIPSY_GL_FUNC_ADD ||
		t->active_texture != (TipsyGLint)TIPSY_GL_TEXTURE0 || t->texture_2d_0 == 0)
		t->draw_contract_failures++;
}

static void tipsy_test_egl_foreground_gen_vertex_arrays(TipsyGLsizei count,
	TipsyGLuint *out)
{ if (count == 1 && out != NULL) *out = 303; }
static void tipsy_test_egl_foreground_delete_vertex_arrays(TipsyGLsizei count,
	const TipsyGLuint *arrays)
{ (void)count; (void)arrays; }
static void tipsy_test_egl_foreground_bind_vertex_array(TipsyGLuint array)
{
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->vertex_array = (TipsyGLint)array;
}

static void tipsy_test_egl_foreground_bind_sampler(TipsyGLuint unit, TipsyGLuint sampler)
{
	if (active_egl_foreground_test != NULL && unit == 0)
		active_egl_foreground_test->sampler_0 = (TipsyGLint)sampler;
}

static EGLDisplay tipsy_test_egl_foreground_current_display(void)
{
	if (active_egl_foreground_test != NULL)
		active_egl_foreground_test->current_display_calls++;
	return active_egl_foreground_test != NULL ? active_egl_foreground_test->display : NULL;
}
static EGLSurface tipsy_test_egl_foreground_current_surface(EGLint which)
{ (void)which; return active_egl_foreground_test != NULL ?
	active_egl_foreground_test->current_surface : NULL; }
static void *tipsy_test_egl_foreground_current_context(void)
{ return active_egl_foreground_test != NULL ? active_egl_foreground_test->context : NULL; }
static EGLBoolean tipsy_test_egl_foreground_query(EGLDisplay display, EGLSurface surface,
	EGLint attribute, EGLint *out)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL || display != t->display || surface != t->surface || out == NULL)
		return TIPSY_EGL_FALSE;
	if (attribute == TIPSY_EGL_SWAP_BEHAVIOR) *out = t->swap_behavior;
	else if (attribute == TIPSY_EGL_WIDTH) *out = 800;
	else if (attribute == TIPSY_EGL_HEIGHT) *out = 600;
	else return TIPSY_EGL_FALSE;
	return TIPSY_EGL_TRUE;
}

static int tipsy_test_egl_foreground_acquire(struct tipsy_focused_text_frame *frame)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL || frame == NULL) return -1;
	t->acquire_calls++;
	if (!t->foreground_active) return 0;
	memset(frame, 0, sizeof(*frame));
	frame->rgba = tipsy_egl_foreground_test_pixels;
	frame->width = 4; frame->height = 2; frame->stride = 16;
	frame->x = 20; frame->y = 30;
	frame->generation = t->foreground_generation;
	frame->lease = 77;
	return 1;
}

static int tipsy_test_egl_foreground_overlay_live(void)
{
	return active_egl_foreground_test != NULL &&
		active_egl_foreground_test->overlay_live != 0;
}

static void tipsy_test_egl_foreground_release(uint64_t lease)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t != NULL) {
		if (lease != 77) t->draw_contract_failures++;
		t->release_calls++;
	}
}

static void tipsy_test_egl_foreground_check_restored(void)
{
	struct tipsy_egl_foreground_test *t = active_egl_foreground_test;
	if (t == NULL) return;
	if (t->read_framebuffer != (t->gles_major == 3 ? 31 : 32) ||
		t->draw_framebuffer != 32 ||
		t->program != 17 || t->array_buffer != 18 || t->vertex_array != 23 ||
		t->viewport[0] != 3 || t->viewport[1] != 4 ||
		t->viewport[2] != 640 || t->viewport[3] != 480 ||
		t->active_texture != (TipsyGLint)(TIPSY_GL_TEXTURE0 + 3) ||
		t->texture_2d_0 != 19 ||
		(t->gles_major == 3 && (t->sampler_0 != 24 ||
		 t->pixel_unpack_buffer != 25 || t->unpack_row_length != 26 ||
		 t->unpack_skip_rows != 27 || t->unpack_skip_pixels != 28 ||
		 t->unpack_skip_images != 29 || t->unpack_image_height != 30)) ||
		t->unpack_alignment != 8 ||
		t->blend_src_rgb != 0x302 || t->blend_dst_rgb != 0x303 ||
		t->blend_src_alpha != 0x304 || t->blend_dst_alpha != 0x305 ||
		t->blend_equation_rgb != 0x8007 || t->blend_equation_alpha != 0x8008 ||
		!t->color_mask[0] || t->color_mask[1] || !t->color_mask[2] || t->color_mask[3] ||
		!t->depth_mask || !t->scissor_enabled || t->blend_enabled ||
		!t->depth_enabled || !t->stencil_enabled || !t->cull_enabled ||
		(t->gles_major == 3 && !t->rasterizer_discard_enabled) ||
		!t->framebuffer_srgb_enabled ||
		!t->sample_alpha_to_coverage_enabled || !t->sample_coverage_enabled ||
		(t->gles_major == 3 && !t->sample_mask_enabled) || t->transform_feedback_active ||
		(t->gles_major == 2 &&
		 (t->attrib[0].enabled != 1 || t->attrib[0].size != 3 ||
		  t->attrib[0].type != (TipsyGLint)TIPSY_GL_FLOAT ||
		  t->attrib[0].normalized != 0 || t->attrib[0].stride != 12 ||
		  t->attrib[0].buffer != 0 || t->attrib[0].pointer != (void *)(uintptr_t)0x1000 ||
		  t->attrib[1].enabled != 0 || t->attrib[1].size != 4 ||
		  t->attrib[1].type != (TipsyGLint)TIPSY_GL_UNSIGNED_BYTE ||
		  t->attrib[1].normalized != 1 || t->attrib[1].stride != 16 ||
		  t->attrib[1].buffer != 77 || t->attrib[1].pointer != (void *)(uintptr_t)0x20)))
		t->guest_state_restore_failures++;
}

static uint32_t tipsy_test_egl_foreground_live_states(void)
{
	struct tipsy_egl_text_state *state;
	uint32_t count = 0;
	for (state = egl_text_states; state != NULL; state = state->next) count++;
	return count;
}

int tipsy_test_egl_foreground_fixture(void)
{
	struct tipsy_egl_foreground_test t = {0};
	struct tipsy_egl_foreground_fixture fixture = {0};
	struct tipsy_egl_foreground_fixture *out = &fixture;
	struct tipsy_gl_api saved_gl;
	egl_get_current_display_fn saved_current_display;
	egl_get_current_surface_fn saved_current_surface;
	egl_get_current_context_fn saved_current_context;
	egl_query_surface_fn saved_query_surface;
	tipsy_egl_text_frame_acquire_fn saved_acquire;
	tipsy_egl_text_frame_release_fn saved_release;
	tipsy_egl_text_overlay_live_fn saved_live;
	uint32_t draws_before, acquires_before, state_failures_before, currents_before;
	int passed;

	ensure_egl();
	saved_gl = egl_text_gl;
	saved_current_display = host_eglGetCurrentDisplay;
	saved_current_surface = host_eglGetCurrentSurface;
	saved_current_context = host_eglGetCurrentContext;
	saved_query_surface = host_eglQuerySurface;
	saved_acquire = egl_text_frame_acquire_fn;
	saved_release = egl_text_frame_release_fn;
	saved_live = egl_text_overlay_live_fn;

	t.display = (EGLDisplay)(uintptr_t)0x51;
	t.surface = (EGLSurface)(uintptr_t)0x52;
	t.current_surface = t.surface;
	t.context = (void *)(uintptr_t)0x53;
	t.swap_behavior = TIPSY_EGL_BUFFER_DESTROYED;
	t.gles_major = 3;
	t.foreground_active = 1;
	t.overlay_live = 1;
	t.foreground_generation = 1;
	t.read_framebuffer = 31; t.draw_framebuffer = 32; t.program = 17;
	t.array_buffer = 18; t.vertex_array = 23;
	t.viewport[0] = 3; t.viewport[1] = 4; t.viewport[2] = 640; t.viewport[3] = 480;
	t.active_texture = TIPSY_GL_TEXTURE0 + 3; t.texture_2d_0 = 19;
	t.sampler_0 = 24; t.pixel_unpack_buffer = 25; t.unpack_alignment = 8;
	t.unpack_row_length = 26; t.unpack_skip_rows = 27; t.unpack_skip_pixels = 28;
	t.unpack_skip_images = 29; t.unpack_image_height = 30;
	t.blend_src_rgb = 0x302; t.blend_dst_rgb = 0x303;
	t.blend_src_alpha = 0x304; t.blend_dst_alpha = 0x305;
	t.blend_equation_rgb = 0x8007; t.blend_equation_alpha = 0x8008;
	t.color_mask[0] = 1; t.color_mask[2] = 1; t.depth_mask = 1;
	t.scissor_enabled = 1; t.depth_enabled = 1; t.stencil_enabled = 1;
	t.cull_enabled = 1; t.rasterizer_discard_enabled = 1;
	t.framebuffer_srgb_enabled = 1;
	t.sample_alpha_to_coverage_enabled = 1; t.sample_coverage_enabled = 1;
	t.sample_mask_enabled = 1;
	t.next_shader = 400; t.next_program = 500;

	memset(&egl_text_gl, 0, sizeof(egl_text_gl));
	egl_text_gl.GetString = tipsy_test_egl_foreground_get_string;
	egl_text_gl.GetStringi = tipsy_test_egl_foreground_get_string_i;
	egl_text_gl.GetIntegerv = tipsy_test_egl_foreground_get_integer;
	egl_text_gl.GetBooleanv = tipsy_test_egl_foreground_get_boolean;
	egl_text_gl.IsEnabled = tipsy_test_egl_foreground_is_enabled;
	egl_text_gl.Enable = tipsy_test_egl_foreground_enable;
	egl_text_gl.Disable = tipsy_test_egl_foreground_disable;
	egl_text_gl.Viewport = tipsy_test_egl_foreground_viewport;
	egl_text_gl.ColorMask = tipsy_test_egl_foreground_color_mask;
	egl_text_gl.BlendFuncSeparate = tipsy_test_egl_foreground_blend_func;
	egl_text_gl.BlendEquationSeparate = tipsy_test_egl_foreground_blend_equation;
	egl_text_gl.DepthMask = tipsy_test_egl_foreground_depth_mask;
	egl_text_gl.BindFramebuffer = tipsy_test_egl_foreground_bind_framebuffer;
	egl_text_gl.ActiveTexture = tipsy_test_egl_foreground_active_texture;
	egl_text_gl.BindTexture = tipsy_test_egl_foreground_bind_texture;
	egl_text_gl.TexParameteri = tipsy_test_egl_foreground_tex_parameter;
	egl_text_gl.PixelStorei = tipsy_test_egl_foreground_pixel_store;
	egl_text_gl.GenTextures = tipsy_test_egl_foreground_gen_textures;
	egl_text_gl.DeleteTextures = tipsy_test_egl_foreground_delete_textures;
	egl_text_gl.TexImage2D = tipsy_test_egl_foreground_tex_image;
	egl_text_gl.TexSubImage2D = tipsy_test_egl_foreground_tex_sub_image;
	egl_text_gl.CreateShader = tipsy_test_egl_foreground_create_shader;
	egl_text_gl.ShaderSource = tipsy_test_egl_foreground_shader_source;
	egl_text_gl.CompileShader = tipsy_test_egl_foreground_noop_uint;
	egl_text_gl.GetShaderiv = tipsy_test_egl_foreground_compile_status;
	egl_text_gl.DeleteShader = tipsy_test_egl_foreground_noop_uint;
	egl_text_gl.CreateProgram = tipsy_test_egl_foreground_create_program;
	egl_text_gl.AttachShader = tipsy_test_egl_foreground_attach_shader;
	egl_text_gl.BindAttribLocation = tipsy_test_egl_foreground_bind_attrib_location;
	egl_text_gl.LinkProgram = tipsy_test_egl_foreground_noop_uint;
	egl_text_gl.GetProgramiv = tipsy_test_egl_foreground_program_status;
	egl_text_gl.DeleteProgram = tipsy_test_egl_foreground_noop_uint;
	egl_text_gl.UseProgram = tipsy_test_egl_foreground_use_program;
	egl_text_gl.GetUniformLocation = tipsy_test_egl_foreground_uniform_location;
	egl_text_gl.Uniform1i = tipsy_test_egl_foreground_uniform_1i;
	egl_text_gl.GenBuffers = tipsy_test_egl_foreground_gen_buffers;
	egl_text_gl.DeleteBuffers = tipsy_test_egl_foreground_delete_buffers;
	egl_text_gl.BindBuffer = tipsy_test_egl_foreground_bind_buffer;
	egl_text_gl.BufferData = tipsy_test_egl_foreground_buffer_data;
	egl_text_gl.EnableVertexAttribArray = tipsy_test_egl_foreground_enable_attrib;
	egl_text_gl.DisableVertexAttribArray = tipsy_test_egl_foreground_disable_attrib;
	egl_text_gl.VertexAttribPointer = tipsy_test_egl_foreground_attrib_pointer;
	egl_text_gl.GetVertexAttribiv = tipsy_test_egl_foreground_get_attrib;
	egl_text_gl.GetVertexAttribPointerv = tipsy_test_egl_foreground_get_attrib_pointer;
	egl_text_gl.DrawArrays = tipsy_test_egl_foreground_draw;
	egl_text_gl.GenVertexArrays = tipsy_test_egl_foreground_gen_vertex_arrays;
	egl_text_gl.DeleteVertexArrays = tipsy_test_egl_foreground_delete_vertex_arrays;
	egl_text_gl.BindVertexArray = tipsy_test_egl_foreground_bind_vertex_array;
	egl_text_gl.BindSampler = tipsy_test_egl_foreground_bind_sampler;
	egl_text_gl.ready = 1;

	active_egl_foreground_test = &t;
	host_eglGetCurrentDisplay = tipsy_test_egl_foreground_current_display;
	host_eglGetCurrentSurface = tipsy_test_egl_foreground_current_surface;
	host_eglGetCurrentContext = tipsy_test_egl_foreground_current_context;
	host_eglQuerySurface = tipsy_test_egl_foreground_query;
	egl_text_frame_acquire_fn = tipsy_test_egl_foreground_acquire;
	egl_text_frame_release_fn = tipsy_test_egl_foreground_release;
	egl_text_overlay_live_fn = tipsy_test_egl_foreground_overlay_live;

	tipsy_egl_compose_focused_text(t.display, t.surface);
	tipsy_test_egl_foreground_check_restored();
	tipsy_egl_compose_focused_text(t.display, t.surface);
	tipsy_test_egl_foreground_check_restored();
	t.foreground_generation = 2;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	tipsy_test_egl_foreground_check_restored();
	draws_before = t.draw_calls;
	t.transform_feedback_active = 1;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	if (!t.transform_feedback_active) t.guest_state_restore_failures++;
	out->transform_feedback_draws = t.draw_calls - draws_before;
	t.transform_feedback_active = 0;
	draws_before = t.draw_calls;
	t.swap_behavior = TIPSY_EGL_BUFFER_PRESERVED;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	tipsy_test_egl_foreground_check_restored();
	out->preserved_buffer_draws = t.draw_calls - draws_before;
	acquires_before = t.acquire_calls;
	t.current_surface = (EGLSurface)(uintptr_t)0x54;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	out->context_mismatch_acquires = t.acquire_calls - acquires_before;
	t.current_surface = t.surface;
	t.swap_behavior = TIPSY_EGL_BUFFER_DESTROYED;
	t.overlay_live = 0;
	t.foreground_active = 0;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	tipsy_test_egl_foreground_check_restored();
	acquires_before = t.acquire_calls;
	currents_before = t.current_display_calls;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	out->unpublished_skip_acquires = t.acquire_calls - acquires_before;
	out->unpublished_skip_currents = t.current_display_calls - currents_before;

	tipsy_egl_text_forget(t.display, t.surface, NULL);
	out->acquire_calls = t.acquire_calls;
	out->release_calls = t.release_calls;
	out->draw_calls = t.draw_calls;
	out->texture_allocations = t.texture_allocations;
	out->full_texture_uploads = t.full_texture_uploads;
	out->same_size_texture_updates = t.same_size_texture_updates;
	out->geometry_uploads = t.geometry_uploads;
	out->cache_texture_deletions = t.texture_deletions;

	/* Exercise the no-core-VAO GLES 2 fallback separately. In particular,
	 * attribute zero starts as a client-memory array with ARRAY_BUFFER zero;
	 * restoring only nonzero VBO bindings would corrupt that guest state. */
	t.gles_major = 2;
	t.context = (void *)(uintptr_t)0x55;
	t.overlay_live = 1;
	t.foreground_active = 1;
	t.foreground_generation = 10;
	t.read_framebuffer = t.draw_framebuffer = 32;
	t.rasterizer_discard_enabled = 0;
	t.sample_mask_enabled = 0;
	t.attrib[0].enabled = 1; t.attrib[0].size = 3;
	t.attrib[0].type = TIPSY_GL_FLOAT; t.attrib[0].normalized = 0;
	t.attrib[0].stride = 12; t.attrib[0].buffer = 0;
	t.attrib[0].pointer = (void *)(uintptr_t)0x1000;
	t.attrib[1].enabled = 0; t.attrib[1].size = 4;
	t.attrib[1].type = TIPSY_GL_UNSIGNED_BYTE; t.attrib[1].normalized = 1;
	t.attrib[1].stride = 16; t.attrib[1].buffer = 77;
	t.attrib[1].pointer = (void *)(uintptr_t)0x20;
	draws_before = t.draw_calls;
	state_failures_before = t.guest_state_restore_failures;
	tipsy_egl_compose_focused_text(t.display, t.surface);
	tipsy_test_egl_foreground_check_restored();
	out->gles2_draws = t.draw_calls - draws_before;
	out->gles2_state_restore_failures =
		t.guest_state_restore_failures - state_failures_before;
	tipsy_egl_text_forget(t.display, t.surface, NULL);
	out->live_state_records = tipsy_test_egl_foreground_live_states();
	out->guest_state_restore_failures = t.guest_state_restore_failures;
	out->draw_contract_failures = t.draw_contract_failures;
	passed = out->acquire_calls == 6 && out->release_calls == 5 &&
		out->draw_calls == 3 && out->texture_allocations == 1 &&
		out->full_texture_uploads == 1 && out->same_size_texture_updates == 1 &&
		out->geometry_uploads == 1 && out->guest_state_restore_failures == 0 &&
		out->draw_contract_failures == 0 && out->preserved_buffer_draws == 0 &&
		out->context_mismatch_acquires == 0 && out->transform_feedback_draws == 0 &&
		out->cache_texture_deletions == 1 &&
		out->unpublished_skip_acquires == 0 &&
		out->unpublished_skip_currents == 0 &&
		out->gles2_draws == 1 && out->gles2_state_restore_failures == 0 &&
		out->live_state_records == 0;
	out->passed = passed != 0;

	egl_text_frame_acquire_fn = saved_acquire;
	egl_text_frame_release_fn = saved_release;
	egl_text_overlay_live_fn = saved_live;
	host_eglGetCurrentDisplay = saved_current_display;
	host_eglGetCurrentSurface = saved_current_surface;
	host_eglGetCurrentContext = saved_current_context;
	host_eglQuerySurface = saved_query_surface;
	egl_text_gl = saved_gl;
	active_egl_foreground_test = NULL;
	return passed;
}

void *tipsy_egl_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, "eglGetPlatformDisplayEXT") == 0) {
		ensure_egl();
		if (host_eglGetPlatformDisplayEXT == NULL) {
			tipsy_egl_trace_platform_display_ext_resolver_absence();
			return NULL;
		}
	}
	tipsy_egl_trace_resolver_name(name);
	p = tipsy_egl_wrapped_proc(name);
	if (p != NULL) return p;
	ensure_egl();
	if (lib_egl != NULL) {
		p = dlsym(lib_egl, name);
		if (p != NULL) {
			return p;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

struct tipsy_egl_policy_test {
	int policy_result;
	int policy_error;
	int client_result;
	int client_error;
	int desired;
	int requested;
	int first_interval;
	int second_interval;
	int calls;
	int reported_error;
};

static struct tipsy_egl_policy_test *active_egl_policy_test;

static EGLBoolean tipsy_test_egl_swap_interval(EGLDisplay dpy, EGLint interval)
{
	struct tipsy_egl_policy_test *t = active_egl_policy_test;
	(void)dpy;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	if (t->calls == 0) {
		t->first_interval = interval;
	} else if (t->calls == 1) {
		t->second_interval = interval;
	}
	t->calls++;
	if (interval == t->desired) {
		t->reported_error = t->policy_error;
		return t->policy_result ? TIPSY_EGL_TRUE : TIPSY_EGL_FALSE;
	}
	t->reported_error = t->client_error;
	return t->client_result ? TIPSY_EGL_TRUE : TIPSY_EGL_FALSE;
}

static EGLint tipsy_test_egl_get_error(void)
{
	EGLint result;
	if (active_egl_policy_test == NULL) {
		return 0;
	}
	result = active_egl_policy_test->reported_error;
	active_egl_policy_test->reported_error = 0;
	return result;
}

int tipsy_test_egl_swap_interval_policy(int vsync, int requested,
	int policy_result, int policy_error, int client_result, int client_error,
	int *first_interval, int *second_interval, int *calls, int *reported_error)
{
	struct tipsy_egl_policy_test t = {0};
	egl_swap_interval_fn saved_swap;
	egl_get_error_fn saved_error;
	EGLBoolean result;

	/* Complete one-time host resolution before injecting the test doubles. */
	ensure_egl();
	t.policy_result = policy_result;
	t.policy_error = policy_error;
	t.client_result = client_result;
	t.client_error = client_error;
	t.desired = vsync ? 1 : 0;
	t.requested = requested;
	saved_swap = host_eglSwapInterval;
	saved_error = host_eglGetError;
	active_egl_policy_test = &t;
	host_eglSwapInterval = tipsy_test_egl_swap_interval;
	host_eglGetError = tipsy_test_egl_get_error;
	tipsy_egl_set_vsync(vsync);
	result = tipsy_eglSwapInterval((EGLDisplay)(uintptr_t)1, requested);
	host_eglSwapInterval = saved_swap;
	host_eglGetError = saved_error;
	active_egl_policy_test = NULL;
	if (first_interval != NULL) {
		*first_interval = t.first_interval;
	}
	if (second_interval != NULL) {
		*second_interval = t.second_interval;
	}
	if (calls != NULL) {
		*calls = t.calls;
	}
	if (reported_error != NULL) {
		*reported_error = t.reported_error;
	}
	return result == TIPSY_EGL_TRUE;
}

struct tipsy_egl_guest_handoff_test {
	EGLSurface next_surface;
	EGLBoolean swap_result;
	EGLBoolean destroy_result;
	int guest_swap_result;
	uint64_t next_generation;
	int reenter_created;
	int reenter_destroyed;
	int create_calls;
	uintptr_t create_host_windows[4];
	int swap_calls;
	int destroy_calls;
	int destroy_callback_order_violations;
	uint32_t callback_count;
	TipsyEGLGuestHandoffEvent callbacks[6];
};

static struct tipsy_egl_guest_handoff_test *active_egl_guest_handoff_test;

static void tipsy_test_egl_guest_handoff_record(uint32_t kind, uintptr_t window,
	uintptr_t display, uintptr_t surface, uint64_t generation)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	TipsyEGLGuestHandoffEvent *event;

	if (t == NULL || t->callback_count >= sizeof(t->callbacks) / sizeof(t->callbacks[0])) {
		return;
	}
	event = &t->callbacks[t->callback_count++];
	event->kind = kind;
	event->window = window;
	event->display = display;
	event->surface = surface;
	event->generation = generation;
}

static EGLSurface tipsy_test_egl_guest_handoff_create(EGLDisplay dpy, EGLConfig config,
	void *host_window, const EGLint *attrib_list)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	(void)dpy;
	(void)config;
	(void)attrib_list;
	if (t == NULL) {
		return NULL;
	}
	if (t->create_calls < (int)(sizeof(t->create_host_windows) /
		sizeof(t->create_host_windows[0]))) {
		t->create_host_windows[t->create_calls] = (uintptr_t)host_window;
	}
	t->create_calls++;
	return t->next_surface;
}

static EGLBoolean tipsy_test_egl_guest_handoff_swap(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	(void)dpy;
	(void)surface;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	t->swap_calls++;
	return t->swap_result;
}

static EGLBoolean tipsy_test_egl_guest_handoff_destroy(EGLDisplay dpy, EGLSurface surface)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	(void)dpy;
	(void)surface;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	if ((uintptr_t)dpy == 0x1001 && (uintptr_t)surface == 0x3001 &&
		(t->callback_count == 0 ||
		 t->callbacks[t->callback_count - 1].kind != TIPSY_EGL_GUEST_HANDOFF_DESTROYED)) {
		t->destroy_callback_order_violations++;
	}
	t->destroy_calls++;
	return t->destroy_result;
}

static uint64_t tipsy_test_egl_guest_handoff_created(uintptr_t window, uintptr_t display,
	uintptr_t surface)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	uint64_t generation;

	if (t == NULL) {
		return 0;
	}
	tipsy_test_egl_guest_handoff_record(TIPSY_EGL_GUEST_HANDOFF_CREATED, window,
		display, surface, t->next_generation);
	generation = t->next_generation;
	if (t->reenter_created) {
		t->reenter_created = 0;
		/* Creation has not retained an Android generation yet. An external
		 * callback may re-enter the shim, but that early successful host swap
		 * must not be guessed as a guest signal. */
		(void)tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface);
	}
	return generation;
}

static int tipsy_test_egl_guest_handoff_swap_callback(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;
	tipsy_test_egl_guest_handoff_record(TIPSY_EGL_GUEST_HANDOFF_SWAP, window,
		display, surface, generation);
	return t != NULL ? t->guest_swap_result : 0;
}

static void tipsy_test_egl_guest_handoff_destroyed(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation)
{
	struct tipsy_egl_guest_handoff_test *t = active_egl_guest_handoff_test;

	tipsy_test_egl_guest_handoff_record(TIPSY_EGL_GUEST_HANDOFF_DESTROYED, window,
		display, surface, generation);
	if (t != NULL && t->reenter_destroyed) {
		t->reenter_destroyed = 0;
		/* The entry was removed before this external callback, so a reentrant
		 * successful host swap is stale and must not produce a second signal. */
		(void)tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface);
	}
}

static EGLSurface tipsy_test_egl_guest_handoff_create_window(uintptr_t display,
	uintptr_t xid)
{
	TipsyNativeWindow window = {0};

	window.magic = TIPSY_ANW_MAGIC;
	window.native_handle = xid;
	return tipsy_eglCreateWindowSurface((EGLDisplay)display, NULL, &window, NULL);
}

int tipsy_test_egl_guest_handoff_fixture(TipsyEGLGuestHandoffFixture *out)
{
	const uintptr_t display = 0x1001;
	const uintptr_t other_display = 0x1002;
	const uintptr_t xid = 0x2001;
	const uintptr_t surface = 0x3001;
	const uintptr_t other_surface = 0x3002;
	const uint64_t first_generation = 41;
	const uint64_t second_generation = 42;
	struct tipsy_egl_guest_handoff_test t = {0};
	egl_create_window_surface_fn saved_create;
	egl_swap_buffers_fn saved_swap;
	egl_destroy_surface_fn saved_destroy;
	tipsy_egl_guest_surface_created_fn saved_created;
	tipsy_egl_guest_swap_fn saved_guest_swap;
	tipsy_egl_guest_surface_destroyed_fn saved_destroyed;
	int passed = 1;

	if (out == NULL) {
		return 0;
	}
	memset(out, 0, sizeof(*out));
	/* Complete host resolution before substituting every direct shim edge. */
	ensure_egl();
	tipsy_egl_guest_surface_clear_all();
	saved_create = host_eglCreateWindowSurface;
	saved_swap = host_eglSwapBuffers;
	saved_destroy = host_eglDestroySurface;
	saved_created = egl_guest_surface_created_fn;
	saved_guest_swap = egl_guest_swap_fn;
	saved_destroyed = egl_guest_surface_destroyed_fn;
	active_egl_guest_handoff_test = &t;
	host_eglCreateWindowSurface = tipsy_test_egl_guest_handoff_create;
	host_eglSwapBuffers = tipsy_test_egl_guest_handoff_swap;
	host_eglDestroySurface = tipsy_test_egl_guest_handoff_destroy;
	egl_guest_surface_created_fn = tipsy_test_egl_guest_handoff_created;
	egl_guest_swap_fn = tipsy_test_egl_guest_handoff_swap_callback;
	egl_guest_surface_destroyed_fn = tipsy_test_egl_guest_handoff_destroyed;

	/* A failed host create cannot register a generation. */
	t.next_surface = NULL;
	if (tipsy_test_egl_guest_handoff_create_window(display, xid) != NULL) {
		passed = 0;
	}

	/* First exact surface: creation callback re-enters a successful swap before
	 * local retention. The reentrant call is intentionally not signaled. */
	t.next_surface = (EGLSurface)surface;
	t.next_generation = first_generation;
	t.swap_result = TIPSY_EGL_TRUE;
	t.guest_swap_result = 1;
	t.reenter_created = 1;
	if (tipsy_test_egl_guest_handoff_create_window(display, xid) != (EGLSurface)surface) {
		passed = 0;
	}
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}
	/* The first accepted boot handoff remains retained for destroy, but a
	 * second successful guest swap must stay entirely in C. */
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE ||
		t.callback_count != 2) {
		passed = 0;
	}
	/* A failed host swap, a different surface, and a different display never
	 * cross the bridge. */
	t.swap_result = TIPSY_EGL_FALSE;
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_FALSE) {
		passed = 0;
	}
	t.swap_result = TIPSY_EGL_TRUE;
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)other_surface) != TIPSY_EGL_TRUE ||
		tipsy_eglSwapBuffers((EGLDisplay)other_display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	/* Destroy removes the entry before invoking the external callback. Its
	 * reentrant swap and a post-destroy swap are both stale. */
	t.destroy_result = TIPSY_EGL_TRUE;
	t.reenter_destroyed = 1;
	if (tipsy_eglDestroySurface((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	/* A non-Tipsy/zero-XID native window is unwrapped for the host but never
	 * registered. It cannot manufacture a bridge signal. */
	t.next_surface = (EGLSurface)other_surface;
	t.next_generation = first_generation;
	if (tipsy_test_egl_guest_handoff_create_window(display, 0) != (EGLSurface)other_surface ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)other_surface) != TIPSY_EGL_TRUE ||
		tipsy_eglDestroySurface((EGLDisplay)display, (EGLSurface)other_surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	/* Recreate the exact numeric host surface with a new generation. A destroy
	 * callback is required before the host call even when that host call fails. */
	t.next_surface = (EGLSurface)surface;
	t.next_generation = second_generation;
	/* Graphics reports -1 when this exact surface was already retired. The C
	 * gate treats every nonzero reply as handoff-complete, retaining only the
	 * generation for the later destroy callback. */
	t.guest_swap_result = -1;
	t.reenter_created = 1;
	if (tipsy_test_egl_guest_handoff_create_window(display, xid) != (EGLSurface)surface ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}
	if (tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE ||
		t.callback_count != 5) {
		passed = 0;
	}
	t.destroy_result = TIPSY_EGL_FALSE;
	t.reenter_destroyed = 1;
	if (tipsy_eglDestroySurface((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_FALSE ||
		tipsy_eglSwapBuffers((EGLDisplay)display, (EGLSurface)surface) != TIPSY_EGL_TRUE) {
		passed = 0;
	}

	if (t.create_calls != 4 || t.create_host_windows[0] != xid ||
		t.create_host_windows[1] != xid || t.create_host_windows[2] != 0 ||
		t.create_host_windows[3] != xid || t.destroy_callback_order_violations != 0 ||
		t.callback_count != 6) {
		passed = 0;
	}
	if (t.callback_count == 6 &&
		(t.callbacks[0].kind != TIPSY_EGL_GUEST_HANDOFF_CREATED ||
		 t.callbacks[1].kind != TIPSY_EGL_GUEST_HANDOFF_SWAP ||
		 t.callbacks[2].kind != TIPSY_EGL_GUEST_HANDOFF_DESTROYED ||
		 t.callbacks[3].kind != TIPSY_EGL_GUEST_HANDOFF_CREATED ||
		 t.callbacks[4].kind != TIPSY_EGL_GUEST_HANDOFF_SWAP ||
		 t.callbacks[5].kind != TIPSY_EGL_GUEST_HANDOFF_DESTROYED ||
		 t.callbacks[0].generation != first_generation ||
		 t.callbacks[1].generation != first_generation ||
		 t.callbacks[2].generation != first_generation ||
		 t.callbacks[3].generation != second_generation ||
		 t.callbacks[4].generation != second_generation ||
		 t.callbacks[5].generation != second_generation)) {
		passed = 0;
	}

	out->create_calls = t.create_calls;
	memcpy(out->create_host_windows, t.create_host_windows, sizeof(out->create_host_windows));
	out->swap_calls = t.swap_calls;
	out->destroy_calls = t.destroy_calls;
	out->destroy_callback_order_violations = t.destroy_callback_order_violations;
	out->callback_count = t.callback_count;
	memcpy(out->callbacks, t.callbacks, sizeof(out->callbacks));
	out->passed = passed;
	tipsy_egl_guest_surface_clear_all();
	egl_guest_surface_created_fn = saved_created;
	egl_guest_swap_fn = saved_guest_swap;
	egl_guest_surface_destroyed_fn = saved_destroyed;
	host_eglCreateWindowSurface = saved_create;
	host_eglSwapBuffers = saved_swap;
	host_eglDestroySurface = saved_destroy;
	active_egl_guest_handoff_test = NULL;
	return passed;
}

void *tipsy_gles_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	if (name[0] == 'e' && name[1] == 'g' && name[2] == 'l') {
		return tipsy_egl_dlsym(name);
	}
	ensure_egl();
	if (lib_gles != NULL) {
		p = dlsym(lib_gles, name);
		if (p != NULL) {
			return p;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}
