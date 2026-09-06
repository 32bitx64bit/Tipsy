package releasemeta

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
	"github.com/tipsy-linux/tipsy/internal/securitypolicy"
)

func VerifyLocal(options VerifyOptions) (Result, error) {
	result := Result{State: DevelopmentUnrestricted, Reason: ReasonDevelopment}
	if len(options.InitialRoot) == 0 {
		return result, fail(ReasonBootstrapMissing, "no externally supplied or embedded root")
	}
	if len(options.InitialRoot) > MaxRootBytes {
		return result, fail(ReasonMetadataTooLarge, "initial root exceeds %d bytes", MaxRootBytes)
	}
	if options.Channel == "" {
		options.Channel = "stable"
	}
	if !oneOf(options.Channel, "stable", "beta") {
		return result, fail(ReasonChannelMismatch, "unsupported channel")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if options.VerifierVersion == "" {
		options.VerifierVersion = VerifierVersion
	}
	if options.StateDir == "" {
		return result, fail(ReasonStateUnsafe, "state directory is required")
	}
	previous, firstState, err := loadRollbackState(options.StateDir)
	if err != nil {
		return result, err
	}
	fetcher, err := newLocalFetcher(options.MetadataDir)
	if err != nil {
		return result, wrap(ReasonUnsafePath, err, "open metadata directory")
	}
	if err := checkJSONDepth(options.InitialRoot, MaxMetadataDepth); err != nil {
		return result, err
	}
	configuration, err := config.New("https://local.tipsy.invalid/metadata", options.InitialRoot)
	if err != nil {
		return result, wrap(ReasonRootInvalid, err, "configure TUF updater")
	}
	configuration.Fetcher = fetcher
	configuration.DisableLocalCache = true
	configuration.MaxRootRotations = 32
	configuration.MaxDelegations = 12
	configuration.RootMaxLength = MaxRootBytes
	configuration.TimestampMaxLength = MaxTimestampBytes
	configuration.SnapshotMaxLength = MaxSnapshotBytes
	configuration.TargetsMaxLength = MaxTargetsBytes
	configuration.PrefixTargetsWithHash = true
	client, err := updater.New(configuration)
	if err != nil {
		return result, wrap(CodeOf(err), err, "load initial TUF root")
	}
	client.UnsafeSetRefTime(options.Now.UTC())
	if err := client.Refresh(); err != nil {
		return result, wrap(CodeOf(err), err, "refresh TUF metadata")
	}
	if err := validateRepositoryLayout(client.GetTrustedMetadataSet()); err != nil {
		return result, err
	}
	if err := validateTargetPath(options.TargetPath, options.Channel+"/linux/x86_64/"); err != nil {
		return result, err
	}
	artifactInfo, err := client.GetTargetInfo(options.TargetPath)
	if err != nil {
		return result, wrap(CodeOf(err), err, "resolve release target")
	}
	if err := requireTargetOwner(client.GetTrustedMetadataSet(), options.TargetPath, options.Channel); err != nil {
		return result, err
	}
	release, err := decodeReleaseTarget(artifactInfo.Custom, options.TargetPath, options.Channel)
	if err != nil {
		return result, err
	}
	if !versionAtLeast(options.VerifierVersion, release.MinVerifierVersion) {
		return result, fail(ReasonTargetMetadata, "verifier %s is below required %s", options.VerifierVersion, release.MinVerifierVersion)
	}
	if previous.ReleaseSequences[options.Channel] > release.ReleaseSequence {
		return result, fail(ReasonStateRollback, "release sequence %d is below %d", release.ReleaseSequence, previous.ReleaseSequences[options.Channel])
	}
	artifactSHA, artifactSize, err := hashArtifactNoFollow(options.ArtifactPath, MaxArtifactBytes)
	if err != nil {
		return result, wrap(ReasonArtifactMismatch, err, "read artifact")
	}
	if err := verifyArtifactInfo(artifactInfo, artifactSize, artifactSHA); err != nil {
		return result, err
	}
	if !strings.HasPrefix(filepath.Base(options.TargetPath), artifactSHA+".") {
		return result, fail(ReasonTargetMetadata, "artifact target is not content-addressed")
	}
	refs := []struct {
		kind  string
		ref   EvidenceRef
		owner string
	}{
		{"manifest", release.Manifest, options.Channel},
		{"sbom", release.SBOM, options.Channel},
		{"provenance", release.Provenance, options.Channel},
		{"rebuild", release.Rebuild, options.Channel},
		{"sigstore", release.Sigstore, options.Channel},
		{"roblox_policy", release.RobloxPolicy, "roblox-policy"},
		{"runtime_policy", release.RuntimePolicy, "runtime-policy"},
	}
	var robloxSequence, runtimeSequence uint64
	productionEvidenceBound := false
	for _, item := range refs {
		info, lookupErr := client.GetTargetInfo(item.ref.Target)
		if lookupErr != nil {
			return result, wrap(CodeOf(lookupErr), lookupErr, "resolve "+item.kind)
		}
		if ownerErr := requireTargetOwner(client.GetTrustedMetadataSet(), item.ref.Target, item.owner); ownerErr != nil {
			return result, ownerErr
		}
		data, digest, readErr := readTargetBeneath(options.TargetsDir, item.ref.Target, MaxEvidenceBytes)
		if readErr != nil {
			return result, wrap(ReasonEvidenceMissing, readErr, "read "+item.kind)
		}
		if digest != item.ref.SHA256 {
			return result, fail(ReasonEvidenceMismatch, "%s digest differs from release target", item.kind)
		}
		if !strings.HasPrefix(filepath.Base(item.ref.Target), digest+".") {
			return result, fail(ReasonTargetMetadata, "%s target is not content-addressed", item.kind)
		}
		if verifyErr := verifyTargetBytes(info, data, item.ref.SHA256); verifyErr != nil {
			return result, verifyErr
		}
		switch item.kind {
		case "roblox_policy":
			policy, policyErr := securitypolicy.DecodeRoblox(data, options.Now, previous.PolicySequences["roblox"])
			if policyErr != nil {
				return result, wrap(ReasonPolicyInvalid, policyErr, "Roblox policy")
			}
			robloxSequence = policy.Validity.Sequence
		case "runtime_policy":
			policy, policyErr := securitypolicy.DecodeRuntime(data, options.Now, previous.PolicySequences["runtime"])
			if policyErr != nil {
				return result, wrap(ReasonPolicyInvalid, policyErr, "runtime policy")
			}
			runtimeSequence = policy.Validity.Sequence
		default:
			if evidenceErr := validateEvidence(item.kind, data, release, artifactBaseName(options.ArtifactPath), artifactSHA, artifactSize); evidenceErr != nil {
				return result, evidenceErr
			}
			if item.kind == "sigstore" {
				var admission sigstoreEvidence
				if decodeErr := decodeCanonicalJSON(data, &admission); decodeErr == nil {
					productionEvidenceBound = admission.productionBound()
				}
			}
		}
		result.Evidence = append(result.Evidence, EvidenceStatus{Target: item.ref.Target, SHA256: digest, Kind: item.kind})
	}
	trusted := client.GetTrustedMetadataSet()
	nextState, err := reconcileRollbackState(previous, trusted, fetcher, options.InitialRoot, options.Channel, release.ReleaseSequence, robloxSequence, runtimeSequence)
	if err != nil {
		return result, err
	}
	if err := persistRollbackState(options.StateDir, nextState, firstState); err != nil {
		return result, err
	}
	rootDigest := sha256.Sum256(options.InitialRoot)
	rootBound := ProductionRootSHA256 != "" && hex.EncodeToString(rootDigest[:]) == ProductionRootSHA256
	result = Result{
		State: DevelopmentUnrestricted, Reason: ReasonDevelopment, Verified: true,
		Target: options.TargetPath, Version: release.Version, ReleaseSequence: release.ReleaseSequence,
		RootVersion: trusted.Root.Signed.Version, SnapshotVersion: trusted.Snapshot.Signed.Version,
		TargetsVersion: trusted.Targets[metadata.TARGETS].Signed.Version, Evidence: result.Evidence,
		ProductionRootBound:     rootBound,
		ProductionEvidenceBound: productionEvidenceBound,
	}
	if rootBound && productionEvidenceBound {
		result.State = OfficialVerified
		result.Reason = ReasonOK
	}
	return result, nil
}

func verifyArtifactInfo(info *metadata.TargetFiles, size int64, digest string) error {
	if info == nil || info.Length <= 0 || info.Length > MaxArtifactBytes {
		return fail(ReasonTargetMetadata, "invalid artifact target length")
	}
	shaValue, ok := info.Hashes["sha256"]
	if !ok || len(info.Hashes) != 1 {
		return fail(ReasonTargetMetadata, "artifact target must use only sha256")
	}
	if info.Length != size || fmt.Sprintf("%x", []byte(shaValue)) != digest {
		return fail(ReasonArtifactMismatch, "artifact length/hash mismatch")
	}
	return nil
}

func DefaultStateDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		if !filepath.IsAbs(state) {
			return "", errors.New("XDG_STATE_HOME must be absolute")
		}
		return filepath.Join(state, "tipsy", "release-verifier"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "tipsy", "release-verifier"), nil
}

func ReadInitialRoot(name string) ([]byte, error) {
	data, _, err := readArtifactNoFollow(name, MaxRootBytes)
	if err != nil {
		return nil, wrap(ReasonRootInvalid, err, "read initial root")
	}
	return data, nil
}

func DescribeError(err error) string {
	if err == nil {
		return "verification succeeded"
	}
	if CodeOf(err) != ReasonInternal {
		return err.Error()
	}
	return fmt.Sprintf("%s: %v", ReasonInternal, err)
}

func versionAtLeast(current, required string) bool {
	parse := func(value string) ([3]uint64, bool, bool) {
		var result [3]uint64
		parts := strings.SplitN(value, "-", 2)
		numbers := strings.Split(parts[0], ".")
		if len(numbers) != 3 {
			return result, false, false
		}
		for i, number := range numbers {
			parsed, err := strconv.ParseUint(number, 10, 64)
			if err != nil {
				return result, false, false
			}
			result[i] = parsed
		}
		return result, len(parts) == 2, true
	}
	left, leftPre, leftOK := parse(current)
	right, rightPre, rightOK := parse(required)
	if !leftOK || !rightOK {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	if leftPre != rightPre {
		return !leftPre
	}
	return current >= required
}
