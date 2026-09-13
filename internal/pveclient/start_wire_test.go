// Wire regression for the create-time `start` form-value.
//
// PVE 9.2 declares `start` as a boolean and REJECTS the Go-style "true"
// with HTTP 400 "type check ('boolean') failed - got 'true'" (probed on
// conformance-dev 2026-09-13). Before the fix, pveclient.VM().Create /
// LXC().Create set start="true", so EVERY `state: started` create failed
// against real PVE — while passing against the mock, which accepted any
// non-empty value. These tests pin the exact wire bytes.
package pveclient_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
)

func startFormRecorder(t *testing.T, got *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if got != nil {
			*got = r.PostForm
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":"UPID:n:1:2:3:qmcreate:100:root@pam:"}`)
	}))
}

func newClientAt(url string) *pveclient.Client {
	c, err := pveclient.New(pveclient.Options{
		PVE:     pveclient.PVEParams{User: "root@pam", Auth: "token", TokenID: "t", Token: "tok"},
		BaseURL: url,
	}, slog.Default())
	if err != nil {
		panic(err)
	}
	return c
}

func TestVMCreateStartWireValue(t *testing.T) {
	var got url.Values
	srv := startFormRecorder(t, &got)
	defer srv.Close()

	if _, err := newClientAt(srv.URL).VM().Create(context.Background(), "n", url.Values{"vmid": {"100"}}, true); err != nil {
		t.Fatalf("Create(start=true): %v", err)
	}
	if v := got.Get("start"); v != "1" {
		t.Errorf("start wire value = %q, want \"1\" (PVE 9.2 boolean form; \"true\" is rejected with 400)", v)
	}

	got = nil
	if _, err := newClientAt(srv.URL).VM().Create(context.Background(), "n", url.Values{"vmid": {"101"}}, false); err != nil {
		t.Fatalf("Create(start=false): %v", err)
	}
	if v, ok := got["start"]; ok {
		t.Errorf("start present on stopped create: %v", v)
	}
}

func TestLXCCreateStartWireValue(t *testing.T) {
	var got url.Values
	srv := startFormRecorder(t, &got)
	defer srv.Close()

	if _, err := newClientAt(srv.URL).LXC().Create(context.Background(), "n", url.Values{"ctid": {"200"}}, true); err != nil {
		t.Fatalf("LXC Create(start=true): %v", err)
	}
	if v := got.Get("start"); v != "1" {
		t.Errorf("start wire value = %q, want \"1\"", v)
	}
}
