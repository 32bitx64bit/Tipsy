package apk

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// AOSP resource chunk types (frameworks/base/libs/androidfw/ResourceTypes.h).
const (
	resStringPoolType        = 0x0001
	resXMLType               = 0x0003
	resXMLStartNamespaceType = 0x0100
	resXMLEndNamespaceType   = 0x0101
	resXMLStartElementType   = 0x0102
	resXMLEndElementType     = 0x0103
	resXMLCDATAType          = 0x0104
	resXMLResourceMapType    = 0x0180
)

const (
	stringPoolUTF8Flag = 1 << 8
)

const (
	typeNull      = 0x00
	typeReference = 0x01
	typeAttribute = 0x02
	typeString    = 0x03
	typeFloat     = 0x04
	typeIntDec    = 0x10
	typeIntHex    = 0x11
	typeIntBool   = 0x12
)

const (
	androidNS     = "http://schemas.android.com/apk/res/android"
	stringUnset   = uint32(0xFFFFFFFF)
	chunkHeaderSz = 8
)

// Well-known android: attribute resource IDs from AOSP public.xml.
var androidAttrOrder = []string{
	"name", "label", "debuggable", "versionCode", "versionName",
	"required", "glEsVersion", "value",
}

var androidAttrIDs = map[string]uint32{
	"name":        0x01010003,
	"label":       0x01010001,
	"debuggable":  0x0101000f,
	"versionCode": 0x0101021b,
	"versionName": 0x0101021c,
	"required":    0x0101028e,
	"glEsVersion": 0x01010281,
	"value":       0x01010024,
}

var androidAttrNames = func() map[uint32]string {
	m := make(map[uint32]string, len(androidAttrIDs))
	for name, id := range androidAttrIDs {
		m[id] = name
	}
	return m
}()

type xmlAttr struct {
	Name    string
	Value   string
	Android bool
}

type xmlElem struct {
	Name     string
	Attrs    []xmlAttr
	Children []*xmlElem
}

func (e *xmlElem) attr(name string) string {
	for i := range e.Attrs {
		if e.Attrs[i].Name == name {
			return e.Attrs[i].Value
		}
	}
	return ""
}

func (e *xmlElem) walk(fn func(*xmlElem)) {
	if e == nil {
		return
	}
	fn(e)
	for _, c := range e.Children {
		c.walk(fn)
	}
}

type manifestInfo struct {
	PackageName      string
	VersionName      string
	VersionCode      int64
	SplitName        string
	ApplicationLabel string
	Debuggable       bool
	NativeCode       []string
	UsesFeatures     []string
	LauncherActivity string
	GameActivities   []string
	Activities       []string
}

func parseManifestAXML(data []byte) (*manifestInfo, error) {
	root, err := parseAXML(data)
	if err != nil {
		return nil, err
	}
	return extractManifest(root), nil
}

func extractManifest(root *xmlElem) *manifestInfo {
	info := &manifestInfo{}
	if root == nil {
		return info
	}
	man := root
	if !strings.EqualFold(root.Name, "manifest") {
		man = findChild(root, "manifest")
		if man == nil {
			man = root
		}
	}
	info.PackageName = man.attr("package")
	info.VersionName = man.attr("versionName")
	info.SplitName = man.attr("split")
	if v := man.attr("versionCode"); v != "" {
		if n, err := strconv.ParseInt(v, 0, 64); err == nil {
			info.VersionCode = n
		}
	}
	if nc := man.attr("nativeCode"); nc != "" {
		info.NativeCode = splitList(nc)
	}

	var launcher string
	root.walk(func(e *xmlElem) {
		switch e.Name {
		case "application":
			if info.ApplicationLabel == "" {
				info.ApplicationLabel = e.attr("label")
			}
			if isTruthy(e.attr("debuggable")) {
				info.Debuggable = true
			}
			if nc := e.attr("nativeCode"); nc != "" {
				info.NativeCode = appendUnique(info.NativeCode, splitList(nc)...)
			}
		case "uses-feature":
			if n := e.attr("name"); n != "" {
				info.UsesFeatures = appendUnique(info.UsesFeatures, n)
			}
		case "activity", "activity-alias":
			name := e.attr("name")
			if name == "" {
				return
			}
			info.Activities = appendUnique(info.Activities, name)
			if strings.Contains(name, "GameActivity") {
				info.GameActivities = appendUnique(info.GameActivities, name)
			}
			if isLauncherActivity(e) && launcher == "" {
				launcher = name
			}
		}
	})
	info.LauncherActivity = launcher
	if abi := abiFromSplit(info.SplitName); abi != "" {
		info.NativeCode = appendUnique(info.NativeCode, abi)
	}
	return info
}

func isLauncherActivity(e *xmlElem) bool {
	for _, c := range e.Children {
		if c.Name != "intent-filter" {
			continue
		}
		var main, launcher bool
		for _, ic := range c.Children {
			switch ic.Name {
			case "action":
				if ic.attr("name") == "android.intent.action.MAIN" {
					main = true
				}
			case "category":
				if ic.attr("name") == "android.intent.category.LAUNCHER" {
					launcher = true
				}
			}
		}
		if main && launcher {
			return true
		}
	}
	return false
}

func findChild(e *xmlElem, name string) *xmlElem {
	if e == nil {
		return nil
	}
	for _, c := range e.Children {
		if c.Name == name {
			return c
		}
		if f := findChild(c, name); f != nil {
			return f
		}
	}
	return nil
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' '
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func parseAXML(data []byte) (*xmlElem, error) {
	if len(data) < chunkHeaderSz {
		return nil, fmt.Errorf("axml: too short")
	}
	typ, headerSize, size := readChunkHeader(data, 0)
	if int(size) > len(data) {
		size = uint32(len(data))
	}
	start := 0
	end := int(size)
	if typ == resXMLType {
		if headerSize < chunkHeaderSz || int(headerSize) > end {
			return nil, fmt.Errorf("axml: bad XML header")
		}
		start = int(headerSize)
	} else if typ != resStringPoolType {
		// Some producers omit the outer RES_XML wrapper.
		start = 0
		end = len(data)
	}

	var pool *stringPool
	var resMap []uint32
	var stack []*xmlElem
	var root *xmlElem
	pos := start
	for pos+chunkHeaderSz <= end && pos+chunkHeaderSz <= len(data) {
		pos = align4(pos)
		if pos+chunkHeaderSz > len(data) {
			break
		}
		ctyp, chs, csize := readChunkHeader(data, pos)
		if csize < uint32(chs) || int(csize) < chunkHeaderSz {
			return nil, fmt.Errorf("axml: invalid chunk size at %d", pos)
		}
		if pos+int(csize) > len(data) {
			return nil, fmt.Errorf("axml: chunk overruns buffer at %d", pos)
		}
		chunk := data[pos : pos+int(csize)]
		switch ctyp {
		case resStringPoolType:
			p, err := parseStringPool(chunk)
			if err != nil {
				return nil, err
			}
			pool = p
		case resXMLResourceMapType:
			resMap = parseResourceMap(chunk, chs)
		case resXMLStartElementType:
			elem, err := parseStartElement(chunk, chs, pool, resMap)
			if err != nil {
				return nil, err
			}
			if len(stack) == 0 {
				root = elem
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, elem)
			}
			stack = append(stack, elem)
		case resXMLEndElementType:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case resXMLStartNamespaceType, resXMLEndNamespaceType, resXMLCDATAType:
			// Parsed structurally by walking other chunks; values come from attributes.
		}
		next := pos + int(csize)
		if next <= pos {
			return nil, fmt.Errorf("axml: zero-size chunk at %d", pos)
		}
		pos = next
	}
	if root == nil {
		return nil, fmt.Errorf("axml: no XML elements")
	}
	return root, nil
}

func readChunkHeader(data []byte, off int) (typ uint16, headerSize uint16, size uint32) {
	typ = binary.LittleEndian.Uint16(data[off:])
	headerSize = binary.LittleEndian.Uint16(data[off+2:])
	size = binary.LittleEndian.Uint32(data[off+4:])
	return
}

func parseResourceMap(chunk []byte, headerSize uint16) []uint32 {
	if int(headerSize) > len(chunk) {
		return nil
	}
	body := chunk[headerSize:]
	n := len(body) / 4
	out := make([]uint32, n)
	for i := 0; i < n; i++ {
		out[i] = binary.LittleEndian.Uint32(body[i*4:])
	}
	return out
}

func parseStartElement(chunk []byte, headerSize uint16, pool *stringPool, resMap []uint32) (*xmlElem, error) {
	if int(headerSize) > len(chunk) {
		return nil, fmt.Errorf("axml: start element header too large")
	}
	extOff := int(headerSize)
	if extOff+20 > len(chunk) {
		return nil, fmt.Errorf("axml: start element missing attrExt")
	}
	nsIdx := binary.LittleEndian.Uint32(chunk[extOff:])
	nameIdx := binary.LittleEndian.Uint32(chunk[extOff+4:])
	attrStart := binary.LittleEndian.Uint16(chunk[extOff+8:])
	attrSize := binary.LittleEndian.Uint16(chunk[extOff+10:])
	attrCount := binary.LittleEndian.Uint16(chunk[extOff+12:])
	_ = nsIdx
	if attrSize < 20 {
		attrSize = 20
	}
	elem := &xmlElem{Name: pool.stringAt(nameIdx)}
	attrOff := extOff + int(attrStart)
	for i := 0; i < int(attrCount); i++ {
		off := attrOff + i*int(attrSize)
		if off+20 > len(chunk) {
			break
		}
		attrNS := binary.LittleEndian.Uint32(chunk[off:])
		attrName := binary.LittleEndian.Uint32(chunk[off+4:])
		rawValue := binary.LittleEndian.Uint32(chunk[off+8:])
		// Res_value at off+12: size u16, res0 u8, dataType u8, data u32
		dataType := chunk[off+15]
		data := binary.LittleEndian.Uint32(chunk[off+16:])

		name := pool.stringAt(attrName)
		if attrName != stringUnset && int(attrName) < len(resMap) {
			if known, ok := androidAttrNames[resMap[attrName]]; ok {
				name = known
			} else if name == "" && resMap[attrName] != 0 {
				name = fmt.Sprintf("0x%08x", resMap[attrName])
			}
		}
		if name == "" {
			continue
		}
		val := typedValue(pool, dataType, data, rawValue)
		android := attrNS != stringUnset && pool.stringAt(attrNS) == androidNS
		if !android {
			if attrName != stringUnset && int(attrName) < len(resMap) && resMap[attrName] != 0 {
				android = true
			}
		}
		elem.Attrs = append(elem.Attrs, xmlAttr{Name: name, Value: val, Android: android})
	}
	return elem, nil
}

func typedValue(pool *stringPool, dataType byte, data, raw uint32) string {
	switch dataType {
	case typeString:
		if data != stringUnset {
			return pool.stringAt(data)
		}
		if raw != stringUnset {
			return pool.stringAt(raw)
		}
	case typeIntBool:
		if data != 0 {
			return "true"
		}
		return "false"
	case typeIntDec:
		return strconv.FormatInt(int64(int32(data)), 10)
	case typeIntHex:
		return "0x" + strconv.FormatUint(uint64(data), 16)
	case typeReference, typeAttribute:
		if raw != stringUnset {
			if s := pool.stringAt(raw); s != "" {
				return s
			}
		}
		return fmt.Sprintf("@0x%08x", data)
	default:
		if raw != stringUnset {
			if s := pool.stringAt(raw); s != "" {
				return s
			}
		}
		if dataType >= typeIntDec && dataType <= 0x1f {
			return strconv.FormatInt(int64(int32(data)), 10)
		}
	}
	return ""
}

type stringPool struct {
	strs []string
}

func (p *stringPool) stringAt(idx uint32) string {
	if p == nil || idx == stringUnset || int(idx) >= len(p.strs) {
		return ""
	}
	return p.strs[idx]
}

func parseStringPool(chunk []byte) (*stringPool, error) {
	const poolHeaderSize = 28
	if len(chunk) < poolHeaderSize {
		return nil, fmt.Errorf("axml: string pool too short")
	}
	headerSize := binary.LittleEndian.Uint16(chunk[2:])
	if headerSize < poolHeaderSize || int(headerSize) > len(chunk) {
		return nil, fmt.Errorf("axml: bad string pool header")
	}
	stringCount := binary.LittleEndian.Uint32(chunk[8:])
	styleCount := binary.LittleEndian.Uint32(chunk[12:])
	flags := binary.LittleEndian.Uint32(chunk[16:])
	stringsStart := binary.LittleEndian.Uint32(chunk[20:])
	utf8 := flags&stringPoolUTF8Flag != 0

	offsetStart := int(headerSize)
	need := offsetStart + int(stringCount)*4 + int(styleCount)*4
	if need > len(chunk) {
		return nil, fmt.Errorf("axml: string pool offsets truncated")
	}
	if int(stringsStart) > len(chunk) {
		return nil, fmt.Errorf("axml: string pool data offset truncated")
	}
	pool := &stringPool{strs: make([]string, 0, stringCount)}
	for i := uint32(0); i < stringCount; i++ {
		rel := binary.LittleEndian.Uint32(chunk[offsetStart+int(i)*4:])
		abs := int(stringsStart) + int(rel)
		if abs < 0 || abs >= len(chunk) {
			pool.strs = append(pool.strs, "")
			continue
		}
		var s string
		var err error
		if utf8 {
			s, err = decodeUTF8String(chunk[abs:])
		} else {
			s, err = decodeUTF16String(chunk[abs:])
		}
		if err != nil {
			s = ""
		}
		pool.strs = append(pool.strs, s)
	}
	return pool, nil
}

func decodeUTF16String(b []byte) (string, error) {
	if len(b) < 2 {
		return "", fmt.Errorf("utf16 string truncated")
	}
	u := binary.LittleEndian.Uint16(b[0:2])
	var charCount int
	off := 2
	if u&0x8000 != 0 {
		if len(b) < 4 {
			return "", fmt.Errorf("utf16 long length truncated")
		}
		charCount = int(u&0x7fff)<<16 | int(binary.LittleEndian.Uint16(b[2:4]))
		off = 4
	} else {
		charCount = int(u)
	}
	need := off + charCount*2
	if need > len(b) {
		return "", fmt.Errorf("utf16 data truncated")
	}
	u16 := make([]uint16, charCount)
	for i := 0; i < charCount; i++ {
		u16[i] = binary.LittleEndian.Uint16(b[off+i*2:])
	}
	return string(utf16.Decode(u16)), nil
}

func decodeUTF8String(b []byte) (string, error) {
	// UTF-8 entries: encoded UTF-16 char length, then UTF-8 byte length, then bytes, then 0.
	rest := b
	_, n, err := decodeUTF8Length(rest)
	if err != nil {
		return "", err
	}
	rest = rest[n:]
	byteLen, n, err := decodeUTF8Length(rest)
	if err != nil {
		return "", err
	}
	rest = rest[n:]
	if byteLen > len(rest) {
		return "", fmt.Errorf("utf8 data truncated")
	}
	return string(rest[:byteLen]), nil
}

func decodeUTF8Length(b []byte) (value int, consumed int, err error) {
	if len(b) < 1 {
		return 0, 0, fmt.Errorf("utf8 length truncated")
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, nil
	}
	if len(b) < 2 {
		return 0, 0, fmt.Errorf("utf8 long length truncated")
	}
	return int(b[0]&0x7f)<<8 | int(b[1]), 2, nil
}

func encodeUTF8Length(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	return []byte{byte((n >> 8) | 0x80), byte(n)}
}

func align4(n int) int {
	return (n + 3) &^ 3
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
	// Resource-mapped android attribute names first so the resource map
	// lines up with string-pool indices (AOSP convention).
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
	// ResXMLTree_node: header + lineNumber + comment, then prefix + uri.
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
	// headerSize may be > 8; payload is the rest after the 8-byte chunk header.
	// Callers pass payload as everything after the 8-byte ResChunk_header,
	// and headerSize as the node/pool header size. Pad so the chunk body
	// occupies headerSize-8 bytes before payload if needed.
	bodyNeed := int(headerSize) - chunkHeaderSz
	if bodyNeed < 0 {
		bodyNeed = 0
	}
	if len(payload) < bodyNeed {
		// Should not happen; keep encoder honest.
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
