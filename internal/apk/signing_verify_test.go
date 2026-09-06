// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package apk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVerifyV2SignatureAndRejectTamper(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "Tipsy Signature Test"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(4102444800, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := zipBytes(t, map[string][]byte{"payload.bin": []byte("signed-payload")})
	signed := signV2ForTest(t, unsigned, key, certDER)
	verified, err := verifySourceV2(context.Background(), bytesReaderAt(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0] != sha256Hex(certDER) {
		t.Fatalf("verified identities=%v", verified)
	}
	zr, err := zip.NewReader(bytes.NewReader(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	var payload *zip.File
	for _, f := range zr.File {
		if f.Name == "payload.bin" {
			payload = f
			break
		}
	}
	if payload == nil {
		t.Fatal("payload.bin not found")
	}
	payloadOffset, err := payload.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	if payloadOffset < 0 || payload.CompressedSize64 == 0 ||
		uint64(payloadOffset) > uint64(len(signed)) ||
		payload.CompressedSize64 > uint64(len(signed))-uint64(payloadOffset) {
		t.Fatalf("invalid payload extent offset=%d size=%d archive=%d", payloadOffset, payload.CompressedSize64, len(signed))
	}
	tampered := append([]byte(nil), signed...)
	tampered[int(payloadOffset)+int(payload.CompressedSize64/2)] ^= 1
	if _, err := verifySourceV2(context.Background(), bytesReaderAt(tampered), int64(len(tampered))); err == nil {
		t.Fatal("tampered APK signature verified")
	} else if !strings.Contains(err.Error(), "v2 APK content digest mismatch") {
		t.Fatalf("tampered APK returned the wrong error: %v", err)
	}
}

func TestVerifyOfficialAPKWhenProvided(t *testing.T) {
	path := os.Getenv("TIPSY_TEST_OFFICIAL_APK")
	if path == "" {
		t.Skip("set TIPSY_TEST_OFFICIAL_APK to a locally owned official APK")
	}
	rep, err := Inspect(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("declared schemes: v2=%v v3=%v v3.1=%v", rep.Packages[0].Signing.HasV2, rep.Packages[0].Signing.HasV3, rep.Packages[0].Signing.HasV3_1)
	if err := VerifyReportSignatures(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Packages[0].Signing.CryptographicallyValid || len(rep.Packages[0].Signing.VerifiedCertSHA256) == 0 {
		t.Fatalf("signature=%+v", rep.Packages[0].Signing)
	}
	t.Logf("verified signer identities: %v", rep.Packages[0].Signing.VerifiedCertSHA256)
}

func TestVerifyV3ProofOfRotationAndRejectTamper(t *testing.T) {
	oldKey, oldCert := signingIdentityForTest(t, 100)
	newKey, newCert := signingIdentityForTest(t, 101)
	proof := proofOfRotationForTest(t, oldKey, oldCert, newCert)
	unsigned := zipBytes(t, map[string][]byte{"payload.bin": []byte("v3-authenticated-payload")})
	signed := signV3BlocksForTest(t, unsigned, []v3TestSigner{{
		id: apkSigIDV3, key: newKey, certDER: newCert, minSDK: 28, maxSDK: ^uint32(0), proof: proof,
	}})
	verified, err := VerifyReaderAt(context.Background(), bytesReaderAt(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	wantLineage := []string{sha256Hex(oldCert), sha256Hex(newCert)}
	if verified.Scheme != "v3" || len(verified.CurrentSHA256) != 1 || verified.CurrentSHA256[0] != wantLineage[1] ||
		len(verified.LineageSHA256) != 2 || verified.LineageSHA256[0] != wantLineage[0] || verified.LineageSHA256[1] != wantLineage[1] {
		t.Fatalf("verification=%+v want lineage=%v", verified, wantLineage)
	}

	invalidProof := append([]byte(nil), proof...)
	invalidProof[len(invalidProof)-1] ^= 1
	bad := signV3BlocksForTest(t, unsigned, []v3TestSigner{{
		id: apkSigIDV3, key: newKey, certDER: newCert, minSDK: 28, maxSDK: ^uint32(0), proof: invalidProof,
	}})
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(bad), int64(len(bad))); err == nil || !strings.Contains(err.Error(), "proof-of-rotation signature invalid") {
		t.Fatalf("invalid rotation proof err=%v", err)
	}

	repeated := proofOfRotationForTest(t, oldKey, oldCert, oldCert)
	repeatedAPK := signV3BlocksForTest(t, unsigned, []v3TestSigner{{
		id: apkSigIDV3, key: oldKey, certDER: oldCert, minSDK: 28, maxSDK: ^uint32(0), proof: repeated,
	}})
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(repeatedAPK), int64(len(repeatedAPK))); err == nil || !strings.Contains(err.Error(), "repeats a certificate") {
		t.Fatalf("repeated rotation certificate err=%v", err)
	}

	thirdKey, thirdCert := signingIdentityForTest(t, 102)
	wrongTerminus := signV3BlocksForTest(t, unsigned, []v3TestSigner{{
		id: apkSigIDV3, key: thirdKey, certDER: thirdCert, minSDK: 28, maxSDK: ^uint32(0), proof: proof,
	}})
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(wrongTerminus), int64(len(wrongTerminus))); err == nil || !strings.Contains(err.Error(), "does not terminate") {
		t.Fatalf("wrong rotation terminus err=%v", err)
	}
}

func TestVerifyV31RequiresBoundCompatibilityBlock(t *testing.T) {
	oldKey, oldCert := signingIdentityForTest(t, 110)
	newKey, newCert := signingIdentityForTest(t, 111)
	proof := proofOfRotationForTest(t, oldKey, oldCert, newCert)
	unsigned := zipBytes(t, map[string][]byte{"payload.bin": []byte("v31-authenticated-payload")})
	rotationMin := uint32(33)
	signed := signV3BlocksForTest(t, unsigned, []v3TestSigner{
		{id: apkSigIDV3, key: oldKey, certDER: oldCert, minSDK: 28, maxSDK: 32, rotationMinSDK: &rotationMin},
		{id: apkSigIDV31, key: newKey, certDER: newCert, minSDK: 33, maxSDK: ^uint32(0), proof: proof},
	})
	verified, err := VerifyReaderAt(context.Background(), bytesReaderAt(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Scheme != "v3.1" || len(verified.LineageSHA256) != 2 || verified.CurrentSHA256[0] != sha256Hex(newCert) {
		t.Fatalf("verification=%+v", verified)
	}

	stripped := signV3BlocksForTest(t, unsigned, []v3TestSigner{{
		id: apkSigIDV3, key: oldKey, certDER: oldCert, minSDK: 28, maxSDK: ^uint32(0), rotationMinSDK: &rotationMin,
	}})
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(stripped), int64(len(stripped))); err == nil || !strings.Contains(err.Error(), "stripped v3.1") {
		t.Fatalf("stripped v3.1 err=%v", err)
	}

	missingV3 := signV3BlocksForTest(t, unsigned, []v3TestSigner{{
		id: apkSigIDV31, key: newKey, certDER: newCert, minSDK: 33, maxSDK: ^uint32(0), proof: proof,
	}})
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(missingV3), int64(len(missingV3))); err == nil || !strings.Contains(err.Error(), "requires a v3 compatibility block") {
		t.Fatalf("v3.1 without v3 err=%v", err)
	}

	invalidProof := append([]byte(nil), proof...)
	invalidProof[len(invalidProof)-1] ^= 1
	invalidStronger := signV3BlocksForTest(t, unsigned, []v3TestSigner{
		{id: apkSigIDV3, key: oldKey, certDER: oldCert, minSDK: 28, maxSDK: 32, rotationMinSDK: &rotationMin},
		{id: apkSigIDV31, key: newKey, certDER: newCert, minSDK: 33, maxSDK: ^uint32(0), proof: invalidProof},
	})
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(invalidStronger), int64(len(invalidStronger))); err == nil || !strings.Contains(err.Error(), "verify v3.1 signature") {
		t.Fatalf("invalid stronger scheme fell back to v3: %v", err)
	}
}

func FuzzV3SigningParsers(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0, 0})
	f.Add([]byte{4, 0, 0, 0, 1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = parseV3Attributes(raw)
		_, _ = verifyProofOfRotation(raw, raw)
		_, _ = VerifyReaderAt(context.Background(), bytesReaderAt(raw), int64(len(raw)))
	})
}

func signV2ForTest(t *testing.T, unsigned []byte, key *rsa.PrivateKey, certDER []byte) []byte {
	return signV2ForTestWithAttributes(t, unsigned, key, certDER, nil, true)
}

func signV2ForTestWithAttributes(t *testing.T, unsigned []byte, key *rsa.PrivateKey, certDER, attributes []byte, aospEmptyField bool) []byte {
	t.Helper()
	placeholder, err := insertAPKSigningBlock(unsigned, sigPair{id: apkSigIDV2, value: v2SignerBlock(certDER)})
	if err != nil {
		t.Fatal(err)
	}
	contentDigest, err := apkContentDigest(context.Background(), bytesReaderAt(placeholder), int64(len(placeholder)), crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	digestRecord := appendU32(nil, sigRSAPKCS1SHA256)
	digestRecord = append(digestRecord, u32pref(contentDigest)...)
	digests := u32pref(u32pref(digestRecord))
	certificates := u32pref(u32pref(certDER))
	attributesField := u32pref(attributes)
	signedData := append(append(digests, certificates...), attributesField...)
	if aospEmptyField {
		signedData = append(signedData, u32pref(nil)...)
	}
	h := sha256.Sum256(signedData)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	sigRecord := appendU32(nil, sigRSAPKCS1SHA256)
	sigRecord = append(sigRecord, u32pref(signature)...)
	signatures := u32pref(u32pref(sigRecord))
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signer := append(u32pref(signedData), signatures...)
	signer = append(signer, u32pref(publicKey)...)
	value := u32pref(u32pref(signer))
	got, err := insertAPKSigningBlock(unsigned, sigPair{id: apkSigIDV2, value: value})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestV2AOSPEmptyFieldAndStrippingProtection(t *testing.T) {
	key, certDER := signingIdentityForTest(t, 77)
	unsigned := zipBytes(t, map[string][]byte{"payload.bin": []byte("aosp-v2")})
	signed := signV2ForTest(t, unsigned, key, certDER)
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(signed), int64(len(signed))); err != nil {
		t.Fatalf("AOSP-compatible empty field rejected: %v", err)
	}
	attribute := appendU32(nil, v2StrippingProtectionAttrID)
	attribute = appendU32(attribute, 3)
	withStripping := signV2ForTestWithAttributes(t, unsigned, key, certDER, u32pref(attribute), true)
	if _, err := VerifyReaderAt(context.Background(), bytesReaderAt(withStripping), int64(len(withStripping))); err == nil || !strings.Contains(err.Error(), "stripping protection") {
		t.Fatalf("stripped v3 signature accepted: %v", err)
	}
}

type v3TestSigner struct {
	id             uint32
	key            *rsa.PrivateKey
	certDER        []byte
	minSDK         uint32
	maxSDK         uint32
	proof          []byte
	rotationMinSDK *uint32
}

func signV3BlocksForTest(t *testing.T, unsigned []byte, signers []v3TestSigner) []byte {
	t.Helper()
	placeholderPairs := make([]sigPair, len(signers))
	for i, signer := range signers {
		placeholderPairs[i] = sigPair{id: signer.id, value: []byte{0}}
	}
	placeholder, err := insertAPKSigningBlock(unsigned, placeholderPairs...)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest, err := apkContentDigest(context.Background(), bytesReaderAt(placeholder), int64(len(placeholder)), crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	pairs := make([]sigPair, len(signers))
	for i, signer := range signers {
		pairs[i] = sigPair{id: signer.id, value: v3SignerValueForTest(t, signer, contentDigest)}
	}
	got, err := insertAPKSigningBlock(unsigned, pairs...)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func v3SignerValueForTest(t *testing.T, signer v3TestSigner, contentDigest []byte) []byte {
	t.Helper()
	digestRecord := appendU32(nil, sigRSAPKCS1SHA256)
	digestRecord = append(digestRecord, u32pref(contentDigest)...)
	digests := u32pref(u32pref(digestRecord))
	certificates := u32pref(u32pref(signer.certDER))
	var attributes []byte
	if len(signer.proof) > 0 {
		attribute := appendU32(nil, proofOfRotationAttrID)
		attribute = append(attribute, signer.proof...)
		attributes = append(attributes, u32pref(attribute)...)
	}
	if signer.rotationMinSDK != nil {
		attribute := appendU32(nil, rotationMinSDKAttrID)
		attribute = appendU32(attribute, *signer.rotationMinSDK)
		attributes = append(attributes, u32pref(attribute)...)
	}
	signedData := append(append([]byte(nil), digests...), certificates...)
	signedData = appendU32(signedData, signer.minSDK)
	signedData = appendU32(signedData, signer.maxSDK)
	signedData = append(signedData, u32pref(attributes)...)
	h := sha256.Sum256(signedData)
	signature, err := rsa.SignPKCS1v15(rand.Reader, signer.key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	signatureRecord := appendU32(nil, sigRSAPKCS1SHA256)
	signatureRecord = append(signatureRecord, u32pref(signature)...)
	publicKey, err := x509.MarshalPKIXPublicKey(&signer.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	encodedSigner := append([]byte(nil), u32pref(signedData)...)
	encodedSigner = appendU32(encodedSigner, signer.minSDK)
	encodedSigner = appendU32(encodedSigner, signer.maxSDK)
	encodedSigner = append(encodedSigner, u32pref(u32pref(signatureRecord))...)
	encodedSigner = append(encodedSigner, u32pref(publicKey)...)
	return u32pref(u32pref(encodedSigner))
}

func proofOfRotationForTest(t *testing.T, oldKey *rsa.PrivateKey, oldCert, newCert []byte) []byte {
	t.Helper()
	oldSigned := append([]byte(nil), u32pref(oldCert)...)
	oldSigned = appendU32(oldSigned, 0)
	oldNode := append([]byte(nil), u32pref(oldSigned)...)
	oldNode = appendU32(oldNode, 0)
	oldNode = appendU32(oldNode, sigRSAPKCS1SHA256)
	oldNode = append(oldNode, u32pref(nil)...)

	newSigned := append([]byte(nil), u32pref(newCert)...)
	newSigned = appendU32(newSigned, sigRSAPKCS1SHA256)
	h := sha256.Sum256(newSigned)
	signature, err := rsa.SignPKCS1v15(rand.Reader, oldKey, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	newNode := append([]byte(nil), u32pref(newSigned)...)
	newNode = appendU32(newNode, 0)
	newNode = appendU32(newNode, 0)
	newNode = append(newNode, u32pref(signature)...)

	proof := make([]byte, 4)
	binary.LittleEndian.PutUint32(proof, 1)
	proof = append(proof, u32pref(oldNode)...)
	proof = append(proof, u32pref(newNode)...)
	return proof
}

func signingIdentityForTest(t *testing.T, serial int64) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "Tipsy V3 Test"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(4102444800, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, certDER
}
