package elfinspect

import (
	"debug/elf"
	"fmt"
	"io"
)

// Android packed relocation dynamic tags.
// AOSP: DT_ANDROID_REL=0x6000000f, DT_ANDROID_RELSZ=0x60000010,
// DT_ANDROID_RELA=0x60000011, DT_ANDROID_RELASZ=0x60000012.
// Historical notes often pair DT_ANDROID_REL / DT_ANDROID_RELA as 0x6000000f / 0x60000010.
const (
	dtAndroidRel    elf.DynTag = 0x6000000f
	dtAndroidRelsz  elf.DynTag = 0x60000010
	dtAndroidRela   elf.DynTag = 0x60000011
	dtAndroidRelasz elf.DynTag = 0x60000012

	shtAndroidRel  elf.SectionType = 0x60000001
	shtAndroidRela elf.SectionType = 0x60000002
	shtRELR        elf.SectionType = 19

	dtRELR        elf.DynTag = 36
	dtAndroidRELR elf.DynTag = 0x6fffe000
)

func summarizeRelocs(f *elf.File) RelocationSummary {
	sum := RelocationSummary{ByType: map[string]int{}}
	ents := dynEntries(f)
	if hasDynTag(ents, dtAndroidRel) {
		sum.AndroidREL = true
		sum.AndroidPacked = true
	}
	if hasDynTag(ents, dtAndroidRela) {
		sum.AndroidRELA = true
		sum.AndroidPacked = true
	}
	// Historical pairing: 0x60000010 as DT_ANDROID_RELA when 0x60000011 is absent.
	if hasDynTag(ents, dtAndroidRelsz) && !hasDynTag(ents, dtAndroidRela) {
		sum.AndroidPacked = true
	}

	for _, sec := range f.Sections {
		if sec == nil {
			continue
		}
		switch sec.Type {
		case elf.SHT_REL:
			countRelSection(f, sec, false, &sum)
		case elf.SHT_RELA:
			countRelSection(f, sec, true, &sum)
		case shtAndroidRel:
			sum.AndroidREL = true
			sum.AndroidPacked = true
			countPackedSection(sec, "DT_ANDROID_REL", &sum)
		case shtAndroidRela:
			sum.AndroidRELA = true
			sum.AndroidPacked = true
			countPackedSection(sec, "DT_ANDROID_RELA", &sum)
		case shtRELR:
			countRELRSection(f, sec, &sum)
		}
	}

	// Packed relocs may live in a PROGBITS/REL section addressed by DT_ANDROID_*.
	if sum.AndroidREL && sum.ByType["DT_ANDROID_REL"] == 0 {
		if n := packedCountFromDT(f, ents, dtAndroidRel, dtAndroidRelsz); n > 0 {
			sum.ByType["DT_ANDROID_REL"] = n
			sum.Total += n
		}
	}
	if sum.AndroidRELA && sum.ByType["DT_ANDROID_RELA"] == 0 {
		if n := packedCountFromDT(f, ents, dtAndroidRela, dtAndroidRelasz); n > 0 {
			sum.ByType["DT_ANDROID_RELA"] = n
			sum.Total += n
		}
	}

	if len(sum.ByType) == 0 {
		sum.ByType = nil
	}
	return sum
}

func countRelSection(f *elf.File, sec *elf.Section, rela bool, sum *RelocationSummary) {
	data, err := sec.Data()
	if err != nil || len(data) == 0 {
		return
	}
	ent := int(sec.Entsize)
	if ent == 0 {
		if f.Class == elf.ELFCLASS64 {
			if rela {
				ent = 24
			} else {
				ent = 16
			}
		} else {
			if rela {
				ent = 12
			} else {
				ent = 8
			}
		}
	}
	if ent <= 0 || len(data)%ent != 0 {
		return
	}
	infoOff := 4
	infoSize := 4
	if f.Class == elf.ELFCLASS64 {
		infoOff = 8
		infoSize = 8
	}
	if infoOff+infoSize > ent {
		return
	}
	for i := 0; i+ent <= len(data); i += ent {
		var info uint64
		if infoSize == 8 {
			info = f.ByteOrder.Uint64(data[i+infoOff : i+infoOff+8])
		} else {
			info = uint64(f.ByteOrder.Uint32(data[i+infoOff : i+infoOff+4]))
		}
		typ := relocType(f.Class, info)
		name := relocTypeName(f.Machine, typ)
		sum.ByType[name]++
		sum.Total++
	}
}

func relocType(class elf.Class, info uint64) uint32 {
	if class == elf.ELFCLASS32 {
		return uint32(info & 0xff)
	}
	return uint32(info)
}

func relocTypeName(machine elf.Machine, typ uint32) string {
	switch machine {
	case elf.EM_X86_64:
		return elf.R_X86_64(typ).String()
	case elf.EM_386:
		return elf.R_386(typ).String()
	case elf.EM_AARCH64:
		return elf.R_AARCH64(typ).String()
	case elf.EM_ARM:
		return elf.R_ARM(typ).String()
	default:
		return fmt.Sprintf("R_%s_%d", machine.String(), typ)
	}
}

func countPackedSection(sec *elf.Section, key string, sum *RelocationSummary) {
	data, err := sec.Data()
	if err != nil || len(data) == 0 {
		return
	}
	n, ok := parseAPS2Count(data)
	if !ok {
		return
	}
	sum.ByType[key] += n
	sum.Total += n
}

func countRELRSection(f *elf.File, sec *elf.Section, sum *RelocationSummary) {
	data, err := sec.Data()
	if err != nil || len(data) == 0 {
		return
	}
	word := 8
	if f.Class == elf.ELFCLASS32 {
		word = 4
	}
	if word > 0 && len(data)%word == 0 {
		n := len(data) / word
		sum.ByType["SHT_RELR_WORDS"] += n
		sum.Total += n
	}
}

func packedCountFromDT(f *elf.File, ents []dynEnt, addrTag, sizeTag elf.DynTag) int {
	addr, ok := dynVal(ents, addrTag)
	if !ok {
		return 0
	}
	sz, _ := dynVal(ents, sizeTag)
	data := sectionBytesAt(f, addr, sz)
	if len(data) == 0 {
		return 0
	}
	n, ok := parseAPS2Count(data)
	if !ok {
		return 0
	}
	return n
}

func sectionBytesAt(f *elf.File, addr, size uint64) []byte {
	for _, s := range f.Sections {
		if s == nil || s.Type == elf.SHT_NOBITS {
			continue
		}
		if addr >= s.Addr && addr < s.Addr+s.Size {
			data, err := s.Data()
			if err != nil {
				return nil
			}
			off := addr - s.Addr
			if int(off) >= len(data) {
				return nil
			}
			data = data[off:]
			if size > 0 && size < uint64(len(data)) {
				data = data[:size]
			}
			return data
		}
	}
	return nil
}

// parseAPS2Count reads the APS2 packed-reloc header and returns the reloc count.
func parseAPS2Count(data []byte) (int, bool) {
	if len(data) < 6 || string(data[:4]) != "APS2" {
		return 0, false
	}
	val, _, ok := readSLEB128(data[4:])
	if !ok || val < 0 {
		return 0, false
	}
	if val > 1<<31 {
		return 0, false
	}
	return int(val), true
}

func readSLEB128(b []byte) (val int64, n int, ok bool) {
	var shift uint
	for i := 0; i < len(b); i++ {
		c := b[i]
		val |= int64(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			if shift < 64 && c&0x40 != 0 {
				val |= int64(-1) << shift
			}
			return val, i + 1, true
		}
		if shift >= 64 {
			return 0, 0, false
		}
	}
	return 0, 0, false
}

func ioReadAllLimited(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}
