/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Opt-in Vulkan latency helpers. See vulkan_latency.h for the contract.
 *
 * Anti-lag (VK_AMD_anti_lag) is possible only when TIPSY_ANTILAG=1 and the Go
 * driver probe proved RADV + VK_AMD_anti_lag on the same physical device. The
 * gate starts closed, so no anti-lag call exists before that proof. Any
 * disagreement closes it softly with exactly one CatGraphics report.
 *
 * Default cost, switch unset: one acquire load per present and one relaxed
 * load per device create. No lock, allocation, clock, log or counter is
 * touched.
 */
#include "vulkan_latency.h"

#include <pthread.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

extern void GoAndroid_LogVulkanAntiLag(int state, char *reason);
extern void GoAndroid_LogVulkanShaderCache(int persistent, char *dir);

#define TIPSY_VK_LATENCY_MAX 8u
#define TIPSY_VK_ANTI_LAG_EXTENSION "VK_AMD_anti_lag"
#define TIPSY_VK_ANTI_LAG_PROC "vkAntiLagUpdateAMD"
/* VkDeviceCreateInfo x86-64 ABI (pinned by vulkan.c). */
#define TIPSY_VK_DEVICE_CREATE_INFO_SIZE 72u
#define TIPSY_VK_DEVICE_CREATE_PNEXT 8u
#define TIPSY_VK_DEVICE_CREATE_EXT_COUNT 48u
#define TIPSY_VK_DEVICE_CREATE_EXT_NAMES 56u

typedef void *(*TipsyVkLatencyGetProc)(void *object, const char *name);

typedef struct {
	_Atomic(void *) device;
	tipsy_vkAntiLagUpdate_fn update;
} TipsyVkLatencyDevice;

static _Atomic int vk_latency_antilag_wanted;
static _Atomic int vk_antilag_armed;
static _Atomic int vk_antilag_closed;
static _Atomic int vk_antilag_updates;
static _Atomic int vk_antilag_soft_closes;
static _Atomic int vk_instance_creation_closed;
static _Atomic int vk_shader_cache_logged;
/* Test-visible: every latency-unit mutex acquisition and anti-lag report.
 * The default (unarmed) path reaches neither, so both stay zero. */
static _Atomic int vk_latency_test_locks;
static _Atomic int vk_latency_test_logs;
static TipsyVkLatencyGetProc vk_latency_get_proc;
static pthread_mutex_t vk_latency_mu = PTHREAD_MUTEX_INITIALIZER;

static void latency_lock(void)
{
	atomic_fetch_add_explicit(&vk_latency_test_locks, 1, memory_order_relaxed);
	pthread_mutex_lock(&vk_latency_mu);
}

static void latency_unlock(void)
{
	pthread_mutex_unlock(&vk_latency_mu);
}

void tipsy_test_vk_latency_reset_counters(void);
static TipsyVkLatencyDevice vk_latency_devices[TIPSY_VK_LATENCY_MAX];
/* Queue -> owning device, filled by GetDeviceQueue so anti-lag can be updated
 * from the present wrapper. */
static _Atomic(void *) vk_latency_queues[TIPSY_VK_LATENCY_MAX];
static _Atomic(void *) vk_latency_queue_devices[TIPSY_VK_LATENCY_MAX];

/* Caller holds vk_latency_mu. Disabled launches must not retain handles that
 * may be destroyed while the gate is closed and subsequently reused. */
static void latency_clear_locked(void)
{
	for (uint32_t i = 0; i < TIPSY_VK_LATENCY_MAX; i++) {
		atomic_store_explicit(&vk_latency_devices[i].device, NULL, memory_order_relaxed);
		vk_latency_devices[i].update = NULL;
		atomic_store_explicit(&vk_latency_queues[i], NULL, memory_order_relaxed);
		atomic_store_explicit(&vk_latency_queue_devices[i], NULL, memory_order_relaxed);
	}
}
/* Owned side allocations of one published clone, freed after the host call. */
typedef struct TipsyVkAntiLagPublish {
	void *out_info;
	char **names;
	uint32_t name_count;
	TipsyVkAntiLagFeatures *feature;
	void *original_pnext;
	uint32_t original_ext_count;
	const char *const *original_ext_names;
	struct TipsyVkAntiLagPublish *next;
} TipsyVkAntiLagPublish;

static TipsyVkAntiLagPublish *vk_antilag_publish;

static int antilag_close(const char *reason);

static int env_is_true(const char *name)
{
	const char *value = getenv(name);
	const char *end;
	if (value == NULL) {
		return 0;
	}
	while (*value == ' ' || (*value >= '\t' && *value <= '\r')) value++;
	end = value + strlen(value);
	while (end > value && (end[-1] == ' ' || (end[-1] >= '\t' && end[-1] <= '\r'))) end--;
	return (end - value == 1 && value[0] == '1') ||
		(end - value == 4 && strncasecmp(value, "true", 4) == 0) ||
		(end - value == 3 && strncasecmp(value, "yes", 3) == 0) ||
		(end - value == 2 && strncasecmp(value, "on", 2) == 0);
}

/* Switches are reread at arm time and cached; the present path only loads the
 * cached flag. */
static void read_env_locked(void)
{
	atomic_store_explicit(&vk_latency_antilag_wanted, env_is_true("TIPSY_ANTILAG"), memory_order_relaxed);
}

int tipsy_vk_latency_install_get_proc(void *fn)
{
	vk_latency_get_proc = (TipsyVkLatencyGetProc)fn;
	return 1;
}

/* Raw reads for the go side and tests; never cached. */
int tipsy_vk_antilag_requested(void) { return env_is_true("TIPSY_ANTILAG"); }

int tipsy_vk_latency_feature_supported(void *physical_device, void *query)
{
	struct { uint32_t sType; void *pNext; uint32_t features[55]; } features = {0};
	TipsyVkAntiLagFeatures anti = {0};
	if (physical_device == NULL || query == NULL) return 0;
	features.sType = 1000059000u;
	features.pNext = &anti;
	anti.sType = TIPSY_VK_ANTI_LAG_FEATURES_STYPE;
	((void (*)(void *, void *))query)(physical_device, &features);
	return anti.antiLag != 0;
}

int tipsy_vk_antilag_armed(void)
{
	return atomic_load_explicit(&vk_antilag_armed, memory_order_acquire);
}

int tipsy_vk_antilag_arm(const char *driver, int proved)
{
	int open = proved && driver != NULL && strcmp(driver, "radv") == 0 &&
		env_is_true("TIPSY_ANTILAG");
	latency_lock();
	read_env_locked();
	atomic_store_explicit(&vk_antilag_closed, 0, memory_order_release);
	if (!open) {
		atomic_store_explicit(&vk_antilag_armed, 0, memory_order_release);
		latency_clear_locked();
	}
	atomic_store_explicit(&vk_antilag_armed, open ? 1 : 0, memory_order_release);
	latency_unlock();
	return open;
}

/* Close the gate softly and report once. Caller holds vk_latency_mu. */
static void antilag_close_locked(const char *reason)
{
	char text[256];
	if (atomic_load_explicit(&vk_antilag_closed, memory_order_relaxed)) {
		return;
	}
	atomic_store_explicit(&vk_antilag_closed, 1, memory_order_relaxed);
	atomic_store_explicit(&vk_antilag_armed, 0, memory_order_release);
	latency_clear_locked();
	atomic_fetch_add_explicit(&vk_antilag_soft_closes, 1, memory_order_relaxed);
	atomic_fetch_add_explicit(&vk_latency_test_logs, 1, memory_order_relaxed);
	snprintf(text, sizeof(text), "%s", reason != NULL ? reason : "unknown");
	GoAndroid_LogVulkanAntiLag(1, text);
}

static int antilag_close(const char *reason)
{
	latency_lock();
	antilag_close_locked(reason);
	latency_unlock();
	return 0;
}

/* Walk a const sType/pNext chain read-only. Returns the first struct whose
 * sType matches, or NULL. Bounded so a cyclic chain cannot spin. */
static const void *chain_find_stype(const void *pnext, uint32_t s_type)
{
	const uint8_t *p = (const uint8_t *)pnext;
	int steps = 0;
	while (p != NULL && steps < 64) {
		uint32_t type;
		const void *next;
		memcpy(&type, p, sizeof(type));
		if (type == s_type) {
			return p;
		}
		memcpy(&next, p + 8u, sizeof(next));
		p = (const uint8_t *)next;
		steps++;
	}
	return NULL;
}

static int chain_has_stype(const void *pnext, uint32_t s_type)
{
	return chain_find_stype(pnext, s_type) != NULL;
}

static int names_contains(uint32_t count, const char *const *names, const char *name)
{
	uint32_t i;
	if (names == NULL || count > 4096u) {
		return 0;
	}
	for (i = 0; i < count; i++) {
		if (names[i] != NULL && strcmp(names[i], name) == 0) {
			return 1;
		}
	}
	return 0;
}

static char *name_dup(const char *str)
{
	size_t n;
	char *copy;
	if (str == NULL) {
		str = "";
	}
	n = strlen(str) + 1u;
	copy = (char *)malloc(n);
	if (copy != NULL) {
		memcpy(copy, str, n);
	}
	return copy;
}

/* Extends the caller's stack copy out with VK_AMD_anti_lag and the antiLag
 * feature. Returns 0 to forward the client info unchanged. The owned
 * allocations are released by release_device_create once the host call
 * returns. */
int tipsy_vk_latency_prepare_device_create(void *physical_device, const void *create_info,
	void *out_info, uint32_t out_info_size, int *added, int supported)
{
	const uint8_t *info = (const uint8_t *)create_info;
	uint8_t *out = (uint8_t *)out_info;
	TipsyVkAntiLagFeatures *feature;
	char **names;
	uint32_t ext_count;
	uint32_t i;
	const char *const *ext_names;

	(void)physical_device;
	if (added != NULL) {
		*added = 0;
	}
	if (info == NULL || out == NULL || out_info == create_info ||
		out_info_size < TIPSY_VK_DEVICE_CREATE_INFO_SIZE || !tipsy_vk_antilag_armed()) {
		return 0;
	}
	memcpy(out, info, TIPSY_VK_DEVICE_CREATE_INFO_SIZE);
	if (chain_has_stype(*(const void *const *)(out + TIPSY_VK_DEVICE_CREATE_PNEXT),
			TIPSY_VK_ANTI_LAG_FEATURES_STYPE)) {
		const void *chain = *(const void *const *)(out + TIPSY_VK_DEVICE_CREATE_PNEXT);
		const void *record = chain_find_stype(chain, TIPSY_VK_ANTI_LAG_FEATURES_STYPE);
		uint32_t anti_lag = 0;
		if (record != NULL) {
			memcpy(&anti_lag, (const uint8_t *)record + 16u, sizeof(anti_lag));
		}
		if (anti_lag != 0) {
			return 0;
		}
		return antilag_close("VK_AMD_anti_lag was supplied by the client with antiLag disabled");
	}
	ext_count = *(const uint32_t *)(out + TIPSY_VK_DEVICE_CREATE_EXT_COUNT);
	ext_names = *(const char *const **)(out + TIPSY_VK_DEVICE_CREATE_EXT_NAMES);
	if (!supported) {
		return antilag_close("the device does not expose VK_AMD_anti_lag");
	}
	if (names_contains(ext_count, ext_names, TIPSY_VK_ANTI_LAG_EXTENSION)) {
		return antilag_close("the client enabled anti-lag without its feature");
	}
	if (ext_count > 4096u || (ext_count != 0 && ext_names == NULL)) {
		return antilag_close("invalid device extension list");
	}
	/* vkAntiLagUpdateAMD is resolved only after the host vkCreateDevice
	 * returned the VkDevice; no device exists at this point. */
	feature = (TipsyVkAntiLagFeatures *)calloc(1, sizeof(*feature));
	names = (char **)calloc((size_t)ext_count + 1u, sizeof(char *));
	if (feature == NULL || names == NULL) {
		free(feature);
		free(names);
		return antilag_close("anti-lag setup allocation failed");
	}
	for (i = 0; i < ext_count; i++) {
		names[i] = name_dup(ext_names[i]);
		if (names[i] == NULL) {
			break;
		}
	}
	if (i < ext_count || (names[ext_count] = name_dup(TIPSY_VK_ANTI_LAG_EXTENSION)) == NULL) {
		uint32_t k;
		for (k = 0; k < ext_count; k++) {
			free(names[k]);
		}
		free(names);
		free(feature);
		return antilag_close("anti-lag setup allocation failed");
	}
	feature->sType = TIPSY_VK_ANTI_LAG_FEATURES_STYPE;
	feature->pNext = *(const void *const *)(out + TIPSY_VK_DEVICE_CREATE_PNEXT);
	feature->antiLag = 1;
	*(const void **)(out + TIPSY_VK_DEVICE_CREATE_PNEXT) = feature;
	*(uint32_t *)(out + TIPSY_VK_DEVICE_CREATE_EXT_COUNT) = ext_count + 1u;
	*(const char *const **)(out + TIPSY_VK_DEVICE_CREATE_EXT_NAMES) = (const char *const *)names;
	{
		TipsyVkAntiLagPublish *owned =
			(TipsyVkAntiLagPublish *)calloc(1, sizeof(*owned));
		if (owned != NULL) {
			owned->out_info = out;
			owned->names = names;
			owned->name_count = ext_count + 1u;
			owned->feature = feature;
			latency_lock();
			owned->next = vk_antilag_publish;
			vk_antilag_publish = owned;
			latency_unlock();
		} else {
			for (i = 0; i < ext_count + 1u; i++) free(names[i]);
			free(names);
			free(feature);
			return antilag_close("anti-lag setup allocation failed");
		}
	}
	if (added != NULL) {
		*added = 1;
	}
	return 1;
}

void tipsy_vk_latency_release_device_create(void *out_info)
{
	TipsyVkAntiLagPublish *owned;
	TipsyVkAntiLagPublish **link;
	uint32_t i;
	latency_lock();
	link = &vk_antilag_publish;
	while (*link != NULL && (*link)->out_info != out_info) link = &(*link)->next;
	owned = *link;
	if (owned == NULL) {
		latency_unlock();
		return;
	}
	*link = owned->next;
	latency_unlock();
	for (i = 0; i < owned->name_count; i++) {
		free(owned->names[i]);
	}
	free(owned->names);
	free(owned->feature);
	free(owned);
}

/* One record per live device: the anti-lag update entry point it exposes. */
int tipsy_vk_latency_open_session(void *device, void *anti_lag_update,
	const char *reason)
{
	uint32_t i;
	int empty = -1;
	if (device == NULL) {
		return 0;
	}
	if (anti_lag_update == NULL) {
		return antilag_close(reason != NULL ? reason : "the device has no anti-lag entry point");
	}
	latency_lock();
	if (!tipsy_vk_antilag_armed()) {
		latency_unlock();
		return 0;
	}
	for (i = 0; i < TIPSY_VK_LATENCY_MAX; i++) {
		if (atomic_load_explicit(&vk_latency_devices[i].device, memory_order_relaxed) == device) {
			empty = (int)i;
			break;
		}
		if (empty < 0 && atomic_load_explicit(&vk_latency_devices[i].device, memory_order_relaxed) == NULL) {
			empty = (int)i;
		}
	}
	if (empty < 0) {
		/* Not a close: the device is real, only anti-lag goes without. */
		latency_unlock();
		return antilag_close("no free latency device slot");
	}
	vk_latency_devices[empty].update = (tipsy_vkAntiLagUpdate_fn)anti_lag_update;
	atomic_store_explicit(&vk_latency_devices[empty].device, device, memory_order_release);
	latency_unlock();
	return 1;
}

void tipsy_vk_latency_queue_observed(void *device, void *queue)
{
	int empty = -1;
	uint32_t idx;
	if (device == NULL || queue == NULL) {
		return;
	}
	/* The shared mutex reserves each slot until both fields are published;
	 * registration and destruction cannot select or clear the same slot. */
	latency_lock();
	if (!tipsy_vk_antilag_armed()) {
		latency_unlock();
		return;
	}
	for (idx = 0; idx < TIPSY_VK_LATENCY_MAX; idx++) {
		if (atomic_load_explicit(&vk_latency_queues[idx], memory_order_relaxed) == queue) {
			atomic_store_explicit(&vk_latency_queue_devices[idx], device, memory_order_relaxed);
			latency_unlock();
			return;
		}
		if (empty < 0 && atomic_load_explicit(&vk_latency_queues[idx], memory_order_relaxed) == NULL) {
			empty = (int)idx;
		}
	}
	if (empty < 0) {
		latency_unlock();
		return;
	}
	atomic_store_explicit(&vk_latency_queue_devices[empty], device, memory_order_relaxed);
	atomic_store_explicit(&vk_latency_queues[empty], queue, memory_order_release);
	latency_unlock();
}

/* Device that owns queue, or NULL when GetDeviceQueue never saw it. */
static void *latency_queue_device(void *queue)
{
	uint32_t i;
	void *device = NULL;
	latency_lock();
	for (i = 0; i < TIPSY_VK_LATENCY_MAX; i++) {
		if (atomic_load_explicit(&vk_latency_queues[i], memory_order_relaxed) == queue) {
			device = atomic_load_explicit(&vk_latency_queue_devices[i], memory_order_relaxed);
			break;
		}
	}
	latency_unlock();
	return device;
}

/* Test seam: exercises the same serialized lookup used by present. */
void *tipsy_test_vk_latency_queue_device(void *queue)
{
	return latency_queue_device(queue);
}

void tipsy_vk_latency_device_destroyed(void *device)
{
	uint32_t i;
	latency_lock();
	for (i = 0; i < TIPSY_VK_LATENCY_MAX; i++) {
		if (device == NULL || atomic_load_explicit(&vk_latency_devices[i].device, memory_order_relaxed) == device) {
			atomic_store_explicit(&vk_latency_devices[i].device, NULL, memory_order_release);
			vk_latency_devices[i].update = NULL;
		}
		if (device == NULL ||
			atomic_load_explicit(&vk_latency_queue_devices[i], memory_order_relaxed) == device) {
			atomic_store_explicit(&vk_latency_queues[i], NULL, memory_order_release);
			atomic_store_explicit(&vk_latency_queue_devices[i], NULL, memory_order_release);
		}
	}
	latency_unlock();
}

/* Single present-hook entry used by vulkan.c. The caller checks the gate, so
 * an anti-lag-disabled present never reaches this function. */
void tipsy_vk_latency_present(void *queue)
{
	void *device;
	if (!tipsy_vk_antilag_armed()) {
		return;
	}
	device = latency_queue_device(queue);
	if (device != NULL) {
		(void)tipsy_vk_antilag_update(device);
	}
}

int tipsy_vk_antilag_update(void *device)
{
	tipsy_vkAntiLagUpdate_fn update = NULL;
	TipsyVkAntiLagData data;
	uint32_t i;
	if (!tipsy_vk_antilag_armed() || device == NULL) {
		return 0;
	}
	latency_lock();
	for (i = 0; i < TIPSY_VK_LATENCY_MAX; i++) {
		if (atomic_load_explicit(&vk_latency_devices[i].device, memory_order_relaxed) == device) {
			update = vk_latency_devices[i].update;
			break;
		}
	}
	latency_unlock();
	if (update == NULL) {
		/* Armed but the device did not retain the entry point: turn off. */
		return antilag_close("no anti-lag device record for the present queue");
	}
	memset(&data, 0, sizeof(data));
	data.sType = TIPSY_VK_ANTI_LAG_DATA_STYPE;
	data.mode = TIPSY_VK_ANTI_LAG_MODE_ON;
	data.maxFPS = 0;
	data.pPresentationInfo = NULL;
	update(device, &data);
	atomic_fetch_add_explicit(&vk_antilag_updates, 1, memory_order_relaxed);
	return 1;
}

/* Resolves vkAntiLagUpdateAMD through the device resolver, never the loader:
 * an instance fallback would hand back a different, unusable entry. */
int tipsy_vk_latency_resolve_anti_lag(void *device, void **update_out)
{
	void *update;
	if (update_out != NULL) {
		*update_out = NULL;
	}
	if (!tipsy_vk_antilag_armed()) {
		return 0;
	}
	if (device == NULL || update_out == NULL || vk_latency_get_proc == NULL) {
		return antilag_close("vkAntiLagUpdateAMD is unresolved");
	}
	update = vk_latency_get_proc(device, TIPSY_VK_ANTI_LAG_PROC);
	if (update == NULL) {
		/* Soft close, one log: anti-lag stays off for this run. */
		return antilag_close("vkAntiLagUpdateAMD is unresolved");
	}
	*update_out = update;
	return 1;
}

int tipsy_vk_instance_creation_begin(void)
{
	int expected = 0;
	return atomic_compare_exchange_strong_explicit(&vk_instance_creation_closed, &expected, 1,
		memory_order_acq_rel, memory_order_acquire);
}

int tipsy_vk_instance_creation_closed(void)
{
	return atomic_load_explicit(&vk_instance_creation_closed, memory_order_acquire);
}

/* One report about whether Mesa keeps its pipeline cache between runs. The
 * shader-cache env is never changed here. */
void tipsy_vk_latency_log_shader_cache(void)
{
	int expected = 0;
	const char *dir = getenv("MESA_SHADER_CACHE_DIR");
	const char *disable = getenv("MESA_SHADER_CACHE_DISABLE");
	const char *max_size = getenv("MESA_SHADER_CACHE_MAX_SIZE");
	int persistent;
	if (!atomic_compare_exchange_strong_explicit(&vk_shader_cache_logged, &expected, 1,
			memory_order_acq_rel, memory_order_acquire)) {
		return;
	}
	persistent = (disable == NULL || !env_is_true("MESA_SHADER_CACHE_DISABLE")) &&
		(max_size == NULL || strcmp(max_size, "0") != 0);
	GoAndroid_LogVulkanShaderCache(persistent, (char *)(dir != NULL ? dir : ""));
}

/* Classifies one offline probe sample. Go finalises RADV + AMD; this only
 * normalises the driver string and rejects software and non-AMD vendors. */
void tipsy_vk_antilag_classify(const char *driver_name, uint32_t vendor_id,
	int anti_lag_listed, int software, char *out_driver, size_t out_size, int *out_accept)
{
	size_t i;
	if (out_driver != NULL && out_size > 0) {
		snprintf(out_driver, out_size, "%s", driver_name != NULL ? driver_name : "");
		for (i = 0; out_driver[i] != '\0'; i++) {
			if (out_driver[i] >= 'A' && out_driver[i] <= 'Z') {
				out_driver[i] = (char)(out_driver[i] - 'A' + 'a');
			}
		}
	}
	if (out_accept != NULL) {
		*out_accept = !software && vendor_id == 0x1002u && anti_lag_listed != 0 &&
			out_driver != NULL && strcmp(out_driver, "radv") == 0;
	}
}

void tipsy_test_vk_latency_reset(void)
{
	latency_lock();
	atomic_store_explicit(&vk_latency_antilag_wanted, 0, memory_order_relaxed);
	atomic_store_explicit(&vk_antilag_armed, 0, memory_order_release);
	atomic_store_explicit(&vk_antilag_closed, 0, memory_order_release);
	atomic_store_explicit(&vk_antilag_updates, 0, memory_order_relaxed);
	atomic_store_explicit(&vk_antilag_soft_closes, 0, memory_order_relaxed);
	atomic_store_explicit(&vk_instance_creation_closed, 0, memory_order_relaxed);
	atomic_store_explicit(&vk_shader_cache_logged, 0, memory_order_relaxed);
	latency_unlock();
	tipsy_vk_latency_device_destroyed(NULL);
	tipsy_test_vk_latency_reset_counters();
}

void tipsy_test_vk_latency_reset_counters(void)
{
	atomic_store_explicit(&vk_latency_test_locks, 0, memory_order_relaxed);
	atomic_store_explicit(&vk_latency_test_logs, 0, memory_order_relaxed);
}

int tipsy_test_vk_latency_lock_count(void)
{
	return atomic_load_explicit(&vk_latency_test_locks, memory_order_relaxed);
}

int tipsy_test_vk_latency_log_count(void)
{
	return atomic_load_explicit(&vk_latency_test_logs, memory_order_relaxed);
}

/* Tests: a counting anti-lag entry that proves the exact data the wrapper
 * sends to the driver. */
static int test_antilag_calls;
static uint32_t test_antilag_mode;
static uint32_t test_antilag_max_fps;
static int test_antilag_presentation_null;

static void test_antilag_update(void *device, const void *data)
{
	const TipsyVkAntiLagData *d = (const TipsyVkAntiLagData *)data;
	(void)device;
	test_antilag_calls++;
	if (d != NULL) {
		test_antilag_mode = d->mode;
		test_antilag_max_fps = d->maxFPS;
		test_antilag_presentation_null = d->pPresentationInfo == NULL &&
			d->sType == TIPSY_VK_ANTI_LAG_DATA_STYPE;
	}
}

static void *test_antilag_proc(void *object, const char *name)
{
	(void)object;
	if (name != NULL && strcmp(name, TIPSY_VK_ANTI_LAG_PROC) == 0) {
		return (void *)(intptr_t)&test_antilag_update;
	}
	return NULL;
}

void tipsy_test_vk_latency_install_test_procs(void)
{
	(void)tipsy_vk_latency_install_get_proc((void *)test_antilag_proc);
	test_antilag_calls = 0;
	test_antilag_mode = 0;
	test_antilag_max_fps = 1;
	test_antilag_presentation_null = 0;
}

int tipsy_test_vk_latency_resolve_anti_lag(void *device, void **update_out)
{
	return tipsy_vk_latency_resolve_anti_lag(device, update_out);
}

void tipsy_test_vk_latency_install_device_resolver(void *fn)
{
	(void)tipsy_vk_latency_install_get_proc(fn);
}

void *tipsy_test_vk_latency_fake_device_proc(void)
{
	return (void *)&test_antilag_update;
}

int tipsy_test_vk_antilag_update_count(void) { return test_antilag_calls; }

int tipsy_test_vk_antilag_last_mode(void) { return (int)test_antilag_mode; }

uint32_t tipsy_test_vk_antilag_last_max_fps(void) { return test_antilag_max_fps; }

int tipsy_test_vk_antilag_last_presentation_null(void) { return test_antilag_presentation_null; }

int tipsy_test_vk_antilag_soft_closes(void)
{
	return atomic_load_explicit(&vk_antilag_soft_closes, memory_order_relaxed);
}

int tipsy_test_vk_antilag_total_updates(void)
{
	return atomic_load_explicit(&vk_antilag_updates, memory_order_relaxed);
}

/* Arms through the real gate and records the test device so updates land. */
int tipsy_test_vk_latency_arm(const char *driver, int proved, void *device)
{
	int armed = tipsy_vk_antilag_arm(driver, proved);
	if (!armed || device == NULL) {
		return armed;
	}
	{
		/* Bypass open_session wait resolution; the test device has no wait. */
		uint32_t i;
		latency_lock();
		for (i = 0; i < TIPSY_VK_LATENCY_MAX; i++) {
			if (atomic_load_explicit(&vk_latency_devices[i].device, memory_order_relaxed) == NULL) {
				vk_latency_devices[i].update = test_antilag_update;
				atomic_store_explicit(&vk_latency_devices[i].device, device, memory_order_release);
				break;
			}
		}
		latency_unlock();
	}
	return armed;
}

int tipsy_test_vk_latency_prepare_device_create(int supports_ext, int client_has_struct,
	int client_enabled, int *added)
{
	uint8_t info[TIPSY_VK_DEVICE_CREATE_INFO_SIZE];
	uint8_t out[TIPSY_VK_DEVICE_CREATE_INFO_SIZE];
	static const char *const without_ext[] = { "VK_KHR_swapchain" };
	static TipsyVkAntiLagFeatures chain;
	int prepared;
	memset(info, 0, sizeof(info));
	memset(out, 0, sizeof(out));
	*(void **)(info + TIPSY_VK_DEVICE_CREATE_PNEXT) = NULL;
	*(uint32_t *)(info + TIPSY_VK_DEVICE_CREATE_EXT_COUNT) = 1u;
	*(const char *const **)(info + TIPSY_VK_DEVICE_CREATE_EXT_NAMES) =
		without_ext;
	if (client_has_struct) {
		memset(&chain, 0, sizeof(chain));
		chain.sType = TIPSY_VK_ANTI_LAG_FEATURES_STYPE;
		chain.antiLag = client_enabled ? 1u : 0u;
		*(void **)(info + TIPSY_VK_DEVICE_CREATE_PNEXT) = &chain;
	}
	prepared = tipsy_vk_latency_prepare_device_create(NULL, info, out, sizeof(out), added, supports_ext);
	if (prepared) {
		tipsy_vk_latency_release_device_create(out);
	}
	return prepared;
}

/* Tests: runs the off-path present entry iterations times without cgo
 * overhead between iterations. With the gate closed it is one load and a
 * return. */
void tipsy_test_vk_latency_present_loop(void *queue, uint32_t iterations)
{
	uint32_t i;
	for (i = 0; i < iterations; i++) {
		tipsy_vk_latency_present(queue);
	}
}

/* Tests: reports how many expected entries disagree with the values this unit
 * actually uses. Names are for the failure message; only values are compared. */
uint32_t tipsy_test_vk_latency_abi(const TipsyVkLatencyAbiEntry *expected,
	uint32_t count)
{
	uint32_t i;
	uint32_t wrong = 0;
	for (i = 0; i < count; i++) {
		uint32_t actual = 0;
		int found = 1;
		const char *name = expected[i].name != NULL ? expected[i].name : "";
		if (strcmp(name, "anti_lag_features") == 0) {
			actual = TIPSY_VK_ANTI_LAG_FEATURES_STYPE;
		} else if (strcmp(name, "anti_lag_data") == 0) {
			actual = TIPSY_VK_ANTI_LAG_DATA_STYPE;
		} else if (strcmp(name, "anti_lag_mode_on") == 0) {
			actual = TIPSY_VK_ANTI_LAG_MODE_ON;
		} else {
			found = 0;
		}
		if (!found || actual != expected[i].value) {
			wrong++;
		}
	}
	return wrong;
}

/* Tests: walks a synthetic chain of n nodes (sType 0xa000+i, pNext forming the
 * list) and reports whether target_s_type is in it. */
#define TIPSY_TEST_CHAIN_NODE 32u
#define TIPSY_TEST_CHAIN_MAX 16u
int tipsy_test_vk_latency_chain_finds(uint32_t nodes, uint32_t target_s_type)
{
	/* VkPhysicalDeviceFeatures2 layout; sType 0xa000+i, pNext to the next. */
	uint8_t chain[TIPSY_TEST_CHAIN_NODE * TIPSY_TEST_CHAIN_MAX];
	uint32_t i;
	memset(chain, 0, sizeof(chain));
	if (nodes > TIPSY_TEST_CHAIN_MAX) {
		nodes = TIPSY_TEST_CHAIN_MAX;
	}
	for (i = 0; i < nodes; i++) {
		uint32_t type = (uint32_t)(0xa000u + i);
		const void *next = i + 1u < nodes ?
			(const void *)&chain[(i + 1u) * TIPSY_TEST_CHAIN_NODE] : NULL;
		memcpy(&chain[i * TIPSY_TEST_CHAIN_NODE], &type, sizeof(type));
		memcpy(&chain[i * TIPSY_TEST_CHAIN_NODE + 8u], &next, sizeof(next));
	}
	return chain_has_stype(nodes > 0u ? (const void *)chain : NULL, target_s_type);
}
