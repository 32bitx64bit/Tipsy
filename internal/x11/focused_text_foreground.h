/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_FOCUSED_TEXT_FOREGROUND_H
#define TIPSY_FOCUSED_TEXT_FOREGROUND_H

#include <stdint.h>

/*
 * Premultiplied RGBA8 foreground leased to the host's pre-present compositor.
 * The pixels are transient, never contain a game background, and remain valid
 * only until tipsy_focused_text_frame_release(lease). Consumers must release
 * within the same presentation turn and may cache only GPU-owned copies.
 */
struct tipsy_focused_text_frame {
	const uint8_t *rgba;
	int x, y, width, height, stride;
	uint64_t generation;
	uintptr_t lease;
};

int tipsy_focused_text_frame_acquire(struct tipsy_focused_text_frame *out);
void tipsy_focused_text_frame_release(uintptr_t lease);

/* Acquire-load: 1 while a published overlay exists, else 0. Present paths may
 * skip overlay/output mutex and GLES GetCurrent/save-restore when this is 0
 * and they hold no retained child/texture. Acquire/release stay required for
 * the live compose path. */
int tipsy_focused_text_overlay_live(void);

#endif /* TIPSY_FOCUSED_TEXT_FOREGROUND_H */
