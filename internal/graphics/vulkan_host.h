/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_VULKAN_HOST_H
#define TIPSY_VULKAN_HOST_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

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

int tipsy_vulkan_host_probe(uint32_t *out_api, uint32_t *out_devices,
	int32_t *out_result, int *out_xcb, int *out_xlib);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_VULKAN_HOST_H */
