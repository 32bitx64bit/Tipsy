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

extern void *GoAndroid_dlopen(char *filename, int flags);
extern void *GoAndroid_dlsym(void *handle, char *symbol);
extern int GoAndroid_dlclose(void *handle);
extern char *GoAndroid_dlerror(void);

typedef struct DLHandle {
	uint64_t magic;
	char *soname;
	int refs;
} DLHandle;

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

char *tipsy_dlerror(void)
{
	return GoAndroid_dlerror();
}

int tipsy_dladdr(const void *addr, Dl_info *info)
{
	if (info != NULL) {
		memset(info, 0, sizeof(*info));
	}
	return dladdr(addr, info);
}

#define TIPSY_MAX_IMAGES 16

struct tipsy_image {
	uintptr_t addr;
	char name[256];
};

static struct tipsy_image g_images[TIPSY_MAX_IMAGES];
static int g_nimages;
static pthread_mutex_t g_image_mu = PTHREAD_MUTEX_INITIALIZER;

void tipsy_register_image(uintptr_t load_bias, const char *name)
{
	int i;

	if (load_bias == 0) {
		return;
	}
	pthread_mutex_lock(&g_image_mu);
	for (i = 0; i < g_nimages; i++) {
		if (g_images[i].addr == load_bias) {
			pthread_mutex_unlock(&g_image_mu);
			return;
		}
	}
	if (g_nimages < TIPSY_MAX_IMAGES) {
		g_images[g_nimages].addr = load_bias;
		if (name != NULL) {
			strncpy(g_images[g_nimages].name, name, sizeof(g_images[0].name) - 1);
		}
		g_nimages++;
	}
	pthread_mutex_unlock(&g_image_mu);
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

void *tipsy_dlhandle_new(const char *soname)
{
	DLHandle *h = calloc(1, sizeof(*h));
	if (h == NULL) {
		return NULL;
	}
	h->magic = TIPSY_DLHANDLE_MAGIC;
	h->refs = 1;
	if (soname != NULL) {
		h->soname = strdup(soname);
	}
	return h;
}

const char *tipsy_dlhandle_soname(void *handle)
{
	DLHandle *h = (DLHandle *)handle;
	if (h == NULL || h->magic != TIPSY_DLHANDLE_MAGIC) {
		return NULL;
	}
	return h->soname;
}

int tipsy_dlhandle_valid(void *handle)
{
	DLHandle *h = (DLHandle *)handle;
	return h != NULL && h->magic == TIPSY_DLHANDLE_MAGIC;
}

void tipsy_dlhandle_free(void *handle)
{
	DLHandle *h = (DLHandle *)handle;
	if (h == NULL || h->magic != TIPSY_DLHANDLE_MAGIC) {
		return;
	}
	free(h->soname);
	h->magic = 0;
	free(h);
}
