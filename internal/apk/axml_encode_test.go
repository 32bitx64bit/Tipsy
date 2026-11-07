// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later
package apk

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"unicode/utf16"
)

func encodeUTF8Length(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	return []byte{byte((n >> 8) | 0x80), byte(n)}
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// encodeAXML encodes a minimal binary AndroidManifest tree (AOSP ResXML format).
// utf8 selects the string-pool encoding; both are valid and exercised by tests.
func encodeAXML(root *xmlElem, utf8 bool) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("axml: nil root")
	}
	enc := newAXMLEncoder(utf8)
	enc.collect(root)
	return enc.encode(root)
}

type axmlEncoder struct {
	utf8     bool
	strings  []string
	index    map[string]uint32
	resIDs   []uint32 // resource map parallel to the first N strings
	nsURII   uint32
	nsPrefix uint32
}

func newAXMLEncoder(utf8 bool) *axmlEncoder {
	e := &axmlEncoder{
		utf8:  utf8,
		index: make(map[string]uint32),
	}
	// Resource-mapped android attribute names first so the resource map lines
	// up with string-pool indices.
	for _, name := range androidAttrOrder {
		e.addMapped(name, androidAttrIDs[name])
	}
	e.nsPrefix = e.add("android")
	e.nsURII = e.add(androidNS)
	return e
}

func (e *axmlEncoder) add(s string) uint32 {
	if i, ok := e.index[s]; ok {
		return i
	}
	i := uint32(len(e.strings))
	e.strings = append(e.strings, s)
	e.index[s] = i
	return i
}

func (e *axmlEncoder) addMapped(s string, id uint32) uint32 {
	if i, ok := e.index[s]; ok {
		return i
	}
	i := uint32(len(e.strings))
	e.strings = append(e.strings, s)
	e.index[s] = i
	if int(i) == len(e.resIDs) {
		e.resIDs = append(e.resIDs, id)
	}
	return i
}

func (e *axmlEncoder) collect(elem *xmlElem) {
	e.add(elem.Name)
	for _, a := range elem.Attrs {
		e.add(a.Name)
		if !looksIntAttr(a) {
			e.add(a.Value)
		}
	}
	for _, c := range elem.Children {
		e.collect(c)
	}
}

func looksIntAttr(a xmlAttr) bool {
	if a.Name == "versionCode" || a.Name == "debuggable" || a.Name == "required" {
		return true
	}
	return false
}

func (e *axmlEncoder) encode(root *xmlElem) ([]byte, error) {
	pool := e.encodeStringPool()
	resMap := e.encodeResourceMap()
	var body []byte
	body = append(body, e.encodeNamespace(true)...)
	body = append(body, e.encodeElement(root)...)
	body = append(body, e.encodeNamespace(false)...)

	inner := append(append(pool, resMap...), body...)
	out := make([]byte, 0, chunkHeaderSz+len(inner))
	out = appendChunk(out, resXMLType, chunkHeaderSz, inner)
	return out, nil
}

func (e *axmlEncoder) encodeStringPool() []byte {
	n := len(e.strings)
	offsets := make([]uint32, n)
	var data []byte
	for i, s := range e.strings {
		offsets[i] = uint32(len(data))
		if e.utf8 {
			data = append(data, encodeUTF8PoolString(s)...)
		} else {
			data = append(data, encodeUTF16PoolString(s)...)
		}
	}
	data = pad4(data)
	const headerSize = 28
	offsetsSize := n * 4
	stringsStart := headerSize + offsetsSize
	payload := make([]byte, 0, 20+offsetsSize+len(data))
	payload = appendU32(payload, uint32(n)) // stringCount
	payload = appendU32(payload, 0)         // styleCount
	flags := uint32(0)
	if e.utf8 {
		flags = stringPoolUTF8Flag
	}
	payload = appendU32(payload, flags)
	payload = appendU32(payload, uint32(stringsStart))
	payload = appendU32(payload, 0) // stylesStart
	for _, off := range offsets {
		payload = appendU32(payload, off)
	}
	payload = append(payload, data...)
	return appendChunk(nil, resStringPoolType, headerSize, payload)
}

func (e *axmlEncoder) encodeResourceMap() []byte {
	if len(e.resIDs) == 0 {
		return nil
	}
	payload := make([]byte, 0, len(e.resIDs)*4)
	for _, id := range e.resIDs {
		payload = appendU32(payload, id)
	}
	return appendChunk(nil, resXMLResourceMapType, chunkHeaderSz, payload)
}

func (e *axmlEncoder) encodeNamespace(start bool) []byte {
	typ := uint16(resXMLStartNamespaceType)
	if !start {
		typ = resXMLEndNamespaceType
	}
	payload := make([]byte, 0, 16)
	payload = appendU32(payload, 1) // lineNumber
	payload = appendU32(payload, stringUnset)
	payload = appendU32(payload, e.nsPrefix)
	payload = appendU32(payload, e.nsURII)
	return appendChunk(nil, typ, 16, payload)
}

func (e *axmlEncoder) encodeElement(elem *xmlElem) []byte {
	var out []byte
	out = append(out, e.encodeStartElement(elem)...)
	for _, c := range elem.Children {
		out = append(out, e.encodeElement(c)...)
	}
	out = append(out, e.encodeEndElement(elem)...)
	return out
}

func (e *axmlEncoder) encodeStartElement(elem *xmlElem) []byte {
	attrs := make([]byte, 0, len(elem.Attrs)*20)
	for _, a := range elem.Attrs {
		attrs = append(attrs, e.encodeAttr(a)...)
	}
	nameIdx := e.index[elem.Name]
	ext := make([]byte, 0, 20+len(attrs))
	ext = appendU32(ext, stringUnset) // ns
	ext = appendU32(ext, nameIdx)
	ext = appendU16(ext, 20) // attributeStart
	ext = appendU16(ext, 20) // attributeSize
	ext = appendU16(ext, uint16(len(elem.Attrs)))
	ext = appendU16(ext, 0) // idIndex
	ext = appendU16(ext, 0) // classIndex
	ext = appendU16(ext, 0) // styleIndex
	ext = append(ext, attrs...)

	payload := make([]byte, 0, 8+len(ext))
	payload = appendU32(payload, 1) // line
	payload = appendU32(payload, stringUnset)
	payload = append(payload, ext...)
	return appendChunk(nil, resXMLStartElementType, 16, payload)
}

func (e *axmlEncoder) encodeEndElement(elem *xmlElem) []byte {
	payload := make([]byte, 0, 16)
	payload = appendU32(payload, 1)
	payload = appendU32(payload, stringUnset)
	payload = appendU32(payload, stringUnset) // ns
	payload = appendU32(payload, e.index[elem.Name])
	return appendChunk(nil, resXMLEndElementType, 16, payload)
}

func (e *axmlEncoder) encodeAttr(a xmlAttr) []byte {
	ns := stringUnset
	if a.Android {
		ns = e.nsURII
	}
	nameIdx := e.index[a.Name]
	raw := stringUnset
	dataType := byte(typeString)
	data := uint32(0)
	switch a.Name {
	case "versionCode":
		dataType = typeIntDec
		n, _ := strconv.ParseInt(a.Value, 0, 32)
		data = uint32(n)
	case "debuggable", "required":
		dataType = typeIntBool
		if isTruthy(a.Value) {
			data = 0xFFFFFFFF
		}
	default:
		dataType = typeString
		raw = e.index[a.Value]
		data = raw
	}
	buf := make([]byte, 0, 20)
	buf = appendU32(buf, ns)
	buf = appendU32(buf, nameIdx)
	buf = appendU32(buf, raw)
	buf = appendU16(buf, 8) // Res_value.size
	buf = append(buf, 0)    // res0
	buf = append(buf, dataType)
	buf = appendU32(buf, data)
	return buf
}

func encodeUTF16PoolString(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	n := len(u16)
	var b []byte
	if n >= 0x8000 {
		b = appendU16(b, uint16(0x8000|(n>>16)))
		b = appendU16(b, uint16(n))
	} else {
		b = appendU16(b, uint16(n))
	}
	for _, c := range u16 {
		b = appendU16(b, c)
	}
	b = appendU16(b, 0)
	return b
}

func encodeUTF8PoolString(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	raw := []byte(s)
	var b []byte
	b = append(b, encodeUTF8Length(len(u16))...)
	b = append(b, encodeUTF8Length(len(raw))...)
	b = append(b, raw...)
	b = append(b, 0)
	return b
}

func appendChunk(dst []byte, typ uint16, headerSize uint16, payload []byte) []byte {
	size := align4(int(headerSize) + len(payload))
	start := len(dst)
	dst = appendU16(dst, typ)
	dst = appendU16(dst, headerSize)
	dst = appendU32(dst, uint32(size))
	// Callers pass payload as everything after the 8-byte chunk header, and
	// headerSize as the node/pool header size; pad the body to headerSize-8.
	bodyNeed := int(headerSize) - chunkHeaderSz
	if bodyNeed < 0 {
		bodyNeed = 0
	}
	if len(payload) < bodyNeed {
		pad := make([]byte, bodyNeed-len(payload))
		payload = append(payload, pad...)
	}
	dst = append(dst, payload...)
	for len(dst)-start < size {
		dst = append(dst, 0)
	}
	return dst
}

func appendU16(b []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}
