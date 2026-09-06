// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package apk

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const (
	proofOfRotationAttrID = 0x3ba06f8c
	rotationMinSDKAttrID  = 0x559f8b02
	rotationDevAttrID     = 0xc2a6b3ba
	maxV3Signers          = 32
	maxRotationLevels     = 32
)

// SigningVerification is the authenticated signing identity of one APK. The
// lineage is ordered oldest to current. It never includes unverified
// certificates obtained only from package metadata.
type SigningVerification struct {
	Scheme        string
	CurrentSHA256 []string
	LineageSHA256 []string
}

type verifiedV3Signer struct {
	minSDK         uint32
	maxSDK         uint32
	current        string
	lineage        []string
	rotationMinSDK *uint32
}

// VerifyReaderAt verifies the strongest APK signature scheme present without
// reopening a pathname. A malformed stronger scheme never falls back to a
// weaker one.
func VerifyReaderAt(ctx context.Context, ra io.ReaderAt, size int64) (SigningVerification, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ra == nil || size <= 0 {
		return SigningVerification{}, fmt.Errorf("apk: invalid signature input")
	}
	pairs, _, err := readAPKSigningBlock(ra, size)
	if err != nil {
		return SigningVerification{}, err
	}
	byID := make(map[uint32][]byte)
	for _, pair := range pairs {
		switch pair.id {
		case apkSigIDV2, apkSigIDV3, apkSigIDV31:
			if _, exists := byID[pair.id]; exists {
				return SigningVerification{}, fmt.Errorf("apk: duplicate signature scheme block 0x%x", pair.id)
			}
			byID[pair.id] = pair.value
		}
	}

	v2 := byID[apkSigIDV2]
	v3 := byID[apkSigIDV3]
	v31 := byID[apkSigIDV31]
	if len(v31) > 0 {
		if len(v3) == 0 {
			return SigningVerification{}, fmt.Errorf("apk: v3.1 signature requires a v3 compatibility block")
		}
		older, err := verifyV3Scheme(ctx, ra, size, v3, false)
		if err != nil {
			return SigningVerification{}, fmt.Errorf("apk: verify v3 compatibility signature: %w", err)
		}
		current, err := verifyV3Scheme(ctx, ra, size, v31, true)
		if err != nil {
			return SigningVerification{}, fmt.Errorf("apk: verify v3.1 signature: %w", err)
		}
		if older.rotationMinSDK == nil || *older.rotationMinSDK != current.minSDK || current.minSDK == 0 || older.maxSDK != current.minSDK-1 {
			return SigningVerification{}, fmt.Errorf("apk: v3.1 rotation minimum SDK is missing or inconsistent")
		}
		if !lineagePrefix(older.lineage, current.lineage) {
			return SigningVerification{}, fmt.Errorf("apk: v3 and v3.1 signer lineages are inconsistent")
		}
		if err := verifyV2Compatibility(ctx, ra, size, v2, current.lineage); err != nil {
			return SigningVerification{}, err
		}
		return SigningVerification{Scheme: "v3.1", CurrentSHA256: []string{current.current}, LineageSHA256: current.lineage}, nil
	}
	if len(v3) > 0 {
		current, err := verifyV3Scheme(ctx, ra, size, v3, false)
		if err != nil {
			return SigningVerification{}, fmt.Errorf("apk: verify v3 signature: %w", err)
		}
		if current.rotationMinSDK != nil {
			return SigningVerification{}, fmt.Errorf("apk: v3 declares a stripped v3.1 signer")
		}
		if err := verifyV2Compatibility(ctx, ra, size, v2, current.lineage); err != nil {
			return SigningVerification{}, err
		}
		return SigningVerification{Scheme: "v3", CurrentSHA256: []string{current.current}, LineageSHA256: current.lineage}, nil
	}
	if len(v2) == 0 {
		return SigningVerification{}, fmt.Errorf("apk: v2/v3 signing block missing")
	}
	certs, err := verifySourceV2(ctx, ra, size)
	if err != nil {
		return SigningVerification{}, err
	}
	return SigningVerification{Scheme: "v2", CurrentSHA256: certs, LineageSHA256: append([]string(nil), certs...)}, nil
}

func verifyV2Compatibility(ctx context.Context, ra io.ReaderAt, size int64, block []byte, lineage []string) error {
	if len(block) == 0 {
		return nil
	}
	certs, err := verifySourceV2(ctx, ra, size)
	if err != nil {
		return fmt.Errorf("apk: verify v2 compatibility signature: %w", err)
	}
	if len(certs) != 1 || len(lineage) == 0 || certs[0] != lineage[0] {
		return fmt.Errorf("apk: v2 signer is not the oldest authenticated v3 signer")
	}
	return nil
}

func verifyV3Scheme(ctx context.Context, ra io.ReaderAt, size int64, value []byte, v31 bool) (verifiedV3Signer, error) {
	signers, next, err := readU32Prefixed(value, 0)
	if err != nil || next != len(value) || len(signers) == 0 {
		return verifiedV3Signer{}, fmt.Errorf("malformed signer sequence")
	}
	var verified []verifiedV3Signer
	for off := 0; off < len(signers); {
		if len(verified) == maxV3Signers {
			return verifiedV3Signer{}, fmt.Errorf("too many v3 signers")
		}
		signer, n, err := readU32Prefixed(signers, off)
		if err != nil {
			return verifiedV3Signer{}, fmt.Errorf("malformed signer %d: %w", len(verified), err)
		}
		off = n
		got, err := verifyV3Signer(ctx, ra, size, signer, v31)
		if err != nil {
			return verifiedV3Signer{}, fmt.Errorf("signer %d: %w", len(verified), err)
		}
		verified = append(verified, got)
	}
	sort.Slice(verified, func(i, j int) bool {
		if verified[i].minSDK != verified[j].minSDK {
			return verified[i].minSDK < verified[j].minSDK
		}
		return verified[i].maxSDK < verified[j].maxSDK
	})
	for i := 1; i < len(verified); i++ {
		if verified[i].minSDK <= verified[i-1].maxSDK {
			return verifiedV3Signer{}, fmt.Errorf("overlapping signer SDK ranges")
		}
	}
	merged := append([]string(nil), verified[0].lineage...)
	for _, signer := range verified[1:] {
		var err error
		merged, err = mergeLineages(merged, signer.lineage)
		if err != nil {
			return verifiedV3Signer{}, err
		}
	}
	latest := verified[len(verified)-1]
	if len(merged) == 0 || merged[len(merged)-1] != latest.current {
		return verifiedV3Signer{}, fmt.Errorf("latest signer does not terminate the consolidated lineage")
	}
	latest.lineage = merged
	return latest, nil
}

func verifyV3Signer(ctx context.Context, ra io.ReaderAt, size int64, signer []byte, v31 bool) (verifiedV3Signer, error) {
	signedData, off, err := readU32Prefixed(signer, 0)
	if err != nil || off+8 > len(signer) {
		return verifiedV3Signer{}, fmt.Errorf("signed data is malformed")
	}
	outerMin := binary.LittleEndian.Uint32(signer[off:])
	outerMax := binary.LittleEndian.Uint32(signer[off+4:])
	off += 8
	if outerMin > outerMax {
		return verifiedV3Signer{}, fmt.Errorf("invalid SDK range")
	}
	signaturesRaw, off, err := readU32Prefixed(signer, off)
	if err != nil {
		return verifiedV3Signer{}, fmt.Errorf("signatures are malformed")
	}
	publicKeyRaw, off, err := readU32Prefixed(signer, off)
	if err != nil || off != len(signer) {
		return verifiedV3Signer{}, fmt.Errorf("public key is malformed")
	}
	signatures, err := parseSignatureRecords(signaturesRaw)
	if err != nil || len(signatures) == 0 {
		return verifiedV3Signer{}, fmt.Errorf("signatures are malformed")
	}
	publicKey, err := x509.ParsePKIXPublicKey(publicKeyRaw)
	if err != nil {
		return verifiedV3Signer{}, fmt.Errorf("public key parse: %w", err)
	}

	digestsRaw, dataOff, err := readU32Prefixed(signedData, 0)
	if err != nil {
		return verifiedV3Signer{}, fmt.Errorf("digests are malformed")
	}
	certificatesRaw, dataOff, err := readU32Prefixed(signedData, dataOff)
	if err != nil || dataOff+8 > len(signedData) {
		return verifiedV3Signer{}, fmt.Errorf("certificates or SDK range are malformed")
	}
	signedMin := binary.LittleEndian.Uint32(signedData[dataOff:])
	signedMax := binary.LittleEndian.Uint32(signedData[dataOff+4:])
	dataOff += 8
	attributesRaw, dataOff, err := readU32Prefixed(signedData, dataOff)
	if err != nil || dataOff != len(signedData) || signedMin != outerMin || signedMax != outerMax {
		return verifiedV3Signer{}, fmt.Errorf("signed and unsigned SDK ranges differ")
	}
	digests, err := parseDigestRecords(digestsRaw)
	if err != nil || len(digests) == 0 || !sameAlgorithmOrder(signatures, digests) {
		return verifiedV3Signer{}, fmt.Errorf("signature and digest algorithm lists differ")
	}
	selected, digest, err := selectSignature(signatures, digests)
	if err != nil {
		return verifiedV3Signer{}, err
	}
	if err := verifySignedData(publicKey, selected, signedData); err != nil {
		return verifiedV3Signer{}, fmt.Errorf("signed data signature invalid: %w", err)
	}

	certDER, certOff, err := readU32Prefixed(certificatesRaw, 0)
	if err != nil || len(certDER) == 0 {
		return verifiedV3Signer{}, fmt.Errorf("leaf certificate is malformed")
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return verifiedV3Signer{}, fmt.Errorf("leaf certificate parse: %w", err)
	}
	encodedKey, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil || !bytes.Equal(encodedKey, publicKeyRaw) {
		return verifiedV3Signer{}, fmt.Errorf("public key does not match leaf certificate")
	}
	seenCertificates := map[string]struct{}{sha256Hex(cert.Raw): {}}
	for count := 1; certOff < len(certificatesRaw); count++ {
		if count == maxRotationLevels {
			return verifiedV3Signer{}, fmt.Errorf("certificate chain is too long")
		}
		chainDER, next, err := readU32Prefixed(certificatesRaw, certOff)
		if err != nil || len(chainDER) == 0 {
			return verifiedV3Signer{}, fmt.Errorf("certificate chain is malformed")
		}
		certOff = next
		chainCert, err := x509.ParseCertificate(chainDER)
		if err != nil {
			return verifiedV3Signer{}, fmt.Errorf("certificate chain parse: %w", err)
		}
		digest := sha256Hex(chainCert.Raw)
		if _, duplicate := seenCertificates[digest]; duplicate {
			return verifiedV3Signer{}, fmt.Errorf("certificate chain repeats a certificate")
		}
		seenCertificates[digest] = struct{}{}
	}
	computed, err := apkContentDigest(ctx, ra, size, hashForAlgorithm(selected.algorithm))
	if err != nil {
		return verifiedV3Signer{}, err
	}
	if !bytes.Equal(computed, digest.value) {
		return verifiedV3Signer{}, fmt.Errorf("APK content digest mismatch")
	}

	attrs, err := parseV3Attributes(attributesRaw)
	if err != nil {
		return verifiedV3Signer{}, err
	}
	if _, ok := attrs[rotationDevAttrID]; ok && !v31 {
		return verifiedV3Signer{}, fmt.Errorf("v3.1 development-rotation attribute appears in v3")
	}
	current := sha256Hex(cert.Raw)
	lineage := []string{current}
	if raw, ok := attrs[proofOfRotationAttrID]; ok {
		lineage, err = verifyProofOfRotation(raw, cert.Raw)
		if err != nil {
			return verifiedV3Signer{}, err
		}
	}
	var rotationMin *uint32
	if raw, ok := attrs[rotationMinSDKAttrID]; ok {
		if v31 || len(raw) != 4 {
			return verifiedV3Signer{}, fmt.Errorf("invalid rotation minimum SDK attribute")
		}
		value := binary.LittleEndian.Uint32(raw)
		rotationMin = &value
	}
	return verifiedV3Signer{minSDK: outerMin, maxSDK: outerMax, current: current, lineage: lineage, rotationMinSDK: rotationMin}, nil
}

func parseV3Attributes(raw []byte) (map[uint32][]byte, error) {
	out := make(map[uint32][]byte)
	for off := 0; off < len(raw); {
		attribute, next, err := readU32Prefixed(raw, off)
		if err != nil || len(attribute) < 4 {
			return nil, fmt.Errorf("malformed v3 additional attribute")
		}
		off = next
		id := binary.LittleEndian.Uint32(attribute)
		if _, exists := out[id]; exists {
			return nil, fmt.Errorf("duplicate v3 additional attribute 0x%x", id)
		}
		out[id] = append([]byte(nil), attribute[4:]...)
	}
	return out, nil
}

func verifyProofOfRotation(raw, currentDER []byte) ([]string, error) {
	if len(raw) < 4 || binary.LittleEndian.Uint32(raw) != 1 {
		return nil, fmt.Errorf("proof-of-rotation has an unsupported version")
	}
	var previous *x509.Certificate
	var previousAlgorithm uint32
	seen := make(map[string]struct{})
	lineage := make([]string, 0, 4)
	off := 4
	for off < len(raw) {
		if len(lineage) == maxRotationLevels {
			return nil, fmt.Errorf("proof-of-rotation has too many levels")
		}
		node, next, err := readU32Prefixed(raw, off)
		if err != nil {
			return nil, fmt.Errorf("proof-of-rotation level is malformed")
		}
		off = next
		signedData, nodeOff, err := readU32Prefixed(node, 0)
		if err != nil || nodeOff+8 > len(node) {
			return nil, fmt.Errorf("proof-of-rotation signed level is malformed")
		}
		_ = binary.LittleEndian.Uint32(node[nodeOff:]) // Android capability flags are signed but not trust policy here.
		algorithmForNext := binary.LittleEndian.Uint32(node[nodeOff+4:])
		nodeOff += 8
		signature, nodeOff, err := readU32Prefixed(node, nodeOff)
		if err != nil || nodeOff != len(node) {
			return nil, fmt.Errorf("proof-of-rotation signature is malformed")
		}
		certDER, signedOff, err := readU32Prefixed(signedData, 0)
		if err != nil || signedOff+4 != len(signedData) {
			return nil, fmt.Errorf("proof-of-rotation certificate is malformed")
		}
		parentAlgorithm := binary.LittleEndian.Uint32(signedData[signedOff:])
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			return nil, fmt.Errorf("proof-of-rotation certificate parse: %w", err)
		}
		digest := sha256Hex(cert.Raw)
		if _, duplicate := seen[digest]; duplicate {
			return nil, fmt.Errorf("proof-of-rotation repeats a certificate")
		}
		seen[digest] = struct{}{}
		if previous == nil {
			if parentAlgorithm != 0 || len(signature) != 0 {
				return nil, fmt.Errorf("proof-of-rotation root is not self-contained")
			}
		} else {
			if parentAlgorithm != previousAlgorithm {
				return nil, fmt.Errorf("proof-of-rotation algorithm linkage mismatch")
			}
			if err := verifySignedData(previous.PublicKey, signatureRecord{algorithm: previousAlgorithm, value: signature}, signedData); err != nil {
				return nil, fmt.Errorf("proof-of-rotation signature invalid: %w", err)
			}
		}
		previous = cert
		previousAlgorithm = algorithmForNext
		lineage = append(lineage, digest)
	}
	if len(lineage) == 0 || previousAlgorithm != 0 || previous == nil || !bytes.Equal(previous.Raw, currentDER) {
		return nil, fmt.Errorf("proof-of-rotation does not terminate at the APK signer")
	}
	return lineage, nil
}

func sameAlgorithmOrder(signatures []signatureRecord, digests []digestRecord) bool {
	if len(signatures) != len(digests) {
		return false
	}
	seen := make(map[uint32]struct{}, len(signatures))
	for i := range signatures {
		if signatures[i].algorithm != digests[i].algorithm {
			return false
		}
		if _, duplicate := seen[signatures[i].algorithm]; duplicate {
			return false
		}
		seen[signatures[i].algorithm] = struct{}{}
	}
	return true
}

func mergeLineages(a, b []string) ([]string, error) {
	if lineagePrefix(a, b) {
		return append([]string(nil), b...), nil
	}
	if lineagePrefix(b, a) {
		return append([]string(nil), a...), nil
	}
	return nil, fmt.Errorf("inconsistent proof-of-rotation lineages")
}

func lineagePrefix(prefix, complete []string) bool {
	if len(prefix) > len(complete) {
		return false
	}
	for i := range prefix {
		if prefix[i] != complete[i] {
			return false
		}
	}
	return true
}
