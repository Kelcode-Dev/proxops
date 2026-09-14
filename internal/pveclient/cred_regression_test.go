// Regression tests for pveclient's credential-handling surface. These pin
// the WIRE behavior: the composed PVEAPIToken header actually reaches PVE,
// AND pveclient.New errors cleanly (no header-silent call) when
// PVEParams are under-specified.
//
// The historical bug that this file's sibling (app_test.go) covers was:
// a TokenID being dropped in the config->client wiring. From
// pveclient's own POV, that bug should have been caught if we were
// testing pveclient.New directly against real PVE. This file does that,
// with a recording HTTP server instead of the full stateful mock.
package pveclient_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
)

// recordingServer returns an httptest.Server that:
//   - records every request to a slice,
//   - serves a PVE-shaped "cluster/resources" GET for path
//     /api2/json/cluster/resources (empty array + 200),
//   - serves a PVE-shaped /api2/json/access/ticket for POST, returning
//     a synthetic ticket (for the ticket-auth tests),
//   - always records the incoming Authorization / Cookie / X-CSRF-Token
//     headers into .Req so the test can assert what pveclient actually put
//     on the wire.
func recordingServer(t *testing.T, records *[]recordedReq) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		rec := recordedReq{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Cookie: r.Header.Get("Cookie"),
			CSRF:   r.Header.Get("X-CSRF-Token"),
		}
		body, _ := io.ReadAll(r.Body)
		rec.Body = string(body)
		*records = append(*records, rec)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	mux.HandleFunc("/api2/json/access/ticket", func(w http.ResponseWriter, r *http.Request) {
		rec := recordedReq{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Cookie: r.Header.Get("Cookie"),
			CSRF:   r.Header.Get("X-CSRF-Token"),
		}
		body, _ := io.ReadAll(r.Body)
		rec.Body = string(body)
		*records = append(*records, rec)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-X-Userid", "root@pam")
		_, _ = w.Write([]byte(`{"data":{"ticket":"synthetic","CSRFToken":"syn"}}`))
	})
	return httptest.NewServer(mux)
}

type recordedReq struct {
	Method, Path, Auth, Cookie, CSRF, Body string
}

// TestTokenCredentialReachesWire — when user+tokenID+token are supplied,
// the composed PVEAPIToken header reaches PVE on the first read call.
func TestTokenCredentialReachesWire(t *testing.T) {
	var records []recordedReq
	srv := recordingServer(t, &records)
	defer srv.Close()

	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:    "root@pam",
			Auth:    "token",
			TokenID: "proxops",
			Token:   "deadbeef00",
		},
		BaseURL:     srv.URL,
		HTTPTimeout: srvTimeout(),
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.ClusterResources(ctx); err != nil {
		t.Fatalf("ClusterResources: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("no recorded request")
	}
	got := records[0].Auth
	want := "PVEAPIToken=root@pam!proxops=deadbeef00"
	if got != want {
		t.Fatalf("Authorization header = %q, want %q", got, want)
	}
}

// TestTokenCredentialValueOverrideReachesWire — TokenValue is used verbatim
// when set; the raw user/token-id/token triple is ignored.
func TestTokenCredentialValueOverrideReachesWire(t *testing.T) {
	var records []recordedReq
	srv := recordingServer(t, &records)
	defer srv.Close()

	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:       "ignored@pam",
			Auth:       "token",
			TokenID:    "ignored-id",
			Token:      "ignored-token",
			TokenValue: "composed@pam!ci=v1",
		},
		BaseURL:     srv.URL,
		HTTPTimeout: srvTimeout(),
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClusterResources(context.Background()); err != nil {
		t.Fatalf("ClusterResources: %v", err)
	}
	got := records[0].Auth
	want := "PVEAPIToken=composed@pam!ci=v1"
	if got != want {
		t.Fatalf("Authorization header = %q, want %q", got, want)
	}
}

// TestTokenUnderSpecFails — with only user + token-id (no token uuid),
// pveclient MUST fail the request cleanly with the not-configured error,
// NOT proceed without an auth header.
func TestTokenUnderSpecFails(t *testing.T) {
	var records []recordedReq
	srv := recordingServer(t, &records)
	defer srv.Close()

	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:    "root@pam",
			Auth:    "token",
			TokenID: "proxops",
			// no Token
		},
		BaseURL:     srv.URL,
		HTTPTimeout: srvTimeout(),
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ClusterResources(context.Background())
	if err == nil {
		t.Fatal("expected 'not configured' error, got nil")
	}
	if !strings.Contains(err.Error(), "pve token auth is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 requests to the wire, got %d: %+v", len(records), records)
	}
}

// TestTicketAuthReachesWire — with user + password under auth=ticket,
// pveclient exchanges for a ticket, then the cluster/resources call carries
// the PVEAuthCookie. The initial exchange should NOT carry PVEAPIToken.
func TestTicketAuthReachesWire(t *testing.T) {
	var records []recordedReq
	srv := recordingServer(t, &records)
	defer srv.Close()

	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:     "root@pam",
			Auth:     "ticket",
			Password: "secret-pass",
		},
		BaseURL:     srv.URL,
		HTTPTimeout: srvTimeout(),
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClusterResources(context.Background()); err != nil {
		t.Fatalf("ClusterResources: %v", err)
	}

	if len(records) < 2 {
		t.Fatalf("expected at least 2 requests (ticket + resources), got %d", len(records))
	}
	// The first request should be the ticket exchange.
	tk := records[0]
	if !strings.Contains(tk.Path, "access/ticket") {
		t.Fatalf("first request = %q, want access/ticket", tk.Path)
	}
	if tk.Auth != "" {
		t.Errorf("ticket exchange should not carry PVEAPIToken; got %q", tk.Auth)
	}
	if !strings.Contains(tk.Body, "password=secret") {
		t.Errorf("ticket exchange body missing password form: %q", tk.Body)
	}
	// The subsequent request should carry the PVEAuthCookie.
	for _, r := range records[1:] {
		if strings.Contains(r.Path, "cluster/resources") {
			if !strings.Contains(r.Cookie, "PVEAuthCookie=synthetic") {
				t.Errorf("ClusterResources cookie = %q, want PVEAuthCookie=synthetic", r.Cookie)
			}
			if r.Auth != "" {
				t.Errorf("post-ticket request should not carry PVEAPIToken; got %q", r.Auth)
			}
			return
		}
	}
	t.Fatalf("no cluster/resources request recorded; got %+v", records)
}

// TestNoCredentialInErrorText — the pveclient's error surfaces MUST NOT
// include the raw PVE token / password, even on failure.
func TestNoCredentialInErrorText(t *testing.T) {
	secretToken := "secret-uuid-a1b2"
	secretPassword := "secret-pass-c3d4"

	// Case 1: under-specified token (auth=ticket but token set). pveclient
	// should fail cleanly without echoing either.
	{
		var records []recordedReq
		srv := recordingServer(t, &records)
		defer srv.Close()
		c, err := pveclient.New(pveclient.Options{
			PVE: pveclient.PVEParams{
				User:     "root@pam",
				Auth:     "ticket",
				Password: secretPassword,
				Token:    secretToken,
				TokenID:  "id",
			},
			BaseURL:     srv.URL,
			HTTPTimeout: srvTimeout(),
		}, slog.Default())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, cErr := c.PveVersion(context.Background())
		if cErr == nil {
			t.Fatal("expected error, got nil")
		}
		// The version endpoint 404s; the APIError should carry just the
		// 404 + path, not the secrets.
		for _, s := range []string{secretToken, secretPassword} {
			if strings.Contains(cErr.Error(), s) {
				t.Errorf("PveVersion error leaked secret %q: %v", s, cErr)
			}
		}
	}
}

func srvTimeout() time.Duration { return 0 } // 0 → pveclient.New defaults to 30s

// TestURLRoutesNodeNameThroughSingleHost — the exact property that broke
// against conformance-dev: a PVE node name is NOT necessarily a resolvable
// DNS hostname. The client must fix the host from base-url and put the
// node name ONLY in the path.
func TestURLRoutesNodeNameThroughSingleHost(t *testing.T) {
	// A node name that is NOT a valid host (no DNS, no FQDN).
	nonFQDNNode := "pve-dev-01"
	// A cluster FQDN that IS the API endpoint.
	fqdnBase := "https://pve-dev-01.example.invalid:8006"

	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			Auth:    "token",
			User:    "root@pam",
			TokenID: "proxops",
			Token:   "t0k",
			BaseURL: fqdnBase,
			Nodes:   []string{nonFQDNNode},
		},
		HTTPTimeout: 0,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	// A per-node path on a non-FQDN node must STILL hit the FQDN base.
	got := c.URL(nonFQDNNode, "nodes/"+nonFQDNNode+"/qemu")
	want := fqdnBase + "/api2/json/nodes/" + nonFQDNNode + "/qemu"
	if got != want {
		t.Fatalf("URL = %q, want %q\n(host must be the base-url, never the node name)", got, want)
	}

	// Cluster-wide paths also hit the FQDN base.
	gotCw := c.URL("gateway", "cluster/resources")
	wantCw := fqdnBase + "/api2/json/cluster/resources"
	if gotCw != wantCw {
		t.Errorf("cluster URL = %q, want %q", gotCw, wantCw)
	}
}

// TestPVEParamsCredentialComposition — pin the composition rules for
// PVEParams.Credential(): TokenValue wins when set; otherwise
// user!tokenid=token; otherwise empty string (which makes applyAuth refuse
// the request cleanly instead of sending an unauthenticated header).
func TestPVEParamsCredentialComposition(t *testing.T) {
	cases := []struct {
		name string
		p    pveclient.PVEParams
		want string
	}{
		{"full triple", pveclient.PVEParams{User: "u@pam", TokenID: "id", Token: "tok"}, "u@pam!id=tok"},
		{"token-value wins", pveclient.PVEParams{User: "u@pam", TokenID: "id", Token: "tok", TokenValue: "v@pam!x=y"}, "v@pam!x=y"},
		{"missing user", pveclient.PVEParams{TokenID: "id", Token: "tok"}, ""},
		{"missing id", pveclient.PVEParams{User: "u@pam", Token: "tok"}, ""},
		{"missing token", pveclient.PVEParams{User: "u@pam", TokenID: "id"}, ""},
		{"empty", pveclient.PVEParams{}, ""},
	}
	for _, tc := range cases {
		if got := tc.p.Credential(); got != tc.want {
			t.Errorf("%s: Credential() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
