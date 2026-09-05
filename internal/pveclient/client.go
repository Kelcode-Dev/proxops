// Package pveclient is a thin, hand-rolled client for the Proxmox VE JSON
// API. It deliberately avoids third-party PVE libraries so that auth, CSRF,
// task polling and error mapping are fully under the project's control.
//
// Endpoint shape: https://<node>:<port>/api2/json/<path>. Most methods are
// node-scoped; the caller picks the target node per manifest. A few cluster
// endpoints accept the gatewayLabel placeholder and are routed to the
// configured gateway host.
package pveclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultPort is the standard PVE API (pveproxy) port.
const DefaultPort = 8006

// gatewayLabel is the node placeholder accepted by Do() for cluster-wide
// endpoints; it is resolved to the configured gateway host.
const gatewayLabel = "gateway"

// maxRetries bounds transient retry attempts on a single request.
const maxRetries = 3

// ErrReadOnly is returned by a write call while the write circuit breaker is
// open. The client keeps reading PVE state; only mutations are gated.
var ErrReadOnly = errors.New("pve write circuit breaker open: running read-only")

// PVEParams carries PVE connection settings into New (decoupled from the
// config package).
type PVEParams struct {
	User       string // token owner or ticket user, e.g. "root@pam"
	Auth       string // "token" (default) or "ticket"
	TokenID    string
	TokenValue string // composed user@realm!id=uuid
	Token      string
	Password   string
	Gateway    string
	Port       int
	CAFile     string
}

// Options configure a Client.
type Options struct {
	PVE              PVEParams
	HTTPTimeout      time.Duration // bounds a single API request (task waits are separate); default 30s
	BreakerThreshold int           // consecutive write failures before read-only mode; default 20
	BreakerDuration  time.Duration // how long the write circuit stays open; default 10m
	// BaseURL, when set, overrides the per-node https:// base for ALL nodes.
	// Intended for tests pointing at an httptest mock server; production always
	// derives bases from node name + port.
	BaseURL string
}

// Client is a PVE API client. Safe for concurrent use.
type Client struct {
	auth    *Auth
	http    *http.Client
	gateway string
	port    int
	baseURL func(node string) string
	breaker *writeBreaker
	log     *slog.Logger

	mu        sync.Mutex
	lastRead  time.Time
	lastWrite time.Time
}

// New builds a Client. CAFile may be empty (the system trust store is used).
func New(o Options, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	o = *normalizeOptions(&o)

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if o.PVE.CAFile != "" {
		caPEM, err := os.ReadFile(o.PVE.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", o.PVE.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("CA file %s contains no usable certificates", o.PVE.CAFile)
		}
		tlsCfg.RootCAs = pool
	}

	// baseURLFn builds a base URL for a node, honoring Options.BaseURL.
	var baseURLFn func(node string) string
	if o.BaseURL != "" {
		baseURLFn = func(node string) string { return o.BaseURL }
	} else {
		baseURLFn = func(node string) string {
			if node == gatewayLabel {
				node = o.PVE.Gateway
			}
			return fmt.Sprintf("https://%s:%d", node, o.PVE.Port)
		}
	}

	return &Client{
		auth: newAuth(authParams{
			Method:     o.PVE.Auth,
			User:       o.PVE.User,
			TokenID:    o.PVE.TokenID,
			TokenValue: o.PVE.TokenValue,
			Token:      o.PVE.Token,
			Password:   o.PVE.Password,
			Gateway:    o.PVE.Gateway,
			Port:       o.PVE.Port,
		}, baseURLFn),
		http:    &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: o.HTTPTimeout},
		gateway: o.PVE.Gateway,
		port:    o.PVE.Port,
		baseURL: baseURLFn,
		breaker: newWriteBreaker(o.BreakerThreshold, o.BreakerDuration),
		log:     log,
	}, nil
}

func normalizeOptions(o *Options) *Options {
	if o.PVE.Port <= 0 {
		o.PVE.Port = DefaultPort
	}
	if o.PVE.Gateway == "" {
		o.PVE.Gateway = "pve"
	}
	if o.BreakerThreshold <= 0 {
		o.BreakerThreshold = 20
	}
	if o.BreakerDuration <= 0 {
		o.BreakerDuration = 10 * time.Minute
	}
	if o.HTTPTimeout <= 0 {
		o.HTTPTimeout = 30 * time.Second
	}
	return o
}

// URL returns the full API URL for a node and json-api path. gatewayLabel is
// resolved to the gateway host (unless BaseURL is set).
func (c *Client) URL(node, path string) string {
	base := c.baseURL(node)
	return base + "/api2/json/" + strings.TrimLeft(path, "/")
}

// Do performs a PVE API request with retry/backoff. It applies authentication,
// maps PVE error envelopes, and (for POST/DELETE) counts toward the write
// circuit breaker.
//
//   - GET: params go in the query string.
//   - POST/DELETE: params are form-encoded in the body.
//   - out: when non-nil, the response "data" object is decoded into it.
//
// If the response "data" is a JSON string, it is a task UPID and is returned
// as upID (out is left untouched in that case).
func (c *Client) Do(ctx context.Context, method, node, path string, params url.Values, out any) (upID string, err error) {
	if params == nil {
		params = url.Values{}
	}
	isWrite := method == http.MethodPost || method == http.MethodDelete
	if isWrite && !c.breaker.allows() {
		return "", fmt.Errorf("%w: %s %s on %s", ErrReadOnly, method, path, node)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(c.backoffDelay(attempt)):
			}
		}

		var (
			reqBody []byte
			u       *url.URL
			dErr    error
		)
		if method == http.MethodGet {
			u, dErr = url.Parse(c.URL(node, path) + "?" + params.Encode())
		} else {
			u, dErr = url.Parse(c.URL(node, path))
			reqBody = []byte(params.Encode())
		}
		if dErr != nil {
			return "", fmt.Errorf("bad url for %s %s: %w", method, path, dErr)
		}

		var body io.Reader
		if len(reqBody) > 0 {
			body = bytes.NewReader(reqBody)
		}
		req, dErr := http.NewRequestWithContext(ctx, method, u.String(), body)
		if dErr != nil {
			return "", dErr
		}
		if len(reqBody) > 0 {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")

		if err := c.auth.applyAuth(ctx, c, req); err != nil {
			return "", err
		}

		resp, wErr := c.http.Do(req)
		if wErr != nil {
			c.registerFailure(isWrite)
			lastErr = wErr
			c.log.Debug("pve request transient error", "method", method, "node", node, "path", path, "err", wErr.Error(), "attempt", attempt)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			upID, err = c.parseEnvelope(respBody, out)
			c.registerSuccess(isWrite)
			if err != nil {
				return "", err
			}
			if upID != "" {
				c.log.Debug("pve task started", "upID", upID, "node", node, "path", path)
			}
			return upID, nil

		case resp.StatusCode == http.StatusUnauthorized && c.auth.Method() == "ticket":
			c.log.Warn("pve ticket rejected, re-authenticating", "method", method, "node", node, "path", path)
			c.auth.invalidate()
			lastErr = &APIError{StatusCode: resp.StatusCode, Message: "unauthorized", Target: path}
			c.registerFailure(isWrite)
			continue

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			// PVE signals missing resources with HTTP 500 + "no such ..."
			// (e.g. "no such vm 100", "no such task ..."). Those are semantic,
			// not transient — fail fast without retrying.
			ae := &APIError{StatusCode: resp.StatusCode, Message: errStringFromEnvelope(respBody), Target: path}
			if IsNotFound(ae) {
				c.registerFailure(isWrite)
				return "", ae
			}
			c.registerFailure(isWrite)
			lastErr = ae
			c.log.Debug("pve server error", "method", method, "node", node, "path", path, "status", resp.StatusCode, "msg", ae.Message, "attempt", attempt)
			continue

		default:
			ae := &APIError{StatusCode: resp.StatusCode, Message: errStringFromEnvelope(respBody), Target: path}
			if ae.Message == "" {
				ae.Message = strings.TrimSpace(string(respBody))
			}
			c.registerFailure(isWrite)
			return "", ae
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("pve %s %s: unknown failure after retries", method, path)
	}
	return "", lastErr
}

// backoffDelay computes delay before retry number n (1-based):
// 1s → 2s → 4s with ±20% jitter, capped at 30s.
func (c *Client) backoffDelay(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return time.Duration(float64(d) * jitterFactor())
}

// registerSuccess updates breaker and timestamps.
func (c *Client) registerSuccess(isWrite bool) {
	now := time.Now()
	c.breaker.recordOK()
	c.mu.Lock()
	c.lastRead = now
	if isWrite {
		c.lastWrite = now
	}
	c.mu.Unlock()
}

// registerFailure records a write failure against the circuit breaker.
func (c *Client) registerFailure(isWrite bool) {
	if isWrite {
		c.breaker.recordFail()
	}
}

func (c *Client) parseEnvelope(body []byte, out any) (upID string, err error) {
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("decode pve response: %w (body %s)", err, truncate(string(body), 120))
	}
	// "data" is either a JSON string (a task UPID) or an object/array.
	var s string
	if json.Unmarshal(envelope.Data, &s) == nil {
		return s, nil
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return "", fmt.Errorf("decode pve data: %w", err)
		}
	}
	return "", nil
}

func errStringFromEnvelope(body []byte) string {
	var envelope struct {
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Errors) > 0 {
		var msg string
		if json.Unmarshal(envelope.Errors, &msg) == nil {
			return msg
		}
	}
	return strings.TrimSpace(string(body))
}

// ClusterResources returns the full cluster resource list via the gateway.
// Each entry is a raw map carrying type, vmid, node, status, etc.
func (c *Client) ClusterResources(ctx context.Context) ([]map[string]any, error) {
	var out []map[string]any
	if _, err := c.Do(ctx, http.MethodGet, gatewayLabel, "/cluster/resources", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PveVersion returns the PVE major version as reported by /version.
func (c *Client) PveVersion(ctx context.Context) (int, error) {
	var out int
	if _, err := c.Do(ctx, http.MethodGet, gatewayLabel, "/version", nil, &out); err != nil {
		return 0, err
	}
	return out, nil
}

// Gateway returns the configured gateway node name.
func (c *Client) Gateway() string { return c.gateway }

// Port returns the configured PVE API port.
func (c *Client) Port() int { return c.port }

// httpClient returns the underlying http.Client (used by Auth for ticket exchange).
func (c *Client) httpClient() *http.Client { return c.http }

// LastReadAt returns the time of the last successful PVE read.
func (c *Client) LastReadAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastRead
}

// LastWriteAt returns the time of the last successful PVE write.
func (c *Client) LastWriteAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastWrite
}

// Readiness reports whether the client has successfully read PVE recently
// (within the given window) — used by /healthz.
func (c *Client) Readiness(window time.Duration) bool {
	last := c.LastReadAt()
	return !last.IsZero() && time.Since(last) < window
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
