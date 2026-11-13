// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type syntheticSigstorePKI struct {
	rootKey         *ecdsa.PrivateKey
	rootCert        *x509.Certificate
	intermKey       *ecdsa.PrivateKey
	intermCert      *x509.Certificate
	rekorKey        *ecdsa.PrivateKey
	rekorLogIDBytes []byte
	rekorLogIDHex   string
	trust           TrustRoots
}

type syntheticCertOptions struct {
	sanURI          string
	issuerV1        string
	issuerV2        string
	buildSignerURI  string
	runnerEnv       string
	sourceRepoURI   string
	sourceRepoV1    string
	sourceRef       string
	sourceRefV1     string
	buildConfigURI  string
	buildTrigger    string
	notBefore       time.Time
	notAfter        time.Time
	omitRunnerEnv   bool
	omitBuildSigner bool
	wrongCA         bool
}

func newSyntheticSigstorePKI(t *testing.T) *syntheticSigstorePKI {
	t.Helper()

	baseTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"synthetic-sigstore.test"}, CommonName: "synthetic-fulcio-root"},
		NotBefore:             baseTime.Add(-365 * 24 * time.Hour),
		NotAfter:              baseTime.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create root cert: %v", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("parse root cert: %v", err)
	}

	intermKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate intermediate key: %v", err)
	}
	intermTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{Organization: []string{"synthetic-sigstore.test"}, CommonName: "synthetic-fulcio-intermediate"},
		NotBefore:             baseTime.Add(-180 * 24 * time.Hour),
		NotAfter:              baseTime.Add(180 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	intermDER, err := x509.CreateCertificate(rand.Reader, intermTmpl, rootCert, &intermKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("create intermediate cert: %v", err)
	}
	intermCert, err := x509.ParseCertificate(intermDER)
	if err != nil {
		t.Fatalf("parse intermediate cert: %v", err)
	}

	rekorKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate rekor key: %v", err)
	}
	rekorSPKI, err := x509.MarshalPKIXPublicKey(&rekorKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal rekor public key: %v", err)
	}
	logIDSum := sha256.Sum256(rekorSPKI)

	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(intermCert)

	return &syntheticSigstorePKI{
		rootKey:         rootKey,
		rootCert:        rootCert,
		intermKey:       intermKey,
		intermCert:      intermCert,
		rekorKey:        rekorKey,
		rekorLogIDBytes: logIDSum[:],
		rekorLogIDHex:   hex.EncodeToString(logIDSum[:]),
		trust: TrustRoots{
			FulcioRoots:         roots,
			FulcioIntermediates: intermediates,
			RekorPublicKey:      &rekorKey.PublicKey,
			RekorLogIDHex:       hex.EncodeToString(logIDSum[:]),
		},
	}
}

func defaultSyntheticCertOptions(tag string) syntheticCertOptions {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	signer := "https://github.com/32bitx64bit/Tipsy/.github/workflows/release.yml@refs/tags/" + tag
	return syntheticCertOptions{
		sanURI:         signer,
		issuerV1:       OfficialOIDCIssuer,
		issuerV2:       OfficialOIDCIssuer,
		buildSignerURI: signer,
		runnerEnv:      OfficialRunnerEnvironment,
		sourceRepoURI:  "https://github.com/32bitx64bit/Tipsy",
		sourceRepoV1:   "32bitx64bit/Tipsy",
		sourceRef:      "refs/tags/" + tag,
		sourceRefV1:    "refs/tags/" + tag,
		buildConfigURI: signer,
		buildTrigger:   "push",
		notBefore:      base,
		notAfter:       base.Add(10 * time.Minute),
	}
}

func mustASN1UTF8(t *testing.T, v string) []byte {
	t.Helper()
	b, err := asn1.MarshalWithParams(v, "utf8")
	if err != nil {
		t.Fatalf("asn1 marshal utf8 %q: %v", v, err)
	}
	return b
}

func mustCriticalSANURIExt(t *testing.T, rawURI string) pkix.Extension {
	t.Helper()
	// GeneralNames ::= SEQUENCE OF GeneralName; uniformResourceIdentifier [6] IA5String
	seq, err := asn1.Marshal([]asn1.RawValue{{
		Class: asn1.ClassContextSpecific,
		Tag:   6,
		Bytes: []byte(rawURI),
	}})
	if err != nil {
		t.Fatalf("marshal critical SAN URI: %v", err)
	}
	return pkix.Extension{
		Id:       oidSubjectAltName,
		Critical: true,
		Value:    seq,
	}
}

func (p *syntheticSigstorePKI) issueLeaf(t *testing.T, opts syntheticCertOptions) (*ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	parsedURI, err := url.Parse(opts.sanURI)
	if err != nil {
		t.Fatalf("parse sanURI: %v", err)
	}

	var extraExts []pkix.Extension
	// Match real Fulcio keyless leaf certificates: Subject DN is empty and SAN is marked critical.
	extraExts = append(extraExts, mustCriticalSANURIExt(t, parsedURI.String()))
	if opts.issuerV1 != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioIssuerV1, Value: []byte(opts.issuerV1)})
	}
	if opts.buildTrigger != "" {
		extraExts = append(extraExts,
			pkix.Extension{Id: oidFulcioTriggerV1, Value: []byte(opts.buildTrigger)},
			pkix.Extension{Id: oidFulcioBuildTrigger, Value: mustASN1UTF8(t, opts.buildTrigger)},
		)
	}
	if opts.sourceRepoV1 != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioRepoV1, Value: []byte(opts.sourceRepoV1)})
	}
	if opts.sourceRefV1 != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioRefV1, Value: []byte(opts.sourceRefV1)})
	}
	if opts.issuerV2 != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioIssuerV2, Value: mustASN1UTF8(t, opts.issuerV2)})
	}
	if !opts.omitBuildSigner && opts.buildSignerURI != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioBuildSignerURI, Value: mustASN1UTF8(t, opts.buildSignerURI)})
	}
	if !opts.omitRunnerEnv && opts.runnerEnv != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioRunnerEnvironment, Value: mustASN1UTF8(t, opts.runnerEnv)})
	}
	if opts.sourceRepoURI != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioSourceRepositoryURI, Value: mustASN1UTF8(t, opts.sourceRepoURI)})
	}
	if opts.sourceRef != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioSourceRepositoryRef, Value: mustASN1UTF8(t, opts.sourceRef)})
	}
	if opts.buildConfigURI != "" {
		extraExts = append(extraExts, pkix.Extension{Id: oidFulcioBuildConfigURI, Value: mustASN1UTF8(t, opts.buildConfigURI)})
	}

	tmpl := &x509.Certificate{
		SerialNumber:    big.NewInt(100),
		NotBefore:       opts.notBefore,
		NotAfter:        opts.notAfter,
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: extraExts,
	}

	issuerCert := p.intermCert
	issuerKey := p.intermKey
	if opts.wrongCA {
		rogueKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			t.Fatalf("generate rogue CA: %v", err)
		}
		rogueTmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(999),
			Subject:               pkix.Name{CommonName: "untrusted-ca"},
			NotBefore:             opts.notBefore.Add(-time.Hour),
			NotAfter:              opts.notAfter.Add(time.Hour),
			KeyUsage:              x509.KeyUsageCertSign,
			BasicConstraintsValid: true,
			IsCA:                  true,
		}
		rogueDER, err := x509.CreateCertificate(rand.Reader, rogueTmpl, rogueTmpl, &rogueKey.PublicKey, rogueKey)
		if err != nil {
			t.Fatalf("create rogue CA: %v", err)
		}
		issuerCert, err = x509.ParseCertificate(rogueDER)
		if err != nil {
			t.Fatalf("parse rogue CA: %v", err)
		}
		issuerKey = rogueKey
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, issuerCert, &leafKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	leafCert, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	return leafKey, leafCert
}

func (p *syntheticSigstorePKI) signRekorEntry(t *testing.T, bodyJSON []byte, integratedTime time.Time, logIndex int64) tlogEntryJSON {
	t.Helper()
	bodyB64 := base64.StdEncoding.EncodeToString(bodyJSON)
	canonical, err := json.Marshal(rekorSETCanonicalPayload{
		Body:           bodyB64,
		IntegratedTime: integratedTime.Unix(),
		LogID:          p.rekorLogIDHex,
		LogIndex:       logIndex,
	})
	if err != nil {
		t.Fatalf("marshal SET payload: %v", err)
	}
	sum := sha256.Sum256(canonical)
	setSig, err := ecdsa.SignASN1(rand.Reader, p.rekorKey, sum[:])
	if err != nil {
		t.Fatalf("sign Rekor SET: %v", err)
	}
	return tlogEntryJSON{
		LogIndex:       json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("%d", logIndex))),
		LogID:          &logIDJSON{KeyID: base64.StdEncoding.EncodeToString(p.rekorLogIDBytes)},
		KindVersion:    &kindVersionJSON{Kind: "hashedrekord", Version: "0.0.1"},
		IntegratedTime: json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("%d", integratedTime.Unix()))),
		InclusionPromise: &inclusionPromiseRaw{
			SignedEntryTimestamp: base64.StdEncoding.EncodeToString(setSig),
		},
		CanonicalizedBody: bodyB64,
	}
}

func (p *syntheticSigstorePKI) makeBlobBundle(t *testing.T, artifact []byte, opts syntheticCertOptions, integratedAt time.Time) ([]byte, string) {
	t.Helper()
	leafKey, leafCert := p.issueLeaf(t, opts)
	sum := sha256.Sum256(artifact)
	digestHex := hex.EncodeToString(sum[:])

	sigBytes, err := ecdsa.SignASN1(rand.Reader, leafKey, sum[:])
	if err != nil {
		t.Fatalf("sign artifact digest: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCert.Raw})
	rekorBody := map[string]any{
		"apiVersion": "0.0.1",
		"kind":       "hashedrekord",
		"spec": map[string]any{
			"data": map[string]any{
				"hash": map[string]any{
					"algorithm": "sha256",
					"value":     digestHex,
				},
			},
			"signature": map[string]any{
				"content": base64.StdEncoding.EncodeToString(sigBytes),
				"publicKey": map[string]any{
					"content": base64.StdEncoding.EncodeToString(certPEM),
				},
			},
		},
	}
	rekorBodyBytes, err := json.Marshal(rekorBody)
	if err != nil {
		t.Fatalf("marshal hashedrekord: %v", err)
	}
	tlogEntry := p.signRekorEntry(t, rekorBodyBytes, integratedAt, 424242)

	bundle := sigstoreBundleJSON{
		MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		VerificationMaterial: &verificationMaterialRaw{
			Certificate: &certBytesRaw{RawBytes: base64.StdEncoding.EncodeToString(leafCert.Raw)},
			TlogEntries: []tlogEntryJSON{tlogEntry},
		},
		MessageSignature: &messageSignatureRaw{
			MessageDigest: &hashOutputRaw{
				Algorithm: "SHA2_256",
				Digest:    base64.StdEncoding.EncodeToString(sum[:]),
			},
			Signature: base64.StdEncoding.EncodeToString(sigBytes),
		},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return raw, digestHex
}

func (p *syntheticSigstorePKI) makeDSSEBundle(t *testing.T, artifact []byte, subjectName string, rekorKind string, opts syntheticCertOptions, integratedAt time.Time) ([]byte, string) {
	t.Helper()
	leafKey, leafCert := p.issueLeaf(t, opts)
	sum := sha256.Sum256(artifact)
	digestHex := hex.EncodeToString(sum[:])

	stmt := map[string]any{
		"_type":         inTotoStatementV1,
		"predicateType": "https://slsa.dev/provenance/v1",
		"subject": []map[string]any{
			{
				"name":   subjectName,
				"digest": map[string]string{"sha256": digestHex},
			},
		},
		"predicate": map[string]any{"buildDefinition": map[string]any{}},
	}
	payloadBytes, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("marshal statement: %v", err)
	}
	pae := dssePAE(inTotoPayloadType, payloadBytes)
	paeHash := sha256.Sum256(pae)
	sigBytes, err := ecdsa.SignASN1(rand.Reader, leafKey, paeHash[:])
	if err != nil {
		t.Fatalf("sign DSSE PAE: %v", err)
	}

	payloadSum := sha256.Sum256(payloadBytes)
	payloadSumHex := hex.EncodeToString(payloadSum[:])
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCert.Raw})

	var rekorBody map[string]any
	switch rekorKind {
	case "dsse":
		rekorBody = map[string]any{
			"apiVersion": "0.0.1",
			"kind":       "dsse",
			"spec": map[string]any{
				"payloadHash": map[string]any{
					"algorithm": "sha256",
					"value":     payloadSumHex,
				},
				"signatures": []map[string]any{
					{
						"signature": base64.StdEncoding.EncodeToString(sigBytes),
						"verifier":  base64.StdEncoding.EncodeToString(certPEM),
					},
				},
			},
		}
	case "intoto":
		rekorBody = map[string]any{
			"apiVersion": "0.0.2",
			"kind":       "intoto",
			"spec": map[string]any{
				"content": map[string]any{
					"payloadHash": map[string]any{
						"algorithm": "sha256",
						"value":     payloadSumHex,
					},
					"envelope": map[string]any{
						"payloadType": inTotoPayloadType,
						"signatures": []map[string]any{
							{
								"sig":       base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(sigBytes))),
								"publicKey": base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(certPEM))),
							},
						},
					},
				},
			},
		}
	default:
		t.Fatalf("unsupported rekorKind %q", rekorKind)
	}
	rekorBodyBytes, err := json.Marshal(rekorBody)
	if err != nil {
		t.Fatalf("marshal DSSE rekor body: %v", err)
	}
	tlogEntry := p.signRekorEntry(t, rekorBodyBytes, integratedAt, 987654)
	tlogEntry.KindVersion = &kindVersionJSON{Kind: rekorKind, Version: "0.0.1"}

	bundle := sigstoreBundleJSON{
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.3",
		VerificationMaterial: &verificationMaterialRaw{
			X509CertificateChain: &certChainRaw{
				Certificates: []certBytesRaw{{RawBytes: base64.StdEncoding.EncodeToString(leafCert.Raw)}},
			},
			TlogEntries: []tlogEntryJSON{tlogEntry},
		},
		DSSEEnvelope: &dsseEnvelopeRaw{
			Payload:     base64.StdEncoding.EncodeToString(payloadBytes),
			PayloadType: inTotoPayloadType,
			Signatures:  []dsseSignatureJS{{Sig: base64.StdEncoding.EncodeToString(sigBytes)}},
		},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal DSSE bundle: %v", err)
	}
	return raw, digestHex
}

func TestPublicGoodTrustRootsEmbedded(t *testing.T) {
	t.Parallel()

	roots, err := PublicGoodTrustRoots()
	if err != nil {
		t.Fatalf("PublicGoodTrustRoots: %v", err)
	}
	if roots.FulcioRoots == nil || roots.FulcioIntermediates == nil || roots.RekorPublicKey == nil {
		t.Fatalf("PublicGoodTrustRoots returned nil fields: %+v", roots)
	}
	if roots.RekorLogIDHex != SigstoreRekorLogIDHex {
		t.Fatalf("RekorLogIDHex = %s, want %s", roots.RekorLogIDHex, SigstoreRekorLogIDHex)
	}
	intermCert, err := parseSinglePEMCertificate(sigstoreFulcioIntermediatePEM)
	if err != nil {
		t.Fatalf("parse intermediate: %v", err)
	}
	chains, err := intermCert.Verify(x509.VerifyOptions{
		Roots:       roots.FulcioRoots,
		CurrentTime: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil || len(chains) == 0 {
		t.Fatalf("embedded Fulcio intermediate does not chain to embedded Fulcio root: %v", err)
	}
}

func TestVerifySigstoreBundleValidMessageSignature(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := opts.notBefore.Add(2 * time.Minute)
	bundleJSON, digestHex := pki.makeBlobBundle(t, []byte("official-appimage-bytes"), opts, integratedAt)

	got, err := VerifySigstoreBundle(bundleJSON, digestHex, BundlePolicy{
		Trust:        &pki.trust,
		Tag:          "v1.5.0",
		ArtifactName: "Tipsy-1.5.0-x86_64.AppImage",
	})
	if err != nil {
		t.Fatalf("VerifySigstoreBundle: %v", err)
	}
	if got.ArtifactSHA256 != digestHex {
		t.Fatalf("ArtifactSHA256 = %q, want %q", got.ArtifactSHA256, digestHex)
	}
	if got.SignerURI != opts.sanURI {
		t.Fatalf("SignerURI = %q, want %q", got.SignerURI, opts.sanURI)
	}
	if got.OIDCIssuer != OfficialOIDCIssuer {
		t.Fatalf("OIDCIssuer = %q", got.OIDCIssuer)
	}
	if got.RunnerEnvironment != OfficialRunnerEnvironment {
		t.Fatalf("RunnerEnvironment = %q", got.RunnerEnvironment)
	}
	if !got.IntegratedTime.Equal(integratedAt) {
		t.Fatalf("IntegratedTime = %v, want %v", got.IntegratedTime, integratedAt)
	}
}

func TestVerifySigstoreBundleValidDSSEInToto(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v2.2.0")
	integratedAt := opts.notBefore.Add(3 * time.Minute)

	for _, kind := range []string{"dsse", "intoto"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			bundleJSON, digestHex := pki.makeDSSEBundle(
				t,
				[]byte("reproducible-appdir-tar-gz"),
				"/tmp/work/Tipsy-2.2.0-x86_64.AppDir.tar.gz",
				kind,
				opts,
				integratedAt,
			)
			got, err := VerifySigstoreBundle(bundleJSON, digestHex, BundlePolicy{
				Trust:        &pki.trust,
				Tag:          "v2.2.0",
				ArtifactName: "Tipsy-2.2.0-x86_64.AppDir.tar.gz",
			})
			if err != nil {
				t.Fatalf("VerifySigstoreBundle(%s): %v", kind, err)
			}
			if got.ArtifactSHA256 != digestHex {
				t.Fatalf("ArtifactSHA256 = %q, want %q", got.ArtifactSHA256, digestHex)
			}
		})
	}
}

func TestVerifySigstoreBundleWorkflowDispatchMainPolicy(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	mainURI := "https://github.com/32bitx64bit/Tipsy/.github/workflows/release.yml@refs/heads/main"
	opts := defaultSyntheticCertOptions("v1.5.0")
	opts.sanURI = mainURI
	opts.buildSignerURI = mainURI
	opts.buildConfigURI = mainURI
	opts.sourceRef = "refs/heads/main"
	opts.sourceRefV1 = "refs/heads/main"
	opts.buildTrigger = "workflow_dispatch"
	integratedAt := opts.notBefore.Add(time.Minute)

	bundleJSON, digestHex := pki.makeBlobBundle(t, []byte("dispatch-release"), opts, integratedAt)

	// Rejected by default when AllowWorkflowDispatchMain is false.
	if _, err := VerifySigstoreBundle(bundleJSON, digestHex, BundlePolicy{
		Trust: &pki.trust,
		Tag:   "v1.5.0",
	}); err == nil {
		t.Fatal("expected @refs/heads/main bundle to be rejected when AllowWorkflowDispatchMain is false")
	}

	// Accepted when explicitly configured and trigger is workflow_dispatch.
	if _, err := VerifySigstoreBundle(bundleJSON, digestHex, BundlePolicy{
		Trust:                     &pki.trust,
		Tag:                       "v1.5.0",
		AllowWorkflowDispatchMain: true,
	}); err != nil {
		t.Fatalf("expected workflow_dispatch @refs/heads/main bundle to pass: %v", err)
	}

	// Rejected even with AllowWorkflowDispatchMain when trigger is push/pull_request.
	badTriggerOpts := opts
	badTriggerOpts.buildTrigger = "push"
	badBundleJSON, badDigest := pki.makeBlobBundle(t, []byte("push-main"), badTriggerOpts, integratedAt)
	if _, err := VerifySigstoreBundle(badBundleJSON, badDigest, BundlePolicy{
		Trust:                     &pki.trust,
		Tag:                       "v1.5.0",
		AllowWorkflowDispatchMain: true,
	}); err == nil {
		t.Fatal("expected non-workflow_dispatch @refs/heads/main bundle to be rejected")
	}
}

func TestVerifySigstoreBundleRejectsWrongProvenanceClaims(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	base := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := base.notBefore.Add(2 * time.Minute)

	cases := []struct {
		name   string
		mutate func(*syntheticCertOptions)
		want   string
	}{
		{
			name: "wrong repo",
			mutate: func(o *syntheticCertOptions) {
				u := "https://github.com/attacker-fork/Tipsy/.github/workflows/release.yml@refs/tags/v1.5.0"
				o.sanURI = u
				o.buildSignerURI = u
				o.buildConfigURI = u
				o.sourceRepoURI = "https://github.com/attacker-fork/Tipsy"
				o.sourceRepoV1 = "attacker-fork/Tipsy"
			},
			want: "not pinned",
		},
		{
			name: "wrong workflow",
			mutate: func(o *syntheticCertOptions) {
				u := "https://github.com/32bitx64bit/Tipsy/.github/workflows/ci.yml@refs/tags/v1.5.0"
				o.sanURI = u
				o.buildSignerURI = u
				o.buildConfigURI = u
			},
			want: "not pinned",
		},
		{
			name: "wrong tag ref",
			mutate: func(o *syntheticCertOptions) {
				u := "https://github.com/32bitx64bit/Tipsy/.github/workflows/release.yml@refs/tags/v1.4.9"
				o.sanURI = u
				o.buildSignerURI = u
				o.buildConfigURI = u
				o.sourceRef = "refs/tags/v1.4.9"
				o.sourceRefV1 = "refs/tags/v1.4.9"
			},
			want: "not pinned",
		},
		{
			name: "wrong OIDC issuer",
			mutate: func(o *syntheticCertOptions) {
				o.issuerV2 = "https://accounts.google.com"
				o.issuerV1 = "https://accounts.google.com"
			},
			want: "OIDC issuer",
		},
		{
			name: "self-hosted runner environment",
			mutate: func(o *syntheticCertOptions) {
				o.runnerEnv = "self-hosted"
			},
			want: "runner environment",
		},
		{
			name: "missing runner environment",
			mutate: func(o *syntheticCertOptions) {
				o.omitRunnerEnv = true
			},
			want: "runner environment",
		},
		{
			name: "mismatched BuildSignerURI",
			mutate: func(o *syntheticCertOptions) {
				o.buildSignerURI = "https://github.com/32bitx64bit/Tipsy/.github/workflows/other.yml@refs/tags/v1.5.0"
			},
			want: "Build Signer URI",
		},
		{
			name: "untrusted CA",
			mutate: func(o *syntheticCertOptions) {
				o.wrongCA = true
			},
			want: "certificate chain verification failed",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := base
			tc.mutate(&opts)
			bundleJSON, digestHex := pki.makeBlobBundle(t, []byte("payload-"+tc.name), opts, integratedAt)
			_, err := VerifySigstoreBundle(bundleJSON, digestHex, BundlePolicy{
				Trust: &pki.trust,
				Tag:   "v1.5.0",
			})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestVerifySigstoreBundleRejectsInvalidRekorSETAndTransplants(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := opts.notBefore.Add(2 * time.Minute)
	validBundle, digestHex := pki.makeBlobBundle(t, []byte("legit-binary"), opts, integratedAt)

	t.Run("tampered SET signature", func(t *testing.T) {
		t.Parallel()
		var parsed sigstoreBundleJSON
		if err := json.Unmarshal(validBundle, &parsed); err != nil {
			t.Fatal(err)
		}
		rawSET, _ := base64.StdEncoding.DecodeString(parsed.VerificationMaterial.TlogEntries[0].InclusionPromise.SignedEntryTimestamp)
		rawSET[len(rawSET)-1] ^= 0xff
		parsed.VerificationMaterial.TlogEntries[0].InclusionPromise.SignedEntryTimestamp = base64.StdEncoding.EncodeToString(rawSET)
		tampered, _ := json.Marshal(parsed)

		_, err := VerifySigstoreBundle(tampered, digestHex, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"})
		if err == nil || !strings.Contains(err.Error(), "signedEntryTimestamp") {
			t.Fatalf("expected signedEntryTimestamp error, got %v", err)
		}
	})

	t.Run("wrong Rekor log ID", func(t *testing.T) {
		t.Parallel()
		var parsed sigstoreBundleJSON
		if err := json.Unmarshal(validBundle, &parsed); err != nil {
			t.Fatal(err)
		}
		wrongID := make([]byte, sha256.Size)
		parsed.VerificationMaterial.TlogEntries[0].LogID.KeyID = base64.StdEncoding.EncodeToString(wrongID)
		tampered, _ := json.Marshal(parsed)

		_, err := VerifySigstoreBundle(tampered, digestHex, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"})
		if err == nil || !strings.Contains(err.Error(), "Rekor log ID") {
			t.Fatalf("expected Rekor log ID error, got %v", err)
		}
	})

	t.Run("SET transplant from different artifact", func(t *testing.T) {
		t.Parallel()
		otherBundle, _ := pki.makeBlobBundle(t, []byte("different-binary"), opts, integratedAt)
		var parsedValid, parsedOther sigstoreBundleJSON
		_ = json.Unmarshal(validBundle, &parsedValid)
		_ = json.Unmarshal(otherBundle, &parsedOther)
		// Transplant valid Rekor tlogEntry from another artifact onto validBundle.
		parsedValid.VerificationMaterial.TlogEntries = parsedOther.VerificationMaterial.TlogEntries
		tampered, _ := json.Marshal(parsedValid)

		_, err := VerifySigstoreBundle(tampered, digestHex, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"})
		if err == nil || !strings.Contains(err.Error(), "Rekor hashedrekord") {
			t.Fatalf("expected Rekor hashedrekord binding failure, got %v", err)
		}
	})
}

func TestVerifySigstoreBundleRejectsOutOfWindowIntegratedTime(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")

	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"before NotBefore", opts.notBefore.Add(-1 * time.Second)},
		{"after NotAfter", opts.notAfter.Add(1 * time.Second)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundleJSON, digestHex := pki.makeBlobBundle(t, []byte("window-test"), opts, tc.at)
			_, err := VerifySigstoreBundle(bundleJSON, digestHex, BundlePolicy{
				Trust: &pki.trust,
				Tag:   "v1.5.0",
			})
			if err == nil || !strings.Contains(err.Error(), "outside certificate validity") {
				t.Fatalf("expected validity window error for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestVerifySigstoreBundleRejectsTamperedDigestAndMissingBundle(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := opts.notBefore.Add(time.Minute)
	bundleJSON, _ := pki.makeBlobBundle(t, []byte("authentic-bytes"), opts, integratedAt)

	tamperedSum := sha256.Sum256([]byte("tampered-bytes"))
	tamperedHex := hex.EncodeToString(tamperedSum[:])

	if _, err := VerifySigstoreBundle(bundleJSON, tamperedHex, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"}); err == nil {
		t.Fatal("expected error for tampered SHA-256 digest")
	}
	if _, err := VerifySigstoreBundle(nil, tamperedHex, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"}); err == nil {
		t.Fatal("expected error for missing bundle")
	}
	if _, err := VerifySigstoreBundle([]byte{}, tamperedHex, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"}); err == nil {
		t.Fatal("expected error for empty bundle")
	}
}

func TestVerifyArtifactFileAndBytes(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := opts.notBefore.Add(time.Minute)

	artifactName := "Tipsy-1.5.0-x86_64.AppImage"
	artifactBytes := []byte("verified-local-appimage-binary")
	bundleJSON, digestHex := pki.makeBlobBundle(t, artifactBytes, opts, integratedAt)
	sha256Line := []byte(fmt.Sprintf("%s  %s\n", digestHex, artifactName))

	dir := t.TempDir()
	artifactPath := filepath.Join(dir, artifactName)
	sha256Path := artifactPath + ".sha256"
	bundlePath := artifactPath + ".sigstore.json"

	if err := os.WriteFile(artifactPath, artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sha256Path, sha256Line, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, bundleJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	policy := BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0", ArtifactName: artifactName}
	if _, err := VerifyArtifactBytes(artifactBytes, sha256Line, bundleJSON, policy); err != nil {
		t.Fatalf("VerifyArtifactBytes: %v", err)
	}
	if _, err := VerifyArtifactFile(artifactPath, sha256Path, bundlePath, BundlePolicy{Trust: &pki.trust, Tag: "v1.5.0"}); err != nil {
		t.Fatalf("VerifyArtifactFile: %v", err)
	}

	// Tampered artifact file on disk must fail.
	tamperedArtifactPath := filepath.Join(dir, "tampered.AppImage")
	if err := os.WriteFile(tamperedArtifactPath, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArtifactFile(tamperedArtifactPath, sha256Path, bundlePath, policy); err == nil {
		t.Fatal("expected VerifyArtifactFile to reject tampered artifact")
	}

	// Symlink artifact must be rejected.
	symlinkPath := filepath.Join(dir, "symlink.AppImage")
	if err := os.Symlink(artifactPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArtifactFile(symlinkPath, sha256Path, bundlePath, policy); err == nil {
		t.Fatal("expected VerifyArtifactFile to reject symlink artifact")
	}

	// Missing bundle file must fail.
	if _, err := VerifyArtifactFile(artifactPath, sha256Path, filepath.Join(dir, "missing.sigstore.json"), policy); err == nil {
		t.Fatal("expected VerifyArtifactFile to reject missing bundle file")
	}
}
