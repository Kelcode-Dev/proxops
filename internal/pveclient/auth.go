package pveclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const ticketRefreshSafetyMargin = 5 * time.Minute

// Auth implements PVE API authentication. Two methods are supported:
//
//  1. token (default) — PVE API token, sent on every request as
//     Authorization: PVEAPIToken=<user>@<realm>!<id>=<uuid>. No CSRF handling,
//     no expiry. This is the recommended path.
//  2. ticket — user/password exchanged for a session ticket via
//     POST /access/ticket. Subsequent requests carry PVEAuthCookie/X-Userid
//     cookies and an X-CSRF-Token header. Tickets expire (PVE auth timeout);
//     the client proactively refreshes and re-exchanges on 401.
//
// NOTE (M1 live-host verification): exact cookie names and whether PVEAPIToken
// requests still need X-CSRF-Token should be confirmed on a real PVE. The token
// path deliberately sends no CSRF header.
type Auth struct {
	method     string // "token" | "ticket"
	user       string
	tokenID    string
	tokenValue string // fully-composed user@realm!id=uuid
	token      string // raw uuid; composed with user+tokenID
	password   string
	gateway    string
	port       int

	// apiBase returns the https base for a node (no trailing /api2/json),
	// honoring the client's BaseURL override when set.
	apiBase func(node string) string

	refreshAfter time.Duration

	mu           sync.Mutex
	ticket       string
	csrfToken    string
	userIdCookie string
	fetchedAt    time.Time
}

// authParams bundles the auth-relevant config fields.
type authParams struct {
	Method     string
	User       string
	TokenID    string
	TokenValue string
	Token      string
	Password   string
	Gateway    string
	Port       int
}

func newAuth(p authParams, apiBase func(node string) string) *Auth {
	if p.Gateway == "" {
		p.Gateway = "pve"
	}
	if p.Method == "" {
		p.Method = "token"
	}
	if apiBase == nil {
		apiBase = func(node string) string { return node }
	}
	return &Auth{
		method:       p.Method,
		user:         p.User,
		tokenID:      p.TokenID,
		tokenValue:   p.TokenValue,
		token:        p.Token,
		password:     p.Password,
		gateway:      p.Gateway,
		port:         p.Port,
		apiBase:      apiBase,
		refreshAfter: ticketRefreshSafetyMargin,
	}
}

// Method returns the active auth method.
func (a *Auth) Method() string { return a.method }

// gatewayHost is the hostname:port the gateway is reached on.
func (a *Auth) gatewayHost() string {
	if a.port > 0 {
		return fmt.Sprintf("%s:%d", a.gateway, a.port)
	}
	return a.gateway
}

// ticketEndpoint builds the /access/ticket URL via the client's base-URL logic.
func (a *Auth) ticketEndpoint() string {
	return a.apiBase(a.gateway) + "/api2/json/access/ticket"
}

// tokenAuthValue composes the PVEAPIToken credential.
func (a *Auth) tokenAuthValue() string {
	if a.tokenValue != "" {
		return a.tokenValue
	}
	if a.user == "" || a.tokenID == "" || a.token == "" {
		return ""
	}
	return fmt.Sprintf("%s!%s=%s", a.user, a.tokenID, a.token)
}

// applyAuth decorates an outgoing request with authentication material. For
// ticket auth it lazily fetches (or refreshes) the session ticket. It mutates
// nothing shared except the ticket state, guarded by the internal mutex.
func (a *Auth) applyAuth(ctx context.Context, c *Client, req *http.Request) error {
	if a.method == "token" {
		cred := a.tokenAuthValue()
		if cred == "" {
			return errors.New("pve token auth is not configured (need pve.user + pve.token-id + pve.token or a composed pve.token-value)")
		}
		req.Header.Set("Authorization", "PVEAPIToken="+cred)
		return nil
	}
	// ticket auth: ensure a live ticket, then stamp cookies.
	a.mu.Lock()
	if a.ticket == "" || time.Since(a.fetchedAt) > a.refreshAfter {
		a.mu.Unlock()
		if err := a.fetchTicket(ctx, c); err != nil {
			return err
		}
		a.mu.Lock()
	}
	req.Header.Set("Cookie", "PVEAuthCookie="+a.ticket+"; X-Userid="+a.userIdCookie)
	if a.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", a.csrfToken)
	}
	a.mu.Unlock()
	return nil
}

// invalidate clears a bad ticket so the next request re-exchanges it.
func (a *Auth) invalidate() {
	a.mu.Lock()
	a.ticket = ""
	a.csrfToken = ""
	a.userIdCookie = ""
	a.fetchedAt = time.Time{}
	a.mu.Unlock()
}

// ticketData mirrors the PVE /access/ticket response body:
//
//	{"data": {"CSRFToken": "...", "ticket": "..."}}
type ticketData struct {
	Ticket    string `json:"ticket"`
	CSRFToken string `json:"CSRFToken"`
}

// fetchTicket exchanges user/password for a session ticket on the gateway node.
func (a *Auth) fetchTicket(ctx context.Context, c *Client) error {
	form := url.Values{"user": {a.user}, "password": {a.password}}
	endpoint := a.ticketEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("ticket exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return &APIError{
			StatusCode: resp.StatusCode,
			Message:    "ticket exchange failed: " + msg,
			Target:     "access/ticket",
		}
	}

	var envelope struct {
		Data ticketData `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("ticket exchange: decode response: %w", err)
	}
	if envelope.Data.Ticket == "" {
		return errors.New("ticket exchange: pve returned an empty ticket")
	}

	userIdCookie := url.QueryEscape(a.user)
	if uid := resp.Header.Get("Set-X-Userid"); uid != "" {
		userIdCookie = url.QueryEscape(uid)
	}

	a.mu.Lock()
	a.ticket = envelope.Data.Ticket
	a.csrfToken = envelope.Data.CSRFToken // may be empty in some PVE versions
	a.userIdCookie = userIdCookie
	a.fetchedAt = time.Now()
	a.mu.Unlock()
	return nil
}
