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
	typeReference = 0x01
	typeAttribute = 0x02
	typeString    = 0x03
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

func align4(n int) int {
	return (n + 3) &^ 3
}
