package releasemeta

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestVerifyLocalCompleteBundleAndRootRotation(t *testing.T) {
	repository := newFixtureRepository(t)
	result, err := VerifyLocal(repository.options())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.State != DevelopmentUnrestricted || result.Reason != ReasonDevelopment || result.RootVersion != 2 || result.SnapshotVersion != 1 || result.TargetsVersion != 1 {
		t.Fatalf("result=%+v", result)
	}
	if result.ProductionRootBound || len(result.Evidence) != 7 {
		t.Fatalf("production=%v evidence=%d", result.ProductionRootBound, len(result.Evidence))
	}
	info, err := os.Stat(filepath.Join(repository.stateDir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state mode=%o", info.Mode().Perm())
	}
	if _, err := VerifyLocal(repository.options()); err != nil {
		t.Fatalf("repeat offline verification: %v", err)
	}
}

func TestMinimumVerifierVersion(t *testing.T) {
	if !versionAtLeast("1.2.3", "1.2.3") || !versionAtLeast("1.3.0", "1.2.9") || versionAtLeast("1.2.3-beta.1", "1.2.3") || versionAtLeast("0.1.0", "0.2.0") {
		t.Fatal("semantic version ordering failed")
	}
	repository := newFixtureRepository(t)
	options := repository.options()
	options.VerifierVersion = "0.0.9"
	_, err := VerifyLocal(options)
	if CodeOf(err) != ReasonTargetMetadata {
		t.Fatalf("code=%s err=%v", CodeOf(err), err)
	}
}

func TestArtifactAndEvidenceMutationFail(t *testing.T) {
	t.Run("artifact-one-bit", func(t *testing.T) {
		repository := newFixtureRepository(t)
		data, err := os.ReadFile(repository.artifactPath)
		if err != nil {
			t.Fatal(err)
		}
		data[0] ^= 1
		if err := os.WriteFile(repository.artifactPath, data, 0644); err != nil {
			t.Fatal(err)
		}
		_, err = VerifyLocal(repository.options())
		if CodeOf(err) != ReasonArtifactMismatch {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("evidence-one-bit", func(t *testing.T) {
		repository := newFixtureRepository(t)
		target := repository.findTarget(".manifest.json")
		name := filepath.Join(repository.targetsDir, filepath.FromSlash(target))
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)/2] ^= 1
		if err := os.WriteFile(name, data, 0644); err != nil {
			t.Fatal(err)
		}
		_, err = VerifyLocal(repository.options())
		if CodeOf(err) != ReasonEvidenceMismatch {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
}

func TestDelegatedThresholdAndWrongKeyFail(t *testing.T) {
	for _, test := range []struct {
		name string
		keys string
	}{
		{"one-of-three", "stable-one"},
		{"wrong-role-key", "beta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newFixtureRepository(t)
			stable := repository.targets["stable"]
			stable.ClearSignatures()
			if test.keys == "stable-one" {
				signTargets(t, stable, repository.keys["stable"][:1])
			} else {
				signTargets(t, stable, repository.keys["beta"])
			}
			repository.rewriteChain("stable")
			_, err := VerifyLocal(repository.options())
			if CodeOf(err) != ReasonSignatureThreshold {
				t.Fatalf("code=%s err=%v", CodeOf(err), err)
			}
		})
	}
}

func TestRootV2RequiresOldAndNewThresholds(t *testing.T) {
	repository := newFixtureRepository(t)
	rootV2, err := metadata.Root().FromBytes(mustRead(t, filepath.Join(repository.metadataDir, "2.root.json")))
	if err != nil {
		t.Fatal(err)
	}
	rootV2.Signatures = rootV2.Signatures[:2]
	repository.writeMetadata("2.root.json", mustMetadataBytes(t, rootV2))
	_, err = VerifyLocal(repository.options())
	if CodeOf(err) != ReasonSignatureThreshold {
		t.Fatalf("code=%s err=%v", CodeOf(err), err)
	}
}

func TestRoleLayoutAndChannelIsolation(t *testing.T) {
	t.Run("broad-stable-delegation", func(t *testing.T) {
		repository := newFixtureRepository(t)
		top := repository.targets["targets"]
		for i := range top.Signed.Delegations.Roles {
			if top.Signed.Delegations.Roles[i].Name == "stable" {
				top.Signed.Delegations.Roles[i].Paths = []string{"stable/*"}
			}
		}
		top.ClearSignatures()
		signTargets(t, top, repository.keys["targets"])
		repository.rewriteChain("targets")
		_, err := VerifyLocal(repository.options())
		if CodeOf(err) != ReasonRoleLayout {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("stable-as-beta", func(t *testing.T) {
		repository := newFixtureRepository(t)
		options := repository.options()
		options.Channel = "beta"
		_, err := VerifyLocal(options)
		if code := CodeOf(err); code != ReasonUnsafePath && code != ReasonChannelMismatch {
			t.Fatalf("code=%s err=%v", code, err)
		}
	})
	t.Run("path-confusion", func(t *testing.T) {
		repository := newFixtureRepository(t)
		options := repository.options()
		options.TargetPath = "stable/linux/x86_64/../beta/evil.AppImage"
		_, err := VerifyLocal(options)
		if CodeOf(err) != ReasonUnsafePath {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
}

func TestExpiryFreezeMixMatchAndMissingIntermediate(t *testing.T) {
	t.Run("expiry-freeze", func(t *testing.T) {
		repository := newFixtureRepository(t)
		options := repository.options()
		options.Now = repository.now.Add(48 * time.Hour)
		_, err := VerifyLocal(options)
		if CodeOf(err) != ReasonMetadataExpired {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("mix-and-match", func(t *testing.T) {
		repository := newFixtureRepository(t)
		name := filepath.Join(repository.metadataDir, "1.stable.json")
		data := mustRead(t, name)
		data[len(data)-1] ^= 1
		if err := os.WriteFile(name, data, 0644); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyLocal(repository.options())
		if code := CodeOf(err); code != ReasonMetadataMixMatch && code != ReasonMetadataMalformed {
			t.Fatalf("code=%s err=%v", code, err)
		}
	})
	t.Run("missing-intermediate-after-v2", func(t *testing.T) {
		repository := newFixtureRepository(t)
		if _, err := VerifyLocal(repository.options()); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(repository.metadataDir, "2.root.json")); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyLocal(repository.options())
		if CodeOf(err) != ReasonStateRollback {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
}

func TestAtomicStateRejectsRollbackEquivocationAndDeletion(t *testing.T) {
	t.Run("all-metadata-levels", func(t *testing.T) {
		for _, role := range []string{"root", "timestamp", "snapshot", "targets", "stable", "roblox-policy", "runtime-policy"} {
			t.Run(role, func(t *testing.T) {
				repository := newFixtureRepository(t)
				if _, err := VerifyLocal(repository.options()); err != nil {
					t.Fatal(err)
				}
				state := repository.readState()
				record := state.Roles[role]
				record.Version++
				state.Roles[role] = record
				repository.writeState(state)
				_, err := VerifyLocal(repository.options())
				if CodeOf(err) != ReasonStateRollback {
					t.Fatalf("code=%s err=%v", CodeOf(err), err)
				}
			})
		}
	})
	t.Run("same-version-different-bytes", func(t *testing.T) {
		repository := newFixtureRepository(t)
		if _, err := VerifyLocal(repository.options()); err != nil {
			t.Fatal(err)
		}
		repository.timestamp.Signed.Expires = repository.timestamp.Signed.Expires.Add(time.Minute)
		repository.timestamp.ClearSignatures()
		signTimestamp(t, repository.timestamp, repository.keys["timestamp"])
		repository.writeMetadata("timestamp.json", mustMetadataBytes(t, repository.timestamp))
		_, err := VerifyLocal(repository.options())
		if CodeOf(err) != ReasonStateConflict {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("release-and-policy-sequence", func(t *testing.T) {
		repository := newFixtureRepository(t)
		if _, err := VerifyLocal(repository.options()); err != nil {
			t.Fatal(err)
		}
		state := repository.readState()
		state.ReleaseSequences["stable"]++
		state.PolicySequences["roblox"]++
		repository.writeState(state)
		_, err := VerifyLocal(repository.options())
		if CodeOf(err) != ReasonStateRollback {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("state-deletion", func(t *testing.T) {
		repository := newFixtureRepository(t)
		if _, err := VerifyLocal(repository.options()); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(repository.stateDir, stateFileName)); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyLocal(repository.options())
		if CodeOf(err) != ReasonStateMissing {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
}

func TestBoundedMetadataAndSymlinkTargets(t *testing.T) {
	t.Run("metadata-size", func(t *testing.T) {
		repository := newFixtureRepository(t)
		if err := os.WriteFile(filepath.Join(repository.metadataDir, "3.root.json"), []byte(strings.Repeat("x", 33)), 0644); err != nil {
			t.Fatal(err)
		}
		fetcher, err := newLocalFetcher(repository.metadataDir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetcher.DownloadFile("https://local.tipsy.invalid/metadata/3.root.json", 32, 0)
		if CodeOf(err) != ReasonMetadataTooLarge {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("metadata-depth", func(t *testing.T) {
		repository := newFixtureRepository(t)
		deep := strings.Repeat("[", MaxMetadataDepth+1) + strings.Repeat("]", MaxMetadataDepth+1)
		if err := os.WriteFile(filepath.Join(repository.metadataDir, "3.root.json"), []byte(deep), 0644); err != nil {
			t.Fatal(err)
		}
		fetcher, _ := newLocalFetcher(repository.metadataDir)
		_, err := fetcher.DownloadFile("https://local.tipsy.invalid/metadata/3.root.json", int64(len(deep)), 0)
		if CodeOf(err) != ReasonMetadataTooLarge {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
	t.Run("evidence-symlink", func(t *testing.T) {
		repository := newFixtureRepository(t)
		target := repository.findTarget(".manifest.json")
		name := filepath.Join(repository.targetsDir, filepath.FromSlash(target))
		data := mustRead(t, name)
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(repository.root, "outside")
		if err := os.WriteFile(outside, data, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, name); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyLocal(repository.options())
		if CodeOf(err) != ReasonEvidenceMissing {
			t.Fatalf("code=%s err=%v", CodeOf(err), err)
		}
	})
}

func TestRecoveryDelegationCannotBroadenAuthority(t *testing.T) {
	repository := newFixtureRepository(t)
	top := repository.targets["targets"]
	for i := range top.Signed.Delegations.Roles {
		if top.Signed.Delegations.Roles[i].Name == "recovery" {
			top.Signed.Delegations.Roles[i].Paths = []string{"stable/linux/x86_64/*", "recovery/*"}
		}
	}
	top.ClearSignatures()
	signTargets(t, top, repository.keys["targets"])
	repository.rewriteChain("targets")
	_, err := VerifyLocal(repository.options())
	if CodeOf(err) != ReasonRoleLayout {
		t.Fatalf("code=%s err=%v", CodeOf(err), err)
	}
}

func (r *fixtureRepository) findTarget(suffix string) string {
	r.t.Helper()
	for target := range r.targetContents {
		if strings.HasSuffix(target, suffix) {
			return target
		}
	}
	r.t.Fatalf("no target with suffix %q", suffix)
	return ""
}

func (r *fixtureRepository) readState() rollbackState {
	r.t.Helper()
	state, _, err := loadRollbackState(r.stateDir)
	if err != nil {
		r.t.Fatal(err)
	}
	return state
}

func (r *fixtureRepository) writeState(state rollbackState) {
	r.t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.stateDir, stateFileName), data, 0600); err != nil {
		r.t.Fatal(err)
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func reasonIs(err error, code ReasonCode) bool {
	var releaseErr *Error
	return errors.As(err, &releaseErr) && releaseErr.Code == code
}
