// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
)

type countingReaderAt struct {
	io.ReaderAt
	reads atomic.Int64
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads.Add(1)
	return r.ReaderAt.ReadAt(p, off)
}

func TestContentDigestReuseIsPerVerification(t *testing.T) {
	archive := zipBytes(t, map[string][]byte{"payload": []byte("original")})
	data, err := insertAPKSigningBlock(archive, sigPair{id: apkSigIDV2, value: []byte("fixture")})
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingReaderAt{ReaderAt: bytes.NewReader(data)}
	reader := &verificationReader{ReaderAt: counting, digests: make(map[crypto.Hash][]byte)}
	first, err := apkContentDigest(context.Background(), reader, int64(len(data)), crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	reads := counting.reads.Load()
	second, err := apkContentDigest(context.Background(), reader, int64(len(data)), crypto.SHA256)
	if err != nil || !bytes.Equal(first, second) || counting.reads.Load() != reads {
		t.Fatalf("same-verification digest not reused: %v", err)
	}
	data[0] ^= 1
	reader = &verificationReader{ReaderAt: counting, digests: make(map[crypto.Hash][]byte)}
	changed, err := apkContentDigest(context.Background(), reader, int64(len(data)), crypto.SHA256)
	if err != nil || bytes.Equal(first, changed) || counting.reads.Load() == reads {
		t.Fatalf("new verification did not hash changed content: %v", err)
	}
}

func TestHashReaderChunksOrderAndCancellation(t *testing.T) {
	data := make([]byte, 3*apkChunkSize+37)
	for i := range data {
		data[i] = byte(i / 251)
	}
	for _, hashID := range []crypto.Hash{crypto.SHA256, crypto.SHA512} {
		var got, want [][]byte
		if err := hashReaderChunks(context.Background(), bytes.NewReader(data), 0, int64(len(data)), hashID, &got); err != nil {
			t.Fatal(err)
		}
		if err := hashBytesChunks(context.Background(), data, hashID, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v: parallel chunk digests differ", hashID)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var got [][]byte
	if err := hashReaderChunks(ctx, bytes.NewReader(data), 0, int64(len(data)), crypto.SHA256, &got); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled digest returned %v", err)
	}
}

// BenchmarkAPKContentDigest measures the APK signing-scheme digest over an
// uncompressed 32 MiB test APK, excluding fixture construction.
func BenchmarkAPKContentDigest(b *testing.B) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "payload.bin", Method: zip.Store})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 32<<20)); err != nil {
		b.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		b.Fatal(err)
	}
	data, err := insertAPKSigningBlock(archive.Bytes(), sigPair{id: apkSigIDV2, value: []byte("benchmark")})
	if err != nil {
		b.Fatal(err)
	}
	reader := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := apkContentDigest(context.Background(), reader, int64(len(data)), crypto.SHA256); err != nil {
			b.Fatal(err)
		}
	}
}
