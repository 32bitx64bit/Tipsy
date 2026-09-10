// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Official Android ExtraContent / rbxasset fetch. Same host and query the
// engine hardcodes (https://assetdelivery.roblox.com/v1/asset/?id=).
// FFlag DebugOTAForceSkipContentProvider is True on the public CDN, so the
// engine will not download these itself via ContentProvider; place them on
// disk under content / ExtraContent / android before V2Start.
const (
	officialAssetDeliveryHost = "https://assetdelivery.roblox.com"
	officialAssetDeliveryPath = "/v1/asset/"
	maxOfficialPatchBytes     = 64 << 20
	officialPatchHTTPTimeout  = 90 * time.Second
)

var (
	assetHTTPClient  = http.DefaultClient
	assetDeliveryURL = func(assetID, version string) string {
		q := url.Values{"id": {assetID}}
		if version != "" {
			q.Set("version", version)
		}
		return officialAssetDeliveryHost + officialAssetDeliveryPath + "?" + q.Encode()
	}
)

type otaPatchConfig struct {
	AssetID        string `json:"AssetId"`
	AssetVersion   string `json:"AssetVersion"`
	LocalAssetURI  string `json:"LocalAssetURI"`
	LocalAssetHash string `json:"LocalAssetHash"`
}

// EnsureOfficialPatches fetches missing rbxasset ExtraContent named by
// extracted *PatchConfig JSON (AssetId + LocalAssetURI) from Roblox's
// official assetdelivery endpoint and writes it under assets/content,
// assets/ExtraContent, and assets/android. Does not invent files; hash
// must match LocalAssetHash when the JSON provides one. Never logs bodies.
func EnsureOfficialPatches(ctx context.Context, assetsDir string) error {
	if strings.TrimSpace(assetsDir) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfgDir := filepath.Join(assetsDir, "content", "configs")
	st, err := os.Stat(cfgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !st.IsDir() {
		return nil
	}

	var first error
	errWalk := filepath.WalkDir(cfgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		if err := ensurePatchFromConfig(ctx, assetsDir, path); err != nil {
			if first == nil {
				first = err
			}
			logging.Logger(logging.CatFilesystem).Info("official patch", "config", filepath.Base(filepath.Dir(path)), "err", err)
		}
		return nil
	})
	if errWalk != nil {
		return errWalk
	}
	return first
}

func ensurePatchFromConfig(ctx context.Context, assetsDir, cfgPath string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg otaPatchConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	cfg.AssetID = strings.TrimSpace(cfg.AssetID)
	cfg.AssetVersion = strings.TrimSpace(cfg.AssetVersion)
	cfg.LocalAssetURI = strings.TrimSpace(cfg.LocalAssetURI)
	cfg.LocalAssetHash = strings.ToLower(strings.TrimSpace(cfg.LocalAssetHash))
	if cfg.AssetID == "" || cfg.LocalAssetURI == "" {
		return nil
	}
	rel, err := rbxassetRelPath(cfg.LocalAssetURI)
	if err != nil {
		return err
	}
	dests, err := officialPatchDests(assetsDir, rel)
	if err != nil {
		return err
	}
	if cacheName, err := otaRbxmCacheName(cfg.AssetID, cfg.AssetVersion); err == nil {
		dests = append(dests, otaRbxmCacheDests(assetsDir, cacheName)...)
	}
	// Bundled ExtraContent (UniversalApp, InExperience) is already on disk
	// and may not match LocalAssetHash (that hash is for a specific OTA
	// version). Do not refetch or overwrite a present rbxm.
	if have := existingOfficialPatch(dests); have != "" {
		return placeOfficialPatchCopies(have, dests)
	}

	body, err := fetchOfficialAsset(ctx, cfg.AssetID, cfg.AssetVersion)
	if err != nil {
		return err
	}
	if cfg.LocalAssetHash != "" {
		sum := md5.Sum(body)
		got := hex.EncodeToString(sum[:])
		if got != cfg.LocalAssetHash {
			return fmt.Errorf("official patch md5 %s want %s (asset %s v%s)", got, cfg.LocalAssetHash, cfg.AssetID, cfg.AssetVersion)
		}
	}
	if !bytes.HasPrefix(body, []byte("<roblox")) {
		return fmt.Errorf("official patch is not an rbxm (asset %s)", cfg.AssetID)
	}

	primary := dests[0]
	if err := writeOfficialPatchFile(primary, body); err != nil {
		return err
	}
	logging.Logger(logging.CatFilesystem).Info("official patch ready",
		"config", filepath.Base(filepath.Dir(cfgPath)),
		"asset", cfg.AssetID,
		"version", cfg.AssetVersion,
		"path", "content/"+rel,
	)
	return placeOfficialPatchCopies(primary, dests)
}

func rbxassetRelPath(uri string) (string, error) {
	s := strings.TrimSpace(uri)
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.Index(s, "://"); i >= 0 {
		scheme := strings.ToLower(s[:i])
		if scheme != "rbxasset" {
			return "", fmt.Errorf("unsupported asset scheme %q", scheme)
		}
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimPrefix(s, "content/")
	if s == "" || strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("invalid LocalAssetURI")
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("invalid LocalAssetURI")
		}
	}
	return s, nil
}

func officialPatchDests(assetsDir, rel string) ([]string, error) {
	roots := []string{"content", "ExtraContent", "android"}
	out := make([]string, 0, len(roots)+2)
	for _, root := range roots {
		dest, err := safeJoinUnder(filepath.Join(assetsDir, root), rel)
		if err != nil {
			return nil, err
		}
		out = append(out, dest)
	}
	return out, nil
}

// RbxmFileManager 0x24d25fd → 0x56e51e6 joins named folder
// ota_rbxm_decompressed_cache (0x4f2113) with
// DataModelPatch_{AssetId}_{AssetVersion}_cache (0x44ccd8).
const (
	otaRbxmCacheFolder = "ota_rbxm_decompressed_cache"
	otaRbxmCachePrefix = "DataModelPatch_"
	otaRbxmCacheSuffix = "_cache"
)

func otaRbxmCacheName(assetID, version string) (string, error) {
	if !decimalID(assetID) || !decimalID(version) {
		return "", fmt.Errorf("invalid OTA cache id")
	}
	return otaRbxmCachePrefix + assetID + "_" + version + otaRbxmCacheSuffix, nil
}

func decimalID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func otaRbxmCacheDests(assetsDir, name string) []string {
	var out []string
	for _, root := range otaRbxmCacheRoots(assetsDir) {
		dest, err := safeJoinUnder(root, name)
		if err != nil {
			continue
		}
		out = append(out, dest)
	}
	return out
}

func otaRbxmCacheRoots(assetsDir string) []string {
	// Launch: assetsDir is runtime/assets. Named app-storage folders
	// (OTAPatchBackups, ota_rbxm_decompressed_cache) live under the stable
	// Android FilesDir returned to the official client via 0x234168a. They are
	// intentionally separate from replaceable APK/runtime artifacts.
	if filepath.Base(assetsDir) == "assets" {
		storage := AppStorage()
		appData := filepath.Join(storage.FilesDir, "appData")
		cache := storage.CacheDir
		return []string{
			filepath.Join(appData, otaRbxmCacheFolder),
			filepath.Join(cache, otaRbxmCacheFolder),
		}
	}
	return []string{filepath.Join(assetsDir, otaRbxmCacheFolder)}
}

func safeJoinUnder(root, rel string) (string, error) {
	dest := filepath.Join(root, filepath.FromSlash(rel))
	relToRoot, err := filepath.Rel(root, dest)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid patch path")
	}
	return dest, nil
}

func officialPatchFileReady(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var hdr [8]byte
	n, err := f.Read(hdr[:])
	return err == nil && n >= 7 && bytes.Equal(hdr[:7], []byte("<roblox"))
}

func existingOfficialPatch(dests []string) string {
	for _, p := range dests {
		if officialPatchFileReady(p) {
			return p
		}
	}
	return ""
}

func placeOfficialPatchCopies(src string, dests []string) error {
	for _, dest := range dests {
		if dest == src || officialPatchFileReady(dest) || sameOfficialPatchFile(src, dest) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		_ = os.Remove(dest)
		if err := os.Link(src, dest); err == nil {
			if err := os.Chmod(dest, 0o600); err != nil {
				return err
			}
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := writeOfficialPatchFile(dest, b); err != nil {
			return err
		}
	}
	return nil
}

func sameOfficialPatchFile(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

func writeOfficialPatchFile(dest string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fetchOfficialAsset(ctx context.Context, assetID, version string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, officialPatchHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetDeliveryURL(assetID, version), nil)
	if err != nil {
		return nil, err
	}
	// The licensed UA is version-independent, so do not read meta.json from
	// RuntimeDir just to feed an ignored parameter.
	req.Header.Set("User-Agent", robloxUserAgent(""))
	req.Header.Set("Accept", "*/*")
	resp, err := assetHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("assetdelivery HTTP %d (asset %s)", resp.StatusCode, assetID)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOfficialPatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxOfficialPatchBytes {
		return nil, fmt.Errorf("official patch exceeds %d bytes", maxOfficialPatchBytes)
	}
	body, err = maybeGunzipPatch(body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func maybeGunzipPatch(body []byte) ([]byte, error) {
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		return body, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxOfficialPatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maxOfficialPatchBytes {
		return nil, fmt.Errorf("official patch exceeds %d bytes", maxOfficialPatchBytes)
	}
	return out, nil
}
