// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package integrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type FileIdentity struct {
	Device      uint64
	Inode       uint64
	Size        int64
	Mode        uint32
	UID         uint32
	Links       uint64
	MTimeSec    int64
	MTimeNsec   int64
	CTimeSec    int64
	CTimeNsec   int64
	Openat2Used bool
}

type PinnedFile struct {
	File     *os.File
	Record   FileRecord
	Identity FileIdentity
}

type Generation struct {
	ID              string
	Inventory       Inventory
	InventorySHA256 string
	Files           map[string]*PinnedFile
}

// NativeDescriptor is a verified still-open guest DSO prepared for Loader.
// Loader must duplicate File and map that duplicate without resolving a path.
type NativeDescriptor struct {
	GenerationID    string
	InventorySHA256 string
	SONAME          string
	APKEntry        string
	APKDigest       string
	ExpectedSHA256  string
	ExpectedSize    int64
	File            *PinnedFile
}

// NativeDescriptorSet binds every descriptor to the one canonical generation
// inventory that authorized it. The slice is private so callers cannot append
// a descriptor from another generation without failing Validate.
type NativeDescriptorSet struct {
	generationID    string
	inventorySHA256 string
	descriptors     []NativeDescriptor
}

func (s *NativeDescriptorSet) GenerationID() string {
	if s == nil {
		return ""
	}
	return s.generationID
}

func (s *NativeDescriptorSet) InventorySHA256() string {
	if s == nil {
		return ""
	}
	return s.inventorySHA256
}

// Descriptors returns a copy for the Loader handoff. Each descriptor repeats
// the binding so a future Loader API can reject a mixed slice defensively.
func (s *NativeDescriptorSet) Descriptors() ([]NativeDescriptor, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return append([]NativeDescriptor(nil), s.descriptors...), nil
}

func (s *NativeDescriptorSet) Validate() error {
	if s == nil || ValidateGenerationID(s.generationID) != nil || !validDigest(s.inventorySHA256) || s.generationID != s.inventorySHA256 || len(s.descriptors) == 0 {
		return fmt.Errorf("integrity: invalid native descriptor-set binding")
	}
	seen := make(map[string]struct{}, len(s.descriptors))
	for _, descriptor := range s.descriptors {
		if descriptor.GenerationID != s.generationID || descriptor.InventorySHA256 != s.inventorySHA256 || descriptor.File == nil || descriptor.File.File == nil {
			return fmt.Errorf("integrity: native descriptor is from a different or closed generation")
		}
		if _, err := descriptor.File.File.Stat(); err != nil {
			return fmt.Errorf("integrity: native descriptor is from a different or closed generation")
		}
		if _, duplicate := seen[descriptor.SONAME]; duplicate {
			return fmt.Errorf("integrity: duplicate native SONAME %q", descriptor.SONAME)
		}
		seen[descriptor.SONAME] = struct{}{}
	}
	return nil
}

// NativeDescriptorSet returns every authenticated native dependency in stable
// SONAME order and binds it to this generation. Generation owns all underlying
// descriptors until it is closed.
func (g *Generation) NativeDescriptorSet() (*NativeDescriptorSet, error) {
	if g == nil {
		return nil, fmt.Errorf("integrity: generation is nil")
	}
	result := make([]NativeDescriptor, 0)
	seen := make(map[string]struct{})
	for _, record := range g.Inventory.Files {
		if !record.Executable {
			continue
		}
		file := g.Files[record.Path]
		if file == nil || file.File == nil {
			return nil, fmt.Errorf("integrity: native descriptor %q is unavailable", record.Path)
		}
		soname := filepath.Base(record.Path)
		if soname == "." || soname == "/" || soname == "" {
			return nil, fmt.Errorf("integrity: native descriptor has no SONAME")
		}
		if _, duplicate := seen[soname]; duplicate {
			return nil, fmt.Errorf("integrity: duplicate native SONAME %q", soname)
		}
		seen[soname] = struct{}{}
		result = append(result, NativeDescriptor{
			GenerationID: g.ID, InventorySHA256: g.InventorySHA256,
			SONAME: soname, APKEntry: record.APKEntry, APKDigest: record.APKDigest,
			ExpectedSHA256: record.SHA256, ExpectedSize: record.Size, File: file,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SONAME < result[j].SONAME })
	set := &NativeDescriptorSet{generationID: g.ID, inventorySHA256: g.InventorySHA256, descriptors: result}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	return set, nil
}

// NativeDescriptors is a compatibility shim for the Phase 3B runtime
// interface. Official integration must consume NativeDescriptorSet so the
// generation binding is not discarded.
func (g *Generation) NativeDescriptors() ([]NativeDescriptor, error) {
	set, err := g.NativeDescriptorSet()
	if err != nil {
		return nil, err
	}
	return set.Descriptors()
}

func OpenGeneration(ctx context.Context, storeRoot, id string) (*Generation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateGenerationID(id); err != nil {
		return nil, err
	}
	root := filepath.Join(storeRoot, "generations", id)
	raw, err := readBoundedSafe(root, inventoryFileName, MaxInventorySize, true)
	if err != nil {
		return nil, fmt.Errorf("integrity: read generation inventory: %w", err)
	}
	inventory, digest, err := DecodeInventory(raw)
	if err != nil {
		return nil, err
	}
	if digest != id {
		return nil, fmt.Errorf("integrity: generation id does not match inventory")
	}
	generation := &Generation{ID: id, Inventory: inventory, InventorySHA256: digest, Files: make(map[string]*PinnedFile, len(inventory.Files))}
	for _, record := range inventory.Files {
		if err := ctx.Err(); err != nil {
			generation.Close()
			return nil, err
		}
		opened, err := openVerifiedAt(ctx, root, record, true)
		if err != nil {
			generation.Close()
			return nil, fmt.Errorf("integrity: open generation file %s: %w", record.Path, err)
		}
		generation.Files[record.Path] = opened
	}
	return generation, nil
}

func (g *Generation) Close() error {
	if g == nil {
		return nil
	}
	var first error
	for name, file := range g.Files {
		if file != nil && file.File != nil {
			if err := file.File.Close(); err != nil && first == nil {
				first = err
			}
		}
		delete(g.Files, name)
	}
	return first
}

// Dup returns another descriptor for Loader handoff. It names the same open
// file description; Loader must map this descriptor and call Recheck after
// mapping, never reopen Record.Path.
func (p *PinnedFile) Dup() (*os.File, error) {
	if p == nil || p.File == nil {
		return nil, fmt.Errorf("integrity: pinned file is closed")
	}
	fd, err := unix.FcntlInt(p.File.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), p.Record.Path), nil
}

func (p *PinnedFile) Recheck(ctx context.Context) error {
	if p == nil || p.File == nil {
		return fmt.Errorf("integrity: pinned file is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	before, err := identityOf(int(p.File.Fd()), p.Identity.Openat2Used)
	if err != nil {
		return err
	}
	if !sameIdentity(before, p.Identity) {
		return fmt.Errorf("integrity: pinned file identity changed")
	}
	digest, err := digestWithContext(ctx, p.File, p.Record.Size)
	if err != nil {
		return err
	}
	after, err := identityOf(int(p.File.Fd()), p.Identity.Openat2Used)
	if err != nil {
		return err
	}
	if digest != p.Record.SHA256 || !sameIdentity(before, after) {
		return fmt.Errorf("integrity: pinned file content changed")
	}
	return nil
}

func openVerifiedAt(ctx context.Context, root string, record FileRecord, requireReadOnly bool) (*PinnedFile, error) {
	if !safeRelative(record.Path) {
		return nil, fmt.Errorf("unsafe relative path")
	}
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	fd, usedOpenat2, err := openBeneath(rootFD, record.Path)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), record.Path)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("cannot own opened descriptor")
	}
	identity, err := identityOf(fd, usedOpenat2)
	if err != nil {
		file.Close()
		return nil, err
	}
	if identity.Mode&unix.S_IFMT != unix.S_IFREG || identity.UID != uint32(os.Geteuid()) || identity.Links != 1 || identity.Size != record.Size || identity.Mode&0o022 != 0 ||
		(requireReadOnly && identity.Mode&0o222 != 0) {
		file.Close()
		return nil, fmt.Errorf("unsafe file identity or permissions")
	}
	digest, err := digestWithContext(ctx, file, record.Size)
	if err != nil {
		file.Close()
		return nil, err
	}
	after, err := identityOf(fd, usedOpenat2)
	if err != nil {
		file.Close()
		return nil, err
	}
	if digest != record.SHA256 || !sameIdentity(identity, after) {
		file.Close()
		return nil, fmt.Errorf("content digest or file identity mismatch")
	}
	return &PinnedFile{File: file, Record: record, Identity: after}, nil
}

func openBeneath(rootFD int, name string) (int, bool, error) {
	how := &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := openat2Call(rootFD, name, how)
	if err == nil {
		return fd, true, nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.E2BIG) {
		return -1, false, err
	}
	return openatFallback(rootFD, name)
}

var openat2Call = unix.Openat2

func exchangeGeneration(left, right string) error {
	return unix.Renameat2(unix.AT_FDCWD, left, unix.AT_FDCWD, right, unix.RENAME_EXCHANGE)
}

func openatFallback(rootFD int, name string) (int, bool, error) {
	parts := strings.Split(name, "/")
	if len(parts) == 0 {
		return -1, false, fmt.Errorf("empty path")
	}
	current, err := unix.FcntlInt(uintptr(rootFD), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, false, err
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(current)
			return -1, false, fmt.Errorf("unsafe path component")
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i != len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(current, part, flags, 0)
		unix.Close(current)
		if err != nil {
			return -1, false, err
		}
		current = next
	}
	return current, false, nil
}

func identityOf(fd int, openat2 bool) (FileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return FileIdentity{}, err
	}
	return FileIdentity{
		Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size, Mode: stat.Mode,
		UID: stat.Uid, Links: uint64(stat.Nlink), MTimeSec: stat.Mtim.Sec, MTimeNsec: stat.Mtim.Nsec,
		CTimeSec: stat.Ctim.Sec, CTimeNsec: stat.Ctim.Nsec, Openat2Used: openat2,
	}, nil
}

func sameIdentity(a, b FileIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Size == b.Size && a.Mode == b.Mode && a.UID == b.UID && a.Links == b.Links &&
		a.MTimeSec == b.MTimeSec && a.MTimeNsec == b.MTimeNsec && a.CTimeSec == b.CTimeSec && a.CTimeNsec == b.CTimeNsec
}

func digestWithContext(ctx context.Context, file *os.File, size int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	reader := io.NewSectionReader(file, 0, size)
	buffer := make([]byte, 1<<20)
	hash := sha256.New()
	var read int64
	for read < size {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := reader.Read(buffer)
		read += int64(n)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if read != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readBoundedSafe(root, name string, maximum int, requireReadOnly bool) ([]byte, error) {
	record := FileRecord{Path: name, Size: 1, SHA256: strings.Repeat("0", 64)}
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFD)
	fd, _, err := openBeneath(rootFD, record.Path)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("cannot own opened descriptor")
	}
	defer file.Close()
	identity, err := identityOf(fd, false)
	if err != nil {
		return nil, err
	}
	if identity.Mode&unix.S_IFMT != unix.S_IFREG || identity.UID != uint32(os.Geteuid()) || identity.Links != 1 || identity.Size <= 0 || identity.Size > int64(maximum) || identity.Mode&0o022 != 0 ||
		(requireReadOnly && identity.Mode&0o222 != 0) {
		return nil, fmt.Errorf("unsafe bounded file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || int64(len(raw)) != identity.Size {
		return nil, fmt.Errorf("read bounded file: %w", err)
	}
	after, err := identityOf(fd, false)
	if err != nil || !sameIdentity(identity, after) {
		return nil, fmt.Errorf("bounded file changed while reading")
	}
	return raw, nil
}
