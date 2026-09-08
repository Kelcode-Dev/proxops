package pveclient_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
)

// TestLXCUpdateUsesPUT pins PVE 9.x's LXC config verb: PUT /nodes/{n}/lxc/{id}
// /config. PVE 9.2 returns 501 for POST /lxc/{id}/config on conformance-dev
// (probed 2026-09-08); LXC power ops stay POST. Regression for "LXC config
// drift apply 501s every cycle".
func TestLXCUpdateUsesPUT(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	t.Cleanup(srv.Close)
	c, err := pveclient.New(pveclient.Options{
		PVE:     pveclient.PVEParams{User: "root@pam", Auth: "token", TokenValue: "root@pam!proxops=test"},
		BaseURL: srv.URL,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v := url.Values{}
	v.Set("memory", "1024")
	if _, err := c.LXC().Update(context.Background(), "pve01", 9200, v); err != nil {
		t.Fatalf("LXC().Update: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("LXC Update used %s, want PUT (PVE 9.x)", gotMethod)
	}
	if !strings.Contains(gotPath, "/lxc/9200/config") {
		t.Errorf("LXC Update path = %q, want .../lxc/9200/config", gotPath)
	}
}

// TestLXCPowerUsesPOST pins LXC power verbs (PVE asymmetric: config PUT,
// power POST).
func TestLXCPowerUsesPOST(t *testing.T) {
	var startMethod, stopMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status/start"):
			startMethod = r.Method
		case strings.HasSuffix(r.URL.Path, "/status/stop"):
			stopMethod = r.Method
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:pve01:00000001:0000:abcd:vzstart:9200:root@pam!p:"}`))
	}))
	t.Cleanup(srv.Close)
	c, err := pveclient.New(pveclient.Options{
		PVE:     pveclient.PVEParams{User: "root@pam", Auth: "token", TokenValue: "root@pam!p=t"},
		BaseURL: srv.URL,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _ = c.LXC().Start(context.Background(), "pve01", 9200)
	_, _ = c.LXC().Stop(context.Background(), "pve01", 9200)
	if startMethod != http.MethodPost {
		t.Errorf("LXC Start used %s, want POST", startMethod)
	}
	if stopMethod != http.MethodPost {
		t.Errorf("LXC Stop used %s, want POST", stopMethod)
	}
}
