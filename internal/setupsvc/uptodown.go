// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UptodownSource downloads the latest Roblox Android package from Uptodown.
// Bytes are untrusted until Install cryptographically verifies the official
// Roblox signer and x86-64 libroblox.so.
type UptodownSource struct {
	Client   *http.Client
	Origin   string
	DWOrigin string
	MaxBytes int64

	clientOnce sync.Once
	httpClient *http.Client
}

func (s *UptodownSource) Availability(context.Context) SourceAvailability {
	return SourceAvailability{
		Available:   true,
		Name:        "Uptodown",
		LegalURL:    uptodownLegalURL,
		Explanation: uptodownExplanation,
	}
}

func (s *UptodownSource) Acquire(ctx context.Context, dir string, progress ProgressFunc) ([]string, error) {
	if err := checkContext(ctx, uptodownOp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, uptodownOp, "cannot create the private download directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	report(progress, PhaseAcquiring, 0, 0, "Looking up the latest Roblox package on Uptodown")

	listingHTML, err := s.getHTML(ctx, uptodownAppPath, s.origin()+"/")
	if err != nil {
		return nil, err
	}
	listing, err := parseUptodownListing(listingHTML)
	if err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 1, 4, "Selecting the x86-64 Roblox package")
	filePath, err := s.resolveFilePath(ctx, listing)
	if err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 2, 4, "Downloading the selected Uptodown package")
	packagePath := filepath.Join(dir, "roblox-package.bin")
	referer := s.origin() + uptodownAppPath + "/" + listing.FileID + "-x"
	if err := s.downloadFile(ctx, filePath, referer, packagePath, progress); err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 3, 4, "Keeping only x86-64 Roblox package files")
	return selectX86PackageFiles(packagePath, dir, uptodownOp)
}

func (s *UptodownSource) resolveFilePath(ctx context.Context, listing uptodownListing) (string, error) {
	if listing.DataURL != "" {
		return listing.DataURL, nil
	}
	payload, err := json.Marshal(map[string]string{
		"onlyXapk": "1",
	})
	if err != nil {
		return "", setupError(ErrInvalidRequest, uptodownOp, "cannot encode the Uptodown download request", err)
	}
	ajaxPath := uptodownDownloadAJAXPath(listing.AppID, listing.FileID)
	body, err := s.postJSON(ctx, ajaxPath, s.origin()+uptodownAppPath, payload)
	if err != nil {
		return "", err
	}
	return parseUptodownDownloadURL(body)
}

func (s *UptodownSource) origin() string {
	if s != nil && strings.TrimSpace(s.Origin) != "" {
		return strings.TrimRight(s.Origin, "/")
	}
	return uptodownDefaultOrigin
}

func (s *UptodownSource) dwOrigin() string {
	if s != nil && strings.TrimSpace(s.DWOrigin) != "" {
		return strings.TrimRight(s.DWOrigin, "/")
	}
	return uptodownDefaultDW
}

func (s *UptodownSource) maxBytes() int64 {
	if s != nil && s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultLimits().MaxFileBytes
}

func (s *UptodownSource) client() *http.Client {
	if s == nil {
		return http.DefaultClient
	}
	if s.Client != nil {
		return s.Client
	}
	s.clientOnce.Do(func() {
		jar, _ := cookiejar.New(nil)
		s.httpClient = &http.Client{
			Timeout: 15 * time.Minute,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 8 {
					return fmt.Errorf("too many redirects")
				}
				return s.trustedURL(req.URL)
			},
		}
	})
	return s.httpClient
}

func (s *UptodownSource) getHTML(ctx context.Context, path, referer string) ([]byte, error) {
	u, err := s.resolve(s.origin(), path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, setupError(ErrInvalidRequest, uptodownOp, "cannot create Uptodown request", err)
	}
	s.setHeaders(req, referer, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	return s.readOK(req, 2<<20, "Uptodown page")
}

func (s *UptodownSource) postJSON(ctx context.Context, path, referer string, payload []byte) ([]byte, error) {
	u, err := s.resolve(s.origin(), path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, setupError(ErrInvalidRequest, uptodownOp, "cannot create Uptodown download request", err)
	}
	s.setHeaders(req, referer, "application/json, text/javascript;q=0.9,*/*;q=0.8")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", s.origin())
	return s.readOK(req, 1<<20, "Uptodown download metadata")
}

func (s *UptodownSource) downloadFile(ctx context.Context, raw, referer, dest string, progress ProgressFunc) error {
	u, err := s.resolveDownload(raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return setupError(ErrInvalidRequest, uptodownOp, "cannot create Uptodown file request", err)
	}
	s.setHeaders(req, referer, "application/vnd.android.package-archive, application/zip, */*")
	resp, err := s.client().Do(req)
	if err != nil {
		return uptodownNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return uptodownStatusError(resp.StatusCode)
	}
	max := s.maxBytes()
	if resp.ContentLength > max {
		return setupError(ErrSizeLimit, uptodownOp, "Uptodown package exceeds the safe download limit", nil)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return setupError(ErrInstall, uptodownOp, "cannot create a private download file", err)
	}
	var done int64
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	reader := &progressReader{ctx: ctx, r: io.LimitReader(resp.Body, max+1), onRead: func(n int64) {
		done += n
		report(progress, PhaseAcquiring, done, total, "Downloading the selected Uptodown package")
	}}
	n, copyErr := io.Copy(f, reader)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(dest)
		if copyErr == nil {
			copyErr = syncErr
		}
		if copyErr == nil {
			copyErr = closeErr
		}
		return setupError(ErrNetwork, uptodownOp, "Uptodown package transfer did not complete", copyErr)
	}
	if n <= 0 || n > max {
		_ = os.Remove(dest)
		return setupError(ErrSizeLimit, uptodownOp, "Uptodown package has an invalid size", nil)
	}
	return nil
}

func (s *UptodownSource) readOK(req *http.Request, limit int64, what string) ([]byte, error) {
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, uptodownNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, uptodownStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, setupError(ErrNetwork, uptodownOp, what+" could not be read", err)
	}
	if int64(len(data)) > limit {
		return nil, setupError(ErrSizeLimit, uptodownOp, what+" exceeds the safe size limit", nil)
	}
	return data, nil
}

func (s *UptodownSource) setHeaders(req *http.Request, referer, accept string) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

func (s *UptodownSource) resolveDownload(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, setupError(ErrSourceUnavailable, uptodownOp, "Uptodown file path is empty", nil)
	}
	if strings.Contains(raw, "://") {
		return s.resolve(s.dwOrigin(), raw)
	}
	return s.resolve(s.dwOrigin(), "/dwn/"+strings.TrimPrefix(raw, "/"))
}

func (s *UptodownSource) resolve(baseOrigin, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	base, err := url.Parse(strings.TrimRight(baseOrigin, "/") + "/")
	if err != nil {
		return nil, setupError(ErrSourceTrust, uptodownOp, "Uptodown origin is invalid", err)
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, setupError(ErrSourceTrust, uptodownOp, "Uptodown returned an invalid URL", err)
	}
	resolved := base.ResolveReference(ref)
	if err := s.trustedURL(resolved); err != nil {
		return nil, setupError(ErrSourceTrust, uptodownOp, "Uptodown URL is outside the Roblox listing or download path", err)
	}
	return resolved, nil
}

func (s *UptodownSource) trustedURL(u *url.URL) error {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("HTTPS URL without credentials required")
	}
	origin, err := url.Parse(s.origin() + "/")
	if err != nil {
		return err
	}
	if !strings.EqualFold(u.Scheme, origin.Scheme) {
		return fmt.Errorf("unexpected URL scheme")
	}
	host := strings.ToLower(u.Hostname())
	if !s.trustedHost(host) {
		return fmt.Errorf("host not trusted")
	}
	path := u.EscapedPath()
	if isUptodownAppPath(path) || isUptodownDWPath(path) {
		return nil
	}
	return fmt.Errorf("path not allowed")
}

func (s *UptodownSource) trustedHost(host string) bool {
	host = strings.ToLower(host)
	if host == "dw.uptodown.com" || host == "uptodown.com" || strings.HasSuffix(host, ".uptodown.com") {
		return true
	}
	if originHost := strings.ToLower(mustHostname(s.origin())); originHost != "" && host == originHost {
		return true
	}
	if dwHost := strings.ToLower(mustHostname(s.dwOrigin())); dwHost != "" && host == dwHost {
		return true
	}
	return false
}

func isUptodownAppPath(path string) bool {
	path = strings.ToLower(path)
	switch {
	case strings.HasPrefix(path, "/android"):
		return true
	case strings.HasPrefix(path, "/ajax/app/"):
		return true
	case strings.HasPrefix(path, "/app/"):
		return true
	default:
		return false
	}
}

func isUptodownDWPath(path string) bool {
	path = strings.ToLower(path)
	return path == "/dwn" || strings.HasPrefix(path, "/dwn/")
}

func mustHostname(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func uptodownNetworkError(err error) error {
	return setupError(ErrNetwork, uptodownOp, "Uptodown request failed; the site may be blocking automated downloads", err)
}

func uptodownStatusError(status int) error {
	if status == http.StatusForbidden || status == http.StatusTooManyRequests || status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return setupError(ErrNetwork, uptodownOp, "Uptodown blocked or rate-limited the automated download", nil)
	}
	return setupError(ErrNetwork, uptodownOp, fmt.Sprintf("Uptodown returned HTTP %d", status), nil)
}

var _ Source = (*UptodownSource)(nil)
