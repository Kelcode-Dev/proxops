package pveclient_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/pveclient"
)

// newTestClient points the pveclient at a local httptest server.
func newTestClient(t *testing.T, h http.Handler) *pveclient.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:       "root@pam",
			Auth:       "token",
			TokenValue: "root@pam!proxops=test",
		},
		BaseURL: srv.URL,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestStorageDownloadURLPath pins the PVE 9.2 artifact-download endpoint:
// POST /api2/json/nodes/{n}/storage/{s}/download-url with url/filename/content
// form params. The PVE 8-era path /storage/{s}/download was renamed
// /download-url; hitting it on PVE 9.2 dir storage returns 501 "Method ...
// not implemented" — that is the regression test for the "ISO/CTTemplate
// downloads fail with 501" defect found on conformance-dev during M6
// integration testing.
func TestStorageDownloadURLPath(t *testing.T) {
	var (
		gotPath     string
		gotMethod   string
		gotURL      string
		gotFilename string
		gotContent  string
	)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		_ = r.ParseForm()
		gotURL = r.PostForm.Get("url")
		gotFilename = r.PostForm.Get("filename")
		gotContent = r.PostForm.Get("content")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:pve01:00001337:0110:abcd:download:boot.iso:root@pam!proxops:"}`))
	})
	c := newTestClient(t, h)

	upid, err := c.Storage().Download(context.Background(), "pve01", "local",
		"https://example.com/boot.iso", "boot.iso", "iso")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(upid, "download") {
		t.Errorf("UPID = %q, want a download task UPID", upid)
	}
	wantPath := "/api2/json/nodes/pve01/storage/local/download-url"
	if gotPath != wantPath {
		t.Errorf("Download hit %q, want %q", gotPath, wantPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("Download method = %q, want POST", gotMethod)
	}
	if gotURL != "https://example.com/boot.iso" {
		t.Errorf("url form param = %q, want the download URL", gotURL)
	}
	if gotFilename != "boot.iso" {
		t.Errorf("filename form param = %q, want boot.iso", gotFilename)
	}
	if gotContent != "iso" {
		t.Errorf("content form param = %q, want iso", gotContent)
	}
}

// TestStorageDownloadVZTMPLContent pins that CTTemplate downloads use
// content=vztmpl on the SAME /download-url endpoint (the `content` form
// value is what routes PVE to the right pool).
func TestStorageDownloadVZTMPLContent(t *testing.T) {
	var gotContent string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotContent = r.PostForm.Get("content")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:pve01:00001337:0111:abcd:download:tmpl.vztmpl:root@pam!proxops:"}`))
	})
	c := newTestClient(t, h)
	if _, err := c.Storage().Download(context.Background(), "pve01", "local",
		"https://example.com/t.tar.zst", "tmpl.vztmpl", "vztmpl"); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if gotContent != "vztmpl" {
		t.Errorf("content form param = %q, want vztmpl", gotContent)
	}
}

// TestStorageDownloadOmitsContentWhenEmpty pins that pveclient omits the
// `content` form param entirely when the caller passes "" (rather than
// sending an empty value PVE would reject).
func TestStorageDownloadOmitsContentWhenEmpty(t *testing.T) {
	var sawContentKey bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if _, present := r.PostForm["content"]; present {
			sawContentKey = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:pve01:00001337:0112:abcd:download:x:root@pam!p:"}`))
	})
	c := newTestClient(t, h)
	if _, err := c.Storage().Download(context.Background(), "pve01", "local",
		"https://x/y", "y", ""); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if sawContentKey {
		t.Errorf("pveclient sent the content form key with empty value; it should be omitted")
	}
}

// TestStorageDownloadLegacyPathReturns501 drives an httptest PVE exactly as
// PVE 9.2 behaves (501 on the legacy /download path, 200 on /download-url)
// and asserts proxops's Storage.Download still succeeds. If someone
// regresses the pveclient to the old path, this test fails with the same
// 501 the real cluster returns.
func TestStorageDownloadLegacyPathReturns501(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api2/json/nodes/pve01/storage/local/download") {
			// PVE 9.2 realism: the legacy path is gone.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"message":"Method 'POST /nodes/pve01/storage/local/download' not implemented","data":null}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/api2/json/nodes/pve01/storage/local/download-url") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"UPID:pve01:00001337:0113:abcd:download:ok.iso:root@pam!p:"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"unexpected","data":null}`))
	}))
	defer srv.Close()
	c, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:       "root@pam",
			Auth:       "token",
			TokenValue: "root@pam!proxops=test",
		},
		BaseURL: srv.URL,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Storage().Download(context.Background(), "pve01", "local",
		"https://x/y.iso", "y.iso", "iso"); err != nil {
		t.Fatalf("Download against 501-on-legacy server: %v — proxops is still calling the legacy /download path", err)
	}
}
