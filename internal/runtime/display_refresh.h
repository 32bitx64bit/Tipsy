/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_DISPLAY_REFRESH_H
#define TIPSY_DISPLAY_REFRESH_H

#include "jni_bridge.h"

#ifdef __cplusplus
extern "C" {
#endif

uintptr_t tipsy_display_refresh_publisher_addr(void);
void tipsy_test_reset_display_refresh(void);
uintptr_t tipsy_test_current_refresh_addr(void);
uintptr_t tipsy_test_supported_refresh_addr(void);
jfloat tipsy_test_recorded_current_refresh(void);
jsize tipsy_test_recorded_supported_count(void);
jfloat tipsy_test_recorded_supported_refresh(jsize index);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_DISPLAY_REFRESH_H */
