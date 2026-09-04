// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <string.h>

// Minimal Vulkan 1.0 ABI declarations keep Vulkan an optional runtime
// dependency. Tipsy must still build on systems without Vulkan development
// headers or a libvulkan linker symlink.
typedef void *TipsyVkInstance;
typedef void *TipsyVkPhysicalDevice;
typedef int32_t TipsyVkResult;

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

enum {
	TIPSY_VK_NO_LIBRARY = 0,
	TIPSY_VK_NO_LOADER = 1,
	TIPSY_VK_NO_CREATE = 2,
	TIPSY_VK_CREATE_FAILED = 3,
	TIPSY_VK_NO_ENUMERATE = 4,
	TIPSY_VK_ENUMERATE_FAILED = 5,
	TIPSY_VK_NO_DEVICE = 6,
	TIPSY_VK_DEVICE_READY = 7,
};

static int tipsy_vulkan_host_probe(uint32_t *out_api, uint32_t *out_devices, int32_t *out_result) {
	void *lib = dlopen("libvulkan.so.1", RTLD_NOW | RTLD_LOCAL);
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
*/
import "C"

import "fmt"

func platformOpenGLConstructed() bool { return true }

func platformProbeHostVulkan() VulkanHostProbe {
	var api, devices C.uint32_t
	var result C.int32_t
	stage := int(C.tipsy_vulkan_host_probe(&api, &devices, &result))
	probe := VulkanHostProbe{
		Library:         stage != int(C.TIPSY_VK_NO_LIBRARY),
		Loader:          stage >= int(C.TIPSY_VK_NO_CREATE),
		APIVersion:      uint32(api),
		PhysicalDevices: uint32(devices),
		Result:          int32(result),
	}
	switch stage {
	case int(C.TIPSY_VK_NO_LIBRARY):
		probe.Detail = "host libvulkan was not found"
	case int(C.TIPSY_VK_NO_LOADER):
		probe.Detail = "vkGetInstanceProcAddr is missing"
	case int(C.TIPSY_VK_NO_CREATE):
		probe.Detail = "vkCreateInstance is missing"
	case int(C.TIPSY_VK_CREATE_FAILED):
		probe.Detail = fmt.Sprintf("vkCreateInstance failed with VkResult %d", probe.Result)
	case int(C.TIPSY_VK_NO_ENUMERATE):
		probe.Detail = "vkEnumeratePhysicalDevices is missing"
	case int(C.TIPSY_VK_ENUMERATE_FAILED):
		probe.Detail = fmt.Sprintf("vkEnumeratePhysicalDevices failed with VkResult %d", probe.Result)
	case int(C.TIPSY_VK_NO_DEVICE):
		probe.Detail = "the host Vulkan loader found no physical devices"
	case int(C.TIPSY_VK_DEVICE_READY):
		probe.Detail = fmt.Sprintf("host Vulkan loader found %d physical device(s)", probe.PhysicalDevices)
	default:
		probe.Detail = "unknown host Vulkan probe result"
	}
	return probe
}
