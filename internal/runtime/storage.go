// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

const (
	robloxPackageName      = "com.roblox.client"
	robloxPreferencesName  = "rbx.prefs" // Retained legacy artifact; never a JNI preference name.
	robloxPreferencesID    = "prefs"
	robloxCookieFileName   = "cookies-v1.json"
	legacyMigrationMarker  = ".legacy-runtime-files-migrated-v1"
	legacyMigrationVersion = "tipsy-app-storage-v1\n"
)

// AppStorageLayout separates the official client installation from Android
// application data. FilesDir is durable, account-bearing data and must survive
// APK replacement. CacheDir is deliberately under XDG_CACHE_HOME and may be
// discarded without losing the account session.
type AppStorageLayout struct {
	DataRoot        string
	FilesDir        string
	PreferencesDir  string
	PreferencesFile string // Legacy artifact retained without interpreting or deleting it.
	CookieFile      string
	CacheRoot       string
	CacheDir        string
}

// AppStorage returns the stable, version-independent storage paths for the
// current OS user. These paths never include an APK version or RuntimeDir.
func AppStorage() AppStorageLayout {
	p := config.Paths()
	dataRoot := filepath.Join(p.DataDir, "app-data", robloxPackageName)
	cacheRoot := filepath.Join(p.CacheDir, "app-data", robloxPackageName)
	return AppStorageLayout{
		DataRoot:        dataRoot,
		FilesDir:        filepath.Join(dataRoot, "files"),
		PreferencesDir:  filepath.Join(dataRoot, "shared_prefs"),
		PreferencesFile: filepath.Join(dataRoot, "shared_prefs", robloxPreferencesName),
		CookieFile:      filepath.Join(dataRoot, "shared_prefs", robloxCookieFileName),
		CacheRoot:       cacheRoot,
		CacheDir:        filepath.Join(cacheRoot, "cache"),
	}
}

type appStorageMigration struct {
	Copied    int
	Preserved int
	Skipped   bool
}

func logAppStorageMigration(m appStorageMigration) {
	if m.Copied == 0 && m.Preserved == 0 {
		return
	}
	// Counts only: account-bearing filenames and payloads are intentionally
	// absent from logs.
	logging.Logger(logging.CatFilesystem).Info("legacy app data preserved",
		"copied_files", m.Copied,
		"existing_files", m.Preserved)
}

// prepareAppStorage creates private XDG roots and, once, copies the former
// <runtime>/files tree into the durable FilesDir. The old tree is deliberately
// retained as a recoverable backup. Payloads are opaque: this code never
// parses, prints, or assigns meaning to session data.
func prepareAppStorage(runtimeDir string) (AppStorageLayout, appStorageMigration, error) {
	layout := AppStorage()
	for _, dir := range []string{layout.DataRoot, layout.FilesDir, layout.PreferencesDir, layout.CacheRoot, layout.CacheDir} {
		if err := ensurePrivateDir(dir); err != nil {
			return AppStorageLayout{}, appStorageMigration{}, fmt.Errorf("prepare private app storage: %w", err)
		}
	}
	if err := cleanupInterruptedPrivateWrites(layout.PreferencesDir); err != nil {
		return AppStorageLayout{}, appStorageMigration{}, fmt.Errorf("recover native preferences directory: %w", err)
	}
	if err := securePrivateFileIfPresent(layout.PreferencesFile); err != nil {
		return AppStorageLayout{}, appStorageMigration{}, fmt.Errorf("secure native preferences file: %w", err)
	}
	if err := securePrivateFileIfPresent(layout.CookieFile); err != nil {
		return AppStorageLayout{}, appStorageMigration{}, fmt.Errorf("secure cookie storage: %w", err)
	}
	if err := hardenPrivateTree(layout.FilesDir); err != nil {
		return AppStorageLayout{}, appStorageMigration{}, fmt.Errorf("secure persistent FilesDir: %w", err)
	}

	legacy := filepath.Join(runtimeDir, "files")
	result, err := migrateLegacyFiles(legacy, layout.DataRoot, layout.FilesDir)
	if err != nil {
		return AppStorageLayout{}, result, fmt.Errorf("migrate legacy FilesDir: %w", err)
	}
	return layout, result, nil
}

// cleanupInterruptedPrivateWrites removes only Tipsy's own atomic-write
// staging names. A crash can leave one behind, but rename guarantees the
// official primary file remains either the old complete generation or the new
// complete generation. The process-wide client lock excludes a live writer.
func cleanupInterruptedPrivateWrites(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".tipsy-atomic-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("interrupted private write staging entry is a directory")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDirectory(dir)
	}
	return nil
}

// securePrivateFileIfPresent validates and tightens the official client's
// opaque native-preferences file without opening or interpreting its payload.
// The file is allowed not to exist on a first launch. NativeSetPreferencesFile
// takes an Android preference name; it does not create any file itself.
func securePrivateFileIfPresent(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return fmt.Errorf("native preferences path is not a regular file")
	}
	return os.Chmod(path, 0o600)
}

// syncPrivateOpaqueFile establishes the graceful-close durability boundary
// after Roblox's lifecycle callbacks have returned. It never reads the file,
// and therefore cannot expose or make assumptions about account state. The
// process-wide client lock serializes it against other Tipsy launches.
func syncPrivateOpaqueFile(path string) error {
	if err := securePrivateFileIfPresent(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return syncDirectory(filepath.Dir(path))
	}
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func migrateLegacyFiles(source, dataRoot, filesDir string) (appStorageMigration, error) {
	marker := filepath.Join(dataRoot, legacyMigrationMarker)
	if ok, err := validMigrationMarker(marker); err != nil {
		return appStorageMigration{}, err
	} else if ok {
		return appStorageMigration{Skipped: true}, nil
	}

	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return appStorageMigration{}, err
	}
	destAbs, err := filepath.Abs(filesDir)
	if err != nil {
		return appStorageMigration{}, err
	}
	if pathsOverlap(sourceAbs, destAbs) {
		return appStorageMigration{}, fmt.Errorf("legacy and persistent FilesDir overlap")
	}

	st, err := os.Lstat(sourceAbs)
	if errors.Is(err, os.ErrNotExist) {
		if err := writePrivateFileAtomic(marker, []byte(legacyMigrationVersion)); err != nil {
			return appStorageMigration{}, err
		}
		return appStorageMigration{}, nil
	}
	if err != nil {
		return appStorageMigration{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return appStorageMigration{}, fmt.Errorf("legacy FilesDir is not a real directory")
	}
	if err := os.Chmod(sourceAbs, 0o700); err != nil {
		return appStorageMigration{}, err
	}

	var result appStorageMigration
	err = filepath.WalkDir(sourceAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("legacy FilesDir contains a symbolic link")
		}
		rel, err := filepath.Rel(sourceAbs, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target, err := safeStorageJoin(destAbs, rel)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := os.Chmod(path, 0o700); err != nil {
				return err
			}
			return ensurePrivateDir(target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("legacy FilesDir contains an unsupported entry")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		copied, err := copyPrivateFileAtomicNoReplace(path, target, info)
		if err != nil {
			return err
		}
		if copied {
			result.Copied++
		} else {
			result.Preserved++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if err := hardenPrivateTree(filesDir); err != nil {
		return result, err
	}
	if err := writePrivateFileAtomic(marker, []byte(legacyMigrationVersion)); err != nil {
		return result, err
	}
	return result, nil
}

func validMigrationMarker(path string) (bool, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return false, fmt.Errorf("migration marker is not a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return false, err
	}
	return bytes.Equal(b, []byte(legacyMigrationVersion)), nil
}

func ensurePrivateDir(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		st, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("private storage path is not a real directory")
	}
	return os.Chmod(path, 0o700)
}

func hardenPrivateTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private storage contains a symbolic link")
		}
		if info.IsDir() {
			return os.Chmod(path, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("private storage contains an unsupported entry")
		}
		return os.Chmod(path, 0o600)
	})
}

func safeStorageJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) || rel == "." || rel == "" {
		return "", fmt.Errorf("invalid app-storage relative path")
	}
	target := filepath.Join(root, rel)
	check, err := filepath.Rel(root, target)
	if err != nil || check == ".." || filepath.IsAbs(check) || len(check) > 3 && check[:3] == ".."+string(os.PathSeparator) {
		return "", fmt.Errorf("app-storage path escapes its root")
	}
	return target, nil
}

func pathsOverlap(a, b string) bool {
	within := func(root, candidate string) bool {
		rel, err := filepath.Rel(root, candidate)
		return err == nil && (rel == "." || rel != ".." && !filepath.IsAbs(rel) && !(len(rel) > 3 && rel[:3] == ".."+string(os.PathSeparator)))
	}
	return within(a, b) || within(b, a)
}

func copyPrivateFileAtomicNoReplace(source, dest string, sourceInfo os.FileInfo) (bool, error) {
	if st, err := os.Lstat(dest); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return false, fmt.Errorf("persistent FilesDir destination is not a regular file")
		}
		return false, os.Chmod(dest, 0o600)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := ensurePrivateDir(filepath.Dir(dest)); err != nil {
		return false, err
	}
	src, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer src.Close()
	openedInfo, err := src.Stat()
	if err != nil {
		return false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
		return false, fmt.Errorf("legacy file changed during migration")
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tipsy-migrate-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmpName, dest); err != nil {
		if st, statErr := os.Lstat(dest); statErr == nil && st.Mode().IsRegular() {
			return false, os.Chmod(dest, 0o600)
		}
		return false, err
	}
	if err := syncDirectory(filepath.Dir(dest)); err != nil {
		return false, err
	}
	return true, nil
}

func writePrivateFileAtomic(dest string, body []byte) error {
	if err := ensurePrivateDir(filepath.Dir(dest)); err != nil {
		return err
	}
	if st, err := os.Lstat(dest); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return fmt.Errorf("private file destination is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".tipsy-atomic-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(dest))
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
