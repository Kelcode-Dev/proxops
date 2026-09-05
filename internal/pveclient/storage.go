package pveclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Storage bundles the /nodes/{n}/storage endpoints used for ISO management.
type Storage struct {
	c *Client
}

// Storage returns a Storage handle.
func (c *Client) Storage() *Storage { return &Storage{c: c} }

// ISOContentEntry is one entry of GET /nodes/{n}/storage/{s}/content/iso.
type ISOContentEntry struct {
	Volid string `json:"volid"` // "local:iso/name.iso"
	// PVE's iso listing does not guarantee a "filename" field; the filename
	// is the volid suffix after ":iso/".
	filename string // populated by HasISO helpers
}

// HasISO reports whether a file with the given name exists on the storage
// backend's ISO pool.
func (s *Storage) HasISO(ctx context.Context, node, storage, filename string) (bool, error) {
	entries, err := s.isoContent(ctx, node, storage)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if isoVolidFilename(e.Volid) == filename {
			return true, nil
		}
	}
	return false, nil
}

// isoContent lists ISOs on a node storage.
func (s *Storage) isoContent(ctx context.Context, node, storage string) ([]ISOContentEntry, error) {
	var out []ISOContentEntry
	path := "nodes/" + node + "/storage/" + storage + "/content/iso"
	if _, err := s.c.Do(ctx, http.MethodGet, node, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Download issues an ISO download to the storage backend. Returns the PVE task
// UPID (downloads are asynchronous).
func (s *Storage) Download(ctx context.Context, node, storage, downloadURL, filename string) (string, error) {
	p := url.Values{
		"url":      {downloadURL},
		"filename": {filename},
	}
	path := "nodes/" + node + "/storage/" + storage + "/download"
	return s.c.Do(ctx, http.MethodPost, node, path, p, nil)
}

// isoVolidFilename extracts the filename from a PVE iso volid.
// volid form: "<storage>:iso/<filename>" or "<storage>:/iso/<filename>"
func isoVolidFilename(volid string) string {
	if i := strings.Index(volid, ":iso/"); i >= 0 {
		return volid[i+len(":iso/"):]
	}
	// fall back to last path component
	if j := strings.LastIndex(volid, "/"); j >= 0 && j < len(volid)-1 {
		return volid[j+1:]
	}
	return volid
}

func lastSlash(s string) int { return strings.LastIndex(s, "/") }
