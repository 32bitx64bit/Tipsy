/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Android libvulkan.so adapter: identity-handle passthrough to the host
 * Khronos loader with VK_KHR_android_surface rewritten to XCB (Xlib fallback).
 * Draw-family entry points are never wrapped. WSI present-mode policy is
 * applied at vkCreateSwapchainKHR (IMMEDIATE off / FIFO on).
 */
#include "android_bridge.h"

#include <dlfcn.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <X11/Xlib.h>
#include <X11/Xlib-xcb.h>
#include <xcb/xcb.h>

extern void GoAndroid_LogMissing(char *name);
extern void GoAndroid_LogVulkanPresentMode(int vsync, int requested, int effective,
	int primaryOK, int primaryError, int fallbackOK, int fallbackError);
extern void GoAndroid_LogVulkanDevice(char **names, uint32_t n, int host_present_wait,
	int host_present_wait2, int host_present_id, int host_present_id2,
	int host_present_timing, int host_support_probed);
extern void GoAndroid_LogVulkanPacingQueries(uint64_t wait_for_present,
	uint64_t wait_for_present2, uint64_t set_present_timing_queue_size,
	uint64_t get_past_presentation_timing);

typedef void *TipsyVkInstance;
typedef void *TipsyVkPhysicalDevice;
typedef void *TipsyVkDevice;
typedef void *TipsyVkQueue;
typedef uint64_t TipsyVkSurfaceKHR;
typedef int32_t TipsyVkResult;
typedef uint32_t TipsyVkFlags;

#define TIPSY_VK_SUCCESS 0
#define TIPSY_VK_INCOMPLETE 5
#define TIPSY_VK_ERROR_INITIALIZATION_FAILED (-3)
#define TIPSY_VK_ERROR_EXTENSION_NOT_PRESENT (-7)
#define TIPSY_VK_ERROR_UNKNOWN (-13)
#define TIPSY_VK_ERROR_NATIVE_WINDOW_IN_USE_KHR (-1000000001)

#define TIPSY_VK_EXT_NAME_SIZE 256
#define TIPSY_VK_ANDROID_SURFACE_NAME "VK_KHR_android_surface"
#define TIPSY_VK_XCB_SURFACE_NAME "VK_KHR_xcb_surface"
#define TIPSY_VK_XLIB_SURFACE_NAME "VK_KHR_xlib_surface"
#define TIPSY_VK_WAYLAND_SURFACE_NAME "VK_KHR_wayland_surface"
#define TIPSY_VK_WIN32_SURFACE_NAME "VK_KHR_win32_surface"

#define TIPSY_VK_STRUCTURE_TYPE_SWAPCHAIN_CREATE_INFO_KHR 1000001000u
#define TIPSY_VK_STRUCTURE_TYPE_XLIB_SURFACE_CREATE_INFO_KHR 1000004000u
#define TIPSY_VK_STRUCTURE_TYPE_XCB_SURFACE_CREATE_INFO_KHR 1000005000u
#define TIPSY_VK_STRUCTURE_TYPE_ANDROID_SURFACE_CREATE_INFO_KHR 1000008000u

#define TIPSY_VK_PRESENT_MODE_IMMEDIATE_KHR 0u
#define TIPSY_VK_PRESENT_MODE_MAILBOX_KHR 1u
#define TIPSY_VK_PRESENT_MODE_FIFO_KHR 2u
#define TIPSY_VK_PRESENT_MODE_FIFO_RELAXED_KHR 3u

typedef struct {
	char extensionName[TIPSY_VK_EXT_NAME_SIZE];
	uint32_t specVersion;
} TipsyVkExtensionProperties;

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t flags;
	const void *pApplicationInfo;
	uint32_t enabledLayerCount;
	const char *const *ppEnabledLayerNames;
	uint32_t enabledExtensionCount;
	const char *const *ppEnabledExtensionNames;
} TipsyVkInstanceCreateInfo;

typedef struct {
	uint32_t sType;
	const void *pNext;
	TipsyVkFlags flags;
	void *window;
} TipsyVkAndroidSurfaceCreateInfoKHR;

typedef struct {
	uint32_t sType;
	const void *pNext;
	TipsyVkFlags flags;
	xcb_connection_t *connection;
	xcb_window_t window;
} TipsyVkXcbSurfaceCreateInfoKHR;

typedef struct {
	uint32_t sType;
	const void *pNext;
	TipsyVkFlags flags;
	Display *dpy;
	Window window;
} TipsyVkXlibSurfaceCreateInfoKHR;

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t flags;
	uint64_t surface;
	uint32_t minImageCount;
	uint32_t imageFormat;
	uint32_t imageColorSpace;
	uint32_t imageExtentWidth;
	uint32_t imageExtentHeight;
	uint32_t imageArrayLayers;
	uint32_t imageUsage;
	uint32_t imageSharingMode;
	uint32_t queueFamilyIndexCount;
	const uint32_t *pQueueFamilyIndices;
	uint32_t preTransform;
	uint32_t compositeAlpha;
	uint32_t presentMode;
	uint32_t clipped;
	uint64_t oldSwapchain;
} TipsyVkSwapchainCreateInfoKHR;

_Static_assert(sizeof(TipsyVkSwapchainCreateInfoKHR) == 104, "VkSwapchainCreateInfoKHR x86-64 ABI");
_Static_assert(offsetof(TipsyVkSwapchainCreateInfoKHR, presentMode) == 88, "presentMode x86-64 offset");

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t flags;
	uint32_t queueCreateInfoCount;
	const void *pQueueCreateInfos;
	uint32_t enabledLayerCount;
	const char *const *ppEnabledLayerNames;
	uint32_t enabledExtensionCount;
	const char *const *ppEnabledExtensionNames;
	const void *pEnabledFeatures;
} TipsyVkDeviceCreateInfo;

_Static_assert(sizeof(TipsyVkDeviceCreateInfo) == 72, "VkDeviceCreateInfo x86-64 ABI");
_Static_assert(offsetof(TipsyVkDeviceCreateInfo, ppEnabledExtensionNames) == 56,
	"ppEnabledExtensionNames x86-64 offset");

typedef void *(*tipsy_vkGIPA_fn)(TipsyVkInstance, const char *);
typedef void *(*tipsy_vkGDPA_fn)(TipsyVkDevice, const char *);
typedef TipsyVkResult (*tipsy_vkCreateInstance_fn)(const TipsyVkInstanceCreateInfo *, const void *, TipsyVkInstance *);
typedef TipsyVkResult (*tipsy_vkCreateDevice_fn)(TipsyVkPhysicalDevice, const TipsyVkDeviceCreateInfo *, const void *, TipsyVkDevice *);
typedef TipsyVkResult (*tipsy_vkEnumerateInstanceExtensionProperties_fn)(const char *, uint32_t *, TipsyVkExtensionProperties *);
typedef TipsyVkResult (*tipsy_vkEnumerateDeviceExtensionProperties_fn)(TipsyVkPhysicalDevice, const char *, uint32_t *, TipsyVkExtensionProperties *);
typedef TipsyVkResult (*tipsy_vkCreateXcbSurface_fn)(TipsyVkInstance, const TipsyVkXcbSurfaceCreateInfoKHR *, const void *, TipsyVkSurfaceKHR *);
typedef TipsyVkResult (*tipsy_vkCreateXlibSurface_fn)(TipsyVkInstance, const TipsyVkXlibSurfaceCreateInfoKHR *, const void *, TipsyVkSurfaceKHR *);
typedef TipsyVkResult (*tipsy_vkGetPresentModes_fn)(TipsyVkPhysicalDevice, TipsyVkSurfaceKHR, uint32_t *, uint32_t *);
typedef TipsyVkResult (*tipsy_vkQueuePresent_fn)(TipsyVkQueue, const void *);
typedef TipsyVkResult (*tipsy_vkCreateSwapchain_fn)(TipsyVkDevice, const TipsyVkSwapchainCreateInfoKHR *, const void *, uint64_t *);

static pthread_once_t vk_once = PTHREAD_ONCE_INIT;
static void *lib_vulkan;
static tipsy_vkGIPA_fn host_vkGetInstanceProcAddr;
static tipsy_vkGDPA_fn host_vkGetDeviceProcAddr;
static tipsy_vkCreateInstance_fn host_vkCreateInstance;
static tipsy_vkCreateDevice_fn host_vkCreateDevice;
static tipsy_vkEnumerateInstanceExtensionProperties_fn host_vkEnumerateInstanceExtensionProperties;
static tipsy_vkEnumerateDeviceExtensionProperties_fn host_vkEnumerateDeviceExtensionProperties;
static tipsy_vkGetPresentModes_fn host_vkGetPhysicalDeviceSurfacePresentModesKHR;
static tipsy_vkQueuePresent_fn host_vkQueuePresentKHR;
static tipsy_vkCreateSwapchain_fn host_vkCreateSwapchainKHR;
static tipsy_vkCreateSwapchain_fn test_vkCreateSwapchainKHR;
static int host_has_xcb;
static int host_has_xlib;

/*
 * E1: one-time, read-only client device-capability observation. No policy,
 * no per-call logging, no result change. The client extension list is read
 * from the first vkCreateDevice call and forwarded once to Go; host support
 * comes from a single device-extension enumeration on that physical device.
 * vkGetDeviceProcAddr pacing-name queries are counted (relaxed) and reported
 * once, the first time any count becomes non-zero.
 */
#define TIPSY_VK_HOST_PACING_EXTENSIONS 5
static const char *const vk_pacing_extension_names[TIPSY_VK_HOST_PACING_EXTENSIONS] = {
	"VK_KHR_present_wait",
	"VK_KHR_present_wait2",
	"VK_KHR_present_id",
	"VK_KHR_present_id2",
	"VK_EXT_present_timing",
};
#define TIPSY_VK_PACING_QUERY_NAMES 4
static const char *const vk_pacing_query_names[TIPSY_VK_PACING_QUERY_NAMES] = {
	"vkWaitForPresentKHR",
	"vkWaitForPresent2KHR",
	"vkSetSwapchainPresentTimingQueueSizeEXT",
	"vkGetPastPresentationTimingEXT",
};
static _Atomic int vk_create_device_logged;
static _Atomic int vk_pacing_query_logged;
static _Atomic uint64_t vk_pacing_query_counts[TIPSY_VK_PACING_QUERY_NAMES];

static Display *wsi_dpy;
static unsigned long wsi_xid;
static xcb_connection_t *wsi_xcb;

static _Atomic int vk_vsync_enabled;
/* Default off. Go enables this when the 2s graphics Info logger will emit. */
static _Atomic int vk_present_stats_enabled;
static _Atomic uint64_t vk_successful_presents;
static _Atomic uint64_t vk_first_present_ns;
static _Atomic uint64_t vk_last_present_ns;

// Opt-in platform diagnostics, entirely outside Roblox. The default path
// reads only this relaxed flag/epoch: no clock, lock, allocation, or ring
// writes. Odd epochs enable recording; changing the epoch invalidates a
// recorder that was in flight across disable/re-enable.
#define TIPSY_VK_PRESENT_TIMING_CAPACITY 4096u
static _Atomic uint64_t vk_present_timing_epoch;
static pthread_mutex_t vk_present_timing_mu = PTHREAD_MUTEX_INITIALIZER;
static uint64_t vk_present_timing_sequence;
static uint64_t vk_present_timing_ns[TIPSY_VK_PRESENT_TIMING_CAPACITY];
/* Opt-in host-call duration histogram (E2b), recorded only inside the same
 * TIPSY_PRESENT_TIMING epoch and kept in its own bounded ring so the default
 * path performs no clock, lock, or write. */
static uint64_t vk_present_call_duration_sequence;
static uint64_t vk_present_call_duration_ns[TIPSY_VK_PRESENT_TIMING_CAPACITY];

static void *tipsy_vkGetInstanceProcAddr(TipsyVkInstance instance, const char *name);
static void *tipsy_vkGetDeviceProcAddr(TipsyVkDevice device, const char *name);
static TipsyVkResult tipsy_vkCreateInstance(const TipsyVkInstanceCreateInfo *pCreateInfo, const void *pAllocator, TipsyVkInstance *pInstance);
static TipsyVkResult tipsy_vkCreateDevice(TipsyVkPhysicalDevice physicalDevice, const TipsyVkDeviceCreateInfo *pCreateInfo, const void *pAllocator, TipsyVkDevice *pDevice);
static TipsyVkResult tipsy_vkEnumerateInstanceExtensionProperties(const char *pLayerName, uint32_t *pPropertyCount, TipsyVkExtensionProperties *pProperties);
static TipsyVkResult tipsy_vkCreateAndroidSurfaceKHR(TipsyVkInstance instance, const TipsyVkAndroidSurfaceCreateInfoKHR *pCreateInfo, const void *pAllocator, TipsyVkSurfaceKHR *pSurface);
static TipsyVkResult tipsy_vkGetPhysicalDeviceSurfacePresentModesKHR(TipsyVkPhysicalDevice physicalDevice, TipsyVkSurfaceKHR surface, uint32_t *pPresentModeCount, uint32_t *pPresentModes);
static TipsyVkResult tipsy_vkCreateSwapchainKHR(TipsyVkDevice device, const TipsyVkSwapchainCreateInfoKHR *pCreateInfo, const void *pAllocator, uint64_t *pSwapchain);
static TipsyVkResult tipsy_vkQueuePresentKHR(TipsyVkQueue queue, const void *pPresentInfo);

static int hide_host_wsi_name(const char *name)
{
	if (name == NULL) {
		return 0;
	}
	return strcmp(name, TIPSY_VK_XCB_SURFACE_NAME) == 0 ||
		strcmp(name, TIPSY_VK_XLIB_SURFACE_NAME) == 0 ||
		strcmp(name, TIPSY_VK_WAYLAND_SURFACE_NAME) == 0 ||
		strcmp(name, TIPSY_VK_WIN32_SURFACE_NAME) == 0;
}

static int android_exclusive_proc(const char *name)
{
	if (name == NULL) {
		return 0;
	}
	if (strcmp(name, "vkCreateAndroidSurfaceKHR") == 0) {
		return 0;
	}
	return strstr(name, "ANDROID") != NULL ||
		strstr(name, "AHardwareBuffer") != NULL ||
		strstr(name, "Gralloc") != NULL;
}

static uint64_t tipsy_vk_monotonic_ns(void)
{
	struct timespec ts;
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return 0;
	}
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

static void tipsy_vk_record_present(uint64_t now_ns)
{
	if (now_ns != 0) {
		uint64_t expected = 0;
		atomic_compare_exchange_strong_explicit(&vk_first_present_ns, &expected, now_ns,
			memory_order_acq_rel, memory_order_acquire);
		atomic_store_explicit(&vk_last_present_ns, now_ns, memory_order_release);
	}
	atomic_fetch_add_explicit(&vk_successful_presents, 1, memory_order_relaxed);
}

static void tipsy_vk_record_present_timing(uint64_t test_ns, uint64_t duration_ns)
{
	uint64_t epoch = atomic_load_explicit(&vk_present_timing_epoch, memory_order_relaxed);
	if ((epoch & 1) == 0) {
		return;
	}
	uint64_t now_ns = test_ns;
	if (now_ns == 0) {
		// Capture before the snapshot mutex so a delayed diagnostic reader
		// does not turn its lock hold into an apparent presentation gap.
		// Host glibc uses the vDSO monotonic clock on the supported Linux host.
		now_ns = tipsy_vk_monotonic_ns();
		if (now_ns == 0) {
			return;
		}
	}
	pthread_mutex_lock(&vk_present_timing_mu);
	if (atomic_load_explicit(&vk_present_timing_epoch, memory_order_relaxed) == epoch) {
		uint64_t sequence = ++vk_present_timing_sequence;
		vk_present_timing_ns[(sequence - 1) % TIPSY_VK_PRESENT_TIMING_CAPACITY] = now_ns;
		if (duration_ns != 0) {
			uint64_t duration_sequence = ++vk_present_call_duration_sequence;
			vk_present_call_duration_ns[(duration_sequence - 1) % TIPSY_VK_PRESENT_TIMING_CAPACITY] = duration_ns;
		}
	}
	pthread_mutex_unlock(&vk_present_timing_mu);
}

static int vk_host_supports_extension(const char *name, const TipsyVkExtensionProperties *props, uint32_t n)
{
	uint32_t i;
	for (i = 0; i < n; i++) {
		if (strcmp(props[i].extensionName, name) == 0) {
			return 1;
		}
	}
	return 0;
}

static int tipsy_vk_observe_device_create(TipsyVkPhysicalDevice physical_device, const TipsyVkDeviceCreateInfo *pCreateInfo)
{
	int expected = 0;
	uint32_t i;
	int support[TIPSY_VK_HOST_PACING_EXTENSIONS];
	int probed = 0;
	const char *const *names = NULL;
	uint32_t name_count = 0;

	for (i = 0; i < TIPSY_VK_HOST_PACING_EXTENSIONS; i++) {
		support[i] = 0;
	}
	if (!atomic_compare_exchange_strong_explicit(&vk_create_device_logged, &expected, 1,
		memory_order_acq_rel, memory_order_acquire)) {
		return 0;
	}
	if (pCreateInfo != NULL) {
		name_count = pCreateInfo->enabledExtensionCount;
		names = pCreateInfo->ppEnabledExtensionNames;
		if (names == NULL) {
			name_count = 0;
		}
	}
	// One read-only enumeration on the physical device that is about to be
	// used. Allocation happens once per process and never on the present path.
	if (host_vkEnumerateDeviceExtensionProperties != NULL && physical_device != NULL) {
		uint32_t total = 0;
		if (host_vkEnumerateDeviceExtensionProperties(physical_device, NULL, &total, NULL) ==
				TIPSY_VK_SUCCESS && total > 0) {
			TipsyVkExtensionProperties *props =
				(TipsyVkExtensionProperties *)calloc(total, sizeof(*props));
			if (props != NULL) {
				uint32_t got = total;
				TipsyVkResult result =
					host_vkEnumerateDeviceExtensionProperties(physical_device, NULL, &got, props);
				if (result == TIPSY_VK_SUCCESS || result == TIPSY_VK_INCOMPLETE) {
					probed = 1;
					for (i = 0; i < TIPSY_VK_HOST_PACING_EXTENSIONS; i++) {
						support[i] = vk_host_supports_extension(vk_pacing_extension_names[i],
							props, got);
					}
				}
				free(props);
			}
		}
	}
	GoAndroid_LogVulkanDevice((char **)names, name_count, support[0], support[1],
		support[2], support[3], support[4], probed);
	return 1;
}

static void tipsy_vk_note_pacing_query(const char *name)
{
	uint32_t i;
	int index = -1;
	int expected = 0;

	if (name == NULL) {
		return;
	}
	for (i = 0; i < TIPSY_VK_PACING_QUERY_NAMES; i++) {
		if (strcmp(name, vk_pacing_query_names[i]) == 0) {
			index = (int)i;
			break;
		}
	}
	if (index < 0) {
		return;
	}
	atomic_fetch_add_explicit(&vk_pacing_query_counts[index], 1, memory_order_relaxed);
	if (atomic_compare_exchange_strong_explicit(&vk_pacing_query_logged, &expected, 1,
		memory_order_acq_rel, memory_order_acquire)) {
		uint64_t counts[TIPSY_VK_PACING_QUERY_NAMES];
		for (i = 0; i < TIPSY_VK_PACING_QUERY_NAMES; i++) {
			counts[i] = atomic_load_explicit(&vk_pacing_query_counts[i], memory_order_relaxed);
		}
		GoAndroid_LogVulkanPacingQueries(counts[0], counts[1], counts[2], counts[3]);
	}
}

static void tipsy_vk_note_present_result_at(TipsyVkResult result, uint64_t test_ns, uint64_t duration_ns)
{
	// Preserve the existing counter's exact result filtering independently
	// of the new diagnostic. A failed call never becomes a timing sample.
	if ((result == TIPSY_VK_SUCCESS || result == TIPSY_VK_INCOMPLETE) &&
		atomic_load_explicit(&vk_present_stats_enabled, memory_order_relaxed)) {
		tipsy_vk_record_present(0);
	}
	if (result == TIPSY_VK_SUCCESS) {
		tipsy_vk_record_present_timing(test_ns, duration_ns);
	}
}

static void tipsy_vk_note_present_result(TipsyVkResult result, uint64_t test_ns)
{
	tipsy_vk_note_present_result_at(result, test_ns, 0);
}

uint64_t tipsy_vk_set_present_timing(int enabled)
{
	pthread_mutex_lock(&vk_present_timing_mu);
	uint64_t epoch = atomic_load_explicit(&vk_present_timing_epoch, memory_order_relaxed);
	if ((epoch & 1) != (uint64_t)(enabled != 0)) {
		atomic_store_explicit(&vk_present_timing_epoch, epoch + 1, memory_order_relaxed);
	}
	uint64_t cursor = vk_present_timing_sequence;
	pthread_mutex_unlock(&vk_present_timing_mu);
	// Neither enable nor counter/swapchain resets clear this lifetime cursor.
	return cursor;
}

uint32_t tipsy_vk_present_timing_snapshot(uint64_t after, uint64_t *out_ns,
	uint32_t capacity, uint64_t *out_cursor, uint64_t *out_overwritten)
{
	*out_cursor = after;
	*out_overwritten = 0;
	if (out_ns == NULL || capacity == 0) {
		return 0;
	}
	pthread_mutex_lock(&vk_present_timing_mu);
	uint64_t end = vk_present_timing_sequence;
	if (after >= end) {
		pthread_mutex_unlock(&vk_present_timing_mu);
		return 0;
	}
	uint64_t first = after + 1;
	uint64_t oldest = end >= TIPSY_VK_PRESENT_TIMING_CAPACITY ?
		end - TIPSY_VK_PRESENT_TIMING_CAPACITY + 1 : 1;
	if (first < oldest) {
		*out_overwritten = oldest - first;
		first = oldest;
	}
	uint64_t count = end - first + 1;
	if (count > capacity) {
		count = capacity;
	}
	for (uint64_t i = 0; i < count; i++) {
		out_ns[i] = vk_present_timing_ns[(first + i - 1) % TIPSY_VK_PRESENT_TIMING_CAPACITY];
	}
	*out_cursor = first + count - 1;
	pthread_mutex_unlock(&vk_present_timing_mu);
	return (uint32_t)count;
}

uint32_t tipsy_vk_present_call_durations(uint64_t after, uint64_t *out_ns,
	uint32_t capacity, uint64_t *out_cursor, uint64_t *out_overwritten)
{
	*out_cursor = after;
	*out_overwritten = 0;
	if (out_ns == NULL || capacity == 0) {
		return 0;
	}
	pthread_mutex_lock(&vk_present_timing_mu);
	uint64_t end = vk_present_call_duration_sequence;
	if (after >= end) {
		pthread_mutex_unlock(&vk_present_timing_mu);
		return 0;
	}
	uint64_t first = after + 1;
	uint64_t oldest = end >= TIPSY_VK_PRESENT_TIMING_CAPACITY ?
		end - TIPSY_VK_PRESENT_TIMING_CAPACITY + 1 : 1;
	if (first < oldest) {
		*out_overwritten = oldest - first;
		first = oldest;
	}
	uint64_t count = end - first + 1;
	if (count > capacity) {
		count = capacity;
	}
	for (uint64_t i = 0; i < count; i++) {
		out_ns[i] = vk_present_call_duration_ns[(first + i - 1) % TIPSY_VK_PRESENT_TIMING_CAPACITY];
	}
	*out_cursor = first + count - 1;
	pthread_mutex_unlock(&vk_present_timing_mu);
	return (uint32_t)count;
}

static void vk_scan_host_wsi(void)
{
	uint32_t count = 0;
	TipsyVkExtensionProperties *props;
	uint32_t i;
	TipsyVkResult result;

	host_has_xcb = 0;
	host_has_xlib = 0;
	if (host_vkEnumerateInstanceExtensionProperties == NULL) {
		return;
	}
	result = host_vkEnumerateInstanceExtensionProperties(NULL, &count, NULL);
	if (result != TIPSY_VK_SUCCESS || count == 0) {
		return;
	}
	props = (TipsyVkExtensionProperties *)calloc(count, sizeof(*props));
	if (props == NULL) {
		return;
	}
	result = host_vkEnumerateInstanceExtensionProperties(NULL, &count, props);
	if (result == TIPSY_VK_SUCCESS || result == TIPSY_VK_INCOMPLETE) {
		for (i = 0; i < count; i++) {
			if (strcmp(props[i].extensionName, TIPSY_VK_XCB_SURFACE_NAME) == 0) {
				host_has_xcb = 1;
			} else if (strcmp(props[i].extensionName, TIPSY_VK_XLIB_SURFACE_NAME) == 0) {
				host_has_xlib = 1;
			}
		}
	}
	free(props);
}

static void vk_init_once(void)
{
	/*
	 * The Android Vulkan contract requires ETC2, while desktop RADV leaves
	 * its conformant shader emulation opt-in on hardware without native ETC2.
	 * Let the driver implement and advertise the format; never fabricate the
	 * Vulkan feature bit in this adapter. Preserve an explicit user override.
	 */
	if (getenv("vk_require_etc2") == NULL) {
		(void)setenv("vk_require_etc2", "true", 0);
	}
	lib_vulkan = dlopen("libvulkan.so.1", RTLD_NOW | RTLD_LOCAL);
	if (lib_vulkan == NULL) {
		lib_vulkan = dlopen("libvulkan.so", RTLD_NOW | RTLD_LOCAL);
	}
	if (lib_vulkan == NULL) {
		return;
	}
	host_vkGetInstanceProcAddr = (tipsy_vkGIPA_fn)dlsym(lib_vulkan, "vkGetInstanceProcAddr");
	if (host_vkGetInstanceProcAddr == NULL) {
		return;
	}
	host_vkGetDeviceProcAddr = (tipsy_vkGDPA_fn)host_vkGetInstanceProcAddr(NULL, "vkGetDeviceProcAddr");
	if (host_vkGetDeviceProcAddr == NULL) {
		host_vkGetDeviceProcAddr = (tipsy_vkGDPA_fn)dlsym(lib_vulkan, "vkGetDeviceProcAddr");
	}
	host_vkCreateInstance = (tipsy_vkCreateInstance_fn)host_vkGetInstanceProcAddr(NULL, "vkCreateInstance");
	if (host_vkCreateInstance == NULL) {
		host_vkCreateInstance = (tipsy_vkCreateInstance_fn)dlsym(lib_vulkan, "vkCreateInstance");
	}
	host_vkCreateDevice = (tipsy_vkCreateDevice_fn)dlsym(lib_vulkan, "vkCreateDevice");
	host_vkEnumerateDeviceExtensionProperties =
		(tipsy_vkEnumerateDeviceExtensionProperties_fn)dlsym(lib_vulkan, "vkEnumerateDeviceExtensionProperties");
	host_vkEnumerateInstanceExtensionProperties =
		(tipsy_vkEnumerateInstanceExtensionProperties_fn)host_vkGetInstanceProcAddr(NULL, "vkEnumerateInstanceExtensionProperties");
	if (host_vkEnumerateInstanceExtensionProperties == NULL) {
		host_vkEnumerateInstanceExtensionProperties =
			(tipsy_vkEnumerateInstanceExtensionProperties_fn)dlsym(lib_vulkan, "vkEnumerateInstanceExtensionProperties");
	}
	host_vkGetPhysicalDeviceSurfacePresentModesKHR =
		(tipsy_vkGetPresentModes_fn)dlsym(lib_vulkan, "vkGetPhysicalDeviceSurfacePresentModesKHR");
	host_vkQueuePresentKHR = (tipsy_vkQueuePresent_fn)dlsym(lib_vulkan, "vkQueuePresentKHR");
	vk_scan_host_wsi();
}

static void ensure_vulkan(void)
{
	(void)pthread_once(&vk_once, vk_init_once);
}

static uint32_t merge_instance_extensions(const TipsyVkExtensionProperties *host, uint32_t host_n,
	TipsyVkExtensionProperties *out, uint32_t out_cap)
{
	uint32_t n = 0;
	uint32_t i;
	int has_android = 0;
	int can_android = 0;

	for (i = 0; i < host_n; i++) {
		if (strcmp(host[i].extensionName, TIPSY_VK_XCB_SURFACE_NAME) == 0 ||
			strcmp(host[i].extensionName, TIPSY_VK_XLIB_SURFACE_NAME) == 0) {
			can_android = 1;
		}
		if (hide_host_wsi_name(host[i].extensionName) ||
			strcmp(host[i].extensionName, TIPSY_VK_ANDROID_SURFACE_NAME) == 0) {
			continue;
		}
		if (n < out_cap && out != NULL) {
			out[n] = host[i];
		}
		n++;
	}
	if (can_android) {
		if (n < out_cap && out != NULL) {
			memset(&out[n], 0, sizeof(out[n]));
			strncpy(out[n].extensionName, TIPSY_VK_ANDROID_SURFACE_NAME, TIPSY_VK_EXT_NAME_SIZE - 1);
			out[n].specVersion = 6;
			has_android = 1;
		} else if (out == NULL) {
			has_android = 1;
		}
		if (has_android) {
			n++;
		}
	}
	return n;
}

static const char *rewrite_enabled_extension(const char *name, int has_xcb, int has_xlib)
{
	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, TIPSY_VK_ANDROID_SURFACE_NAME) != 0) {
		return name;
	}
	if (has_xcb) {
		return TIPSY_VK_XCB_SURFACE_NAME;
	}
	if (has_xlib) {
		return TIPSY_VK_XLIB_SURFACE_NAME;
	}
	return NULL;
}

static void present_mode_inventory(const uint32_t *in, uint32_t n, int *has_mailbox, int *has_immediate, int *has_fifo)
{
	uint32_t i;
	*has_mailbox = 0;
	*has_immediate = 0;
	*has_fifo = 0;
	for (i = 0; i < n; i++) {
		if (in[i] == TIPSY_VK_PRESENT_MODE_MAILBOX_KHR) {
			*has_mailbox = 1;
		} else if (in[i] == TIPSY_VK_PRESENT_MODE_IMMEDIATE_KHR) {
			*has_immediate = 1;
		} else if (in[i] == TIPSY_VK_PRESENT_MODE_FIFO_KHR ||
			in[i] == TIPSY_VK_PRESENT_MODE_FIFO_RELAXED_KHR) {
			*has_fifo = 1;
		}
	}
}

static int present_mode_allowed(uint32_t mode, int vsync, int has_mailbox, int has_immediate, int has_fifo)
{
	if (vsync) {
		if (mode == TIPSY_VK_PRESENT_MODE_MAILBOX_KHR || mode == TIPSY_VK_PRESENT_MODE_IMMEDIATE_KHR) {
			return !has_fifo;
		}
		return 1;
	}
	if (mode == TIPSY_VK_PRESENT_MODE_FIFO_KHR || mode == TIPSY_VK_PRESENT_MODE_FIFO_RELAXED_KHR) {
		return !(has_mailbox || has_immediate);
	}
	return 1;
}

static uint32_t policy_present_mode(int vsync)
{
	if (vsync) {
		return TIPSY_VK_PRESENT_MODE_FIFO_KHR;
	}
	return TIPSY_VK_PRESENT_MODE_IMMEDIATE_KHR;
}

static uint32_t present_mode_rank(uint32_t mode, int vsync)
{
	if (!vsync) {
		if (mode == TIPSY_VK_PRESENT_MODE_IMMEDIATE_KHR) {
			return 0;
		}
		if (mode == TIPSY_VK_PRESENT_MODE_MAILBOX_KHR) {
			return 1;
		}
		return 10 + mode;
	}
	if (mode == TIPSY_VK_PRESENT_MODE_FIFO_KHR) {
		return 0;
	}
	if (mode == TIPSY_VK_PRESENT_MODE_FIFO_RELAXED_KHR) {
		return 1;
	}
	return 10 + mode;
}

static uint32_t filter_present_modes(const uint32_t *in, uint32_t n, int vsync, uint32_t *out)
{
	int has_mailbox, has_immediate, has_fifo;
	uint32_t i, j, m = 0;
	uint32_t tmp[64];

	if (n > 64) {
		n = 64;
	}
	present_mode_inventory(in, n, &has_mailbox, &has_immediate, &has_fifo);
	for (i = 0; i < n; i++) {
		if (present_mode_allowed(in[i], vsync, has_mailbox, has_immediate, has_fifo)) {
			tmp[m++] = in[i];
		}
	}
	for (i = 0; i < m; i++) {
		uint32_t best = i;
		for (j = i + 1; j < m; j++) {
			if (present_mode_rank(tmp[j], vsync) < present_mode_rank(tmp[best], vsync)) {
				best = j;
			}
		}
		if (best != i) {
			uint32_t swap = tmp[i];
			tmp[i] = tmp[best];
			tmp[best] = swap;
		}
	}
	if (out != NULL) {
		for (i = 0; i < m; i++) {
			out[i] = tmp[i];
		}
	}
	return m;
}

static TipsyVkResult tipsy_vkEnumerateInstanceExtensionProperties(const char *pLayerName, uint32_t *pPropertyCount, TipsyVkExtensionProperties *pProperties)
{
	uint32_t host_n = 0;
	TipsyVkExtensionProperties *host = NULL;
	TipsyVkResult result;
	uint32_t filtered;
	uint32_t copy;

	ensure_vulkan();
	if (host_vkEnumerateInstanceExtensionProperties == NULL) {
		GoAndroid_LogMissing("vkEnumerateInstanceExtensionProperties");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	if (pLayerName != NULL) {
		return host_vkEnumerateInstanceExtensionProperties(pLayerName, pPropertyCount, pProperties);
	}
	if (pPropertyCount == NULL) {
		return TIPSY_VK_ERROR_UNKNOWN;
	}
	result = host_vkEnumerateInstanceExtensionProperties(NULL, &host_n, NULL);
	if (result != TIPSY_VK_SUCCESS) {
		return result;
	}
	if (host_n > 0) {
		host = (TipsyVkExtensionProperties *)calloc(host_n, sizeof(*host));
		if (host == NULL) {
			return TIPSY_VK_ERROR_UNKNOWN;
		}
		result = host_vkEnumerateInstanceExtensionProperties(NULL, &host_n, host);
		if (result != TIPSY_VK_SUCCESS && result != TIPSY_VK_INCOMPLETE) {
			free(host);
			return result;
		}
	}
	filtered = merge_instance_extensions(host, host_n, NULL, 0);
	if (pProperties == NULL) {
		*pPropertyCount = filtered;
		free(host);
		return TIPSY_VK_SUCCESS;
	}
	copy = *pPropertyCount;
	if (copy > filtered) {
		copy = filtered;
	}
	(void)merge_instance_extensions(host, host_n, pProperties, copy);
	free(host);
	if (filtered > *pPropertyCount) {
		return TIPSY_VK_INCOMPLETE;
	}
	*pPropertyCount = filtered;
	return TIPSY_VK_SUCCESS;
}

static TipsyVkResult tipsy_vkCreateInstance(const TipsyVkInstanceCreateInfo *pCreateInfo, const void *pAllocator, TipsyVkInstance *pInstance)
{
	TipsyVkInstanceCreateInfo local;
	const char **names = NULL;
	uint32_t i;
	TipsyVkResult result;

	ensure_vulkan();
	if (host_vkCreateInstance == NULL) {
		GoAndroid_LogMissing("vkCreateInstance");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	if (pCreateInfo == NULL) {
		return host_vkCreateInstance(pCreateInfo, pAllocator, pInstance);
	}
	local = *pCreateInfo;
	if (local.enabledExtensionCount > 0 && local.ppEnabledExtensionNames != NULL) {
		names = (const char **)calloc(local.enabledExtensionCount, sizeof(*names));
		if (names == NULL) {
			return TIPSY_VK_ERROR_UNKNOWN;
		}
		for (i = 0; i < local.enabledExtensionCount; i++) {
			const char *in = local.ppEnabledExtensionNames[i];
			names[i] = rewrite_enabled_extension(in, host_has_xcb, host_has_xlib);
			if (in != NULL && names[i] == NULL) {
				free(names);
				return TIPSY_VK_ERROR_EXTENSION_NOT_PRESENT;
			}
		}
		local.ppEnabledExtensionNames = names;
	}
	result = host_vkCreateInstance(&local, pAllocator, pInstance);
	free(names);
	return result;
}

static TipsyVkResult tipsy_vkCreateDevice(TipsyVkPhysicalDevice physicalDevice,
	const TipsyVkDeviceCreateInfo *pCreateInfo, const void *pAllocator, TipsyVkDevice *pDevice)
{
	ensure_vulkan();
	if (host_vkCreateDevice == NULL) {
		host_vkCreateDevice = (tipsy_vkCreateDevice_fn)dlsym(lib_vulkan, "vkCreateDevice");
	}
	if (host_vkCreateDevice == NULL) {
		GoAndroid_LogMissing("vkCreateDevice");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	// Read-only observation before the host call so the client's request is
	// reported even when device creation itself fails. The host result is
	// returned unchanged.
	tipsy_vk_observe_device_create(physicalDevice, pCreateInfo);
	return host_vkCreateDevice(physicalDevice, pCreateInfo, pAllocator, pDevice);
}

static TipsyVkResult tipsy_vkCreateAndroidSurfaceKHR(TipsyVkInstance instance,
	const TipsyVkAndroidSurfaceCreateInfoKHR *pCreateInfo, const void *pAllocator, TipsyVkSurfaceKHR *pSurface)
{
	void *window;
	unsigned long xid;

	ensure_vulkan();
	if (pCreateInfo == NULL || pSurface == NULL) {
		return TIPSY_VK_ERROR_UNKNOWN;
	}
	if (pCreateInfo->sType != TIPSY_VK_STRUCTURE_TYPE_ANDROID_SURFACE_CREATE_INFO_KHR) {
		return TIPSY_VK_ERROR_UNKNOWN;
	}
	if (wsi_dpy == NULL || wsi_xid == 0) {
		GoAndroid_LogMissing("vkCreateAndroidSurfaceKHR(WSI unbound)");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	window = pCreateInfo->window;
	if (!tipsy_is_anative_window(window)) {
		GoAndroid_LogMissing("vkCreateAndroidSurfaceKHR");
		return TIPSY_VK_ERROR_NATIVE_WINDOW_IN_USE_KHR;
	}
	xid = (unsigned long)tipsy_ANativeWindow_get_handle(window);
	if (xid != 0 && xid != wsi_xid) {
		GoAndroid_LogMissing("vkCreateAndroidSurfaceKHR(XID mismatch)");
		return TIPSY_VK_ERROR_NATIVE_WINDOW_IN_USE_KHR;
	}
	if (xid == 0) {
		xid = wsi_xid;
	}
	if (host_has_xcb && wsi_xcb != NULL && host_vkGetInstanceProcAddr != NULL) {
		TipsyVkXcbSurfaceCreateInfoKHR xcb_info;
		tipsy_vkCreateXcbSurface_fn create_xcb;

		memset(&xcb_info, 0, sizeof(xcb_info));
		xcb_info.sType = TIPSY_VK_STRUCTURE_TYPE_XCB_SURFACE_CREATE_INFO_KHR;
		xcb_info.flags = pCreateInfo->flags;
		xcb_info.connection = wsi_xcb;
		xcb_info.window = (xcb_window_t)xid;
		create_xcb = (tipsy_vkCreateXcbSurface_fn)host_vkGetInstanceProcAddr(instance, "vkCreateXcbSurfaceKHR");
		if (create_xcb != NULL) {
			return create_xcb(instance, &xcb_info, pAllocator, pSurface);
		}
	}
	if (host_has_xlib && host_vkGetInstanceProcAddr != NULL) {
		TipsyVkXlibSurfaceCreateInfoKHR xlib_info;
		tipsy_vkCreateXlibSurface_fn create_xlib;

		memset(&xlib_info, 0, sizeof(xlib_info));
		xlib_info.sType = TIPSY_VK_STRUCTURE_TYPE_XLIB_SURFACE_CREATE_INFO_KHR;
		xlib_info.flags = pCreateInfo->flags;
		xlib_info.dpy = wsi_dpy;
		xlib_info.window = (Window)xid;
		create_xlib = (tipsy_vkCreateXlibSurface_fn)host_vkGetInstanceProcAddr(instance, "vkCreateXlibSurfaceKHR");
		if (create_xlib != NULL) {
			return create_xlib(instance, &xlib_info, pAllocator, pSurface);
		}
	}
	GoAndroid_LogMissing("vkCreateAndroidSurfaceKHR(host WSI)");
	return TIPSY_VK_ERROR_EXTENSION_NOT_PRESENT;
}

static tipsy_vkCreateSwapchain_fn resolve_create_swapchain(TipsyVkDevice device)
{
	if (test_vkCreateSwapchainKHR != NULL) {
		return test_vkCreateSwapchainKHR;
	}
	ensure_vulkan();
	if (host_vkCreateSwapchainKHR != NULL) {
		return host_vkCreateSwapchainKHR;
	}
	// Prefer the loader trampoline, exactly like the present path: it is not
	// bound to a VkDevice and cannot be poisoned by a device from another ICD.
	if (lib_vulkan != NULL) {
		host_vkCreateSwapchainKHR = (tipsy_vkCreateSwapchain_fn)dlsym(lib_vulkan, "vkCreateSwapchainKHR");
	}
	if (host_vkCreateSwapchainKHR == NULL && host_vkGetDeviceProcAddr != NULL && device != NULL) {
		// Non-cached fallback: the pointer is device-dispatch specific.
		return (tipsy_vkCreateSwapchain_fn)host_vkGetDeviceProcAddr(device, "vkCreateSwapchainKHR");
	}
	return host_vkCreateSwapchainKHR;
}

static TipsyVkResult tipsy_vkCreateSwapchainKHR(TipsyVkDevice device, const TipsyVkSwapchainCreateInfoKHR *pCreateInfo, const void *pAllocator, uint64_t *pSwapchain)
{
	tipsy_vkCreateSwapchain_fn fn;
	TipsyVkSwapchainCreateInfoKHR local;
	TipsyVkResult result;
	int vsync;
	uint32_t desired;
	uint32_t requested;

	fn = resolve_create_swapchain(device);
	if (fn == NULL) {
		GoAndroid_LogMissing("vkCreateSwapchainKHR");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	if (pCreateInfo == NULL) {
		return TIPSY_VK_ERROR_UNKNOWN;
	}
	vsync = atomic_load_explicit(&vk_vsync_enabled, memory_order_acquire);
	requested = pCreateInfo->presentMode;
	desired = policy_present_mode(vsync);
	if (requested == desired) {
		result = fn(device, pCreateInfo, pAllocator, pSwapchain);
		if (result == TIPSY_VK_SUCCESS) {
			tipsy_vk_reset_present_stats();
		}
		GoAndroid_LogVulkanPresentMode(vsync, (int)requested, (int)desired,
			result == TIPSY_VK_SUCCESS, result, 0, 0);
		return result;
	}
	local = *pCreateInfo;
	local.presentMode = desired;
	result = fn(device, &local, pAllocator, pSwapchain);
	if (result == TIPSY_VK_SUCCESS) {
		tipsy_vk_reset_present_stats();
		GoAndroid_LogVulkanPresentMode(vsync, (int)requested, (int)desired, 1, 0, 0, 0);
		return result;
	}
	{
		TipsyVkResult fallback = fn(device, pCreateInfo, pAllocator, pSwapchain);
		if (fallback == TIPSY_VK_SUCCESS) {
			tipsy_vk_reset_present_stats();
		}
		GoAndroid_LogVulkanPresentMode(vsync, (int)requested, (int)desired, 0, result,
			fallback == TIPSY_VK_SUCCESS, fallback);
		return fallback;
	}
}

static TipsyVkResult tipsy_vkGetPhysicalDeviceSurfacePresentModesKHR(TipsyVkPhysicalDevice physicalDevice,
	TipsyVkSurfaceKHR surface, uint32_t *pPresentModeCount, uint32_t *pPresentModes)
{
	uint32_t host_n = 0;
	uint32_t *host = NULL;
	TipsyVkResult result;
	uint32_t filtered;
	uint32_t copy;
	int vsync;

	ensure_vulkan();
	if (host_vkGetPhysicalDeviceSurfacePresentModesKHR == NULL && host_vkGetInstanceProcAddr != NULL) {
		host_vkGetPhysicalDeviceSurfacePresentModesKHR =
			(tipsy_vkGetPresentModes_fn)host_vkGetInstanceProcAddr(NULL, "vkGetPhysicalDeviceSurfacePresentModesKHR");
	}
	if (host_vkGetPhysicalDeviceSurfacePresentModesKHR == NULL) {
		GoAndroid_LogMissing("vkGetPhysicalDeviceSurfacePresentModesKHR");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	if (pPresentModeCount == NULL) {
		return TIPSY_VK_ERROR_UNKNOWN;
	}
	result = host_vkGetPhysicalDeviceSurfacePresentModesKHR(physicalDevice, surface, &host_n, NULL);
	if (result != TIPSY_VK_SUCCESS) {
		return result;
	}
	if (host_n > 0) {
		host = (uint32_t *)calloc(host_n, sizeof(*host));
		if (host == NULL) {
			return TIPSY_VK_ERROR_UNKNOWN;
		}
		result = host_vkGetPhysicalDeviceSurfacePresentModesKHR(physicalDevice, surface, &host_n, host);
		if (result != TIPSY_VK_SUCCESS && result != TIPSY_VK_INCOMPLETE) {
			free(host);
			return result;
		}
	}
	vsync = atomic_load_explicit(&vk_vsync_enabled, memory_order_acquire);
	filtered = filter_present_modes(host, host_n, vsync, NULL);
	if (pPresentModes == NULL) {
		*pPresentModeCount = filtered;
		free(host);
		return TIPSY_VK_SUCCESS;
	}
	copy = *pPresentModeCount;
	if (copy > filtered) {
		copy = filtered;
	}
	{
		uint32_t tmp[64];
		uint32_t n = filter_present_modes(host, host_n, vsync, tmp);
		if (copy > n) {
			copy = n;
		}
		memcpy(pPresentModes, tmp, copy * sizeof(uint32_t));
	}
	free(host);
	if (filtered > *pPresentModeCount) {
		return TIPSY_VK_INCOMPLETE;
	}
	*pPresentModeCount = filtered;
	return TIPSY_VK_SUCCESS;
}

static TipsyVkResult tipsy_vkQueuePresentKHR(TipsyVkQueue queue, const void *pPresentInfo)
{
	TipsyVkResult result;
	uint64_t epoch;
	uint64_t start_ns = 0;
	uint64_t end_ns = 0;
	uint64_t duration_ns = 0;

	ensure_vulkan();
	if (host_vkQueuePresentKHR == NULL && host_vkGetDeviceProcAddr != NULL && queue != NULL) {
		/* Loader trampoline is preferred; device GIPA is a last resort. */
		host_vkQueuePresentKHR = (tipsy_vkQueuePresent_fn)dlsym(lib_vulkan, "vkQueuePresentKHR");
	}
	if (host_vkQueuePresentKHR == NULL) {
		GoAndroid_LogMissing("vkQueuePresentKHR");
		return TIPSY_VK_ERROR_INITIALIZATION_FAILED;
	}
	// One relaxed epoch load and a predictable branch on the default path;
	// the clock, lock, and histogram writes exist only inside the opt-in
	// TIPSY_PRESENT_TIMING epoch.
	epoch = atomic_load_explicit(&vk_present_timing_epoch, memory_order_relaxed);
	if ((epoch & 1) != 0) {
		start_ns = tipsy_vk_monotonic_ns();
	}
	result = host_vkQueuePresentKHR(queue, pPresentInfo);
	if (start_ns != 0) {
		end_ns = tipsy_vk_monotonic_ns();
		if (end_ns > start_ns) {
			duration_ns = end_ns - start_ns;
		}
	}
	tipsy_vk_note_present_result_at(result, end_ns, duration_ns);
	return result;
}

static void *host_proc(TipsyVkInstance instance, const char *name)
{
	void *p = NULL;
	if (host_vkGetInstanceProcAddr != NULL) {
		p = host_vkGetInstanceProcAddr(instance, name);
	}
	if (p == NULL && lib_vulkan != NULL && name != NULL) {
		p = dlsym(lib_vulkan, name);
	}
	return p;
}

static void *tipsy_vkGetInstanceProcAddr(TipsyVkInstance instance, const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, "vkGetInstanceProcAddr") == 0) {
		return (void *)tipsy_vkGetInstanceProcAddr;
	}
	if (strcmp(name, "vkGetDeviceProcAddr") == 0) {
		return (void *)tipsy_vkGetDeviceProcAddr;
	}
	if (strcmp(name, "vkEnumerateInstanceExtensionProperties") == 0) {
		return (void *)tipsy_vkEnumerateInstanceExtensionProperties;
	}
	if (strcmp(name, "vkCreateInstance") == 0) {
		return (void *)tipsy_vkCreateInstance;
	}
	if (strcmp(name, "vkCreateDevice") == 0) {
		return (void *)tipsy_vkCreateDevice;
	}
	if (strcmp(name, "vkCreateAndroidSurfaceKHR") == 0) {
		return (void *)tipsy_vkCreateAndroidSurfaceKHR;
	}
	if (strcmp(name, "vkGetPhysicalDeviceSurfacePresentModesKHR") == 0) {
		return (void *)tipsy_vkGetPhysicalDeviceSurfacePresentModesKHR;
	}
	if (strcmp(name, "vkCreateSwapchainKHR") == 0) {
		return (void *)tipsy_vkCreateSwapchainKHR;
	}
	if (strcmp(name, "vkQueuePresentKHR") == 0) {
		return (void *)tipsy_vkQueuePresentKHR;
	}
	if (strcmp(name, "vkCreateXcbSurfaceKHR") == 0 ||
		strcmp(name, "vkCreateXlibSurfaceKHR") == 0 ||
		strcmp(name, "vkCreateWaylandSurfaceKHR") == 0 ||
		strcmp(name, "vkCreateWin32SurfaceKHR") == 0) {
		return NULL;
	}
	ensure_vulkan();
	p = host_proc(instance, name);
	if (p == NULL && android_exclusive_proc(name)) {
		GoAndroid_LogMissing((char *)name);
	}
	return p;
}

static void *tipsy_vkGetDeviceProcAddr(TipsyVkDevice device, const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	// E1 observation: count (never alter) client queries for the pacing
	// entry points; report the counts once, on the first non-zero count.
	tipsy_vk_note_pacing_query(name);
	if (strcmp(name, "vkGetDeviceProcAddr") == 0) {
		return (void *)tipsy_vkGetDeviceProcAddr;
	}
	if (strcmp(name, "vkCreateSwapchainKHR") == 0) {
		return (void *)tipsy_vkCreateSwapchainKHR;
	}
	if (strcmp(name, "vkQueuePresentKHR") == 0) {
		return (void *)tipsy_vkQueuePresentKHR;
	}
	ensure_vulkan();
	if (host_vkGetDeviceProcAddr != NULL) {
		p = host_vkGetDeviceProcAddr(device, name);
		if (p != NULL) {
			return p;
		}
	}
	return host_proc(NULL, name);
}

void *tipsy_vk_dlsym(const char *name)
{
	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, "vkGetInstanceProcAddr") == 0) {
		return (void *)tipsy_vkGetInstanceProcAddr;
	}
	if (strcmp(name, "vkGetDeviceProcAddr") == 0) {
		return (void *)tipsy_vkGetDeviceProcAddr;
	}
	if (strcmp(name, "vkEnumerateInstanceExtensionProperties") == 0) {
		return (void *)tipsy_vkEnumerateInstanceExtensionProperties;
	}
	if (strcmp(name, "vkCreateInstance") == 0) {
		return (void *)tipsy_vkCreateInstance;
	}
	if (strcmp(name, "vkCreateDevice") == 0) {
		return (void *)tipsy_vkCreateDevice;
	}
	if (strcmp(name, "vkCreateAndroidSurfaceKHR") == 0) {
		return (void *)tipsy_vkCreateAndroidSurfaceKHR;
	}
	if (strcmp(name, "vkGetPhysicalDeviceSurfacePresentModesKHR") == 0) {
		return (void *)tipsy_vkGetPhysicalDeviceSurfacePresentModesKHR;
	}
	if (strcmp(name, "vkCreateSwapchainKHR") == 0) {
		return (void *)tipsy_vkCreateSwapchainKHR;
	}
	if (strcmp(name, "vkQueuePresentKHR") == 0) {
		return (void *)tipsy_vkQueuePresentKHR;
	}
	return tipsy_vkGetInstanceProcAddr(NULL, name);
}

int tipsy_vk_bind_wsi(uintptr_t display, uintptr_t xid)
{
	if (display == 0 || xid == 0) {
		wsi_dpy = NULL;
		wsi_xid = 0;
		wsi_xcb = NULL;
		return -1;
	}
	wsi_dpy = (Display *)display;
	wsi_xid = (unsigned long)xid;
	wsi_xcb = XGetXCBConnection(wsi_dpy);
	ensure_vulkan();
	return 0;
}

void tipsy_vk_unbind_wsi(void)
{
	wsi_dpy = NULL;
	wsi_xid = 0;
	wsi_xcb = NULL;
}

int tipsy_vk_wsi_bound(void)
{
	return wsi_dpy != NULL && wsi_xid != 0;
}

void tipsy_vk_set_vsync(int enabled)
{
	atomic_store_explicit(&vk_vsync_enabled, enabled != 0, memory_order_release);
	atomic_store_explicit(&vk_successful_presents, 0, memory_order_release);
	atomic_store_explicit(&vk_first_present_ns, 0, memory_order_release);
	atomic_store_explicit(&vk_last_present_ns, 0, memory_order_release);
}

int tipsy_vk_vsync_enabled(void)
{
	return atomic_load_explicit(&vk_vsync_enabled, memory_order_acquire);
}

void tipsy_vk_present_stats(uint64_t *successful_presents, uint64_t *first_ns, uint64_t *last_ns)
{
	if (successful_presents != NULL) {
		*successful_presents = atomic_load_explicit(&vk_successful_presents, memory_order_acquire);
	}
	if (first_ns != NULL) {
		*first_ns = atomic_load_explicit(&vk_first_present_ns, memory_order_acquire);
	}
	if (last_ns != NULL) {
		*last_ns = atomic_load_explicit(&vk_last_present_ns, memory_order_acquire);
	}
}

void tipsy_vk_reset_present_stats(void)
{
	atomic_store_explicit(&vk_successful_presents, 0, memory_order_release);
	atomic_store_explicit(&vk_first_present_ns, 0, memory_order_release);
	atomic_store_explicit(&vk_last_present_ns, 0, memory_order_release);
}

void tipsy_vk_set_present_stats(int enabled)
{
	atomic_store_explicit(&vk_present_stats_enabled, enabled != 0, memory_order_relaxed);
}

int tipsy_vk_present_stats_enabled(void)
{
	return atomic_load_explicit(&vk_present_stats_enabled, memory_order_relaxed);
}

void tipsy_test_vk_record_present(uint64_t now_ns)
{
	tipsy_vk_record_present(now_ns);
}

void tipsy_test_vk_note_present_success(void)
{
	tipsy_vk_note_present_result(TIPSY_VK_SUCCESS, 0);
}

void tipsy_test_vk_note_present_result(int32_t result, uint64_t now_ns)
{
	tipsy_vk_note_present_result(result, now_ns);
}

void tipsy_test_vk_note_present_result_duration(int32_t result, uint64_t now_ns, uint64_t duration_ns)
{
	tipsy_vk_note_present_result_at(result, now_ns, duration_ns);
}

int tipsy_test_vk_observe_device_create(char **names, uint32_t n)
{
	TipsyVkDeviceCreateInfo info;
	memset(&info, 0, sizeof(info));
	info.enabledExtensionCount = n;
	info.ppEnabledExtensionNames = (const char *const *)names;
	return tipsy_vk_observe_device_create(NULL, &info);
}

void tipsy_test_vk_note_pacing_query(const char *name)
{
	tipsy_vk_note_pacing_query(name);
}

void tipsy_test_vk_pacing_query_counts(uint64_t *out)
{
	uint32_t i;
	if (out == NULL) {
		return;
	}
	for (i = 0; i < TIPSY_VK_PACING_QUERY_NAMES; i++) {
		out[i] = atomic_load_explicit(&vk_pacing_query_counts[i], memory_order_relaxed);
	}
}

int tipsy_test_vk_pacing_query_logged(void)
{
	return atomic_load_explicit(&vk_pacing_query_logged, memory_order_relaxed);
}

void *tipsy_test_vk_host_loader_symbol(const char *name)
{
	ensure_vulkan();
	if (name == NULL || lib_vulkan == NULL) {
		return NULL;
	}
	return dlsym(lib_vulkan, name);
}

void *tipsy_test_vk_resolve_create_swapchain(void)
{
	ensure_vulkan();
	host_vkCreateSwapchainKHR = NULL;
	if (lib_vulkan == NULL || dlsym(lib_vulkan, "vkCreateSwapchainKHR") == NULL) {
		return NULL;
	}
	// A non-NULL bogus device proves the loader path is taken before any
	// device-dispatch lookup could dereference it.
	return (void *)resolve_create_swapchain((TipsyVkDevice)1);
}

int tipsy_test_vk_proc_is_wrapped(const char *name)
{
	void *got;

	if (name == NULL) {
		return 0;
	}
	got = tipsy_vkGetInstanceProcAddr(NULL, name);
	if (strcmp(name, "vkCreateInstance") == 0) {
		return got == (void *)tipsy_vkCreateInstance;
	}
	if (strcmp(name, "vkCreateDevice") == 0) {
		return got == (void *)tipsy_vkCreateDevice;
	}
	if (strcmp(name, "vkCreateAndroidSurfaceKHR") == 0) {
		return got == (void *)tipsy_vkCreateAndroidSurfaceKHR;
	}
	if (strcmp(name, "vkEnumerateInstanceExtensionProperties") == 0) {
		return got == (void *)tipsy_vkEnumerateInstanceExtensionProperties;
	}
	if (strcmp(name, "vkGetInstanceProcAddr") == 0) {
		return got == (void *)tipsy_vkGetInstanceProcAddr;
	}
	if (strcmp(name, "vkGetPhysicalDeviceSurfacePresentModesKHR") == 0) {
		return got == (void *)tipsy_vkGetPhysicalDeviceSurfacePresentModesKHR;
	}
	if (strcmp(name, "vkCreateSwapchainKHR") == 0) {
		return got == (void *)tipsy_vkCreateSwapchainKHR;
	}
	if (strcmp(name, "vkQueuePresentKHR") == 0) {
		return got == (void *)tipsy_vkQueuePresentKHR;
	}
	return 0;
}

int tipsy_test_vk_proc_is_host_passthrough(const char *name)
{
	void *got;
	void *host;

	if (name == NULL) {
		return 0;
	}
	if (tipsy_test_vk_proc_is_wrapped(name)) {
		return 0;
	}
	ensure_vulkan();
	got = tipsy_vkGetInstanceProcAddr(NULL, name);
	host = host_proc(NULL, name);
	return got != NULL && got == host;
}

int tipsy_test_vk_android_surface_advertised(const char **host_names, uint32_t n)
{
	TipsyVkExtensionProperties *host;
	TipsyVkExtensionProperties *out;
	uint32_t i;
	uint32_t filtered;
	int advertised = 0;

	host = (TipsyVkExtensionProperties *)calloc(n == 0 ? 1 : n, sizeof(*host));
	out = (TipsyVkExtensionProperties *)calloc(n + 1, sizeof(*out));
	if (host == NULL || out == NULL) {
		free(host);
		free(out);
		return 0;
	}
	for (i = 0; i < n; i++) {
		if (host_names[i] != NULL) {
			strncpy(host[i].extensionName, host_names[i], TIPSY_VK_EXT_NAME_SIZE - 1);
			host[i].specVersion = 1;
		}
	}
	filtered = merge_instance_extensions(host, n, out, n + 1);
	for (i = 0; i < filtered; i++) {
		if (strcmp(out[i].extensionName, TIPSY_VK_ANDROID_SURFACE_NAME) == 0) {
			advertised = 1;
		}
		if (hide_host_wsi_name(out[i].extensionName)) {
			advertised = 0;
			break;
		}
	}
	free(host);
	free(out);
	return advertised;
}

int tipsy_test_vk_rewrite_enabled_extensions(const char **in, uint32_t n, int has_xcb, int has_xlib, const char **out)
{
	uint32_t i;
	for (i = 0; i < n; i++) {
		out[i] = rewrite_enabled_extension(in[i], has_xcb, has_xlib);
		if (in[i] != NULL && out[i] == NULL) {
			return -1;
		}
	}
	return 0;
}

int tipsy_test_vk_filter_present_modes(const uint32_t *in, uint32_t n, int vsync, uint32_t *out, uint32_t *out_n)
{
	uint32_t tmp[64];
	uint32_t m = filter_present_modes(in, n, vsync, tmp);
	if (out != NULL && out_n != NULL) {
		uint32_t i;
		uint32_t cap = *out_n;
		if (cap > m) {
			cap = m;
		}
		for (i = 0; i < cap; i++) {
			out[i] = tmp[i];
		}
	}
	if (out_n != NULL) {
		*out_n = m;
	}
	return (int)m;
}

static struct {
	int calls;
	uint32_t modes[4];
	int32_t policy_result;
	int32_t fallback_result;
	uint32_t policy_mode;
} vk_swapchain_policy_test;

static TipsyVkResult test_vkCreateSwapchainKHR_stub(TipsyVkDevice device, const TipsyVkSwapchainCreateInfoKHR *info, const void *alloc, uint64_t *out)
{
	(void)device;
	(void)alloc;
	if (vk_swapchain_policy_test.calls < 4 && info != NULL) {
		vk_swapchain_policy_test.modes[vk_swapchain_policy_test.calls] = info->presentMode;
	}
	vk_swapchain_policy_test.calls++;
	if (out != NULL) {
		*out = 1;
	}
	if (info != NULL && info->presentMode == vk_swapchain_policy_test.policy_mode) {
		return vk_swapchain_policy_test.policy_result;
	}
	return vk_swapchain_policy_test.fallback_result;
}

int tipsy_test_vk_create_swapchain_policy(int vsync, uint32_t requested,
	int32_t policy_result, int32_t fallback_result,
	uint32_t *first_mode, uint32_t *second_mode, int *calls, int32_t *final_result)
{
	tipsy_vkCreateSwapchain_fn saved;
	int saved_vsync;
	TipsyVkSwapchainCreateInfoKHR info;
	uint64_t swapchain = 0;
	TipsyVkResult result;

	saved = test_vkCreateSwapchainKHR;
	saved_vsync = atomic_load_explicit(&vk_vsync_enabled, memory_order_acquire);
	memset(&vk_swapchain_policy_test, 0, sizeof(vk_swapchain_policy_test));
	vk_swapchain_policy_test.policy_result = policy_result;
	vk_swapchain_policy_test.fallback_result = fallback_result;
	vk_swapchain_policy_test.policy_mode = policy_present_mode(vsync);
	memset(&info, 0, sizeof(info));
	info.sType = TIPSY_VK_STRUCTURE_TYPE_SWAPCHAIN_CREATE_INFO_KHR;
	info.minImageCount = 2;
	info.presentMode = requested;
	atomic_store_explicit(&vk_vsync_enabled, vsync ? 1 : 0, memory_order_release);
	test_vkCreateSwapchainKHR = test_vkCreateSwapchainKHR_stub;
	result = tipsy_vkCreateSwapchainKHR((TipsyVkDevice)1, &info, NULL, &swapchain);
	test_vkCreateSwapchainKHR = saved;
	atomic_store_explicit(&vk_vsync_enabled, saved_vsync, memory_order_release);
	if (first_mode != NULL) {
		*first_mode = vk_swapchain_policy_test.modes[0];
	}
	if (second_mode != NULL) {
		*second_mode = vk_swapchain_policy_test.modes[1];
	}
	if (calls != NULL) {
		*calls = vk_swapchain_policy_test.calls;
	}
	if (final_result != NULL) {
		*final_result = result;
	}
	return result;
}

int tipsy_vk_host_has_xcb_surface(void)
{
	ensure_vulkan();
	return host_has_xcb;
}

int tipsy_vk_host_has_xlib_surface(void)
{
	ensure_vulkan();
	return host_has_xlib;
}
