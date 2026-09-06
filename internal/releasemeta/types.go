package releasemeta

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxRootBytes      = 128 << 10
	MaxTimestampBytes = 32 << 10
	MaxSnapshotBytes  = 256 << 10
	MaxTargetsBytes   = 256 << 10
	MaxArtifactBytes  = int64(1 << 30)
	MaxEvidenceBytes  = int64(16 << 20)
	MaxMetadataDepth  = 32
)

// ProductionRootSHA256 remains intentionally empty until H0 approves and
// publishes a production root. A supplied development root can verify bytes,
// but can never grant OfficialVerified while this value is empty.
const ProductionRootSHA256 = ""

const (
	VerifierVersion                   = "0.1.0"
	ProductionSigstoreIssuer          = ""
	ProductionSigstoreRepository      = ""
	ProductionSigstoreWorkflow        = ""
	ProductionSigstoreVerifierEnabled = false
)

type TrustState string

const (
	DevelopmentUnrestricted TrustState = "DevelopmentUnrestricted"
	OfficialVerified        TrustState = "OfficialVerified"
)

type EvidenceRef struct {
	Target string `json:"target"`
	SHA256 string `json:"sha256"`
}

type ReleaseTarget struct {
	Schema             string      `json:"schema"`
	Product            string      `json:"product"`
	Channel            string      `json:"channel"`
	Platform           string      `json:"platform"`
	Architecture       string      `json:"architecture"`
	Format             string      `json:"format"`
	Version            string      `json:"version"`
	ReleaseSequence    uint64      `json:"release_sequence"`
	SourceRepository   string      `json:"source_repository"`
	SourceCommit       string      `json:"source_commit"`
	SourceTag          string      `json:"source_tag"`
	MinVerifierVersion string      `json:"min_verifier_version"`
	Manifest           EvidenceRef `json:"manifest"`
	SBOM               EvidenceRef `json:"sbom"`
	Provenance         EvidenceRef `json:"provenance"`
	Rebuild            EvidenceRef `json:"rebuild"`
	Sigstore           EvidenceRef `json:"sigstore"`
	RobloxPolicy       EvidenceRef `json:"roblox_policy"`
	RuntimePolicy      EvidenceRef `json:"runtime_policy"`
}

type VerifyOptions struct {
	InitialRoot     []byte
	MetadataDir     string
	TargetsDir      string
	ArtifactPath    string
	TargetPath      string
	Channel         string
	StateDir        string
	Now             time.Time
	VerifierVersion string
}

type EvidenceStatus struct {
	Target string `json:"target"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}

type Result struct {
	State                   TrustState       `json:"state"`
	Reason                  ReasonCode       `json:"reason"`
	Verified                bool             `json:"verified"`
	Target                  string           `json:"target,omitempty"`
	Version                 string           `json:"version,omitempty"`
	ReleaseSequence         uint64           `json:"release_sequence,omitempty"`
	RootVersion             int64            `json:"root_version,omitempty"`
	SnapshotVersion         int64            `json:"snapshot_version,omitempty"`
	TargetsVersion          int64            `json:"targets_version,omitempty"`
	Evidence                []EvidenceStatus `json:"evidence,omitempty"`
	ProductionRootBound     bool             `json:"production_root_bound"`
	ProductionEvidenceBound bool             `json:"production_evidence_bound"`
}

var (
	releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)
	hexCommitPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	repositoryPattern     = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]{1,64}/[A-Za-z0-9_.-]{1,64}$`)
)

func decodeReleaseTarget(raw *json.RawMessage, expectedPath, expectedChannel string) (ReleaseTarget, error) {
	var target ReleaseTarget
	if raw == nil || len(*raw) == 0 || len(*raw) > 32<<10 {
		return target, fail(ReasonTargetMetadata, "missing or oversized custom metadata")
	}
	decoder := json.NewDecoder(bytes.NewReader(*raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return target, wrap(ReasonTargetMetadata, err, "decode release target")
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return target, fail(ReasonTargetMetadata, "trailing custom metadata")
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, *raw) {
		return target, fail(ReasonTargetMetadata, "custom metadata is not canonical")
	}
	if target.Schema != "tipsy.release-target.v1" || target.Product != "tipsy" ||
		target.Platform != "linux" || target.Architecture != "x86_64" || target.ReleaseSequence == 0 {
		return target, fail(ReasonTargetMetadata, "unexpected schema/product/platform/architecture/sequence")
	}
	if target.Channel != expectedChannel || !oneOf(target.Channel, "stable", "beta") {
		return target, fail(ReasonChannelMismatch, "target channel %q", target.Channel)
	}
	if !oneOf(target.Format, "appimage", "appdir-tar") || !releaseVersionPattern.MatchString(target.Version) ||
		target.SourceTag != "v"+target.Version || !hexCommitPattern.MatchString(target.SourceCommit) ||
		!repositoryPattern.MatchString(target.SourceRepository) || !releaseVersionPattern.MatchString(target.MinVerifierVersion) {
		return target, fail(ReasonTargetMetadata, "invalid release identity fields")
	}
	expectedPrefix := expectedChannel + "/linux/x86_64/"
	if err := validateTargetPath(expectedPath, expectedPrefix); err != nil {
		return target, err
	}
	refs := []struct {
		name   string
		ref    EvidenceRef
		prefix string
	}{
		{"manifest", target.Manifest, expectedChannel + "/evidence/"},
		{"sbom", target.SBOM, expectedChannel + "/evidence/"},
		{"provenance", target.Provenance, expectedChannel + "/evidence/"},
		{"rebuild", target.Rebuild, expectedChannel + "/evidence/"},
		{"sigstore", target.Sigstore, expectedChannel + "/evidence/"},
		{"roblox_policy", target.RobloxPolicy, "policies/roblox/"},
		{"runtime_policy", target.RuntimePolicy, "policies/runtime/"},
	}
	seen := map[string]bool{}
	for _, item := range refs {
		if err := validateTargetPath(item.ref.Target, item.prefix); err != nil {
			return target, fail(ReasonTargetMetadata, "%s target: %v", item.name, err)
		}
		if !validSHA256(item.ref.SHA256) || seen[item.ref.Target] {
			return target, fail(ReasonTargetMetadata, "%s digest/path", item.name)
		}
		seen[item.ref.Target] = true
	}
	return target, nil
}

func validateTargetPath(target, prefix string) error {
	if target == "" || len(target) > 240 || !utf8.ValidString(target) || strings.ContainsAny(target, "\\\x00") ||
		strings.HasPrefix(target, "/") || path.Clean(target) != target || !strings.HasPrefix(target, prefix) {
		return fail(ReasonUnsafePath, "unsafe target path")
	}
	for _, component := range strings.Split(target, "/") {
		if component == "" || component == "." || component == ".." || len(component) > 96 {
			return fail(ReasonUnsafePath, "unsafe target path component")
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (r Result) Summary() string {
	if r.Verified {
		return fmt.Sprintf("%s: verified %s (%s, sequence %d)", r.State, r.Target, r.Version, r.ReleaseSequence)
	}
	return fmt.Sprintf("%s: %s", r.State, r.Reason)
}
