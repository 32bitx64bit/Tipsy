// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
)

// Embedded Sigstore Public Good Production Instance trust material (sourced from
// the TUF-verified Sigstore root-signing targets). Stored as Go string constants
// because repository policy forbids committing standalone *.pem/*.crt/*.key files.
const (
	// sigstoreFulcioRootPEM is the Sigstore Public Good Fulcio Root CA
	// (O=sigstore.dev, CN=sigstore, valid 2021-10-07T13:56:59Z to 2031-10-05T13:56:58Z).
	sigstoreFulcioRootPEM = `-----BEGIN CERTIFICATE-----
MIIB9zCCAXygAwIBAgIUALZNAPFdxHPwjeDloDwyYChAO/4wCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MTEwMDcxMzU2NTlaFw0zMTEwMDUxMzU2NThaMCoxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjERMA8GA1UEAxMIc2lnc3RvcmUwdjAQBgcqhkjOPQIBBgUrgQQAIgNiAAT7
XeFT4rb3PQGwS4IajtLk3/OlnpgangaBclYpsYBr5i+4ynB07ceb3LP0OIOZdxex
X69c5iVuyJRQ+Hz05yi+UF3uBWAlHpiS5sh0+H2GHE7SXrk1EC5m1Tr19L9gg92j
YzBhMA4GA1UdDwEB/wQEAwIBBjAPBgNVHRMBAf8EBTADAQH/MB0GA1UdDgQWBBRY
wB5fkUWlZql6zJChkyLQKsXF+jAfBgNVHSMEGDAWgBRYwB5fkUWlZql6zJChkyLQ
KsXF+jAKBggqhkjOPQQDAwNpADBmAjEAj1nHeXZp+13NWBNa+EDsDP8G1WWg1tCM
WP/WHPqpaVo0jhsweNFZgSs0eE7wYI4qAjEA2WB9ot98sIkoF3vZYdd3/VtWB5b9
TNMea7Ix/stJ5TfcLLeABLE4BNJOsQ4vnBHJ
-----END CERTIFICATE-----`

	// sigstoreFulcioIntermediatePEM is the Sigstore Public Good Fulcio Intermediate CA
	// (O=sigstore.dev, CN=sigstore-intermediate, valid 2022-04-13T20:06:15Z to 2031-10-05T13:56:58Z).
	sigstoreFulcioIntermediatePEM = `-----BEGIN CERTIFICATE-----
MIICGjCCAaGgAwIBAgIUALnViVfnU0brJasmRkHrn/UnfaQwCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MjA0MTMyMDA2MTVaFw0zMTEwMDUxMzU2NThaMDcxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjEeMBwGA1UEAxMVc2lnc3RvcmUtaW50ZXJtZWRpYXRlMHYwEAYHKoZIzj0C
AQYFK4EEACIDYgAE8RVS/ysH+NOvuDZyPIZtilgUF9NlarYpAd9HP1vBBH1U5CV7
7LSS7s0ZiH4nE7Hv7ptS6LvvR/STk798LVgMzLlJ4HeIfF3tHSaexLcYpSASr1kS
0N/RgBJz/9jWCiXno3sweTAOBgNVHQ8BAf8EBAMCAQYwEwYDVR0lBAwwCgYIKwYB
BQUHAwMwEgYDVR0TAQH/BAgwBgEB/wIBADAdBgNVHQ4EFgQU39Ppz1YkEZb5qNjp
KFWixi4YZD8wHwYDVR0jBBgwFoAUWMAeX5FFpWapesyQoZMi0CrFxfowCgYIKoZI
zj0EAwMDZwAwZAIwPCsQK4DYiZYDPIaDi5HFKnfxXx6ASSVmERfsynYBiX2X6SJR
nZU84/9DZdnFvvxmAjBOt6QpBlc4J/0DxvkTCqpclvziL6BCCPnjdlIB3Pu3BxsP
mygUY7Ii2zbdCdliiow=
-----END CERTIFICATE-----`

	// sigstoreRekorPublicKeyPEM is the Sigstore Public Good Rekor transparency log
	// ECDSA P-256 public key (https://rekor.sigstore.dev).
	sigstoreRekorPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE2G2Y+2tabdTV5BcGiBIx0a9fAFwr
kBbmLSGtks4L3qX6yYY0zufBnhC8Ur/iy55GhWP/9A/bY2LhC30M9+RYtw==
-----END PUBLIC KEY-----`

	// SigstoreRekorLogIDHex is the SHA-256 digest of the DER-encoded SubjectPublicKeyInfo
	// of sigstoreRekorPublicKeyPEM.
	SigstoreRekorLogIDHex = "c0d23d6ad406973f9559f3ba2d1ca01f84147d8ffc5b8445c224f98b9591801d"
)

// TrustRoots holds the offline certificate authority and transparency-log keys
// used to verify Sigstore bundles without network calls.
type TrustRoots struct {
	FulcioRoots         *x509.CertPool
	FulcioIntermediates *x509.CertPool
	RekorPublicKey      *ecdsa.PublicKey
	RekorLogIDHex       string
}

var loadPublicGoodTrustRoots = sync.OnceValues(func() (TrustRoots, error) {
	rootCert, err := parseSinglePEMCertificate(sigstoreFulcioRootPEM)
	if err != nil {
		return TrustRoots{}, fmt.Errorf("updatecheck: parse embedded Fulcio root CA: %w", err)
	}
	intermCert, err := parseSinglePEMCertificate(sigstoreFulcioIntermediatePEM)
	if err != nil {
		return TrustRoots{}, fmt.Errorf("updatecheck: parse embedded Fulcio intermediate CA: %w", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermCert)

	rekorKey, logIDHex, err := parseRekorECDSAPublicKeyPEM(sigstoreRekorPublicKeyPEM)
	if err != nil {
		return TrustRoots{}, fmt.Errorf("updatecheck: parse embedded Rekor public key: %w", err)
	}
	if logIDHex != SigstoreRekorLogIDHex {
		return TrustRoots{}, fmt.Errorf("updatecheck: embedded Rekor log ID %s does not match pinned %s", logIDHex, SigstoreRekorLogIDHex)
	}

	return TrustRoots{
		FulcioRoots:         roots,
		FulcioIntermediates: intermediates,
		RekorPublicKey:      rekorKey,
		RekorLogIDHex:       logIDHex,
	}, nil
})

// PublicGoodTrustRoots returns the embedded Sigstore Public Good Fulcio CA pool
// and Rekor ECDSA verification key.
func PublicGoodTrustRoots() (TrustRoots, error) {
	return loadPublicGoodTrustRoots()
}

func parseSinglePEMCertificate(pemText string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(pemText))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("expected CERTIFICATE PEM block")
	}
	if extra, _ := pem.Decode(rest); extra != nil {
		return nil, errors.New("unexpected trailing PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseRekorECDSAPublicKeyPEM(pemText string) (*ecdsa.PublicKey, string, error) {
	block, rest := pem.Decode([]byte(pemText))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, "", errors.New("expected PUBLIC KEY PEM block")
	}
	if extra, _ := pem.Decode(rest); extra != nil {
		return nil, "", errors.New("unexpected trailing PEM block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	ecKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, "", fmt.Errorf("expected ECDSA public key, got %T", parsed)
	}
	sum := sha256.Sum256(block.Bytes)
	return ecKey, hex.EncodeToString(sum[:]), nil
}

func rekorLogIDHexForKey(pub *ecdsa.PublicKey) (string, error) {
	if pub == nil {
		return "", errors.New("nil Rekor public key")
	}
	spkiDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal Rekor public key: %w", err)
	}
	sum := sha256.Sum256(spkiDER)
	return hex.EncodeToString(sum[:]), nil
}
