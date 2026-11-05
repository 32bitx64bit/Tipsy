/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Compiled by cgo as part of package loader.
 *
 * Per-name "unsupported native function" thunks for x86-64 SysV.
 *
 * The thunks are a fixed table of 16-byte slots assembled into this
 * object's read-only text. Nothing is mapped, patched or freed at run
 * time, so no page is ever writable and executable at once. Slot i loads
 * its index into %edi and jumps to tipsy_unsupported_hit, which reports
 * the call to Go (the index selects the symbol name) and returns 0.
 * The jump keeps the stack exactly as the guest's call left it, so the
 * handler runs with a normal function-entry frame and returns straight
 * to the guest caller. Guest arguments in %rdi and friends are ignored.
 */
#include <stddef.h>
#include <stdint.h>

#define TIPSY_UNSUPPORTED_SLOTS 1024u
#define TIPSY_UNSUPPORTED_STRIDE 16u

/* Exported from unsupported.go. */
extern void GoLoader_UnsupportedCalled(unsigned int idx);

__attribute__((used, visibility("hidden")))
uintptr_t tipsy_unsupported_hit(unsigned int idx)
{
	GoLoader_UnsupportedCalled(idx);
	return 0;
}

__asm__(
	".pushsection .text,\"ax\",@progbits\n"
	".balign 16\n"
	".globl tipsy_unsupported_slots\n"
	".hidden tipsy_unsupported_slots\n"
	".type tipsy_unsupported_slots,@function\n"
	"tipsy_unsupported_slots:\n"
	".set tipsy_slot_i, 0\n"
	".rept 1024\n"
	".balign 16\n"
	"	endbr64\n"
	"	movl $tipsy_slot_i, %edi\n"
	"	jmp tipsy_unsupported_hit\n"
	".set tipsy_slot_i, tipsy_slot_i+1\n"
	".endr\n"
	".size tipsy_unsupported_slots, .-tipsy_unsupported_slots\n"
	".popsection\n");

extern const char tipsy_unsupported_slots[] __attribute__((visibility("hidden")));

unsigned int tipsy_unsupported_slot_count(void)
{
	return TIPSY_UNSUPPORTED_SLOTS;
}

uintptr_t tipsy_unsupported_slot(unsigned int idx)
{
	if (idx >= TIPSY_UNSUPPORTED_SLOTS)
		return 0;
	return (uintptr_t)tipsy_unsupported_slots + (uintptr_t)idx * TIPSY_UNSUPPORTED_STRIDE;
}
