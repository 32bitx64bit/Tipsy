// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package apk

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
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
	tampered := append([]byte(nil), signed...)
	i := strings.Index(string(tampered), "signed-payload")
	if i < 0 {
		t.Fatal("payload not found")
	}
	tampered[i] ^= 1
	if _, err := verifySourceV2(context.Background(), bytesReaderAt(tampered), int64(len(tampered))); err == nil {
		t.Fatal("tampered APK signature verified")
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
	if err := VerifyReportSignatures(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Packages[0].Signing.CryptographicallyValid || len(rep.Packages[0].Signing.VerifiedCertSHA256) == 0 {
		t.Fatalf("signature=%+v", rep.Packages[0].Signing)
	}
	t.Logf("verified signer identities: %v", rep.Packages[0].Signing.VerifiedCertSHA256)
}

func signV2ForTest(t *testing.T, unsigned []byte, key *rsa.PrivateKey, certDER []byte) []byte {
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
	attributes := u32pref(nil)
	signedData := append(append(digests, certificates...), attributes...)
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
