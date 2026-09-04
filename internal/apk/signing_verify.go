// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package apk

import (
	"bytes"
	"context"
	"crypto"
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math/big"
	"sort"
)

const (
	sigRSAPSSSHA256   = 0x0101
	sigRSAPSSSHA512   = 0x0102
	sigRSAPKCS1SHA256 = 0x0103
	sigRSAPKCS1SHA512 = 0x0104
	sigECDSASHA256    = 0x0201
	sigECDSASHA512    = 0x0202
	sigDSASHA256      = 0x0301
	apkChunkSize      = 1 << 20
)

type signatureRecord struct {
	algorithm uint32
	value     []byte
}

type digestRecord struct {
	algorithm uint32
	value     []byte
}

// VerifyReportSignatures cryptographically verifies each APK's v2 content
// signature and records only the verified leaf signing identities.
func VerifyReportSignatures(ctx context.Context, rep *Report) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if rep == nil || len(rep.Packages) == 0 {
		return fmt.Errorf("apk: no packages to verify")
	}
	for i := range rep.Packages {
		if err := ctx.Err(); err != nil {
			return err
		}
		src, err := openSource(rep.Packages[i].Path)
		if err != nil {
			return fmt.Errorf("apk: open package for signature verification: %w", err)
		}
		certs, verifyErr := verifySourceV2(ctx, src.Reader, src.Size)
		_ = src.Close()
		if verifyErr != nil {
			return fmt.Errorf("apk: verify v2 signature: %w", verifyErr)
		}
		rep.Packages[i].Signing.CryptographicallyValid = true
		rep.Packages[i].Signing.VerifiedCertSHA256 = certs
	}
	rep.Merged = mergePackages(rep.Packages)
	return nil
}

func verifySourceV2(ctx context.Context, ra io.ReaderAt, size int64) ([]string, error) {
	pairs, _, err := readAPKSigningBlock(ra, size)
	if err != nil {
		return nil, err
	}
	var v2 []byte
	for _, pair := range pairs {
		if pair.id == apkSigIDV2 {
			v2 = pair.value
			break
		}
	}
	if len(v2) == 0 {
		return nil, fmt.Errorf("v2 signing block missing")
	}
	signers, next, err := readU32Prefixed(v2, 0)
	if err != nil || next != len(v2) {
		return nil, fmt.Errorf("malformed v2 signer sequence")
	}
	var verified []string
	for off := 0; off < len(signers); {
		signer, n, err := readU32Prefixed(signers, off)
		if err != nil {
			return nil, fmt.Errorf("malformed v2 signer: %w", err)
		}
		off = n
		cert, err := verifyV2Signer(ctx, ra, size, signer)
		if err != nil {
			return nil, err
		}
		verified = append(verified, sha256Hex(cert.Raw))
	}
	if len(verified) == 0 {
		return nil, fmt.Errorf("v2 block has no signers")
	}
	sort.Strings(verified)
	return uniqueStrings(verified), nil
}

func verifyV2Signer(ctx context.Context, ra io.ReaderAt, size int64, signer []byte) (*x509.Certificate, error) {
	signedData, off, err := readU32Prefixed(signer, 0)
	if err != nil {
		return nil, fmt.Errorf("v2 signed data: %w", err)
	}
	signaturesRaw, off, err := readU32Prefixed(signer, off)
	if err != nil {
		return nil, fmt.Errorf("v2 signatures: %w", err)
	}
	publicKey, off, err := readU32Prefixed(signer, off)
	if err != nil || off != len(signer) {
		return nil, fmt.Errorf("v2 public key is malformed")
	}
	digestsRaw, dOff, err := readU32Prefixed(signedData, 0)
	if err != nil {
		return nil, fmt.Errorf("v2 digests: %w", err)
	}
	certsRaw, _, err := readU32Prefixed(signedData, dOff)
	if err != nil {
		return nil, fmt.Errorf("v2 certificates: %w", err)
	}
	digests, err := parseDigestRecords(digestsRaw)
	if err != nil {
		return nil, err
	}
	signatures, err := parseSignatureRecords(signaturesRaw)
	if err != nil {
		return nil, err
	}
	certDER, _, err := readU32Prefixed(certsRaw, 0)
	if err != nil {
		return nil, fmt.Errorf("v2 leaf certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("v2 leaf certificate parse: %w", err)
	}
	encodedKey, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil || !bytes.Equal(encodedKey, publicKey) {
		return nil, fmt.Errorf("v2 public key does not match leaf certificate")
	}
	selected, digest, err := selectSignature(signatures, digests)
	if err != nil {
		return nil, err
	}
	if err := verifySignedData(cert.PublicKey, selected, signedData); err != nil {
		return nil, fmt.Errorf("v2 signer signature invalid: %w", err)
	}
	computed, err := apkContentDigest(ctx, ra, size, hashForAlgorithm(selected.algorithm))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(computed, digest.value) {
		return nil, fmt.Errorf("v2 APK content digest mismatch")
	}
	return cert, nil
}

func parseSignatureRecords(raw []byte) ([]signatureRecord, error) {
	var out []signatureRecord
	for off := 0; off < len(raw); {
		record, n, err := readU32Prefixed(raw, off)
		if err != nil || len(record) < 8 {
			return nil, fmt.Errorf("malformed v2 signature record")
		}
		off = n
		value, end, err := readU32Prefixed(record, 4)
		if err != nil || end != len(record) {
			return nil, fmt.Errorf("malformed v2 signature value")
		}
		out = append(out, signatureRecord{algorithm: binary.LittleEndian.Uint32(record), value: value})
	}
	return out, nil
}

func parseDigestRecords(raw []byte) ([]digestRecord, error) {
	var out []digestRecord
	for off := 0; off < len(raw); {
		record, n, err := readU32Prefixed(raw, off)
		if err != nil || len(record) < 8 {
			return nil, fmt.Errorf("malformed v2 digest record")
		}
		off = n
		value, end, err := readU32Prefixed(record, 4)
		if err != nil || end != len(record) {
			return nil, fmt.Errorf("malformed v2 digest value")
		}
		out = append(out, digestRecord{algorithm: binary.LittleEndian.Uint32(record), value: value})
	}
	return out, nil
}

func selectSignature(sigs []signatureRecord, digests []digestRecord) (signatureRecord, digestRecord, error) {
	rank := map[uint32]int{
		sigRSAPSSSHA512: 7, sigRSAPSSSHA256: 6,
		sigECDSASHA512: 5, sigECDSASHA256: 4,
		sigRSAPKCS1SHA512: 3, sigRSAPKCS1SHA256: 2,
		sigDSASHA256: 1,
	}
	best := -1
	var selected signatureRecord
	for _, sig := range sigs {
		if r, ok := rank[sig.algorithm]; ok && r > best {
			selected, best = sig, r
		}
	}
	if best < 0 {
		return signatureRecord{}, digestRecord{}, fmt.Errorf("v2 signer has no supported signature algorithm")
	}
	for _, digest := range digests {
		if digest.algorithm == selected.algorithm {
			return selected, digest, nil
		}
	}
	return signatureRecord{}, digestRecord{}, fmt.Errorf("v2 signer has no digest for selected signature")
}

func hashForAlgorithm(algorithm uint32) crypto.Hash {
	switch algorithm {
	case sigRSAPSSSHA512, sigRSAPKCS1SHA512, sigECDSASHA512:
		return crypto.SHA512
	default:
		return crypto.SHA256
	}
}

func verifySignedData(publicKey any, sig signatureRecord, signedData []byte) error {
	hashID := hashForAlgorithm(sig.algorithm)
	h := hashID.New()
	_, _ = h.Write(signedData)
	digest := h.Sum(nil)
	switch sig.algorithm {
	case sigRSAPSSSHA256, sigRSAPSSSHA512:
		key, ok := publicKey.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("RSA-PSS signature with non-RSA key")
		}
		return rsa.VerifyPSS(key, hashID, digest, sig.value, &rsa.PSSOptions{SaltLength: hashID.Size(), Hash: hashID})
	case sigRSAPKCS1SHA256, sigRSAPKCS1SHA512:
		key, ok := publicKey.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("RSA signature with non-RSA key")
		}
		return rsa.VerifyPKCS1v15(key, hashID, digest, sig.value)
	case sigECDSASHA256, sigECDSASHA512:
		key, ok := publicKey.(*ecdsa.PublicKey)
		if !ok || !ecdsa.VerifyASN1(key, digest, sig.value) {
			return fmt.Errorf("ECDSA signature verification failed")
		}
		return nil
	case sigDSASHA256:
		key, ok := publicKey.(*dsa.PublicKey)
		if !ok {
			return fmt.Errorf("DSA signature with non-DSA key")
		}
		var parsed struct{ R, S *big.Int }
		if _, err := asn1.Unmarshal(sig.value, &parsed); err != nil || parsed.R == nil || parsed.S == nil || !dsa.Verify(key, digest, parsed.R, parsed.S) {
			return fmt.Errorf("DSA signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported v2 signature algorithm")
	}
}

func apkContentDigest(ctx context.Context, ra io.ReaderAt, size int64, hashID crypto.Hash) ([]byte, error) {
	cdOff, err := findCentralDirectoryOffset(ra, size)
	if err != nil {
		return nil, err
	}
	blockStart, err := signingBlockStart(ra, cdOff)
	if err != nil {
		return nil, err
	}
	eocdOff, eocd, err := readEOCD(ra, size)
	if err != nil {
		return nil, err
	}
	if blockStart > 0xffffffff {
		return nil, fmt.Errorf("v2 ZIP64 content digest is not supported")
	}
	binary.LittleEndian.PutUint32(eocd[16:], uint32(blockStart))
	var chunks [][]byte
	if err := hashReaderChunks(ctx, ra, 0, blockStart, hashID, &chunks); err != nil {
		return nil, err
	}
	if err := hashReaderChunks(ctx, ra, cdOff, eocdOff-cdOff, hashID, &chunks); err != nil {
		return nil, err
	}
	if err := hashBytesChunks(ctx, eocd, hashID, &chunks); err != nil {
		return nil, err
	}
	h := hashID.New()
	_, _ = h.Write([]byte{0x5a})
	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(chunks)))
	_, _ = h.Write(count[:])
	for _, chunk := range chunks {
		_, _ = h.Write(chunk)
	}
	return h.Sum(nil), nil
}

func signingBlockStart(ra io.ReaderAt, cdOff int64) (int64, error) {
	if cdOff < 24 {
		return 0, fmt.Errorf("signing block is truncated")
	}
	tail := make([]byte, 24)
	if _, err := ra.ReadAt(tail, cdOff-24); err != nil {
		return 0, err
	}
	if string(tail[8:]) != apkSigBlockMagic {
		return 0, fmt.Errorf("signing block magic missing")
	}
	sz := binary.LittleEndian.Uint64(tail)
	if sz > uint64(cdOff)-8 {
		return 0, fmt.Errorf("signing block size invalid")
	}
	return cdOff - int64(sz) - 8, nil
}

func readEOCD(ra io.ReaderAt, size int64) (int64, []byte, error) {
	readLen := int64(22 + 65535)
	if readLen > size {
		readLen = size
	}
	buf := make([]byte, readLen)
	base := size - readLen
	if _, err := ra.ReadAt(buf, base); err != nil && err != io.EOF {
		return 0, nil, err
	}
	for i := len(buf) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != 0x06054b50 {
			continue
		}
		comment := int(binary.LittleEndian.Uint16(buf[i+20:]))
		if i+22+comment == len(buf) {
			return base + int64(i), append([]byte(nil), buf[i:]...), nil
		}
	}
	return 0, nil, fmt.Errorf("EOCD not found")
}

func hashReaderChunks(ctx context.Context, ra io.ReaderAt, off, size int64, hashID crypto.Hash, out *[][]byte) error {
	for size > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := int64(apkChunkSize)
		if n > size {
			n = size
		}
		buf := make([]byte, n)
		if _, err := ra.ReadAt(buf, off); err != nil {
			return err
		}
		*out = append(*out, digestChunk(buf, hashID))
		off += n
		size -= n
	}
	return nil
}

func hashBytesChunks(ctx context.Context, data []byte, hashID crypto.Hash, out *[][]byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := apkChunkSize
		if n > len(data) {
			n = len(data)
		}
		*out = append(*out, digestChunk(data[:n], hashID))
		data = data[n:]
	}
	return nil
}

func digestChunk(data []byte, hashID crypto.Hash) []byte {
	var h hash.Hash
	if hashID == crypto.SHA512 {
		h = sha512.New()
	} else {
		h = sha256.New()
	}
	_, _ = h.Write([]byte{0xa5})
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(data)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func uniqueStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
