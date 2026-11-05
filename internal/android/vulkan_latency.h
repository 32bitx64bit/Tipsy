/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Opt-in Vulkan latency helpers for the Android libvulkan.so adapter.
 *
 * Anti-lag (VK_AMD_anti_lag) is possible only when TIPSY_ANTILAG=1 and the Go
 * driver probe proved RADV + VK_AMD_anti_lag. The gate starts closed.
 * With the switch unset the present wrapper does one acquire load and the
 * device-create hook does one relaxed load, then nothing else.
 */
#ifndef TIPSY_ANDROID_VULKAN_LATENCY_H
#define TIPSY_ANDROID_VULKAN_LATENCY_H

#include <stddef.h>
#include <stdint.h>
#include <stdatomic.h>

#ifdef __cplusplus
extern "C" {
#endif

/* VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_ANTI_LAG_FEATURES_AMD */
#define TIPSY_VK_ANTI_LAG_FEATURES_STYPE 1000476000u
/* VK_STRUCTURE_TYPE_ANTI_LAG_DATA_AMD */
#define TIPSY_VK_ANTI_LAG_DATA_STYPE 1000476001u
/* VK_ANTI_LAG_MODE_ON_AMD */
#define TIPSY_VK_ANTI_LAG_MODE_ON 1u

typedef void (*tipsy_vkAntiLagUpdate_fn)(void *device, const void *data);

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t antiLag;
} TipsyVkAntiLagFeatures;

typedef struct {
	uint32_t sType;
	const void *pNext;
	uint32_t mode;
	uint32_t maxFPS;
	const void *pPresentationInfo;
} TipsyVkAntiLagData;

_Static_assert(sizeof(TipsyVkAntiLagFeatures) == 24, "VkPhysicalDeviceAntiLagFeaturesAMD x86-64 ABI");
_Static_assert(sizeof(TipsyVkAntiLagData) == 32, "VkAntiLagDataAMD x86-64 ABI");

/* Installs the adapter's device-proc resolver. Idempotent; returns one. */
int tipsy_vk_latency_install_get_proc(void *fn);
/* Raw env reads, reread on every call (tests rely on this). */
int tipsy_vk_antilag_requested(void);
/* Opens the anti-lag gate. Returns 0 when TIPSY_ANTILAG=1 is unset or the
 * probe did not prove RADV; the gate then stays closed. */
int tipsy_vk_antilag_arm(const char *driver, int proved);
/* Reads the gate without side effects. */
int tipsy_vk_antilag_armed(void);
/* One vkAntiLagUpdateAMD(ON, maxFPS 0, pPresentationInfo NULL) for device.
 * Returns one when the call was made. */
int tipsy_vk_antilag_update(void *device);
int tipsy_vk_latency_feature_supported(void *physical_device, void *query);

/* vkCreateDevice hook. Injects VK_AMD_anti_lag plus the antiLag feature when
 * the gate is armed and the client did not interpose its own structs. On 0
 * the caller forwards pCreateInfo unchanged. On 1 it calls the host with
 * out_info (the caller's local TipsyVkDeviceCreateInfo) and frees the owned
 * side allocations after the host call returns. */
int tipsy_vk_latency_prepare_device_create(void *physical_device, const void *create_info,
	void *out_info, uint32_t out_info_size, int *added, int supported);
void tipsy_vk_latency_release_device_create(void *out_info);
/* One record per live device: the anti-lag update entry it exposes. */
int tipsy_vk_latency_open_session(void *device, void *anti_lag_update,
	const char *reason);
/* Records queue -> device so the present wrapper can update anti-lag. */
void tipsy_vk_latency_queue_observed(void *device, void *queue);
/* Single present-hook entry. Call only while the anti-lag gate is armed. */
void tipsy_vk_latency_present(void *queue);
/* Resolves vkAntiLagUpdateAMD for a live VkDevice through the installed device
 * resolver, after the host vkCreateDevice returned VK_SUCCESS. Call only with
 * that device. *update_out is the entry, or NULL when it is unresolved; a NULL
 * lookup (or a NULL device) closes anti-lag softly with one log. Returns 1 when
 * *update_out was set and anti-lag stays armed. */
int tipsy_vk_latency_resolve_anti_lag(void *device, void **update_out);
/* Retires every anti-lag record for device (NULL: all). */
void tipsy_vk_latency_device_destroyed(void *device);

/* Closes instance creation once per process. Returns one on the first call. */
int tipsy_vk_instance_creation_begin(void);
int tipsy_vk_instance_creation_closed(void);

/* Logs Mesa shader-cache persistence once per process. Never sets env. */
void tipsy_vk_latency_log_shader_cache(void);
/* Classifies an offline probe sample. Go finalises the RADV decision. */
void tipsy_vk_antilag_classify(const char *driver_name, uint32_t vendor_id,
	int anti_lag_listed, int software, char *out_driver, size_t out_size,
	int *out_accept);

/* Tests: reset every latency global and the cached env flags. */
void tipsy_test_vk_latency_reset(void);
/* Tests: every structure type and enum this unit relies on, checked against
 * the Vulkan headers (vulkan_core.h, Vulkan 1.4 SDK). */
typedef struct {
	const char *name;
	uint32_t value;
} TipsyVkLatencyAbiEntry;
uint32_t tipsy_test_vk_latency_abi(const TipsyVkLatencyAbiEntry *expected,
	uint32_t count);
int tipsy_test_vk_latency_chain_finds(uint32_t nodes, uint32_t target_s_type);
void tipsy_test_vk_latency_install_test_procs(void);
int tipsy_test_vk_latency_arm(const char *driver, int proved, void *device);
int tipsy_test_vk_latency_prepare_device_create(int supports_ext, int client_has_struct,
	int client_enabled, int *added);
int tipsy_test_vk_antilag_update_count(void);
int tipsy_test_vk_latency_resolve_anti_lag(void *device, void **update_out);
/* Tests: drives the adapter's production latency resolver with a fake host
 * vkGetDeviceProcAddr that returns NULL and a fake instance lookup that
 * answers non-NULL, then resolves both device entry points for device.
 * Returns bit 0 when anti-lag stayed resolved, bit 1 when present-wait
 * resolved, bit 2 when the instance fallback was consulted. */
int tipsy_test_vk_latency_adapter_resolver(void *device);
/* Installs fn as the device-proc resolver the resolve hook uses, the way
 * vulkan.c installs its host vkGetDeviceProcAddr wrapper. NULL restores. */
void tipsy_test_vk_latency_install_device_resolver(void *fn);
/* The test anti-lag entry point the fake device-proc resolver returns. */
void *tipsy_test_vk_latency_fake_device_proc(void);
int tipsy_test_vk_antilag_last_mode(void);
uint32_t tipsy_test_vk_antilag_last_max_fps(void);
int tipsy_test_vk_antilag_last_presentation_null(void);
int tipsy_test_vk_antilag_soft_closes(void);
int tipsy_test_vk_antilag_total_updates(void);
/* Tests: drives the real present entry iterations times with the gate closed. */
void tipsy_test_vk_latency_present_loop(void *queue, uint32_t iterations);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_ANDROID_VULKAN_LATENCY_H */
