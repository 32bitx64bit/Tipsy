// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// OfficialOIDCIssuer is the GitHub Actions OIDC issuer required on Fulcio certificates.
	OfficialOIDCIssuer = "https://token.actions.githubusercontent.com"
	// OfficialRunnerEnvironment is the Fulcio runner-environment claim required for official builds.
	OfficialRunnerEnvironment = "github-hosted"
	// OfficialWorkflowPath is the repository-relative release workflow path pinned by Tipsy.
	OfficialWorkflowPath = ".github/workflows/release.yml"

	inTotoPayloadType   = "application/vnd.in-toto+json"
	inTotoStatementV1   = "https://in-toto.io/Statement/v1"
	inTotoStatementV01  = "https://in-toto.io/Statement/v0.1"
	maxSigstoreBundleSz = 2 << 20
)

var (
	// RFC 5280 Subject Alternative Name extension OID (2.5.29.17).
	oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}

	// Fulcio v1 deprecated extensions (raw UTF-8 bytes in ext.Value).
	oidFulcioIssuerV1  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidFulcioTriggerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 2}
	oidFulcioRepoV1    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 5}
	oidFulcioRefV1     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 6}

	// Fulcio v2 extensions (ASN.1 DER UTF8String in ext.Value).
	oidFulcioIssuerV2            = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidFulcioBuildSignerURI      = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 9}
	oidFulcioRunnerEnvironment   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 11}
	oidFulcioSourceRepositoryURI = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
	oidFulcioSourceRepositoryRef = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 14}
	oidFulcioBuildConfigURI      = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 18}
	oidFulcioBuildTrigger        = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 20}
)

// BundlePolicy configures offline cryptographic verification of a Sigstore bundle.
// A zero-value BundlePolicy (with Tag set) pins official 32bitx64bit/Tipsy
// release.yml@refs/tags/<Tag> builds on github-hosted runners against the
// embedded Sigstore Public Good Fulcio and Rekor roots.
type BundlePolicy struct {
	// Trust overrides the embedded Sigstore Public Good roots. Tests supply
	// synthetic Fulcio/Rekor roots here; nil uses PublicGoodTrustRoots().
	Trust *TrustRoots
	// Repository is the GitHub owner/repo. Empty defaults to Repository ("32bitx64bit/Tipsy").
	Repository string
	// WorkflowPath is the workflow path inside Repository. Empty defaults to OfficialWorkflowPath.
	WorkflowPath string
	// Tag is the release tag being verified (for example "v1.5.0"). Required.
	Tag string
	// AllowWorkflowDispatchMain permits certificates issued for @refs/heads/main
	// ONLY when the Fulcio build-trigger extension explicitly records "workflow_dispatch".
	AllowWorkflowDispatchMain bool
	// ArtifactName optionally restricts DSSE in-toto subjects to a matching basename.
	ArtifactName string
}

// VerifiedBundle summarizes the cryptographically verified provenance of a bundle.
type VerifiedBundle struct {
	ArtifactSHA256    string
	SignerURI         string
	OIDCIssuer        string
	RunnerEnvironment string
	IntegratedTime    time.Time
	LogIndex          int64
}

type sigstoreBundleJSON struct {
	MediaType            string                   `json:"mediaType"`
	VerificationMaterial *verificationMaterialRaw `json:"verificationMaterial"`
	MessageSignature     *messageSignatureRaw     `json:"messageSignature"`
	DSSEEnvelope         *dsseEnvelopeRaw         `json:"dsseEnvelope"`
}

type verificationMaterialRaw struct {
	Certificate          *certBytesRaw   `json:"certificate"`
	X509CertificateChain *certChainRaw   `json:"x509CertificateChain"`
	TlogEntries          []tlogEntryJSON `json:"tlogEntries"`
}

type certChainRaw struct {
	Certificates []certBytesRaw `json:"certificates"`
}

type certBytesRaw struct {
	RawBytes string `json:"rawBytes"`
}

type tlogEntryJSON struct {
	LogIndex          json.RawMessage      `json:"logIndex"`
	LogID             *logIDJSON           `json:"logId"`
	KindVersion       *kindVersionJSON     `json:"kindVersion"`
	IntegratedTime    json.RawMessage      `json:"integratedTime"`
	InclusionPromise  *inclusionPromiseRaw `json:"inclusionPromise"`
	CanonicalizedBody string               `json:"canonicalizedBody"`
}

type logIDJSON struct {
	KeyID string `json:"keyId"`
}

type kindVersionJSON struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

type inclusionPromiseRaw struct {
	SignedEntryTimestamp string `json:"signedEntryTimestamp"`
}

type messageSignatureRaw struct {
	MessageDigest *hashOutputRaw `json:"messageDigest"`
	Signature     string         `json:"signature"`
}

type hashOutputRaw struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

type dsseEnvelopeRaw struct {
	Payload     string            `json:"payload"`
	PayloadType string            `json:"payloadType"`
	Signatures  []dsseSignatureJS `json:"signatures"`
}

type dsseSignatureJS struct {
	Sig string `json:"sig"`
}

// rekorSETCanonicalPayload matches Rekor's RFC 8785 JCS canonicalization of
// LogEntryAnon: field order is strictly alphabetical ("body", "integratedTime",
// "logID", "logIndex").
type rekorSETCanonicalPayload struct {
	Body           string `json:"body"`
	IntegratedTime int64  `json:"integratedTime"`
	LogID          string `json:"logID"`
	LogIndex       int64  `json:"logIndex"`
}

type rekorEntryBodyJSON struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Spec       json.RawMessage `json:"spec"`
}

type hashedRekordSpecJSON struct {
	Data struct {
		Hash struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"value"`
		} `json:"hash"`
	} `json:"data"`
	Signature struct {
		Content   string `json:"content"`
		PublicKey struct {
			Content string `json:"content"`
		} `json:"publicKey"`
	} `json:"signature"`
}

type dsseRekorSpecJSON struct {
	PayloadHash *struct {
		Algorithm string `json:"algorithm"`
		Value     string `json:"value"`
	} `json:"payloadHash"`
	Signatures []struct {
		Signature string `json:"signature"`
		Verifier  string `json:"verifier"`
	} `json:"signatures"`
	PublicKey string `json:"publicKey"`
	Content   *struct {
		PayloadHash *struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"value"`
		} `json:"payloadHash"`
		Envelope *struct {
			Signatures []struct {
				Sig       string `json:"sig"`
				PublicKey string `json:"publicKey"`
			} `json:"signatures"`
		} `json:"envelope"`
	} `json:"content"`
}

type inTotoStatementJSON struct {
	Type          string              `json:"_type"`
	PredicateType string              `json:"predicateType"`
	Subject       []inTotoSubjectJSON `json:"subject"`
}

type inTotoSubjectJSON struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// VerifySigstoreBundle cryptographically verifies a .sigstore.json bundle offline
// against expectedSHA256 and policy using only the Go standard library.
func VerifySigstoreBundle(bundleJSON []byte, expectedSHA256 string, policy BundlePolicy) (VerifiedBundle, error) {
	if len(bundleJSON) == 0 {
		return VerifiedBundle{}, errors.New("updatecheck: missing or empty Sigstore bundle")
	}
	if len(bundleJSON) > maxSigstoreBundleSz {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: Sigstore bundle exceeds %d bytes", maxSigstoreBundleSz)
	}

	wantDigestHex, wantDigestBytes, err := normalizeSHA256Hex(expectedSHA256)
	if err != nil {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: invalid expected SHA-256: %w", err)
	}

	trust, err := resolveTrustRoots(policy.Trust)
	if err != nil {
		return VerifiedBundle{}, err
	}

	repo, workflow, normalizedTag, err := resolveIdentityPolicy(policy)
	if err != nil {
		return VerifiedBundle{}, err
	}
	tagSignerURI := "https://github.com/" + repo + "/" + workflow + "@refs/tags/" + normalizedTag
	mainSignerURI := "https://github.com/" + repo + "/" + workflow + "@refs/heads/main"

	var bundle sigstoreBundleJSON
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: decode Sigstore bundle: %w", err)
	}
	if !isSupportedBundleMediaType(bundle.MediaType) {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: unsupported Sigstore bundle mediaType %q", bundle.MediaType)
	}
	if bundle.VerificationMaterial == nil {
		return VerifiedBundle{}, errors.New("updatecheck: Sigstore bundle has no verificationMaterial")
	}

	leaf, leafPub, err := extractLeafCertificate(bundle.VerificationMaterial)
	if err != nil {
		return VerifiedBundle{}, err
	}

	if len(bundle.VerificationMaterial.TlogEntries) == 0 {
		return VerifiedBundle{}, errors.New("updatecheck: Sigstore bundle has no Rekor transparency log entries")
	}
	tlogEntry := bundle.VerificationMaterial.TlogEntries[0]
	integratedTimeUnix, logIndex, rekorBodyBytes, err := verifyRekorSET(tlogEntry, trust)
	if err != nil {
		return VerifiedBundle{}, err
	}
	integratedAt := time.Unix(integratedTimeUnix, 0).UTC()

	if err := verifyFulcioChainAtTime(leaf, trust, integratedAt); err != nil {
		return VerifiedBundle{}, err
	}

	signerURI, issuer, runnerEnv, err := verifyFulcioIdentity(leaf, repo, normalizedTag, tagSignerURI, mainSignerURI, policy.AllowWorkflowDispatchMain)
	if err != nil {
		return VerifiedBundle{}, err
	}

	switch {
	case bundle.MessageSignature != nil && bundle.DSSEEnvelope != nil:
		return VerifiedBundle{}, errors.New("updatecheck: Sigstore bundle contains both messageSignature and dsseEnvelope")
	case bundle.MessageSignature != nil:
		if err := verifyMessageSignature(bundle.MessageSignature, leaf, leafPub, wantDigestHex, wantDigestBytes, rekorBodyBytes); err != nil {
			return VerifiedBundle{}, err
		}
	case bundle.DSSEEnvelope != nil:
		if err := verifyDSSEEnvelope(bundle.DSSEEnvelope, leaf, leafPub, wantDigestHex, policy.ArtifactName, rekorBodyBytes); err != nil {
			return VerifiedBundle{}, err
		}
	default:
		return VerifiedBundle{}, errors.New("updatecheck: Sigstore bundle has neither messageSignature nor dsseEnvelope")
	}

	return VerifiedBundle{
		ArtifactSHA256:    wantDigestHex,
		SignerURI:         signerURI,
		OIDCIssuer:        issuer,
		RunnerEnvironment: runnerEnv,
		IntegratedTime:    integratedAt,
		LogIndex:          logIndex,
	}, nil
}

func resolveTrustRoots(override *TrustRoots) (TrustRoots, error) {
	if override == nil {
		return PublicGoodTrustRoots()
	}
	if override.FulcioRoots == nil {
		return TrustRoots{}, errors.New("updatecheck: trust policy has nil FulcioRoots")
	}
	if override.RekorPublicKey == nil {
		return TrustRoots{}, errors.New("updatecheck: trust policy has nil RekorPublicKey")
	}
	t := *override
	if strings.TrimSpace(t.RekorLogIDHex) == "" {
		logID, err := rekorLogIDHexForKey(t.RekorPublicKey)
		if err != nil {
			return TrustRoots{}, fmt.Errorf("updatecheck: %w", err)
		}
		t.RekorLogIDHex = logID
	} else {
		t.RekorLogIDHex = strings.ToLower(strings.TrimSpace(t.RekorLogIDHex))
	}
	return t, nil
}

func resolveIdentityPolicy(policy BundlePolicy) (repo, workflow, tag string, err error) {
	repo = strings.TrimSpace(policy.Repository)
	if repo == "" {
		repo = Repository
	}
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") || strings.ContainsAny(repo, " \t\r\n@") {
		return "", "", "", fmt.Errorf("updatecheck: invalid repository policy %q", repo)
	}
	workflow = strings.TrimPrefix(strings.TrimSpace(policy.WorkflowPath), "/")
	if workflow == "" {
		workflow = OfficialWorkflowPath
	}
	if strings.Contains(workflow, "..") || strings.ContainsAny(workflow, " \t\r\n@") {
		return "", "", "", fmt.Errorf("updatecheck: invalid workflow policy %q", workflow)
	}
	rawTag := strings.TrimSpace(policy.Tag)
	if rawTag == "" {
		return "", "", "", errors.New("updatecheck: BundlePolicy.Tag is required")
	}
	rawTag = strings.TrimPrefix(rawTag, "refs/tags/")
	if !strings.HasPrefix(rawTag, "v") {
		rawTag = "v" + rawTag
	}
	if len(rawTag) < 2 || strings.Contains(rawTag, "..") || strings.ContainsAny(rawTag, " \t\r\n/@") {
		return "", "", "", fmt.Errorf("updatecheck: invalid release tag %q", policy.Tag)
	}
	return repo, workflow, rawTag, nil
}

func isSupportedBundleMediaType(mediaType string) bool {
	switch strings.TrimSpace(mediaType) {
	case "application/vnd.dev.sigstore.bundle+json;version=0.1",
		"application/vnd.dev.sigstore.bundle+json;version=0.2",
		"application/vnd.dev.sigstore.bundle+json;version=0.3",
		"application/vnd.dev.sigstore.bundle.v0.3+json":
		return true
	default:
		return false
	}
}

func extractLeafCertificate(vm *verificationMaterialRaw) (*x509.Certificate, *ecdsa.PublicKey, error) {
	var rawB64 string
	switch {
	case vm.Certificate != nil && strings.TrimSpace(vm.Certificate.RawBytes) != "":
		rawB64 = strings.TrimSpace(vm.Certificate.RawBytes)
	case vm.X509CertificateChain != nil && len(vm.X509CertificateChain.Certificates) > 0:
		rawB64 = strings.TrimSpace(vm.X509CertificateChain.Certificates[0].RawBytes)
	default:
		return nil, nil, errors.New("updatecheck: Sigstore bundle has no signing certificate")
	}

	der, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return nil, nil, fmt.Errorf("updatecheck: decode signing certificate base64: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("updatecheck: parse signing certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("updatecheck: unsupported leaf public key type %T", cert.PublicKey)
	}
	switch pub.Curve {
	case elliptic.P256(), elliptic.P384():
	default:
		return nil, nil, fmt.Errorf("updatecheck: unsupported leaf ECDSA curve %s", pub.Curve.Params().Name)
	}
	return cert, pub, nil
}

func verifyRekorSET(entry tlogEntryJSON, trust TrustRoots) (integratedTime int64, logIndex int64, bodyBytes []byte, err error) {
	logIndex, err = parseJSONInt64(entry.LogIndex)
	if err != nil || logIndex < 0 {
		return 0, 0, nil, fmt.Errorf("updatecheck: invalid Rekor logIndex: %w", err)
	}
	integratedTime, err = parseJSONInt64(entry.IntegratedTime)
	if err != nil || integratedTime <= 0 {
		return 0, 0, nil, fmt.Errorf("updatecheck: invalid Rekor integratedTime: %w", err)
	}
	if entry.LogID == nil || strings.TrimSpace(entry.LogID.KeyID) == "" {
		return 0, 0, nil, errors.New("updatecheck: Rekor entry missing logId.keyId")
	}
	logIDBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(entry.LogID.KeyID))
	if err != nil || len(logIDBytes) != sha256.Size {
		return 0, 0, nil, errors.New("updatecheck: invalid Rekor logId.keyId")
	}
	entryLogIDHex := hex.EncodeToString(logIDBytes)
	if entryLogIDHex != trust.RekorLogIDHex {
		return 0, 0, nil, fmt.Errorf("updatecheck: Rekor log ID %s does not match trusted log %s", entryLogIDHex, trust.RekorLogIDHex)
	}
	if entry.InclusionPromise == nil || strings.TrimSpace(entry.InclusionPromise.SignedEntryTimestamp) == "" {
		return 0, 0, nil, errors.New("updatecheck: Rekor entry missing signedEntryTimestamp")
	}
	setSig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(entry.InclusionPromise.SignedEntryTimestamp))
	if err != nil || len(setSig) == 0 {
		return 0, 0, nil, errors.New("updatecheck: invalid Rekor signedEntryTimestamp encoding")
	}

	rawBodyB64 := strings.TrimSpace(entry.CanonicalizedBody)
	if rawBodyB64 == "" {
		return 0, 0, nil, errors.New("updatecheck: Rekor entry missing canonicalizedBody")
	}
	bodyBytes, err = base64.StdEncoding.DecodeString(rawBodyB64)
	if err != nil || len(bodyBytes) == 0 || base64.StdEncoding.EncodeToString(bodyBytes) != rawBodyB64 {
		return 0, 0, nil, errors.New("updatecheck: invalid Rekor canonicalizedBody encoding")
	}

	canonicalJSON, err := json.Marshal(rekorSETCanonicalPayload{
		Body:           rawBodyB64,
		IntegratedTime: integratedTime,
		LogID:          entryLogIDHex,
		LogIndex:       logIndex,
	})
	if err != nil {
		return 0, 0, nil, fmt.Errorf("updatecheck: marshal Rekor SET payload: %w", err)
	}
	setDigest := sha256.Sum256(canonicalJSON)
	if !ecdsa.VerifyASN1(trust.RekorPublicKey, setDigest[:], setSig) {
		return 0, 0, nil, errors.New("updatecheck: invalid Rekor signedEntryTimestamp signature")
	}
	return integratedTime, logIndex, bodyBytes, nil
}

func verifyFulcioChainAtTime(leaf *x509.Certificate, trust TrustRoots, integratedAt time.Time) error {
	if integratedAt.Before(leaf.NotBefore) || integratedAt.After(leaf.NotAfter) {
		return fmt.Errorf(
			"updatecheck: Rekor integratedTime %s is outside certificate validity [%s, %s]",
			integratedAt.Format(time.RFC3339),
			leaf.NotBefore.UTC().Format(time.RFC3339),
			leaf.NotAfter.UTC().Format(time.RFC3339),
		)
	}

	// Clone the certificate header before clearing only SubjectAltName (2.5.29.17)
	// from UnhandledCriticalExtensions when Subject DN is empty per RFC 5280 §4.1.2.6.
	// Any other unrecognized critical extension remains unhandled and fails Verify.
	certCopy := *leaf
	if len(certCopy.URIs) > 0 && len(certCopy.UnhandledCriticalExtensions) > 0 {
		filtered := make([]asn1.ObjectIdentifier, 0, len(certCopy.UnhandledCriticalExtensions))
		for _, oid := range certCopy.UnhandledCriticalExtensions {
			if !oid.Equal(oidSubjectAltName) {
				filtered = append(filtered, oid)
			}
		}
		certCopy.UnhandledCriticalExtensions = filtered
	}

	chains, err := certCopy.Verify(x509.VerifyOptions{
		Roots:         trust.FulcioRoots,
		Intermediates: trust.FulcioIntermediates,
		CurrentTime:   integratedAt,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	})
	if err != nil || len(chains) == 0 {
		return fmt.Errorf("updatecheck: Fulcio certificate chain verification failed: %w", err)
	}
	return nil
}

func verifyFulcioIdentity(
	leaf *x509.Certificate,
	repo, normalizedTag, tagSignerURI, mainSignerURI string,
	allowWorkflowDispatchMain bool,
) (signerURI, issuer, runnerEnv string, err error) {
	if len(leaf.URIs) != 1 || leaf.URIs[0] == nil {
		return "", "", "", fmt.Errorf("updatecheck: expected exactly 1 SAN URI on Fulcio certificate, got %d", len(leaf.URIs))
	}
	if len(leaf.DNSNames) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.IPAddresses) != 0 {
		return "", "", "", errors.New("updatecheck: unexpected non-URI SAN entry on Fulcio certificate")
	}

	exts, err := collectFulcioExtensions(leaf)
	if err != nil {
		return "", "", "", err
	}

	issuerV2, hasIssuerV2, err := exts.utf8String(oidFulcioIssuerV2)
	if err != nil {
		return "", "", "", err
	}
	issuerV1, hasIssuerV1 := exts.rawString(oidFulcioIssuerV1)
	if !hasIssuerV2 && !hasIssuerV1 {
		return "", "", "", errors.New("updatecheck: Fulcio certificate missing OIDC issuer extension")
	}
	if hasIssuerV2 && issuerV2 != OfficialOIDCIssuer {
		return "", "", "", fmt.Errorf("updatecheck: unexpected OIDC issuer %q", issuerV2)
	}
	if hasIssuerV1 && issuerV1 != OfficialOIDCIssuer {
		return "", "", "", fmt.Errorf("updatecheck: unexpected v1 OIDC issuer %q", issuerV1)
	}
	issuer = OfficialOIDCIssuer

	runnerEnv, hasRunner, err := exts.utf8String(oidFulcioRunnerEnvironment)
	if err != nil {
		return "", "", "", err
	}
	if !hasRunner || runnerEnv != OfficialRunnerEnvironment {
		return "", "", "", fmt.Errorf("updatecheck: unexpected Fulcio runner environment %q (want %q)", runnerEnv, OfficialRunnerEnvironment)
	}

	buildSignerURI, hasBuildSigner, err := exts.utf8String(oidFulcioBuildSignerURI)
	if err != nil {
		return "", "", "", err
	}
	if !hasBuildSigner {
		return "", "", "", errors.New("updatecheck: Fulcio certificate missing Build Signer URI extension")
	}

	sanURI := leaf.URIs[0].String()
	if sanURI != buildSignerURI {
		return "", "", "", fmt.Errorf("updatecheck: SAN URI %q does not match Build Signer URI %q", sanURI, buildSignerURI)
	}

	var expectedRef string
	switch {
	case sanURI == tagSignerURI:
		expectedRef = "refs/tags/" + normalizedTag
	case allowWorkflowDispatchMain && sanURI == mainSignerURI:
		triggerV2, hasTriggerV2, err := exts.utf8String(oidFulcioBuildTrigger)
		if err != nil {
			return "", "", "", err
		}
		triggerV1, hasTriggerV1 := exts.rawString(oidFulcioTriggerV1)
		if !hasTriggerV2 && !hasTriggerV1 {
			return "", "", "", errors.New("updatecheck: @refs/heads/main certificate missing workflow trigger extension")
		}
		if (hasTriggerV2 && triggerV2 != "workflow_dispatch") || (hasTriggerV1 && triggerV1 != "workflow_dispatch") {
			return "", "", "", fmt.Errorf("updatecheck: @refs/heads/main certificate requires workflow_dispatch trigger")
		}
		expectedRef = "refs/heads/main"
	default:
		return "", "", "", fmt.Errorf("updatecheck: certificate signer URI %q is not pinned to %q", sanURI, tagSignerURI)
	}

	if sourceRepoURI, ok, err := exts.utf8String(oidFulcioSourceRepositoryURI); err != nil {
		return "", "", "", err
	} else if ok && sourceRepoURI != "https://github.com/"+repo {
		return "", "", "", fmt.Errorf("updatecheck: unexpected source repository URI %q", sourceRepoURI)
	}
	if repoV1, ok := exts.rawString(oidFulcioRepoV1); ok && repoV1 != repo {
		return "", "", "", fmt.Errorf("updatecheck: unexpected v1 workflow repository %q", repoV1)
	}
	if sourceRef, ok, err := exts.utf8String(oidFulcioSourceRepositoryRef); err != nil {
		return "", "", "", err
	} else if ok && sourceRef != expectedRef {
		return "", "", "", fmt.Errorf("updatecheck: source repository ref %q does not match %q", sourceRef, expectedRef)
	}
	if refV1, ok := exts.rawString(oidFulcioRefV1); ok && refV1 != expectedRef {
		return "", "", "", fmt.Errorf("updatecheck: v1 workflow ref %q does not match %q", refV1, expectedRef)
	}
	if buildConfigURI, ok, err := exts.utf8String(oidFulcioBuildConfigURI); err != nil {
		return "", "", "", err
	} else if ok && buildConfigURI != sanURI {
		return "", "", "", fmt.Errorf("updatecheck: build config URI %q does not match %q", buildConfigURI, sanURI)
	}

	return sanURI, issuer, runnerEnv, nil
}

type fulcioExtensionMap map[string][]byte

func collectFulcioExtensions(cert *x509.Certificate) (fulcioExtensionMap, error) {
	out := make(fulcioExtensionMap, len(cert.Extensions))
	for _, ext := range cert.Extensions {
		key := ext.Id.String()
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("updatecheck: duplicate X.509 extension OID %s", key)
		}
		out[key] = ext.Value
	}
	return out, nil
}

func (m fulcioExtensionMap) rawString(oid asn1.ObjectIdentifier) (string, bool) {
	raw, ok := m[oid.String()]
	if !ok {
		return "", false
	}
	return string(raw), true
}

func (m fulcioExtensionMap) utf8String(oid asn1.ObjectIdentifier) (string, bool, error) {
	raw, ok := m[oid.String()]
	if !ok {
		return "", false, nil
	}
	var decoded string
	rest, err := asn1.Unmarshal(raw, &decoded)
	if err != nil || len(rest) != 0 {
		return "", false, fmt.Errorf("updatecheck: malformed ASN.1 string in Fulcio OID %s", oid.String())
	}
	return decoded, true, nil
}

func verifyMessageSignature(
	msgSig *messageSignatureRaw,
	leaf *x509.Certificate,
	leafPub *ecdsa.PublicKey,
	wantDigestHex string,
	wantDigestBytes []byte,
	rekorBodyBytes []byte,
) error {
	if msgSig.MessageDigest == nil {
		return errors.New("updatecheck: messageSignature missing messageDigest")
	}
	alg := strings.ToUpper(strings.TrimSpace(msgSig.MessageDigest.Algorithm))
	if alg != "SHA2_256" && alg != "SHA256" {
		return fmt.Errorf("updatecheck: unsupported messageDigest algorithm %q", msgSig.MessageDigest.Algorithm)
	}
	gotDigestBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(msgSig.MessageDigest.Digest))
	if err != nil || len(gotDigestBytes) != sha256.Size {
		return errors.New("updatecheck: invalid messageDigest.digest base64")
	}
	if !bytes.Equal(gotDigestBytes, wantDigestBytes) {
		return fmt.Errorf("updatecheck: bundle digest %x does not match expected SHA-256 %s", gotDigestBytes, wantDigestHex)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(msgSig.Signature))
	if err != nil || len(sigBytes) == 0 {
		return errors.New("updatecheck: invalid messageSignature.signature base64")
	}
	if !ecdsa.VerifyASN1(leafPub, wantDigestBytes, sigBytes) {
		return errors.New("updatecheck: artifact ECDSA signature verification failed")
	}

	var entry rekorEntryBodyJSON
	if err := json.Unmarshal(rekorBodyBytes, &entry); err != nil {
		return fmt.Errorf("updatecheck: decode Rekor entry body: %w", err)
	}
	if entry.Kind != "hashedrekord" {
		return fmt.Errorf("updatecheck: expected Rekor hashedrekord entry for messageSignature, got %q", entry.Kind)
	}
	var spec hashedRekordSpecJSON
	if err := json.Unmarshal(entry.Spec, &spec); err != nil {
		return fmt.Errorf("updatecheck: decode hashedrekord spec: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(spec.Data.Hash.Algorithm), "sha256") {
		return fmt.Errorf("updatecheck: unexpected Rekor hash algorithm %q", spec.Data.Hash.Algorithm)
	}
	if strings.ToLower(strings.TrimSpace(spec.Data.Hash.Value)) != wantDigestHex {
		return errors.New("updatecheck: Rekor hashedrekord digest does not match expected SHA-256")
	}
	rekorSig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spec.Signature.Content))
	if err != nil || !bytes.Equal(rekorSig, sigBytes) {
		return errors.New("updatecheck: Rekor hashedrekord signature does not match bundle signature")
	}
	rekorCertDER, err := decodeRekorCertPEMBase64(spec.Signature.PublicKey.Content)
	if err != nil || !bytes.Equal(rekorCertDER, leaf.Raw) {
		return errors.New("updatecheck: Rekor hashedrekord certificate does not match bundle leaf certificate")
	}
	return nil
}

func verifyDSSEEnvelope(
	env *dsseEnvelopeRaw,
	leaf *x509.Certificate,
	leafPub *ecdsa.PublicKey,
	wantDigestHex string,
	artifactName string,
	rekorBodyBytes []byte,
) error {
	if strings.TrimSpace(env.PayloadType) != inTotoPayloadType {
		return fmt.Errorf("updatecheck: unsupported DSSE payloadType %q", env.PayloadType)
	}
	payloadBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.Payload))
	if err != nil || len(payloadBytes) == 0 {
		return errors.New("updatecheck: invalid DSSE payload base64")
	}
	if len(env.Signatures) == 0 {
		return errors.New("updatecheck: DSSE envelope has no signatures")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.Signatures[0].Sig))
	if err != nil || len(sigBytes) == 0 {
		return errors.New("updatecheck: invalid DSSE signature base64")
	}

	pae := dssePAE(env.PayloadType, payloadBytes)
	paeSum := sha256.Sum256(pae)
	if !ecdsa.VerifyASN1(leafPub, paeSum[:], sigBytes) {
		return errors.New("updatecheck: DSSE PAE ECDSA signature verification failed")
	}

	var stmt inTotoStatementJSON
	if err := json.Unmarshal(payloadBytes, &stmt); err != nil {
		return fmt.Errorf("updatecheck: decode in-toto statement: %w", err)
	}
	if stmt.Type != inTotoStatementV1 && stmt.Type != inTotoStatementV01 {
		return fmt.Errorf("updatecheck: unsupported in-toto statement _type %q", stmt.Type)
	}
	wantBase := ""
	if strings.TrimSpace(artifactName) != "" {
		wantBase = filepath.Base(strings.TrimSpace(artifactName))
	}
	matchedSubject := false
	for _, subj := range stmt.Subject {
		if wantBase != "" && filepath.Base(strings.TrimSpace(subj.Name)) != wantBase {
			continue
		}
		if strings.ToLower(strings.TrimSpace(subj.Digest["sha256"])) == wantDigestHex {
			matchedSubject = true
			break
		}
	}
	if !matchedSubject {
		return fmt.Errorf("updatecheck: in-toto subjects do not match expected SHA-256 %s", wantDigestHex)
	}

	payloadHash := sha256.Sum256(payloadBytes)
	payloadHashHex := hex.EncodeToString(payloadHash[:])
	return verifyRekorDSSEBinding(rekorBodyBytes, payloadHashHex, sigBytes, leaf.Raw)
}

func verifyRekorDSSEBinding(rekorBodyBytes []byte, wantPayloadHashHex string, sigBytes, leafDER []byte) error {
	var entry rekorEntryBodyJSON
	if err := json.Unmarshal(rekorBodyBytes, &entry); err != nil {
		return fmt.Errorf("updatecheck: decode Rekor DSSE entry: %w", err)
	}
	var spec dsseRekorSpecJSON
	if err := json.Unmarshal(entry.Spec, &spec); err != nil {
		return fmt.Errorf("updatecheck: decode Rekor DSSE spec: %w", err)
	}

	switch entry.Kind {
	case "dsse":
		if spec.PayloadHash == nil || !strings.EqualFold(spec.PayloadHash.Algorithm, "sha256") ||
			strings.ToLower(strings.TrimSpace(spec.PayloadHash.Value)) != wantPayloadHashHex {
			return errors.New("updatecheck: Rekor dsse payloadHash mismatch")
		}
		if len(spec.Signatures) == 0 {
			return errors.New("updatecheck: Rekor dsse entry has no signatures")
		}
		rekorSig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spec.Signatures[0].Signature))
		if err != nil || !bytes.Equal(rekorSig, sigBytes) {
			return errors.New("updatecheck: Rekor dsse signature mismatch")
		}
		rekorCertDER, err := decodeRekorCertPEMBase64(spec.Signatures[0].Verifier)
		if err != nil || !bytes.Equal(rekorCertDER, leafDER) {
			return errors.New("updatecheck: Rekor dsse verifier certificate mismatch")
		}
		return nil
	case "intoto":
		if spec.Content == nil || spec.Content.PayloadHash == nil ||
			!strings.EqualFold(spec.Content.PayloadHash.Algorithm, "sha256") ||
			strings.ToLower(strings.TrimSpace(spec.Content.PayloadHash.Value)) != wantPayloadHashHex {
			return errors.New("updatecheck: Rekor intoto payloadHash mismatch")
		}
		var certB64 string
		if spec.Content.Envelope != nil && len(spec.Content.Envelope.Signatures) > 0 {
			sigRaw := strings.TrimSpace(spec.Content.Envelope.Signatures[0].Sig)
			rekorSig, err := base64.StdEncoding.DecodeString(sigRaw)
			if err != nil {
				return errors.New("updatecheck: invalid Rekor intoto signature")
			}
			// intoto v0.0.2 double-base64 encodes sig/publicKey in some versions; unwrap once more if needed.
			if !bytes.Equal(rekorSig, sigBytes) {
				if inner, err2 := base64.StdEncoding.DecodeString(string(rekorSig)); err2 == nil {
					rekorSig = inner
				}
			}
			if !bytes.Equal(rekorSig, sigBytes) {
				return errors.New("updatecheck: Rekor intoto signature mismatch")
			}
			certB64 = spec.Content.Envelope.Signatures[0].PublicKey
		} else {
			certB64 = spec.PublicKey
		}
		rekorCertDER, err := decodeRekorCertPEMBase64(certB64)
		if err != nil || !bytes.Equal(rekorCertDER, leafDER) {
			return errors.New("updatecheck: Rekor intoto certificate mismatch")
		}
		return nil
	default:
		return fmt.Errorf("updatecheck: unsupported Rekor entry kind %q for DSSE bundle", entry.Kind)
	}
}

func dssePAE(payloadType string, payload []byte) []byte {
	header := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	out := make([]byte, 0, len(header)+len(payload))
	out = append(out, header...)
	out = append(out, payload...)
	return out
}

func decodeRekorCertPEMBase64(b64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(decoded)
	if block == nil {
		// Handle double-base64 encoded PEM in legacy intoto specs.
		if inner, err2 := base64.StdEncoding.DecodeString(strings.TrimSpace(string(decoded))); err2 == nil {
			block, rest = pem.Decode(inner)
		}
	}
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("expected CERTIFICATE PEM block in Rekor entry")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("unexpected trailing data after Rekor CERTIFICATE PEM")
	}
	return block.Bytes, nil
}

func parseJSONInt64(raw json.RawMessage) (int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, errors.New("empty value")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	}
	return strconv.ParseInt(string(trimmed), 10, 64)
}

func normalizeSHA256Hex(raw string) (string, []byte, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != sha256.Size*2 {
		return "", nil, fmt.Errorf("expected %d hex characters, got %d", sha256.Size*2, len(s))
	}
	decoded, err := hex.DecodeString(s)
	if err != nil || len(decoded) != sha256.Size {
		return "", nil, errors.New("malformed hexadecimal SHA-256")
	}
	return s, decoded, nil
}
