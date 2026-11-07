// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package apk

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/integrity"
)

// ZIPEntryVerifier indexes a retained signed APK descriptor once, then
// verifies any number of extracted entries without repeated central-directory
// scans. The caller remains responsible for retaining and rechecking ra.
type ZIPEntryVerifier struct {
	entries map[string]*zip.File
}

func NewZIPEntryVerifier(ra io.ReaderAt, apkSize int64) (*ZIPEntryVerifier, error) {
	if ra == nil || apkSize <= 0 {
		return nil, fmt.Errorf("apk: invalid ZIP descriptor input")
	}
	zr, err := zip.NewReader(ra, apkSize)
	if err != nil {
		return nil, fmt.Errorf("apk: open authenticated ZIP: %w", err)
	}
	verifier := &ZIPEntryVerifier{entries: make(map[string]*zip.File, len(zr.File))}
	for _, file := range zr.File {
		if _, duplicate := verifier.entries[file.Name]; duplicate {
			return nil, fmt.Errorf("apk: duplicate authenticated ZIP entry %q", file.Name)
		}
		verifier.entries[file.Name] = file
	}
	return verifier, nil
}

// InspectVerifiedReaderAt inspects and verifies one already-open APK without
// resolving or reopening a pathname. label is diagnostic identity only.
func InspectVerifiedReaderAt(ctx context.Context, ra io.ReaderAt, size int64, label string) (Package, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Package{}, err
	}
	if ra == nil || size <= 0 {
		return Package{}, fmt.Errorf("apk: invalid descriptor input")
	}
	if label == "" {
		label = "@descriptor"
	}
	pkg, err := inspectSource(&apkSource{Display: label, Reader: ra, Size: size})
	if err != nil {
		return Package{}, err
	}
	verified, err := VerifyReaderAt(ctx, ra, size)
	if err != nil {
		return Package{}, err
	}
	pkg.Signing.CryptographicallyValid = true
	pkg.Signing.VerifiedScheme = verified.Scheme
	pkg.Signing.VerifiedCertSHA256 = verified.CurrentSHA256
	pkg.Signing.VerifiedLineageSHA256 = verified.LineageSHA256
	return *pkg, nil
}

// ReportFromPackages constructs the same canonical merged report used by
// Inspect, without reopening package paths.
func ReportFromPackages(packages []Package) *Report {
	copyOfPackages := append([]Package(nil), packages...)
	sortPackages(copyOfPackages)
	return &Report{Packages: copyOfPackages, Merged: mergePackages(copyOfPackages)}
}

// Verify checks one derived file against an authenticated APK descriptor. It
// rejects duplicate ZIP names and streams exactly the expected size.
func (v *ZIPEntryVerifier) Verify(ctx context.Context, entry string, expectedSize int64, expectedSHA256 string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if v == nil || expectedSize < 0 || len(expectedSHA256) != sha256.Size*2 {
		return fmt.Errorf("apk: invalid ZIP entry verification input")
	}
	selected := v.entries[entry]
	if selected == nil || selected.FileInfo().IsDir() || selected.UncompressedSize64 != uint64(expectedSize) {
		return fmt.Errorf("apk: authenticated ZIP entry %q is missing or has the wrong size", entry)
	}
	reader, err := selected.Open()
	if err != nil {
		return fmt.Errorf("apk: open authenticated ZIP entry: %w", err)
	}
	defer reader.Close()
	// One byte past the expected size is enough to detect overflow.
	digest, total, err := integrity.SHA256Stream(ctx, io.LimitReader(reader, expectedSize+1))
	if err != nil {
		return err
	}
	if total > expectedSize {
		return fmt.Errorf("apk: authenticated ZIP entry exceeds expected size")
	}
	if total != expectedSize || !strings.EqualFold(digest, expectedSHA256) {
		return fmt.Errorf("apk: authenticated ZIP entry digest mismatch")
	}
	return nil
}
