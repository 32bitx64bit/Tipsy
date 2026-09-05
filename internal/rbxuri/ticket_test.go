// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package rbxuri

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestRedeemAuthenticationTicketCSRFThenCookies(t *testing.T) {
	t.Parallel()
	var sawCSRF bool
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != authTicketRedeemURL {
			t.Fatalf("url=%s", req.URL)
		}
		if req.Header.Get("RBX-Authentication-Ticket") != "SYNTHETIC-TICKET" {
			t.Fatal("missing official ticket header")
		}
		if req.Header.Get("RBXAuthenticationNegotiation") != "1" {
			t.Fatal("missing RBXAuthenticationNegotiation")
		}
		payload, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(payload), `"authenticationTicket":"SYNTHETIC-TICKET"`) {
			t.Fatalf("json body missing ticket field")
		}
		if req.Header.Get("X-CSRF-TOKEN") == "" {
			forbidden := make(http.Header)
			forbidden.Set("X-CSRF-TOKEN", "synthetic-csrf")
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     forbidden,
				Body:       io.NopCloser(strings.NewReader(`{"errors":[]}`)),
				Request:    req,
			}, nil
		}
		sawCSRF = req.Header.Get("X-CSRF-TOKEN") == "synthetic-csrf"
		okHeader := make(http.Header)
		okHeader.Add("Set-Cookie", ".ROBLOSECURITY=SYNTHETIC-COOKIE; Domain=.roblox.com; Path=/; Secure; HttpOnly")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     okHeader,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	})
	got, err := RedeemAuthenticationTicket(context.Background(), client, "SYNTHETIC-TICKET")
	if err != nil {
		t.Fatal(err)
	}
	if !sawCSRF {
		t.Fatal("CSRF retry was skipped")
	}
	if got.Origin != authOrigin || len(got.SetCookie) != 1 || !strings.Contains(got.SetCookie[0], "SYNTHETIC-COOKIE") {
		t.Fatalf("result=%+v", got)
	}
}

func TestRedeemAuthenticationTicketFailuresDoNotEchoTicket(t *testing.T) {
	t.Parallel()
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"errors":[{"code":4,"message":"Authentication ticket was invalid."}]}`)),
			Request:    req,
		}, nil
	})
	_, err := RedeemAuthenticationTicket(context.Background(), client, "SUPER-SECRET-TICKET")
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "SUPER-SECRET-TICKET") {
		t.Fatalf("error leaked ticket: %v", err)
	}
	if !strings.Contains(err.Error(), "http 403") || !strings.Contains(err.Error(), "code 4") {
		t.Fatalf("error=%v", err)
	}
}
