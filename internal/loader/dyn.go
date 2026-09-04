// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"strings"
)

// Android packed-reloc dynamic tags (AOSP / same values as internal/elfinspect).
const (
	dtAndroidRel    elf.DynTag = 0x6000000f
	dtAndroidRelsz  elf.DynTag = 0x60000010
	dtAndroidRela   elf.DynTag = 0x60000011
	dtAndroidRelasz elf.DynTag = 0x60000012

	shtAndroidRel  elf.SectionType = 0x60000001
	shtAndroidRela elf.SectionType = 0x60000002
)

type dynEnt struct {
	Tag elf.DynTag
	Val uint64
}

type dynInfo struct {
	needed []string
	soname string

	symtab  uint64
	strtab  uint64
	strsz   uint64
	syment  uint64
	hash    uint64
	gnuHash uint64

	rela     uint64
	relasz   uint64
	relaent  uint64
	rel      uint64
	relsz    uint64
	relent   uint64
	jmprel   uint64
	pltrelsz uint64
	pltrel   uint64

	androidRela   uint64
	androidRelasz uint64
	androidRel    uint64
	androidRelsz  uint64

	init         uint64
	initArray    uint64
	initArraySz  uint64
	preinitArray uint64
	preinitSz    uint64

	flags  uint64
	flags1 uint64
}

func parseDyn(ents []dynEnt, strtab []byte) (*dynInfo, error) {
	d := &dynInfo{syment: 24, relaent: 24, relent: 16}
	str := func(off uint64) string {
		if off >= uint64(len(strtab)) {
			return ""
		}
		i := off
		for i < uint64(len(strtab)) && strtab[i] != 0 {
			i++
		}
		return string(strtab[off:i])
	}
	for _, e := range ents {
		switch e.Tag {
		case elf.DT_NEEDED:
			if n := str(e.Val); n != "" {
				d.needed = append(d.needed, n)
			}
		case elf.DT_SONAME:
			d.soname = str(e.Val)
		case elf.DT_SYMTAB:
			d.symtab = e.Val
		case elf.DT_STRTAB:
			d.strtab = e.Val
		case elf.DT_STRSZ:
			d.strsz = e.Val
		case elf.DT_SYMENT:
			d.syment = e.Val
		case elf.DT_HASH:
			d.hash = e.Val
		case elf.DT_GNU_HASH:
			d.gnuHash = e.Val
		case elf.DT_RELA:
			d.rela = e.Val
		case elf.DT_RELASZ:
			d.relasz = e.Val
		case elf.DT_RELAENT:
			d.relaent = e.Val
		case elf.DT_REL:
			d.rel = e.Val
		case elf.DT_RELSZ:
			d.relsz = e.Val
		case elf.DT_RELENT:
			d.relent = e.Val
		case elf.DT_JMPREL:
			d.jmprel = e.Val
		case elf.DT_PLTRELSZ:
			d.pltrelsz = e.Val
		case elf.DT_PLTREL:
			d.pltrel = e.Val
		case dtAndroidRela:
			d.androidRela = e.Val
		case dtAndroidRelasz:
			d.androidRelasz = e.Val
		case dtAndroidRel:
			d.androidRel = e.Val
		case dtAndroidRelsz:
			d.androidRelsz = e.Val
		case elf.DT_INIT:
			d.init = e.Val
		case elf.DT_INIT_ARRAY:
			d.initArray = e.Val
		case elf.DT_INIT_ARRAYSZ:
			d.initArraySz = e.Val
		case elf.DT_PREINIT_ARRAY:
			d.preinitArray = e.Val
		case elf.DT_PREINIT_ARRAYSZ:
			d.preinitSz = e.Val
		case elf.DT_FLAGS:
			d.flags = e.Val
		case elf.DT_FLAGS_1:
			d.flags1 = e.Val
		}
	}
	return d, nil
}

func readDynEntries(ef *elf.File) ([]dynEnt, error) {
	var p *elf.Prog
	for _, pr := range ef.Progs {
		if pr.Type == elf.PT_DYNAMIC {
			p = pr
			break
		}
	}
	if p == nil {
		return nil, fmt.Errorf("loader: no PT_DYNAMIC")
	}
	data := make([]byte, p.Filesz)
	n, err := p.ReadAt(data, 0)
	if err != nil && n == 0 {
		return nil, fmt.Errorf("loader: read PT_DYNAMIC: %w", err)
	}
	data = data[:n]
	const ent = 16
	if len(data)%ent != 0 && len(data) >= ent {
		data = data[:len(data)-len(data)%ent]
	}
	var out []dynEnt
	for i := 0; i+ent <= len(data); i += ent {
		tag := elf.DynTag(binary.LittleEndian.Uint64(data[i : i+8]))
		val := binary.LittleEndian.Uint64(data[i+8 : i+16])
		if tag == elf.DT_NULL {
			break
		}
		out = append(out, dynEnt{Tag: tag, Val: val})
	}
	return out, nil
}

func dynVal(ents []dynEnt, tag elf.DynTag) (uint64, bool) {
	for _, e := range ents {
		if e.Tag == tag {
			return e.Val, true
		}
	}
	return 0, false
}

func neededBase(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Bionic / NDK system libraries are Resolver-only even if a file exists next
// to the module (never mmap a host or APK copy of libc.so as the Android DSO).
var systemSonames = map[string]struct{}{
	"libc.so": {}, "libm.so": {}, "libdl.so": {}, "liblog.so": {},
	"libz.so": {}, "libandroid.so": {}, "libEGL.so": {}, "libGLESv1_CM.so": {},
	"libGLESv2.so": {}, "libGLESv3.so": {}, "libvulkan.so": {},
	"libjnigraphics.so": {}, "libmediandk.so": {}, "libOpenMAXAL.so": {},
	"libOpenSLES.so": {}, "libaaudio.so": {}, "libnativewindow.so": {},
	"libc++.so": {}, "libstdc++.so": {},
}
