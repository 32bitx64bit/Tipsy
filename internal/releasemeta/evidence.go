package releasemeta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type artifactManifest struct {
	Artifacts []manifestArtifact `json:"artifacts"`
	Format    string             `json:"format"`
}

type manifestArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type provenanceStatement struct {
	Type          string              `json:"_type"`
	PredicateType string              `json:"predicateType"`
	Subject       []provenanceSubject `json:"subject"`
}

type provenanceSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type spdxDocument struct {
	SPDXVersion string            `json:"spdxVersion"`
	Packages    []json.RawMessage `json:"packages"`
}

type rebuildEvidence struct {
	Schema         string   `json:"schema"`
	ArtifactSHA256 string   `json:"artifact_sha256"`
	Builders       []string `json:"builders"`
	ByteIdentical  bool     `json:"byte_identical"`
}

type sigstoreEvidence struct {
	Schema          string `json:"schema"`
	ArtifactSHA256  string `json:"artifact_sha256"`
	BundleMediaType string `json:"bundle_media_type"`
	Issuer          string `json:"issuer"`
	Repository      string `json:"repository"`
	Workflow        string `json:"workflow"`
	SourceCommit    string `json:"source_commit"`
	InclusionProof  bool   `json:"inclusion_proof"`
	OfflineVerified bool   `json:"offline_verified"`
}

func (s sigstoreEvidence) productionBound() bool {
	return ProductionSigstoreVerifierEnabled && ProductionSigstoreIssuer != "" && ProductionSigstoreRepository != "" && ProductionSigstoreWorkflow != "" &&
		s.Issuer == ProductionSigstoreIssuer && s.Repository == ProductionSigstoreRepository && s.Workflow == ProductionSigstoreWorkflow && s.OfflineVerified
}

func verifyTargetBytes(info *metadata.TargetFiles, data []byte, expectedSHA string) error {
	if info == nil || info.Length < 0 || info.Length > MaxArtifactBytes {
		return fail(ReasonTargetMetadata, "invalid target length")
	}
	if err := info.VerifyLengthHashes(data); err != nil {
		return wrap(ReasonArtifactMismatch, err, "TUF length/hash mismatch")
	}
	shaValue, ok := info.Hashes["sha256"]
	if !ok || fmt.Sprintf("%x", []byte(shaValue)) != expectedSHA {
		return fail(ReasonEvidenceMismatch, "custom digest is not bound to the TUF target digest")
	}
	return nil
}

func validateEvidence(kind string, data []byte, release ReleaseTarget, artifactName, artifactSHA string, artifactSize int64) error {
	if len(data) == 0 || int64(len(data)) > MaxEvidenceBytes {
		return fail(ReasonEvidenceMalformed, "%s size", kind)
	}
	if err := checkJSONDepth(data, MaxMetadataDepth); err != nil {
		return wrap(ReasonEvidenceMalformed, err, kind+" JSON")
	}
	switch kind {
	case "manifest":
		var manifest artifactManifest
		if err := decodeJSON(data, &manifest); err != nil || manifest.Format != "tipsy.artifact-manifest.v1" || len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > 16 {
			return fail(ReasonEvidenceMalformed, "invalid artifact manifest")
		}
		found := false
		for _, artifact := range manifest.Artifacts {
			if artifact.Name == artifactName && artifact.SHA256 == artifactSHA && artifact.Size == artifactSize {
				found = true
			}
		}
		if !found {
			return fail(ReasonEvidenceMismatch, "manifest does not name artifact")
		}
	case "sbom":
		var document spdxDocument
		if err := decodeJSON(data, &document); err != nil || document.SPDXVersion != "SPDX-2.3" || len(document.Packages) == 0 || len(document.Packages) > 256 {
			return fail(ReasonEvidenceMalformed, "invalid SPDX 2.3 document")
		}
	case "provenance":
		var statement provenanceStatement
		if err := decodeJSON(data, &statement); err != nil || statement.Type != "https://in-toto.io/Statement/v1" || statement.PredicateType != "https://slsa.dev/provenance/v1" || len(statement.Subject) == 0 || len(statement.Subject) > 16 {
			return fail(ReasonEvidenceMalformed, "invalid SLSA provenance")
		}
		found := false
		for _, subject := range statement.Subject {
			if subject.Name == artifactName && subject.Digest["sha256"] == artifactSHA {
				found = true
			}
		}
		if !found {
			return fail(ReasonEvidenceMismatch, "provenance subject mismatch")
		}
	case "rebuild":
		var evidence rebuildEvidence
		if err := decodeCanonicalJSON(data, &evidence); err != nil || evidence.Schema != "tipsy.rebuild.v1" || evidence.ArtifactSHA256 != artifactSHA || !evidence.ByteIdentical || len(evidence.Builders) < 2 || len(evidence.Builders) > 8 {
			return fail(ReasonEvidenceMalformed, "invalid independent rebuild evidence")
		}
		if !slices.IsSorted(evidence.Builders) || hasDuplicate(evidence.Builders) {
			return fail(ReasonEvidenceMalformed, "rebuild builders must be sorted and distinct")
		}
	case "sigstore":
		// P2 stores a bounded admission record next to the self-contained bundle.
		// TUF authenticates this evidence; H0 must later bind the production
		// Fulcio/Rekor roots and identity policy before OfficialVerified is possible.
		var evidence sigstoreEvidence
		if err := decodeCanonicalJSON(data, &evidence); err != nil || evidence.Schema != "tipsy.sigstore-admission.v1" ||
			evidence.ArtifactSHA256 != artifactSHA || evidence.SourceCommit != release.SourceCommit || evidence.Repository != release.SourceRepository ||
			evidence.BundleMediaType != "application/vnd.dev.sigstore.bundle.v0.3+json" || evidence.Issuer == "" || evidence.Workflow == "" ||
			!evidence.InclusionProof || !evidence.OfflineVerified {
			return fail(ReasonEvidenceMalformed, "invalid Sigstore admission evidence")
		}
	default:
		return fail(ReasonEvidenceMalformed, "unknown evidence kind")
	}
	return nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func decodeCanonicalJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	canonical, err := json.Marshal(destination)
	if err != nil || !bytes.Equal(data, canonical) {
		return fmt.Errorf("noncanonical JSON")
	}
	return nil
}

func artifactBaseName(name string) string {
	return filepath.Base(strings.ReplaceAll(name, "\\", "/"))
}
