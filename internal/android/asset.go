// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"archive/zip"
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
}

// assetBlob is one unique backing slice retained by an asset source
// generation. Name/path/ZIP maps may all reference it, but bytes are counted
// once. It deliberately carries no asset identity beyond the private map key.
type assetBlob struct {
	bytes   int64
	lastUse uint64
	order   uint64
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

	nameCache    map[string]nameEntry
	pathOK       map[string][]byte
	zipBlobCache map[string][]byte
	blobs        map[uintptr]assetBlob
	loads        map[string]*assetLoad
	nextUse      uint64
	nextBlob     uint64
	nextNegative uint64

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

	assetPinsMu sync.RWMutex
	assetPins   = make(map[uintptr]assetPin)
	emptyAsset  byte

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
		generation:   generation,
		assetsDir:    dir,
		apkPath:      apk,
		nameCache:    make(map[string]nameEntry),
		pathOK:       make(map[string][]byte),
		zipBlobCache: make(map[string][]byte),
		blobs:        make(map[uintptr]assetBlob),
		loads:        make(map[string]*assetLoad),
		inflateSlots: make(chan struct{}, maxConcurrentAssetInflations),
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
// must hold c.mu. Pins are intentionally not part of this cache lifecycle: C
// AAsset_close has no Go callback, so it cannot prove that native code has
// stopped borrowing a pinned Go blob.
func (c *assetCache) dropRetiredCachesLocked() *zip.ReadCloser {
	if !c.retired || c.activeLoads != 0 {
		return nil
	}
	clear(c.nameCache)
	clear(c.pathOK)
	clear(c.zipBlobCache)
	clear(c.blobs)
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
		delete(c.nameCache, name)
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
				c.nameCache[name] = nameEntry{data: data}
				c.recordBlobLocked(data)
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
	c.nextNegative++
	c.nameCache[name] = nameEntry{
		err:               err,
		negativeExpiresAt: now.Add(policy.negativeTTL),
		negativeOrder:     c.nextNegative,
	}
}

func (c *assetCache) pruneExpiredNegativeLocked(now time.Time) {
	for name, entry := range c.nameCache {
		if errors.Is(entry.err, os.ErrNotExist) && !entry.negativeExpiresAt.After(now) {
			delete(c.nameCache, name)
		}
	}
}

func (c *assetCache) enforceAssetCachePolicyLocked(now time.Time) {
	policy := currentAssetCachePolicy()
	c.pruneExpiredNegativeLocked(now)
	for negativeEntries := c.negativeEntryCountLocked(); negativeEntries > policy.negativeCap; negativeEntries-- {
		name, found := c.oldestNegativeLocked()
		if !found {
			break
		}
		delete(c.nameCache, name)
	}

	// Loading may publish aliases and a backing blob in separate maps. Until
	// the final in-flight loader completes, keep that publication intact rather
	// than selecting a partially published blob.
	if c.activeLoads != 0 || c.retired {
		return
	}
	for c.cachedBytesLocked() > policy.byteBudget {
		key, found := c.oldestInactiveBlobLocked()
		if !found {
			return
		}
		c.removeBlobAliasesLocked(key)
	}
}

func (c *assetCache) negativeEntryCountLocked() int {
	count := 0
	for _, entry := range c.nameCache {
		if errors.Is(entry.err, os.ErrNotExist) {
			count++
		}
	}
	return count
}

func (c *assetCache) oldestNegativeLocked() (string, bool) {
	var (
		oldestName  string
		oldestOrder uint64
		found       bool
	)
	for name, entry := range c.nameCache {
		if !errors.Is(entry.err, os.ErrNotExist) {
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
	var bytes int64
	for _, blob := range c.blobs {
		bytes += blob.bytes
	}
	return bytes
}

func (c *assetCache) oldestInactiveBlobLocked() (uintptr, bool) {
	assetPinsMu.RLock()
	defer assetPinsMu.RUnlock()

	var (
		oldestKey  uintptr
		oldestBlob assetBlob
		found      bool
	)
	for key, blob := range c.blobs {
		if pin, active := assetPins[key]; active && pin.activeHandles != 0 {
			continue
		}
		if !found || blob.lastUse < oldestBlob.lastUse || (blob.lastUse == oldestBlob.lastUse && blob.order < oldestBlob.order) {
			oldestKey = key
			oldestBlob = blob
			found = true
		}
	}
	return oldestKey, found
}

// removeBlobAliasesLocked removes every map reference to one blob while
// holding the cache mutex. It never unpins the slice: native AAsset ownership
// belongs solely to the close-token release path.
func (c *assetCache) removeBlobAliasesLocked(key uintptr) {
	for name, entry := range c.nameCache {
		if entry.err == nil && assetBlobKey(entry.data) == key {
			delete(c.nameCache, name)
		}
	}
	for path, data := range c.pathOK {
		if assetBlobKey(data) == key {
			delete(c.pathOK, path)
		}
	}
	for name, data := range c.zipBlobCache {
		if assetBlobKey(data) == key {
			delete(c.zipBlobCache, name)
		}
	}
	delete(c.blobs, key)
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

func (c *assetCache) publishPath(path string, data []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.pathOK[path]; ok {
		return existing
	}
	if !c.retired {
		c.pathOK[path] = data
		c.recordBlobLocked(data)
	}
	return data
}

func (c *assetCache) cacheSuccessAliases(requested, usedRelSlash string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return
	}
	c.nameCache[requested] = nameEntry{data: data}
	c.recordBlobLocked(data)
	if usedRelSlash != "" && usedRelSlash != requested {
		if _, ok := c.nameCache[usedRelSlash]; !ok {
			c.nameCache[usedRelSlash] = nameEntry{data: data}
			c.recordBlobLocked(data)
		}
	}
}

func (c *assetCache) readDirAsset(name string) ([]byte, bool, error) {
	for _, path := range dirCandidates(c.assetsDir, name) {
		if data, found := c.cachedPath(path); found {
			rel, err := filepath.Rel(c.assetsDir, path)
			if err == nil {
				c.cacheSuccessAliases(name, filepath.ToSlash(rel), data)
			}
			return data, true, nil
		}

		// Directory I/O is intentionally outside every cache mutex.
		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, false, err
			}
			continue
		}
		data = c.publishPath(path, data)
		rel, err := filepath.Rel(c.assetsDir, path)
		if err == nil {
			c.cacheSuccessAliases(name, filepath.ToSlash(rel), data)
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

func (c *assetCache) inflateZip(f *zip.File) ([]byte, error) {
	if data, ok := c.cachedZip(f.Name); ok {
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
		c.mu.Unlock()
		return existing, nil
	}
	if !c.retired {
		c.zipBlobCache[f.Name] = data
		c.recordBlobLocked(data)
	}
	c.mu.Unlock()
	return data, nil
}

// recordBlobLocked records an exact unique backing allocation for this source
// generation. Empty data has no retained bytes and does not participate in a
// byte budget, so it has no blob record. The caller holds c.mu.
func (c *assetCache) recordBlobLocked(data []byte) {
	key := assetBlobKey(data)
	if key == 0 {
		return
	}
	if _, exists := c.blobs[key]; exists {
		c.touchBlobLocked(data)
		return
	}
	c.nextUse++
	c.nextBlob++
	c.blobs[key] = assetBlob{bytes: int64(len(data)), lastUse: c.nextUse, order: c.nextBlob}
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
	c.nextUse++
	blob.lastUse = c.nextUse
	c.blobs[key] = blob
}

func (c *assetCache) readAPKAsset(name string) ([]byte, error) {
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
		data, err := c.inflateZip(f)
		if err != nil {
			return nil, err
		}
		alias := strings.TrimPrefix(f.Name, "/")
		alias = strings.TrimPrefix(alias, "assets/")
		c.cacheSuccessAliases(name, alias, data)
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (c *assetCache) loadAsset(name string) ([]byte, error) {
	if c.assetsDir != "" {
		if data, ok, err := c.readDirAsset(name); err != nil {
			return nil, err
		} else if ok {
			return data, nil
		}
	}
	if c.apkPath != "" {
		return c.readAPKAsset(name)
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
		data, err := cache.loadAsset(name)
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
	if !exists {
		pin.bytes = int64(len(data))
		if len(data) != 0 {
			pin.pinner = new(runtime.Pinner)
			pin.pinner.Pin(pointer)
		}
	}
	pin.activeHandles++
	assetPins[key] = pin
	assetPinsMu.Unlock()

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
	if pin.activeHandles != 0 {
		assetPins[key] = pin
		assetPinsMu.Unlock()
		return
	}
	delete(assetPins, key)
	pinner := pin.pinner
	assetPinsMu.Unlock()

	if pinner != nil {
		pinner.Unpin()
	}
	// C has completed descriptor teardown and this was the final borrower. A
	// pending budget may now select the cache blob, but only after the pin map
	// no longer advertises a live native handle.
	cache := assetsForOpen()
	cache.mu.Lock()
	if !cache.retired {
		cache.enforceAssetCachePolicyLocked(assetCacheNow())
	}
	cache.mu.Unlock()
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
		Generation:         c.generation,
		NameEntries:        len(c.nameCache),
		PathEntries:        len(c.pathOK),
		BlobCount:          len(c.blobs),
		ZipEntries:         len(c.zipBlobCache),
		InFlightLoads:      c.activeLoads,
		Inflating:          c.inflating,
		Retired:            c.retired,
		ArchiveOpen:        c.apkArch != nil,
		ArchiveCloseCount:  c.archiveCloseCount,
		CacheByteBudget:    policy.byteBudget,
		NegativeCacheLimit: policy.negativeCap,
	}
	for _, entry := range c.nameCache {
		if entry.err != nil {
			snapshot.NegativeEntries++
			if errors.Is(entry.err, os.ErrNotExist) {
				snapshot.NegativeCandidates++
			} else {
				snapshot.NegativeOther++
			}
		} else {
			snapshot.PositiveEntries++
		}
	}

	assetPinsMu.RLock()
	for key, blob := range c.blobs {
		snapshot.CachedBytes += blob.bytes
		if pin, borrowed := assetPins[key]; borrowed && pin.activeHandles != 0 {
			snapshot.BorrowedCacheBytes += blob.bytes
		} else {
			snapshot.EvictableCacheBytes += blob.bytes
		}
	}
	for _, pin := range assetPins {
		snapshot.ActiveHandles += pin.activeHandles
		if pin.pinner != nil {
			snapshot.PinnedBlobs++
			snapshot.PinnedBytes += pin.bytes
			snapshot.LiveWorkingSetBytes += pin.bytes
		}
	}
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
