// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"archive/zip"
	"container/list"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Asset loading has three deliberately separate phases: an immutable source
// selection, cache lookup/publication under assetCache.mu, and filesystem/ZIP
// I/O without that mutex. A source is retired by replacing it with a new
// generation; active loads retain the old source until they finish, so its APK
// reader cannot be closed beneath zip.File.Open.
const (
	maxConcurrentAssetInflations = 2

	// assetCacheByteBudget is deliberately conservative: it bounds only
	// unborrowed, fully inactive cache blobs. A live native AAsset is never
	// evicted or unpinned to meet this target.
	assetCacheByteBudget = 64 << 20

	// Negative entries are cheap but attacker-controlled by requested names, so
	// use a fixed capacity and lifetime rather than per-entry timers or an
	// unbounded miss map.
	assetNegativeCacheLimit = 128
	assetNegativeCacheTTL   = time.Minute
)

type nameEntry struct {
	data              []byte
	err               error
	negativeExpiresAt time.Time
	negativeOrder     uint64
}

type apkArchive struct {
	zr    *zip.ReadCloser
	path  string
	files map[string]*zip.File
}

type assetLoad struct {
	done chan struct{}
	data []byte
	err  error

	// blobKey is held out of the inactive LRU while this one loader may be
	// publishing aliases. It replaces the former generation-wide eviction
	// deferral: unrelated inactive blobs remain eligible.
	blobKey uintptr
}

// assetBlob is one unique backing slice retained by an asset source
// generation. Name/path/ZIP maps may all reference it, but bytes are counted
// once. It deliberately carries no asset identity beyond the private map key.
type assetBlob struct {
	bytes int64

	// aliases make eviction proportional to this one blob rather than every
	// cache map. lru is non-nil only when no native lease or in-flight load
	// protects the blob, so closing the final native lease can select the LRU
	// front without a full-cache scan.
	aliases       map[assetBlobAlias]struct{}
	inFlight      uint32
	nativeHandles uint32
	lru           *list.Element
}

type assetBlobAliasKind uint8

const (
	assetBlobAliasName assetBlobAliasKind = iota
	assetBlobAliasPath
	assetBlobAliasZIP
)

type assetBlobAlias struct {
	kind assetBlobAliasKind
	key  string
}

type assetCachePolicy struct {
	byteBudget  int64
	negativeCap int
	negativeTTL time.Duration
}

type archiveOpen struct {
	done chan struct{}
	arch *apkArchive
	err  error
}

// assetCache is one immutable assetsDir/APK source generation. Its maps are
// protected by mu; the directory and APK fields never change after creation.
type assetCache struct {
	mu sync.RWMutex

	generation uint64
	assetsDir  string
	apkPath    string

	nameCache     map[string]nameEntry
	pathOK        map[string][]byte
	zipBlobCache  map[string][]byte
	blobs         map[uintptr]assetBlob
	loads         map[string]*assetLoad
	nextNegative  uint64
	negativeNames map[string]struct{}
	// nextNegativeExpiry skips negative-index maintenance until an entry could
	// expire. A stale earlier value is safe; it can only trigger an early scan
	// of the bounded negative index, never leave an expired result reusable.
	nextNegativeExpiry time.Time

	// All eviction/accounting fields below are maintained with each map or
	// lease transition. The normal final AAsset_close path therefore does not
	// walk the cache just to rediscover its byte total or oldest candidate.
	inactiveBlobs      list.List // uintptr keys, least-recently-used first
	positiveEntries    int
	negativeEntries    int
	negativeCandidates int
	negativeOther      int
	cachedBytes        int64
	borrowedCacheBytes int64

	apkArch    *apkArchive
	apkOpening *archiveOpen

	// An active load owns a source reference, including while it waits for an
	// inflation permit. Retiring a source closes its archive only at zero.
	activeLoads int
	inflating   int
	retired     bool

	inflateSlots      chan struct{}
	archiveCloseCount uint64
}

type assetPin struct {
	pinner        *runtime.Pinner
	bytes         int64
	activeHandles uint64
	cache         *assetCache
	cacheKey      uintptr
	tracksCache   bool
}

// assetCacheSnapshot is intentionally content-free. It is package-private so
// deterministic tests can prove cache/lifetime states without exposing asset
// names, paths, data, or pointers to runtime diagnostics.
type assetCacheSnapshot struct {
	Generation          uint64
	NameEntries         int
	PositiveEntries     int
	NegativeEntries     int
	NegativeCandidates  int
	NegativeOther       int
	PathEntries         int
	CachedBytes         int64
	BlobCount           int
	BorrowedCacheBytes  int64
	EvictableCacheBytes int64
	LiveWorkingSetBytes int64
	ZipEntries          int
	InFlightLoads       int
	Inflating           int
	Retired             bool
	ArchiveOpen         bool
	ArchiveCloseCount   uint64
	ActiveHandles       uint64
	PinnedBlobs         int
	PinnedBytes         int64
	CacheByteBudget     int64
	NegativeCacheLimit  int
}

var (
	assetsMu        sync.Mutex
	assetGeneration uint64 = 1
	currentAssets   atomic.Pointer[assetCache]

	assetPinsMu    sync.RWMutex
	assetPins      = make(map[uintptr]assetPin)
	assetPinTotals struct {
		activeHandles uint64
		pinnedBlobs   int
		pinnedBytes   int64
	}
	emptyAsset byte

	assetCachePolicyState = struct {
		sync.RWMutex
		policy assetCachePolicy
	}{policy: assetCachePolicy{
		byteBudget:  assetCacheByteBudget,
		negativeCap: assetNegativeCacheLimit,
		negativeTTL: assetNegativeCacheTTL,
	}}

	assetTestHook struct {
		sync.RWMutex
		beforeZipRead func(string) error
		now           func() time.Time
	}
)

func init() {
	currentAssets.Store(newAssetCache(assetGeneration, "", ""))
}

func newAssetCache(generation uint64, dir, apk string) *assetCache {
	return &assetCache{
		generation:    generation,
		assetsDir:     dir,
		apkPath:       apk,
		nameCache:     make(map[string]nameEntry),
		pathOK:        make(map[string][]byte),
		zipBlobCache:  make(map[string][]byte),
		blobs:         make(map[uintptr]assetBlob),
		loads:         make(map[string]*assetLoad),
		negativeNames: make(map[string]struct{}),
		inflateSlots:  make(chan struct{}, maxConcurrentAssetInflations),
	}
}

func currentAssetCachePolicy() assetCachePolicy {
	assetCachePolicyState.RLock()
	policy := assetCachePolicyState.policy
	assetCachePolicyState.RUnlock()
	return policy
}

func normalizeAssetCachePolicy(policy assetCachePolicy) assetCachePolicy {
	if policy.byteBudget < 0 {
		policy.byteBudget = 0
	}
	if policy.negativeCap < 0 {
		policy.negativeCap = 0
	}
	if policy.negativeTTL < 0 {
		policy.negativeTTL = 0
	}
	return policy
}

// setAssetCachePolicyForTest changes only the private cache policy. It is a
// deterministic seam for ownership tests; production keeps the constants
// above and has no configuration surface for it.
func setAssetCachePolicyForTest(policy assetCachePolicy) (restore func()) {
	assetCachePolicyState.Lock()
	previous := assetCachePolicyState.policy
	assetCachePolicyState.policy = normalizeAssetCachePolicy(policy)
	assetCachePolicyState.Unlock()
	return func() {
		assetCachePolicyState.Lock()
		assetCachePolicyState.policy = previous
		assetCachePolicyState.Unlock()
	}
}

func assetCacheNow() time.Time {
	assetTestHook.RLock()
	now := assetTestHook.now
	assetTestHook.RUnlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

// setAssetCacheNowForTest supplies a deterministic clock for the bounded
// negative-cache policy. It is deliberately a single shared seam, not a
// timer per miss or a background cleanup goroutine.
func setAssetCacheNowForTest(now func() time.Time) (restore func()) {
	assetTestHook.Lock()
	previous := assetTestHook.now
	assetTestHook.now = now
	assetTestHook.Unlock()
	return func() {
		assetTestHook.Lock()
		assetTestHook.now = previous
		assetTestHook.Unlock()
	}
}

// setAssetsLocked preserves its established caller-facing name. It publishes a
// fresh immutable source generation before retiring the old one. It never
// waits for a stalled cold read, and the retired generation owns its archive
// until every in-flight load releases its source reference.
func setAssetsLocked(dir, apk string) {
	assetsMu.Lock()
	previous := currentAssets.Load()
	if previous.assetsDir == dir && previous.apkPath == apk {
		assetsMu.Unlock()
		return
	}
	assetGeneration++
	currentAssets.Store(newAssetCache(assetGeneration, dir, apk))
	assetsMu.Unlock()
	previous.retire()
}

func assetsForOpen() *assetCache {
	return currentAssets.Load()
}

func (c *assetCache) retire() {
	c.mu.Lock()
	c.retired = true
	archive := c.dropRetiredCachesLocked()
	c.mu.Unlock()
	c.closeArchive(archive)
}

// dropRetiredCachesLocked returns an archive that is safe to close. The caller
// must hold c.mu. Native pins retain heap slices or directory mappings
// independently, while this source generation remains alive only until its
// in-flight reads finish; an AAsset close cannot extend an APK reader's
// source lifetime.
func (c *assetCache) dropRetiredCachesLocked() *zip.ReadCloser {
	if !c.retired || c.activeLoads != 0 {
		return nil
	}
	clear(c.nameCache)
	clear(c.pathOK)
	clear(c.zipBlobCache)
	for key := range c.blobs {
		releaseMappedDirAssetFromCache(key)
	}
	clear(c.blobs)
	clear(c.negativeNames)
	c.inactiveBlobs = list.List{}
	c.positiveEntries = 0
	c.negativeEntries = 0
	c.negativeCandidates = 0
	c.negativeOther = 0
	c.cachedBytes = 0
	c.borrowedCacheBytes = 0
	c.nextNegativeExpiry = time.Time{}
	if c.apkArch == nil {
		return nil
	}
	archive := c.apkArch.zr
	c.apkArch = nil
	return archive
}

func (c *assetCache) closeArchive(archive *zip.ReadCloser) {
	if archive == nil {
		return
	}
	_ = archive.Close()
	c.mu.Lock()
	c.archiveCloseCount++
	c.mu.Unlock()
}

func dirCandidates(dir, name string) []string {
	rel := filepath.FromSlash(name)
	candidates := []string{filepath.Join(dir, rel)}
	// rbxasset://configs/... is under content/; AAsset may open either
	// "configs/..." or "content/..." depending on AssetReader root.
	if name != "" && name != "content" && !strings.HasPrefix(name, "content/") {
		candidates = append(candidates, filepath.Join(dir, "content", rel))
	}
	if name != "" && !strings.HasPrefix(name, "ExtraContent/") {
		candidates = append(candidates, filepath.Join(dir, "ExtraContent", rel))
	}
	if name != "" && !strings.HasPrefix(name, "android/") {
		candidates = append(candidates, filepath.Join(dir, "android", rel))
	}
	return candidates
}

func apkWants(name string) []string {
	wants := []string{"assets/" + name}
	if name != "" && name != "content" && !strings.HasPrefix(name, "content/") {
		wants = append(wants, "assets/content/"+name)
	}
	return wants
}

func (a *apkArchive) lookup(want string) *zip.File {
	if a == nil {
		return nil
	}
	if f := a.files[want]; f != nil {
		return f
	}
	if trimmed := strings.TrimPrefix(want, "/"); trimmed != want {
		if f := a.files[trimmed]; f != nil {
			return f
		}
	}
	return a.files["/"+want]
}

func (c *assetCache) cachedName(name string) (nameEntry, bool) {
	c.mu.Lock()
	entry, ok := c.cachedNameLocked(name, assetCacheNow())
	c.mu.Unlock()
	return entry, ok
}

func (c *assetCache) cachedNameLocked(name string, now time.Time) (nameEntry, bool) {
	entry, ok := c.nameCache[name]
	if !ok {
		return nameEntry{}, false
	}
	if errors.Is(entry.err, os.ErrNotExist) && !entry.negativeExpiresAt.After(now) {
		c.deleteNameLocked(name)
		return nameEntry{}, false
	}
	if entry.err == nil {
		c.touchBlobLocked(entry.data)
	}
	return entry, true
}

// beginLoad either returns a cached value, joins the one loader for name, or
// establishes the source reference for a new loader. The retry result means a
// caller raced a source-generation change and must select the current source.
func (c *assetCache) beginLoad(name string) (entry nameEntry, cached bool, load *assetLoad, leader bool, retry bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return nameEntry{}, false, nil, false, true
	}
	if entry, cached = c.cachedNameLocked(name, assetCacheNow()); cached {
		return entry, true, nil, false, false
	}
	if load = c.loads[name]; load != nil {
		return nameEntry{}, false, load, false, false
	}
	load = &assetLoad{done: make(chan struct{})}
	c.loads[name] = load
	c.activeLoads++
	return nameEntry{}, false, load, true, false
}

func (c *assetCache) finishLoad(name string, load *assetLoad, data []byte, err error) {
	c.mu.Lock()
	if !c.retired {
		if _, cached := c.nameCache[name]; !cached {
			switch {
			case err == nil:
				c.protectLoadBlobLocked(load, data)
				c.setNamePositiveLocked(name, data)
			case errors.Is(err, os.ErrNotExist):
				c.cacheNegativeLocked(name, err, assetCacheNow())
			}
		}
	}
	delete(c.loads, name)
	load.data = data
	load.err = err
	close(load.done)
	c.activeLoads--
	c.releaseLoadBlobLocked(load)
	if !c.retired {
		c.enforceAssetCachePolicyLocked(assetCacheNow())
	}
	archive := c.dropRetiredCachesLocked()
	c.mu.Unlock()
	c.closeArchive(archive)
}

func (c *assetCache) cacheNegativeLocked(name string, err error, now time.Time) {
	policy := currentAssetCachePolicy()
	if policy.negativeCap == 0 || policy.negativeTTL == 0 {
		return
	}
	c.deleteNameLocked(name)
	c.nextNegative++
	expiresAt := now.Add(policy.negativeTTL)
	c.nameCache[name] = nameEntry{
		err:               err,
		negativeExpiresAt: expiresAt,
		negativeOrder:     c.nextNegative,
	}
	c.negativeNames[name] = struct{}{}
	if c.nextNegativeExpiry.IsZero() || expiresAt.Before(c.nextNegativeExpiry) {
		c.nextNegativeExpiry = expiresAt
	}
	c.negativeEntries++
	if errors.Is(err, os.ErrNotExist) {
		c.negativeCandidates++
	} else {
		c.negativeOther++
	}
}

func (c *assetCache) pruneExpiredNegativeLocked(now time.Time) {
	if len(c.negativeNames) == 0 || c.nextNegativeExpiry.After(now) {
		return
	}

	var next time.Time
	for name := range c.negativeNames {
		entry, found := c.nameCache[name]
		if !found || !errors.Is(entry.err, os.ErrNotExist) {
			// The normal mutation helpers prevent this. Repair defensively so a
			// malformed private cache cannot keep maintenance permanently armed.
			delete(c.negativeNames, name)
			continue
		}
		if !entry.negativeExpiresAt.After(now) {
			c.deleteNameLocked(name)
			continue
		}
		if next.IsZero() || entry.negativeExpiresAt.Before(next) {
			next = entry.negativeExpiresAt
		}
	}
	c.nextNegativeExpiry = next
}

func (c *assetCache) enforceAssetCachePolicyLocked(now time.Time) {
	policy := currentAssetCachePolicy()
	c.pruneExpiredNegativeLocked(now)
	for c.negativeEntryCountLocked() > policy.negativeCap {
		name, found := c.oldestNegativeLocked()
		if !found {
			break
		}
		c.deleteNameLocked(name)
	}

	if c.retired {
		return
	}
	for c.cachedBytes > policy.byteBudget {
		key, found := c.oldestInactiveBlobLocked()
		if !found {
			return
		}
		c.removeBlobAliasesLocked(key)
	}
}

func (c *assetCache) negativeEntryCountLocked() int {
	return len(c.negativeNames)
}

func (c *assetCache) oldestNegativeLocked() (string, bool) {
	var (
		oldestName  string
		oldestOrder uint64
		found       bool
	)
	for name := range c.negativeNames {
		entry, ok := c.nameCache[name]
		if !ok || !errors.Is(entry.err, os.ErrNotExist) {
			continue
		}
		if !found || entry.negativeOrder < oldestOrder || (entry.negativeOrder == oldestOrder && name < oldestName) {
			oldestName = name
			oldestOrder = entry.negativeOrder
			found = true
		}
	}
	return oldestName, found
}

func (c *assetCache) cachedBytesLocked() int64 {
	return c.cachedBytes
}

func (c *assetCache) oldestInactiveBlobLocked() (uintptr, bool) {
	node := c.inactiveBlobs.Front()
	if node == nil {
		return 0, false
	}
	return node.Value.(uintptr), true
}

// removeBlobAliasesLocked removes exactly this blob's tracked aliases while
// holding c.mu. It never unpins the slice: native AAsset ownership belongs
// solely to the close-token release path.
func (c *assetCache) removeBlobAliasesLocked(key uintptr) {
	blob, found := c.blobs[key]
	if !found {
		return
	}
	for alias := range blob.aliases {
		switch alias.kind {
		case assetBlobAliasName:
			if entry, ok := c.nameCache[alias.key]; ok && entry.err == nil && assetBlobKey(entry.data) == key {
				delete(c.nameCache, alias.key)
				c.positiveEntries--
			}
		case assetBlobAliasPath:
			if data, ok := c.pathOK[alias.key]; ok && assetBlobKey(data) == key {
				delete(c.pathOK, alias.key)
			}
		case assetBlobAliasZIP:
			if data, ok := c.zipBlobCache[alias.key]; ok && assetBlobKey(data) == key {
				delete(c.zipBlobCache, alias.key)
			}
		}
	}
	c.removeBlobLocked(key, blob)
}

func (c *assetCache) deleteNameLocked(name string) {
	entry, found := c.nameCache[name]
	if !found {
		return
	}
	delete(c.nameCache, name)
	if entry.err == nil {
		c.positiveEntries--
		c.unlinkBlobAliasLocked(assetBlobKey(entry.data), assetBlobAlias{kind: assetBlobAliasName, key: name})
		return
	}
	c.negativeEntries--
	if errors.Is(entry.err, os.ErrNotExist) {
		delete(c.negativeNames, name)
		c.negativeCandidates--
	} else {
		c.negativeOther--
	}
	if len(c.negativeNames) == 0 {
		c.nextNegativeExpiry = time.Time{}
	}
}

func (c *assetCache) setNamePositiveLocked(name string, data []byte) {
	if old, found := c.nameCache[name]; found && old.err == nil && assetBlobKey(old.data) == assetBlobKey(data) {
		c.touchBlobLocked(data)
		return
	}
	c.deleteNameLocked(name)
	c.nameCache[name] = nameEntry{data: data}
	c.positiveEntries++
	c.linkBlobAliasLocked(data, assetBlobAlias{kind: assetBlobAliasName, key: name})
}

func (c *assetCache) setPathLocked(path string, data []byte) []byte {
	if existing, found := c.pathOK[path]; found {
		return existing
	}
	c.pathOK[path] = data
	c.linkBlobAliasLocked(data, assetBlobAlias{kind: assetBlobAliasPath, key: path})
	return data
}

func (c *assetCache) setZIPLocked(name string, data []byte) []byte {
	if existing, found := c.zipBlobCache[name]; found {
		return existing
	}
	c.zipBlobCache[name] = data
	c.linkBlobAliasLocked(data, assetBlobAlias{kind: assetBlobAliasZIP, key: name})
	return data
}

func (c *assetCache) linkBlobAliasLocked(data []byte, alias assetBlobAlias) {
	key := c.recordBlobLocked(data)
	if key == 0 {
		return
	}
	blob := c.blobs[key]
	blob.aliases[alias] = struct{}{}
	c.blobs[key] = blob
}

func (c *assetCache) unlinkBlobAliasLocked(key uintptr, alias assetBlobAlias) {
	if key == 0 {
		return
	}
	blob, found := c.blobs[key]
	if !found {
		return
	}
	delete(blob.aliases, alias)
	if len(blob.aliases) == 0 {
		c.removeBlobLocked(key, blob)
		return
	}
	c.blobs[key] = blob
}

func (c *assetCache) removeBlobLocked(key uintptr, blob assetBlob) {
	if blob.lru != nil {
		c.inactiveBlobs.Remove(blob.lru)
	}
	c.cachedBytes -= blob.bytes
	if blob.nativeHandles != 0 {
		c.borrowedCacheBytes -= blob.bytes
	}
	delete(c.blobs, key)
	// Native AAsset pins keep the mapping alive even after this generation
	// drops aliases. LRU eviction of an inactive mmap blob Munmaps here.
	releaseMappedDirAssetFromCache(key)
}

func (c *assetCache) makeBlobEvictableLocked(key uintptr) {
	blob, found := c.blobs[key]
	if !found || blob.inFlight != 0 || blob.nativeHandles != 0 || blob.lru != nil {
		return
	}
	blob.lru = c.inactiveBlobs.PushBack(key)
	c.blobs[key] = blob
}

func (c *assetCache) protectLoadBlobLocked(load *assetLoad, data []byte) {
	if load == nil || load.blobKey != 0 {
		return
	}
	key := c.recordBlobLocked(data)
	if key == 0 {
		return
	}
	blob := c.blobs[key]
	if blob.lru != nil {
		c.inactiveBlobs.Remove(blob.lru)
		blob.lru = nil
	}
	blob.inFlight++
	c.blobs[key] = blob
	load.blobKey = key
}

func (c *assetCache) releaseLoadBlobLocked(load *assetLoad) {
	if load == nil || load.blobKey == 0 {
		return
	}
	key := load.blobKey
	load.blobKey = 0
	blob, found := c.blobs[key]
	if !found || blob.inFlight == 0 {
		return
	}
	blob.inFlight--
	c.blobs[key] = blob
	c.makeBlobEvictableLocked(key)
}

func (c *assetCache) ensureAPK() (*apkArchive, error) {
	c.mu.Lock()
	if c.apkArch != nil {
		archive := c.apkArch
		c.mu.Unlock()
		return archive, nil
	}
	if opening := c.apkOpening; opening != nil {
		c.mu.Unlock()
		<-opening.done
		return opening.arch, opening.err
	}
	opening := &archiveOpen{done: make(chan struct{})}
	c.apkOpening = opening
	apkPath := c.apkPath
	c.mu.Unlock()

	// Opening and indexing are cold filesystem work. Do neither while holding
	// the cache mutex; a warmed name-cache hit remains available meanwhile.
	zr, err := zip.OpenReader(apkPath)
	var archive *apkArchive
	if err == nil {
		files := make(map[string]*zip.File, len(zr.File)*2)
		for _, f := range zr.File {
			if _, ok := files[f.Name]; !ok {
				files[f.Name] = f
			}
			if trimmed := strings.TrimPrefix(f.Name, "/"); trimmed != f.Name {
				if _, ok := files[trimmed]; !ok {
					files[trimmed] = f
				}
			}
		}
		archive = &apkArchive{zr: zr, path: apkPath, files: files}
	}

	c.mu.Lock()
	if archive != nil {
		c.apkArch = archive
	}
	opening.arch = archive
	opening.err = err
	c.apkOpening = nil
	close(opening.done)
	c.mu.Unlock()
	return archive, err
}

func (c *assetCache) cachedPath(path string) (data []byte, found bool) {
	c.mu.Lock()
	data, found = c.pathOK[path]
	if found {
		c.touchBlobLocked(data)
	}
	c.mu.Unlock()
	return data, found
}

func (c *assetCache) publishPath(path string, data []byte, load *assetLoad) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.pathOK[path]; ok {
		c.protectLoadBlobLocked(load, existing)
		if assetBlobKey(existing) != assetBlobKey(data) {
			discardMappedDirAsset(data)
		}
		return existing
	}
	if !c.retired {
		c.protectLoadBlobLocked(load, data)
		return c.setPathLocked(path, data)
	}
	return data
}

func (c *assetCache) cacheSuccessAliases(requested, usedRelSlash string, data []byte, load *assetLoad) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return
	}
	c.protectLoadBlobLocked(load, data)
	c.setNamePositiveLocked(requested, data)
	if usedRelSlash != "" && usedRelSlash != requested {
		if _, ok := c.nameCache[usedRelSlash]; !ok {
			c.setNamePositiveLocked(usedRelSlash, data)
		}
	}
}

func (c *assetCache) readDirAsset(name string, load *assetLoad) ([]byte, bool, error) {
	for _, path := range dirCandidates(c.assetsDir, name) {
		if data, found := c.cachedPath(path); found {
			rel, err := filepath.Rel(c.assetsDir, path)
			if err == nil {
				c.cacheSuccessAliases(name, filepath.ToSlash(rel), data, load)
			}
			return data, true, nil
		}

		// Directory I/O is intentionally outside every cache mutex. Map the
		// file instead of os.ReadFile so the measured Go inuse_space retain
		// is not a heap copy. ZIP inflate remains a separate heap path.
		data, err := mapDirAssetFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, false, err
			}
			continue
		}
		data = c.publishPath(path, data, load)
		rel, err := filepath.Rel(c.assetsDir, path)
		if err == nil {
			c.cacheSuccessAliases(name, filepath.ToSlash(rel), data, load)
		}
		return data, true, nil
	}
	return nil, false, nil
}

func (c *assetCache) cachedZip(name string) ([]byte, bool) {
	c.mu.Lock()
	data, ok := c.zipBlobCache[name]
	if ok {
		c.touchBlobLocked(data)
	}
	c.mu.Unlock()
	return data, ok
}

func (c *assetCache) inflateZip(f *zip.File, load *assetLoad) ([]byte, error) {
	if data, ok := c.cachedZip(f.Name); ok {
		c.mu.Lock()
		c.protectLoadBlobLocked(load, data)
		c.mu.Unlock()
		return data, nil
	}

	// Bound concurrent decompression, but wait without a cache lock. A stalled
	// cold key therefore cannot delay unrelated warmed hits.
	c.inflateSlots <- struct{}{}
	c.mu.Lock()
	c.inflating++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inflating--
		c.mu.Unlock()
		<-c.inflateSlots
	}()

	if err := beforeZipReadForTest(f.Name); err != nil {
		return nil, err
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	c.mu.Lock()
	if existing, ok := c.zipBlobCache[f.Name]; ok {
		c.protectLoadBlobLocked(load, existing)
		c.mu.Unlock()
		return existing, nil
	}
	if !c.retired {
		c.protectLoadBlobLocked(load, data)
		data = c.setZIPLocked(f.Name, data)
	}
	c.mu.Unlock()
	return data, nil
}

// recordBlobLocked records an exact unique backing allocation for this source
// generation. Empty data has no retained bytes and does not participate in a
// byte budget, so it has no blob record. The caller holds c.mu.
func (c *assetCache) recordBlobLocked(data []byte) uintptr {
	key := assetBlobKey(data)
	if key == 0 {
		return 0
	}
	if _, exists := c.blobs[key]; exists {
		c.touchBlobLocked(data)
		return key
	}
	blob := assetBlob{bytes: int64(len(data)), aliases: make(map[assetBlobAlias]struct{})}
	c.blobs[key] = blob
	c.cachedBytes += blob.bytes
	c.makeBlobEvictableLocked(key)
	adoptMappedDirAsset(key)
	return key
}

func assetBlobKey(data []byte) uintptr {
	if len(data) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(unsafe.SliceData(data)))
}

func (c *assetCache) touchBlobLocked(data []byte) {
	key := assetBlobKey(data)
	if key == 0 {
		return
	}
	blob, exists := c.blobs[key]
	if !exists {
		return
	}
	if blob.lru != nil {
		c.inactiveBlobs.MoveToBack(blob.lru)
	}
	c.blobs[key] = blob
}

func (c *assetCache) readAPKAsset(name string, load *assetLoad) ([]byte, error) {
	archive, err := c.ensureAPK()
	if err != nil {
		return nil, err
	}
	if archive == nil {
		return nil, os.ErrNotExist
	}
	for _, want := range apkWants(name) {
		f := archive.lookup(want)
		if f == nil {
			continue
		}
		data, err := c.inflateZip(f, load)
		if err != nil {
			return nil, err
		}
		alias := strings.TrimPrefix(f.Name, "/")
		alias = strings.TrimPrefix(alias, "assets/")
		c.cacheSuccessAliases(name, alias, data, load)
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (c *assetCache) loadAsset(name string, load *assetLoad) ([]byte, error) {
	if c.assetsDir != "" {
		if data, ok, err := c.readDirAsset(name, load); err != nil {
			return nil, err
		} else if ok {
			return data, nil
		}
	}
	if c.apkPath != "" {
		return c.readAPKAsset(name, load)
	}
	return nil, os.ErrNotExist
}

func openAssetBytes(name string) ([]byte, error) {
	name = strings.TrimPrefix(name, "/")
	for {
		cache := assetsForOpen()
		if entry, ok := cache.cachedName(name); ok {
			return entry.data, entry.err
		}
		entry, cached, load, leader, retry := cache.beginLoad(name)
		if retry {
			continue
		}
		if cached {
			return entry.data, entry.err
		}
		if !leader {
			<-load.done
			return load.data, load.err
		}
		data, err := cache.loadAsset(name, load)
		cache.finishLoad(name, load, data, err)
		return data, err
	}
}

// acquireAssetBorrow pins one blob and records one native AAsset handle before
// constructing that AAsset. Its release closure is consumed either by the real
// C close callback or synchronously by newBorrowedAsset when C allocation
// fails. No source cache lifecycle is allowed to infer that a handle closed.
func acquireAssetBorrow(data []byte) (unsafe.Pointer, int64, func()) {
	var pointer unsafe.Pointer
	if len(data) == 0 {
		pointer = unsafe.Pointer(&emptyAsset)
	} else {
		pointer = unsafe.Pointer(unsafe.SliceData(data))
	}
	key := uintptr(pointer)

	assetPinsMu.Lock()
	pin, exists := assetPins[key]
	first := !exists
	if !exists {
		pin.bytes = int64(len(data))
		if len(data) != 0 {
			// mmap pages are not Go objects; runtime.Pinner.Pin panics on
			// them. A live AAsset still holds mappedDirAsset.pins so eviction
			// cannot Munmap to meet the byte budget.
			if !retainMappedDirAssetPin(key) {
				pin.pinner = new(runtime.Pinner)
				pin.pinner.Pin(pointer)
				assetPinTotals.pinnedBlobs++
				assetPinTotals.pinnedBytes += pin.bytes
			}
		}
	}
	pin.activeHandles++
	assetPinTotals.activeHandles++
	assetPins[key] = pin
	assetPinsMu.Unlock()

	// Exactly one shared pin may protect one current-cache blob. Do this after
	// publishing the pin record so duplicate handles cannot each claim the
	// cache entry; no C close token exists until this function returns.
	if first {
		cache := assetsForOpen()
		if cache.beginNativeBorrow(key) {
			assetPinsMu.Lock()
			if current, ok := assetPins[key]; ok {
				current.cache = cache
				current.cacheKey = key
				current.tracksCache = true
				assetPins[key] = current
			}
			assetPinsMu.Unlock()
		}
	}

	return pointer, int64(len(data)), func() { releaseAssetBorrowedPin(key) }
}

func releaseAssetBorrowedPin(key uintptr) {
	assetPinsMu.Lock()
	pin, exists := assetPins[key]
	if !exists || pin.activeHandles == 0 {
		assetPinsMu.Unlock()
		return
	}
	pin.activeHandles--
	assetPinTotals.activeHandles--
	if pin.activeHandles != 0 {
		assetPins[key] = pin
		assetPinsMu.Unlock()
		return
	}
	delete(assetPins, key)
	pinner := pin.pinner
	if pinner != nil {
		assetPinTotals.pinnedBlobs--
		assetPinTotals.pinnedBytes -= pin.bytes
	}
	assetPinsMu.Unlock()

	if pinner != nil {
		pinner.Unpin()
	}
	// C has completed descriptor teardown and this was the final borrower. The
	// tracked cache entry returns directly to its inactive LRU; enforcement is
	// O(number evicted), not a scan over names, blobs, or global pins.
	if pin.tracksCache && pin.cache != nil {
		pin.cache.endNativeBorrow(pin.cacheKey)
	}
	releaseMappedDirAssetPin(key)
}

func (c *assetCache) beginNativeBorrow(key uintptr) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return false
	}
	blob, found := c.blobs[key]
	if !found {
		return false
	}
	if blob.nativeHandles == 0 {
		if blob.lru != nil {
			c.inactiveBlobs.Remove(blob.lru)
			blob.lru = nil
		}
		c.borrowedCacheBytes += blob.bytes
	}
	blob.nativeHandles++
	c.blobs[key] = blob
	return true
}

func (c *assetCache) endNativeBorrow(key uintptr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	blob, found := c.blobs[key]
	if !found || blob.nativeHandles == 0 {
		return
	}
	blob.nativeHandles--
	if blob.nativeHandles == 0 {
		c.borrowedCacheBytes -= blob.bytes
	}
	c.blobs[key] = blob
	c.makeBlobEvictableLocked(key)
	if !c.retired {
		c.enforceAssetCachePolicyLocked(assetCacheNow())
	}
}

func assetFromBytes(data []byte) unsafe.Pointer {
	buffer, length, release := acquireAssetBorrow(data)
	// owned=0: C neither frees the Go blob nor learns its identity. It consumes
	// only an opaque release token after invalidating the descriptor and
	// finishing C-side teardown, which makes zero-handle unpin safe.
	return newBorrowedAsset(buffer, length, -1, release)
}

func (c *assetCache) snapshot() assetCacheSnapshot {
	policy := currentAssetCachePolicy()
	c.mu.RLock()
	snapshot := assetCacheSnapshot{
		Generation:          c.generation,
		NameEntries:         len(c.nameCache),
		PositiveEntries:     c.positiveEntries,
		NegativeEntries:     c.negativeEntries,
		NegativeCandidates:  c.negativeCandidates,
		NegativeOther:       c.negativeOther,
		PathEntries:         len(c.pathOK),
		CachedBytes:         c.cachedBytes,
		BlobCount:           len(c.blobs),
		BorrowedCacheBytes:  c.borrowedCacheBytes,
		EvictableCacheBytes: c.cachedBytes - c.borrowedCacheBytes,
		ZipEntries:          len(c.zipBlobCache),
		InFlightLoads:       c.activeLoads,
		Inflating:           c.inflating,
		Retired:             c.retired,
		ArchiveOpen:         c.apkArch != nil,
		ArchiveCloseCount:   c.archiveCloseCount,
		CacheByteBudget:     policy.byteBudget,
		NegativeCacheLimit:  policy.negativeCap,
	}
	assetPinsMu.RLock()
	snapshot.ActiveHandles = assetPinTotals.activeHandles
	snapshot.PinnedBlobs = assetPinTotals.pinnedBlobs
	snapshot.PinnedBytes = assetPinTotals.pinnedBytes
	snapshot.LiveWorkingSetBytes = assetPinTotals.pinnedBytes
	assetPinsMu.RUnlock()
	c.mu.RUnlock()
	return snapshot
}

func assetCacheSnapshotForTest() assetCacheSnapshot {
	return assetsForOpen().snapshot()
}

// setAssetTestBeforeZipRead installs a test-only cold-reader/decompression
// seam. Production callers leave it nil, so it neither observes content nor
// changes the loader's control flow.
func setAssetTestBeforeZipRead(fn func(string) error) (restore func()) {
	assetTestHook.Lock()
	previous := assetTestHook.beforeZipRead
	assetTestHook.beforeZipRead = fn
	assetTestHook.Unlock()
	return func() {
		assetTestHook.Lock()
		assetTestHook.beforeZipRead = previous
		assetTestHook.Unlock()
	}
}

func beforeZipReadForTest(name string) error {
	assetTestHook.RLock()
	fn := assetTestHook.beforeZipRead
	assetTestHook.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(name)
}
