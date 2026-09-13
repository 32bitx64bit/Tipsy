// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package integrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStoreStageActivatePinsExactDescriptorsAndRetainsAPKByDigest(t *testing.T) {
	source, inventory := generationFixture(t, "apk-one", "native-one", 2908)
	store := Store{Root: filepath.Join(t.TempDir(), "store")}
	t.Cleanup(func() { makeTreeWritableForCleanup(store.Root) })
	id, err := store.Stage(context.Background(), source, inventory)
	if err != nil {
		t.Fatal(err)
	}
	wantIDRaw, wantID, err := CanonicalInventory(inventory)
	if err != nil || len(wantIDRaw) == 0 || id != wantID {
		t.Fatalf("id=%q want=%q err=%v", id, wantID, err)
	}
	apkDigest := inventory.Files[0].SHA256
	blob := filepath.Join(store.Root, "apks", "sha256", apkDigest+".apk")
	if raw, err := os.ReadFile(blob); err != nil || string(raw) != "apk-one" {
		t.Fatalf("retained APK=%q err=%v", raw, err)
	}
	if info, err := os.Stat(blob); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("retained APK mode=%v err=%v", infoMode(info), err)
	}
	if err := os.Chmod(blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("hostile"), 0o400); err != nil {
		t.Fatal(err)
	}
	if repairedID, err := store.Stage(context.Background(), source, inventory); err != nil || repairedID != id {
		t.Fatalf("retained APK repair id=%q err=%v", repairedID, err)
	}
	if raw, err := os.ReadFile(blob); err != nil || string(raw) != "apk-one" {
		t.Fatalf("repaired retained APK=%q err=%v", raw, err)
	}
	if err := store.Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	generation, err := store.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	natives, err := generation.NativeDescriptors()
	if err != nil || len(natives) != 1 || natives[0].SONAME != "libroblox.so" {
		t.Fatalf("native descriptors=%+v err=%v", natives, err)
	}
	duplicate, err := natives[0].File.Dup()
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	raw := make([]byte, len("native-one"))
	if _, err := duplicate.ReadAt(raw, 0); err != nil || string(raw) != "native-one" {
		t.Fatalf("duplicate contents=%q err=%v", raw, err)
	}

	// Replace the pathname after verification. The pinned descriptor continues
	// to name the authenticated inode, while a new open rejects the replacement.
	libDir := filepath.Join(store.Root, "generations", id, "lib", "x86_64")
	if err := os.Chmod(libDir, 0o700); err != nil {
		t.Fatal(err)
	}
	libPath := filepath.Join(libDir, "libroblox.so")
	if err := os.Rename(libPath, filepath.Join(libDir, "displaced")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libPath, []byte("hostile-one"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := natives[0].File.Recheck(context.Background()); err == nil {
		t.Fatal("pathname replacement race was not detected by the post-map recheck")
	}
	raw = make([]byte, len("native-one"))
	if _, err := duplicate.ReadAt(raw, 0); err != nil || string(raw) != "native-one" {
		t.Fatalf("pinned descriptor followed replacement: %q err=%v", raw, err)
	}
	if reopened, err := OpenGeneration(context.Background(), store.Root, id); err == nil {
		reopened.Close()
		t.Fatal("pathname replacement was accepted")
	}
}

func TestNativeDescriptorSetBindsOneGenerationAndEndsWithOwner(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: filepath.Join(root, "store")}
	t.Cleanup(func() { makeTreeWritableForCleanup(store.Root) })
	source1, inventory1 := generationFixtureAt(t, filepath.Join(root, "source1"), "apk-one", "native-one", 2908)
	id1, err := store.Stage(context.Background(), source1, inventory1)
	if err != nil {
		t.Fatal(err)
	}
	generation1, err := OpenGeneration(context.Background(), store.Root, id1)
	if err != nil {
		t.Fatal(err)
	}
	set1, err := generation1.NativeDescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	if set1.GenerationID() != id1 || set1.InventorySHA256() != generation1.InventorySHA256 {
		t.Fatalf("descriptor binding=(%q,%q), generation=(%q,%q)", set1.GenerationID(), set1.InventorySHA256(), id1, generation1.InventorySHA256)
	}
	descriptors, err := set1.Descriptors()
	if err != nil || len(descriptors) != 1 || descriptors[0].GenerationID != id1 || descriptors[0].InventorySHA256 != generation1.InventorySHA256 {
		t.Fatalf("descriptors=%+v err=%v", descriptors, err)
	}
	// A caller receives a copy; changing it cannot rewrite the sealed set.
	descriptors[0].GenerationID = strings.Repeat("f", 64)
	unchanged, err := set1.Descriptors()
	if err != nil || unchanged[0].GenerationID != id1 {
		t.Fatalf("caller mutation changed descriptor set: %+v err=%v", unchanged, err)
	}

	source2, inventory2 := generationFixtureAt(t, filepath.Join(root, "source2"), "apk-two", "native-two", 2909)
	id2, err := store.Stage(context.Background(), source2, inventory2)
	if err != nil {
		t.Fatal(err)
	}
	generation2, err := OpenGeneration(context.Background(), store.Root, id2)
	if err != nil {
		t.Fatal(err)
	}
	defer generation2.Close()
	set2, err := generation2.NativeDescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := set2.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	set1.descriptors = append(set1.descriptors, foreign[0])
	if err := set1.Validate(); err == nil {
		t.Fatal("mixed-generation descriptor splice was accepted")
	}
	set1.descriptors = set1.descriptors[:1]
	if err := generation1.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := set1.Descriptors(); err == nil {
		t.Fatal("descriptor set outlived its owning generation")
	}
}

func TestStoreRejectsMutationLinksSpecialFilesAndInconsistentDedup(t *testing.T) {
	t.Run("source mutation during copy", func(t *testing.T) {
		source, inventory := generationFixture(t, "apk-original", "native", 2908)
		store := Store{Root: filepath.Join(t.TempDir(), "store")}
		t.Cleanup(func() { makeTreeWritableForCleanup(store.Root) })
		store.afterCopy = func(name string) {
			if name == "apk/base.apk" {
				if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte("apk-mutated!"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := store.Stage(context.Background(), source, inventory); err == nil {
			t.Fatal("source mutation was accepted")
		}
	})

	for _, tc := range []struct {
		name string
		make func(*testing.T, string) string
	}{
		{"symlink", func(t *testing.T, root string) string {
			t.Helper()
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "input")
			if err := os.Symlink(target, name); err != nil {
				t.Fatal(err)
			}
			return name
		}},
		{"magiclink", func(t *testing.T, root string) string {
			t.Helper()
			name := filepath.Join(root, "input")
			if err := os.Symlink("/proc/self/fd/0", name); err != nil {
				t.Fatal(err)
			}
			return name
		}},
		{"hardlink", func(t *testing.T, root string) string {
			t.Helper()
			name := filepath.Join(root, "input")
			if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(name, filepath.Join(root, "other")); err != nil {
				t.Fatal(err)
			}
			return name
		}},
		{"fifo", func(t *testing.T, root string) string {
			t.Helper()
			name := filepath.Join(root, "input")
			if err := unix.Mkfifo(name, 0o600); err != nil {
				t.Fatal(err)
			}
			return name
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_ = tc.make(t, root)
			record := FileRecord{Path: "input", Size: 1, SHA256: digestString("x"), Origin: OriginAPK, APKEntry: "entry", APKDigest: digestString("apk")}
			if opened, err := openVerifiedAt(context.Background(), root, record, false); err == nil {
				opened.File.Close()
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}

	t.Run("atomically repair modified existing generation", func(t *testing.T) {
		source, inventory := generationFixture(t, "apk", "native", 2908)
		store := Store{Root: filepath.Join(t.TempDir(), "store")}
		t.Cleanup(func() { makeTreeWritableForCleanup(store.Root) })
		id, err := store.Stage(context.Background(), source, inventory)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Activate(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(store.Root, "generations", id, "apk", "base.apk")
		if err := os.Chmod(name, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("bad"), 0o400); err != nil {
			t.Fatal(err)
		}
		if repairedID, err := store.Stage(context.Background(), source, inventory); err != nil || repairedID != id {
			t.Fatalf("repair id=%q err=%v", repairedID, err)
		}
		active, err := store.Active(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if active.ID != id {
			t.Fatalf("active after repair=%q want=%q", active.ID, id)
		}
		active.Close()
		rejected, err := filepath.Glob(filepath.Join(store.Root, "rejected", id+"-*"))
		if err != nil || len(rejected) != 1 {
			t.Fatalf("rejected generations=%v err=%v", rejected, err)
		}
	})
}

func TestOpenatFallbackAndPinnedWriteDetection(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "file")
	if err := os.WriteFile(name, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := FileRecord{Path: "file", Size: 8, SHA256: digestString("original"), Origin: OriginAPK, APKEntry: "entry", APKDigest: digestString("apk")}
	originalOpenat2 := openat2Call
	openat2Call = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }
	defer func() { openat2Call = originalOpenat2 }()
	opened, err := openVerifiedAt(context.Background(), root, record, false)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.File.Close()
	if opened.Identity.Openat2Used {
		t.Fatal("fallback was not recorded")
	}
	if err := os.WriteFile(name, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := opened.Recheck(context.Background()); err == nil {
		t.Fatal("write through the still-open inode was accepted")
	}
}

func TestActivationFailureKeepsPriorGenerationAndAccountData(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: filepath.Join(root, "runtime-generations")}
	t.Cleanup(func() { makeTreeWritableForCleanup(store.Root) })
	source1, inventory1 := generationFixtureAt(t, filepath.Join(root, "source1"), "apk-one", "native-one", 2908)
	id1, err := store.Stage(context.Background(), source1, inventory1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(context.Background(), id1); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(root, "app-data", "session-sentinel")
	if err := os.MkdirAll(filepath.Dir(account), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(account, []byte("opaque-account-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	source2, inventory2 := generationFixtureAt(t, filepath.Join(root, "source2"), "apk-two", "native-two", 2909)
	id2, err := store.Stage(context.Background(), source2, inventory2)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Activate(canceled, id2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled activation err=%v", err)
	}
	active, err := store.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != id1 {
		t.Fatalf("active=%q want prior=%q", active.ID, id1)
	}
	active.Close()
	if raw, err := os.ReadFile(account); err != nil || string(raw) != "opaque-account-state" {
		t.Fatalf("account data changed: %q err=%v", raw, err)
	}
}

func TestActiveRecordRejectsMixedGenerationInventoryBinding(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: filepath.Join(root, "runtime-generations")}
	t.Cleanup(func() { makeTreeWritableForCleanup(store.Root) })
	source1, inventory1 := generationFixtureAt(t, filepath.Join(root, "source1"), "apk-one", "native-one", 2908)
	id1, err := store.Stage(context.Background(), source1, inventory1)
	if err != nil {
		t.Fatal(err)
	}
	source2, inventory2 := generationFixtureAt(t, filepath.Join(root, "source2"), "apk-two", "native-two", 2909)
	id2, err := store.Stage(context.Background(), source2, inventory2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(context.Background(), id1); err != nil {
		t.Fatal(err)
	}
	mixed, err := json.Marshal(ActiveRecord{Schema: ActiveSchema, Generation: id1, InventorySHA256: id2})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root, activeFileName), mixed, 0o600); err != nil {
		t.Fatal(err)
	}
	if generation, err := store.Active(context.Background()); err == nil {
		generation.Close()
		t.Fatal("active record spliced a generation to another inventory digest")
	}
	if err := store.Activate(context.Background(), id2); err != nil {
		t.Fatal(err)
	}
	active, err := store.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if active.ID != id2 || active.InventorySHA256 != id2 {
		t.Fatalf("repaired active binding=(%q,%q), want %q", active.ID, active.InventorySHA256, id2)
	}
}

func TestOfficialAndDevelopmentActiveSlotsDoNotClobberEachOther(t *testing.T) {
	root := t.TempDir()
	storeRoot := filepath.Join(root, "runtime-generations")
	t.Cleanup(func() { makeTreeWritableForCleanup(storeRoot) })

	devSource, devInventory := generationFixtureAt(t, filepath.Join(root, "source-dev"), "apk-dev", "native-dev", 2908)
	officialSource := filepath.Join(root, "source-official")
	officialInventory := officialFixtureInventory("apk-official", "native-official", 2909)
	if err := os.MkdirAll(filepath.Join(officialSource, "apk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(officialSource, "lib", "x86_64"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(officialSource, "apk", "base.apk"), []byte("apk-official"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(officialSource, "lib", "x86_64", "libroblox.so"), []byte("native-official"), 0o600); err != nil {
		t.Fatal(err)
	}

	legacy := Store{Root: storeRoot}
	devID, err := legacy.Stage(context.Background(), devSource, devInventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Activate(context.Background(), devID); err != nil {
		t.Fatal(err)
	}

	official := Store{Root: storeRoot, ActiveSlot: ActiveSlotOfficial}
	if generation, err := official.Active(context.Background()); err == nil {
		generation.Close()
		t.Fatal("official slot accepted a development generation as live")
	}
	if _, err := os.Stat(filepath.Join(storeRoot, developmentActiveFileName)); err != nil {
		t.Fatalf("development active pointer was not adopted: %v", err)
	}

	dev := Store{Root: storeRoot, ActiveSlot: ActiveSlotDevelopment}
	activeDev, err := dev.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if activeDev.ID != devID {
		t.Fatalf("development slot id=%q want=%q", activeDev.ID, devID)
	}
	activeDev.Close()

	officialID, err := official.Stage(context.Background(), officialSource, officialInventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := official.Activate(context.Background(), officialID); err != nil {
		t.Fatal(err)
	}
	activeOfficial, err := official.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer activeOfficial.Close()
	if activeOfficial.ID != officialID {
		t.Fatalf("official slot id=%q want=%q", activeOfficial.ID, officialID)
	}

	stillDev, err := dev.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stillDev.Close()
	if stillDev.ID != devID {
		t.Fatalf("official activate stole development slot: got=%q want=%q", stillDev.ID, devID)
	}
}

func TestInventoryRejectsPathsDuplicatesAndNonCanonicalData(t *testing.T) {
	_, inventory := generationFixture(t, "apk", "native", 2908)
	raw, _, err := CanonicalInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "source") || strings.Contains(string(raw), "account") {
		t.Fatalf("inventory contains forbidden origin/account field: %s", raw)
	}
	if _, _, err := DecodeInventory(append(raw, '\n')); err == nil {
		t.Fatal("non-canonical inventory was accepted")
	}
	for _, unsafe := range []string{"../x", "/x", "a//b", "a/./b", "a\\b", "a\x00b"} {
		bad := inventory
		bad.Files = append([]FileRecord(nil), inventory.Files...)
		bad.Files[0].Path = unsafe
		if _, _, err := CanonicalInventory(bad); err == nil {
			t.Fatalf("unsafe path %q was accepted", unsafe)
		}
	}
	badLineage := inventory
	badLineage.SignerLineage = []string{inventory.SignerLineage[0], inventory.SignerLineage[0]}
	if _, _, err := CanonicalInventory(badLineage); err == nil {
		t.Fatal("duplicate signer lineage was accepted")
	}
	claimedOfficial := inventory
	claimedOfficial.AuthorizationMode = "official-verified"
	if _, _, err := CanonicalInventory(claimedOfficial); err == nil {
		t.Fatal("development inventory claimed official status without authenticated policy")
	}
	claimedPolicy := inventory
	claimedPolicy.PolicyAuthorized = true
	if _, _, err := CanonicalInventory(claimedPolicy); err == nil {
		t.Fatal("development inventory claimed policy authorization")
	}
}

func TestInventoryAuthorizationPolicyMatrix(t *testing.T) {
	_, development := generationFixture(t, "apk", "native", 2908)

	tests := []struct {
		name             string
		mode             string
		lineageID        string
		policySequence   uint64
		policyAuthorized bool
		wantValid        bool
	}{
		{
			name:             "authenticated keyless official policy",
			mode:             "official-verified",
			lineageID:        "compiled-keyless-release",
			policyAuthorized: true,
			wantValid:        true,
		},
		{
			name:             "authenticated legacy official policy",
			mode:             "official-verified",
			lineageID:        "tuf-stable",
			policySequence:   7,
			policyAuthorized: true,
			wantValid:        true,
		},
		{
			name:             "keyless official policy with nonzero sequence",
			mode:             "official-verified",
			lineageID:        "compiled-keyless-release",
			policySequence:   1,
			policyAuthorized: true,
		},
		{
			name:             "legacy official policy with zero sequence",
			mode:             "official-verified",
			lineageID:        "tuf-stable",
			policyAuthorized: true,
		},
		{
			name:      "keyless identity in development mode",
			mode:      "development-unrestricted",
			lineageID: "compiled-keyless-release",
		},
		{
			name:      "unauthenticated keyless official policy",
			mode:      "official-verified",
			lineageID: "compiled-keyless-release",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inventory := development
			inventory.AuthorizationMode = tc.mode
			inventory.SignerLineageID = tc.lineageID
			inventory.PolicySequence = tc.policySequence
			inventory.PolicyAuthorized = tc.policyAuthorized

			_, _, err := CanonicalInventory(inventory)
			if tc.wantValid && err != nil {
				t.Fatalf("authenticated policy was rejected: %v", err)
			}
			if !tc.wantValid && err == nil {
				t.Fatal("spoofed policy combination was accepted")
			}
		})
	}
}

func FuzzDecodeInventory(f *testing.F) {
	source := f.TempDir()
	_, inventory := generationFixtureAtFuzz(f, source, "apk", "native", 2908)
	raw, _, err := CanonicalInventory(inventory)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"schema":"unknown"}`))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, input []byte) {
		decoded, _, err := DecodeInventory(input)
		if err != nil {
			return
		}
		canonical, _, err := CanonicalInventory(decoded)
		if err != nil || string(canonical) != string(input) {
			t.Fatalf("accepted inventory did not round trip: err=%v", err)
		}
	})
}

func generationFixture(t *testing.T, apkData, nativeData string, version int64) (string, Inventory) {
	t.Helper()
	return generationFixtureAt(t, filepath.Join(t.TempDir(), "source"), apkData, nativeData, version)
}

func generationFixtureAt(t *testing.T, source, apkData, nativeData string, version int64) (string, Inventory) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(source, "apk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "lib", "x86_64"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "apk", "base.apk"), []byte(apkData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "lib", "x86_64", "libroblox.so"), []byte(nativeData), 0o600); err != nil {
		t.Fatal(err)
	}
	return source, fixtureInventory(apkData, nativeData, version)
}

func generationFixtureAtFuzz(f *testing.F, source, apkData, nativeData string, version int64) (string, Inventory) {
	f.Helper()
	return source, fixtureInventory(apkData, nativeData, version)
}

func fixtureInventory(apkData, nativeData string, version int64) Inventory {
	apkDigest := digestString(apkData)
	return Inventory{
		Schema: InventorySchema, PackageName: "com.roblox.client", VersionName: "2.734.917", VersionCode: version,
		AuthorizationMode: "development-unrestricted", PolicyAuthorized: false,
		SignerLineageID: "compiled-development", SignerLineage: []string{digestString("signer")}, Splits: []string{"base"},
		Files: []FileRecord{
			{Path: "apk/base.apk", Size: int64(len(apkData)), SHA256: apkDigest, Origin: OriginAPK, APKEntry: "@apk", APKDigest: apkDigest},
			{Path: "lib/x86_64/libroblox.so", Size: int64(len(nativeData)), SHA256: digestString(nativeData), Origin: OriginAPK, APKEntry: "lib/x86_64/libroblox.so", APKDigest: apkDigest, Executable: true},
		},
	}
}

func officialFixtureInventory(apkData, nativeData string, version int64) Inventory {
	inventory := fixtureInventory(apkData, nativeData, version)
	inventory.AuthorizationMode = "official-verified"
	inventory.PolicyAuthorized = true
	inventory.SignerLineageID = keylessReleaseSignerLineageID
	inventory.PolicySequence = 0
	return inventory
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
