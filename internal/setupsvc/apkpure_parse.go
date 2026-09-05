// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
)

const (
	apkpureDefaultAPI    = "https://api.pureapk.com"
	apkpureLegalURL      = "https://apkpure.com/roblox/com.roblox.client"
	apkpureExplanation   = "APKPure is unofficial transport. Tipsy then verifies the official Roblox package name, APK signatures, and x86-64 libroblox.so. Extra language/ARM splits are dropped. ARM-only or tampered packages are rejected."
	apkpureRobloxPkg     = "com.roblox.client"
	apkpureOp            = "APKPure"
	apkpureAPIPath       = "/m/v3/cms/app_version"
	apkpureX86ABI        = "x86_64"
	apkpureAllABI        = "arm64-v8a,armeabi-v7a,armeabi,x86,x86_64"
	apkpureClientVersion = "3172501"
	apkpureClientSV      = "29"
	apkpureMetadataLimit = 4 << 20
	apkpureMarkerXAPK    = "XAPKJ"
	apkpureMarkerAPK     = "APKJ"
)

type apkpureListing struct {
	Version string
	URL     string
	XAPK    bool
}

var apkpureVersionRe = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

func parseAPKPureListing(body []byte) (apkpureListing, error) {
	if !bytes.Contains(body, []byte(apkpureRobloxPkg)) {
		return apkpureListing{}, setupError(ErrWrongPackage, apkpureOp, "APKPure listing is not the official Roblox package", nil)
	}
	listing, ok := firstAPKPureDownload(body)
	if !ok {
		return apkpureListing{}, setupError(ErrSourceUnavailable, apkpureOp, "could not find an APKPure Roblox download URL", nil)
	}
	listing.Version = firstAPKPureVersion(body[:listing.offset])
	return listing.apkpureListing, nil
}

type apkpureHit struct {
	apkpureListing
	offset int
}

func firstAPKPureDownload(body []byte) (apkpureHit, bool) {
	for i := 0; i < len(body); i++ {
		kind, skip := apkpureMarkerAt(body, i)
		if skip == 0 {
			continue
		}
		rest := body[i+skip:]
		if len(rest) < 2 {
			break
		}
		rest = rest[2:]
		if !bytes.HasPrefix(rest, []byte("https://")) && !bytes.HasPrefix(rest, []byte("http://")) {
			continue
		}
		raw := string(takeAPKPureURL(rest))
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" {
			continue
		}
		if !isAPKPureDownloadPath(parsed.EscapedPath()) {
			continue
		}
		xapk := kind == apkpureMarkerXAPK || strings.Contains(strings.ToLower(parsed.EscapedPath()), "/xapk/")
		return apkpureHit{
			apkpureListing: apkpureListing{URL: raw, XAPK: xapk},
			offset:         i,
		}, true
	}
	return apkpureHit{}, false
}

func apkpureMarkerAt(body []byte, i int) (kind string, skip int) {
	if i+len(apkpureMarkerXAPK) <= len(body) && string(body[i:i+len(apkpureMarkerXAPK)]) == apkpureMarkerXAPK {
		return apkpureMarkerXAPK, len(apkpureMarkerXAPK)
	}
	if i+len(apkpureMarkerAPK) <= len(body) && string(body[i:i+len(apkpureMarkerAPK)]) == apkpureMarkerAPK {
		if i > 0 && body[i-1] == 'X' {
			return "", 0
		}
		return apkpureMarkerAPK, len(apkpureMarkerAPK)
	}
	return "", 0
}

func takeAPKPureURL(raw []byte) []byte {
	n := 0
	for n < len(raw) && isAPKPureURLByte(raw[n]) {
		n++
	}
	return raw[:n]
}

func isAPKPureURLByte(b byte) bool {
	if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' {
		return true
	}
	return strings.IndexByte("-@:%._+~#=?&/()", b) >= 0
}

func isAPKPureDownloadPath(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "/b/apk/") || strings.Contains(path, "/b/xapk/")
}

func firstAPKPureVersion(prefix []byte) string {
	matches := apkpureVersionRe.FindAll(prefix, -1)
	if len(matches) == 0 {
		return ""
	}
	return string(matches[len(matches)-1])
}
