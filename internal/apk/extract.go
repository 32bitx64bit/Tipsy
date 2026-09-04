package apk

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	abiX86_64   = "x86_64"
	metaFile    = "meta.json"
	apkSubdir   = "apk"
	libSubdir   = "lib"
	assetSubdir = "assets"
)

// ExtractResult is the layout written by Extract.
type ExtractResult struct {
	DestDir   string   `json:"destDir"`
	Report    *Report  `json:"report,omitempty"`
	Libraries []string `json:"libraries,omitempty"`
	AssetsDir string   `json:"assetsDir,omitempty"`
	BaseAPK   string   `json:"baseApk,omitempty"`
	MetaPath  string   `json:"metaPath,omitempty"`
}

// Meta is destDir/meta.json: package identity and Inspect file hashes.
type Meta struct {
	PackageName string     `json:"packageName,omitempty"`
	VersionName string     `json:"versionName,omitempty"`
	VersionCode int64      `json:"versionCode,omitempty"`
	Packages    []MetaFile `json:"packages"`
	Libraries   []MetaLib  `json:"libraries,omitempty"`
}

// MetaFile is one copied APK (base or split) and its Inspect hash.
type MetaFile struct {
	Source    string `json:"source"`
	Dest      string `json:"dest"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	SplitName string `json:"splitName,omitempty"`
}

// MetaLib is one extracted x86-64 .so and its Inspect hash.
type MetaLib struct {
	Name    string `json:"name"`
	ZIPPath string `json:"zipPath"`
	Dest    string `json:"dest"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size,omitempty"`
}

// Extract copies official x86-64 libraries, base assets, and APKs into destDir.
// Extracted .so SHA-256 must match the Inspect report.
func Extract(ctx context.Context, paths []string, destDir string) (*ExtractResult, error) {
	if strings.TrimSpace(destDir) == "" {
		return nil, fmt.Errorf("apk: extract destination is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(destDir)
	if err != nil {
		return nil, fmt.Errorf("apk: extract dest: %w", err)
	}
	destDir = abs

	rep, err := Inspect(ctx, paths)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("apk: mkdir %s: %w", destDir, err)
	}

	pkgMeta, baseAPK, err := copyAPKs(ctx, destDir, rep)
	if err != nil {
		return nil, err
	}

	libs, libMeta, err := extractX86_64Libs(ctx, destDir, x86_64Libs(rep))
	if err != nil {
		return nil, err
	}

	assetsDir, err := extractAssets(ctx, destDir, pickBase(rep.Packages))
	if err != nil {
		return nil, err
	}

	meta := Meta{
		Packages:  pkgMeta,
		Libraries: libMeta,
	}
	if m := rep.Merged; m != nil {
		meta.PackageName = m.PackageName
		meta.VersionName = m.VersionName
		meta.VersionCode = m.VersionCode
	}

	metaPath := filepath.Join(destDir, metaFile)
	if err := writeMeta(metaPath, &meta); err != nil {
		return nil, err
	}

	apkLog().Debug("extracted apk",
		"dest", destDir,
		"package", meta.PackageName,
		"version", meta.VersionName,
		"x86_64_libs", len(libs),
	)

	return &ExtractResult{
		DestDir:   destDir,
		Report:    rep,
		Libraries: libs,
		AssetsDir: assetsDir,
		BaseAPK:   baseAPK,
		MetaPath:  metaPath,
	}, nil
}

func x86_64Libs(rep *Report) []NativeLib {
	var src []NativeLib
	if rep.Merged != nil {
		src = rep.Merged.NativeLibraries
	} else {
		for _, p := range rep.Packages {
			src = append(src, p.NativeLibraries...)
		}
	}
	var out []NativeLib
	for _, lib := range src {
		if lib.ABI == abiX86_64 {
			out = append(out, lib)
		}
	}
	return out
}

func copyAPKs(ctx context.Context, destDir string, rep *Report) ([]MetaFile, string, error) {
	apkDir := filepath.Join(destDir, apkSubdir)
	if err := os.RemoveAll(apkDir); err != nil {
		return nil, "", fmt.Errorf("apk: clear %s: %w", apkDir, err)
	}
	if err := os.MkdirAll(apkDir, 0o755); err != nil {
		return nil, "", err
	}

	base := pickBase(rep.Packages)
	used := map[string]int{}
	var meta []MetaFile
	var baseAPK string

	for _, pkg := range rep.Packages {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		name := uniqueAPKName(apkDestName(pkg.Path), used)
		dest := filepath.Join(apkDir, name)
		if err := copyPackageAPK(pkg.Path, dest); err != nil {
			return nil, "", err
		}
		sum, err := hashFile(dest)
		if err != nil {
			return nil, "", err
		}
		if sum != pkg.FileSHA256 {
			return nil, "", fmt.Errorf("apk: integrity mismatch copying %s: extracted %s, inspect %s", pkg.Path, sum, pkg.FileSHA256)
		}
		rel, err := filepath.Rel(destDir, dest)
		if err != nil {
			rel = filepath.Join(apkSubdir, name)
		}
		meta = append(meta, MetaFile{
			Source:    pkg.Path,
			Dest:      filepath.ToSlash(rel),
			SHA256:    sum,
			Size:      pkg.Size,
			SplitName: pkg.SplitName,
		})
		if pkg.Path == base.Path {
			baseAPK = dest
		}
	}
	if baseAPK == "" && len(rep.Packages) > 0 {
		baseAPK = filepath.Join(apkDir, filepath.Base(meta[0].Dest))
	}
	return meta, baseAPK, nil
}

func extractX86_64Libs(ctx context.Context, destDir string, libs []NativeLib) ([]string, []MetaLib, error) {
	libDir := filepath.Join(destDir, libSubdir, abiX86_64)
	if err := os.RemoveAll(libDir); err != nil {
		return nil, nil, fmt.Errorf("apk: clear %s: %w", libDir, err)
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return nil, nil, err
	}

	seen := map[string]string{}
	var paths []string
	var meta []MetaLib

	for _, lib := range libs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := filepath.Base(lib.Name)
		if name == "" || name != lib.Name || name == "." || name == ".." {
			return nil, nil, fmt.Errorf("apk: invalid native library name %q", lib.Name)
		}
		if prev, ok := seen[name]; ok {
			if prev != lib.SHA256 {
				return nil, nil, fmt.Errorf("apk: conflicting hashes for %s", name)
			}
			continue
		}
		data, err := ReadZipFile(lib.APKPath, lib.ZIPPath)
		if err != nil {
			return nil, nil, fmt.Errorf("apk: extract %s: %w", lib.ZIPPath, err)
		}
		dest := filepath.Join(libDir, name)
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return nil, nil, err
		}
		sum, err := hashFile(dest)
		if err != nil {
			return nil, nil, err
		}
		if sum != lib.SHA256 {
			return nil, nil, fmt.Errorf("apk: integrity mismatch for %s: extracted %s, inspect %s", name, sum, lib.SHA256)
		}
		seen[name] = sum
		paths = append(paths, dest)
		rel, err := filepath.Rel(destDir, dest)
		if err != nil {
			rel = filepath.Join(libSubdir, abiX86_64, name)
		}
		meta = append(meta, MetaLib{
			Name:    name,
			ZIPPath: lib.ZIPPath,
			Dest:    filepath.ToSlash(rel),
			SHA256:  sum,
			Size:    lib.Size,
		})
	}
	sort.Strings(paths)
	sort.Slice(meta, func(i, j int) bool { return meta[i].Name < meta[j].Name })
	return paths, meta, nil
}

func extractAssets(ctx context.Context, destDir string, base Package) (string, error) {
	if base.Path == "" {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	src, err := openSource(base.Path)
	if err != nil {
		return "", fmt.Errorf("apk: open base %s: %w", base.Path, err)
	}
	defer src.Close()
	zr, err := zip.NewReader(src.Reader, src.Size)
	if err != nil {
		return "", fmt.Errorf("apk: zip %s: %w", base.Path, err)
	}

	assetsDest := filepath.Join(destDir, assetSubdir)
	if err := os.RemoveAll(assetsDest); err != nil {
		return "", fmt.Errorf("apk: clear %s: %w", assetsDest, err)
	}

	extracted := false
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := filepath.ToSlash(f.Name)
		if !strings.HasPrefix(name, "assets/") {
			continue
		}
		rel := strings.TrimPrefix(name, "assets/")
		if rel == "" || strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
			continue
		}
		dest, err := safeRelFile(assetsDest, rel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := writeZipFileTo(f, dest); err != nil {
			return "", fmt.Errorf("apk: extract %s: %w", name, err)
		}
		extracted = true
	}
	if !extracted {
		return "", nil
	}
	return assetsDest, nil
}

func writeZipFileTo(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyPackageAPK(apkPath, dest string) error {
	src, err := openSource(apkPath)
	if err != nil {
		return fmt.Errorf("apk: open %s: %w", apkPath, err)
	}
	defer src.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.NewSectionReader(src.Reader, 0, src.Size))
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("apk: copy %s: %w", apkPath, copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func apkDestName(display string) string {
	if i := strings.LastIndex(display, nestedSep); i >= 0 {
		display = display[i+1:]
	}
	name := filepath.Base(filepath.FromSlash(display))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "package.apk"
	}
	return name
}

func uniqueAPKName(base string, used map[string]int) string {
	n := used[base]
	used[base] = n + 1
	if n == 0 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s-%d%s", stem, n, ext)
}

func writeMeta(path string, m *Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("apk: write %s: %w", path, err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func safeRelFile(root, rel string) (string, error) {
	rel = filepath.ToSlash(rel)
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("apk: invalid asset path %q", rel)
	}
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("apk: invalid asset path %q", rel)
		}
	}
	dest := filepath.Join(root, filepath.FromSlash(rel))
	relToRoot, err := filepath.Rel(root, dest)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("apk: invalid asset path %q", rel)
	}
	return dest, nil
}
