package apk

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractBaseLibAssetsAndMeta(t *testing.T) {
	dir := t.TempDir()
	dummy := []byte("dummy-native-lib")
	files := inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", dummy)
	files["assets/www/index.html"] = []byte("<html>ok</html>")
	files["assets/foo.txt"] = []byte("asset-body")
	files["lib/arm64-v8a/libdummy.so"] = []byte("arm-lib")
	apkPath := writeAPK(t, dir, "base.apk", files)

	dest := filepath.Join(dir, "runtime")
	res, err := Extract(context.Background(), []string{apkPath}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.DestDir != dest {
		t.Fatalf("DestDir=%s want %s", res.DestDir, dest)
	}
	if res.Report == nil || res.Report.Merged == nil || res.Report.Merged.PackageName != "com.example.tipsy" {
		t.Fatalf("report %+v", res.Report)
	}
	if len(res.Libraries) != 1 {
		t.Fatalf("libraries=%d %v", len(res.Libraries), res.Libraries)
	}
	libPath := filepath.Join(dest, "lib", "x86_64", "libdummy.so")
	if res.Libraries[0] != libPath {
		t.Fatalf("lib path %s want %s", res.Libraries[0], libPath)
	}
	got, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dummy) {
		t.Fatalf("lib bytes mismatch")
	}
	sum, err := hashFile(libPath)
	if err != nil {
		t.Fatal(err)
	}
	wantSum := sha256Hex(dummy)
	if sum != wantSum {
		t.Fatalf("lib sha %s want %s", sum, wantSum)
	}
	matched := false
	for _, lib := range res.Report.Packages[0].NativeLibraries {
		if lib.ABI == "x86_64" && lib.Name == "libdummy.so" {
			if lib.SHA256 != sum {
				t.Fatalf("extracted lib hash %s inspect %s", sum, lib.SHA256)
			}
			matched = true
		}
	}
	if !matched {
		t.Fatal("inspect report missing x86_64 libdummy.so")
	}
	if _, err := os.Stat(filepath.Join(dest, "lib", "arm64-v8a")); err == nil {
		t.Fatal("arm64 library directory should not be extracted")
	}

	if res.AssetsDir != filepath.Join(dest, "assets") {
		t.Fatalf("AssetsDir=%s", res.AssetsDir)
	}
	html, err := os.ReadFile(filepath.Join(res.AssetsDir, "www", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(html) != "<html>ok</html>" {
		t.Fatalf("asset html %q", html)
	}
	foo, err := os.ReadFile(filepath.Join(res.AssetsDir, "foo.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(foo) != "asset-body" {
		t.Fatalf("asset foo %q", foo)
	}

	if res.BaseAPK != filepath.Join(dest, "apk", "base.apk") {
		t.Fatalf("BaseAPK=%s", res.BaseAPK)
	}
	if _, err := os.Stat(res.BaseAPK); err != nil {
		t.Fatal(err)
	}
	if res.MetaPath != filepath.Join(dest, "meta.json") {
		t.Fatalf("MetaPath=%s", res.MetaPath)
	}

	raw, err := os.ReadFile(res.MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.PackageName != "com.example.tipsy" || meta.VersionName != "1.2.3" || meta.VersionCode != 42 {
		t.Fatalf("meta identity %+v", meta)
	}
	if len(meta.Packages) != 1 || meta.Packages[0].SHA256 != res.Report.Packages[0].FileSHA256 {
		t.Fatalf("meta packages %+v", meta.Packages)
	}
	if len(meta.Libraries) != 1 || meta.Libraries[0].SHA256 != wantSum || meta.Libraries[0].Name != "libdummy.so" {
		t.Fatalf("meta libraries %+v", meta.Libraries)
	}
}

func TestExtractSplitsCopiesAPKsAndSplitLib(t *testing.T) {
	dir := t.TempDir()
	baseFiles := inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", nil)
	baseFiles["assets/config.json"] = []byte(`{"ok":true}`)
	base := writeAPK(t, dir, "base.apk", baseFiles)
	splitLib := []byte("split-lib")
	split := writeAPK(t, dir, "split_config.x86_64.apk", inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "config.x86_64", splitLib))

	dest := filepath.Join(dir, "out")
	res, err := Extract(context.Background(), []string{base, split}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Libraries) != 1 {
		t.Fatalf("libraries=%d", len(res.Libraries))
	}
	got, err := os.ReadFile(res.Libraries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, splitLib) {
		t.Fatal("split lib not extracted")
	}
	if hash, _ := hashFile(res.Libraries[0]); hash != sha256Hex(splitLib) {
		t.Fatal("split lib hash mismatch")
	}
	if _, err := os.Stat(filepath.Join(dest, "apk", "base.apk")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "apk", "split_config.x86_64.apk")); err != nil {
		t.Fatal(err)
	}
	if res.BaseAPK != filepath.Join(dest, "apk", "base.apk") {
		t.Fatalf("BaseAPK=%s", res.BaseAPK)
	}
	cfg, err := os.ReadFile(filepath.Join(res.AssetsDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg) != `{"ok":true}` {
		t.Fatalf("asset %q", cfg)
	}

	raw, err := os.ReadFile(res.MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Packages) != 2 {
		t.Fatalf("meta packages=%d", len(meta.Packages))
	}
	for i, p := range res.Report.Packages {
		if meta.Packages[i].SHA256 != p.FileSHA256 {
			t.Fatalf("package hash %s want %s", meta.Packages[i].SHA256, p.FileSHA256)
		}
	}
}

func TestExtractNestedAPKM(t *testing.T) {
	dir := t.TempDir()
	baseFiles := inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", nil)
	baseFiles["assets/nested.txt"] = []byte("from-base")
	baseBytes := zipBytes(t, baseFiles)
	splitLib := []byte("nested-split-lib")
	splitBytes := zipBytes(t, inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "config.x86_64", splitLib))
	container := filepath.Join(dir, "bundle.apkm")
	writeZipFile(t, container, map[string][]byte{
		"base.apk":                baseBytes,
		"split_config.x86_64.apk": splitBytes,
	})

	dest := filepath.Join(dir, "runtime")
	res, err := Extract(context.Background(), []string{container}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Libraries) != 1 {
		t.Fatalf("libraries=%d", len(res.Libraries))
	}
	got, err := os.ReadFile(res.Libraries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, splitLib) {
		t.Fatal("nested split lib not extracted")
	}
	if _, err := os.Stat(filepath.Join(dest, "apk", "base.apk")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "apk", "split_config.x86_64.apk")); err != nil {
		t.Fatal(err)
	}
	nested, err := os.ReadFile(filepath.Join(res.AssetsDir, "nested.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(nested) != "from-base" {
		t.Fatalf("nested asset %q", nested)
	}
	if !strings.Contains(res.Report.Packages[0].Path, "!") {
		t.Fatalf("expected nested inspect paths, got %q", res.Report.Packages[0].Path)
	}
}

func TestExtractDirOfAPKs(t *testing.T) {
	dir := t.TempDir()
	writeAPK(t, dir, "base.apk", inspectFiles(t, "com.example.tipsy", "1.0.0", 1, "", []byte("dir-lib")))
	dest := filepath.Join(t.TempDir(), "out")
	res, err := Extract(context.Background(), []string{dir}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Libraries) != 1 {
		t.Fatalf("libraries=%d", len(res.Libraries))
	}
}

func TestExtractIntegrityMatchesInspectReport(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("integrity-payload")
	apkPath := writeAPK(t, dir, "base.apk", inspectFiles(t, "com.example.tipsy", "1.0.0", 7, "", payload))
	rep, err := Inspect(context.Background(), []string{apkPath})
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, lib := range rep.Merged.NativeLibraries {
		if lib.ABI == "x86_64" && lib.Name == "libdummy.so" {
			want = lib.SHA256
		}
	}
	if want == "" {
		t.Fatal("inspect missing x86_64 lib")
	}
	res, err := Extract(context.Background(), []string{apkPath}, filepath.Join(dir, "dest"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(res.Libraries[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("extracted %s inspect %s", got, want)
	}
}

func TestExtractEmptyDest(t *testing.T) {
	_, err := Extract(context.Background(), []string{"x.apk"}, "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractNoPaths(t *testing.T) {
	_, err := Extract(context.Background(), nil, t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Extract(ctx, []string{t.TempDir()}, t.TempDir())
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestExtractRejectsZipSlipAssets(t *testing.T) {
	dir := t.TempDir()
	files := inspectFiles(t, "com.example.tipsy", "1.0.0", 1, "", []byte("so"))
	files["assets/../../evil.txt"] = []byte("nope")
	apkPath := writeAPK(t, dir, "base.apk", files)
	_, err := Extract(context.Background(), []string{apkPath}, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected zip-slip error")
	}
}

func TestSafeRelFile(t *testing.T) {
	root := t.TempDir()
	ok, err := safeRelFile(root, "www/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ok, root) {
		t.Fatalf("dest %s not under %s", ok, root)
	}
	for _, bad := range []string{"../x", "/abs", "a/../b", ""} {
		if _, err := safeRelFile(root, bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
