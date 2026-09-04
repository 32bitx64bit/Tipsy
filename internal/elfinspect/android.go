package elfinspect

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"strings"
)

// Android NDK / graphics / audio libraries we always call out when DT_NEEDED.
var androidNeededHighlight = []string{
	"libandroid.so",
	"liblog.so",
	"libjnigraphics.so",
	"libEGL.so",
	"libGLESv1_CM.so",
	"libGLESv2.so",
	"libGLESv3.so",
	"libvulkan.so",
	"libOpenSLES.so",
	"libaaudio.so",
	"libnativewindow.so",
	"libc.so",
	"libm.so",
	"libdl.so",
}

func collectAndroidNotes(f *elf.File, rep *Report) []string {
	var notes []string
	if n := archNote(rep.Class, rep.Machine); n != "" {
		notes = append(notes, n)
	}
	notes = append(notes, androidIdentNotes(f)...)

	ents := dynEntries(f)
	if hasDynTag(ents, dtAndroidRel) {
		notes = append(notes, "DT_ANDROID_REL (0x6000000f packed relocations)")
	}
	if hasDynTag(ents, dtAndroidRelsz) {
		// AOSP: size of DT_ANDROID_REL. Historical tooling treated this as DT_ANDROID_RELA.
		if hasDynTag(ents, dtAndroidRela) {
			notes = append(notes, "DT_ANDROID_RELSZ (0x60000010)")
		} else {
			notes = append(notes, "DT_ANDROID_RELSZ/historical DT_ANDROID_RELA (0x60000010)")
		}
	}
	if hasDynTag(ents, dtAndroidRela) {
		notes = append(notes, "DT_ANDROID_RELA (0x60000011 packed relocations)")
	}
	if hasDynTag(ents, dtAndroidRelasz) {
		notes = append(notes, "DT_ANDROID_RELASZ (0x60000012)")
	}
	if hasDynTag(ents, dtRELR) || hasDynTag(ents, dtAndroidRELR) {
		notes = append(notes, "DT_RELR (relative relocations)")
	}

	for _, s := range f.Sections {
		if s == nil {
			continue
		}
		switch {
		case s.Name == ".note.android.ident":
			// already covered via note contents
		case s.Type == shtAndroidRel:
			notes = append(notes, "SHT_ANDROID_REL (0x60000001)")
		case s.Type == shtAndroidRela:
			notes = append(notes, "SHT_ANDROID_RELA (0x60000002)")
		case s.Type == shtRELR:
			notes = append(notes, "SHT_RELR")
		}
	}

	seenLib := map[string]struct{}{}
	for _, n := range rep.DTNeeded {
		base := neededBase(n)
		if _, ok := seenLib[base]; ok {
			continue
		}
		seenLib[base] = struct{}{}
		switch base {
		case "libc.so":
			notes = append(notes, "needed libc.so (bionic)")
		case "libc.so.6":
			notes = append(notes, "needed libc.so.6 (glibc, not bionic libc.so)")
		default:
			for _, want := range androidNeededHighlight {
				if base == want {
					notes = append(notes, "needed "+base)
					break
				}
			}
		}
	}

	if rep.ExportsTruncated {
		notes = append(notes, fmt.Sprintf("export listing truncated (%d listed of %d defined dynsym; JNI_OnLoad/Java_* kept)", len(rep.Exports), rep.ExportTotal))
	}
	return uniqueStrings(notes)
}

func archNote(class, machine string) string {
	if class == "ELF64" && machine == "EM_X86_64" {
		return ""
	}
	return fmt.Sprintf("UNSUPPORTED_ARCH: %s %s (Tipsy requires ELF64 EM_X86_64)", class, machine)
}

func androidIdentNotes(f *elf.File) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	if sec := f.Section(".note.android.ident"); sec != nil {
		if data, err := sec.Data(); err == nil {
			for _, n := range parseNotes(data, f.ByteOrder) {
				add(formatAndroidNote(n, f.ByteOrder))
			}
		}
	}
	for _, p := range f.Progs {
		if p.Type != elf.PT_NOTE {
			continue
		}
		data, err := ioReadAllLimited(p.Open(), 1<<20)
		if err != nil {
			continue
		}
		for _, n := range parseNotes(data, f.ByteOrder) {
			if n.Name == "Android" {
				add(formatAndroidNote(n, f.ByteOrder))
			}
		}
	}
	return out
}

func formatAndroidNote(n elfNote, order binary.ByteOrder) string {
	if n.Name != "Android" && n.Name != "android" {
		if n.Name != "" {
			return ""
		}
	}
	label := ".note.android.ident"
	switch n.Type {
	case 1:
		// NT_ANDROID_TYPE_IDENT: ABI API level, optional NDK string.
		if len(n.Desc) >= 4 {
			api := order.Uint32(n.Desc[:4])
			label = fmt.Sprintf(".note.android.ident API=%d", api)
			if rest := firstNULFields(n.Desc[4:]); rest != "" {
				label += " NDK=" + rest
			}
		}
	case 4:
		label = ".note.android.ident NT_ANDROID_TYPE_MEMTAG"
	default:
		if n.Name == "Android" || n.Name == "android" {
			label = fmt.Sprintf(".note.android.ident type=%d", n.Type)
		} else {
			return ""
		}
	}
	return label
}

// firstNULFields joins non-empty NUL-separated fields (NDK name, then optional
// version) so padded .note.android.ident blobs do not leak NULs into reports.
func firstNULFields(b []byte) string {
	parts := strings.Split(string(b), "\x00")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
