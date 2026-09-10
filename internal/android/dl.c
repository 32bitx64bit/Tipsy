/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * libdl.so: Android-namespace dlopen/dlsym/dlclose via a Go registry.
 * Host glibc dlopen is NOT interposed; these symbols are returned by Lookup().
 */
#include "android_bridge.h"

#include <dlfcn.h>
#include <elf.h>
#include <link.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>
#include <stdatomic.h>
#include <sys/uio.h>
#include <unistd.h>

extern void *GoAndroid_dlopen(char *filename, int flags);
extern void *GoAndroid_dlsym(void *handle, char *symbol);
extern int GoAndroid_dlclose(void *handle);
extern char *GoAndroid_dlerror(void);

/* Opaque dlopen token. Identity lives in the Go registry, so this allocation
 * only guarantees a unique non-NULL handle; nothing dereferences it in C. */
void *tipsy_dlhandle_new(const char *soname)
{
	(void)soname;
	return malloc(1);
}

void *tipsy_dlopen(const char *filename, int flags)
{
	return GoAndroid_dlopen((char *)filename, flags);
}

void *tipsy_dlsym(void *handle, const char *symbol)
{
	return GoAndroid_dlsym(handle, (char *)symbol);
}

int tipsy_dlclose(void *handle)
{
	return GoAndroid_dlclose(handle);
}

/* dlerror is thread-local by contract. GoAndroid_dlerror always hands back a
 * freshly allocated string; cache it per OS thread and free the previous one
 * exactly once here. */
static __thread char *tls_dlerror;

char *tipsy_dlerror(void)
{
	char *msg = GoAndroid_dlerror();

	if (tls_dlerror != NULL) {
		free(tls_dlerror);
		tls_dlerror = NULL;
	}
	if (msg != NULL) {
		tls_dlerror = msg;
	}
	return tls_dlerror;
}

int tipsy_dladdr(const void *addr, Dl_info *info)
{
	if (info != NULL) {
		memset(info, 0, sizeof(*info));
	}
	return dladdr(addr, info);
}

#define TIPSY_MAX_IMAGES 16

struct tipsy_code_range {
	uintptr_t start;
	uintptr_t end;
};

struct tipsy_image {
	uintptr_t addr;
	char name[256];
	struct tipsy_code_range *code;
	size_t ncode;
	int module_class;
};

static struct tipsy_image g_images[TIPSY_MAX_IMAGES];
static int g_nimages;
static pthread_mutex_t g_image_mu = PTHREAD_MUTEX_INITIALIZER;
static _Atomic uint64_t g_image_generation = 1;

/* Copy guest bytes without faulting: process_vm_readv reports EFAULT for an
 * unmapped, PROT_NONE, or otherwise unreadable range instead of raising
 * SIGSEGV, so a malformed e_phoff/e_phnum cannot crash the process. A short
 * copy counts as failure. */
static int tipsy_read_guest(const void *src, size_t len, void *dst)
{
	struct iovec local, remote;

	if (len == 0) {
		return 1;
	}
	local.iov_base = dst;
	local.iov_len = len;
	remote.iov_base = (void *)src;
	remote.iov_len = len;
	return process_vm_readv(getpid(), &local, 1, &remote, 1, 0) == (ssize_t)len;
}

void tipsy_register_image(uintptr_t load_bias, const char *name)
{
	const Elf64_Ehdr *eh = (const Elf64_Ehdr *)load_bias;
	Elf64_Phdr *phdr = NULL;
	const char *base;
	struct tipsy_image image = {0};
	uint64_t phdr_bytes;
	int i;
	size_t p;

	if (load_bias == 0) {
		return;
	}
	/* The loader owns the mapping and must keep it live until unregister.
	 * Copy executable PT_LOAD bounds once; diagnostic reads never dereference
	 * guest memory, and must not mistake gaps/data for a caller module. */
	if (memcmp(eh->e_ident, ELFMAG, SELFMAG) != 0 ||
	    eh->e_ident[EI_CLASS] != ELFCLASS64 || eh->e_ident[EI_DATA] != ELFDATA2LSB ||
	    eh->e_phentsize != sizeof(Elf64_Phdr)) {
		return;
	}
	if (eh->e_phnum != 0) {
		phdr_bytes = (uint64_t)eh->e_phnum * sizeof(Elf64_Phdr);
		if (eh->e_phoff > UINTPTR_MAX - load_bias || phdr_bytes > SIZE_MAX) {
			return;
		}
		phdr = malloc((size_t)phdr_bytes);
		if (phdr == NULL ||
		    !tipsy_read_guest((const void *)(load_bias + (uintptr_t)eh->e_phoff),
				      (size_t)phdr_bytes, phdr)) {
			/* Unreadable program headers would also fault later unwind
			 * callbacks through dlpi_phdr, so reject the image. */
			free(phdr);
			return;
		}
	}
	image.addr = load_bias;
	if (name != NULL) {
		strncpy(image.name, name, sizeof(image.name) - 1);
	}
	base = name == NULL ? NULL : strrchr(name, '/');
	base = base == NULL ? name : base + 1;
	image.module_class = base == NULL || *base == '\0' ? TIPSY_BIONIC_SYNC_MODULE_UNKNOWN :
		strcmp(base, "libroblox.so") == 0 ? TIPSY_BIONIC_SYNC_MODULE_ROBLOX : TIPSY_BIONIC_SYNC_MODULE_OTHER;
	if (eh->e_phnum != 0) {
		image.code = calloc(eh->e_phnum, sizeof(*image.code));
	}
	/* image.code is diagnostic only: an allocation failure still registers
	 * the image for unwind. */
	for (p = 0; image.code != NULL && p < eh->e_phnum; p++) {
		uintptr_t start;
		if (phdr[p].p_type != PT_LOAD || !(phdr[p].p_flags & PF_X) || phdr[p].p_memsz == 0 ||
		    phdr[p].p_vaddr > UINTPTR_MAX - load_bias) {
			continue;
		}
		start = load_bias + phdr[p].p_vaddr;
		if (phdr[p].p_memsz > UINTPTR_MAX - start) {
			continue;
		}
		image.code[image.ncode++] = (struct tipsy_code_range){start, start + phdr[p].p_memsz};
	}
	free(phdr);
	pthread_mutex_lock(&g_image_mu);
	for (i = 0; i < g_nimages; i++) {
		if (g_images[i].addr == load_bias) {
			break;
		}
	}
	if (i < TIPSY_MAX_IMAGES) {
		if (i == g_nimages) {
			g_nimages++;
		} else {
			free(g_images[i].code);
		}
		g_images[i] = image;
		atomic_fetch_add_explicit(&g_image_generation, 1, memory_order_release);
	} else {
		free(image.code);
	}
	pthread_mutex_unlock(&g_image_mu);
}

void tipsy_unregister_image(uintptr_t load_bias)
{
	int i;
	pthread_mutex_lock(&g_image_mu);
	for (i = 0; i < g_nimages; i++) {
		if (g_images[i].addr == load_bias) {
			free(g_images[i].code);
			g_images[i] = g_images[--g_nimages];
			memset(&g_images[g_nimages], 0, sizeof(g_images[0]));
			atomic_fetch_add_explicit(&g_image_generation, 1, memory_order_release);
			break;
		}
	}
	pthread_mutex_unlock(&g_image_mu);
}

uint64_t tipsy_image_generation(void)
{
	return atomic_load_explicit(&g_image_generation, memory_order_acquire);
}

/* Only a diagnostic cache miss takes the registry lock. Bounds and generation
 * are copied together, so TLS retains no pointers to replaceable metadata. */
int tipsy_image_code_range(uintptr_t address, uintptr_t *start, uintptr_t *end,
	uint64_t *generation)
{
	int i, module_class = TIPSY_BIONIC_SYNC_MODULE_UNKNOWN;
	size_t p;
	*start = *end = 0;
	pthread_mutex_lock(&g_image_mu);
	*generation = atomic_load_explicit(&g_image_generation, memory_order_relaxed);
	for (i = 0; i < g_nimages; i++) {
		for (p = 0; p < g_images[i].ncode; p++) {
			struct tipsy_code_range range = g_images[i].code[p];
			if (address >= range.start && address < range.end) {
				*start = range.start;
				*end = range.end;
				module_class = g_images[i].module_class;
				goto done;
			}
		}
	}
done:
	pthread_mutex_unlock(&g_image_mu);
	return module_class;
}

static int report_mapped_image(struct tipsy_image *im,
			       int (*callback)(struct dl_phdr_info *, size_t, void *), void *data)
{
	Elf64_Ehdr *eh;
	struct dl_phdr_info info;

	eh = (Elf64_Ehdr *)im->addr;
	if (eh == NULL || memcmp(eh->e_ident, ELFMAG, SELFMAG) != 0 || eh->e_phentsize != sizeof(Elf64_Phdr)) {
		return 0;
	}
	memset(&info, 0, sizeof info);
	info.dlpi_addr = (Elf64_Addr)im->addr;
	info.dlpi_name = im->name;
	info.dlpi_phdr = (const Elf64_Phdr *)(im->addr + eh->e_phoff);
	info.dlpi_phnum = eh->e_phnum;
	return callback(&info, sizeof info, data);
}

int tipsy_dl_iterate_phdr(int (*callback)(struct dl_phdr_info *, size_t, void *), void *data)
{
	struct tipsy_image copy[TIPSY_MAX_IMAGES];
	int n, i, rc;

	if (callback == NULL) {
		return 0;
	}
	pthread_mutex_lock(&g_image_mu);
	n = g_nimages;
	memcpy(copy, g_images, (size_t)n * sizeof copy[0]);
	pthread_mutex_unlock(&g_image_mu);
	for (i = 0; i < n; i++) {
		rc = report_mapped_image(&copy[i], callback, data);
		if (rc != 0) {
			return rc;
		}
	}
	/* Call libc directly. dlsym("dl_iterate_phdr") from the 64 MiB
	 * Main pthread SIGSEGVs inside ld-linux. */
	return dl_iterate_phdr(callback, data);
}

static int count_cb(struct dl_phdr_info *info, size_t size, void *data)
{
	(void)info;
	(void)size;
	++*(int *)data;
	return 0;
}

int tipsy_dl_iterate_count(void)
{
	int n = 0;
	(void)tipsy_dl_iterate_phdr(count_cb, &n);
	return n;
}

