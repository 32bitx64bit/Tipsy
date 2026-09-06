// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/integrity"
	"golang.org/x/sys/unix"
)

const maxAuthorizedNativeSize int64 = 2 << 30

type authorizedFile struct {
	descriptor integrity.NativeDescriptor
	duplicate  *os.File
	identity   integrity.FileIdentity
}

// authorizedFiles owns the Loader-side duplicates for one authenticated
// generation. The setup/integrity descriptors remain owned by their caller.
type authorizedFiles struct {
	source          *integrity.NativeDescriptorSet
	generationID    string
	inventorySHA256 string
	bySONAME        map[string]*authorizedFile
	ordered         []*authorizedFile

	closeOnce sync.Once
	closeErr  error
}

// OpenFD maps rootSONAME and its guest DT_NEEDED closure exclusively from a
// closed set of already-authenticated, still-open descriptors. It never opens
// a pathname (including /proc/self/fd), and keeps its duplicates alive until
// the returned root Module is closed.
//
// The caller must keep the AuthorizedGeneration that produced set alive through
// Init. OpenFD duplicates and rechecks every input before parsing, then rechecks
// the bound set plus both the original and duplicate after all mappings are
// complete. Init performs the same gate immediately before any constructor.
func OpenFD(ctx context.Context, rootSONAME string, set *integrity.NativeDescriptorSet, r Resolver) (*Module, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAuthorizedSONAME(rootSONAME); err != nil {
		return nil, fmt.Errorf("loader: authorized root: %w", err)
	}
	if err := validateNativeDescriptorSet(set); err != nil {
		return nil, err
	}
	files, err := prepareAuthorizedFiles(ctx, set)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = files.close()
		}
	}()
	if err := files.verifyBinding(); err != nil {
		return nil, fmt.Errorf("loader: authorized descriptor-set binding changed during duplication: %w", err)
	}

	if files.bySONAME[rootSONAME] == nil {
		return nil, fmt.Errorf("loader: authorized root %s is missing", rootSONAME)
	}
	m, err := openAuthorized(rootSONAME, r, newSession(), files)
	if err != nil {
		return nil, err
	}
	m.authorized = files
	m.authorizedRoot = true
	if err := files.verify(ctx); err != nil {
		_ = m.Close()
		return nil, fmt.Errorf("loader: authorized descriptors changed while mapping: %w", err)
	}
	keep = true
	return m, nil
}

func prepareAuthorizedFiles(ctx context.Context, set *integrity.NativeDescriptorSet) (*authorizedFiles, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	descriptors, err := set.Descriptors()
	if err != nil {
		return nil, fmt.Errorf("loader: authenticated native descriptor set: %w", err)
	}
	if len(descriptors) == 0 {
		return nil, fmt.Errorf("loader: authorized native descriptor set is empty")
	}
	files := &authorizedFiles{
		source: set, generationID: set.GenerationID(), inventorySHA256: set.InventorySHA256(),
		bySONAME: make(map[string]*authorizedFile, len(descriptors)),
	}
	identities := make(map[[2]uint64]string, len(descriptors))
	fail := func(err error) (*authorizedFiles, error) {
		_ = files.close()
		return nil, err
	}
	for _, descriptor := range descriptors {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := validateAuthorizedDescriptor(descriptor); err != nil {
			return fail(err)
		}
		if descriptor.GenerationID != files.generationID || descriptor.InventorySHA256 != files.inventorySHA256 {
			return fail(fmt.Errorf("loader: authorized %s is bound to a different generation", descriptor.SONAME))
		}
		if _, duplicate := files.bySONAME[descriptor.SONAME]; duplicate {
			return fail(fmt.Errorf("loader: duplicate authorized SONAME %s", descriptor.SONAME))
		}
		if err := descriptor.File.Recheck(ctx); err != nil {
			return fail(fmt.Errorf("loader: recheck authorized %s: %w", descriptor.SONAME, err))
		}
		duplicate, err := descriptor.File.Dup()
		if err != nil {
			return fail(fmt.Errorf("loader: duplicate authorized %s: %w", descriptor.SONAME, err))
		}
		authorized := &authorizedFile{descriptor: descriptor, duplicate: duplicate, identity: descriptor.File.Identity}
		files.bySONAME[descriptor.SONAME] = authorized
		files.ordered = append(files.ordered, authorized)

		identityKey := [2]uint64{authorized.identity.Device, authorized.identity.Inode}
		if other := identities[identityKey]; other != "" {
			return fail(fmt.Errorf("loader: authorized SONAMEs %s and %s name the same file", other, descriptor.SONAME))
		}
		identities[identityKey] = descriptor.SONAME
		if err := authorized.verifyDuplicate(ctx); err != nil {
			return fail(fmt.Errorf("loader: verify duplicate %s: %w", descriptor.SONAME, err))
		}
	}
	return files, nil
}

func validateNativeDescriptorSet(set *integrity.NativeDescriptorSet) error {
	if set == nil {
		return fmt.Errorf("loader: authenticated native descriptor set is required")
	}
	if err := set.Validate(); err != nil {
		return fmt.Errorf("loader: authenticated native descriptor set: %w", err)
	}
	generationID := set.GenerationID()
	inventorySHA256 := set.InventorySHA256()
	if !validAuthorizedDigest(generationID) || !validAuthorizedDigest(inventorySHA256) || generationID != inventorySHA256 {
		return fmt.Errorf("loader: authenticated native descriptor set has inconsistent generation identity")
	}
	return nil
}

func validateAuthorizedDescriptor(descriptor integrity.NativeDescriptor) error {
	if err := validateAuthorizedSONAME(descriptor.SONAME); err != nil {
		return fmt.Errorf("loader: invalid authorized descriptor: %w", err)
	}
	if descriptor.File == nil || descriptor.File.File == nil {
		return fmt.Errorf("loader: authorized %s has no open descriptor", descriptor.SONAME)
	}
	record := descriptor.File.Record
	wantPath := "lib/x86_64/" + descriptor.SONAME
	if record.Path != wantPath || path.Base(record.Path) != descriptor.SONAME || !record.Executable {
		return fmt.Errorf("loader: authorized %s has inconsistent native inventory path", descriptor.SONAME)
	}
	if descriptor.ExpectedSize <= 0 || descriptor.ExpectedSize > maxAuthorizedNativeSize || descriptor.ExpectedSize != record.Size {
		return fmt.Errorf("loader: authorized %s has invalid expected size", descriptor.SONAME)
	}
	identity := descriptor.File.Identity
	if identity.Mode&unix.S_IFMT != unix.S_IFREG || identity.UID != uint32(os.Geteuid()) || identity.Links != 1 || identity.Size != descriptor.ExpectedSize || identity.Mode&0o222 != 0 {
		return fmt.Errorf("loader: authorized %s has unsafe file identity or permissions", descriptor.SONAME)
	}
	if !validAuthorizedDigest(descriptor.ExpectedSHA256) || descriptor.ExpectedSHA256 != record.SHA256 {
		return fmt.Errorf("loader: authorized %s has inconsistent content digest", descriptor.SONAME)
	}
	if !validAuthorizedDigest(descriptor.APKDigest) || descriptor.APKDigest != record.APKDigest {
		return fmt.Errorf("loader: authorized %s has inconsistent APK digest", descriptor.SONAME)
	}
	if descriptor.APKEntry == "" || descriptor.APKEntry != record.APKEntry || !safeAuthorizedRelative(descriptor.APKEntry) {
		return fmt.Errorf("loader: authorized %s has inconsistent APK origin", descriptor.SONAME)
	}
	return nil
}

func validateAuthorizedSONAME(soname string) error {
	if soname == "" || len(soname) > 255 || path.Base(soname) != soname || strings.ContainsAny(soname, "/\\\x00") || soname == "." || soname == ".." {
		return fmt.Errorf("unsafe SONAME %q", soname)
	}
	if _, system := systemSonames[soname]; system {
		return fmt.Errorf("system SONAME %q cannot be supplied as a guest descriptor", soname)
	}
	return nil
}

func safeAuthorizedRelative(name string) bool {
	if name == "" || len(name) > 512 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") || path.Clean(name) != name {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func validAuthorizedDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (files *authorizedFiles) verify(ctx context.Context) error {
	if files == nil {
		return fmt.Errorf("authorized descriptor set is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := files.verifyBinding(); err != nil {
		return err
	}
	for _, file := range files.ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file == nil || file.descriptor.File == nil {
			return fmt.Errorf("authorized descriptor is unavailable")
		}
		if err := file.descriptor.File.Recheck(ctx); err != nil {
			return fmt.Errorf("original %s: %w", file.descriptor.SONAME, err)
		}
		if err := file.verifyDuplicate(ctx); err != nil {
			return fmt.Errorf("duplicate %s: %w", file.descriptor.SONAME, err)
		}
	}
	return nil
}

func (files *authorizedFiles) verifyBinding() error {
	if files.source == nil {
		return fmt.Errorf("authorized descriptor-set binding is unavailable")
	}
	if err := validateNativeDescriptorSet(files.source); err != nil {
		return err
	}
	if files.source.GenerationID() != files.generationID || files.source.InventorySHA256() != files.inventorySHA256 {
		return fmt.Errorf("authorized descriptor-set generation identity changed")
	}
	descriptors, err := files.source.Descriptors()
	if err != nil {
		return fmt.Errorf("authorized descriptor-set binding: %w", err)
	}
	if len(descriptors) != len(files.ordered) {
		return fmt.Errorf("authorized descriptor-set membership changed")
	}
	for _, descriptor := range descriptors {
		if descriptor.GenerationID != files.generationID || descriptor.InventorySHA256 != files.inventorySHA256 {
			return fmt.Errorf("authorized %s is bound to a different generation", descriptor.SONAME)
		}
		file := files.bySONAME[descriptor.SONAME]
		if file == nil || !sameAuthorizedDescriptor(file.descriptor, descriptor) {
			return fmt.Errorf("authorized descriptor-set membership changed")
		}
	}
	return nil
}

func sameAuthorizedDescriptor(a, b integrity.NativeDescriptor) bool {
	return a.GenerationID == b.GenerationID && a.InventorySHA256 == b.InventorySHA256 &&
		a.SONAME == b.SONAME && a.APKEntry == b.APKEntry && a.APKDigest == b.APKDigest &&
		a.ExpectedSHA256 == b.ExpectedSHA256 && a.ExpectedSize == b.ExpectedSize && a.File == b.File
}

func (file *authorizedFile) verifyDuplicate(ctx context.Context) error {
	if file == nil || file.duplicate == nil {
		return fmt.Errorf("descriptor is closed")
	}
	before, err := authorizedIdentityOf(file.duplicate)
	if err != nil {
		return err
	}
	if !sameAuthorizedIdentity(before, file.identity) || before.Size != file.descriptor.ExpectedSize {
		return fmt.Errorf("descriptor identity changed")
	}
	digest, err := digestAuthorizedFile(ctx, file.duplicate, file.descriptor.ExpectedSize)
	if err != nil {
		return err
	}
	after, err := authorizedIdentityOf(file.duplicate)
	if err != nil {
		return err
	}
	if !sameAuthorizedIdentity(before, after) || digest != file.descriptor.ExpectedSHA256 {
		return fmt.Errorf("descriptor content changed")
	}
	return nil
}

func authorizedIdentityOf(file *os.File) (integrity.FileIdentity, error) {
	if file == nil {
		return integrity.FileIdentity{}, fmt.Errorf("descriptor is closed")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return integrity.FileIdentity{}, err
	}
	return integrity.FileIdentity{
		Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size, Mode: stat.Mode,
		UID: stat.Uid, Links: uint64(stat.Nlink), MTimeSec: stat.Mtim.Sec, MTimeNsec: stat.Mtim.Nsec,
		CTimeSec: stat.Ctim.Sec, CTimeNsec: stat.Ctim.Nsec,
	}, nil
}

func sameAuthorizedIdentity(a, b integrity.FileIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Size == b.Size && a.Mode == b.Mode && a.UID == b.UID && a.Links == b.Links &&
		a.MTimeSec == b.MTimeSec && a.MTimeNsec == b.MTimeNsec && a.CTimeSec == b.CTimeSec && a.CTimeNsec == b.CTimeNsec
}

func digestAuthorizedFile(ctx context.Context, file *os.File, size int64) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := sha256.New()
	reader := io.NewSectionReader(file, 0, size)
	buffer := make([]byte, 1<<20)
	var read int64
	for read < size {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			read += int64(n)
			_, _ = hash.Write(buffer[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
	}
	if read != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (files *authorizedFiles) close() error {
	if files == nil {
		return nil
	}
	files.closeOnce.Do(func() {
		for _, file := range files.ordered {
			if file != nil && file.duplicate != nil {
				if err := file.duplicate.Close(); err != nil && files.closeErr == nil {
					files.closeErr = err
				}
				file.duplicate = nil
			}
		}
	})
	return files.closeErr
}
