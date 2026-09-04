package apk

import (
	"sort"
)

// Diff is a package-level comparison of two Inspect reports (merged view).
type Diff struct {
	PackageNameOld       string   `json:"packageNameOld,omitempty"`
	PackageNameNew       string   `json:"packageNameNew,omitempty"`
	VersionNameOld       string   `json:"versionNameOld,omitempty"`
	VersionNameNew       string   `json:"versionNameNew,omitempty"`
	VersionCodeOld       int64    `json:"versionCodeOld,omitempty"`
	VersionCodeNew       int64    `json:"versionCodeNew,omitempty"`
	ArchitecturesAdded   []string `json:"architecturesAdded,omitempty"`
	ArchitecturesRemoved []string `json:"architecturesRemoved,omitempty"`
	NativesAdded         []string `json:"nativesAdded,omitempty"`
	NativesRemoved       []string `json:"nativesRemoved,omitempty"`
	NativesChanged       []string `json:"nativesChanged,omitempty"`
	CertSHA256Added      []string `json:"certSha256Added,omitempty"`
	CertSHA256Removed    []string `json:"certSha256Removed,omitempty"`
}

// Compare reports version, ABI, native ZIP-path, and certificate hash changes
// between two Inspect results. Nil reports are treated as empty.
func Compare(old, new *Report) *Diff {
	om, nm := mergedOf(old), mergedOf(new)
	d := &Diff{
		PackageNameOld: om.PackageName,
		PackageNameNew: nm.PackageName,
		VersionNameOld: om.VersionName,
		VersionNameNew: nm.VersionName,
		VersionCodeOld: om.VersionCode,
		VersionCodeNew: nm.VersionCode,
	}
	d.ArchitecturesAdded, d.ArchitecturesRemoved = addedRemoved(om.Architectures, nm.Architectures)

	oldN := nativeMap(om.NativeLibraries)
	newN := nativeMap(nm.NativeLibraries)
	d.NativesAdded, d.NativesRemoved = addedRemoved(keysOf(oldN), keysOf(newN))
	var changed []string
	for path, oldHash := range oldN {
		if newHash, ok := newN[path]; ok && newHash != oldHash {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	d.NativesChanged = emptyNil(changed)

	d.CertSHA256Added, d.CertSHA256Removed = addedRemoved(om.Signing.CertSHA256, nm.Signing.CertSHA256)
	return d
}

func mergedOf(r *Report) Merged {
	if r == nil {
		return Merged{}
	}
	if r.Merged != nil {
		return *r.Merged
	}
	if m := mergePackages(r.Packages); m != nil {
		return *m
	}
	return Merged{}
}

func nativeMap(libs []NativeLib) map[string]string {
	m := make(map[string]string, len(libs))
	for _, lib := range libs {
		if _, ok := m[lib.ZIPPath]; !ok {
			m[lib.ZIPPath] = lib.SHA256
		}
	}
	return m
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// addedRemoved returns items in next but not prev, and items in prev but not next.
func addedRemoved(prev, next []string) (added, removed []string) {
	ps := toSet(prev)
	ns := toSet(next)
	for _, s := range uniqueSorted(append([]string(nil), next...)) {
		if _, ok := ps[s]; !ok {
			added = append(added, s)
		}
	}
	for _, s := range uniqueSorted(append([]string(nil), prev...)) {
		if _, ok := ns[s]; !ok {
			removed = append(removed, s)
		}
	}
	return emptyNil(added), emptyNil(removed)
}

func toSet(ss []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		if s != "" {
			m[s] = struct{}{}
		}
	}
	return m
}

func emptyNil(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	return ss
}

func (d *Diff) empty() bool {
	if d == nil {
		return true
	}
	return d.PackageNameOld == d.PackageNameNew &&
		d.VersionNameOld == d.VersionNameNew &&
		d.VersionCodeOld == d.VersionCodeNew &&
		len(d.ArchitecturesAdded) == 0 &&
		len(d.ArchitecturesRemoved) == 0 &&
		len(d.NativesAdded) == 0 &&
		len(d.NativesRemoved) == 0 &&
		len(d.NativesChanged) == 0 &&
		len(d.CertSHA256Added) == 0 &&
		len(d.CertSHA256Removed) == 0
}
