package releasemeta

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/tipsy-linux/tipsy/internal/securitypolicy"
)

type fixtureRepository struct {
	t              *testing.T
	root           string
	metadataDir    string
	targetsDir     string
	stateDir       string
	artifactPath   string
	targetPath     string
	now            time.Time
	rootV1         []byte
	rootMetadata   *metadata.Metadata[metadata.RootType]
	targets        map[string]*metadata.Metadata[metadata.TargetsType]
	snapshot       *metadata.Metadata[metadata.SnapshotType]
	timestamp      *metadata.Metadata[metadata.TimestampType]
	keys           map[string][]ed25519.PrivateKey
	targetContents map[string][]byte
}

func newFixtureRepository(t *testing.T) *fixtureRepository {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	repository := &fixtureRepository{
		t: t, root: root, metadataDir: filepath.Join(root, "metadata"), targetsDir: filepath.Join(root, "targets"),
		stateDir: filepath.Join(root, "state"), now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
		targets: map[string]*metadata.Metadata[metadata.TargetsType]{}, keys: map[string][]ed25519.PrivateKey{}, targetContents: map[string][]byte{},
	}
	for _, dir := range []string{repository.metadataDir, repository.targetsDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for role, count := range map[string]int{
		"root": 5, "targets": 3, "snapshot": 1, "timestamp": 1,
		"stable": 3, "beta": 2, "recovery": 3, "roblox-policy": 3, "runtime-policy": 3,
	} {
		for range count {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			repository.keys[role] = append(repository.keys[role], private)
		}
	}
	t.Cleanup(func() {
		for _, keys := range repository.keys {
			for _, key := range keys {
				clear(key)
			}
		}
	})
	repository.construct()
	return repository
}

func (r *fixtureRepository) construct() {
	t := r.t
	artifact := []byte("Tipsy deterministic fixture artifact\n")
	artifactDigest := digest(artifact)
	r.targetPath = "stable/linux/x86_64/" + artifactDigest + ".Tipsy-1.2.3-x86_64.AppImage"
	r.artifactPath = filepath.Join(r.root, "Tipsy-1.2.3-x86_64.AppImage")
	if err := os.WriteFile(r.artifactPath, artifact, 0644); err != nil {
		t.Fatal(err)
	}

	validity := securitypolicy.Validity{Sequence: 4, NotBefore: r.now.Add(-time.Hour).Format(time.RFC3339), Expires: r.now.Add(24 * time.Hour).Format(time.RFC3339)}
	roblox := securitypolicy.RobloxPolicy{
		Schema: "tipsy.roblox-policy.v1", Validity: validity, PackageName: "com.roblox.client", Platform: "android", Architecture: "x86_64",
		MinVersionCode: 100, MaxVersionCode: 200, AllowedSplits: []string{"base", "config.x86_64"},
		SignerLineages: []securitypolicy.SignerLineage{{ID: "roblox-main", SHA256: []string{strings.Repeat("1", 64)}, MinVersionCode: 100, MaxVersionCode: 200}},
	}
	runtime := securitypolicy.RuntimePolicy{
		Schema: "tipsy.runtime-policy.v1", Validity: validity, MinimumTipsyVersion: "1.2.3",
		RequiredCapabilities: []string{"no-new-privs", "wx"},
		Selectors:            []securitypolicy.RuntimeSelector{{ReleaseClass: "stable", RobloxClass: "authorized", KernelClass: "modern", BackendClass: "vulkan", ProfileID: "linux-strict-v1"}},
	}
	manifest := map[string]any{"artifacts": []map[string]any{{"name": filepath.Base(r.artifactPath), "sha256": artifactDigest, "size": len(artifact)}}, "format": "tipsy.artifact-manifest.v1"}
	spdx := map[string]any{"SPDXID": "SPDXRef-DOCUMENT", "packages": []map[string]any{{"name": "Tipsy"}}, "spdxVersion": "SPDX-2.3"}
	provenance := map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicate": map[string]any{}, "predicateType": "https://slsa.dev/provenance/v1", "subject": []map[string]any{{"digest": map[string]string{"sha256": artifactDigest}, "name": filepath.Base(r.artifactPath)}}}
	rebuild := rebuildEvidence{Schema: "tipsy.rebuild.v1", ArtifactSHA256: artifactDigest, Builders: []string{"builder-a", "builder-b"}, ByteIdentical: true}
	sigstore := sigstoreEvidence{Schema: "tipsy.sigstore-admission.v1", ArtifactSHA256: artifactDigest, BundleMediaType: "application/vnd.dev.sigstore.bundle.v0.3+json", Issuer: "https://token.actions.githubusercontent.com", Repository: "https://github.com/32bitx64bit/Tipsy", Workflow: ".github/workflows/release.yml", SourceCommit: strings.Repeat("a", 40), InclusionProof: true, OfflineVerified: true}

	evidence := map[string][]byte{
		"manifest": canonicalFixture(t, manifest), "sbom": canonicalFixture(t, spdx), "provenance": canonicalFixture(t, provenance),
		"rebuild": canonicalFixture(t, rebuild), "sigstore": canonicalFixture(t, sigstore),
		"roblox_policy": canonicalFixture(t, roblox), "runtime_policy": canonicalFixture(t, runtime),
	}
	paths := map[string]string{}
	for kind, data := range evidence {
		var target string
		switch kind {
		case "roblox_policy":
			target = "policies/roblox/" + digest(data) + ".json"
		case "runtime_policy":
			target = "policies/runtime/" + digest(data) + ".json"
		default:
			target = "stable/evidence/1.2.3/" + digest(data) + "." + kind + ".json"
		}
		paths[kind] = target
		r.targetContents[target] = data
		r.writeTarget(target, data)
	}

	release := ReleaseTarget{
		Schema: "tipsy.release-target.v1", Product: "tipsy", Channel: "stable", Platform: "linux", Architecture: "x86_64",
		Format: "appimage", Version: "1.2.3", ReleaseSequence: 12, SourceRepository: "https://github.com/32bitx64bit/Tipsy",
		SourceCommit: strings.Repeat("a", 40), SourceTag: "v1.2.3", MinVerifierVersion: "0.1.0",
		Manifest: ref(paths["manifest"], evidence["manifest"]), SBOM: ref(paths["sbom"], evidence["sbom"]),
		Provenance: ref(paths["provenance"], evidence["provenance"]), Rebuild: ref(paths["rebuild"], evidence["rebuild"]),
		Sigstore: ref(paths["sigstore"], evidence["sigstore"]), RobloxPolicy: ref(paths["roblox_policy"], evidence["roblox_policy"]),
		RuntimePolicy: ref(paths["runtime_policy"], evidence["runtime_policy"]),
	}

	for _, role := range []string{"targets", "stable", "beta", "recovery", "roblox-policy", "runtime-policy"} {
		r.targets[role] = metadata.Targets(r.now.Add(30 * 24 * time.Hour))
	}
	artifactInfo, err := metadata.TargetFile().FromBytes(r.targetPath, artifact, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	custom := json.RawMessage(canonicalFixture(t, release))
	artifactInfo.Custom = &custom
	r.targets["stable"].Signed.Targets[r.targetPath] = artifactInfo
	for kind, target := range paths {
		info, err := metadata.TargetFile().FromBytes(target, evidence[kind], "sha256")
		if err != nil {
			t.Fatal(err)
		}
		role := "stable"
		if kind == "roblox_policy" {
			role = "roblox-policy"
		} else if kind == "runtime_policy" {
			role = "runtime-policy"
		}
		r.targets[role].Signed.Targets[target] = info
	}

	rootV1 := metadata.Root(r.now.Add(365 * 24 * time.Hour))
	rootV1.Signed.ConsistentSnapshot = true
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		for _, key := range r.keys[role] {
			public, err := metadata.KeyFromPublicKey(key.Public())
			if err != nil {
				t.Fatal(err)
			}
			if err := rootV1.Signed.AddKey(public, role); err != nil {
				t.Fatal(err)
			}
		}
	}
	rootV1.Signed.Roles["root"].Threshold = 3
	rootV1.Signed.Roles["targets"].Threshold = 2
	signRoot(t, rootV1, r.keys["root"])
	r.rootV1 = mustMetadataBytes(t, rootV1)
	r.writeMetadata("1.root.json", r.rootV1)

	rootV2, err := metadata.Root().FromBytes(r.rootV1)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _ := metadata.KeyFromPublicKey(r.keys["root"][4].Public())
	oldID, _ := oldKey.ID()
	if err := rootV2.Signed.RevokeKey(oldID, "root"); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r.keys["root-v2"] = []ed25519.PrivateKey{replacement}
	replacementMetadata, _ := metadata.KeyFromPublicKey(replacement.Public())
	if err := rootV2.Signed.AddKey(replacementMetadata, "root"); err != nil {
		t.Fatal(err)
	}
	rootV2.Signed.Version = 2
	rootV2.ClearSignatures()
	signRoot(t, rootV2, []ed25519.PrivateKey{r.keys["root"][0], r.keys["root"][1], r.keys["root"][4], replacement})
	r.rootMetadata = rootV2
	r.writeMetadata("2.root.json", mustMetadataBytes(t, rootV2))

	delegations := &metadata.Delegations{Keys: map[string]*metadata.Key{}}
	for _, role := range []string{"stable", "beta", "recovery", "roblox-policy", "runtime-policy"} {
		ids := []string{}
		for _, private := range r.keys[role] {
			public, _ := metadata.KeyFromPublicKey(private.Public())
			id, _ := public.ID()
			delegations.Keys[id] = public
			ids = append(ids, id)
		}
		requirement := requiredDelegations[role]
		delegations.Roles = append(delegations.Roles, metadata.DelegatedRole{Name: role, KeyIDs: ids, Threshold: requirement.threshold, Terminating: true, Paths: append([]string(nil), requirement.paths...)})
	}
	r.targets["targets"].Signed.Delegations = delegations
	for role, signed := range r.targets {
		signTargets(t, signed, r.keys[role])
	}

	r.snapshot = metadata.Snapshot(r.now.Add(7 * 24 * time.Hour))
	for role, signed := range r.targets {
		data := mustMetadataBytes(t, signed)
		r.snapshot.Signed.Meta[role+".json"] = metaFile(signed.Signed.Version, data)
		r.writeMetadata(fmt.Sprintf("%d.%s.json", signed.Signed.Version, role), data)
	}
	signSnapshot(t, r.snapshot, r.keys["snapshot"])
	snapshotBytes := mustMetadataBytes(t, r.snapshot)
	r.writeMetadata("1.snapshot.json", snapshotBytes)

	r.timestamp = metadata.Timestamp(r.now.Add(24 * time.Hour))
	r.timestamp.Signed.Meta["snapshot.json"] = metaFile(r.snapshot.Signed.Version, snapshotBytes)
	signTimestamp(t, r.timestamp, r.keys["timestamp"])
	r.writeMetadata("timestamp.json", mustMetadataBytes(t, r.timestamp))
}

func (r *fixtureRepository) options() VerifyOptions {
	return VerifyOptions{InitialRoot: r.rootV1, MetadataDir: r.metadataDir, TargetsDir: r.targetsDir, ArtifactPath: r.artifactPath, TargetPath: r.targetPath, Channel: "stable", StateDir: r.stateDir, Now: r.now}
}

func (r *fixtureRepository) rewriteChain(role string) {
	r.t.Helper()
	roleBytes := mustMetadataBytes(r.t, r.targets[role])
	r.writeMetadata(fmt.Sprintf("%d.%s.json", r.targets[role].Signed.Version, role), roleBytes)
	r.snapshot.Signed.Meta[role+".json"] = metaFile(r.targets[role].Signed.Version, roleBytes)
	r.snapshot.ClearSignatures()
	signSnapshot(r.t, r.snapshot, r.keys["snapshot"])
	snapshotBytes := mustMetadataBytes(r.t, r.snapshot)
	r.writeMetadata(fmt.Sprintf("%d.snapshot.json", r.snapshot.Signed.Version), snapshotBytes)
	r.timestamp.Signed.Meta["snapshot.json"] = metaFile(r.snapshot.Signed.Version, snapshotBytes)
	r.timestamp.ClearSignatures()
	signTimestamp(r.t, r.timestamp, r.keys["timestamp"])
	r.writeMetadata("timestamp.json", mustMetadataBytes(r.t, r.timestamp))
}

func (r *fixtureRepository) writeMetadata(name string, data []byte) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.metadataDir, name), data, 0644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fixtureRepository) writeTarget(target string, data []byte) {
	r.t.Helper()
	name := filepath.Join(r.targetsDir, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0644); err != nil {
		r.t.Fatal(err)
	}
}

func canonicalFixture(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func ref(target string, data []byte) EvidenceRef {
	return EvidenceRef{Target: target, SHA256: digest(data)}
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func metaFile(version int64, data []byte) *metadata.MetaFiles {
	value := sha256.Sum256(data)
	return &metadata.MetaFiles{Version: version, Length: int64(len(data)), Hashes: metadata.Hashes{"sha256": metadata.HexBytes(value[:])}}
}

func mustMetadataBytes[T metadata.Roles](t *testing.T, value *metadata.Metadata[T]) []byte {
	t.Helper()
	data, err := value.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func loadSigner(t *testing.T, key ed25519.PrivateKey) signature.Signer {
	t.Helper()
	signer, err := signature.LoadSigner(key, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func signRoot(t *testing.T, value *metadata.Metadata[metadata.RootType], keys []ed25519.PrivateKey) {
	t.Helper()
	for _, key := range keys {
		if _, err := value.Sign(loadSigner(t, key)); err != nil {
			t.Fatal(err)
		}
	}
}

func signTargets(t *testing.T, value *metadata.Metadata[metadata.TargetsType], keys []ed25519.PrivateKey) {
	t.Helper()
	for _, key := range keys {
		if _, err := value.Sign(loadSigner(t, key)); err != nil {
			t.Fatal(err)
		}
	}
}

func signSnapshot(t *testing.T, value *metadata.Metadata[metadata.SnapshotType], keys []ed25519.PrivateKey) {
	t.Helper()
	for _, key := range keys {
		if _, err := value.Sign(loadSigner(t, key)); err != nil {
			t.Fatal(err)
		}
	}
}

func signTimestamp(t *testing.T, value *metadata.Metadata[metadata.TimestampType], keys []ed25519.PrivateKey) {
	t.Helper()
	for _, key := range keys {
		if _, err := value.Sign(loadSigner(t, key)); err != nil {
			t.Fatal(err)
		}
	}
}
