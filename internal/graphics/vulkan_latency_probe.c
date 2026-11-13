/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: AGPL-3.0-or-later
 *
 * Isolated VK_AMD_anti_lag support probe for the Graphics settings row.
 *
 * Runs in a short-lived helper process (tipsy antilag-probe) with the user's
 * environment verbatim, so the Mesa anti-lag layer can be loaded without
 * touching the real client process. Accepts only VK_DRIVER_ID_MESA_RADV on an
 * AMD vendor id with VK_AMD_anti_lag advertised on that same physical device.
 * Software rasterisers (llvmpipe, lavapipe) are rejected by name and device
 * type. When the first attempt finds no eligible device, one retry runs with
 * ENABLE_LAYER_MESA_ANTI_LAG=1 set in this process; succeeds only if that makes
 * the layer's anti-lag device appear, and then reports NeedsLayerEnv.
 *
 * This file never creates a VkDevice and never presents: it is a read-only
 * capability query.
 */
#define _DEFAULT_SOURCE 1
#include "vulkan_latency_probe.h"

#include <dlfcn.h>
#include <stdio.h>
#include <stdlib.h>
#include <stddef.h>
#include <string.h>

#define PROBE_NAME_SIZE 256
#define PROBE_MAX_DEVICES 16u
#define PROBE_MAX_EXTENSIONS 512u
#define PROBE_ANTI_LAG_EXTENSION "VK_AMD_anti_lag"
#define PROBE_ANTI_LAG_LAYER_ENV "ENABLE_LAYER_MESA_ANTI_LAG"

/* VK_STRUCTURE_TYPE_APPLICATION_INFO */
#define PROBE_APPLICATION_INFO_STYPE 0u
/* VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO */
#define PROBE_INSTANCE_CREATE_STYPE 1u
/* VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_DRIVER_PROPERTIES */
#define PROBE_DRIVER_PROPERTIES_STYPE 1000196000u
/* VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_FEATURES_2 */
#define PROBE_FEATURES_2_STYPE 1000059000u
/* VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD */
#define PROBE_ANTI_LAG_FEATURES_STYPE 1000476000u
#define PROBE_VK_SUCCESS 0
#define PROBE_CORE_FEATURES_SIZE 220u
#define PROBE_LIMITS_SIZE 504u
#define PROBE_SPARSE_SIZE 20u
#define PROBE_VENDOR_AMD 0x1002u
/* VK_DRIVER_ID_MESA_RADV */
#define PROBE_DRIVER_ID_MESA_RADV 3u
/* VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_PROPERTIES_2 */
#define PROBE_PHYSICAL_DEVICE_PROPERTIES_2_STYPE 1000059001u
/* VK_PHYSICAL_DEVICE_TYPE_CPU */
#define PROBE_DEVICE_TYPE_CPU 4u
/* VK_API_VERSION_1_2: the anti-lag feature struct needs a 1.1+ device. */
#define PROBE_API_VERSION_1_2 0x00402000u

typedef void *ProbeInstance;
typedef void *ProbePhysicalDevice;

typedef struct {
	char extensionName[PROBE_NAME_SIZE];
	uint32_t specVersion;
} ProbeExtensionProperties;

typedef struct {
	uint32_t sType;
	const void *pNext;
	const char *pApplicationName;
	uint32_t applicationVersion;
	const char *pEngineName;
	uint32_t engineVersion;
	uint32_t apiVersion;
} ProbeApplicationInfo;

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t flags;
	const ProbeApplicationInfo *pApplicationInfo;
	uint32_t enabledLayerCount;
	const char *const *ppEnabledLayerNames;
	uint32_t enabledExtensionCount;
	const char *const *ppEnabledExtensionNames;
} ProbeInstanceCreateInfo;

/* VkPhysicalDeviceProperties (Vulkan 1.0: no sType/pNext, no flags). Only
 * head fields are read; VkPhysicalDeviceLimits (504 bytes) and the sparse
 * properties (20 bytes) stay opaque, so the tail is byte-accounted. */
typedef struct {
	uint32_t apiVersion;
	uint32_t driverVersion;
	uint32_t vendorID;
	uint32_t deviceID;
	uint32_t deviceType;
	char deviceName[PROBE_NAME_SIZE];
	uint8_t pipelineCacheUUID[16];
	_Alignas(8) uint8_t limits[PROBE_LIMITS_SIZE];
	uint8_t sparseProperties[PROBE_SPARSE_SIZE];
} ProbePhysicalDeviceProperties;

/* VkPhysicalDeviceProperties2. */
typedef struct {
	uint32_t sType;
	const void *pNext;
	ProbePhysicalDeviceProperties properties;
} ProbePhysicalDeviceProperties2;

/* VkPhysicalDeviceDriverProperties, chained from Properties2. driverID is
 * authoritative for VK_DRIVER_ID_MESA_RADV. */
typedef struct {
	uint32_t sType;
	void *pNext;
	uint32_t driverID;
	char driverName[PROBE_NAME_SIZE];
	char driverInfo[PROBE_NAME_SIZE];
	uint8_t conformance[4];
} ProbeDriverProperties;

_Static_assert(sizeof(ProbePhysicalDeviceProperties) == 824,
	"VkPhysicalDeviceProperties x86-64 ABI");
_Static_assert(offsetof(ProbePhysicalDeviceProperties, deviceName) == 20,
	"deviceName offset");
_Static_assert(offsetof(ProbePhysicalDeviceProperties, vendorID) == 8,
	"vendorID offset");
_Static_assert(sizeof(ProbePhysicalDeviceProperties2) == 840,
	"VkPhysicalDeviceProperties2 x86-64 ABI");
_Static_assert(sizeof(ProbeDriverProperties) == 536, "VkPhysicalDeviceDriverProperties x86-64 ABI");
_Static_assert(offsetof(ProbeDriverProperties, driverID) == 16,
	"VkPhysicalDeviceDriverProperties driverID offset");

typedef struct {
	uint32_t sType;
	void *pNext;
	/* VkPhysicalDeviceFeatures: 55 VkBool32 fields, all ignored here. */
	uint8_t coreFeatures[PROBE_CORE_FEATURES_SIZE];
} ProbePhysicalDeviceFeatures2;

/* VkPhysicalDeviceAntiLagFeaturesAMD chained from Features2. */
typedef struct {
	uint32_t sType;
	void *pNext;
	uint32_t antiLag;
} ProbeAntiLagFeatures;

_Static_assert(sizeof(ProbePhysicalDeviceFeatures2) == 240,
	"VkPhysicalDeviceFeatures2 x86-64 ABI");
_Static_assert(sizeof(ProbeAntiLagFeatures) == 24, "VkPhysicalDeviceAntiLagFeaturesAMD x86-64 ABI");
_Static_assert(offsetof(ProbeAntiLagFeatures, antiLag) == 16,
	"VkPhysicalDeviceAntiLagFeaturesAMD antiLag offset");
_Static_assert(sizeof(ProbeExtensionProperties) == 260, "VkExtensionProperties x86-64 ABI");
/* Values from vulkan_core.h (VK_AMD_anti_lag, VK_KHR_present_id/wait) must be
 * the ones this probe queries; a header change is a compile error, not a
 * silent "unsupported". */
_Static_assert(PROBE_ANTI_LAG_FEATURES_STYPE == 1000476000u, "anti-lag features sType");
_Static_assert(PROBE_DRIVER_PROPERTIES_STYPE == 1000196000u, "driver properties sType");
_Static_assert(PROBE_FEATURES_2_STYPE == 1000059000u, "features2 sType");
_Static_assert(PROBE_PHYSICAL_DEVICE_PROPERTIES_2_STYPE == 1000059001u, "properties2 sType");
_Static_assert(PROBE_DRIVER_ID_MESA_RADV == 3u, "VK_DRIVER_ID_MESA_RADV");

typedef void *(*ProbeGIPA)(ProbeInstance, const char *);
typedef int32_t (*ProbeCreateInstance)(const ProbeInstanceCreateInfo *, const void *, ProbeInstance *);
typedef void (*ProbeDestroyInstance)(ProbeInstance, const void *);
typedef int32_t (*ProbeEnumeratePhysicalDevices)(ProbeInstance, uint32_t *, ProbePhysicalDevice *);
typedef int32_t (*ProbeEnumerateDeviceExtensionProperties)(ProbePhysicalDevice, const char *,
	uint32_t *, ProbeExtensionProperties *);
typedef void (*ProbeGetPhysicalDeviceProperties)(ProbePhysicalDevice, ProbePhysicalDeviceProperties *);
typedef void (*ProbeGetPhysicalDeviceProperties2)(ProbePhysicalDevice, void *);
typedef void (*ProbeGetPhysicalDeviceFeatures2)(ProbePhysicalDevice, ProbePhysicalDeviceFeatures2 *);

typedef struct {
	void *lib;
	ProbeGIPA get_proc;
	ProbeCreateInstance create;
	ProbeDestroyInstance destroy;
	ProbeEnumeratePhysicalDevices enumerate;
	ProbeEnumerateDeviceExtensionProperties extensions;
	ProbeGetPhysicalDeviceProperties properties;
	ProbeGetPhysicalDeviceProperties2 properties2;
	ProbeGetPhysicalDeviceFeatures2 features2;
} ProbeApi;

static int probe_name_is_software(const char *name)
{
	if (name == NULL) {
		return 0;
	}
	return strstr(name, "llvmpipe") != NULL || strstr(name, "lavapipe") != NULL ||
		strstr(name, "softpipe") != NULL || strstr(name, "swiftshader") != NULL ||
		strstr(name, "SwiftShader") != NULL;
}

static void probe_set_reason(TipsyAntiLagProbe *result, const char *text)
{
	snprintf(result->Reason, sizeof(result->Reason), "%s", text != NULL ? text : "");
}

static int probe_open(ProbeApi *api)
{
	memset(api, 0, sizeof(*api));
	api->lib = dlopen("libvulkan.so.1", RTLD_NOW | RTLD_LOCAL);
	if (api->lib == NULL) {
		api->lib = dlopen("libvulkan.so", RTLD_NOW | RTLD_LOCAL);
	}
	if (api->lib == NULL) {
		return 0;
	}
	api->get_proc = (ProbeGIPA)dlsym(api->lib, "vkGetInstanceProcAddr");
	if (api->get_proc == NULL) {
		return 0;
	}
	/* Instance-level entries are only guaranteed after vkCreateInstance on
	 * every loader, so they are resolved again from the live instance. */
	api->create = (ProbeCreateInstance)api->get_proc(NULL, "vkCreateInstance");
	return api->create != NULL;
}

static void probe_close(ProbeApi *api)
{
	if (api->lib != NULL) {
		dlclose(api->lib);
		api->lib = NULL;
	}
}

/* Fills result for one physical device. Returns one when it may drive
 * anti-lag (AMD vendor id, not a software device, extension and feature both
 * present). */
static int probe_device(TipsyAntiLagProbe *result, ProbePhysicalDevice device, const ProbeApi *api)
{
	ProbePhysicalDeviceProperties props;
	uint32_t total = 0;
	uint32_t i;
	int radv = 0;

	memset(&props, 0, sizeof(props));
	api->properties(device, &props);
	if (result->Driver[0] == '\0') {
		snprintf(result->Driver, sizeof(result->Driver), "%s", props.deviceName);
	}
	if (props.deviceType == PROBE_DEVICE_TYPE_CPU || probe_name_is_software(props.deviceName)) {
		probe_set_reason(result, "software rasteriser is not eligible for anti-lag");
		return 0;
	}
	if (props.vendorID != PROBE_VENDOR_AMD) {
		char text[128];
		snprintf(text, sizeof(text), "vendor 0x%04x is not AMD", props.vendorID);
		probe_set_reason(result, text);
		result->VendorID = props.vendorID;
		result->DeviceType = props.deviceType;
		return 0;
	}
	/* VK_DRIVER_ID_MESA_RADV is 3. Vulkan 1.2+ exposes driverID; older
	 * properties only carry the conventional "RADV" device-name prefix. */
	if (api->properties2 != NULL) {
		ProbePhysicalDeviceProperties2 props2;
		ProbeDriverProperties driver;
		memset(&props2, 0, sizeof(props2));
		memset(&driver, 0, sizeof(driver));
		props2.sType = PROBE_PHYSICAL_DEVICE_PROPERTIES_2_STYPE;
		props2.pNext = &driver;
		driver.sType = PROBE_DRIVER_PROPERTIES_STYPE;
		api->properties2(device, &props2);
		/* RADV on current Mesa leaves driverID 0 and identifies itself only
		 * through the device name; trust driverName only when driverID is a
		 * real id (some drivers report "radv"-like generic names otherwise). */
		radv = driver.driverID == PROBE_DRIVER_ID_MESA_RADV ||
			(driver.driverID == 0 && (strncmp(props.deviceName, "RADV", 4) == 0 ||
				strstr(props.deviceName, "RADV ") != NULL));
		if (driver.driverID != 0 && driver.driverName[0] != '\0') {
			snprintf(result->Driver, sizeof(result->Driver), "%s", driver.driverName);
		}
	} else {
		radv = strncmp(props.deviceName, "RADV", 4) == 0 ||
			strstr(props.deviceName, "RADV ") != NULL;
	}
	if (!radv) {
		probe_set_reason(result, "driver is not VK_DRIVER_ID_MESA_RADV");
		result->VendorID = props.vendorID;
		result->DeviceType = props.deviceType;
		return 0;
	}
	if (api->extensions == NULL ||
		api->extensions(device, NULL, &total, NULL) != PROBE_VK_SUCCESS || total == 0) {
		probe_set_reason(result, "device extensions could not be enumerated");
		return 0;
	}
	{
		ProbeExtensionProperties *exts =
			(ProbeExtensionProperties *)calloc(total, sizeof(*exts));
		uint32_t got = total;
		int listed = 0;
		if (exts == NULL) {
			probe_set_reason(result, "device extension query could not allocate");
			return 0;
		}
		if (api->extensions(device, NULL, &got, exts) != PROBE_VK_SUCCESS) {
			free(exts);
			probe_set_reason(result, "device extensions could not be enumerated");
			return 0;
		}
		for (i = 0; i < got && i < total; i++) {
			if (strcmp(exts[i].extensionName, PROBE_ANTI_LAG_EXTENSION) == 0) {
				listed = 1;
				break;
			}
		}
		free(exts);
		if (!listed) {
			probe_set_reason(result, "VK_AMD_anti_lag is not exposed");
			result->VendorID = props.vendorID;
			result->DeviceType = props.deviceType;
			return 0;
		}
	}
	if (api->features2 == NULL) {
		probe_set_reason(result, "antiLag feature query unavailable");
		return 0;
	}
	{
		ProbePhysicalDeviceFeatures2 features;
		ProbeAntiLagFeatures anti_lag;
		memset(&features, 0, sizeof(features));
		memset(&anti_lag, 0, sizeof(anti_lag));
		features.sType = PROBE_FEATURES_2_STYPE;
		features.pNext = &anti_lag;
		anti_lag.sType = PROBE_ANTI_LAG_FEATURES_STYPE;
		api->features2(device, &features);
		if (!anti_lag.antiLag) {
			probe_set_reason(result, "VK_AMD_anti_lag is listed but antiLag is unsupported");
			return 0;
		}
	}
	snprintf(result->Driver, sizeof(result->Driver), "%s", props.deviceName);
	result->VendorID = props.vendorID;
	result->DeviceType = props.deviceType;
	result->Listed = 1;
	probe_set_reason(result, "RADV exposes VK_AMD_anti_lag on an AMD device");
	return 1;
}

/* One full probe attempt. Returns one when some physical device qualifies. */
static int probe_attempt(TipsyAntiLagProbe *result)
{
	ProbeApi api;
	ProbeApplicationInfo app;
	ProbeInstanceCreateInfo info;
	ProbeInstance instance = NULL;
	uint32_t count = 0;
	uint32_t i;
	int accepted = 0;

	memset(result, 0, sizeof(*result));
	if (!probe_open(&api)) {
		probe_set_reason(result, "no usable libvulkan loader");
		probe_close(&api);
		return 0;
	}
	memset(&app, 0, sizeof(app));
	app.sType = PROBE_APPLICATION_INFO_STYPE;
	app.pApplicationName = "tipsy-antilag-probe";
	app.pEngineName = "tipsy";
	app.apiVersion = PROBE_API_VERSION_1_2;
	memset(&info, 0, sizeof(info));
	info.sType = PROBE_INSTANCE_CREATE_STYPE;
	info.pApplicationInfo = &app;
	if (api.create(&info, NULL, &instance) != PROBE_VK_SUCCESS || instance == NULL) {
		probe_set_reason(result, "vkCreateInstance failed");
		probe_close(&api);
		return 0;
	}
	api.destroy = (ProbeDestroyInstance)api.get_proc(instance, "vkDestroyInstance");
	api.enumerate = (ProbeEnumeratePhysicalDevices)api.get_proc(instance, "vkEnumeratePhysicalDevices");
	api.extensions = (ProbeEnumerateDeviceExtensionProperties)
		api.get_proc(instance, "vkEnumerateDeviceExtensionProperties");
	api.properties = (ProbeGetPhysicalDeviceProperties)
		api.get_proc(instance, "vkGetPhysicalDeviceProperties");
	api.properties2 = (ProbeGetPhysicalDeviceProperties2)
		api.get_proc(instance, "vkGetPhysicalDeviceProperties2");
	api.features2 = (ProbeGetPhysicalDeviceFeatures2)
		api.get_proc(instance, "vkGetPhysicalDeviceFeatures2");
	if (api.enumerate == NULL || api.properties == NULL) {
		probe_set_reason(result, "instance-level Vulkan entry points are missing");
		if (api.destroy != NULL) {
			api.destroy(instance, NULL);
		}
		probe_close(&api);
		return 0;
	}
	if (api.enumerate(instance, &count, NULL) == PROBE_VK_SUCCESS && count > 0) {
		ProbePhysicalDevice devices[PROBE_MAX_DEVICES];
		uint32_t got = count > PROBE_MAX_DEVICES ? PROBE_MAX_DEVICES : count;
		if (api.enumerate(instance, &got, devices) == PROBE_VK_SUCCESS || got > 0) {
			for (i = 0; i < got && i < PROBE_MAX_DEVICES; i++) {
				TipsyAntiLagProbe one;
				memset(&one, 0, sizeof(one));
				if (probe_device(&one, devices[i], &api)) {
					*result = one;
					accepted = 1;
					break;
				}
				/* Keep the first ineligible device in the report. */
				if (result->Driver[0] == '\0') {
					snprintf(result->Driver, sizeof(result->Driver), "%s", one.Driver);
					snprintf(result->Reason, sizeof(result->Reason), "%s", one.Reason);
					result->VendorID = one.VendorID;
					result->DeviceType = one.DeviceType;
					result->Listed = one.Listed;
				}
			}
		}
	} else {
		probe_set_reason(result, "vkEnumeratePhysicalDevices failed");
	}
	if (api.destroy != NULL) {
		api.destroy(instance, NULL);
	}
	probe_close(&api);
	return accepted;
}

int tipsy_vulkan_antilag_probe(void)
{
	TipsyAntiLagProbe result;
	int accepted = probe_attempt(&result);
	if (!accepted) {
		/* One retry with the Mesa anti-lag layer named in this child only. */
		const char *previous = getenv(PROBE_ANTI_LAG_LAYER_ENV);
		char saved[PROBE_NAME_SIZE];
		snprintf(saved, sizeof(saved), "%s", previous != NULL ? previous : "");
		if (setenv(PROBE_ANTI_LAG_LAYER_ENV, "1", 1) == 0) {
			TipsyAntiLagProbe second;
			if (probe_attempt(&second)) {
				second.NeedsLayerEnv = 1;
				probe_set_reason(&second,
					"VK_AMD_anti_lag needs ENABLE_LAYER_MESA_ANTI_LAG=1");
				result = second;
				accepted = 1;
			}
			if (previous != NULL) {
				setenv(PROBE_ANTI_LAG_LAYER_ENV, saved, 1);
			} else {
				unsetenv(PROBE_ANTI_LAG_LAYER_ENV);
			}
		}
	}
	result.Available = accepted ? 1 : 0;
	fprintf(stdout, "available=%d\n", result.Available);
	fprintf(stdout, "needs_layer_env=%d\n", result.NeedsLayerEnv);
	fprintf(stdout, "driver=%s\n", result.Driver);
	fprintf(stdout, "vendor=0x%04x\n", result.VendorID);
	fprintf(stdout, "listed=%d\n", result.Listed);
	fprintf(stdout, "reason=%s\n", result.Reason);
	fflush(stdout);
	return accepted ? 0 : 1;
}
