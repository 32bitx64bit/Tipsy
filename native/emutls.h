/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * LLVM emulated TLS used by libroblox.so. The in-tree copy mallocs
 * through mimalloc, which stores its TLD in emutls — unbounded
 * recursion. This implementation uses glibc malloc + pthread TSD.
 */
#ifndef TIPSY_EMUTLS_H
#define TIPSY_EMUTLS_H

#include <stdint.h>

void *tipsy_emutls_get_address(void *control);
void tipsy_install_emutls_hook(void *at);
void tipsy_patch_cxa_once(void *at);
void tipsy_set_roblox_bias(uintptr_t bias);
/* File vaddr of the LLVM emutls control holding Roblox's thread_local
 * JNINativeInterface*. Discovered from the official FindClass thunks
 * (call *[slot+0x30]); not a version-pinned constant. */
void tipsy_set_roblox_jni_tls(uintptr_t file_vaddr);
/* Original JNINativeInterface* (not JNIEnv*). Roblox FindClass thunks
 * do call *[tls_slot+0x30]. */
void tipsy_set_jni_functions(void *functions);

#endif
