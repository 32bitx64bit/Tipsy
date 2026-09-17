/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Same-queue in-place focused-text compositor.  While a valid Pango lease
 * exists, Tipsy records one premultiplied source-over draw into the guest
 * swapchain image being presented, then presents that same image once.  The
 * guest device request is never enlarged.  There is no private X11 child,
 * extra swapchain, image copy, or second present.  Unpublished frames skip
 * the overlay mutex.  Guest submit wrappers do not take the overlay lock
 * unless the host reports device loss.
 */
#define VK_USE_PLATFORM_XCB_KHR
#define VK_USE_PLATFORM_XLIB_KHR

#include "vulkan_output.h"
#include "../x11/focused_text_foreground.h"

#include <vulkan/vulkan.h>

#ifndef VK_DEVICE_QUEUE_CREATE_INTERNALLY_SYNCHRONIZED_BIT_KHR
#define VK_DEVICE_QUEUE_CREATE_INTERNALLY_SYNCHRONIZED_BIT_KHR 0x00000002u
#endif
#ifndef VK_PIPELINE_CACHE_CREATE_EXTERNALLY_SYNCHRONIZED_BIT_EXT
#define VK_PIPELINE_CACHE_CREATE_EXTERNALLY_SYNCHRONIZED_BIT_EXT 0x00000001u
#endif

#include <pthread.h>
#include <stdatomic.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#pragma weak tipsy_focused_text_frame_acquire
#pragma weak tipsy_focused_text_frame_release
#pragma weak tipsy_focused_text_overlay_live

#define TIPSY_VK_OUTPUT_MAX_IMAGES 16u
#define TIPSY_VK_OUTPUT_MAX_WAITS 16u
#define TIPSY_VK_OUTPUT_MAX_RETIRED 4u

static const uint32_t tipsy_text_vertex_spirv[] =
#include "vulkan_text_vert.inc"
;
static const uint32_t tipsy_text_fragment_spirv[] =
#include "vulkan_text_frag.inc"
;

typedef struct TipsyVkSourceImageState {
	VkImage image;
	VkImageView view;
	VkFramebuffer framebuffer;
	VkCommandBuffer command;
	VkSemaphore completion;
	VkFence fence;
	uint32_t completion_reusable;
	uint32_t in_flight;
} TipsyVkSourceImageState;

typedef struct TipsyVkOutputPair {
	uint32_t active;
	uint32_t ready;
	VkSwapchainKHR guest;
	VkExtent2D extent;
	VkFormat format;
	uint32_t source_count;
	TipsyVkSourceImageState source[TIPSY_VK_OUTPUT_MAX_IMAGES];
	VkQueue queue;

	VkCommandPool command_pool;
	VkRenderPass render_pass;
	VkDescriptorSetLayout descriptor_layout;
	VkPipelineLayout pipeline_layout;
	VkPipeline pipeline;
	VkDescriptorPool descriptor_pool;
	VkDescriptorSet descriptor;
	VkSampler sampler;

	VkBuffer staging;
	VkDeviceMemory staging_memory;
	void *staging_map;
	VkImage foreground;
	VkDeviceMemory foreground_memory;
	VkImageView foreground_view;
	uint32_t foreground_width;
	uint32_t foreground_height;
	uint64_t foreground_generation;
	uint32_t foreground_initialized;
} TipsyVkOutputPair;

typedef struct TipsyVkOutputDispatch {
	PFN_vkGetPhysicalDeviceQueueFamilyProperties get_queue_families;
	PFN_vkGetPhysicalDeviceSurfaceSupportKHR get_surface_support;
	PFN_vkGetPhysicalDeviceSurfaceCapabilitiesKHR get_surface_capabilities;
	PFN_vkGetPhysicalDeviceMemoryProperties get_memory_properties;
	PFN_vkGetPhysicalDeviceFormatProperties get_format_properties;

	PFN_vkCreateSemaphore create_semaphore;
	PFN_vkDestroySemaphore destroy_semaphore;
	PFN_vkCreateFence create_fence;
	PFN_vkDestroyFence destroy_fence;
	PFN_vkWaitForFences wait_fences;
	PFN_vkResetFences reset_fences;
	PFN_vkCreateCommandPool create_command_pool;
	PFN_vkDestroyCommandPool destroy_command_pool;
	PFN_vkAllocateCommandBuffers allocate_command_buffers;
	PFN_vkResetCommandBuffer reset_command_buffer;
	PFN_vkBeginCommandBuffer begin_command_buffer;
	PFN_vkEndCommandBuffer end_command_buffer;
	PFN_vkQueueSubmit queue_submit;
	PFN_vkCreateImageView create_image_view;
	PFN_vkDestroyImageView destroy_image_view;
	PFN_vkCreateRenderPass create_render_pass;
	PFN_vkDestroyRenderPass destroy_render_pass;
	PFN_vkCreateFramebuffer create_framebuffer;
	PFN_vkDestroyFramebuffer destroy_framebuffer;
	PFN_vkCreateShaderModule create_shader_module;
	PFN_vkDestroyShaderModule destroy_shader_module;
	PFN_vkCreateDescriptorSetLayout create_descriptor_set_layout;
	PFN_vkDestroyDescriptorSetLayout destroy_descriptor_set_layout;
	PFN_vkCreatePipelineLayout create_pipeline_layout;
	PFN_vkDestroyPipelineLayout destroy_pipeline_layout;
	PFN_vkCreateGraphicsPipelines create_graphics_pipelines;
	PFN_vkDestroyPipeline destroy_pipeline;
	PFN_vkCreateDescriptorPool create_descriptor_pool;
	PFN_vkDestroyDescriptorPool destroy_descriptor_pool;
	PFN_vkAllocateDescriptorSets allocate_descriptor_sets;
	PFN_vkUpdateDescriptorSets update_descriptor_sets;
	PFN_vkCreateSampler create_sampler;
	PFN_vkDestroySampler destroy_sampler;
	PFN_vkCreateBuffer create_buffer;
	PFN_vkDestroyBuffer destroy_buffer;
	PFN_vkGetBufferMemoryRequirements get_buffer_memory_requirements;
	PFN_vkCreateImage create_image;
	PFN_vkDestroyImage destroy_image;
	PFN_vkGetImageMemoryRequirements get_image_memory_requirements;
	PFN_vkAllocateMemory allocate_memory;
	PFN_vkFreeMemory free_memory;
	PFN_vkBindBufferMemory bind_buffer_memory;
	PFN_vkBindImageMemory bind_image_memory;
	PFN_vkMapMemory map_memory;
	PFN_vkUnmapMemory unmap_memory;
	PFN_vkCmdPipelineBarrier cmd_pipeline_barrier;
	PFN_vkCmdCopyBufferToImage cmd_copy_buffer_to_image;
	PFN_vkCmdBeginRenderPass cmd_begin_render_pass;
	PFN_vkCmdEndRenderPass cmd_end_render_pass;
	PFN_vkCmdBindPipeline cmd_bind_pipeline;
	PFN_vkCmdBindDescriptorSets cmd_bind_descriptor_sets;
	PFN_vkCmdSetViewport cmd_set_viewport;
	PFN_vkCmdSetScissor cmd_set_scissor;
	PFN_vkCmdPushConstants cmd_push_constants;
	PFN_vkCmdDraw cmd_draw;
} TipsyVkOutputDispatch;

typedef struct TipsyVkOutputQueueRecord {
	VkDevice device;
	VkQueue queue;
	uint32_t family;
	uint32_t index;
	VkDeviceQueueCreateFlags flags;
} TipsyVkOutputQueueRecord;

typedef struct TipsyVkOutputState {
	pthread_mutex_t mutex;
	PFN_vkGetInstanceProcAddr gipa;
	PFN_vkGetDeviceProcAddr gdpa;
	VkInstance instance;
	VkSurfaceKHR source_surface;
	VkPhysicalDevice physical;
	VkDevice device;
	uint32_t eligible_family;
	uint32_t eligible_queue_count;
	uint32_t device_candidate;
	uint32_t device_ready;
	uint32_t device_lost;
	TipsyVkOutputQueueRecord queues[32];

	uint32_t pending_swapchain_qualified;
	VkExtent2D pending_extent;
	VkFormat pending_format;

	TipsyVkOutputDispatch vk;
	TipsyVkOutputPair active;
	TipsyVkOutputPair retired[TIPSY_VK_OUTPUT_MAX_RETIRED];
	uint32_t retired_count;
} TipsyVkOutputState;

static TipsyVkOutputState tipsy_output = {
	.mutex = PTHREAD_MUTEX_INITIALIZER,
};

static _Atomic uint32_t output_route_ready;
static _Atomic uint32_t output_foreground_retained;

static TipsyVkOutputTransactionFixture *test_transaction_fixture;
static int test_transaction_overlay_live = 1;
static int test_transaction_frame_x = 2;
static int test_transaction_frame_y = 3;
static int test_transaction_frame_width = 1;
static int test_transaction_frame_height = 1;

static void tipsy_vk_output_note_overlay_retained_locked(void)
{
	atomic_store_explicit(&output_foreground_retained,
		tipsy_output.active.foreground != VK_NULL_HANDLE, memory_order_release);
}

static int tipsy_vk_output_overlay_live(void)
{
	if (test_transaction_fixture != NULL) {
		return test_transaction_overlay_live != 0;
	}
	if (tipsy_focused_text_overlay_live == NULL) {
		return 0;
	}
	return tipsy_focused_text_overlay_live() != 0;
}

static int tipsy_vk_output_foreground_acquire(
	struct tipsy_focused_text_frame *frame)
{
	static const uint8_t test_pixel[4] = {0xff, 0xff, 0xff, 0xff};
	if (test_transaction_fixture != NULL) {
		test_transaction_fixture->overlay_acquire_calls++;
		memset(frame, 0, sizeof(*frame));
		frame->rgba = test_pixel;
		frame->x = test_transaction_frame_x;
		frame->y = test_transaction_frame_y;
		frame->width = test_transaction_frame_width;
		frame->height = test_transaction_frame_height;
		frame->stride = frame->width * 4;
		frame->generation = 1;
		frame->lease = 1;
		return 1;
	}
	if (tipsy_focused_text_frame_acquire == NULL) return 0;
	return tipsy_focused_text_frame_acquire(frame);
}

static void tipsy_vk_output_foreground_release(uintptr_t lease)
{
	if (test_transaction_fixture != NULL) return;
	if (tipsy_focused_text_frame_release != NULL) {
		tipsy_focused_text_frame_release(lease);
	}
}

#define LOAD_INSTANCE(field, name, loaded) do { \
	tipsy_output.vk.field = (PFN_vk##name)tipsy_output.gipa( \
		tipsy_output.instance, "vk" #name); \
	(loaded) &= tipsy_output.vk.field != NULL; \
} while (0)
#define LOAD_DEVICE(field, name, loaded) do { \
	tipsy_output.vk.field = (PFN_vk##name)tipsy_output.gdpa( \
		tipsy_output.device, "vk" #name); \
	(loaded) &= tipsy_output.vk.field != NULL; \
} while (0)

static int tipsy_vk_output_load_instance_dispatch_locked(void)
{
	int loaded = 1;
	if (tipsy_output.gipa == NULL || tipsy_output.instance == VK_NULL_HANDLE) {
		return 0;
	}
	LOAD_INSTANCE(get_queue_families, GetPhysicalDeviceQueueFamilyProperties, loaded);
	LOAD_INSTANCE(get_surface_support, GetPhysicalDeviceSurfaceSupportKHR, loaded);
	LOAD_INSTANCE(get_surface_capabilities, GetPhysicalDeviceSurfaceCapabilitiesKHR,
		loaded);
	LOAD_INSTANCE(get_memory_properties, GetPhysicalDeviceMemoryProperties, loaded);
	LOAD_INSTANCE(get_format_properties, GetPhysicalDeviceFormatProperties, loaded);
	return loaded;
}

static int tipsy_vk_output_load_device_dispatch_locked(void)
{
	int loaded = 1;
	if (tipsy_output.gdpa == NULL || tipsy_output.device == VK_NULL_HANDLE) {
		return 0;
	}
	LOAD_DEVICE(create_semaphore, CreateSemaphore, loaded);
	LOAD_DEVICE(destroy_semaphore, DestroySemaphore, loaded);
	LOAD_DEVICE(create_fence, CreateFence, loaded);
	LOAD_DEVICE(destroy_fence, DestroyFence, loaded);
	LOAD_DEVICE(wait_fences, WaitForFences, loaded);
	LOAD_DEVICE(reset_fences, ResetFences, loaded);
	LOAD_DEVICE(create_command_pool, CreateCommandPool, loaded);
	LOAD_DEVICE(destroy_command_pool, DestroyCommandPool, loaded);
	LOAD_DEVICE(allocate_command_buffers, AllocateCommandBuffers, loaded);
	LOAD_DEVICE(reset_command_buffer, ResetCommandBuffer, loaded);
	LOAD_DEVICE(begin_command_buffer, BeginCommandBuffer, loaded);
	LOAD_DEVICE(end_command_buffer, EndCommandBuffer, loaded);
	LOAD_DEVICE(queue_submit, QueueSubmit, loaded);
	LOAD_DEVICE(create_image_view, CreateImageView, loaded);
	LOAD_DEVICE(destroy_image_view, DestroyImageView, loaded);
	LOAD_DEVICE(create_render_pass, CreateRenderPass, loaded);
	LOAD_DEVICE(destroy_render_pass, DestroyRenderPass, loaded);
	LOAD_DEVICE(create_framebuffer, CreateFramebuffer, loaded);
	LOAD_DEVICE(destroy_framebuffer, DestroyFramebuffer, loaded);
	LOAD_DEVICE(create_shader_module, CreateShaderModule, loaded);
	LOAD_DEVICE(destroy_shader_module, DestroyShaderModule, loaded);
	LOAD_DEVICE(create_descriptor_set_layout, CreateDescriptorSetLayout, loaded);
	LOAD_DEVICE(destroy_descriptor_set_layout, DestroyDescriptorSetLayout, loaded);
	LOAD_DEVICE(create_pipeline_layout, CreatePipelineLayout, loaded);
	LOAD_DEVICE(destroy_pipeline_layout, DestroyPipelineLayout, loaded);
	LOAD_DEVICE(create_graphics_pipelines, CreateGraphicsPipelines, loaded);
	LOAD_DEVICE(destroy_pipeline, DestroyPipeline, loaded);
	LOAD_DEVICE(create_descriptor_pool, CreateDescriptorPool, loaded);
	LOAD_DEVICE(destroy_descriptor_pool, DestroyDescriptorPool, loaded);
	LOAD_DEVICE(allocate_descriptor_sets, AllocateDescriptorSets, loaded);
	LOAD_DEVICE(update_descriptor_sets, UpdateDescriptorSets, loaded);
	LOAD_DEVICE(create_sampler, CreateSampler, loaded);
	LOAD_DEVICE(destroy_sampler, DestroySampler, loaded);
	LOAD_DEVICE(create_buffer, CreateBuffer, loaded);
	LOAD_DEVICE(destroy_buffer, DestroyBuffer, loaded);
	LOAD_DEVICE(get_buffer_memory_requirements, GetBufferMemoryRequirements, loaded);
	LOAD_DEVICE(create_image, CreateImage, loaded);
	LOAD_DEVICE(destroy_image, DestroyImage, loaded);
	LOAD_DEVICE(get_image_memory_requirements, GetImageMemoryRequirements, loaded);
	LOAD_DEVICE(allocate_memory, AllocateMemory, loaded);
	LOAD_DEVICE(free_memory, FreeMemory, loaded);
	LOAD_DEVICE(bind_buffer_memory, BindBufferMemory, loaded);
	LOAD_DEVICE(bind_image_memory, BindImageMemory, loaded);
	LOAD_DEVICE(map_memory, MapMemory, loaded);
	LOAD_DEVICE(unmap_memory, UnmapMemory, loaded);
	LOAD_DEVICE(cmd_pipeline_barrier, CmdPipelineBarrier, loaded);
	LOAD_DEVICE(cmd_copy_buffer_to_image, CmdCopyBufferToImage, loaded);
	LOAD_DEVICE(cmd_begin_render_pass, CmdBeginRenderPass, loaded);
	LOAD_DEVICE(cmd_end_render_pass, CmdEndRenderPass, loaded);
	LOAD_DEVICE(cmd_bind_pipeline, CmdBindPipeline, loaded);
	LOAD_DEVICE(cmd_bind_descriptor_sets, CmdBindDescriptorSets, loaded);
	LOAD_DEVICE(cmd_set_viewport, CmdSetViewport, loaded);
	LOAD_DEVICE(cmd_set_scissor, CmdSetScissor, loaded);
	LOAD_DEVICE(cmd_push_constants, CmdPushConstants, loaded);
	LOAD_DEVICE(cmd_draw, CmdDraw, loaded);
	return loaded;
}

static int tipsy_vk_output_queue_request_valid(const VkDeviceCreateInfo *info,
	uint32_t family, const VkQueueFamilyProperties *properties,
	VkBool32 source_supported, VkBool32 output_supported,
	uint32_t *requested_count)
{
	uint32_t i;
	(void)output_supported;
	if (info == NULL || info->queueCreateInfoCount == 0 ||
		info->pQueueCreateInfos == NULL || properties == NULL ||
		(properties->queueFlags & VK_QUEUE_GRAPHICS_BIT) == 0 ||
		!source_supported) {
		return 0;
	}
	for (i = 0; i < info->queueCreateInfoCount; i++) {
		const VkDeviceQueueCreateInfo *queue = &info->pQueueCreateInfos[i];
		if (queue->queueFamilyIndex != family) {
			continue;
		}
		if (queue->sType != VK_STRUCTURE_TYPE_DEVICE_QUEUE_CREATE_INFO ||
			queue->pNext != NULL || queue->flags != 0 || queue->queueCount == 0 ||
			queue->pQueuePriorities == NULL || queue->queueCount > properties->queueCount) {
			return 0;
		}
		if (requested_count != NULL) *requested_count = queue->queueCount;
		return 1;
	}
	return 0;
}

void tipsy_vk_output_set_loader(void *gipa, void *gdpa)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	tipsy_output.gipa = (PFN_vkGetInstanceProcAddr)gipa;
	tipsy_output.gdpa = (PFN_vkGetDeviceProcAddr)gdpa;
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_bind_x11(uintptr_t display, unsigned long parent,
	void *xcb_connection)
{
	(void)display;
	(void)parent;
	(void)xcb_connection;
}

void tipsy_vk_output_unbind_x11(void)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	atomic_store_explicit(&output_route_ready, 0, memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_instance_created(void *instance, int32_t result)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	if (result == VK_SUCCESS && instance != NULL &&
		tipsy_output.instance == VK_NULL_HANDLE) {
		tipsy_output.instance = (VkInstance)instance;
		if (!tipsy_vk_output_load_instance_dispatch_locked()) {
			tipsy_output.instance = VK_NULL_HANDLE;
			memset(&tipsy_output.vk, 0, sizeof(tipsy_output.vk));
		}
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_source_surface_created(void *instance, uint64_t surface,
	int32_t result)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	if (result == VK_SUCCESS && instance != NULL && surface != 0 &&
		(VkInstance)instance == tipsy_output.instance &&
		tipsy_output.source_surface == VK_NULL_HANDLE) {
		tipsy_output.source_surface = (VkSurfaceKHR)surface;
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_device_preparing(void *physical_ptr,
	const void *create_info_ptr)
{
	const VkDeviceCreateInfo *info = (const VkDeviceCreateInfo *)create_info_ptr;
	VkPhysicalDevice physical = (VkPhysicalDevice)physical_ptr;
	VkQueueFamilyProperties *properties = NULL;
	uint32_t property_count = 0;
	uint32_t chosen_family = UINT32_MAX;
	uint32_t chosen_queue_count = 0;
	uint32_t i;

	pthread_mutex_lock(&tipsy_output.mutex);
	tipsy_output.device_candidate = 0;
	tipsy_output.eligible_family = UINT32_MAX;
	tipsy_output.eligible_queue_count = 0;
	if (physical == VK_NULL_HANDLE || info == NULL ||
		tipsy_output.vk.get_queue_families == NULL ||
		info->queueCreateInfoCount == 0 || info->pQueueCreateInfos == NULL) {
		goto reject;
	}
	tipsy_output.vk.get_queue_families(physical, &property_count, NULL);
	if (property_count == 0 || property_count > 256u) {
		goto reject;
	}
	properties = calloc(property_count, sizeof(*properties));
	if (properties == NULL) {
		goto reject;
	}
	tipsy_output.vk.get_queue_families(physical, &property_count, properties);
	for (i = 0; i < property_count; i++) {
		if (tipsy_vk_output_queue_request_valid(info, i, &properties[i],
			VK_TRUE, VK_TRUE, &chosen_queue_count)) {
			chosen_family = i;
			break;
		}
	}
	if (chosen_family == UINT32_MAX) {
		goto reject;
	}
	tipsy_output.physical = physical;
	tipsy_output.eligible_family = chosen_family;
	tipsy_output.eligible_queue_count = chosen_queue_count;
	tipsy_output.device_candidate = 1;
	free(properties);
	pthread_mutex_unlock(&tipsy_output.mutex);
	return;

reject:
	free(properties);
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_device_created(void *physical, void *device, int32_t result)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	if (result != VK_SUCCESS || device == NULL || !tipsy_output.device_candidate ||
		(VkPhysicalDevice)physical != tipsy_output.physical) {
		tipsy_output.device_candidate = 0;
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	tipsy_output.device = (VkDevice)device;
	if (!tipsy_vk_output_load_device_dispatch_locked()) {
		tipsy_output.device = VK_NULL_HANDLE;
		tipsy_output.device_candidate = 0;
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	tipsy_output.device_ready = 1;
	pthread_mutex_unlock(&tipsy_output.mutex);
}

static TipsyVkOutputQueueRecord *tipsy_vk_output_queue_locked(VkQueue queue)
{
	uint32_t i;
	for (i = 0; i < 32u; i++) {
		if (tipsy_output.queues[i].queue == queue && queue != VK_NULL_HANDLE) {
			return &tipsy_output.queues[i];
		}
	}
	return NULL;
}

int tipsy_vk_output_prepare_swapchain(void *device_ptr, const void *create_info_ptr,
	TipsyVkOutputSwapchainClone *clone)
{
	const VkSwapchainCreateInfoKHR *info =
		(const VkSwapchainCreateInfoKHR *)create_info_ptr;
	VkSwapchainCreateInfoKHR *clone_info = NULL;
	VkSurfaceCapabilitiesKHR source_caps;
	VkBool32 present_supported = VK_FALSE;

	if (clone == NULL) {
		return 0;
	}
	memset(clone, 0, sizeof(*clone));
	pthread_mutex_lock(&tipsy_output.mutex);
	tipsy_output.pending_swapchain_qualified = 0;
	if (!tipsy_output.device_ready || tipsy_output.device_lost ||
		(VkDevice)device_ptr != tipsy_output.device || info == NULL ||
		info->sType != VK_STRUCTURE_TYPE_SWAPCHAIN_CREATE_INFO_KHR ||
		info->surface != tipsy_output.source_surface ||
		info->imageArrayLayers != 1 ||
		info->imageSharingMode != VK_SHARING_MODE_EXCLUSIVE ||
		info->imageExtent.width == 0 || info->imageExtent.height == 0 ||
		(info->oldSwapchain == VK_NULL_HANDLE && tipsy_output.active.active) ||
		(info->oldSwapchain != VK_NULL_HANDLE &&
			(!tipsy_output.active.active || tipsy_output.active.guest != info->oldSwapchain ||
			 tipsy_output.retired_count >= TIPSY_VK_OUTPUT_MAX_RETIRED))) {
		goto reject;
	}
	if (tipsy_output.vk.get_surface_support == NULL ||
		tipsy_output.vk.get_surface_capabilities == NULL) {
		goto reject;
	}
	if (tipsy_output.vk.get_surface_support(tipsy_output.physical,
		tipsy_output.eligible_family, tipsy_output.source_surface,
		&present_supported) != VK_SUCCESS || !present_supported) {
		goto reject;
	}
	memset(&source_caps, 0, sizeof(source_caps));
	if (tipsy_output.vk.get_surface_capabilities(tipsy_output.physical,
		tipsy_output.source_surface, &source_caps) != VK_SUCCESS ||
		(source_caps.supportedUsageFlags & VK_IMAGE_USAGE_COLOR_ATTACHMENT_BIT) == 0) {
		goto reject;
	}
	if ((info->imageUsage & VK_IMAGE_USAGE_COLOR_ATTACHMENT_BIT) == 0) {
		clone_info = calloc(1, sizeof(*clone_info));
		if (clone_info == NULL) {
			goto reject;
		}
		*clone_info = *info;
		clone_info->imageUsage |= VK_IMAGE_USAGE_COLOR_ATTACHMENT_BIT;
		clone->create_info = clone_info;
	}
	tipsy_output.pending_extent = info->imageExtent;
	tipsy_output.pending_format = info->imageFormat;
	tipsy_output.pending_swapchain_qualified = 1;
	atomic_store_explicit(&output_route_ready, 0, memory_order_release);
	clone->qualified = 1;
	pthread_mutex_unlock(&tipsy_output.mutex);
	return 1;

reject:
	free(clone_info);
	pthread_mutex_unlock(&tipsy_output.mutex);
	return 0;
}

void tipsy_vk_output_swapchain_clone_release(TipsyVkOutputSwapchainClone *clone)
{
	if (clone == NULL) {
		return;
	}
	free(clone->create_info);
	memset(clone, 0, sizeof(*clone));
}

static uint32_t tipsy_vk_output_memory_type_locked(uint32_t bits,
	VkMemoryPropertyFlags required, VkMemoryPropertyFlags preferred)
{
	VkPhysicalDeviceMemoryProperties memory;
	uint32_t fallback = UINT32_MAX;
	uint32_t i;
	tipsy_output.vk.get_memory_properties(tipsy_output.physical, &memory);
	for (i = 0; i < memory.memoryTypeCount; i++) {
		VkMemoryPropertyFlags flags;
		if ((bits & (1u << i)) == 0) {
			continue;
		}
		flags = memory.memoryTypes[i].propertyFlags;
		if ((flags & required) != required) {
			continue;
		}
		if ((flags & preferred) == preferred) {
			return i;
		}
		if (fallback == UINT32_MAX) {
			fallback = i;
		}
	}
	return fallback;
}

static void tipsy_vk_output_destroy_foreground_locked(TipsyVkOutputPair *pair)
{
	if (pair == NULL) return;
	if (pair->staging_map != NULL && pair->staging_memory != VK_NULL_HANDLE &&
		tipsy_output.vk.unmap_memory != NULL) {
		tipsy_output.vk.unmap_memory(tipsy_output.device, pair->staging_memory);
	}
	if (pair->foreground_view != VK_NULL_HANDLE &&
		tipsy_output.vk.destroy_image_view != NULL) {
		tipsy_output.vk.destroy_image_view(tipsy_output.device,
			pair->foreground_view, NULL);
	}
	if (pair->foreground != VK_NULL_HANDLE &&
		tipsy_output.vk.destroy_image != NULL) {
		tipsy_output.vk.destroy_image(tipsy_output.device, pair->foreground, NULL);
	}
	if (pair->foreground_memory != VK_NULL_HANDLE &&
		tipsy_output.vk.free_memory != NULL) {
		tipsy_output.vk.free_memory(tipsy_output.device, pair->foreground_memory, NULL);
	}
	if (pair->staging != VK_NULL_HANDLE &&
		tipsy_output.vk.destroy_buffer != NULL) {
		tipsy_output.vk.destroy_buffer(tipsy_output.device, pair->staging, NULL);
	}
	if (pair->staging_memory != VK_NULL_HANDLE &&
		tipsy_output.vk.free_memory != NULL) {
		tipsy_output.vk.free_memory(tipsy_output.device, pair->staging_memory, NULL);
	}
	pair->staging = VK_NULL_HANDLE;
	pair->staging_memory = VK_NULL_HANDLE;
	pair->staging_map = NULL;
	pair->foreground = VK_NULL_HANDLE;
	pair->foreground_memory = VK_NULL_HANDLE;
	pair->foreground_view = VK_NULL_HANDLE;
	pair->foreground_width = 0;
	pair->foreground_height = 0;
	pair->foreground_generation = 0;
	pair->foreground_initialized = 0;
	tipsy_vk_output_note_overlay_retained_locked();
}

static void tipsy_vk_output_destroy_pair_locked(TipsyVkOutputPair *pair,
	int destroy_vulkan)
{
	uint32_t i;
	if (pair == NULL || !pair->active) {
		return;
	}
	if (destroy_vulkan) {
		tipsy_vk_output_destroy_foreground_locked(pair);
		if (pair->sampler != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_sampler != NULL) {
			tipsy_output.vk.destroy_sampler(tipsy_output.device, pair->sampler, NULL);
		}
		if (pair->descriptor_pool != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_descriptor_pool != NULL) {
			tipsy_output.vk.destroy_descriptor_pool(tipsy_output.device,
				pair->descriptor_pool, NULL);
		}
		if (pair->pipeline != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_pipeline != NULL) {
			tipsy_output.vk.destroy_pipeline(tipsy_output.device, pair->pipeline, NULL);
		}
		if (pair->pipeline_layout != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_pipeline_layout != NULL) {
			tipsy_output.vk.destroy_pipeline_layout(tipsy_output.device,
				pair->pipeline_layout, NULL);
		}
		if (pair->descriptor_layout != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_descriptor_set_layout != NULL) {
			tipsy_output.vk.destroy_descriptor_set_layout(tipsy_output.device,
				pair->descriptor_layout, NULL);
		}
		for (i = 0; i < pair->source_count; i++) {
			if (pair->source[i].framebuffer != VK_NULL_HANDLE &&
				tipsy_output.vk.destroy_framebuffer != NULL) {
				tipsy_output.vk.destroy_framebuffer(tipsy_output.device,
					pair->source[i].framebuffer, NULL);
			}
			if (pair->source[i].view != VK_NULL_HANDLE &&
				tipsy_output.vk.destroy_image_view != NULL) {
				tipsy_output.vk.destroy_image_view(tipsy_output.device,
					pair->source[i].view, NULL);
			}
			if (pair->source[i].completion != VK_NULL_HANDLE &&
				tipsy_output.vk.destroy_semaphore != NULL) {
				tipsy_output.vk.destroy_semaphore(tipsy_output.device,
					pair->source[i].completion, NULL);
			}
			if (pair->source[i].fence != VK_NULL_HANDLE &&
				tipsy_output.vk.destroy_fence != NULL) {
				tipsy_output.vk.destroy_fence(tipsy_output.device,
					pair->source[i].fence, NULL);
			}
		}
		if (pair->render_pass != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_render_pass != NULL) {
			tipsy_output.vk.destroy_render_pass(tipsy_output.device,
				pair->render_pass, NULL);
		}
		if (pair->command_pool != VK_NULL_HANDLE &&
			tipsy_output.vk.destroy_command_pool != NULL) {
			tipsy_output.vk.destroy_command_pool(tipsy_output.device,
				pair->command_pool, NULL);
		}
	}
	memset(pair, 0, sizeof(*pair));
	tipsy_vk_output_note_overlay_retained_locked();
}

static VkResult tipsy_vk_output_create_pipeline_locked(TipsyVkOutputPair *pair)
{
	VkAttachmentDescription attachment;
	VkAttachmentReference color_ref;
	VkSubpassDescription subpass;
	VkRenderPassCreateInfo render_info;
	VkDescriptorSetLayoutBinding binding;
	VkDescriptorSetLayoutCreateInfo descriptor_layout_info;
	VkPushConstantRange push_range;
	VkPipelineLayoutCreateInfo pipeline_layout_info;
	VkShaderModuleCreateInfo shader_info;
	VkShaderModule vertex = VK_NULL_HANDLE;
	VkShaderModule fragment = VK_NULL_HANDLE;
	VkPipelineShaderStageCreateInfo stages[2];
	VkPipelineVertexInputStateCreateInfo vertex_input;
	VkPipelineInputAssemblyStateCreateInfo assembly;
	VkPipelineViewportStateCreateInfo viewport_state;
	VkPipelineRasterizationStateCreateInfo raster;
	VkPipelineMultisampleStateCreateInfo multisample;
	VkPipelineColorBlendAttachmentState blend_attachment;
	VkPipelineColorBlendStateCreateInfo blend;
	VkDynamicState dynamic_states[2] = {VK_DYNAMIC_STATE_VIEWPORT,
		VK_DYNAMIC_STATE_SCISSOR};
	VkPipelineDynamicStateCreateInfo dynamic;
	VkGraphicsPipelineCreateInfo pipeline_info;
	VkDescriptorPoolSize pool_size;
	VkDescriptorPoolCreateInfo pool_info;
	VkDescriptorSetAllocateInfo allocate_info;
	VkSamplerCreateInfo sampler_info;
	VkResult result;

	memset(&attachment, 0, sizeof(attachment));
	attachment.format = pair->format;
	attachment.samples = VK_SAMPLE_COUNT_1_BIT;
	attachment.loadOp = VK_ATTACHMENT_LOAD_OP_LOAD;
	attachment.storeOp = VK_ATTACHMENT_STORE_OP_STORE;
	attachment.stencilLoadOp = VK_ATTACHMENT_LOAD_OP_DONT_CARE;
	attachment.stencilStoreOp = VK_ATTACHMENT_STORE_OP_DONT_CARE;
	attachment.initialLayout = VK_IMAGE_LAYOUT_COLOR_ATTACHMENT_OPTIMAL;
	attachment.finalLayout = VK_IMAGE_LAYOUT_COLOR_ATTACHMENT_OPTIMAL;
	color_ref.attachment = 0;
	color_ref.layout = VK_IMAGE_LAYOUT_COLOR_ATTACHMENT_OPTIMAL;
	memset(&subpass, 0, sizeof(subpass));
	subpass.pipelineBindPoint = VK_PIPELINE_BIND_POINT_GRAPHICS;
	subpass.colorAttachmentCount = 1;
	subpass.pColorAttachments = &color_ref;
	memset(&render_info, 0, sizeof(render_info));
	render_info.sType = VK_STRUCTURE_TYPE_RENDER_PASS_CREATE_INFO;
	render_info.attachmentCount = 1;
	render_info.pAttachments = &attachment;
	render_info.subpassCount = 1;
	render_info.pSubpasses = &subpass;
	result = tipsy_output.vk.create_render_pass(tipsy_output.device, &render_info,
		NULL, &pair->render_pass);
	if (result != VK_SUCCESS) return result;

	memset(&binding, 0, sizeof(binding));
	binding.binding = 0;
	binding.descriptorType = VK_DESCRIPTOR_TYPE_COMBINED_IMAGE_SAMPLER;
	binding.descriptorCount = 1;
	binding.stageFlags = VK_SHADER_STAGE_FRAGMENT_BIT;
	memset(&descriptor_layout_info, 0, sizeof(descriptor_layout_info));
	descriptor_layout_info.sType = VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO;
	descriptor_layout_info.bindingCount = 1;
	descriptor_layout_info.pBindings = &binding;
	result = tipsy_output.vk.create_descriptor_set_layout(tipsy_output.device,
		&descriptor_layout_info, NULL, &pair->descriptor_layout);
	if (result != VK_SUCCESS) return result;

	memset(&push_range, 0, sizeof(push_range));
	push_range.stageFlags = VK_SHADER_STAGE_VERTEX_BIT;
	push_range.size = sizeof(float) * 4u;
	memset(&pipeline_layout_info, 0, sizeof(pipeline_layout_info));
	pipeline_layout_info.sType = VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO;
	pipeline_layout_info.setLayoutCount = 1;
	pipeline_layout_info.pSetLayouts = &pair->descriptor_layout;
	pipeline_layout_info.pushConstantRangeCount = 1;
	pipeline_layout_info.pPushConstantRanges = &push_range;
	result = tipsy_output.vk.create_pipeline_layout(tipsy_output.device,
		&pipeline_layout_info, NULL, &pair->pipeline_layout);
	if (result != VK_SUCCESS) return result;

	memset(&shader_info, 0, sizeof(shader_info));
	shader_info.sType = VK_STRUCTURE_TYPE_SHADER_MODULE_CREATE_INFO;
	shader_info.codeSize = sizeof(tipsy_text_vertex_spirv);
	shader_info.pCode = tipsy_text_vertex_spirv;
	result = tipsy_output.vk.create_shader_module(tipsy_output.device,
		&shader_info, NULL, &vertex);
	if (result != VK_SUCCESS) return result;
	shader_info.codeSize = sizeof(tipsy_text_fragment_spirv);
	shader_info.pCode = tipsy_text_fragment_spirv;
	result = tipsy_output.vk.create_shader_module(tipsy_output.device,
		&shader_info, NULL, &fragment);
	if (result != VK_SUCCESS) {
		tipsy_output.vk.destroy_shader_module(tipsy_output.device, vertex, NULL);
		return result;
	}
	memset(stages, 0, sizeof(stages));
	stages[0].sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
	stages[0].stage = VK_SHADER_STAGE_VERTEX_BIT;
	stages[0].module = vertex;
	stages[0].pName = "main";
	stages[1].sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
	stages[1].stage = VK_SHADER_STAGE_FRAGMENT_BIT;
	stages[1].module = fragment;
	stages[1].pName = "main";
	memset(&vertex_input, 0, sizeof(vertex_input));
	vertex_input.sType = VK_STRUCTURE_TYPE_PIPELINE_VERTEX_INPUT_STATE_CREATE_INFO;
	memset(&assembly, 0, sizeof(assembly));
	assembly.sType = VK_STRUCTURE_TYPE_PIPELINE_INPUT_ASSEMBLY_STATE_CREATE_INFO;
	assembly.topology = VK_PRIMITIVE_TOPOLOGY_TRIANGLE_STRIP;
	memset(&viewport_state, 0, sizeof(viewport_state));
	viewport_state.sType = VK_STRUCTURE_TYPE_PIPELINE_VIEWPORT_STATE_CREATE_INFO;
	viewport_state.viewportCount = 1;
	viewport_state.scissorCount = 1;
	memset(&raster, 0, sizeof(raster));
	raster.sType = VK_STRUCTURE_TYPE_PIPELINE_RASTERIZATION_STATE_CREATE_INFO;
	raster.polygonMode = VK_POLYGON_MODE_FILL;
	raster.cullMode = VK_CULL_MODE_NONE;
	raster.frontFace = VK_FRONT_FACE_COUNTER_CLOCKWISE;
	raster.lineWidth = 1.0f;
	memset(&multisample, 0, sizeof(multisample));
	multisample.sType = VK_STRUCTURE_TYPE_PIPELINE_MULTISAMPLE_STATE_CREATE_INFO;
	multisample.rasterizationSamples = VK_SAMPLE_COUNT_1_BIT;
	memset(&blend_attachment, 0, sizeof(blend_attachment));
	blend_attachment.blendEnable = VK_TRUE;
	blend_attachment.srcColorBlendFactor = VK_BLEND_FACTOR_ONE;
	blend_attachment.dstColorBlendFactor = VK_BLEND_FACTOR_ONE_MINUS_SRC_ALPHA;
	blend_attachment.colorBlendOp = VK_BLEND_OP_ADD;
	blend_attachment.srcAlphaBlendFactor = VK_BLEND_FACTOR_ONE;
	blend_attachment.dstAlphaBlendFactor = VK_BLEND_FACTOR_ONE_MINUS_SRC_ALPHA;
	blend_attachment.alphaBlendOp = VK_BLEND_OP_ADD;
	blend_attachment.colorWriteMask = VK_COLOR_COMPONENT_R_BIT |
		VK_COLOR_COMPONENT_G_BIT | VK_COLOR_COMPONENT_B_BIT |
		VK_COLOR_COMPONENT_A_BIT;
	memset(&blend, 0, sizeof(blend));
	blend.sType = VK_STRUCTURE_TYPE_PIPELINE_COLOR_BLEND_STATE_CREATE_INFO;
	blend.attachmentCount = 1;
	blend.pAttachments = &blend_attachment;
	memset(&dynamic, 0, sizeof(dynamic));
	dynamic.sType = VK_STRUCTURE_TYPE_PIPELINE_DYNAMIC_STATE_CREATE_INFO;
	dynamic.dynamicStateCount = 2;
	dynamic.pDynamicStates = dynamic_states;
	memset(&pipeline_info, 0, sizeof(pipeline_info));
	pipeline_info.sType = VK_STRUCTURE_TYPE_GRAPHICS_PIPELINE_CREATE_INFO;
	pipeline_info.stageCount = 2;
	pipeline_info.pStages = stages;
	pipeline_info.pVertexInputState = &vertex_input;
	pipeline_info.pInputAssemblyState = &assembly;
	pipeline_info.pViewportState = &viewport_state;
	pipeline_info.pRasterizationState = &raster;
	pipeline_info.pMultisampleState = &multisample;
	pipeline_info.pColorBlendState = &blend;
	pipeline_info.pDynamicState = &dynamic;
	pipeline_info.layout = pair->pipeline_layout;
	pipeline_info.renderPass = pair->render_pass;
	result = tipsy_output.vk.create_graphics_pipelines(tipsy_output.device,
		VK_NULL_HANDLE, 1, &pipeline_info, NULL, &pair->pipeline);
	tipsy_output.vk.destroy_shader_module(tipsy_output.device, fragment, NULL);
	tipsy_output.vk.destroy_shader_module(tipsy_output.device, vertex, NULL);
	if (result != VK_SUCCESS) return result;

	pool_size.type = VK_DESCRIPTOR_TYPE_COMBINED_IMAGE_SAMPLER;
	pool_size.descriptorCount = 1;
	memset(&pool_info, 0, sizeof(pool_info));
	pool_info.sType = VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO;
	pool_info.maxSets = 1;
	pool_info.poolSizeCount = 1;
	pool_info.pPoolSizes = &pool_size;
	result = tipsy_output.vk.create_descriptor_pool(tipsy_output.device,
		&pool_info, NULL, &pair->descriptor_pool);
	if (result != VK_SUCCESS) return result;
	memset(&allocate_info, 0, sizeof(allocate_info));
	allocate_info.sType = VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO;
	allocate_info.descriptorPool = pair->descriptor_pool;
	allocate_info.descriptorSetCount = 1;
	allocate_info.pSetLayouts = &pair->descriptor_layout;
	result = tipsy_output.vk.allocate_descriptor_sets(tipsy_output.device,
		&allocate_info, &pair->descriptor);
	if (result != VK_SUCCESS) return result;
	memset(&sampler_info, 0, sizeof(sampler_info));
	sampler_info.sType = VK_STRUCTURE_TYPE_SAMPLER_CREATE_INFO;
	sampler_info.magFilter = VK_FILTER_LINEAR;
	sampler_info.minFilter = VK_FILTER_LINEAR;
	sampler_info.mipmapMode = VK_SAMPLER_MIPMAP_MODE_NEAREST;
	sampler_info.addressModeU = VK_SAMPLER_ADDRESS_MODE_CLAMP_TO_EDGE;
	sampler_info.addressModeV = VK_SAMPLER_ADDRESS_MODE_CLAMP_TO_EDGE;
	sampler_info.addressModeW = VK_SAMPLER_ADDRESS_MODE_CLAMP_TO_EDGE;
	sampler_info.maxLod = 0.0f;
	return tipsy_output.vk.create_sampler(tipsy_output.device, &sampler_info,
		NULL, &pair->sampler);
}

static VkResult tipsy_vk_output_build_pair_locked(TipsyVkOutputPair *pair,
	VkSwapchainKHR guest)
{
	VkCommandPoolCreateInfo pool_info;
	VkResult result;

	memset(pair, 0, sizeof(*pair));
	pair->active = 1;
	pair->guest = guest;
	pair->extent = tipsy_output.pending_extent;
	pair->format = tipsy_output.pending_format;
	if (pair->extent.width == 0 || pair->extent.height == 0) {
		return VK_ERROR_INITIALIZATION_FAILED;
	}
	memset(&pool_info, 0, sizeof(pool_info));
	pool_info.sType = VK_STRUCTURE_TYPE_COMMAND_POOL_CREATE_INFO;
	pool_info.flags = VK_COMMAND_POOL_CREATE_RESET_COMMAND_BUFFER_BIT;
	pool_info.queueFamilyIndex = tipsy_output.eligible_family;
	result = tipsy_output.vk.create_command_pool(tipsy_output.device, &pool_info,
		NULL, &pair->command_pool);
	if (result != VK_SUCCESS) return result;
	return tipsy_vk_output_create_pipeline_locked(pair);
}

static void tipsy_vk_output_retire_active_locked(void)
{
	if (!tipsy_output.active.active) {
		return;
	}
	if (tipsy_output.retired_count < TIPSY_VK_OUTPUT_MAX_RETIRED) {
		tipsy_output.retired[tipsy_output.retired_count++] = tipsy_output.active;
	} else {
		tipsy_vk_output_destroy_pair_locked(&tipsy_output.active, 1);
	}
	memset(&tipsy_output.active, 0, sizeof(tipsy_output.active));
	tipsy_vk_output_note_overlay_retained_locked();
}

void tipsy_vk_output_swapchain_created(void *device_ptr,
	uint64_t guest_swapchain, int32_t result, uint32_t qualified)
{
	TipsyVkOutputPair pair;
	VkResult build_result;
	pthread_mutex_lock(&tipsy_output.mutex);
	if (result != VK_SUCCESS || guest_swapchain == 0 || !qualified ||
		!tipsy_output.pending_swapchain_qualified ||
		(VkDevice)device_ptr != tipsy_output.device || !tipsy_output.device_ready) {
		tipsy_output.pending_swapchain_qualified = 0;
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	if (tipsy_output.active.active) {
		tipsy_vk_output_retire_active_locked();
	}
	memset(&pair, 0, sizeof(pair));
	build_result = tipsy_vk_output_build_pair_locked(&pair,
		(VkSwapchainKHR)guest_swapchain);
	if (build_result != VK_SUCCESS) {
		tipsy_vk_output_destroy_pair_locked(&pair, 1);
		tipsy_output.pending_swapchain_qualified = 0;
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	tipsy_output.active = pair;
	tipsy_output.pending_swapchain_qualified = 0;
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_swapchain_images(void *device_ptr, uint64_t swapchain,
	uint32_t count, const uint64_t *images, int32_t result)
{
	VkCommandBufferAllocateInfo command_info;
	VkSemaphoreCreateInfo semaphore_info;
	VkFenceCreateInfo fence_info;
	VkImageViewCreateInfo view_info;
	VkFramebufferCreateInfo framebuffer_info;
	VkCommandBuffer commands[TIPSY_VK_OUTPUT_MAX_IMAGES];
	uint32_t i;
	pthread_mutex_lock(&tipsy_output.mutex);
	if (result != VK_SUCCESS || images == NULL || count == 0 ||
		count > TIPSY_VK_OUTPUT_MAX_IMAGES ||
		(VkDevice)device_ptr != tipsy_output.device ||
		!tipsy_output.active.active ||
		tipsy_output.active.guest != (VkSwapchainKHR)swapchain) {
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	if (tipsy_output.active.source_count != 0) {
		if (tipsy_output.active.source_count != count) {
			tipsy_output.active.ready = 0;
			atomic_store_explicit(&output_route_ready, 0, memory_order_release);
		}
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	memset(&command_info, 0, sizeof(command_info));
	command_info.sType = VK_STRUCTURE_TYPE_COMMAND_BUFFER_ALLOCATE_INFO;
	command_info.commandPool = tipsy_output.active.command_pool;
	command_info.level = VK_COMMAND_BUFFER_LEVEL_PRIMARY;
	command_info.commandBufferCount = count;
	memset(commands, 0, sizeof(commands));
	if (tipsy_output.vk.allocate_command_buffers(tipsy_output.device,
		&command_info, commands) != VK_SUCCESS) {
		tipsy_output.active.ready = 0;
		atomic_store_explicit(&output_route_ready, 0, memory_order_release);
		pthread_mutex_unlock(&tipsy_output.mutex);
		return;
	}
	memset(&semaphore_info, 0, sizeof(semaphore_info));
	semaphore_info.sType = VK_STRUCTURE_TYPE_SEMAPHORE_CREATE_INFO;
	memset(&fence_info, 0, sizeof(fence_info));
	fence_info.sType = VK_STRUCTURE_TYPE_FENCE_CREATE_INFO;
	fence_info.flags = VK_FENCE_CREATE_SIGNALED_BIT;
	tipsy_output.active.source_count = count;
	for (i = 0; i < count; i++) {
		tipsy_output.active.source[i].image = (VkImage)images[i];
		tipsy_output.active.source[i].command = commands[i];
		tipsy_output.active.source[i].completion_reusable = 1;
		if (tipsy_output.vk.create_semaphore(tipsy_output.device,
			&semaphore_info, NULL,
			&tipsy_output.active.source[i].completion) != VK_SUCCESS ||
			tipsy_output.vk.create_fence(tipsy_output.device, &fence_info, NULL,
				&tipsy_output.active.source[i].fence) != VK_SUCCESS) {
			tipsy_output.active.ready = 0;
			atomic_store_explicit(&output_route_ready, 0, memory_order_release);
			pthread_mutex_unlock(&tipsy_output.mutex);
			return;
		}
		memset(&view_info, 0, sizeof(view_info));
		view_info.sType = VK_STRUCTURE_TYPE_IMAGE_VIEW_CREATE_INFO;
		view_info.image = tipsy_output.active.source[i].image;
		view_info.viewType = VK_IMAGE_VIEW_TYPE_2D;
		view_info.format = tipsy_output.active.format;
		view_info.subresourceRange.aspectMask = VK_IMAGE_ASPECT_COLOR_BIT;
		view_info.subresourceRange.levelCount = 1;
		view_info.subresourceRange.layerCount = 1;
		if (tipsy_output.vk.create_image_view(tipsy_output.device, &view_info,
			NULL, &tipsy_output.active.source[i].view) != VK_SUCCESS) {
			tipsy_output.active.ready = 0;
			atomic_store_explicit(&output_route_ready, 0, memory_order_release);
			pthread_mutex_unlock(&tipsy_output.mutex);
			return;
		}
		memset(&framebuffer_info, 0, sizeof(framebuffer_info));
		framebuffer_info.sType = VK_STRUCTURE_TYPE_FRAMEBUFFER_CREATE_INFO;
		framebuffer_info.renderPass = tipsy_output.active.render_pass;
		framebuffer_info.attachmentCount = 1;
		framebuffer_info.pAttachments = &tipsy_output.active.source[i].view;
		framebuffer_info.width = tipsy_output.active.extent.width;
		framebuffer_info.height = tipsy_output.active.extent.height;
		framebuffer_info.layers = 1;
		if (tipsy_output.vk.create_framebuffer(tipsy_output.device,
			&framebuffer_info, NULL,
			&tipsy_output.active.source[i].framebuffer) != VK_SUCCESS) {
			tipsy_output.active.ready = 0;
			atomic_store_explicit(&output_route_ready, 0, memory_order_release);
			pthread_mutex_unlock(&tipsy_output.mutex);
			return;
		}
	}
	tipsy_output.active.ready = 1;
	atomic_store_explicit(&output_route_ready, 1, memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_queue_observed(void *device_ptr, uint32_t family,
	uint32_t index, uint32_t flags, void *queue)
{
	uint32_t i;
	TipsyVkOutputQueueRecord *record = NULL;
	if (device_ptr == NULL || queue == NULL) return;
	pthread_mutex_lock(&tipsy_output.mutex);
	record = tipsy_vk_output_queue_locked((VkQueue)queue);
	if (record == NULL) {
		for (i = 0; i < 32u; i++) {
			if (tipsy_output.queues[i].queue == VK_NULL_HANDLE) {
				record = &tipsy_output.queues[i];
				break;
			}
		}
	}
	if (record != NULL) {
		record->device = (VkDevice)device_ptr;
		record->queue = (VkQueue)queue;
		record->family = family;
		record->index = index;
		record->flags = (VkDeviceQueueCreateFlags)flags;
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_acquire_observed(void *device_ptr, uint64_t swapchain,
	uint64_t semaphore, uint64_t fence, const uint32_t *image_index,
	int32_t result)
{
	TipsyVkSourceImageState *source;
	(void)semaphore;
	(void)fence;
	if (result != VK_ERROR_DEVICE_LOST &&
		atomic_load_explicit(&output_route_ready, memory_order_acquire) == 0) {
		return;
	}
	pthread_mutex_lock(&tipsy_output.mutex);
	if ((result == VK_SUCCESS || result == VK_SUBOPTIMAL_KHR) && image_index != NULL &&
		(VkDevice)device_ptr == tipsy_output.device &&
		tipsy_output.active.active && tipsy_output.active.ready &&
		tipsy_output.active.guest == (VkSwapchainKHR)swapchain &&
		*image_index < tipsy_output.active.source_count) {
		source = &tipsy_output.active.source[*image_index];
		source->completion_reusable = 1;
	}
	if (result == VK_ERROR_DEVICE_LOST) {
		tipsy_output.device_lost = 1;
		atomic_store_explicit(&output_route_ready, 0, memory_order_release);
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_submit_observed(void *queue, uint32_t count,
	const void *submits_ptr, int32_t result)
{
	(void)queue;
	(void)count;
	(void)submits_ptr;
	if (result != VK_ERROR_DEVICE_LOST) return;
	pthread_mutex_lock(&tipsy_output.mutex);
	tipsy_output.device_lost = 1;
	atomic_store_explicit(&output_route_ready, 0, memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_submit2_observed(void *queue, uint32_t count,
	const void *submits_ptr, int32_t result)
{
	tipsy_vk_output_submit_observed(queue, count, submits_ptr, result);
}

static VkResult tipsy_vk_output_wait_pair_fences_locked(TipsyVkOutputPair *pair)
{
	uint32_t i;
	VkResult result;
	for (i = 0; i < pair->source_count; i++) {
		if (pair->source[i].fence == VK_NULL_HANDLE) continue;
		result = tipsy_output.vk.wait_fences(tipsy_output.device, 1,
			&pair->source[i].fence, VK_TRUE, UINT64_MAX);
		if (result != VK_SUCCESS) return result;
		pair->source[i].in_flight = 0;
	}
	return VK_SUCCESS;
}

static VkResult tipsy_vk_output_create_foreground_locked(TipsyVkOutputPair *pair,
	const struct tipsy_focused_text_frame *frame)
{
	VkDeviceSize bytes = (VkDeviceSize)frame->width * (VkDeviceSize)frame->height * 4u;
	VkBufferCreateInfo buffer_info;
	VkMemoryRequirements buffer_requirements;
	VkMemoryAllocateInfo allocation;
	VkImageCreateInfo image_info;
	VkMemoryRequirements image_requirements;
	VkImageViewCreateInfo view_info;
	VkDescriptorImageInfo descriptor_image;
	VkWriteDescriptorSet write;
	VkFormatProperties format_properties;
	uint32_t memory_type;
	VkResult result;

	tipsy_output.vk.get_format_properties(tipsy_output.physical,
		VK_FORMAT_R8G8B8A8_UNORM, &format_properties);
	if ((format_properties.optimalTilingFeatures &
		(VK_FORMAT_FEATURE_SAMPLED_IMAGE_BIT |
		 VK_FORMAT_FEATURE_TRANSFER_DST_BIT)) !=
		(VK_FORMAT_FEATURE_SAMPLED_IMAGE_BIT |
		 VK_FORMAT_FEATURE_TRANSFER_DST_BIT)) {
		return VK_ERROR_FORMAT_NOT_SUPPORTED;
	}
	memset(&buffer_info, 0, sizeof(buffer_info));
	buffer_info.sType = VK_STRUCTURE_TYPE_BUFFER_CREATE_INFO;
	buffer_info.size = bytes;
	buffer_info.usage = VK_BUFFER_USAGE_TRANSFER_SRC_BIT;
	buffer_info.sharingMode = VK_SHARING_MODE_EXCLUSIVE;
	result = tipsy_output.vk.create_buffer(tipsy_output.device, &buffer_info,
		NULL, &pair->staging);
	if (result != VK_SUCCESS) return result;
	tipsy_output.vk.get_buffer_memory_requirements(tipsy_output.device,
		pair->staging, &buffer_requirements);
	memory_type = tipsy_vk_output_memory_type_locked(buffer_requirements.memoryTypeBits,
		VK_MEMORY_PROPERTY_HOST_VISIBLE_BIT | VK_MEMORY_PROPERTY_HOST_COHERENT_BIT, 0);
	if (memory_type == UINT32_MAX) return VK_ERROR_FEATURE_NOT_PRESENT;
	memset(&allocation, 0, sizeof(allocation));
	allocation.sType = VK_STRUCTURE_TYPE_MEMORY_ALLOCATE_INFO;
	allocation.allocationSize = buffer_requirements.size;
	allocation.memoryTypeIndex = memory_type;
	result = tipsy_output.vk.allocate_memory(tipsy_output.device, &allocation,
		NULL, &pair->staging_memory);
	if (result != VK_SUCCESS) return result;
	result = tipsy_output.vk.bind_buffer_memory(tipsy_output.device, pair->staging,
		pair->staging_memory, 0);
	if (result != VK_SUCCESS) return result;
	result = tipsy_output.vk.map_memory(tipsy_output.device, pair->staging_memory,
		0, bytes, 0, &pair->staging_map);
	if (result != VK_SUCCESS) return result;

	memset(&image_info, 0, sizeof(image_info));
	image_info.sType = VK_STRUCTURE_TYPE_IMAGE_CREATE_INFO;
	image_info.imageType = VK_IMAGE_TYPE_2D;
	image_info.format = VK_FORMAT_R8G8B8A8_UNORM;
	image_info.extent.width = (uint32_t)frame->width;
	image_info.extent.height = (uint32_t)frame->height;
	image_info.extent.depth = 1;
	image_info.mipLevels = 1;
	image_info.arrayLayers = 1;
	image_info.samples = VK_SAMPLE_COUNT_1_BIT;
	image_info.tiling = VK_IMAGE_TILING_OPTIMAL;
	image_info.usage = VK_IMAGE_USAGE_TRANSFER_DST_BIT | VK_IMAGE_USAGE_SAMPLED_BIT;
	image_info.sharingMode = VK_SHARING_MODE_EXCLUSIVE;
	image_info.initialLayout = VK_IMAGE_LAYOUT_UNDEFINED;
	result = tipsy_output.vk.create_image(tipsy_output.device, &image_info,
		NULL, &pair->foreground);
	if (result != VK_SUCCESS) return result;
	tipsy_vk_output_note_overlay_retained_locked();
	tipsy_output.vk.get_image_memory_requirements(tipsy_output.device,
		pair->foreground, &image_requirements);
	memory_type = tipsy_vk_output_memory_type_locked(image_requirements.memoryTypeBits,
		0, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT);
	if (memory_type == UINT32_MAX) return VK_ERROR_FEATURE_NOT_PRESENT;
	allocation.allocationSize = image_requirements.size;
	allocation.memoryTypeIndex = memory_type;
	result = tipsy_output.vk.allocate_memory(tipsy_output.device, &allocation,
		NULL, &pair->foreground_memory);
	if (result != VK_SUCCESS) return result;
	result = tipsy_output.vk.bind_image_memory(tipsy_output.device,
		pair->foreground, pair->foreground_memory, 0);
	if (result != VK_SUCCESS) return result;
	memset(&view_info, 0, sizeof(view_info));
	view_info.sType = VK_STRUCTURE_TYPE_IMAGE_VIEW_CREATE_INFO;
	view_info.image = pair->foreground;
	view_info.viewType = VK_IMAGE_VIEW_TYPE_2D;
	view_info.format = VK_FORMAT_R8G8B8A8_UNORM;
	view_info.subresourceRange.aspectMask = VK_IMAGE_ASPECT_COLOR_BIT;
	view_info.subresourceRange.levelCount = 1;
	view_info.subresourceRange.layerCount = 1;
	result = tipsy_output.vk.create_image_view(tipsy_output.device, &view_info,
		NULL, &pair->foreground_view);
	if (result != VK_SUCCESS) return result;
	memset(&descriptor_image, 0, sizeof(descriptor_image));
	descriptor_image.sampler = pair->sampler;
	descriptor_image.imageView = pair->foreground_view;
	descriptor_image.imageLayout = VK_IMAGE_LAYOUT_SHADER_READ_ONLY_OPTIMAL;
	memset(&write, 0, sizeof(write));
	write.sType = VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET;
	write.dstSet = pair->descriptor;
	write.dstBinding = 0;
	write.descriptorCount = 1;
	write.descriptorType = VK_DESCRIPTOR_TYPE_COMBINED_IMAGE_SAMPLER;
	write.pImageInfo = &descriptor_image;
	tipsy_output.vk.update_descriptor_sets(tipsy_output.device, 1, &write, 0, NULL);
	pair->foreground_width = (uint32_t)frame->width;
	pair->foreground_height = (uint32_t)frame->height;
	return VK_SUCCESS;
}

static int tipsy_vk_output_prepare_foreground_locked(TipsyVkOutputPair *pair,
	const struct tipsy_focused_text_frame *frame, int *upload)
{
	size_t bytes;
	if (frame == NULL || frame->rgba == NULL || frame->lease == 0 ||
		frame->generation == 0 || frame->x < 0 || frame->y < 0 ||
		frame->width < 1 || frame->height < 1 || frame->stride != frame->width * 4 ||
		pair->extent.width == 0 || pair->extent.height == 0 ||
		(size_t)frame->height > SIZE_MAX / (size_t)frame->stride) {
		return 0;
	}
	*upload = pair->foreground_generation != frame->generation;
	if (!*upload) {
		return pair->foreground != VK_NULL_HANDLE;
	}
	if (tipsy_vk_output_wait_pair_fences_locked(pair) != VK_SUCCESS) {
		return 0;
	}
	if (pair->foreground_width != (uint32_t)frame->width ||
		pair->foreground_height != (uint32_t)frame->height) {
		tipsy_vk_output_destroy_foreground_locked(pair);
		if (tipsy_vk_output_create_foreground_locked(pair, frame) != VK_SUCCESS) {
			tipsy_vk_output_destroy_foreground_locked(pair);
			return 0;
		}
	}
	bytes = (size_t)frame->stride * (size_t)frame->height;
	memcpy(pair->staging_map, frame->rgba, bytes);
	pair->foreground_generation = frame->generation;
	return 1;
}

static void tipsy_vk_output_barrier(VkCommandBuffer command, VkImage image,
	VkImageLayout old_layout, VkImageLayout new_layout,
	VkAccessFlags source_access, VkAccessFlags destination_access,
	VkPipelineStageFlags source_stage, VkPipelineStageFlags destination_stage)
{
	VkImageMemoryBarrier barrier;
	memset(&barrier, 0, sizeof(barrier));
	barrier.sType = VK_STRUCTURE_TYPE_IMAGE_MEMORY_BARRIER;
	barrier.srcAccessMask = source_access;
	barrier.dstAccessMask = destination_access;
	barrier.oldLayout = old_layout;
	barrier.newLayout = new_layout;
	barrier.srcQueueFamilyIndex = VK_QUEUE_FAMILY_IGNORED;
	barrier.dstQueueFamilyIndex = VK_QUEUE_FAMILY_IGNORED;
	barrier.image = image;
	barrier.subresourceRange.aspectMask = VK_IMAGE_ASPECT_COLOR_BIT;
	barrier.subresourceRange.levelCount = 1;
	barrier.subresourceRange.layerCount = 1;
	tipsy_output.vk.cmd_pipeline_barrier(command, source_stage, destination_stage,
		0, 0, NULL, 0, NULL, 1, &barrier);
}

static VkResult tipsy_vk_output_record_locked(TipsyVkOutputPair *pair,
	uint32_t source_index, const struct tipsy_focused_text_frame *foreground,
	int upload)
{
	TipsyVkSourceImageState *source = &pair->source[source_index];
	VkCommandBuffer command = source->command;
	VkCommandBufferBeginInfo begin;
	VkBufferImageCopy upload_region;
	VkRenderPassBeginInfo render;
	VkViewport viewport;
	VkRect2D scissor;
	float rectangle[4];
	VkResult result;
	uint32_t scissor_w;
	uint32_t scissor_h;

	if (pair->extent.width == 0 || pair->extent.height == 0 ||
		(uint32_t)foreground->x >= pair->extent.width ||
		(uint32_t)foreground->y >= pair->extent.height) {
		return VK_ERROR_INITIALIZATION_FAILED;
	}
	scissor_w = (uint32_t)foreground->width;
	scissor_h = (uint32_t)foreground->height;
	if ((uint32_t)foreground->x + scissor_w > pair->extent.width) {
		scissor_w = pair->extent.width - (uint32_t)foreground->x;
	}
	if ((uint32_t)foreground->y + scissor_h > pair->extent.height) {
		scissor_h = pair->extent.height - (uint32_t)foreground->y;
	}
	if (scissor_w == 0 || scissor_h == 0) {
		return VK_ERROR_INITIALIZATION_FAILED;
	}

	memset(&begin, 0, sizeof(begin));
	begin.sType = VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO;
	begin.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
	result = tipsy_output.vk.reset_command_buffer(command, 0);
	if (result != VK_SUCCESS) return result;
	result = tipsy_output.vk.begin_command_buffer(command, &begin);
	if (result != VK_SUCCESS) return result;
	if (upload) {
		tipsy_vk_output_barrier(command, pair->foreground,
			pair->foreground_initialized ? VK_IMAGE_LAYOUT_SHADER_READ_ONLY_OPTIMAL :
				VK_IMAGE_LAYOUT_UNDEFINED,
			VK_IMAGE_LAYOUT_TRANSFER_DST_OPTIMAL,
			pair->foreground_initialized ? VK_ACCESS_SHADER_READ_BIT : 0,
			VK_ACCESS_TRANSFER_WRITE_BIT,
			pair->foreground_initialized ? VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT :
				VK_PIPELINE_STAGE_TOP_OF_PIPE_BIT,
			VK_PIPELINE_STAGE_TRANSFER_BIT);
		memset(&upload_region, 0, sizeof(upload_region));
		upload_region.imageSubresource.aspectMask = VK_IMAGE_ASPECT_COLOR_BIT;
		upload_region.imageSubresource.layerCount = 1;
		upload_region.imageExtent.width = pair->foreground_width;
		upload_region.imageExtent.height = pair->foreground_height;
		upload_region.imageExtent.depth = 1;
		tipsy_output.vk.cmd_copy_buffer_to_image(command, pair->staging,
			pair->foreground, VK_IMAGE_LAYOUT_TRANSFER_DST_OPTIMAL, 1, &upload_region);
		tipsy_vk_output_barrier(command, pair->foreground,
			VK_IMAGE_LAYOUT_TRANSFER_DST_OPTIMAL,
			VK_IMAGE_LAYOUT_SHADER_READ_ONLY_OPTIMAL,
			VK_ACCESS_TRANSFER_WRITE_BIT, VK_ACCESS_SHADER_READ_BIT,
			VK_PIPELINE_STAGE_TRANSFER_BIT, VK_PIPELINE_STAGE_FRAGMENT_SHADER_BIT);
	}
	tipsy_vk_output_barrier(command, source->image,
		VK_IMAGE_LAYOUT_PRESENT_SRC_KHR, VK_IMAGE_LAYOUT_COLOR_ATTACHMENT_OPTIMAL,
		VK_ACCESS_MEMORY_WRITE_BIT,
		VK_ACCESS_COLOR_ATTACHMENT_READ_BIT | VK_ACCESS_COLOR_ATTACHMENT_WRITE_BIT,
		VK_PIPELINE_STAGE_ALL_COMMANDS_BIT, VK_PIPELINE_STAGE_COLOR_ATTACHMENT_OUTPUT_BIT);
	memset(&render, 0, sizeof(render));
	render.sType = VK_STRUCTURE_TYPE_RENDER_PASS_BEGIN_INFO;
	render.renderPass = pair->render_pass;
	render.framebuffer = source->framebuffer;
	render.renderArea.extent = pair->extent;
	tipsy_output.vk.cmd_begin_render_pass(command, &render,
		VK_SUBPASS_CONTENTS_INLINE);
	tipsy_output.vk.cmd_bind_pipeline(command, VK_PIPELINE_BIND_POINT_GRAPHICS,
		pair->pipeline);
	memset(&viewport, 0, sizeof(viewport));
	viewport.width = (float)pair->extent.width;
	viewport.height = (float)pair->extent.height;
	viewport.maxDepth = 1.0f;
	scissor.offset.x = foreground->x;
	scissor.offset.y = foreground->y;
	scissor.extent.width = scissor_w;
	scissor.extent.height = scissor_h;
	tipsy_output.vk.cmd_set_viewport(command, 0, 1, &viewport);
	tipsy_output.vk.cmd_set_scissor(command, 0, 1, &scissor);
	rectangle[0] = (2.0f * (float)foreground->x / (float)pair->extent.width) - 1.0f;
	rectangle[1] = (2.0f * (float)foreground->y / (float)pair->extent.height) - 1.0f;
	rectangle[2] = (2.0f * (float)(foreground->x + foreground->width) /
		(float)pair->extent.width) - 1.0f;
	rectangle[3] = (2.0f * (float)(foreground->y + foreground->height) /
		(float)pair->extent.height) - 1.0f;
	tipsy_output.vk.cmd_push_constants(command, pair->pipeline_layout,
		VK_SHADER_STAGE_VERTEX_BIT, 0, sizeof(rectangle), rectangle);
	tipsy_output.vk.cmd_bind_descriptor_sets(command,
		VK_PIPELINE_BIND_POINT_GRAPHICS, pair->pipeline_layout, 0, 1,
		&pair->descriptor, 0, NULL);
	tipsy_output.vk.cmd_draw(command, 4, 1, 0, 0);
	tipsy_output.vk.cmd_end_render_pass(command);
	tipsy_vk_output_barrier(command, source->image,
		VK_IMAGE_LAYOUT_COLOR_ATTACHMENT_OPTIMAL, VK_IMAGE_LAYOUT_PRESENT_SRC_KHR,
		VK_ACCESS_COLOR_ATTACHMENT_WRITE_BIT, 0,
		VK_PIPELINE_STAGE_COLOR_ATTACHMENT_OUTPUT_BIT,
		VK_PIPELINE_STAGE_BOTTOM_OF_PIPE_BIT);
	result = tipsy_output.vk.end_command_buffer(command);
	if (result == VK_SUCCESS && upload) pair->foreground_initialized = 1;
	return result;
}

static void tipsy_vk_output_clear_foreground_locked(TipsyVkOutputPair *pair)
{
	if (pair->foreground == VK_NULL_HANDLE) return;
	if (tipsy_vk_output_wait_pair_fences_locked(pair) != VK_SUCCESS) return;
	tipsy_vk_output_destroy_foreground_locked(pair);
}

static int tipsy_vk_output_is_oom(VkResult result)
{
	return result == VK_ERROR_OUT_OF_HOST_MEMORY ||
		result == VK_ERROR_OUT_OF_DEVICE_MEMORY;
}

static void tipsy_vk_output_quarantine_locked(TipsyVkOutputPair *pair)
{
	if (pair != NULL) pair->ready = 0;
	atomic_store_explicit(&output_route_ready, 0, memory_order_release);
}

int tipsy_vk_output_present(void *queue_ptr, const void *present_info_ptr,
	void *host_present_ptr, int32_t *guest_result)
{
	const VkPresentInfoKHR *info = (const VkPresentInfoKHR *)present_info_ptr;
	PFN_vkQueuePresentKHR host_present = (PFN_vkQueuePresentKHR)host_present_ptr;
	struct tipsy_focused_text_frame foreground;
	TipsyVkOutputPair *pair;
	TipsyVkSourceImageState *source;
	TipsyVkOutputQueueRecord *queue_record;
	VkSemaphore waits[TIPSY_VK_OUTPUT_MAX_WAITS];
	VkPipelineStageFlags stages[TIPSY_VK_OUTPUT_MAX_WAITS];
	VkSubmitInfo submit;
	VkPresentInfoKHR guest;
	VkResult submit_result = VK_SUCCESS;
	VkResult present_result = VK_SUCCESS;
	VkResult failure_result = VK_SUCCESS;
	VkResult fence_ready;
	uint32_t source_index;
	uint32_t i;
	int acquired = 0;
	int upload = 0;

	if (guest_result == NULL || host_present == NULL || queue_ptr == NULL) return 0;
	if (atomic_load_explicit(&output_route_ready, memory_order_acquire) == 0) {
		return 0;
	}
	if (!tipsy_vk_output_overlay_live()) {
		if (atomic_load_explicit(&output_foreground_retained,
			memory_order_acquire) == 0) {
			return 0;
		}
		if (test_transaction_fixture != NULL) {
			test_transaction_fixture->present_mutex_locks++;
		}
		pthread_mutex_lock(&tipsy_output.mutex);
		if (tipsy_output.active.active) {
			tipsy_vk_output_clear_foreground_locked(&tipsy_output.active);
		}
		pthread_mutex_unlock(&tipsy_output.mutex);
		return 0;
	}
	memset(&foreground, 0, sizeof(foreground));
	if (tipsy_focused_text_frame_release != NULL ||
		test_transaction_fixture != NULL) {
		acquired = tipsy_vk_output_foreground_acquire(&foreground);
	}
	if (test_transaction_fixture != NULL) {
		test_transaction_fixture->present_mutex_locks++;
	}
	pthread_mutex_lock(&tipsy_output.mutex);
	pair = &tipsy_output.active;
	if (acquired != 1) {
		if (pair->active) tipsy_vk_output_clear_foreground_locked(pair);
		pthread_mutex_unlock(&tipsy_output.mutex);
		return 0;
	}
	if (!tipsy_output.device_ready || tipsy_output.device_lost || !pair->active ||
		!pair->ready || info == NULL || info->sType != VK_STRUCTURE_TYPE_PRESENT_INFO_KHR ||
		info->swapchainCount != 1 ||
		info->pSwapchains == NULL || info->pImageIndices == NULL ||
		info->pSwapchains[0] != pair->guest || info->waitSemaphoreCount == 0 ||
		info->waitSemaphoreCount > TIPSY_VK_OUTPUT_MAX_WAITS ||
		info->pWaitSemaphores == NULL) {
		goto direct;
	}
	queue_record = tipsy_vk_output_queue_locked((VkQueue)queue_ptr);
	if (queue_record == NULL || queue_record->device != tipsy_output.device ||
		queue_record->family != tipsy_output.eligible_family ||
		queue_record->index >= tipsy_output.eligible_queue_count ||
		queue_record->flags != 0 ||
		(pair->queue != VK_NULL_HANDLE && pair->queue != (VkQueue)queue_ptr)) {
		goto direct;
	}
	source_index = info->pImageIndices[0];
	if (source_index >= pair->source_count) goto direct;
	source = &pair->source[source_index];
	if (!source->completion_reusable || source->framebuffer == VK_NULL_HANDLE ||
		source->command == VK_NULL_HANDLE || source->completion == VK_NULL_HANDLE) {
		goto direct;
	}
	if ((uint32_t)foreground.x >= pair->extent.width ||
		(uint32_t)foreground.y >= pair->extent.height) {
		goto direct;
	}
	fence_ready = tipsy_output.vk.wait_fences(tipsy_output.device, 1,
		&source->fence, VK_TRUE, 0);
	if (fence_ready == VK_TIMEOUT) goto direct;
	if (fence_ready != VK_SUCCESS) {
		failure_result = fence_ready;
		tipsy_vk_output_quarantine_locked(pair);
		if (fence_ready == VK_ERROR_DEVICE_LOST) goto device_lost;
		goto direct;
	}
	source->in_flight = 0;
	if (!tipsy_vk_output_prepare_foreground_locked(pair, &foreground, &upload)) {
		goto direct;
	}
	if (tipsy_vk_output_record_locked(pair, source_index, &foreground,
		upload) != VK_SUCCESS) {
		goto direct;
	}
	for (i = 0; i < info->waitSemaphoreCount; i++) {
		waits[i] = info->pWaitSemaphores[i];
		stages[i] = VK_PIPELINE_STAGE_ALL_COMMANDS_BIT;
	}
	memset(&submit, 0, sizeof(submit));
	submit.sType = VK_STRUCTURE_TYPE_SUBMIT_INFO;
	submit.waitSemaphoreCount = info->waitSemaphoreCount;
	submit.pWaitSemaphores = waits;
	submit.pWaitDstStageMask = stages;
	submit.commandBufferCount = 1;
	submit.pCommandBuffers = &source->command;
	submit.signalSemaphoreCount = 1;
	submit.pSignalSemaphores = &source->completion;
	submit_result = tipsy_output.vk.reset_fences(tipsy_output.device, 1,
		&source->fence);
	if (submit_result != VK_SUCCESS) {
		failure_result = submit_result;
		if (submit_result == VK_ERROR_DEVICE_LOST) goto device_lost;
		goto direct;
	}
	if (pair->queue == VK_NULL_HANDLE) pair->queue = (VkQueue)queue_ptr;
	submit_result = tipsy_output.vk.queue_submit((VkQueue)queue_ptr,
		1, &submit, source->fence);
	if (submit_result != VK_SUCCESS) {
		failure_result = submit_result;
		tipsy_vk_output_quarantine_locked(pair);
		if (submit_result == VK_ERROR_DEVICE_LOST) goto device_lost;
		if (tipsy_vk_output_is_oom(submit_result)) {
			goto direct;
		}
		goto handled_failure;
	}
	source->in_flight = 1;
	source->completion_reusable = 0;
	guest = *info;
	guest.waitSemaphoreCount = 1;
	guest.pWaitSemaphores = &source->completion;
	present_result = host_present((VkQueue)queue_ptr, &guest);
	if (present_result == VK_ERROR_DEVICE_LOST) {
		failure_result = present_result;
		goto device_lost;
	}
	if (present_result != VK_SUCCESS && present_result != VK_SUBOPTIMAL_KHR) {
		tipsy_vk_output_quarantine_locked(pair);
	}
	*guest_result = present_result;
	pthread_mutex_unlock(&tipsy_output.mutex);
	tipsy_vk_output_foreground_release(foreground.lease);
	return 1;

device_lost:
	(void)failure_result;
	tipsy_output.device_lost = 1;
	tipsy_vk_output_quarantine_locked(pair);
	*guest_result = VK_ERROR_DEVICE_LOST;
	pthread_mutex_unlock(&tipsy_output.mutex);
	tipsy_vk_output_foreground_release(foreground.lease);
	return 1;

handled_failure:
	tipsy_vk_output_quarantine_locked(pair);
	*guest_result = failure_result;
	pthread_mutex_unlock(&tipsy_output.mutex);
	tipsy_vk_output_foreground_release(foreground.lease);
	return 1;

direct:
	pthread_mutex_unlock(&tipsy_output.mutex);
	tipsy_vk_output_foreground_release(foreground.lease);
	return 0;
}

void tipsy_vk_output_swapchain_destroying(void *device_ptr, uint64_t swapchain)
{
	uint32_t i;
	pthread_mutex_lock(&tipsy_output.mutex);
	if ((VkDevice)device_ptr == tipsy_output.device) {
		if (tipsy_output.active.active &&
			tipsy_output.active.guest == (VkSwapchainKHR)swapchain) {
			tipsy_output.active.ready = 0;
			atomic_store_explicit(&output_route_ready, 0, memory_order_release);
			if (!tipsy_output.device_lost) {
				tipsy_vk_output_destroy_pair_locked(&tipsy_output.active, 1);
			} else {
				memset(&tipsy_output.active, 0, sizeof(tipsy_output.active));
				tipsy_vk_output_note_overlay_retained_locked();
			}
		}
		for (i = 0; i < tipsy_output.retired_count; ) {
			if (tipsy_output.retired[i].guest == (VkSwapchainKHR)swapchain) {
				if (!tipsy_output.device_lost) {
					tipsy_vk_output_destroy_pair_locked(&tipsy_output.retired[i], 1);
				} else {
					memset(&tipsy_output.retired[i], 0, sizeof(tipsy_output.retired[i]));
				}
				tipsy_output.retired[i] = tipsy_output.retired[tipsy_output.retired_count - 1u];
				memset(&tipsy_output.retired[tipsy_output.retired_count - 1u], 0,
					sizeof(tipsy_output.retired[0]));
				tipsy_output.retired_count--;
			} else {
				i++;
			}
		}
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_device_destroying(void *device_ptr)
{
	uint32_t i;
	pthread_mutex_lock(&tipsy_output.mutex);
	if ((VkDevice)device_ptr == tipsy_output.device) {
		atomic_store_explicit(&output_route_ready, 0, memory_order_release);
		if (!tipsy_output.device_lost) {
			tipsy_vk_output_destroy_pair_locked(&tipsy_output.active, 1);
			for (i = 0; i < tipsy_output.retired_count; i++) {
				tipsy_vk_output_destroy_pair_locked(&tipsy_output.retired[i], 1);
			}
		} else {
			memset(&tipsy_output.active, 0, sizeof(tipsy_output.active));
			memset(tipsy_output.retired, 0, sizeof(tipsy_output.retired));
			tipsy_vk_output_note_overlay_retained_locked();
		}
		tipsy_output.retired_count = 0;
		tipsy_output.device = VK_NULL_HANDLE;
		tipsy_output.device_ready = 0;
		tipsy_output.device_candidate = 0;
		tipsy_output.eligible_queue_count = 0;
		tipsy_output.device_lost = 0;
		memset(tipsy_output.queues, 0, sizeof(tipsy_output.queues));
		memset(&tipsy_output.vk.create_semaphore, 0,
			sizeof(TipsyVkOutputDispatch) -
			offsetof(TipsyVkOutputDispatch, create_semaphore));
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_source_surface_destroying(void *instance_ptr, uint64_t surface)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	if ((VkInstance)instance_ptr == tipsy_output.instance &&
		(VkSurfaceKHR)surface == tipsy_output.source_surface) {
		atomic_store_explicit(&output_route_ready, 0, memory_order_release);
		tipsy_output.source_surface = VK_NULL_HANDLE;
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

void tipsy_vk_output_instance_destroying(void *instance_ptr)
{
	pthread_mutex_lock(&tipsy_output.mutex);
	if ((VkInstance)instance_ptr == tipsy_output.instance) {
		atomic_store_explicit(&output_route_ready, 0, memory_order_release);
		tipsy_output.instance = VK_NULL_HANDLE;
		tipsy_output.source_surface = VK_NULL_HANDLE;
		memset(&tipsy_output.vk, 0, sizeof(tipsy_output.vk));
	}
	pthread_mutex_unlock(&tipsy_output.mutex);
}

uint32_t tipsy_test_vk_output_queue_eligible(uint32_t scenario)
{
	VkDeviceQueueCreateInfo queue;
	VkDeviceCreateInfo device;
	VkQueueFamilyProperties family;
	float priorities[2] = {0.75f, 0.5f};
	VkBool32 source_supported = VK_TRUE;
	VkBool32 output_supported = VK_TRUE;
	memset(&queue, 0, sizeof(queue));
	queue.sType = VK_STRUCTURE_TYPE_DEVICE_QUEUE_CREATE_INFO;
	queue.queueFamilyIndex = 3;
	queue.queueCount = 2;
	queue.pQueuePriorities = priorities;
	memset(&device, 0, sizeof(device));
	device.sType = VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO;
	device.queueCreateInfoCount = 1;
	device.pQueueCreateInfos = &queue;
	memset(&family, 0, sizeof(family));
	family.queueFlags = VK_QUEUE_GRAPHICS_BIT;
	family.queueCount = 4;
	if (scenario == TIPSY_VK_OUTPUT_FIXTURE_ALL_QUEUES_REQUESTED) {
		family.queueCount = queue.queueCount;
	}
	if (scenario == TIPSY_VK_OUTPUT_FIXTURE_SOURCE_UNSUPPORTED) source_supported = VK_FALSE;
	if (scenario == TIPSY_VK_OUTPUT_FIXTURE_OUTPUT_UNSUPPORTED) output_supported = VK_FALSE;
	if (scenario == TIPSY_VK_OUTPUT_FIXTURE_PROTECTED_QUEUE) queue.flags = VK_DEVICE_QUEUE_CREATE_PROTECTED_BIT;
	if (scenario == TIPSY_VK_OUTPUT_FIXTURE_INTERNALLY_SYNCHRONIZED_QUEUE) {
		queue.flags = VK_DEVICE_QUEUE_CREATE_INTERNALLY_SYNCHRONIZED_BIT_KHR;
	}
	return (uint32_t)tipsy_vk_output_queue_request_valid(&device, 3, &family,
		source_supported, output_supported, NULL);
}

static void tipsy_vk_output_fixture_operation(
	TipsyVkOutputTransactionFixture *out, uint32_t operation)
{
	if (out->order_count < 5u) out->order[out->order_count++] = operation;
}

static uint32_t test_transaction_scenario;
static const VkDevice test_transaction_device = (VkDevice)(uintptr_t)0x41;
static const VkQueue test_transaction_queue = (VkQueue)(uintptr_t)0x42;
static const VkSwapchainKHR test_transaction_guest =
	(VkSwapchainKHR)(uintptr_t)0x43;
static const VkSemaphore test_transaction_original_wait =
	(VkSemaphore)(uintptr_t)0x45;
static const VkSemaphore test_transaction_g = (VkSemaphore)(uintptr_t)0x46;

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_wait_fences(
	VkDevice device, uint32_t count, const VkFence *fences, VkBool32 wait_all,
	uint64_t timeout)
{
	(void)device;
	(void)count;
	(void)fences;
	(void)wait_all;
	(void)timeout;
	if (test_transaction_scenario == TIPSY_VK_OUTPUT_TX_FRAME_BUSY) {
		return VK_TIMEOUT;
	}
	return VK_SUCCESS;
}

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_reset_command(
	VkCommandBuffer command, VkCommandBufferResetFlags flags)
{
	(void)command;
	(void)flags;
	return VK_SUCCESS;
}

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_begin_command(
	VkCommandBuffer command, const VkCommandBufferBeginInfo *info)
{
	(void)command;
	(void)info;
	return VK_SUCCESS;
}

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_end_command(
	VkCommandBuffer command)
{
	(void)command;
	return VK_SUCCESS;
}

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_reset_fences(
	VkDevice device, uint32_t count, const VkFence *fences)
{
	(void)device;
	(void)count;
	(void)fences;
	return VK_SUCCESS;
}

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_submit(
	VkQueue queue, uint32_t count, const VkSubmitInfo *submits, VkFence fence)
{
	(void)queue;
	(void)fence;
	if (count == 1 && submits != NULL && submits[0].commandBufferCount == 0) {
		tipsy_vk_output_fixture_operation(test_transaction_fixture, 5);
		test_transaction_fixture->acquire_consume_calls++;
		return VK_SUCCESS;
	}
	tipsy_vk_output_fixture_operation(test_transaction_fixture, 2);
	test_transaction_fixture->composition_submit_calls++;
	switch (test_transaction_scenario) {
	case TIPSY_VK_OUTPUT_TX_SUBMIT_OOM:
		return VK_ERROR_OUT_OF_HOST_MEMORY;
	case TIPSY_VK_OUTPUT_TX_SUBMIT_DEVICE_LOST:
		return VK_ERROR_DEVICE_LOST;
	case TIPSY_VK_OUTPUT_TX_SUBMIT_AMBIGUOUS:
		return VK_ERROR_UNKNOWN;
	default:
		test_transaction_fixture->guest_wait_consumed_once = 1;
		return VK_SUCCESS;
	}
}

static VKAPI_ATTR VkResult VKAPI_CALL tipsy_vk_output_fixture_present(
	VkQueue queue, const VkPresentInfoKHR *info)
{
	(void)queue;
	tipsy_vk_output_fixture_operation(test_transaction_fixture, 3);
	test_transaction_fixture->guest_present_calls++;
	if (info != NULL && info->waitSemaphoreCount == 1 &&
		info->pWaitSemaphores != NULL &&
		info->pWaitSemaphores[0] == test_transaction_original_wait) {
		test_transaction_fixture->original_guest_forwarded = 1;
		if (info->pResults != NULL) info->pResults[0] = VK_SUCCESS;
		return VK_SUCCESS;
	}
	if (test_transaction_scenario == TIPSY_VK_OUTPUT_TX_GUEST_PRESENT_OOM) {
		return VK_ERROR_OUT_OF_HOST_MEMORY;
	}
	if (test_transaction_scenario ==
		TIPSY_VK_OUTPUT_TX_GUEST_PRESENT_DEVICE_LOST) {
		return VK_ERROR_DEVICE_LOST;
	}
	test_transaction_fixture->g_wait_consumed_once = 1;
	if (test_transaction_scenario == TIPSY_VK_OUTPUT_TX_GUEST_PRESENT_REJECTED) {
		return VK_ERROR_OUT_OF_DATE_KHR;
	}
	if (info != NULL && info->pResults != NULL) info->pResults[0] = VK_SUCCESS;
	return VK_SUCCESS;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_pipeline_barrier(
	VkCommandBuffer command, VkPipelineStageFlags source_stage,
	VkPipelineStageFlags destination_stage, VkDependencyFlags dependencies,
	uint32_t memory_count, const VkMemoryBarrier *memory,
	uint32_t buffer_count, const VkBufferMemoryBarrier *buffers,
	uint32_t image_count, const VkImageMemoryBarrier *images)
{
	(void)command;
	(void)source_stage;
	(void)destination_stage;
	(void)dependencies;
	(void)memory_count;
	(void)memory;
	(void)buffer_count;
	(void)buffers;
	(void)image_count;
	(void)images;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_upload(
	VkCommandBuffer command, VkBuffer source, VkImage destination,
	VkImageLayout destination_layout, uint32_t count,
	const VkBufferImageCopy *regions)
{
	(void)command;
	(void)source;
	(void)destination;
	(void)destination_layout;
	(void)count;
	(void)regions;
	test_transaction_fixture->upload_count++;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_begin_render_pass(
	VkCommandBuffer command, const VkRenderPassBeginInfo *info,
	VkSubpassContents contents)
{
	(void)command;
	(void)info;
	(void)contents;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_end_render_pass(
	VkCommandBuffer command)
{
	(void)command;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_bind_pipeline(
	VkCommandBuffer command, VkPipelineBindPoint bind_point, VkPipeline pipeline)
{
	(void)command;
	(void)bind_point;
	(void)pipeline;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_bind_descriptors(
	VkCommandBuffer command, VkPipelineBindPoint bind_point,
	VkPipelineLayout layout, uint32_t first_set, uint32_t set_count,
	const VkDescriptorSet *sets, uint32_t dynamic_count,
	const uint32_t *dynamic_offsets)
{
	(void)command;
	(void)bind_point;
	(void)layout;
	(void)first_set;
	(void)set_count;
	(void)sets;
	(void)dynamic_count;
	(void)dynamic_offsets;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_set_viewport(
	VkCommandBuffer command, uint32_t first, uint32_t count,
	const VkViewport *viewports)
{
	(void)command;
	(void)first;
	if (count == 1 && viewports != NULL) {
		test_transaction_fixture->viewport_width = (uint32_t)viewports[0].width;
		test_transaction_fixture->viewport_height = (uint32_t)viewports[0].height;
	}
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_set_scissor(
	VkCommandBuffer command, uint32_t first, uint32_t count,
	const VkRect2D *scissors)
{
	(void)command;
	(void)first;
	(void)count;
	(void)scissors;
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_push_constants(
	VkCommandBuffer command, VkPipelineLayout layout,
	VkShaderStageFlags stages, uint32_t offset, uint32_t size,
	const void *values)
{
	(void)command;
	(void)layout;
	(void)stages;
	(void)offset;
	if (size >= sizeof(float) * 4u && values != NULL) {
		memcpy(test_transaction_fixture->push_rect, values, sizeof(float) * 4u);
	}
}

static VKAPI_ATTR void VKAPI_CALL tipsy_vk_output_fixture_draw(
	VkCommandBuffer command, uint32_t vertices, uint32_t instances,
	uint32_t first_vertex, uint32_t first_instance)
{
	(void)command;
	(void)vertices;
	(void)instances;
	(void)first_vertex;
	(void)first_instance;
	test_transaction_fixture->draw_count++;
}

static void tipsy_vk_output_fixture_install_dispatch(void)
{
	tipsy_output.vk.wait_fences = tipsy_vk_output_fixture_wait_fences;
	tipsy_output.vk.reset_fences = tipsy_vk_output_fixture_reset_fences;
	tipsy_output.vk.reset_command_buffer = tipsy_vk_output_fixture_reset_command;
	tipsy_output.vk.begin_command_buffer = tipsy_vk_output_fixture_begin_command;
	tipsy_output.vk.end_command_buffer = tipsy_vk_output_fixture_end_command;
	tipsy_output.vk.queue_submit = tipsy_vk_output_fixture_submit;
	tipsy_output.vk.cmd_pipeline_barrier =
		tipsy_vk_output_fixture_pipeline_barrier;
	tipsy_output.vk.cmd_copy_buffer_to_image = tipsy_vk_output_fixture_upload;
	tipsy_output.vk.cmd_begin_render_pass =
		tipsy_vk_output_fixture_begin_render_pass;
	tipsy_output.vk.cmd_end_render_pass = tipsy_vk_output_fixture_end_render_pass;
	tipsy_output.vk.cmd_bind_pipeline = tipsy_vk_output_fixture_bind_pipeline;
	tipsy_output.vk.cmd_bind_descriptor_sets =
		tipsy_vk_output_fixture_bind_descriptors;
	tipsy_output.vk.cmd_set_viewport = tipsy_vk_output_fixture_set_viewport;
	tipsy_output.vk.cmd_set_scissor = tipsy_vk_output_fixture_set_scissor;
	tipsy_output.vk.cmd_push_constants =
		tipsy_vk_output_fixture_push_constants;
	tipsy_output.vk.cmd_draw = tipsy_vk_output_fixture_draw;
}

static void tipsy_vk_output_fixture_arm_pair(uint8_t *staging)
{
	tipsy_output.device = test_transaction_device;
	tipsy_output.device_ready = 1;
	tipsy_output.eligible_family = 3;
	tipsy_output.eligible_queue_count = 1;
	tipsy_output.queues[0].device = test_transaction_device;
	tipsy_output.queues[0].queue = test_transaction_queue;
	tipsy_output.queues[0].family = 3;
	tipsy_output.active.active = 1;
	tipsy_output.active.ready = 1;
	tipsy_output.active.guest = test_transaction_guest;
	tipsy_output.active.extent.width = 8;
	tipsy_output.active.extent.height = 8;
	tipsy_output.active.source_count = 1;
	tipsy_output.active.source[0].image = (VkImage)(uintptr_t)0x47;
	tipsy_output.active.source[0].view = (VkImageView)(uintptr_t)0x54;
	tipsy_output.active.source[0].framebuffer = (VkFramebuffer)(uintptr_t)0x4a;
	tipsy_output.active.source[0].command = (VkCommandBuffer)(uintptr_t)0x4b;
	tipsy_output.active.source[0].completion = test_transaction_g;
	tipsy_output.active.source[0].fence = (VkFence)(uintptr_t)0x4d;
	tipsy_output.active.source[0].completion_reusable = 1;
	tipsy_output.active.pipeline = (VkPipeline)(uintptr_t)0x4e;
	tipsy_output.active.pipeline_layout = (VkPipelineLayout)(uintptr_t)0x4f;
	tipsy_output.active.descriptor = (VkDescriptorSet)(uintptr_t)0x50;
	tipsy_output.active.staging_map = staging;
	tipsy_output.active.foreground = (VkImage)(uintptr_t)0x51;
	tipsy_output.active.foreground_width = 1;
	tipsy_output.active.foreground_height = 1;
	tipsy_output.active.foreground_initialized = 1;
	tipsy_vk_output_fixture_install_dispatch();
}

void tipsy_test_vk_output_transaction_fixture(uint32_t scenario,
	TipsyVkOutputTransactionFixture *out)
{
	const size_t state_offset = offsetof(TipsyVkOutputState, gipa);
	unsigned char saved_state[sizeof(TipsyVkOutputState) -
		offsetof(TipsyVkOutputState, gipa)];
	uint8_t staging[4] = {0, 0, 0, 0};
	VkSemaphore original_wait = test_transaction_original_wait;
	VkSwapchainKHR guest_swapchains[2] = {test_transaction_guest,
		(VkSwapchainKHR)(uintptr_t)0x52};
	uint32_t image_indices[2] = {0, 0};
	VkPresentInfoKHR present;
	VkApplicationInfo present_pnext;
	VkResult individual[2] = {VK_SUCCESS, VK_SUCCESS};
	int32_t result = VK_ERROR_INITIALIZATION_FAILED;
	uint32_t saved_route_ready;
	int handled;
	if (out == NULL) return;
	memset(out, 0, sizeof(*out));
	pthread_mutex_lock(&tipsy_output.mutex);
	saved_route_ready = atomic_load_explicit(&output_route_ready,
		memory_order_acquire);
	memcpy(saved_state, (unsigned char *)&tipsy_output + state_offset,
		sizeof(saved_state));
	memset((unsigned char *)&tipsy_output + state_offset, 0,
		sizeof(saved_state));
	tipsy_vk_output_fixture_arm_pair(staging);
	test_transaction_scenario = scenario;
	test_transaction_fixture = out;
	test_transaction_overlay_live = 1;
	test_transaction_frame_x = 2;
	test_transaction_frame_y = 3;
	test_transaction_frame_width = 1;
	test_transaction_frame_height = 1;
	if (scenario == TIPSY_VK_OUTPUT_TX_UNPUBLISHED ||
		scenario == TIPSY_VK_OUTPUT_TX_UNPUBLISHED_TEARDOWN) {
		test_transaction_overlay_live = 0;
		if (scenario == TIPSY_VK_OUTPUT_TX_UNPUBLISHED) {
			tipsy_output.active.foreground = VK_NULL_HANDLE;
			tipsy_output.active.staging_map = NULL;
			tipsy_output.active.foreground_width = 0;
			tipsy_output.active.foreground_height = 0;
			tipsy_output.active.foreground_initialized = 0;
		}
	}
	tipsy_vk_output_note_overlay_retained_locked();
	atomic_store_explicit(&output_route_ready, 1, memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);

	memset(&present, 0, sizeof(present));
	memset(&present_pnext, 0, sizeof(present_pnext));
	present_pnext.sType = VK_STRUCTURE_TYPE_APPLICATION_INFO;
	present.sType = VK_STRUCTURE_TYPE_PRESENT_INFO_KHR;
	present.pNext = &present_pnext;
	present.waitSemaphoreCount = 1;
	present.pWaitSemaphores = &original_wait;
	present.swapchainCount = scenario == TIPSY_VK_OUTPUT_TX_DIRECT_GATE ? 2u : 1u;
	present.pSwapchains = guest_swapchains;
	present.pImageIndices = image_indices;
	present.pResults = individual;
	handled = tipsy_vk_output_present(test_transaction_queue, &present,
		(void *)tipsy_vk_output_fixture_present, &result);
	if (!handled) result = tipsy_vk_output_fixture_present(test_transaction_queue,
		&present);
	if (scenario == TIPSY_VK_OUTPUT_TX_UNPUBLISHED_TEARDOWN) {
		uint32_t first_locks = out->present_mutex_locks;
		(void)tipsy_vk_output_present(test_transaction_queue, &present,
			(void *)tipsy_vk_output_fixture_present, &result);
		out->unpublished_followup_mutex_locks =
			out->present_mutex_locks - first_locks;
	}

	pthread_mutex_lock(&tipsy_output.mutex);
	out->quarantined = !tipsy_output.active.ready;
	out->device_terminal = tipsy_output.device_lost;
	out->returned_result = result;
	test_transaction_fixture = NULL;
	test_transaction_scenario = 0;
	test_transaction_overlay_live = 1;
	test_transaction_frame_x = 2;
	test_transaction_frame_y = 3;
	test_transaction_frame_width = 1;
	test_transaction_frame_height = 1;
	memcpy((unsigned char *)&tipsy_output + state_offset, saved_state,
		sizeof(saved_state));
	tipsy_vk_output_note_overlay_retained_locked();
	atomic_store_explicit(&output_route_ready, saved_route_ready,
		memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);
}

static void tipsy_vk_output_fixture_rearm_source_locked(void)
{
	tipsy_output.active.source[0].completion_reusable = 1;
}

static void tipsy_vk_output_fixture_drive_present(VkPresentInfoKHR *present,
	int32_t *result)
{
	int handled = tipsy_vk_output_present(test_transaction_queue, present,
		(void *)tipsy_vk_output_fixture_present, result);
	if (!handled) {
		*result = tipsy_vk_output_fixture_present(test_transaction_queue, present);
	}
}

void tipsy_test_vk_output_overlay_geometry_fixture(
	TipsyVkOutputGeometryFixture *out)
{
	const size_t state_offset = offsetof(TipsyVkOutputState, gipa);
	unsigned char saved_state[sizeof(TipsyVkOutputState) -
		offsetof(TipsyVkOutputState, gipa)];
	uint8_t staging[4] = {0, 0, 0, 0};
	VkSemaphore original_wait = test_transaction_original_wait;
	VkSwapchainKHR guest_swapchain = test_transaction_guest;
	uint32_t image_index = 0;
	VkPresentInfoKHR present;
	VkResult individual = VK_SUCCESS;
	int32_t result = VK_ERROR_INITIALIZATION_FAILED;
	uint32_t saved_route_ready;
	if (out == NULL) return;
	memset(out, 0, sizeof(*out));
	pthread_mutex_lock(&tipsy_output.mutex);
	saved_route_ready = atomic_load_explicit(&output_route_ready,
		memory_order_acquire);
	memcpy(saved_state, (unsigned char *)&tipsy_output + state_offset,
		sizeof(saved_state));
	memset((unsigned char *)&tipsy_output + state_offset, 0,
		sizeof(saved_state));
	tipsy_vk_output_fixture_arm_pair(staging);
	test_transaction_scenario = TIPSY_VK_OUTPUT_TX_SUCCESS;
	test_transaction_overlay_live = 1;
	test_transaction_frame_width = 1;
	test_transaction_frame_height = 1;
	tipsy_vk_output_note_overlay_retained_locked();
	atomic_store_explicit(&output_route_ready, 1, memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);

	memset(&present, 0, sizeof(present));
	present.sType = VK_STRUCTURE_TYPE_PRESENT_INFO_KHR;
	present.waitSemaphoreCount = 1;
	present.pWaitSemaphores = &original_wait;
	present.swapchainCount = 1;
	present.pSwapchains = &guest_swapchain;
	present.pImageIndices = &image_index;
	present.pResults = &individual;

	test_transaction_frame_x = 1;
	test_transaction_frame_y = 2;
	test_transaction_fixture = &out->first;
	result = VK_ERROR_INITIALIZATION_FAILED;
	tipsy_vk_output_fixture_drive_present(&present, &result);
	pthread_mutex_lock(&tipsy_output.mutex);
	out->first.quarantined = !tipsy_output.active.ready;
	out->first.device_terminal = tipsy_output.device_lost;
	out->first.returned_result = result;
	tipsy_vk_output_fixture_rearm_source_locked();
	pthread_mutex_unlock(&tipsy_output.mutex);

	test_transaction_frame_x = 4;
	test_transaction_frame_y = 5;
	test_transaction_fixture = &out->second;
	result = VK_ERROR_INITIALIZATION_FAILED;
	individual = VK_SUCCESS;
	tipsy_vk_output_fixture_drive_present(&present, &result);
	pthread_mutex_lock(&tipsy_output.mutex);
	out->second.quarantined = !tipsy_output.active.ready;
	out->second.device_terminal = tipsy_output.device_lost;
	out->second.returned_result = result;
	tipsy_vk_output_fixture_rearm_source_locked();
	pthread_mutex_unlock(&tipsy_output.mutex);

	test_transaction_frame_x = 8;
	test_transaction_frame_y = 0;
	test_transaction_fixture = &out->empty;
	result = VK_ERROR_INITIALIZATION_FAILED;
	individual = VK_SUCCESS;
	tipsy_vk_output_fixture_drive_present(&present, &result);
	pthread_mutex_lock(&tipsy_output.mutex);
	out->empty.quarantined = !tipsy_output.active.ready;
	out->empty.device_terminal = tipsy_output.device_lost;
	out->empty.returned_result = result;
	test_transaction_fixture = NULL;
	test_transaction_scenario = 0;
	test_transaction_overlay_live = 1;
	test_transaction_frame_x = 2;
	test_transaction_frame_y = 3;
	test_transaction_frame_width = 1;
	test_transaction_frame_height = 1;
	memcpy((unsigned char *)&tipsy_output + state_offset, saved_state,
		sizeof(saved_state));
	tipsy_vk_output_note_overlay_retained_locked();
	atomic_store_explicit(&output_route_ready, saved_route_ready,
		memory_order_release);
	pthread_mutex_unlock(&tipsy_output.mutex);
}
