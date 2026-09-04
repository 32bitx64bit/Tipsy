// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setStorageXDG(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	return root
}

func TestAppStorageUsesStableSeparateXDGRoots(t *testing.T) {
	root := setStorageXDG(t)
	got := AppStorage()
	if want := filepath.Join(root, "data", "tipsy", "app-data", robloxPackageName); got.DataRoot != want {
		t.Fatalf("DataRoot=%q want %q", got.DataRoot, want)
	}
	if got.FilesDir != filepath.Join(got.DataRoot, "files") {
		t.Fatalf("FilesDir=%q", got.FilesDir)
	}
	if want := filepath.Join(root, "cache", "tipsy", "app-data", robloxPackageName); got.CacheRoot != want {
		t.Fatalf("CacheRoot=%q want %q", got.CacheRoot, want)
	}
	if got.CacheDir != filepath.Join(got.CacheRoot, "cache") {
		t.Fatalf("CacheDir=%q", got.CacheDir)
	}
	for _, p := range []string{got.DataRoot, got.FilesDir, got.CacheRoot, got.CacheDir} {
		if strings.Contains(p, "runtime") {
			t.Fatalf("app storage is coupled to replaceable runtime: %q", p)
		}
	}
}

func TestPrepareAppStorageMigratesOpaqueLegacyDataAndRetainsBackup(t *testing.T) {
	setStorageXDG(t)
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	legacy := filepath.Join(runtimeDir, "files")
	legacyFile := filepath.Join(legacy, "appData", "synthetic-session.bin")
	body := []byte{0x00, 0xff, '{', 'n', 'o', 't', '-', 'j', 's', 'o', 'n', '}'}
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, body, 0o644); err != nil {
		t.Fatal(err)
	}

	layout, migration, err := prepareAppStorage(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if migration.Copied != 1 || migration.Preserved != 0 || migration.Skipped {
		t.Fatalf("migration=%+v", migration)
	}
	dest := filepath.Join(layout.FilesDir, "appData", "synthetic-session.bin")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("opaque payload changed: %x", got)
	}
	backup, err := os.ReadFile(legacyFile)
	if err != nil {
		t.Fatalf("legacy backup was not retained: %v", err)
	}
	if !bytes.Equal(backup, body) {
		t.Fatal("legacy backup changed")
	}
	assertMode(t, legacy, 0o700)
	assertMode(t, legacyFile, 0o600)
	assertMode(t, layout.DataRoot, 0o700)
	assertMode(t, layout.FilesDir, 0o700)
	assertMode(t, filepath.Dir(dest), 0o700)
	assertMode(t, dest, 0o600)
	assertMode(t, filepath.Join(layout.DataRoot, legacyMigrationMarker), 0o600)
}

func TestPrepareAppStorageRestartAndExistingDestinationArePreserved(t *testing.T) {
	setStorageXDG(t)
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	legacyFile := filepath.Join(runtimeDir, "files", "state", "value")
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, []byte("legacy-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := AppStorage()
	dest := filepath.Join(layout.FilesDir, "state", "value")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("newer-persistent-sentinel"), 0o666); err != nil {
		t.Fatal(err)
	}

	_, migration, err := prepareAppStorage(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if migration.Copied != 0 || migration.Preserved != 1 {
		t.Fatalf("migration=%+v", migration)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "newer-persistent-sentinel" {
		t.Fatalf("persistent value overwritten: %q err=%v", got, err)
	}
	assertMode(t, dest, 0o600)

	_, restart, err := prepareAppStorage(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !restart.Skipped || restart.Copied != 0 || restart.Preserved != 0 {
		t.Fatalf("restart migration=%+v", restart)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "newer-persistent-sentinel" {
		t.Fatalf("restart lost value: %q err=%v", got, err)
	}
}

func TestPrepareAppStorageRepairsCorruptMarkerWithoutDiscardingData(t *testing.T) {
	setStorageXDG(t)
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	layout, _, err := prepareAppStorage(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(layout.DataRoot, legacyMigrationMarker)
	if err := os.WriteFile(marker, []byte("interrupted-or-corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(runtimeDir, "files", "late-sentinel")
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, []byte("late-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, migration, err := prepareAppStorage(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if migration.Copied != 1 || migration.Skipped {
		t.Fatalf("migration=%+v", migration)
	}
	if got, err := os.ReadFile(filepath.Join(layout.FilesDir, "late-sentinel")); err != nil || string(got) != "late-value" {
		t.Fatalf("recovery copy=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != legacyMigrationVersion {
		t.Fatalf("marker=%q err=%v", got, err)
	}
}

func TestPrepareAppStorageRejectsSymlinksWithoutFollowingThem(t *testing.T) {
	setStorageXDG(t)
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	legacy := filepath.Join(runtimeDir, "files")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("must-not-copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(legacy, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareAppStorage(runtimeDir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(AppStorage().FilesDir, "escape")); !os.IsNotExist(err) {
		t.Fatalf("symlink target appeared in persistent data: %v", err)
	}
}

func TestPrepareAppStorageRejectsSymlinkDestinationRoot(t *testing.T) {
	setStorageXDG(t)
	layout := AppStorage()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(layout.DataRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, layout.DataRoot); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareAppStorage(filepath.Join(t.TempDir(), "runtime")); err == nil {
		t.Fatal("symlink destination root accepted")
	}
}

func TestLegacyMigrationLoggingDoesNotExposeNamesOrPayloads(t *testing.T) {
	setStorageXDG(t)
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	const secret = "super-secret-cookie-sentinel"
	legacyFile := filepath.Join(runtimeDir, "files", secret)
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, migration, err := prepareAppStorage(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	logAppStorageMigration(migration)
	if strings.Contains(out.String(), secret) {
		t.Fatalf("migration log exposed opaque account data: %s", out.String())
	}
	if !strings.Contains(out.String(), "copied_files=1") {
		t.Fatalf("migration log omitted safe count: %s", out.String())
	}
}

func TestSafeStorageJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../escape", filepath.Join("nested", "..", "..", "escape"), root, "", "."} {
		if _, err := safeStorageJoin(root, rel); err == nil {
			t.Errorf("accepted %q", rel)
		}
	}
	if got, err := safeStorageJoin(root, filepath.Join("nested", "value")); err != nil || got != filepath.Join(root, "nested", "value") {
		t.Fatalf("safe join=%q err=%v", got, err)
	}
}

func TestAtomicPrivateCopyDoesNotReplaceExistingOrLeavePartialFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "private", "dest")
	if err := os.WriteFile(source, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := copyPrivateFileAtomicNoReplace(source, dest, info)
	if err != nil || !copied {
		t.Fatalf("first copy copied=%v err=%v", copied, err)
	}
	if err := os.WriteFile(source, []byte("later"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	copied, err = copyPrivateFileAtomicNoReplace(source, dest, info)
	if err != nil || copied {
		t.Fatalf("second copy copied=%v err=%v", copied, err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "legacy" {
		t.Fatalf("dest=%q err=%v", got, err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(dest), ".tipsy-migrate-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("partial migration files=%q err=%v", matches, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("mode %s=%#o want %#o", path, got, want)
	}
}
