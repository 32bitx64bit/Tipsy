// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
)

const (
	uptodownDefaultOrigin = "https://roblox.en.uptodown.com"
	uptodownDefaultDW     = "https://dw.uptodown.com"
	uptodownAppPath       = "/android/download"
	uptodownLegalURL      = "https://roblox.en.uptodown.com/android"
	uptodownExplanation   = "Uptodown is unofficial transport. Tipsy then verifies the official Roblox package name, APK signatures, and x86-64 libroblox.so. Download the XAPK, not Uptodown's small installer APK. ARM-only or tampered packages are rejected."
	uptodownRobloxPkg     = "com.roblox.client"
	uptodownOp            = "Uptodown"
)

type uptodownListing struct {
	AppID    string
	FileID   string
	Version  string
	Kind     string
	DataURL  string
	HasX8664 bool
}

type uptodownDownloadURLBody struct {
	Success int `json:"success"`
	Data    struct {
		DownloadURL string `json:"downloadURL"`
	} `json:"data"`
}

var (
	uptodownNameTagRe = regexp.MustCompile(`(?is)id="detail-app-name"[^>]*>`)
	uptodownButtonRe  = regexp.MustCompile(`(?is)id="detail-download-button"[^>]*>`)
	uptodownAppIDRe   = regexp.MustCompile(`(?i)data-(?:app-id|code)="(\d+)"`)
	uptodownFileIDRe  = regexp.MustCompile(`(?i)data-file-id="(\d+)"`)
	uptodownVersionRe = regexp.MustCompile(`(?i)class="version"[^>]*>\s*([^<]+)`)
	uptodownDataURLRe = regexp.MustCompile(`(?i)\bdata-url="([^"]+)"`)
	uptodownPkgRe     = regexp.MustCompile(`(?i)package\s*name[^a-z0-9]*com\.roblox\.client`)
)

func parseUptodownListing(body []byte) (uptodownListing, error) {
	htmlBody := string(body)
	if !uptodownPkgRe.MatchString(htmlBody) && !strings.Contains(htmlBody, uptodownRobloxPkg) {
		return uptodownListing{}, setupError(ErrWrongPackage, uptodownOp, "Uptodown listing is not the official Roblox package", nil)
	}
	nameTag := firstMatch(uptodownNameTagRe, htmlBody)
	button := firstMatch(uptodownButtonRe, htmlBody)
	listing := uptodownListing{
		AppID:    firstNonEmpty(firstCapture(uptodownAppIDRe, button), firstCapture(uptodownAppIDRe, nameTag)),
		FileID:   firstNonEmpty(firstCapture(uptodownFileIDRe, button), firstCapture(uptodownFileIDRe, nameTag)),
		Version:  strings.TrimSpace(html.UnescapeString(firstCapture(uptodownVersionRe, htmlBody))),
		HasX8664: listingHasX8664(htmlBody),
		Kind:     inferUptodownKind(htmlBody),
		DataURL:  html.UnescapeString(firstCapture(uptodownDataURLRe, button)),
	}
	if listing.AppID == "" || listing.FileID == "" {
		return uptodownListing{}, setupError(ErrSourceUnavailable, uptodownOp, "could not find the Uptodown Roblox file id", nil)
	}
	if !listing.HasX8664 {
		return uptodownListing{}, setupError(ErrMissingX8664, uptodownOp, "Uptodown has no x86-64 Roblox variant for this release", nil)
	}
	return listing, nil
}

func listingHasX8664(htmlBody string) bool {
	lower := strings.ToLower(htmlBody)
	if idx := strings.Index(lower, "architecture"); idx >= 0 {
		window := htmlWindow(htmlBody, idx, 400)
		return strings.Contains(strings.ToLower(window), "x86_64") || strings.Contains(strings.ToLower(window), "x86-64")
	}
	return strings.Contains(lower, "x86_64") || strings.Contains(lower, "x86-64")
}

func inferUptodownKind(htmlBody string) string {
	lower := strings.ToLower(htmlBody)
	if strings.Contains(lower, "xapk") {
		return "xapk"
	}
	return "apk"
}

func firstMatch(re *regexp.Regexp, s string) string {
	return re.FindString(s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseUptodownDownloadURL(body []byte) (string, error) {
	var parsed uptodownDownloadURLBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", setupError(ErrSourceUnavailable, uptodownOp, "Uptodown download metadata is not JSON", err)
	}
	path := strings.TrimSpace(parsed.Data.DownloadURL)
	if parsed.Success == 1 && path != "" {
		return path, nil
	}
	return "", setupError(ErrSourceUnavailable, uptodownOp, "Uptodown did not return a file download path", nil)
}

func firstCapture(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func uptodownDownloadAJAXPath(appID, fileID string) string {
	return "/ajax/app/" + url.PathEscape(appID) + "/file/" + url.PathEscape(fileID) + "/download-url"
}
