/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_VULKAN_OUTPUT_H
#define TIPSY_VULKAN_OUTPUT_H

#include <stdint.h>

/* Same-queue in-place focused-text compositor. Khronos headers live here;
 * vulkan.c keeps its small ABI. Overlay draws into the guest swapchain image
 * being presented; there is no private child, copy, or second present. */
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

/* Returns one when the caller must not forward the original present.
 * guest_result is the exact result to return. Zero means forward the original
 * pointer exactly once. */
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
	TIPSY_VK_OUTPUT_TX_UNPUBLISHED = 11,
	TIPSY_VK_OUTPUT_TX_UNPUBLISHED_TEARDOWN = 12,
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
	uint32_t overlay_acquire_calls;
	uint32_t present_mutex_locks;
	uint32_t unpublished_followup_mutex_locks;
	uint32_t copy_src_x;
	uint32_t copy_src_y;
	uint32_t copy_dst_x;
	uint32_t copy_dst_y;
	uint32_t copy_width;
	uint32_t copy_height;
	uint32_t output_create_calls;
	uint32_t output_create_width;
	uint32_t output_create_height;
	int32_t child_x;
	int32_t child_y;
	uint32_t child_width;
	uint32_t child_height;
	float push_rect[4];
	uint32_t viewport_width;
	uint32_t viewport_height;
} TipsyVkOutputTransactionFixture;
void tipsy_test_vk_output_transaction_fixture(uint32_t scenario,
	TipsyVkOutputTransactionFixture *out);

typedef struct TipsyVkOutputGeometryFixture {
	TipsyVkOutputTransactionFixture first;
	TipsyVkOutputTransactionFixture second;
	TipsyVkOutputTransactionFixture empty;
} TipsyVkOutputGeometryFixture;
void tipsy_test_vk_output_overlay_geometry_fixture(
	TipsyVkOutputGeometryFixture *out);

#endif /* TIPSY_VULKAN_OUTPUT_H */
