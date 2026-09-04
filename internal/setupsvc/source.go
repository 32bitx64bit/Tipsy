// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Source interface {
	Availability(context.Context) SourceAvailability
	Acquire(context.Context, string, ProgressFunc) ([]string, error)
}

type UnavailableSource struct {
	Name     string
	Reason   string
	LegalURL string
}

func (s UnavailableSource) Availability(context.Context) SourceAvailability {
	reason := s.Reason
	if reason == "" {
		reason = "No official public direct APK source is available; choose APK files obtained through your own authorized account."
	}
	return SourceAvailability{Available: false, Name: s.Name, Reason: reason, LegalURL: s.LegalURL}
}

func (s UnavailableSource) Acquire(context.Context, string, ProgressFunc) ([]string, error) {
	a := s.Availability(context.Background())
	return nil, setupError(ErrSourceUnavailable, "automatic setup", a.Reason, nil)
}

type RemoteArtifact struct {
	Name   string
	URL    string
	SHA256 string
	Size   int64
}

// HTTPSBundleSource is a future-source primitive, not Tipsy's default source.
// Callers must supply a lawful manifest with exact hashes and trusted hosts.
type HTTPSBundleSource struct {
	Name         string
	LegalURL     string
	Artifacts    []RemoteArtifact
	AllowedHosts []string
	Client       *http.Client
	MaxBytes     int64
}

func (s *HTTPSBundleSource) Availability(context.Context) SourceAvailability {
	if s == nil || len(s.Artifacts) == 0 || len(s.AllowedHosts) == 0 || strings.TrimSpace(s.LegalURL) == "" {
		return SourceAvailability{Available: false, Reason: "No trusted automatic package source is configured."}
	}
	return SourceAvailability{Available: true, Name: s.Name, LegalURL: s.LegalURL}
}

func (s *HTTPSBundleSource) Acquire(ctx context.Context, dir string, progress ProgressFunc) ([]string, error) {
	if err := checkContext(ctx, "download"); err != nil {
		return nil, err
	}
	if !s.Availability(ctx).Available {
		return nil, setupError(ErrSourceUnavailable, "download", "No trusted automatic package source is configured.", nil)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, "download", "cannot create the private download directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	allowed := make(map[string]struct{}, len(s.AllowedHosts))
	for _, host := range s.AllowedHosts {
		allowed[strings.ToLower(strings.TrimSpace(host))] = struct{}{}
	}
	client := *http.DefaultClient
	if s.Client != nil {
		client = *s.Client
	}
	priorRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if err := trustedURL(req.URL, allowed); err != nil {
			return err
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	max := s.MaxBytes
	if max <= 0 {
		max = DefaultLimits().MaxFileBytes
	}
	var total int64
	for _, a := range s.Artifacts {
		if a.Size <= 0 || a.Size > max {
			return nil, setupError(ErrSizeLimit, "download", "source artifact has an invalid declared size", nil)
		}
		total += a.Size
	}
	var paths []string
	succeeded := false
	defer func() {
		if !succeeded {
			for _, path := range paths {
				_ = os.Remove(path)
			}
		}
	}()
	var done int64
	for _, a := range s.Artifacts {
		u, err := url.Parse(a.URL)
		if err != nil || trustedURL(u, allowed) != nil {
			return nil, setupError(ErrSourceTrust, "download", "source URL is outside the trusted HTTPS hosts", err)
		}
		name := filepath.Base(a.Name)
		if name == "" || name == "." || name != a.Name || !supportedExtension(name) {
			return nil, setupError(ErrInvalidRequest, "download", "source artifact name is invalid", nil)
		}
		wantHash, err := hex.DecodeString(a.SHA256)
		if err != nil || len(wantHash) != sha256.Size {
			return nil, setupError(ErrSourceTrust, "download", "source artifact SHA-256 is invalid", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, setupError(ErrInvalidRequest, "download", "cannot create source request", err)
		}
		req.Header.Set("Accept", "application/vnd.android.package-archive, application/zip")
		resp, err := client.Do(req)
		if err != nil {
			return nil, setupError(ErrNetwork, "download", "trusted source request failed", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, setupError(ErrNetwork, "download", fmt.Sprintf("trusted source returned HTTP %d", resp.StatusCode), nil)
		}
		if resp.ContentLength < 0 || resp.ContentLength != a.Size || resp.ContentLength > max {
			resp.Body.Close()
			return nil, setupError(ErrSizeLimit, "download", "source content length does not match the signed manifest", nil)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			resp.Body.Close()
			return nil, setupError(ErrInstall, "download", "cannot create a private temporary package", err)
		}
		h := sha256.New()
		reader := &progressReader{ctx: ctx, r: io.LimitReader(resp.Body, a.Size+1), onRead: func(n int64) {
			done += n
			report(progress, PhaseAcquiring, done, total, "Downloading verified package data")
		}}
		n, copyErr := io.Copy(io.MultiWriter(f, h), reader)
		syncErr := f.Sync()
		closeErr := f.Close()
		resp.Body.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if copyErr == nil {
				copyErr = syncErr
			}
			if copyErr == nil {
				copyErr = closeErr
			}
			return nil, setupError(ErrNetwork, "download", "package transfer did not complete", copyErr)
		}
		if n != a.Size || !equalBytes(h.Sum(nil), wantHash) {
			_ = os.Remove(path)
			return nil, setupError(ErrIntegrity, "download", "package size or SHA-256 did not match the trusted manifest", nil)
		}
		paths = append(paths, path)
	}
	succeeded = true
	return paths, nil
}

func trustedURL(u *url.URL, allowed map[string]struct{}) error {
	if u == nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("HTTPS URL without credentials required")
	}
	if _, ok := allowed[strings.ToLower(u.Hostname())]; !ok {
		return fmt.Errorf("host not trusted")
	}
	return nil
}

type progressReader struct {
	ctx    context.Context
	r      io.Reader
	onRead func(int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n > 0 && r.onRead != nil {
		r.onRead(int64(n))
	}
	return n, err
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
