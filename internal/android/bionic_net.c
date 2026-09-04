/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Bionic getaddrinfo / getnameinfo / gai_strerror. glibc's struct addrinfo
 * swaps ai_addr and ai_canonname vs BSD/Android, and AI_/NI_/EAI_* numbers
 * differ. Forwarding libc.so getaddrinfo to glibc makes Roblox see a NULL
 * sockaddr (it reads glibc ai_canonname as ai_addr) and FLog DnsResolve
 * even when host DNS works. libroblox.so 2.734.917 imports getaddrinfo@LIBC
 * (not android_getaddrinfofornet / netd).
 */
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include "android_bridge.h"

#include <arpa/inet.h>
#include <netdb.h>
#include <netinet/in.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>

/* AOSP bionic/libc/include/netdb.h (API 26 / NDK r28). */
#define BIONIC_AI_PASSIVE 0x00000001
#define BIONIC_AI_CANONNAME 0x00000002
#define BIONIC_AI_NUMERICHOST 0x00000004
#define BIONIC_AI_NUMERICSERV 0x00000008
#define BIONIC_AI_ALL 0x00000100
#define BIONIC_AI_V4MAPPED_CFG 0x00000200
#define BIONIC_AI_ADDRCONFIG 0x00000400
#define BIONIC_AI_V4MAPPED 0x00000800

#define BIONIC_NI_NOFQDN 0x00000001
#define BIONIC_NI_NUMERICHOST 0x00000002
#define BIONIC_NI_NAMEREQD 0x00000004
#define BIONIC_NI_NUMERICSERV 0x00000008
#define BIONIC_NI_DGRAM 0x00000010

#define BIONIC_EAI_ADDRFAMILY 1
#define BIONIC_EAI_AGAIN 2
#define BIONIC_EAI_BADFLAGS 3
#define BIONIC_EAI_FAIL 4
#define BIONIC_EAI_FAMILY 5
#define BIONIC_EAI_MEMORY 6
#define BIONIC_EAI_NODATA 7
#define BIONIC_EAI_NONAME 8
#define BIONIC_EAI_SERVICE 9
#define BIONIC_EAI_SOCKTYPE 10
#define BIONIC_EAI_SYSTEM 11
#define BIONIC_EAI_BADHINTS 12
#define BIONIC_EAI_PROTOCOL 13
#define BIONIC_EAI_OVERFLOW 14

struct bionic_addrinfo {
	int ai_flags;
	int ai_family;
	int ai_socktype;
	int ai_protocol;
	socklen_t ai_addrlen;
	char *ai_canonname;
	struct sockaddr *ai_addr;
	struct bionic_addrinfo *ai_next;
};

static int bionic_ai_flags_to_glibc(int flags)
{
	int out = 0;

	if (flags & BIONIC_AI_PASSIVE) {
		out |= AI_PASSIVE;
	}
	if (flags & BIONIC_AI_CANONNAME) {
		out |= AI_CANONNAME;
	}
	if (flags & BIONIC_AI_NUMERICHOST) {
		out |= AI_NUMERICHOST;
	}
	if (flags & BIONIC_AI_NUMERICSERV) {
		out |= AI_NUMERICSERV;
	}
	if (flags & BIONIC_AI_ALL) {
		out |= AI_ALL;
	}
	if (flags & BIONIC_AI_ADDRCONFIG) {
		out |= AI_ADDRCONFIG;
	}
	if ((flags & BIONIC_AI_V4MAPPED) || (flags & BIONIC_AI_V4MAPPED_CFG)) {
		out |= AI_V4MAPPED;
	}
	return out;
}

static int glibc_ai_flags_to_bionic(int flags)
{
	int out = 0;

	if (flags & AI_PASSIVE) {
		out |= BIONIC_AI_PASSIVE;
	}
	if (flags & AI_CANONNAME) {
		out |= BIONIC_AI_CANONNAME;
	}
	if (flags & AI_NUMERICHOST) {
		out |= BIONIC_AI_NUMERICHOST;
	}
	if (flags & AI_NUMERICSERV) {
		out |= BIONIC_AI_NUMERICSERV;
	}
	if (flags & AI_ALL) {
		out |= BIONIC_AI_ALL;
	}
	if (flags & AI_ADDRCONFIG) {
		out |= BIONIC_AI_ADDRCONFIG;
	}
	if (flags & AI_V4MAPPED) {
		out |= BIONIC_AI_V4MAPPED;
	}
	return out;
}

static int bionic_ni_flags_to_glibc(int flags)
{
	int out = 0;

	if (flags & BIONIC_NI_NOFQDN) {
		out |= NI_NOFQDN;
	}
	if (flags & BIONIC_NI_NUMERICHOST) {
		out |= NI_NUMERICHOST;
	}
	if (flags & BIONIC_NI_NAMEREQD) {
		out |= NI_NAMEREQD;
	}
	if (flags & BIONIC_NI_NUMERICSERV) {
		out |= NI_NUMERICSERV;
	}
	if (flags & BIONIC_NI_DGRAM) {
		out |= NI_DGRAM;
	}
	return out;
}

static int glibc_eai_to_bionic(int err)
{
	if (err == 0) {
		return 0;
	}
	switch (err) {
	case EAI_BADFLAGS:
		return BIONIC_EAI_BADFLAGS;
	case EAI_NONAME:
		return BIONIC_EAI_NONAME;
	case EAI_AGAIN:
		return BIONIC_EAI_AGAIN;
	case EAI_FAIL:
		return BIONIC_EAI_FAIL;
	case EAI_FAMILY:
		return BIONIC_EAI_FAMILY;
	case EAI_SOCKTYPE:
		return BIONIC_EAI_SOCKTYPE;
	case EAI_SERVICE:
		return BIONIC_EAI_SERVICE;
	case EAI_MEMORY:
		return BIONIC_EAI_MEMORY;
	case EAI_SYSTEM:
		return BIONIC_EAI_SYSTEM;
	case EAI_OVERFLOW:
		return BIONIC_EAI_OVERFLOW;
#ifdef EAI_NODATA
	case EAI_NODATA:
		return BIONIC_EAI_NODATA;
#endif
#ifdef EAI_ADDRFAMILY
	case EAI_ADDRFAMILY:
		return BIONIC_EAI_ADDRFAMILY;
#endif
#ifdef EAI_INPROGRESS
	case EAI_INPROGRESS:
		return BIONIC_EAI_AGAIN;
#endif
	default:
		if (err > 0) {
			/* Already a bionic-style code. */
			return err;
		}
		return BIONIC_EAI_FAIL;
	}
}

void tipsy_freeaddrinfo(void *res)
{
	struct bionic_addrinfo *ai = res;
	struct bionic_addrinfo *next;

	while (ai != NULL) {
		next = ai->ai_next;
		free(ai->ai_addr);
		free(ai->ai_canonname);
		free(ai);
		ai = next;
	}
}

static struct bionic_addrinfo *clone_glibc_list(struct addrinfo *src)
{
	struct bionic_addrinfo *head = NULL;
	struct bionic_addrinfo **tail = &head;

	for (; src != NULL; src = src->ai_next) {
		struct bionic_addrinfo *n = calloc(1, sizeof(*n));

		if (n == NULL) {
			tipsy_freeaddrinfo(head);
			return NULL;
		}
		n->ai_flags = glibc_ai_flags_to_bionic(src->ai_flags);
		n->ai_family = src->ai_family;
		n->ai_socktype = src->ai_socktype;
		n->ai_protocol = src->ai_protocol;
		n->ai_addrlen = src->ai_addrlen;
		if (src->ai_addr != NULL && src->ai_addrlen > 0) {
			n->ai_addr = malloc(src->ai_addrlen);
			if (n->ai_addr == NULL) {
				free(n);
				tipsy_freeaddrinfo(head);
				return NULL;
			}
			memcpy(n->ai_addr, src->ai_addr, src->ai_addrlen);
		}
		if (src->ai_canonname != NULL) {
			n->ai_canonname = strdup(src->ai_canonname);
			if (n->ai_canonname == NULL) {
				free(n->ai_addr);
				free(n);
				tipsy_freeaddrinfo(head);
				return NULL;
			}
		}
		*tail = n;
		tail = &n->ai_next;
	}
	return head;
}

int tipsy_getaddrinfo(const char *node, const char *service, const void *hints, void **res)
{
	const struct bionic_addrinfo *bh = hints;
	struct addrinfo ghints;
	struct addrinfo *ghints_ptr = NULL;
	struct addrinfo *gres = NULL;
	struct bionic_addrinfo *bres;
	int rc;

	if (res == NULL) {
		return BIONIC_EAI_BADFLAGS;
	}
	*res = NULL;

	if (bh != NULL) {
		memset(&ghints, 0, sizeof(ghints));
		ghints.ai_family = bh->ai_family;
		ghints.ai_socktype = bh->ai_socktype;
		ghints.ai_protocol = bh->ai_protocol;
		ghints.ai_flags = bionic_ai_flags_to_glibc(bh->ai_flags);
		ghints_ptr = &ghints;
	}

	rc = getaddrinfo(node, service, ghints_ptr, &gres);
	if (rc != 0) {
		return glibc_eai_to_bionic(rc);
	}

	bres = clone_glibc_list(gres);
	freeaddrinfo(gres);
	if (bres == NULL) {
		return BIONIC_EAI_MEMORY;
	}
	*res = bres;
	return 0;
}

int tipsy_getnameinfo(const void *sa, uint32_t salen, char *host, size_t hostlen, char *serv, size_t servlen, int flags)
{
	int rc;

	if (sa == NULL) {
		return BIONIC_EAI_FAMILY;
	}
	rc = getnameinfo((const struct sockaddr *)sa, (socklen_t)salen, host, hostlen, serv, servlen,
			 bionic_ni_flags_to_glibc(flags));
	return glibc_eai_to_bionic(rc);
}

const char *tipsy_gai_strerror(int errcode)
{
	static const char *const msgs[] = {
		"Success",
		"Address family for hostname not supported",
		"Temporary failure in name resolution",
		"Invalid value for ai_flags",
		"Non-recoverable failure in name resolution",
		"ai_family not supported",
		"Memory allocation failure",
		"No address associated with hostname",
		"hostname nor servname provided, or not known",
		"servname not supported for ai_socktype",
		"ai_socktype not supported",
		"System error returned in errno",
		"Invalid value for hints",
		"Resolved protocol is unknown",
		"Argument buffer overflow",
	};

	if (errcode < 0 || errcode >= (int)(sizeof(msgs) / sizeof(msgs[0]))) {
		return "Unknown error";
	}
	return msgs[errcode];
}

int tipsy_test_getaddrinfo_numeric_loopback(void)
{
	struct bionic_addrinfo hints;
	struct bionic_addrinfo *res = NULL;
	struct sockaddr_in *in;
	int rc;

	memset(&hints, 0, sizeof(hints));
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	hints.ai_flags = BIONIC_AI_NUMERICHOST;

	rc = tipsy_getaddrinfo("127.0.0.1", "443", &hints, (void **)&res);
	if (rc != 0 || res == NULL || res->ai_addr == NULL) {
		return -1;
	}
	if (res->ai_family != AF_INET || res->ai_addrlen < (socklen_t)sizeof(*in)) {
		tipsy_freeaddrinfo(res);
		return -2;
	}
	in = (struct sockaddr_in *)res->ai_addr;
	if (in->sin_family != AF_INET) {
		tipsy_freeaddrinfo(res);
		return -3;
	}
	if (ntohl(in->sin_addr.s_addr) != 0x7f000001u) {
		tipsy_freeaddrinfo(res);
		return -4;
	}
	if (ntohs(in->sin_port) != 443) {
		tipsy_freeaddrinfo(res);
		return -5;
	}
	tipsy_freeaddrinfo(res);
	return 0;
}

int tipsy_test_getaddrinfo_bionic_addrconfig(void)
{
	struct bionic_addrinfo hints;
	struct bionic_addrinfo *res = NULL;
	int rc;

	memset(&hints, 0, sizeof(hints));
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	/* bionic AI_ADDRCONFIG is 0x400; untranslated glibc reads AI_NUMERICSERV. */
	hints.ai_flags = BIONIC_AI_ADDRCONFIG;

	rc = tipsy_getaddrinfo("127.0.0.1", "http", &hints, (void **)&res);
	if (rc != 0) {
		return rc;
	}
	if (res == NULL || res->ai_addr == NULL) {
		return -100;
	}
	tipsy_freeaddrinfo(res);
	return 0;
}

int tipsy_test_glibc_untranslated_addrconfig(void)
{
	struct addrinfo hints;
	struct addrinfo *res = NULL;
	int rc;

	memset(&hints, 0, sizeof(hints));
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	hints.ai_flags = BIONIC_AI_ADDRCONFIG;

	rc = getaddrinfo("127.0.0.1", "http", &hints, &res);
	if (res != NULL) {
		freeaddrinfo(res);
	}
	return rc;
}

int tipsy_test_getaddrinfo_eai_noname(void)
{
	struct bionic_addrinfo hints;
	struct bionic_addrinfo *res = NULL;
	int rc;

	memset(&hints, 0, sizeof(hints));
	hints.ai_family = AF_UNSPEC;
	hints.ai_socktype = SOCK_STREAM;
	hints.ai_flags = BIONIC_AI_NUMERICHOST;

	rc = tipsy_getaddrinfo("not-an-ip.invalid", "443", &hints, (void **)&res);
	if (res != NULL) {
		tipsy_freeaddrinfo(res);
	}
	return rc;
}

int tipsy_test_getnameinfo_numeric(void)
{
	struct sockaddr_in in;
	char host[64];
	char serv[16];
	int rc;

	memset(&in, 0, sizeof(in));
	in.sin_family = AF_INET;
	in.sin_port = htons(443);
	in.sin_addr.s_addr = htonl(0x7f000001u);
	host[0] = '\0';
	serv[0] = '\0';

	rc = tipsy_getnameinfo(&in, sizeof(in), host, sizeof(host), serv, sizeof(serv),
			       BIONIC_NI_NUMERICHOST | BIONIC_NI_NUMERICSERV);
	if (rc != 0) {
		return rc;
	}
	if (strcmp(host, "127.0.0.1") != 0) {
		return -1;
	}
	if (strcmp(serv, "443") != 0) {
		return -2;
	}
	return 0;
}

/* 1 if glibc addrinfo read with bionic field order has a NULL ai_addr
 * (the DnsResolve miss). 0 if this libc already matches bionic. */
int tipsy_test_glibc_bionic_ai_addr_null(void)
{
	struct addrinfo hints;
	struct addrinfo *res = NULL;
	const struct bionic_addrinfo *as_bionic;
	int rc;

	memset(&hints, 0, sizeof(hints));
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	hints.ai_flags = AI_NUMERICHOST;

	rc = getaddrinfo("127.0.0.1", "443", &hints, &res);
	if (rc != 0 || res == NULL) {
		return -1;
	}
	as_bionic = (const struct bionic_addrinfo *)res;
	rc = (as_bionic->ai_addr == NULL) ? 1 : 0;
	freeaddrinfo(res);
	return rc;
}
