// Wire regression for the M12 clone endpoint.
//
// Pins the exact form-values pveclient.VM().Clone submits against
// POST /nodes/{n}/qemu/{src}/clone, matching the conformance-dev PVE 9.2.2
// probe (2026-09-15):
//   - newid + full=1 + name are the only fields proxops sends;
//   - `start` is NEVER sent (PVE's clone schema rejects it: "property is
//     not defined in schema");
//   - the request targets the SOURCE vmid's path, not the destination's
//     (a wrong-resource / wrong-VMID shape proxops must never produce).
package pveclient_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cloneRecorder(t *testing.T, path *string, form *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*path = r.URL.Path
		var enc []string
		for k, vs := range r.PostForm {
			for _, v := range vs {
				enc = append(enc, k+"="+v)
			}
		}
		*form = enc
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":"UPID:n:1:2:3:qmclone:950:root@pam:"}`)
	}))
}

func TestVMCloneWireShape(t *testing.T) {
	var gotPath string
	var gotForm []string
	srv := cloneRecorder(t, &gotPath, &gotForm)
	defer srv.Close()

	if _, err := newClientAt(srv.URL).VM().Clone(context.Background(), "n1", 950, 951, true, "vm-m12"); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if want := "/api2/json/nodes/n1/qemu/950/clone"; gotPath != want {
		t.Errorf("clone path = %q, want %q (source vmid in the path)", gotPath, want)
	}
	form := strings.Join(gotForm, "&")
	for _, want := range []string{"newid=951", "full=1", "name=vm-m12"} {
		if !strings.Contains(form, want) {
			t.Errorf("clone form %q missing %q", form, want)
		}
	}
	if strings.Contains(form, "start=") {
		t.Errorf("clone form must never carry start= (PVE rejects it): %q", form)
	}
}

func TestVMCloneLinkedForm(t *testing.T) {
	var gotPath string
	var gotForm []string
	srv := cloneRecorder(t, &gotPath, &gotForm)
	defer srv.Close()

	if _, err := newClientAt(srv.URL).VM().Clone(context.Background(), "n1", 950, 951, false, ""); err != nil {
		t.Fatalf("Clone(linked): %v", err)
	}
	form := strings.Join(gotForm, "&")
	if !strings.Contains(form, "full=0") {
		t.Errorf("linked clone form = %q, want full=0", form)
	}
	if strings.Contains(form, "name=") {
		t.Errorf("empty name must not be sent: %q", form)
	}
}
