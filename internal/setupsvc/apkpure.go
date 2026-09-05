// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
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

// APKPureSource downloads the latest Roblox Android package from APKPure's
// public CMS API. Bytes are untrusted until Install cryptographically verifies
// the official Roblox signer and x86-64 libroblox.so.
type APKPureSource struct {
	Client   *http.Client
	Origin   string
	MaxBytes int64

	clientOnce sync.Once
	httpClient *http.Client
}

func (s *APKPureSource) Availability(context.Context) SourceAvailability {
	return SourceAvailability{
		Available:   true,
		Name:        "APKPure",
		LegalURL:    apkpureLegalURL,
		Explanation: apkpureExplanation,
	}
}

func (s *APKPureSource) Acquire(ctx context.Context, dir string, progress ProgressFunc) ([]string, error) {
	if err := checkContext(ctx, apkpureOp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, apkpureOp, "cannot create the private download directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	report(progress, PhaseAcquiring, 0, 0, "Looking up the latest Roblox package on APKPure")

	listing, err := s.lookup(ctx, apkpureAllABI)
	if err != nil {
		listing, err = s.lookup(ctx, apkpureX86ABI)
		if err != nil {
			return nil, err
		}
	}
	report(progress, PhaseAcquiring, 1, 4, "Selecting the x86-64 Roblox package")
	packagePath := filepath.Join(dir, "roblox-package.bin")
	report(progress, PhaseAcquiring, 2, 4, "Downloading the selected APKPure package")
	if err := s.downloadFile(ctx, listing.URL, packagePath, progress); err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 3, 4, "Keeping only x86-64 Roblox package files")
	selected, err := selectX86PackageFiles(packagePath, dir, apkpureOp)
	if ErrorKindOf(err) != ErrMissingX8664 {
		return selected, err
	}
	_ = os.Remove(packagePath)
	fallback, fallbackErr := s.lookup(ctx, apkpureX86ABI)
	if fallbackErr != nil {
		return nil, err
	}
	if fallback.URL == listing.URL {
		return nil, err
	}
	report(progress, PhaseAcquiring, 2, 4, "Downloading the x86-64 APKPure package")
	if err := s.downloadFile(ctx, fallback.URL, packagePath, progress); err != nil {
		return nil, err
	}
	return selectX86PackageFiles(packagePath, dir, apkpureOp)
}

func (s *APKPureSource) lookup(ctx context.Context, abis string) (apkpureListing, error) {
	body, err := s.getMetadata(ctx, abis)
	if err != nil {
		return apkpureListing{}, err
	}
	listing, err := parseAPKPureListing(body)
	if err != nil {
		return apkpureListing{}, err
	}
	if _, err := s.resolveDownload(listing.URL); err != nil {
		return apkpureListing{}, err
	}
	return listing, nil
}

func (s *APKPureSource) origin() string {
	if s != nil && strings.TrimSpace(s.Origin) != "" {
		return strings.TrimRight(s.Origin, "/")
	}
	return apkpureDefaultAPI
}

func (s *APKPureSource) maxBytes() int64 {
	if s != nil && s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultLimits().MaxFileBytes
}

func (s *APKPureSource) client() *http.Client {
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

func (s *APKPureSource) getMetadata(ctx context.Context, abis string) ([]byte, error) {
	u, err := s.resolve(s.origin(), apkpureAPIPath+"?hl=en-US&package_name="+url.QueryEscape(apkpureRobloxPkg))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, setupError(ErrInvalidRequest, apkpureOp, "cannot create APKPure request", err)
	}
	s.setHeaders(req, "application/octet-stream, */*")
	req.Header.Set("x-cv", apkpureClientVersion)
	req.Header.Set("x-sv", apkpureClientSV)
	req.Header.Set("x-gp", "1")
	req.Header.Set("x-abis", abis)
	return s.readOK(req, apkpureMetadataLimit, "APKPure metadata")
}

func (s *APKPureSource) downloadFile(ctx context.Context, raw, dest string, progress ProgressFunc) error {
	u, err := s.resolveDownload(raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return setupError(ErrInvalidRequest, apkpureOp, "cannot create APKPure file request", err)
	}
	s.setHeaders(req, "application/vnd.android.package-archive, application/zip, */*")
	resp, err := s.client().Do(req)
	if err != nil {
		return apkpureNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apkpureStatusError(resp.StatusCode)
	}
	max := s.maxBytes()
	if resp.ContentLength > max {
		return setupError(ErrSizeLimit, apkpureOp, "APKPure package exceeds the safe download limit", nil)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return setupError(ErrInstall, apkpureOp, "cannot create a private download file", err)
	}
	var done int64
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	reader := &progressReader{ctx: ctx, r: io.LimitReader(resp.Body, max+1), onRead: func(n int64) {
		done += n
		report(progress, PhaseAcquiring, done, total, "Downloading the selected APKPure package")
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
		return setupError(ErrNetwork, apkpureOp, "APKPure package transfer did not complete", copyErr)
	}
	if n <= 0 || n > max {
		_ = os.Remove(dest)
		return setupError(ErrSizeLimit, apkpureOp, "APKPure package has an invalid size", nil)
	}
	return nil
}

func (s *APKPureSource) readOK(req *http.Request, limit int64, what string) ([]byte, error) {
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, apkpureNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apkpureStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, setupError(ErrNetwork, apkpureOp, what+" could not be read", err)
	}
	if int64(len(data)) > limit {
		return nil, setupError(ErrSizeLimit, apkpureOp, what+" exceeds the safe size limit", nil)
	}
	return data, nil
}

func (s *APKPureSource) setHeaders(req *http.Request, accept string) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
}

func (s *APKPureSource) resolveDownload(raw string) (*url.URL, error) {
	return s.resolve(s.origin(), raw)
}

func (s *APKPureSource) resolve(baseOrigin, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	base, err := url.Parse(strings.TrimRight(baseOrigin, "/") + "/")
	if err != nil {
		return nil, setupError(ErrSourceTrust, apkpureOp, "APKPure origin is invalid", err)
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, setupError(ErrSourceTrust, apkpureOp, "APKPure returned an invalid URL", err)
	}
	resolved := base.ResolveReference(ref)
	if err := s.trustedURL(resolved); err != nil {
		return nil, setupError(ErrSourceTrust, apkpureOp, "APKPure URL is outside the Roblox listing or download path", err)
	}
	return resolved, nil
}

func (s *APKPureSource) trustedURL(u *url.URL) error {
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
	if isAPKPureAPIHost(host) || host == strings.ToLower(mustHostname(s.origin())) {
		if isAPKPureAPIPath(u.EscapedPath()) || isAPKPureDownloadPath(u.EscapedPath()) {
			return nil
		}
		return fmt.Errorf("path not allowed")
	}
	return nil
}

func (s *APKPureSource) trustedHost(host string) bool {
	host = strings.ToLower(host)
	if isAPKPureAPIHost(host) || isAPKPureDownloadHost(host) {
		return true
	}
	if originHost := strings.ToLower(mustHostname(s.origin())); originHost != "" && host == originHost {
		return true
	}
	return false
}

func isAPKPureAPIHost(host string) bool {
	host = strings.ToLower(host)
	return host == "api.pureapk.com"
}

func isAPKPureDownloadHost(host string) bool {
	host = strings.ToLower(host)
	switch host {
	case "download.pureapk.com", "pureapk.com", "apkpure.com", "www.apkpure.com", "winudf.com":
		return true
	}
	return strings.HasSuffix(host, ".pureapk.com") || strings.HasSuffix(host, ".apkpure.com") || strings.HasSuffix(host, ".winudf.com")
}

func isAPKPureAPIPath(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), "/m/v3/")
}

func apkpureNetworkError(err error) error {
	return setupError(ErrNetwork, apkpureOp, "APKPure request failed; the site may be blocking automated downloads", err)
}

func apkpureStatusError(status int) error {
	if status == http.StatusForbidden || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		return setupError(ErrNetwork, apkpureOp, "APKPure blocked or rate-limited the automated download", nil)
	}
	return setupError(ErrNetwork, apkpureOp, fmt.Sprintf("APKPure returned HTTP %d", status), nil)
}

var _ Source = (*APKPureSource)(nil)
