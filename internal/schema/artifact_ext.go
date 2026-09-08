package schema

import "strings"

// artifactExtAccept reports whether fn is a PVE 9.2 `dir`-storage download
// acceptable filename extension for the given content. Empirically probed
// on conformance-dev (PVE 9.2.2) 2026-09-08:
//
//	vztmpl (PVE container-template): .tar | .tar.zst | .tar.xz | .tar.gz
//	iso      (PVE cdrom/vmdk image):  .iso | .img
//
// PVE 9.2 rejects any other extension with HTTP 400 "wrong file
// extension", so parse-time validation fails closed early (before
// planning) instead of hitting the PVE API.
//
// The CTT/ISO schema Validate() MUST call this; it is the
// authoritative source (do not duplicate lists).
func artifactExtAccept(fn, content string) (bool, string) {
	if content == "" {
		return false, "empty content"
	}
	lc := strings.ToLower(fn)
	switch content {
	case "vztmpl":
		if strings.HasSuffix(lc, ".tar") {
			return true, ".tar"
		}
		if strings.HasSuffix(lc, ".tar.zst") {
			return true, ".tar.zst"
		}
		if strings.HasSuffix(lc, ".tar.xz") {
			return true, ".tar.xz"
		}
		if strings.HasSuffix(lc, ".tar.gz") {
			return true, ".tar.gz"
		}
	case "iso":
		if strings.HasSuffix(lc, ".iso") {
			return true, ".iso"
		}
		if strings.HasSuffix(lc, ".img") {
			return true, ".img"
		}
	}
	return false, "extension " + lastExtSuffix(lc) + " not accepted by PVE 9.2 for content " + content
}

// lastExtSuffix returns the trailing ".ext" (or ".a.b.c" for multi-dot
// names like foo.tar.zst) of s. Used in error messages only.
func lastExtSuffix(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		// Walk back over consecutive multi-dot suffixes: .tar.zst -> .zst
		return s[i:]
	}
	return "(none)"
}
