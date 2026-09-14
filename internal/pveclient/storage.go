package pveclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Storage bundles the /nodes/{n}/storage endpoints used for ISO and
// CTTemplate (vztmpl) artifact management.
type Storage struct {
	c *Client
}

// Storage returns a Storage handle.
func (c *Client) Storage() *Storage { return &Storage{c: c} }

// ContentEntry is one entry of GET /nodes/{n}/storage/{s}/content. PVE's
// "bare" content listing reports every volume across all content types
// supported by the storage — each entry has `content` set to one of
// "iso", "vztmpl", "backup", "rootdir", "images", ... depending on the
// pool. This is required on PVE 9.2 because
// `GET /storage/{s}/content/iso` and `/content/vztmpl` 500
// "unable to parse directory volume name" even when the listing contains
// entries of that type — the type segment is (mis)parsed as a volid.
type ContentEntry struct {
	Volid   string `json:"volid"`   // "local:iso/name.iso"
	Content string `json:"content"` // "iso" | "vztmpl" | "backup" | ...
	Format  string `json:"format"`  // "iso" | "tzst" | "dir" | ...
	Size    int64  `json:"size"`
	// Note: PVE also reports `ctime`, but its JSON type varies across PVE
	// builds (string on some, number on others), and proxops never uses
	// it — omit it so the decode stays robust across PVE builds.
}

// HasISO is a backward-compat convenience that uses the bare content listing
// (filter by content="iso"). On PVE 9.2 dir storage this is the only way to
// read the ISO pool (see ContentEntry doc).
func (s *Storage) HasISO(ctx context.Context, node, storage, filename string) (bool, error) {
	return s.HasContent(ctx, node, storage, "iso", filename)
}

// HasContent reports whether a file with the given name + PVE content type
// (e.g. "iso" or "vztmpl") exists on the storage backend.
func (s *Storage) HasContent(ctx context.Context, node, storage, content, filename string) (bool, error) {
	entries, err := s.Content(ctx, node, storage)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Content != content {
			continue
		}
		if volidFilename(e.Volid, content) == filename {
			return true, nil
		}
	}
	return false, nil
}

// Content lists all volume entries on a node storage (across every
// content type the storage supports). Callers that want only ISOs can
// filter on entry.Content == "iso"; callers that want only LXC templates
// filter on entry.Content == "vztmpl".
func (s *Storage) Content(ctx context.Context, node, storage string) ([]ContentEntry, error) {
	var out []ContentEntry
	path := "nodes/" + node + "/storage/" + storage + "/content"
	if _, err := s.c.Do(ctx, http.MethodGet, node, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Download issues an ISO, CTTemplate or DiskImage download to the storage backend.
// The `content` parameter tells PVE which pool the file belongs to:
//   - "iso":       PVE downloads to `<storage>:iso/<filename>`
//   - "vztmpl":    PVE downloads to `<storage>:vztmpl/<filename>`
//
// Endpoint contract (PVE 9.2.x — probed live on conformance-dev,
// PVE 9.2.2 release):
//
//	POST /nodes/{n}/storage/{s}/download-url
//	  form: url=<https URI>, filename=<name>, content=<iso|vztmpl>
//
// returns a task UPID. `POST .../download` was the PVE 8-era path and on
// PVE 9.2 dir storage returns 501 "Method 'POST /nodes/.../storage/.../download'
// not implemented" — the endpoint was renamed. Probes also confirmed
// `GET` on `/download-url` is 501 (POST-only) and that a URL the node
// cannot fetch still returns 200 + task UPID, with exitstatus
// `"download failed: exit code 8"` on the task (curl exit 8 = "Could not
// connect to server"). proxops therefore always treats a 5xx or task
// exitstatus-other-than-OK as a real failure and relies on
// Storage.HasContent for the eventual convergence test: the file appears
// on the storage pool after the task succeeds.
func (s *Storage) Download(ctx context.Context, node, storage, downloadURL, filename, content string) (string, error) {
	p := url.Values{
		"url":      {downloadURL},
		"filename": {filename},
	}
	if content != "" {
		p.Set("content", content)
	}
	path := "nodes/" + node + "/storage/" + storage + "/download-url"
	return s.c.Do(ctx, http.MethodPost, node, path, p, nil)
}

// VolidFilename extracts the filename from a PVE content volid. Forms:
// "<storage>:iso/<filename>", "<storage>:vztmpl/<filename>",
// "<storage>:backup/<path>", etc. Exported so adopt can reuse it when
// reverse-engineering storage listings into proxops artifact manifests.
func VolidFilename(volid, content string) string {
	if content != "" {
		probe := ":" + content + "/"
		if i := strings.Index(volid, probe); i >= 0 {
			return volid[i+len(probe):]
		}
	}
	// fall back to last path component
	if j := strings.LastIndex(volid, "/"); j >= 0 && j < len(volid)-1 {
		return volid[j+1:]
	}
	return volid
}

// volidFilename delegates to VolidFilename (in-package call sites).
func volidFilename(volid, content string) string { return VolidFilename(volid, content) }
