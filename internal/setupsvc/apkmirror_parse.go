// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

const (
	apkMirrorDefaultOrigin = "https://www.apkmirror.com"
	apkMirrorRobloxPrefix  = "/apk/roblox-corporation/roblox/"
	apkMirrorDownloadPHP   = "/wp-content/themes/APKMirror/download.php"
	apkMirrorExplanation   = "APKMirror is unofficial transport. Tipsy then verifies the official Roblox package name, APK signatures, and x86-64 libroblox.so. ARM-only or tampered packages are rejected."
)

type apkMirrorVariant struct {
	Kind string
	Arch string
	Path string
}

var (
	apkMirrorHRefRe = regexp.MustCompile(`(?i)href\s*=\s*"([^"]+)"`)
	apkMirrorRowRe  = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*table-row[^"]*"[^>]*>`)
)

func parseLatestReleasePath(body []byte) (string, error) {
	best := ""
	var bestVer [3]int
	for _, href := range apkMirrorHRefs(body) {
		path := hrefPath(href)
		if !isRobloxReleasePath(path) || isRobloxVariantPath(path) {
			continue
		}
		ver, ok := robloxReleaseVersion(path)
		if !ok {
			continue
		}
		if best == "" || versionGreater(ver, bestVer) {
			best = path
			bestVer = ver
		}
	}
	if best == "" {
		return "", setupError(ErrSourceUnavailable, "APKMirror", "could not find a Roblox release on APKMirror", nil)
	}
	return best, nil
}

func parseVariants(body []byte) []apkMirrorVariant {
	htmlBody := string(body)
	lower := strings.ToLower(htmlBody)
	section := htmlBody
	if idx := strings.Index(lower, "variants-table"); idx >= 0 {
		end := idx + 200000
		if end > len(htmlBody) {
			end = len(htmlBody)
		}
		section = htmlBody[idx:end]
	}
	indexes := apkMirrorRowRe.FindAllStringIndex(section, -1)
	if len(indexes) == 0 {
		return parseVariantsFallback(body)
	}
	var variants []apkMirrorVariant
	for i, loc := range indexes {
		start := loc[0]
		end := len(section)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		if v, ok := parseVariantRow(section[start:end]); ok {
			variants = append(variants, v)
		}
	}
	if len(variants) == 0 {
		return parseVariantsFallback(body)
	}
	return variants
}

func parseVariantsFallback(body []byte) []apkMirrorVariant {
	var variants []apkMirrorVariant
	seen := map[string]bool{}
	for _, href := range apkMirrorHRefs(body) {
		path := hrefPath(href)
		if !isRobloxVariantPath(path) || seen[path] {
			continue
		}
		seen[path] = true
		arch := inferArchFromPath(path)
		if arch == "" {
			arch = "unknown"
		}
		variants = append(variants, apkMirrorVariant{Kind: "apk", Arch: arch, Path: path})
	}
	return variants
}

func parseVariantRow(row string) (apkMirrorVariant, bool) {
	lower := strings.ToLower(row)
	kind := "apk"
	if strings.Contains(lower, "bundle") {
		kind = "bundle"
	}
	arch := firstArchToken(lower)
	var path string
	for _, href := range apkMirrorHRefs([]byte(row)) {
		candidate := hrefPath(href)
		if isRobloxVariantPath(candidate) {
			path = candidate
			break
		}
	}
	if path == "" || arch == "" {
		return apkMirrorVariant{}, false
	}
	return apkMirrorVariant{Kind: kind, Arch: arch, Path: path}, true
}

func selectX86Variant(variants []apkMirrorVariant) (apkMirrorVariant, error) {
	bestRank := 0
	var best apkMirrorVariant
	for _, v := range variants {
		if rank := variantRank(v); rank > bestRank {
			bestRank = rank
			best = v
		}
	}
	if bestRank == 0 {
		return apkMirrorVariant{}, setupError(ErrMissingX8664, "APKMirror", "APKMirror has no x86-64 or universal Roblox variant for this release", nil)
	}
	return best, nil
}

func variantRank(v apkMirrorVariant) int {
	arch := normalizeArch(v.Arch)
	kind := strings.ToLower(strings.TrimSpace(v.Kind))
	switch {
	case arch == "x86_64" && kind == "apk":
		return 400
	case arch == "x86_64" && kind == "bundle":
		return 300
	case arch == "universal" && kind == "bundle":
		return 200
	case arch == "universal" && kind == "apk":
		return 100
	default:
		return 0
	}
}

func parseDownloadButtonPath(body []byte) (string, error) {
	htmlBody := string(body)
	lower := strings.ToLower(htmlBody)
	for _, loc := range findAll(lower, "downloadbutton") {
		window := htmlWindow(htmlBody, loc, 400)
		for _, href := range apkMirrorHRefs([]byte(window)) {
			path := hrefPath(href)
			if isRobloxAppPath(path) || strings.Contains(strings.ToLower(path), "download.php") {
				return path, nil
			}
		}
	}
	return "", setupError(ErrSourceUnavailable, "APKMirror", "could not find the APKMirror download button", nil)
}

func parseDirectDownloadPath(body []byte) (string, error) {
	for _, href := range apkMirrorHRefs(body) {
		path, rawQuery := splitPathQuery(hrefPath(href))
		if strings.EqualFold(path, apkMirrorDownloadPHP) || strings.HasSuffix(strings.ToLower(path), "/download.php") {
			if rawQuery != "" {
				return path + "?" + rawQuery, nil
			}
			return path, nil
		}
	}
	return "", setupError(ErrSourceUnavailable, "APKMirror", "could not find the APKMirror file download link", nil)
}

func apkMirrorHRefs(body []byte) []string {
	matches := apkMirrorHRefRe.FindAllSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		out = append(out, html.UnescapeString(string(m[1])))
	}
	return out
}

func hrefPath(href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(strings.ToLower(href), "javascript:") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if u.Path == "" && strings.HasPrefix(href, "/") {
		return href
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func splitPathQuery(path string) (string, string) {
	path, query, ok := strings.Cut(path, "?")
	if !ok {
		return path, ""
	}
	return path, query
}

func isRobloxAppPath(path string) bool {
	path, _ = splitPathQuery(path)
	return strings.HasPrefix(strings.ToLower(path), apkMirrorRobloxPrefix)
}

func isRobloxReleasePath(path string) bool {
	path, _ = splitPathQuery(path)
	path = strings.TrimSuffix(path, "/")
	if !isRobloxAppPath(path) {
		return false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) == 4 && strings.HasSuffix(parts[3], "-release")
}

func robloxReleaseVersion(path string) ([3]int, bool) {
	path, _ = splitPathQuery(path)
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 {
		return [3]int{}, false
	}
	name := strings.TrimSuffix(parts[3], "-release")
	name = strings.TrimPrefix(name, "roblox-")
	nums := strings.Split(name, "-")
	if len(nums) != 3 {
		return [3]int{}, false
	}
	var ver [3]int
	for i, n := range nums {
		v := 0
		for _, c := range n {
			if c < '0' || c > '9' {
				return [3]int{}, false
			}
			v = v*10 + int(c-'0')
		}
		ver[i] = v
	}
	return ver, true
}

func versionGreater(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func isRobloxVariantPath(path string) bool {
	path, _ = splitPathQuery(path)
	path = strings.TrimSuffix(path, "/")
	if !isRobloxAppPath(path) {
		return false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) >= 5 && strings.HasSuffix(parts[3], "-release")
}

func normalizeArch(arch string) string {
	arch = strings.ToLower(strings.TrimSpace(arch))
	arch = strings.ReplaceAll(arch, " ", "")
	switch arch {
	case "x86-64", "x86_64", "amd64":
		return "x86_64"
	case "arm64", "arm64-v8a", "arm64_v8a":
		return "arm64-v8a"
	case "armeabi-v7a", "armeabi_v7a", "armeabi":
		return "armeabi-v7a"
	case "universal", "nodpi":
		if arch == "nodpi" {
			return ""
		}
		return "universal"
	default:
		return arch
	}
}

func firstArchToken(lowerRow string) string {
	for _, token := range []string{"x86_64", "x86-64", "universal", "arm64-v8a", "arm64_v8a", "armeabi-v7a", "armeabi_v7a", "armeabi", "x86"} {
		if strings.Contains(lowerRow, token) {
			return normalizeArch(token)
		}
	}
	return ""
}

func inferArchFromPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "x86_64"), strings.Contains(lower, "x86-64"):
		return "x86_64"
	case strings.Contains(lower, "universal"):
		return "universal"
	case strings.Contains(lower, "arm64"):
		return "arm64-v8a"
	default:
		return ""
	}
}

func findAll(lower, needle string) []int {
	var out []int
	from := 0
	for {
		i := strings.Index(lower[from:], needle)
		if i < 0 {
			return out
		}
		out = append(out, from+i)
		from += i + len(needle)
	}
}

func htmlWindow(body string, idx, radius int) string {
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + radius
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}
