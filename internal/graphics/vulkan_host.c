/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "vulkan_host.h"

#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// Minimal Vulkan 1.0 ABI declarations keep Vulkan an optional runtime
// dependency. Tipsy must still build on systems without Vulkan development
// headers or a libvulkan linker symlink.
typedef void *TipsyVkInstance;
typedef void *TipsyVkPhysicalDevice;
typedef int32_t TipsyVkResult;

typedef struct {
	char extensionName[256];
	uint32_t specVersion;
} TipsyVkExtensionProperties;

typedef struct {
	uint32_t sType;
	const void *pNext;
	const char *pApplicationName;
	uint32_t applicationVersion;
	const char *pEngineName;
	uint32_t engineVersion;
	uint32_t apiVersion;
} TipsyVkApplicationInfo;

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t flags;
	const TipsyVkApplicationInfo *pApplicationInfo;
	uint32_t enabledLayerCount;
	const char *const *ppEnabledLayerNames;
	uint32_t enabledExtensionCount;
	const char *const *ppEnabledExtensionNames;
} TipsyVkInstanceCreateInfo;

typedef void *(*tipsy_vkGetInstanceProcAddr_fn)(TipsyVkInstance, const char *);
typedef TipsyVkResult (*tipsy_vkCreateInstance_fn)(const TipsyVkInstanceCreateInfo *, const void *, TipsyVkInstance *);
typedef void (*tipsy_vkDestroyInstance_fn)(TipsyVkInstance, const void *);
typedef TipsyVkResult (*tipsy_vkEnumeratePhysicalDevices_fn)(TipsyVkInstance, uint32_t *, TipsyVkPhysicalDevice *);
typedef TipsyVkResult (*tipsy_vkEnumerateInstanceVersion_fn)(uint32_t *);
typedef TipsyVkResult (*tipsy_vkEnumerateInstanceExtensionProperties_fn)(const char *, uint32_t *, TipsyVkExtensionProperties *);

static void tipsy_vulkan_scan_wsi(tipsy_vkGetInstanceProcAddr_fn get_proc, int *out_xcb, int *out_xlib) {
	tipsy_vkEnumerateInstanceExtensionProperties_fn enumerate_ext;
	uint32_t count = 0;
	TipsyVkExtensionProperties *props;
	uint32_t i;
	TipsyVkResult result;

	if (out_xcb == NULL || out_xlib == NULL) {
		return;
	}
	*out_xcb = 0;
	*out_xlib = 0;
	if (get_proc == NULL) {
		return;
	}
	enumerate_ext = (tipsy_vkEnumerateInstanceExtensionProperties_fn)get_proc(NULL, "vkEnumerateInstanceExtensionProperties");
	if (enumerate_ext == NULL) {
		return;
	}
	result = enumerate_ext(NULL, &count, NULL);
	if (result != 0 || count == 0) {
		return;
	}
	props = (TipsyVkExtensionProperties *)malloc((size_t)count * sizeof(*props));
	if (props == NULL) {
		return;
	}
	memset(props, 0, (size_t)count * sizeof(*props));
	result = enumerate_ext(NULL, &count, props);
	if (result == 0 || result == 5) {
		for (i = 0; i < count; i++) {
			if (strcmp(props[i].extensionName, "VK_KHR_xcb_surface") == 0) {
				*out_xcb = 1;
			} else if (strcmp(props[i].extensionName, "VK_KHR_xlib_surface") == 0) {
				*out_xlib = 1;
			}
		}
	}
	free(props);
}

int tipsy_vulkan_host_probe(uint32_t *out_api, uint32_t *out_devices, int32_t *out_result, int *out_xcb, int *out_xlib) {
	void *lib;
	if (out_xcb != NULL) {
		*out_xcb = 0;
	}
	if (out_xlib != NULL) {
		*out_xlib = 0;
	}
	lib = dlopen("libvulkan.so.1", RTLD_NOW | RTLD_LOCAL);
	if (lib == NULL) {
		lib = dlopen("libvulkan.so", RTLD_NOW | RTLD_LOCAL);
	}
	if (lib == NULL) {
		return TIPSY_VK_NO_LIBRARY;
	}

	tipsy_vkGetInstanceProcAddr_fn get_proc = (tipsy_vkGetInstanceProcAddr_fn)dlsym(lib, "vkGetInstanceProcAddr");
	if (get_proc == NULL) {
		dlclose(lib);
		return TIPSY_VK_NO_LOADER;
	}
	tipsy_vulkan_scan_wsi(get_proc, out_xcb, out_xlib);

	tipsy_vkEnumerateInstanceVersion_fn enumerate_version =
		(tipsy_vkEnumerateInstanceVersion_fn)get_proc(NULL, "vkEnumerateInstanceVersion");
	if (enumerate_version != NULL) {
		(void)enumerate_version(out_api);
	}

	tipsy_vkCreateInstance_fn create_instance =
		(tipsy_vkCreateInstance_fn)get_proc(NULL, "vkCreateInstance");
	if (create_instance == NULL) {
		create_instance = (tipsy_vkCreateInstance_fn)dlsym(lib, "vkCreateInstance");
	}
	if (create_instance == NULL) {
		dlclose(lib);
		return TIPSY_VK_NO_CREATE;
	}

	TipsyVkApplicationInfo app;
	memset(&app, 0, sizeof(app));
	app.sType = 0; // VK_STRUCTURE_TYPE_APPLICATION_INFO
	app.pApplicationName = "Tipsy capability probe";
	app.applicationVersion = 1;
	app.pEngineName = "Tipsy";
	app.engineVersion = 1;
	app.apiVersion = (1u << 22); // VK_API_VERSION_1_0

	TipsyVkInstanceCreateInfo create;
	memset(&create, 0, sizeof(create));
	create.sType = 1; // VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO
	create.pApplicationInfo = &app;

	TipsyVkInstance instance = NULL;
	TipsyVkResult result = create_instance(&create, NULL, &instance);
	*out_result = result;
	if (result != 0 || instance == NULL) {
		dlclose(lib);
		return TIPSY_VK_CREATE_FAILED;
	}

	tipsy_vkDestroyInstance_fn destroy_instance =
		(tipsy_vkDestroyInstance_fn)get_proc(instance, "vkDestroyInstance");
	tipsy_vkEnumeratePhysicalDevices_fn enumerate_devices =
		(tipsy_vkEnumeratePhysicalDevices_fn)get_proc(instance, "vkEnumeratePhysicalDevices");
	if (enumerate_devices == NULL) {
		if (destroy_instance != NULL) {
			destroy_instance(instance, NULL);
		}
		dlclose(lib);
		return TIPSY_VK_NO_ENUMERATE;
	}

	uint32_t count = 0;
	result = enumerate_devices(instance, &count, NULL);
	*out_result = result;
	*out_devices = count;
	if (destroy_instance != NULL) {
		destroy_instance(instance, NULL);
	}
	dlclose(lib);
	if (result != 0) {
		return TIPSY_VK_ENUMERATE_FAILED;
	}
	if (count == 0) {
		return TIPSY_VK_NO_DEVICE;
	}
	return TIPSY_VK_DEVICE_READY;
}
