// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type storedAuthCookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	HostOnly bool
	Secure   bool
	HTTPOnly bool
	Expires  time.Time
	Created  uint64
}
type authCookieDisk struct {
	Version int
	Cookies []storedAuthCookie
}
type authCookieStore struct {
	path    string
	base    *url.URL
	domain  string
	cookies []storedAuthCookie
	next    uint64
	now     func() time.Time
}

// This is the CookieManager adapter for the official Roblox origin. Persisted
// values stay opaque. Only cookie routing metadata is interpreted, and only
// cookies scoped to the official site's registrable domain are accepted; a
// parent/public-suffix cookie can never reach the native restore boundary.
func openAuthCookieStore(path, baseURL string) (*authCookieStore, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" {
		return nil, errors.New("invalid cookie origin")
	}
	host := strings.ToLower(base.Hostname())
	domain := strings.TrimPrefix(host, "www.")
	if !strings.Contains(domain, ".") {
		return nil, errors.New("invalid cookie origin")
	}
	s := &authCookieStore{path: path, base: base, domain: domain, now: time.Now}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("cookie directory unavailable")
	}
	if err = os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, errors.New("cookie directory unavailable")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("cookie storage unavailable")
	}
	f := os.NewFile(uintptr(fd), "private-cookie-store")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, errors.New("invalid cookie storage")
	}
	if err = f.Chmod(0o600); err != nil {
		return nil, errors.New("cookie storage unavailable")
	}
	var disk authCookieDisk
	dec := json.NewDecoder(io.LimitReader(f, 8<<20))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&disk); err != nil || disk.Version != 1 {
		return nil, errors.New("invalid cookie storage")
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid cookie storage")
	}
	for _, c := range disk.Cookies {
		if !s.valid(c) {
			return nil, errors.New("invalid cookie storage")
		}
		if !c.Expires.IsZero() && !c.Expires.After(s.now()) {
			continue
		}
		s.cookies = append(s.cookies, c)
		if c.Created >= s.next {
			s.next = c.Created + 1
		}
	}
	return s, nil
}

func domainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
func (s *authCookieStore) valid(c storedAuthCookie) bool {
	return c.Name != "" && c.Domain != "" && domainMatches(c.Domain, s.domain) && c.Path != "" && c.Path[0] == '/' &&
		(&http.Cookie{Name: c.Name, Value: c.Value}).Valid() == nil
}
func cookiePathMatches(request, cookie string) bool {
	if request == cookie {
		return true
	}
	return strings.HasPrefix(request, cookie) && (strings.HasSuffix(cookie, "/") || len(request) > len(cookie) && request[len(cookie)] == '/')
}
func cookieDefaultPath(path string) string {
	if path == "" || path[0] != '/' {
		return "/"
	}
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "/"
}
func (s *authCookieStore) header() string {
	now := s.now()
	host := strings.ToLower(s.base.Hostname())
	path := s.base.EscapedPath()
	if path == "" {
		path = "/"
	}
	var list []storedAuthCookie
	for _, c := range s.cookies {
		if !c.Expires.IsZero() && !c.Expires.After(now) || c.Secure && s.base.Scheme != "https" {
			continue
		}
		if c.HostOnly && c.Domain != host || !domainMatches(host, c.Domain) || !cookiePathMatches(path, c.Path) {
			continue
		}
		list = append(list, c)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if len(list[i].Path) != len(list[j].Path) {
			return len(list[i].Path) > len(list[j].Path)
		}
		return list[i].Created < list[j].Created
	})
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, (&http.Cookie{Name: c.Name, Value: c.Value}).String())
	}
	return strings.Join(out, "; ")
}
func (s *authCookieStore) set(rawURL string, headers []string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil // This adapter only bridges the official HTTPS origin.
	}
	host := strings.ToLower(u.Hostname())
	// Other origins are outside this single official client cookie bridge.
	if !domainMatches(host, s.domain) {
		return nil
	}
	now := s.now()
	next := append([]storedAuthCookie(nil), s.cookies...)
	changed := false
	for _, header := range headers {
		parsed, err := http.ParseSetCookie(header)
		if err != nil {
			continue
		}
		domain := strings.ToLower(strings.TrimPrefix(parsed.Domain, "."))
		hostOnly := domain == ""
		if hostOnly {
			domain = host
		}
		if !domainMatches(host, domain) || !domainMatches(domain, s.domain) || strings.HasSuffix(domain, ".") {
			continue
		}
		if parsed.Secure && u.Scheme != "https" {
			continue
		}
		path := parsed.Path
		if path == "" || path[0] != '/' {
			path = cookieDefaultPath(u.EscapedPath())
		}
		if strings.HasPrefix(parsed.Name, "__Secure-") && (!parsed.Secure || u.Scheme != "https") {
			continue
		}
		if strings.HasPrefix(parsed.Name, "__Host-") && (!parsed.Secure || !hostOnly || path != "/" || parsed.Path != "/" || u.Scheme != "https") {
			continue
		}
		c := storedAuthCookie{Name: parsed.Name, Value: parsed.Value, Domain: domain, Path: path, HostOnly: hostOnly, Secure: parsed.Secure, HTTPOnly: parsed.HttpOnly, Expires: parsed.Expires, Created: s.next}
		if !s.valid(c) {
			continue
		}
		if parsed.MaxAge > 0 {
			c.Expires = now.Add(time.Duration(min(parsed.MaxAge, 2147483647)) * time.Second)
		}
		remove := parsed.MaxAge < 0 || !c.Expires.IsZero() && !c.Expires.After(now)
		found := -1
		for i, v := range next {
			if v.Name == c.Name && v.Domain == c.Domain && v.Path == c.Path {
				found = i
				c.Created = v.Created
				break
			}
		}
		if found >= 0 {
			next = append(next[:found], next[found+1:]...)
			changed = true
		}
		if !remove {
			next = append(next, c)
			s.next++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	// Drop expired records before serializing, including explicit logout deletes.
	kept := next[:0]
	for _, c := range next {
		if c.Expires.IsZero() || c.Expires.After(now) {
			kept = append(kept, c)
		}
	}
	if err = s.write(kept); err != nil {
		return err
	}
	s.cookies = kept
	return nil
}
func (s *authCookieStore) write(cookies []storedAuthCookie) error {
	data, err := json.Marshal(authCookieDisk{Version: 1, Cookies: cookies})
	if err != nil {
		return errors.New("cookie serialization failed")
	}
	if info, err := os.Lstat(s.path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("invalid cookie storage")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cookie storage unavailable")
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".tipsy-atomic-cookies-")
	if err != nil {
		return errors.New("cookie storage unavailable")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("cookie storage write failed")
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return errors.New("cookie storage replace failed")
	}
	d, err := os.Open(dir)
	if err != nil {
		return errors.New("cookie directory unavailable")
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return errors.New("cookie directory sync failed")
	}
	return nil
}
