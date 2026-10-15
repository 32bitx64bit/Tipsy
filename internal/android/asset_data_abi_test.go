// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/loader"
	"golang.org/x/sys/unix"
)

const assetModeBuffer = 3

func dataABISymbol(t *testing.T, library, name string) uintptr {
	t.Helper()
	address, err := Provider().Lookup(library, name)
	if err != nil || address == 0 {
		t.Fatalf("Lookup %s:%s = %#x, %v", library, name, address, err)
	}
	return address
}

func callDataABI(t *testing.T, library, name string, args ...uintptr) int64 {
	t.Helper()
	if len(args) > 8 {
		t.Fatalf("%s has %d test arguments", name, len(args))
	}
	var a [8]uintptr
	copy(a[:], args)
	return loader.CallP8(dataABISymbol(t, library, name), a[0], a[1], a[2], a[3], a[4], a[5], a[6], a[7])
}

func binaryMeshFixture(size int) []byte {
	data := make([]byte, size)
	x := uint32(0x91e10da5)
	for i := range data {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		data[i] = byte(x >> 11)
	}
	copy(data, []byte("TIPSY-MESH-DATA\x00\xff"))
	copy(data[len(data)-16:], []byte("END-MESH\x00\x01\x80\xff\x00\x7f"))
	return data
}

func TestAssetBinaryDataABI(t *testing.T) {
	want := binaryMeshFixture(1<<20 + 37)

	t.Run("deflated APK asset", func(t *testing.T) {
		apk := writeTestAPK(t, t.TempDir(), map[string][]byte{"assets/mesh.bin": want})
		setAssetsLocked("", apk)
		t.Cleanup(func() { setAssetsLocked("", "") })
		assertAssetDataABI(t, want)
	})

	t.Run("directory mapped asset", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "mesh.bin"), want, 0o600); err != nil {
			t.Fatal(err)
		}
		setAssetsLocked(dir, "")
		t.Cleanup(func() { setAssetsLocked("", "") })
		assertAssetDataABI(t, want)
	})
}

func assertAssetDataABI(t *testing.T, want []byte) {
	t.Helper()
	name := mmapCString(t, "mesh.bin")
	asset := uintptr(uint64(callDataABI(t, "libandroid.so", "AAssetManager_open", 0, name, assetModeBuffer)))
	if asset == 0 {
		t.Fatal("AAssetManager_open returned NULL")
	}
	t.Cleanup(func() { callDataABI(t, "libandroid.so", "AAsset_close", asset) })

	length := callDataABI(t, "libandroid.so", "AAsset_getLength", asset)
	if length != int64(len(want)) {
		t.Fatalf("AAsset_getLength = %d, want %d", length, len(want))
	}
	buffer := uintptr(uint64(callDataABI(t, "libandroid.so", "AAsset_getBuffer", asset)))
	if buffer == 0 {
		t.Fatal("AAsset_getBuffer returned NULL")
	}
	view := unsafe.Slice((*byte)(unsafe.Pointer(buffer)), len(want))
	if !bytes.Equal(view, want) {
		t.Fatal("AAsset_getBuffer differs from source bytes")
	}

	chunk, chunkPtr := mmapBuffer(t, 13*1024+19)
	position := 0
	for position < len(want) {
		count := len(chunk)
		if count > len(want)-position {
			count = len(want) - position
		}
		read := int32(callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, uintptr(count)))
		if read != int32(count) || !bytes.Equal(chunk[:count], want[position:position+count]) {
			t.Fatalf("AAsset_read at %d returned %d bytes or changed payload", position, read)
		}
		position += count
	}
	if read := callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, uintptr(len(chunk))); read != 0 {
		t.Fatalf("AAsset_read at EOF = %d, want 0", read)
	}

	if got := callDataABI(t, "libandroid.so", "AAsset_seek", asset, 97, 0); got != 97 {
		t.Fatalf("AAsset_seek(97, SEEK_SET) = %d", got)
	}
	if got := callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, 0); got != 0 {
		t.Fatalf("AAsset_read(count=0) = %d, want 0", got)
	}
	if got := int32(callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, 31)); got != 31 || !bytes.Equal(chunk[:31], want[97:128]) {
		t.Fatalf("AAsset_read after SEEK_SET returned %d or wrong offset bytes", got)
	}
	curOffset := int64(-60)
	if got := callDataABI(t, "libandroid.so", "AAsset_seek", asset, uintptr(curOffset), 1); got != 68 {
		t.Fatalf("AAsset_seek(-60, SEEK_CUR) = %d, want 68", got)
	}
	if got := int32(callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, 29)); got != 29 || !bytes.Equal(chunk[:29], want[68:97]) {
		t.Fatalf("AAsset_read after negative SEEK_CUR returned %d or wrong offset bytes", got)
	}
	endOffset := int64(-41)
	if got := callDataABI(t, "libandroid.so", "AAsset_seek", asset, uintptr(endOffset), 2); got != int64(len(want)-41) {
		t.Fatalf("AAsset_seek(-41, SEEK_END) = %d", got)
	}
	if got := int32(callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, uintptr(len(chunk)))); got != 41 || !bytes.Equal(chunk[:41], want[len(want)-41:]) {
		t.Fatalf("AAsset_read at final 41 bytes returned %d or wrong payload", got)
	}
	if got := callDataABI(t, "libandroid.so", "AAsset_seek", asset, uintptr(int64(len(want))), 0); got != int64(len(want)) {
		t.Fatalf("AAsset_seek(length, SEEK_SET) = %d", got)
	}
	if got := callDataABI(t, "libandroid.so", "AAsset_seek", asset, uintptr(int64(len(want)+1)), 0); got != -1 {
		t.Fatalf("out-of-range AAsset_seek = %d, want -1", got)
	}
	if got := callDataABI(t, "libandroid.so", "AAsset_read", asset, chunkPtr, 1); got != 0 {
		t.Fatalf("failed seek changed EOF position: read = %d, want 0", got)
	}

	metadata, metadataPtr := mmapBuffer(t, 2*8)
	fd := int32(callDataABI(t, "libandroid.so", "AAsset_openFileDescriptor", asset, metadataPtr, uintptr(unsafe.Pointer(&metadata[8]))))
	if fd < 0 {
		t.Fatal("AAsset_openFileDescriptor failed")
	}
	t.Cleanup(func() { _ = unix.Close(int(fd)) })
	if start, size := binary.LittleEndian.Uint64(metadata[:8]), binary.LittleEndian.Uint64(metadata[8:]); start != 0 || size != uint64(len(want)) {
		t.Fatalf("AAsset_openFileDescriptor start/length = %d/%d, want 0/%d", start, size, len(want))
	}
	assertResolvedFileDataABI(t, int(fd), want)
}

func assertResolvedFileDataABI(t *testing.T, fd int, want []byte) {
	t.Helper()
	for _, name := range []string{"read", "pread", "pread64", "readv", "lseek", "lseek64", "stat", "stat64", "fstat", "fstat64", "mmap", "mmap64", "munmap"} {
		resolved := dataABISymbol(t, "libc.so", name)
		host := hostDlsym(name)
		if host == 0 || resolved != host {
			t.Fatalf("libc %s resolved to %#x, host dlsym gives %#x", name, resolved, host)
		}
	}

	statSize := int(unsafe.Sizeof(unix.Stat_t{}))
	statBuffer, statPointer := mmapBuffer(t, statSize+16)
	for i := statSize; i < len(statBuffer); i++ {
		statBuffer[i] = 0xa5
	}
	var expected unix.Stat_t
	if err := unix.Fstat(fd, &expected); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fstat", "fstat64"} {
		clear(statBuffer[:statSize])
		if rc := callDataABI(t, "libc.so", name, uintptr(fd), statPointer); rc != 0 {
			t.Fatalf("%s returned %d", name, rc)
		}
		got := *(*unix.Stat_t)(unsafe.Pointer(&statBuffer[0]))
		if got.Size != expected.Size || got.Mode != expected.Mode || got.Ino != expected.Ino {
			t.Fatalf("%s metadata size/mode/inode = %d/%o/%d, want %d/%o/%d", name, got.Size, got.Mode, got.Ino, expected.Size, expected.Mode, expected.Ino)
		}
		for i, b := range statBuffer[statSize:] {
			if b != 0xa5 {
				t.Fatalf("%s wrote beyond the %d-byte stat buffer at %d", name, statSize, i)
			}
		}
	}

	readBuffer, readPointer := mmapBuffer(t, 4099)
	if got := callDataABI(t, "libc.so", "lseek", uintptr(fd), 0, 0); got != 0 {
		t.Fatalf("lseek(SEEK_SET) = %d", got)
	}
	if got := callDataABI(t, "libc.so", "read", uintptr(fd), readPointer, uintptr(len(readBuffer))); got != int64(len(readBuffer)) || !bytes.Equal(readBuffer, want[:len(readBuffer)]) {
		t.Fatal("resolved read changed the leading payload bytes or count")
	}
	current := callDataABI(t, "libc.so", "lseek64", uintptr(fd), 0, 1)
	if got := callDataABI(t, "libc.so", "pread", uintptr(fd), readPointer, 257, uintptr(12345)); got != 257 || !bytes.Equal(readBuffer[:257], want[12345:12345+257]) {
		t.Fatal("resolved pread changed offset bytes or count")
	}
	if got := callDataABI(t, "libc.so", "lseek", uintptr(fd), 0, 1); got != current {
		t.Fatalf("pread changed the shared file position: before=%d after=%d", current, got)
	}
	if got := callDataABI(t, "libc.so", "pread64", uintptr(fd), readPointer, 311, uintptr(70001)); got != 311 || !bytes.Equal(readBuffer[:311], want[70001:70001+311]) {
		t.Fatal("resolved pread64 changed offset bytes or count")
	}

	first, firstPointer := mmapBuffer(t, 111)
	second, secondPointer := mmapBuffer(t, 173)
	iovecs, iovecsPointer := mmapBuffer(t, 2*16)
	binary.LittleEndian.PutUint64(iovecs[:8], uint64(firstPointer))
	binary.LittleEndian.PutUint64(iovecs[8:16], uint64(len(first)))
	binary.LittleEndian.PutUint64(iovecs[16:24], uint64(secondPointer))
	binary.LittleEndian.PutUint64(iovecs[24:32], uint64(len(second)))
	const vectorOffset = 3333
	if got := callDataABI(t, "libc.so", "lseek64", uintptr(fd), uintptr(vectorOffset), 0); got != vectorOffset {
		t.Fatalf("lseek64(%d) = %d", vectorOffset, got)
	}
	if got := callDataABI(t, "libc.so", "readv", uintptr(fd), iovecsPointer, 2); got != int64(len(first)+len(second)) {
		t.Fatalf("readv count = %d", got)
	}
	if !bytes.Equal(first, want[vectorOffset:vectorOffset+int64(len(first))]) || !bytes.Equal(second, want[vectorOffset+int64(len(first)):vectorOffset+int64(len(first)+len(second))]) {
		t.Fatal("resolved readv changed vector boundaries or payload bytes")
	}

	mapping := callDataABI(t, "libc.so", "mmap", 0, uintptr(len(want)), uintptr(unix.PROT_READ), uintptr(unix.MAP_SHARED), uintptr(fd), 0)
	if mapping == -1 {
		t.Fatal("resolved mmap failed")
	}
	mappedPointer := uintptr(mapping)
	mappingLive := true
	t.Cleanup(func() {
		if mappingLive {
			_ = unix.Munmap(unsafe.Slice((*byte)(unsafe.Pointer(mappedPointer)), len(want)))
		}
	})
	if !bytes.Equal(unsafe.Slice((*byte)(unsafe.Pointer(mappedPointer)), len(want)), want) {
		t.Fatal("resolved mmap differs from AAsset bytes")
	}
	if rc := callDataABI(t, "libc.so", "munmap", mappedPointer, uintptr(len(want))); rc != 0 {
		t.Fatalf("resolved munmap returned %d", rc)
	}
	mappingLive = false
}

func TestResolvedLibcLargeFileOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sparse-mesh.bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	page := int64(unix.Getpagesize())
	offset := int64(1)<<32 + 3*page
	want := binaryMeshFixture(257)
	if err := f.Truncate(offset + 2*page); err != nil {
		t.Skipf("filesystem does not support sparse >4 GiB fixture: %v", err)
	}
	if n, err := f.WriteAt(want, offset); err != nil || n != len(want) {
		t.Fatalf("write sparse fixture n=%d err=%v", n, err)
	}
	fd := int(f.Fd())
	statBuffer, statPointer := mmapBuffer(t, int(unsafe.Sizeof(unix.Stat_t{}))+16)
	if rc := callDataABI(t, "libc.so", "fstat64", uintptr(fd), statPointer); rc != 0 {
		t.Fatalf("fstat64 sparse fixture = %d", rc)
	}
	gotStat := *(*unix.Stat_t)(unsafe.Pointer(&statBuffer[0]))
	if gotStat.Size != offset+2*page {
		t.Fatalf("fstat64 size = %d, want %d", gotStat.Size, offset+2*page)
	}
	assertResolvedStatABI(t, path, offset+2*page)

	for _, name := range []string{"lseek", "lseek64"} {
		if got := callDataABI(t, "libc.so", name, uintptr(fd), uintptr(offset), 0); got != offset {
			t.Fatalf("%s high offset = %d, want %d", name, got, offset)
		}
	}
	readBuffer, readPointer := mmapBuffer(t, len(want))
	for _, name := range []string{"pread", "pread64"} {
		if got := callDataABI(t, "libc.so", name, uintptr(fd), readPointer, uintptr(len(want)), uintptr(offset)); got != int64(len(want)) || !bytes.Equal(readBuffer, want) {
			t.Fatalf("%s high offset count=%d, bytes match=%v", name, got, bytes.Equal(readBuffer, want))
		}
	}

	mapping := callDataABI(t, "libc.so", "mmap64", 0, uintptr(page), uintptr(unix.PROT_READ), uintptr(unix.MAP_SHARED), uintptr(fd), uintptr(offset))
	if mapping == -1 {
		t.Fatal("mmap64 high offset failed")
	}
	mappedPointer := uintptr(mapping)
	mappingLive := true
	t.Cleanup(func() {
		if mappingLive {
			_ = unix.Munmap(unsafe.Slice((*byte)(unsafe.Pointer(mappedPointer)), int(page)))
		}
	})
	if !bytes.Equal(unsafe.Slice((*byte)(unsafe.Pointer(mappedPointer)), len(want)), want) {
		t.Fatal("mmap64 high offset differs from source bytes")
	}
	if rc := callDataABI(t, "libc.so", "munmap", mappedPointer, uintptr(page)); rc != 0 {
		t.Fatalf("munmap high-offset mapping = %d", rc)
	}
	mappingLive = false
}

func assertResolvedStatABI(t *testing.T, path string, wantSize int64) {
	t.Helper()
	pathPointer := mmapCString(t, path)
	statSize := int(unsafe.Sizeof(unix.Stat_t{}))
	statBuffer, statPointer := mmapBuffer(t, statSize+16)
	for i := statSize; i < len(statBuffer); i++ {
		statBuffer[i] = 0xa5
	}
	for _, name := range []string{"stat", "stat64"} {
		clear(statBuffer[:statSize])
		if rc := callDataABI(t, "libc.so", name, pathPointer, statPointer); rc != 0 {
			t.Fatalf("%s returned %d", name, rc)
		}
		got := *(*unix.Stat_t)(unsafe.Pointer(&statBuffer[0]))
		if got.Size != wantSize {
			t.Fatalf("%s size = %d, want %d", name, got.Size, wantSize)
		}
		for i, b := range statBuffer[statSize:] {
			if b != 0xa5 {
				t.Fatalf("%s wrote beyond the %d-byte stat buffer at %d", name, statSize, i)
			}
		}
	}
}
