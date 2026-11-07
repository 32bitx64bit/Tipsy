// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later
package apk

import (
	"encoding/asn1"
	"encoding/binary"
	"fmt"
	"io"
)

func encodePKCS7(certDER []byte) ([]byte, error) {
	emptySET := asn1.RawValue{Class: asn1.ClassUniversal, Tag: 17, IsCompound: true}
	type encap struct {
		OID asn1.ObjectIdentifier
	}
	type signedData struct {
		Version          int
		DigestAlgorithms asn1.RawValue
		EncapContentInfo encap
		Certificates     []asn1.RawValue `asn1:"optional,tag:0,set"`
		SignerInfos      asn1.RawValue
	}
	type envelope struct {
		ContentType asn1.ObjectIdentifier
		Content     signedData `asn1:"explicit,tag:0"`
	}
	return asn1.Marshal(envelope{
		ContentType: oidSignedData,
		Content: signedData{
			Version:          1,
			DigestAlgorithms: emptySET,
			EncapContentInfo: encap{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}},
			Certificates:     []asn1.RawValue{{FullBytes: certDER}},
			SignerInfos:      emptySET,
		},
	})
}

func v2SignerBlock(certDER []byte) []byte {
	certs := u32pref(u32pref(certDER))
	digests := u32pref(nil)
	signedData := append(digests, certs...)
	signer := u32pref(signedData)
	return u32pref(signer)
}

func u32pref(b []byte) []byte {
	out := appendU32(nil, uint32(len(b)))
	return append(out, b...)
}

// insertAPKSigningBlock splices APK Signature Scheme ID-value pairs immediately
// before the ZIP central directory. Used by tests to exercise v2/v3 detection
// without apksigner.
func insertAPKSigningBlock(zipBytes []byte, pairs ...sigPair) ([]byte, error) {
	cdOff, err := findCentralDirectoryOffset(bytesReaderAt(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}
	var pairBytes []byte
	for _, p := range pairs {
		pair := appendU32(nil, p.id)
		pair = append(pair, p.value...)
		pairBytes = appendU64(pairBytes, uint64(len(pair)))
		pairBytes = append(pairBytes, pair...)
	}
	blockSize := uint64(len(pairBytes) + 8 + 16)
	var block []byte
	block = appendU64(block, blockSize)
	block = append(block, pairBytes...)
	block = appendU64(block, blockSize)
	block = append(block, []byte(apkSigBlockMagic)...)

	out := make([]byte, 0, len(zipBytes)+len(block))
	out = append(out, zipBytes[:cdOff]...)
	out = append(out, block...)
	out = append(out, zipBytes[cdOff:]...)

	// Patch EOCD CD offset (and ZIP64 if present).
	newCD := uint64(cdOff) + uint64(len(block))
	eocdOff := int64(len(out) - 22)
	size := int64(len(out))
	maxComment := int64(65535)
	readLen := 22 + maxComment
	if readLen > size {
		readLen = size
	}
	bufStart := size - readLen
	buf := out[bufStart:]
	for i := len(buf) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != 0x06054b50 {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(buf[i+20:]))
		if i+22+commentLen != len(buf) {
			continue
		}
		eocdOff = bufStart + int64(i)
		break
	}
	if newCD > 0xffffffff {
		return nil, fmt.Errorf("signing: CD offset exceeds zip32")
	}
	binary.LittleEndian.PutUint32(out[eocdOff+16:], uint32(newCD))
	return out, nil
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
