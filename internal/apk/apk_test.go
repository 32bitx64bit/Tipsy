package apk

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAXMLRoundTripUTF16AndUTF8(t *testing.T) {
	root := sampleManifest("com.example.tipsy", "1.2.3", 42, "", true)
	for _, utf8 := range []bool{false, true} {
		data, err := encodeAXML(root, utf8)
		if err != nil {
			t.Fatalf("encode utf8=%v: %v", utf8, err)
		}
		got, err := parseManifestAXML(data)
		if err != nil {
			t.Fatalf("parse utf8=%v: %v", utf8, err)
		}
		if got.PackageName != "com.example.tipsy" {
			t.Errorf("utf8=%v package=%q", utf8, got.PackageName)
		}
		if got.VersionName != "1.2.3" || got.VersionCode != 42 {
			t.Errorf("utf8=%v version=%q/%d", utf8, got.VersionName, got.VersionCode)
		}
		if got.ApplicationLabel != "TipsyTest" {
			t.Errorf("utf8=%v label=%q", utf8, got.ApplicationLabel)
		}
		if !got.Debuggable {
			t.Errorf("utf8=%v debuggable=false", utf8)
		}
		if got.LauncherActivity != "com.example.tipsy.GameActivity" {
			t.Errorf("utf8=%v launcher=%q", utf8, got.LauncherActivity)
		}
		if len(got.GameActivities) != 1 {
			t.Errorf("utf8=%v game activities=%v", utf8, got.GameActivities)
		}
		if len(got.UsesFeatures) != 1 || got.UsesFeatures[0] != "android.hardware.touchscreen" {
			t.Errorf("utf8=%v features=%v", utf8, got.UsesFeatures)
		}
	}
}

func TestAXMLCorruptDoesNotPanic(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{1, 2, 3},
		{0x03, 0x00, 0x08, 0x00, 0x08, 0x00, 0x00, 0x00},
		bytes.Repeat([]byte{0xff}, 64),
	}
	root, err := encodeAXML(sampleManifest("com.example.tipsy", "1", 1, "", false), false)
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, root[:len(root)/2])
	for i, c := range cases {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("case %d panicked: %v", i, rec)
				}
			}()
			_, _ = parseAXML(c)
			_, _ = parseManifestAXML(c)
		}()
	}
}

func TestInspectSynthetic(t *testing.T) {
	dir := t.TempDir()
	dummy := []byte("dummy-native-lib")
	apkPath := writeAPK(t, dir, "base.apk", inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", dummy))

	rep, err := Inspect(context.Background(), []string{apkPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Packages) != 1 {
		t.Fatalf("packages=%d", len(rep.Packages))
	}
	p := rep.Packages[0]
	if !p.ManifestOK {
		t.Fatal("manifest not ok")
	}
	if p.PackageName != "com.example.tipsy" || p.VersionName != "1.2.3" || p.VersionCode != 42 {
		t.Fatalf("identity %+v", p)
	}
	if p.IsSplit {
		t.Fatal("base reported as split")
	}
	if len(p.Architectures) != 1 || p.Architectures[0] != "x86_64" {
		t.Fatalf("abis=%v", p.Architectures)
	}
	if len(p.NativeLibraries) != 1 {
		t.Fatalf("natives=%d", len(p.NativeLibraries))
	}
	lib := p.NativeLibraries[0]
	if lib.ZIPPath != "lib/x86_64/libdummy.so" || lib.Name != "libdummy.so" || lib.ABI != "x86_64" {
		t.Fatalf("lib %+v", lib)
	}
	if lib.SHA256 != sha256Hex(dummy) {
		t.Fatalf("lib sha %s want %s", lib.SHA256, sha256Hex(dummy))
	}
	if p.FileSHA256 == "" || p.Size == 0 {
		t.Fatal("missing file hash/size")
	}
	if p.LauncherActivity != "com.example.tipsy.GameActivity" {
		t.Fatalf("launcher %q", p.LauncherActivity)
	}
	if rep.Merged == nil || rep.Merged.PackageName != p.PackageName {
		t.Fatalf("merged %+v", rep.Merged)
	}

	text := FormatText(rep)
	if !strings.Contains(text, "com.example.tipsy") || !strings.Contains(text, "libdummy.so") {
		t.Fatalf("text report missing fields:\n%s", text)
	}
	raw, err := FormatJSON(rep)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Packages[0].PackageName != "com.example.tipsy" {
		t.Fatalf("json decode %+v", decoded.Packages[0])
	}

	got, err := ReadZipFile(apkPath, "lib/x86_64/libdummy.so")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dummy) {
		t.Fatalf("ReadZipFile mismatch")
	}
}

func TestInspectSplits(t *testing.T) {
	dir := t.TempDir()
	base := writeAPK(t, dir, "base.apk", inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", nil))
	splitFiles := inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "config.x86_64", []byte("split-lib"))
	split := writeAPK(t, dir, "split_config.x86_64.apk", splitFiles)

	rep, err := Inspect(context.Background(), []string{base, split})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Packages) != 2 {
		t.Fatalf("packages=%d", len(rep.Packages))
	}
	if rep.Packages[0].IsSplit || rep.Packages[1].SplitName != "config.x86_64" {
		t.Fatalf("split detection %+v %+v", rep.Packages[0], rep.Packages[1])
	}
	if !rep.Packages[1].IsSplit {
		t.Fatal("split not marked")
	}
	if rep.Merged == nil {
		t.Fatal("nil merged")
	}
	if rep.Merged.PackageName != "com.example.tipsy" || rep.Merged.VersionCode != 42 {
		t.Fatalf("merged identity %+v", rep.Merged)
	}
	if len(rep.Merged.Architectures) != 1 || rep.Merged.Architectures[0] != "x86_64" {
		t.Fatalf("merged abis %v", rep.Merged.Architectures)
	}
	if len(rep.Merged.NativeLibraries) != 1 {
		t.Fatalf("merged natives %d", len(rep.Merged.NativeLibraries))
	}
	foundABI := false
	for _, n := range rep.Packages[1].NativeCode {
		if n == "x86_64" {
			foundABI = true
		}
	}
	if !foundABI {
		t.Fatalf("split nativeCode %v", rep.Packages[1].NativeCode)
	}

	dirRep, err := Inspect(context.Background(), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirRep.Packages) != 2 {
		t.Fatalf("dir packages=%d", len(dirRep.Packages))
	}
}

func TestInspectNestedZip(t *testing.T) {
	dir := t.TempDir()
	baseBytes := zipBytes(t, inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", []byte("base-lib")))
	splitBytes := zipBytes(t, inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "config.x86_64", []byte("split-lib")))
	container := filepath.Join(dir, "bundle.apkm")
	writeZipFile(t, container, map[string][]byte{
		"base.apk":                baseBytes,
		"split_config.x86_64.apk": splitBytes,
		"manifest.json":           []byte(`{"package_name":"com.example.tipsy"}`),
	})

	rep, err := Inspect(context.Background(), []string{container})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Packages) != 2 {
		t.Fatalf("nested packages=%d", len(rep.Packages))
	}
	if !strings.Contains(rep.Packages[0].Path, "!") && !strings.Contains(rep.Packages[1].Path, "!") {
		t.Fatalf("expected nested display paths, got %q %q", rep.Packages[0].Path, rep.Packages[1].Path)
	}
	if rep.Merged == nil || rep.Merged.PackageName != "com.example.tipsy" {
		t.Fatalf("merged %+v", rep.Merged)
	}

	var libPath, apkPath string
	for _, p := range rep.Packages {
		for _, lib := range p.NativeLibraries {
			if lib.Name == "libdummy.so" {
				apkPath = p.Path
				libPath = lib.ZIPPath
			}
		}
	}
	if apkPath == "" {
		t.Fatal("no native lib in nested apks")
	}
	data, err := ReadZipFile(apkPath, libPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty nested lib")
	}
}

func TestCompare(t *testing.T) {
	dir := t.TempDir()
	oldAPK := writeAPK(t, dir, "old.apk", inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", []byte("lib-v1")))
	newFiles := inspectFiles(t, "com.example.tipsy", "1.2.4", 43, "", []byte("lib-v2"))
	newFiles["lib/arm64-v8a/libdummy.so"] = []byte("arm-lib")
	newFiles["lib/x86_64/libnew.so"] = []byte("new-lib")
	delete(newFiles, "lib/x86_64/libdummy.so")
	newFiles["lib/x86_64/libdummy.so"] = []byte("lib-v2")
	newAPK := writeAPK(t, dir, "new.apk", newFiles)

	oldRep, err := Inspect(context.Background(), []string{oldAPK})
	if err != nil {
		t.Fatal(err)
	}
	newRep, err := Inspect(context.Background(), []string{newAPK})
	if err != nil {
		t.Fatal(err)
	}
	d := Compare(oldRep, newRep)
	if d.VersionNameOld != "1.2.3" || d.VersionNameNew != "1.2.4" {
		t.Fatalf("version names %+v", d)
	}
	if d.VersionCodeOld != 42 || d.VersionCodeNew != 43 {
		t.Fatalf("version codes %+v", d)
	}
	if !contains(d.ArchitecturesAdded, "arm64-v8a") {
		t.Fatalf("abis added %v", d.ArchitecturesAdded)
	}
	if !contains(d.NativesAdded, "lib/arm64-v8a/libdummy.so") || !contains(d.NativesAdded, "lib/x86_64/libnew.so") {
		t.Fatalf("natives added %v", d.NativesAdded)
	}
	if !contains(d.NativesChanged, "lib/x86_64/libdummy.so") {
		t.Fatalf("natives changed %v", d.NativesChanged)
	}
	text := FormatDiff(d)
	if !strings.Contains(text, "1.2.3") || !strings.Contains(text, "1.2.4") {
		t.Fatalf("diff text:\n%s", text)
	}
	raw, err := FormatDiffJSON(d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("1.2.4")) {
		t.Fatalf("diff json %s", raw)
	}

	same := Compare(oldRep, oldRep)
	if !same.empty() {
		t.Fatalf("identical reports differ: %+v", same)
	}
	if !strings.Contains(FormatDiff(same), "No package-level differences") {
		t.Fatalf("same diff text:\n%s", FormatDiff(same))
	}
}

func TestSigningV1AndV2(t *testing.T) {
	cert, der := testCert(t)
	pkcs7, err := encodePKCS7(der)
	if err != nil {
		t.Fatal(err)
	}
	files := inspectFiles(t, "com.example.tipsy", "1.0.0", 1, "", []byte("so"))
	files["META-INF/CERT.RSA"] = pkcs7
	zipped := zipBytes(t, files)
	wantHash := sha256Hex(der)

	dir := t.TempDir()
	v1Path := filepath.Join(dir, "v1.apk")
	if err := os.WriteFile(v1Path, zipped, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Inspect(context.Background(), []string{v1Path})
	if err != nil {
		t.Fatal(err)
	}
	sig := rep.Packages[0].Signing
	if !sig.HasV1 {
		t.Fatalf("v1 not detected: %+v", sig)
	}
	if !contains(sig.CertSHA256, wantHash) {
		t.Fatalf("v1 cert hash %v want %s parse=%s", sig.CertSHA256, wantHash, sig.ParseError)
	}
	if len(sig.Subjects) == 0 || !strings.Contains(sig.Subjects[0], "Tipsy Test") {
		t.Fatalf("subject %v (cert %s)", sig.Subjects, cert.Subject)
	}

	v2zip, err := insertAPKSigningBlock(zipped,
		sigPair{id: apkSigIDV2, value: v2SignerBlock(der)},
		sigPair{id: apkSigIDV3, value: v2SignerBlock(der)},
		sigPair{id: apkSigIDV31, value: v2SignerBlock(der)},
	)
	if err != nil {
		t.Fatal(err)
	}
	v2Path := filepath.Join(dir, "v2.apk")
	if err := os.WriteFile(v2Path, v2zip, 0o644); err != nil {
		t.Fatal(err)
	}
	rep2, err := Inspect(context.Background(), []string{v2Path})
	if err != nil {
		t.Fatal(err)
	}
	s2 := rep2.Packages[0].Signing
	if !s2.HasV1 || !s2.HasV2 || !s2.HasV3 || !s2.HasV3_1 {
		t.Fatalf("scheme flags %+v", s2)
	}
	if !contains(s2.CertSHA256, wantHash) {
		t.Fatalf("v2 certs %v parse=%s", s2.CertSHA256, s2.ParseError)
	}

	oldRep := rep
	newRep := rep2
	d := Compare(oldRep, newRep)
	// Same cert, so no cert hash change expected.
	if len(d.CertSHA256Added) != 0 || len(d.CertSHA256Removed) != 0 {
		t.Fatalf("cert diff %+v", d)
	}
}

func TestSigningParseErrorDoesNotCrash(t *testing.T) {
	files := inspectFiles(t, "com.example.tipsy", "1.0.0", 1, "", []byte("so"))
	files["META-INF/CERT.RSA"] = []byte("not-a-certificate")
	dir := t.TempDir()
	path := writeAPK(t, dir, "bad-sign.apk", files)
	rep, err := Inspect(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Packages[0].Signing.HasV1 {
		t.Fatal("expected v1 flag from .RSA name")
	}
	if rep.Packages[0].Signing.ParseError == "" {
		t.Fatal("expected parse error")
	}
}

func TestInspectContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Inspect(ctx, []string{t.TempDir()})
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestTestdataSyntheticAPK(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "apk")
	base := filepath.Join(root, "synthetic-base.apk")
	if _, err := os.Stat(base); err != nil {
		t.Skip("testdata fixture missing; run TestGenerateTestdata with TIPSY_WRITE_APK_FIXTURES=1")
	}
	rep, err := Inspect(context.Background(), []string{base})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Packages) != 1 || rep.Packages[0].PackageName != "com.example.tipsy" {
		t.Fatalf("testdata inspect %+v", rep)
	}
	bundle := filepath.Join(root, "synthetic-bundle.zip")
	if _, err := os.Stat(bundle); err == nil {
		brep, err := Inspect(context.Background(), []string{bundle})
		if err != nil {
			t.Fatal(err)
		}
		if len(brep.Packages) != 2 {
			t.Fatalf("bundle packages=%d", len(brep.Packages))
		}
	}
}

func TestInspectMissingPath(t *testing.T) {
	_, err := Inspect(context.Background(), []string{filepath.Join(t.TempDir(), "nope.apk")})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGenerateTestdata(t *testing.T) {
	if os.Getenv("TIPSY_WRITE_APK_FIXTURES") == "" {
		t.Skip("set TIPSY_WRITE_APK_FIXTURES=1 to refresh testdata/apk")
	}
	root := filepath.Join("..", "..", "testdata", "apk")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	base := zipBytes(t, inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "", []byte("dummy-native-lib")))
	split := zipBytes(t, inspectFiles(t, "com.example.tipsy", "1.2.3", 42, "config.x86_64", []byte("dummy-native-lib")))
	if err := os.WriteFile(filepath.Join(root, "synthetic-base.apk"), base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "synthetic-split_config.x86_64.apk"), split, 0o644); err != nil {
		t.Fatal(err)
	}
	bundle := zipBytes(t, map[string][]byte{
		"base.apk":                base,
		"split_config.x86_64.apk": split,
	})
	if err := os.WriteFile(filepath.Join(root, "synthetic-bundle.zip"), bundle, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompareCertChange(t *testing.T) {
	dir := t.TempDir()
	_, der1 := testCert(t)
	_, der2 := testCert(t)
	files1 := inspectFiles(t, "com.example.tipsy", "1.0.0", 1, "", []byte("so"))
	files1["META-INF/CERT.RSA"] = der1
	files2 := inspectFiles(t, "com.example.tipsy", "1.0.0", 1, "", []byte("so"))
	files2["META-INF/CERT.RSA"] = der2
	oldPath := writeAPK(t, dir, "old.apk", files1)
	newPath := writeAPK(t, dir, "new.apk", files2)
	oldRep, err := Inspect(context.Background(), []string{oldPath})
	if err != nil {
		t.Fatal(err)
	}
	newRep, err := Inspect(context.Background(), []string{newPath})
	if err != nil {
		t.Fatal(err)
	}
	d := Compare(oldRep, newRep)
	if len(d.CertSHA256Added) != 1 || len(d.CertSHA256Removed) != 1 {
		t.Fatalf("cert diff %+v", d)
	}
	if d.CertSHA256Added[0] == d.CertSHA256Removed[0] {
		t.Fatal("expected distinct cert hashes")
	}
}

func inspectFiles(t *testing.T, pkg, ver string, code int64, split string, lib []byte) map[string][]byte {
	t.Helper()
	man, err := encodeAXML(sampleManifest(pkg, ver, code, split, split == ""), false)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"AndroidManifest.xml": man,
	}
	if lib != nil {
		files["lib/x86_64/libdummy.so"] = lib
	}
	return files
}

func sampleManifest(pkg, ver string, code int64, split string, withApp bool) *xmlElem {
	attrs := []xmlAttr{
		{Name: "package", Value: pkg},
		{Name: "versionName", Android: true, Value: ver},
		{Name: "versionCode", Android: true, Value: itoa64(code)},
	}
	if split != "" {
		attrs = append(attrs, xmlAttr{Name: "split", Value: split})
	}
	root := &xmlElem{Name: "manifest", Attrs: attrs}
	if withApp {
		root.Children = []*xmlElem{
			{
				Name: "uses-feature",
				Attrs: []xmlAttr{
					{Name: "name", Android: true, Value: "android.hardware.touchscreen"},
				},
			},
			{
				Name: "application",
				Attrs: []xmlAttr{
					{Name: "label", Android: true, Value: "TipsyTest"},
					{Name: "debuggable", Android: true, Value: "true"},
				},
				Children: []*xmlElem{
					{
						Name: "activity",
						Attrs: []xmlAttr{
							{Name: "name", Android: true, Value: "com.example.tipsy.GameActivity"},
						},
						Children: []*xmlElem{
							{
								Name: "intent-filter",
								Children: []*xmlElem{
									{Name: "action", Attrs: []xmlAttr{{Name: "name", Android: true, Value: "android.intent.action.MAIN"}}},
									{Name: "category", Attrs: []xmlAttr{{Name: "name", Android: true, Value: "android.intent.category.LAUNCHER"}}},
								},
							},
						},
					},
				},
			},
		}
	}
	return root
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}

func writeAPK(t *testing.T, dir, name string, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, zipBytes(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func zipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	fixed := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	for _, n := range names {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate, Modified: fixed}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(files[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeZipFile(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	if err := os.WriteFile(path, zipBytes(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func testCert(t *testing.T) (*x509.Certificate, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Tipsy Test", Organization: []string{"Tipsy"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, der
}
