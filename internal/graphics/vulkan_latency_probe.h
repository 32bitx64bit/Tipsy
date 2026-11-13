/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */
#ifndef TIPSY_VULKAN_LATENCY_PROBE_H
#define TIPSY_VULKAN_LATENCY_PROBE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
	int Available;
	int NeedsLayerEnv;
	uint32_t VendorID;
	uint32_t DeviceType;
	int Listed;
	char Driver[256];
	char Reason[256];
} TipsyAntiLagProbe;

/* Runs the probe in this process and prints a small key=value report on
 * stdout. Returns 0 when an eligible RADV device exposes VK_AMD_anti_lag. */
int tipsy_vulkan_antilag_probe(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_VULKAN_LATENCY_PROBE_H */
