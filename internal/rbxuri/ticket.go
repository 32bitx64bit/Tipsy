// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package rbxuri

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

const (
	authTicketRedeemURL = "https://auth.roblox.com/v1/authentication-ticket/redeem"
	authOrigin          = "https://www.roblox.com/"
	redeemUserAgent     = "Roblox/WinInet"
)

var (
	errTicketRedeem = errors.New("official authentication ticket could not be redeemed")
	errTicketHTTP   = errors.New("official authentication ticket request failed")
)

// TicketClient is the HTTP surface used to redeem a website authentication
// ticket. Tests substitute it; production uses a short-timeout client.
type TicketClient interface {
	Do(*http.Request) (*http.Response, error)
}

// RedeemResult is the official Set-Cookie list from ticket redemption.
// Callers must persist it through the private cookie store and must not log it.
type RedeemResult struct {
	Origin    string
	SetCookie []string
}

// RedeemError is a failed redeem. It reports HTTP status and Roblox error
// code only — never the ticket, cookie, CSRF, or response body.
type RedeemError struct {
	Status int
	Code   int
}

func (e RedeemError) Error() string {
	if e.Status == 0 && e.Code == 0 {
		return errTicketRedeem.Error()
	}
	if e.Code != 0 {
		return fmt.Sprintf("official authentication ticket could not be redeemed (http %d code %d)", e.Status, e.Code)
	}
	return fmt.Sprintf("official authentication ticket could not be redeemed (http %d)", e.Status)
}

func (e RedeemError) Unwrap() error { return errTicketRedeem }

type redeemRequestBody struct {
	AuthenticationTicket string `json:"authenticationTicket"`
}

type redeemErrorBody struct {
	Errors []struct {
		Code int `json:"code"`
	} `json:"errors"`
}

// RedeemAuthenticationTicket exchanges a one-time website ticket for the
// official session cookies. The ticket value never appears in returned errors.
func RedeemAuthenticationTicket(ctx context.Context, client TicketClient, ticket string) (RedeemResult, error) {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return RedeemResult{}, errTicketRedeem
	}
	if client == nil {
		client = newTicketHTTPClient()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(redeemRequestBody{AuthenticationTicket: ticket})
	if err != nil {
		return RedeemResult{}, errTicketHTTP
	}
	csrf := ""
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, authTicketRedeemURL, bytes.NewReader(body))
		if err != nil {
			return RedeemResult{}, errTicketHTTP
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", strings.TrimRight(authOrigin, "/"))
		req.Header.Set("Referer", authOrigin)
		req.Header.Set("User-Agent", redeemUserAgent)
		req.Header.Set("RBXAuthenticationNegotiation", "1")
		req.Header.Set("RBX-Authentication-Ticket", ticket)
		if csrf != "" {
			req.Header.Set("X-CSRF-TOKEN", csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			return RedeemResult{}, errTicketHTTP
		}
		limited := io.LimitReader(resp.Body, 1<<20)
		payload, _ := io.ReadAll(limited)
		_ = resp.Body.Close()
		code := redeemAPICode(payload)
		if resp.StatusCode == http.StatusForbidden && csrf == "" {
			if token := strings.TrimSpace(resp.Header.Get("X-CSRF-TOKEN")); token != "" {
				csrf = token
				continue
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return RedeemResult{}, RedeemError{Status: resp.StatusCode, Code: code}
		}
		cookies := resp.Header.Values("Set-Cookie")
		if len(cookies) == 0 {
			return RedeemResult{}, RedeemError{Status: resp.StatusCode, Code: code}
		}
		return RedeemResult{Origin: authOrigin, SetCookie: cookies}, nil
	}
	return RedeemResult{}, errTicketRedeem
}

func newTicketHTTPClient() TicketClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return &http.Client{Timeout: 15 * time.Second}
	}
	return &http.Client{Timeout: 15 * time.Second, Jar: jar}
}

func redeemAPICode(payload []byte) int {
	var body redeemErrorBody
	if err := json.Unmarshal(payload, &body); err != nil {
		return 0
	}
	if len(body.Errors) == 0 {
		return 0
	}
	return body.Errors[0].Code
}
