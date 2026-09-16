/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_VULKAN_OUTPUT_H
#define TIPSY_VULKAN_OUTPUT_H

#include <stdint.h>

/* The Vulkan output backend is kept in a separate translation unit so it can
 * use the Khronos headers while vulkan.c retains its deliberately small ABI
 * declarations.  All public parameters are opaque ABI-compatible handles or
 * pointers to standard Vulkan structures. */
typedef struct TipsyVkOutputSwapchainClone {
	void *create_info;
	uint32_t qualified;
} TipsyVkOutputSwapchainClone;

void tipsy_vk_output_set_loader(void *gipa, void *gdpa);
void tipsy_vk_output_bind_x11(uintptr_t display, unsigned long parent,
	void *xcb_connection);
void tipsy_vk_output_unbind_x11(void);
void tipsy_vk_output_instance_created(void *instance, int32_t result);
void tipsy_vk_output_source_surface_created(void *instance, uint64_t surface,
	int32_t result);
void tipsy_vk_output_source_surface_destroying(void *instance, uint64_t surface);
void tipsy_vk_output_instance_destroying(void *instance);

void tipsy_vk_output_device_preparing(void *physical, const void *create_info);
void tipsy_vk_output_device_created(void *physical, void *device, int32_t result);
void tipsy_vk_output_device_destroying(void *device);

int tipsy_vk_output_prepare_swapchain(void *device, const void *create_info,
	TipsyVkOutputSwapchainClone *clone);
void tipsy_vk_output_swapchain_clone_release(TipsyVkOutputSwapchainClone *clone);
void tipsy_vk_output_swapchain_created(void *device, uint64_t guest_swapchain,
	int32_t result, uint32_t qualified);
void tipsy_vk_output_swapchain_images(void *device, uint64_t swapchain,
	uint32_t count, const uint64_t *images, int32_t result);
void tipsy_vk_output_swapchain_destroying(void *device, uint64_t swapchain);

void tipsy_vk_output_queue_observed(void *device, uint32_t family,
	uint32_t index, uint32_t flags, void *queue);
void tipsy_vk_output_acquire_observed(void *device, uint64_t swapchain,
	uint64_t semaphore, uint64_t fence, const uint32_t *image_index,
	int32_t result);
void tipsy_vk_output_submit_observed(void *queue, uint32_t count,
	const void *submits, int32_t result);
void tipsy_vk_output_submit2_observed(void *queue, uint32_t count,
	const void *submits, int32_t result);

/* Returns one when the caller must not forward the original present. This
 * includes a completed private transaction and any failure after guest waits
 * may have been consumed; guest_result is the exact result to return. Zero
 * means the caller must forward the original pointer exactly once. */
int tipsy_vk_output_present(void *queue, const void *present_info,
	void *host_present, int32_t *guest_result);

enum {
	TIPSY_VK_OUTPUT_FIXTURE_ELIGIBLE = 0,
	TIPSY_VK_OUTPUT_FIXTURE_ALL_QUEUES_REQUESTED = 1,
	TIPSY_VK_OUTPUT_FIXTURE_SOURCE_UNSUPPORTED = 2,
	TIPSY_VK_OUTPUT_FIXTURE_OUTPUT_UNSUPPORTED = 3,
	TIPSY_VK_OUTPUT_FIXTURE_PROTECTED_QUEUE = 4,
	TIPSY_VK_OUTPUT_FIXTURE_INTERNALLY_SYNCHRONIZED_QUEUE = 5,
};
uint32_t tipsy_test_vk_output_queue_eligible(uint32_t scenario);

enum {
	TIPSY_VK_OUTPUT_TX_SUCCESS = 0,
	TIPSY_VK_OUTPUT_TX_DIRECT_GATE = 1,
	TIPSY_VK_OUTPUT_TX_FRAME_BUSY = 2,
	TIPSY_VK_OUTPUT_TX_SUBMIT_OOM = 3,
	TIPSY_VK_OUTPUT_TX_SUBMIT_DEVICE_LOST = 4,
	TIPSY_VK_OUTPUT_TX_SUBMIT_AMBIGUOUS = 5,
	TIPSY_VK_OUTPUT_TX_GUEST_PRESENT_OOM = 6,
	TIPSY_VK_OUTPUT_TX_GUEST_PRESENT_DEVICE_LOST = 7,
	TIPSY_VK_OUTPUT_TX_GUEST_PRESENT_REJECTED = 8,
	TIPSY_VK_OUTPUT_TX_OUTPUT_PRESENT_OOM = 9,
	TIPSY_VK_OUTPUT_TX_OUTPUT_PRESENT_DEVICE_LOST = 10,
};
typedef struct TipsyVkOutputTransactionFixture {
	uint32_t order[5];
	uint32_t order_count;
	uint32_t acquire_calls;
	uint32_t composition_submit_calls;
	uint32_t acquire_consume_calls;
	uint32_t guest_present_calls;
	uint32_t output_present_calls;
	uint32_t original_guest_forwarded;
	uint32_t full_copy_count;
	uint32_t draw_count;
	uint32_t upload_count;
	uint32_t guest_wait_consumed_once;
	uint32_t g_wait_consumed_once;
	uint32_t o_wait_consumed_once;
	uint32_t quarantined;
	uint32_t device_terminal;
	int32_t returned_result;
} TipsyVkOutputTransactionFixture;
void tipsy_test_vk_output_transaction_fixture(uint32_t scenario,
	TipsyVkOutputTransactionFixture *out);

#endif /* TIPSY_VULKAN_OUTPUT_H */
