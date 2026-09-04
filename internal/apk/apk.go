package apk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Report is the result of Inspect.
type Report struct {
	Inputs   []string  `json:"inputs"`
	Packages []Package `json:"packages"`
	Merged   *Merged   `json:"merged,omitempty"`
}

// Package describes one APK file (base or split).
type Package struct {
	Path             string      `json:"path"`
	FileSHA256       string      `json:"fileSha256"`
	Size             int64       `json:"size"`
	PackageName      string      `json:"packageName,omitempty"`
	VersionName      string      `json:"versionName,omitempty"`
	VersionCode      int64       `json:"versionCode,omitempty"`
	SplitName        string      `json:"splitName,omitempty"`
	ApplicationLabel string      `json:"applicationLabel,omitempty"`
	Debuggable       bool        `json:"debuggable,omitempty"`
	NativeCode       []string    `json:"nativeCode,omitempty"`
	Architectures    []string    `json:"architectures,omitempty"`
	NativeLibraries  []NativeLib `json:"nativeLibraries,omitempty"`
	Signing          SigningInfo `json:"signing"`
	IsSplit          bool        `json:"isSplit,omitempty"`
	ManifestOK       bool        `json:"manifestOk"`
	UsesFeatures     []string    `json:"usesFeatures,omitempty"`
	LauncherActivity string      `json:"launcherActivity,omitempty"`
	GameActivities   []string    `json:"gameActivities,omitempty"`
}

// NativeLib is one uncompressed native library inside an APK.
type NativeLib struct {
	APKPath string `json:"apkPath"`
	ZIPPath string `json:"zipPath"`
	ABI     string `json:"abi"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// SigningInfo is JAR v1 and APK Signature Scheme v2/v3/v3.1 metadata.
type SigningInfo struct {
	HasV1      bool     `json:"hasV1"`
	HasV2      bool     `json:"hasV2"`
	HasV3      bool     `json:"hasV3"`
	HasV3_1    bool     `json:"hasV3_1"`
	CertSHA256 []string `json:"certSha256,omitempty"`
	Subjects   []string `json:"subjects,omitempty"`
	ParseError string   `json:"parseError,omitempty"`
}

// Merged is the union of a split set: one package name/version, all ABIs and natives.
type Merged struct {
	PackageName      string      `json:"packageName,omitempty"`
	VersionName      string      `json:"versionName,omitempty"`
	VersionCode      int64       `json:"versionCode,omitempty"`
	ApplicationLabel string      `json:"applicationLabel,omitempty"`
	Debuggable       bool        `json:"debuggable,omitempty"`
	NativeCode       []string    `json:"nativeCode,omitempty"`
	Architectures    []string    `json:"architectures,omitempty"`
	NativeLibraries  []NativeLib `json:"nativeLibraries,omitempty"`
	Signing          SigningInfo `json:"signing"`
	SplitNames       []string    `json:"splitNames,omitempty"`
}

var knownABIs = []string{
	"x86_64",
	"arm64-v8a",
	"armeabi-v7a",
	"x86",
	"armeabi",
	"mips64",
	"mips",
	"riscv64",
}

var knownABISet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(knownABIs))
	for _, a := range knownABIs {
		m[a] = struct{}{}
	}
	return m
}()

const nestedSep = "!"

type apkSource struct {
	Display string
	Reader  io.ReaderAt
	Size    int64
	closer  io.Closer
	owned   []byte // non-nil when the APK bytes are in memory (nested zip)
}

func (s *apkSource) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

func apkLog() *slog.Logger {
	return slog.Default().With("category", "apk")
}

// Inspect opens one or more APKs, a directory of APKs, or a nested
// .apkm/.xapk/.zip container and returns per-file plus merged metadata.
func Inspect(ctx context.Context, paths []string) (*Report, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("apk: no paths")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sources, err := collectSources(ctx, paths)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("apk: no APK files found in %v", paths)
	}
	rep := &Report{Inputs: append([]string(nil), paths...)}
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			for _, s := range sources {
				_ = s.Close()
			}
			return nil, err
		}
		pkg, err := inspectSource(src)
		_ = src.Close()
		if err != nil {
			return nil, fmt.Errorf("apk: inspect %s: %w", src.Display, err)
		}
		rep.Packages = append(rep.Packages, *pkg)
	}
	sortPackages(rep.Packages)
	rep.Merged = mergePackages(rep.Packages)
	return rep, nil
}

// ReadZipFile returns the uncompressed bytes of zipPath inside apkPath.
// Nested containers from Inspect use "container.apkm!inner.apk" display paths.
func ReadZipFile(apkPath, zipPath string) ([]byte, error) {
	src, err := openSource(apkPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	zr, err := zip.NewReader(src.Reader, src.Size)
	if err != nil {
		return nil, fmt.Errorf("apk: open zip %s: %w", apkPath, err)
	}
	zipPath = strings.TrimPrefix(filepath.ToSlash(zipPath), "/")
	for _, f := range zr.File {
		if filepath.ToSlash(f.Name) == zipPath {
			return readZipEntry(f)
		}
	}
	return nil, fmt.Errorf("apk: %s: %s not found", apkPath, zipPath)
}

func inspectSource(src *apkSource) (*Package, error) {
	sum, err := hashReaderAt(src.Reader, src.Size)
	if err != nil {
		return nil, err
	}
	pkg := &Package{
		Path:       src.Display,
		FileSHA256: sum,
		Size:       src.Size,
	}
	zr, err := zip.NewReader(src.Reader, src.Size)
	if err != nil {
		return nil, fmt.Errorf("not a zip/apk: %w", err)
	}

	pkg.Signing = parseSigning(src.Reader, src.Size, zr)
	pkg.NativeLibraries, pkg.Architectures = listNativeLibs(src.Display, zr)

	mf, err := findManifest(zr)
	if err != nil {
		apkLog().Warn("android manifest missing or unreadable", "path", src.Display, "err", err)
		return pkg, nil
	}
	info, err := parseManifestAXML(mf)
	if err != nil {
		apkLog().Warn("android manifest parse failed", "path", src.Display, "err", err)
		return pkg, nil
	}
	pkg.ManifestOK = true
	pkg.PackageName = info.PackageName
	pkg.VersionName = info.VersionName
	pkg.VersionCode = info.VersionCode
	pkg.SplitName = info.SplitName
	pkg.ApplicationLabel = info.ApplicationLabel
	pkg.Debuggable = info.Debuggable
	pkg.NativeCode = info.NativeCode
	pkg.UsesFeatures = info.UsesFeatures
	pkg.LauncherActivity = info.LauncherActivity
	pkg.GameActivities = info.GameActivities
	pkg.IsSplit = info.SplitName != ""
	apkLog().Debug("inspected apk", "path", src.Display, "package", pkg.PackageName, "version", pkg.VersionName)
	return pkg, nil
}

func findManifest(zr *zip.Reader) ([]byte, error) {
	for _, f := range zr.File {
		if filepath.ToSlash(f.Name) == "AndroidManifest.xml" {
			return readZipEntry(f)
		}
	}
	return nil, fmt.Errorf("AndroidManifest.xml not found")
}

func listNativeLibs(apkPath string, zr *zip.Reader) ([]NativeLib, []string) {
	abis := map[string]struct{}{}
	var libs []NativeLib
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(f.Name)
		abi, base, ok := nativeLibPath(name)
		if !ok {
			continue
		}
		sum, size, err := hashZipEntry(f)
		if err != nil {
			apkLog().Warn("native lib hash failed", "apk", apkPath, "zip", name, "err", err)
			continue
		}
		abis[abi] = struct{}{}
		libs = append(libs, NativeLib{
			APKPath: apkPath,
			ZIPPath: name,
			ABI:     abi,
			Name:    base,
			Size:    size,
			SHA256:  sum,
		})
	}
	sort.Slice(libs, func(i, j int) bool {
		if libs[i].ZIPPath != libs[j].ZIPPath {
			return libs[i].ZIPPath < libs[j].ZIPPath
		}
		return libs[i].APKPath < libs[j].APKPath
	})
	return libs, sortABIs(keys(abis))
}

func nativeLibPath(zipPath string) (abi, name string, ok bool) {
	parts := strings.Split(zipPath, "/")
	if len(parts) != 3 || parts[0] != "lib" {
		return "", "", false
	}
	if !strings.HasSuffix(parts[2], ".so") {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func hashZipEntry(f *zip.File) (string, int64, error) {
	rc, err := f.Open()
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func hashReaderAt(ra io.ReaderAt, size int64) (string, error) {
	h := sha256.New()
	_, err := io.Copy(h, io.NewSectionReader(ra, 0, size))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func collectSources(ctx context.Context, paths []string) ([]*apkSource, error) {
	var out []*apkSource
	seen := map[string]struct{}{}
	add := func(s *apkSource) {
		key := s.Display
		if _, ok := seen[key]; ok {
			_ = s.Close()
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			found, err := collectDir(p)
			if err != nil {
				return nil, err
			}
			for _, s := range found {
				add(s)
			}
			continue
		}
		srcs, err := collectFile(p)
		if err != nil {
			return nil, err
		}
		for _, s := range srcs {
			add(s)
		}
	}
	return out, nil
}

func collectDir(dir string) ([]*apkSource, error) {
	var out []*apkSource
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, relErr := filepath.Rel(dir, path)
			if relErr == nil && rel != "." && strings.Count(rel, string(os.PathSeparator)) >= 3 {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".apk", ".apkm", ".xapk", ".zip":
			srcs, err := collectFile(path)
			if err != nil {
				apkLog().Debug("skip unreadable package in directory", "path", path, "err", err)
				return nil
			}
			out = append(out, srcs...)
		}
		return nil
	})
	return out, err
}

func collectFile(path string) ([]*apkSource, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".apkm", ".xapk":
		return openNestedContainer(path, true)
	case ".zip":
		return openNestedContainer(path, false)
	default:
		src, err := openFileSource(path)
		if err != nil {
			return nil, err
		}
		return []*apkSource{src}, nil
	}
}

func openFileSource(path string) (*apkSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &apkSource{Display: path, Reader: f, Size: st.Size(), closer: f}, nil
}

func openSource(apkPath string) (*apkSource, error) {
	if i := strings.Index(apkPath, nestedSep); i > 0 {
		outer, inner := apkPath[:i], apkPath[i+1:]
		if _, err := os.Stat(outer); err == nil {
			return openNestedEntry(outer, inner)
		}
	}
	return openFileSource(apkPath)
}

func openNestedContainer(path string, requireAPK bool) ([]*apkSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, fmt.Errorf("apk: open %s: %w", path, err)
	}
	var apkEntries []string
	hasManifest := false
	for _, zf := range zr.File {
		name := filepath.ToSlash(zf.Name)
		if strings.HasSuffix(strings.ToLower(name), ".apk") && !zf.FileInfo().IsDir() {
			apkEntries = append(apkEntries, name)
		}
		if name == "AndroidManifest.xml" {
			hasManifest = true
		}
	}
	if len(apkEntries) == 0 {
		if requireAPK {
			return nil, fmt.Errorf("apk: %s contains no nested APKs", path)
		}
		if hasManifest {
			src, err := openFileSource(path)
			if err != nil {
				return nil, err
			}
			return []*apkSource{src}, nil
		}
		return nil, fmt.Errorf("apk: %s is not an APK or APK container", path)
	}
	sort.Strings(apkEntries)
	var out []*apkSource
	for _, name := range apkEntries {
		src, err := openNestedEntry(path, name)
		if err != nil {
			for _, s := range out {
				_ = s.Close()
			}
			return nil, err
		}
		out = append(out, src)
	}
	return out, nil
}

func openNestedEntry(container, inner string) (*apkSource, error) {
	f, err := os.Open(container)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, err
	}
	inner = filepath.ToSlash(inner)
	for _, zf := range zr.File {
		if filepath.ToSlash(zf.Name) != inner {
			continue
		}
		data, err := readZipEntry(zf)
		if err != nil {
			return nil, fmt.Errorf("apk: read %s from %s: %w", inner, container, err)
		}
		display := container + nestedSep + inner
		return &apkSource{
			Display: display,
			Reader:  bytes.NewReader(data),
			Size:    int64(len(data)),
			owned:   data,
		}, nil
	}
	return nil, fmt.Errorf("apk: %s not in %s", inner, container)
}

func mergePackages(pkgs []Package) *Merged {
	if len(pkgs) == 0 {
		return nil
	}
	base := pickBase(pkgs)
	m := &Merged{
		PackageName:      base.PackageName,
		VersionName:      base.VersionName,
		VersionCode:      base.VersionCode,
		ApplicationLabel: base.ApplicationLabel,
		Debuggable:       base.Debuggable,
	}
	abiSet := map[string]struct{}{}
	ncSet := map[string]struct{}{}
	libSeen := map[string]struct{}{}
	certSeen := map[string]struct{}{}
	subjSeen := map[string]struct{}{}
	for _, p := range pkgs {
		if p.PackageName != "" && m.PackageName == "" {
			m.PackageName = p.PackageName
		}
		if p.VersionName != "" && m.VersionName == "" {
			m.VersionName = p.VersionName
		}
		if p.VersionCode != 0 && m.VersionCode == 0 {
			m.VersionCode = p.VersionCode
		}
		if p.ApplicationLabel != "" && m.ApplicationLabel == "" {
			m.ApplicationLabel = p.ApplicationLabel
		}
		if p.Debuggable {
			m.Debuggable = true
		}
		if p.SplitName != "" {
			m.SplitNames = appendUnique(m.SplitNames, p.SplitName)
		} else if !p.IsSplit {
			m.SplitNames = appendUnique(m.SplitNames, "")
		}
		for _, a := range p.Architectures {
			abiSet[a] = struct{}{}
		}
		for _, n := range p.NativeCode {
			ncSet[n] = struct{}{}
		}
		for _, lib := range p.NativeLibraries {
			key := lib.ZIPPath
			if _, ok := libSeen[key]; ok {
				continue
			}
			libSeen[key] = struct{}{}
			m.NativeLibraries = append(m.NativeLibraries, lib)
		}
		m.Signing.HasV1 = m.Signing.HasV1 || p.Signing.HasV1
		m.Signing.HasV2 = m.Signing.HasV2 || p.Signing.HasV2
		m.Signing.HasV3 = m.Signing.HasV3 || p.Signing.HasV3
		m.Signing.HasV3_1 = m.Signing.HasV3_1 || p.Signing.HasV3_1
		if p.Signing.ParseError != "" && m.Signing.ParseError == "" {
			m.Signing.ParseError = p.Signing.ParseError
		}
		for i, h := range p.Signing.CertSHA256 {
			if _, ok := certSeen[h]; ok {
				continue
			}
			certSeen[h] = struct{}{}
			m.Signing.CertSHA256 = append(m.Signing.CertSHA256, h)
			if i < len(p.Signing.Subjects) {
				s := p.Signing.Subjects[i]
				if _, ok := subjSeen[s]; !ok {
					subjSeen[s] = struct{}{}
					m.Signing.Subjects = append(m.Signing.Subjects, s)
				}
			}
		}
	}
	m.Architectures = sortABIs(keys(abiSet))
	m.NativeCode = uniqueSorted(keys(ncSet))
	sort.Slice(m.NativeLibraries, func(i, j int) bool {
		return m.NativeLibraries[i].ZIPPath < m.NativeLibraries[j].ZIPPath
	})
	return m
}

func pickBase(pkgs []Package) Package {
	for _, p := range pkgs {
		if !p.IsSplit && p.SplitName == "" && p.ManifestOK {
			return p
		}
	}
	for _, p := range pkgs {
		if p.ManifestOK {
			return p
		}
	}
	return pkgs[0]
}

func sortPackages(pkgs []Package) {
	sort.Slice(pkgs, func(i, j int) bool {
		si, sj := pkgs[i].IsSplit, pkgs[j].IsSplit
		if si != sj {
			return !si && sj
		}
		if pkgs[i].SplitName != pkgs[j].SplitName {
			return pkgs[i].SplitName < pkgs[j].SplitName
		}
		return pkgs[i].Path < pkgs[j].Path
	})
}

func abiFromSplit(split string) string {
	split = strings.TrimSpace(split)
	if split == "" {
		return ""
	}
	s := strings.TrimPrefix(split, "config.")
	s = strings.TrimPrefix(s, "split_config.")
	if _, ok := knownABISet[s]; ok {
		return s
	}
	if _, ok := knownABISet[split]; ok {
		return split
	}
	return ""
}

func sortABIs(abis []string) []string {
	rank := make(map[string]int, len(knownABIs))
	for i, a := range knownABIs {
		rank[a] = i
	}
	sort.Slice(abis, func(i, j int) bool {
		ri, oi := rank[abis[i]]
		rj, oj := rank[abis[j]]
		if oi && oj {
			return ri < rj
		}
		if oi != oj {
			return oi
		}
		return abis[i] < abis[j]
	})
	return abis
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func uniqueSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := make([]string, 0, len(in))
	var last string
	for i, s := range in {
		if s == "" {
			continue
		}
		if i == 0 || s != last {
			out = append(out, s)
			last = s
		}
	}
	return out
}

func appendUnique(dst []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(dst))
	for _, s := range dst {
		seen[s] = struct{}{}
	}
	for _, s := range extra {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		dst = append(dst, s)
	}
	return dst
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
