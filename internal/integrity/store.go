// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package integrity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

const (
	activeFileName    = "active.json"
	inventoryFileName = "inventory.json"
)

type Store struct {
	Root      string
	afterCopy func(string)
}

// Stage copies exactly the authenticated inventory from sourceRoot into a new
// content-addressed, read-only-by-convention generation. It does not activate
// the result.
func (s Store) Stage(ctx context.Context, sourceRoot string, in Inventory) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, id, err := CanonicalInventory(in)
	if err != nil {
		return "", err
	}
	root, err := s.prepareRoot()
	if err != nil {
		return "", err
	}
	generations := filepath.Join(root, "generations")
	if err := secureDirectory(generations, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(generations, id)
	repair := false
	if _, err := os.Lstat(final); err == nil {
		generation, err := OpenGeneration(ctx, root, id)
		if err != nil {
			repair = true
		} else {
			if err := generation.Close(); err != nil {
				return "", err
			}
			for _, record := range in.Files {
				if record.Origin == OriginAPK && record.APKEntry == "@apk" {
					if err := retainAPKBlob(ctx, sourceRoot, root, record); err != nil {
						return "", err
					}
				}
			}
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	stage, err := os.MkdirTemp(generations, ".stage-")
	if err != nil {
		return "", fmt.Errorf("integrity: create generation stage: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			makeTreeWritableForCleanup(stage)
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return "", err
	}
	for _, record := range in.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := copyVerifiedRecord(ctx, sourceRoot, stage, record, s.afterCopy); err != nil {
			return "", err
		}
		if record.Origin == OriginAPK && record.APKEntry == "@apk" {
			if err := retainAPKBlob(ctx, sourceRoot, root, record); err != nil {
				return "", err
			}
		}
	}
	if err := writeExclusiveSynced(filepath.Join(stage, inventoryFileName), raw, 0o400); err != nil {
		return "", err
	}
	if err := makeTreeReadOnly(stage); err != nil {
		return "", err
	}
	if err := syncDirectory(stage); err != nil {
		return "", err
	}
	if repair {
		rejected := filepath.Join(root, "rejected")
		if err := secureDirectory(rejected, 0o700); err != nil {
			return "", err
		}
		if err := exchangeGeneration(stage, final); err != nil {
			return "", fmt.Errorf("integrity: atomically repair generation: %w", err)
		}
		if verified, err := OpenGeneration(ctx, root, id); err != nil {
			// Exchange back when verification of the replacement fails.
			_ = exchangeGeneration(stage, final)
			return "", fmt.Errorf("integrity: repaired generation verification failed: %w", err)
		} else {
			_ = verified.Close()
		}
		// stage now names the rejected old tree. From this point preserve it
		// even if moving it into the rejected directory is interrupted.
		removeStage = false
		if err := os.Chmod(stage, 0o700); err != nil {
			return "", fmt.Errorf("integrity: prepare rejected generation for preservation: %w", err)
		}
		rejectedName, err := os.CreateTemp(rejected, id+"-")
		if err != nil {
			return "", err
		}
		rejectedPath := rejectedName.Name()
		if err := rejectedName.Close(); err != nil {
			return "", err
		}
		if err := os.Remove(rejectedPath); err != nil {
			return "", err
		}
		if err := os.Rename(stage, rejectedPath); err != nil {
			return "", fmt.Errorf("integrity: preserve rejected generation: %w", err)
		}
		if err := syncDirectory(rejected); err != nil {
			return "", err
		}
		if err := syncDirectory(generations); err != nil {
			return "", err
		}
		return id, nil
	}
	if err := os.Rename(stage, final); err != nil {
		if _, statErr := os.Lstat(final); statErr == nil {
			if existing, verifyErr := OpenGeneration(ctx, root, id); verifyErr == nil {
				_ = existing.Close()
				return id, nil
			}
		}
		return "", fmt.Errorf("integrity: publish generation: %w", err)
	}
	removeStage = false
	if err := syncDirectory(generations); err != nil {
		return "", err
	}
	return id, nil
}

// Activate atomically selects a completely verified generation. Previous
// generation directories are retained.
func (s Store) Activate(ctx context.Context, id string) error {
	if err := ValidateGenerationID(id); err != nil {
		return err
	}
	root, err := s.prepareRoot()
	if err != nil {
		return err
	}
	generation, err := OpenGeneration(ctx, root, id)
	if err != nil {
		return err
	}
	inventoryDigest := generation.InventorySHA256
	if err := generation.Close(); err != nil {
		return err
	}
	record := ActiveRecord{Schema: ActiveSchema, Generation: id, InventorySHA256: inventoryDigest}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".active-")
	if err != nil {
		return fmt.Errorf("integrity: create active record: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(root, activeFileName)); err != nil {
		return fmt.Errorf("integrity: activate generation: %w", err)
	}
	keep = true
	return syncDirectory(root)
}

func (s Store) Active(ctx context.Context) (*Generation, error) {
	root, err := s.prepareRoot()
	if err != nil {
		return nil, err
	}
	raw, err := readBoundedSafe(root, activeFileName, 4096, false)
	if err != nil {
		return nil, fmt.Errorf("integrity: read active record: %w", err)
	}
	var record ActiveRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || record.Schema != ActiveSchema || ValidateGenerationID(record.Generation) != nil || !validDigest(record.InventorySHA256) {
		return nil, fmt.Errorf("integrity: active record is malformed")
	}
	canonical, err := json.Marshal(record)
	if err != nil || string(canonical) != string(raw) {
		return nil, fmt.Errorf("integrity: active record is not canonical")
	}
	generation, err := OpenGeneration(ctx, root, record.Generation)
	if err != nil {
		return nil, err
	}
	if generation.InventorySHA256 != record.InventorySHA256 {
		generation.Close()
		return nil, fmt.Errorf("integrity: active inventory digest mismatch")
	}
	return generation, nil
}

func (s Store) prepareRoot() (string, error) {
	if s.Root == "" {
		return "", fmt.Errorf("integrity: store root is empty")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	if err := secureDirectory(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func copyVerifiedRecord(ctx context.Context, sourceRoot, stage string, record FileRecord, afterCopy func(string)) error {
	source, err := openVerifiedAt(ctx, sourceRoot, record, false)
	if err != nil {
		return fmt.Errorf("integrity: verify staged source %s: %w", record.Path, err)
	}
	defer source.File.Close()
	dest := filepath.Join(stage, filepath.FromSlash(record.Path))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.NewSectionReader(source.File, 0, record.Size))
	syncErr := out.Sync()
	chmodErr := out.Chmod(0o400)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if n != record.Size {
		return io.ErrUnexpectedEOF
	}
	if syncErr != nil {
		return syncErr
	}
	if chmodErr != nil {
		return chmodErr
	}
	if closeErr != nil {
		return closeErr
	}
	if afterCopy != nil {
		afterCopy(record.Path)
	}
	return source.Recheck(ctx)
}

func retainAPKBlob(ctx context.Context, sourceRoot, storeRoot string, record FileRecord) error {
	if record.Origin != OriginAPK || record.APKEntry != "@apk" || record.SHA256 != record.APKDigest {
		return fmt.Errorf("integrity: retained APK digest is inconsistent")
	}
	blobRoot := filepath.Join(storeRoot, "apks")
	if err := secureDirectory(filepath.Join(blobRoot, "sha256"), 0o700); err != nil {
		return err
	}
	blobRecord := record
	blobRecord.Path = filepath.ToSlash(filepath.Join("sha256", record.SHA256+".apk"))
	if existing, err := openVerifiedAt(ctx, blobRoot, blobRecord, true); err == nil {
		return existing.File.Close()
	} else if _, statErr := os.Lstat(filepath.Join(blobRoot, filepath.FromSlash(blobRecord.Path))); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	source, err := openVerifiedAt(ctx, sourceRoot, record, false)
	if err != nil {
		return fmt.Errorf("integrity: verify retained APK source: %w", err)
	}
	defer source.File.Close()
	tmp, err := os.CreateTemp(filepath.Join(blobRoot, "sha256"), ".apk-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	published := false
	defer func() {
		_ = tmp.Close()
		if !published {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o400); err != nil {
		return err
	}
	n, copyErr := io.Copy(tmp, io.NewSectionReader(source.File, 0, record.Size))
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if n != record.Size {
		return io.ErrUnexpectedEOF
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := source.Recheck(ctx); err != nil {
		return err
	}
	final := filepath.Join(blobRoot, filepath.FromSlash(blobRecord.Path))
	if err := os.Rename(tmpName, final); err != nil {
		if existing, verifyErr := openVerifiedAt(ctx, blobRoot, blobRecord, true); verifyErr == nil {
			_ = existing.File.Close()
			return nil
		}
		return fmt.Errorf("integrity: publish retained APK: %w", err)
	}
	published = true
	return syncDirectory(filepath.Dir(final))
}

func writeExclusiveSynced(name string, raw []byte, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func secureDirectory(name string, mode os.FileMode) error {
	if err := os.MkdirAll(name, mode); err != nil {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("integrity: unsafe private directory")
	}
	return nil
}

func makeTreeReadOnly(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("integrity: generation contains a symbolic link")
		}
		if entry.IsDir() {
			dirs = append(dirs, name)
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeType != 0 {
			return fmt.Errorf("integrity: generation contains a special file")
		}
		return os.Chmod(name, 0o400)
	})
	if err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if err := os.Chmod(dir, 0o500); err != nil {
			return err
		}
	}
	return nil
}

func makeTreeWritableForCleanup(root string) {
	_ = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(name, 0o700)
		}
		return nil
	})
}

func syncDirectory(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
