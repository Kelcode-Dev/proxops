package schema

import "strings"

// artifactExtAccept reports whether fn is a PVE 9.2 `dir`-storage download
// acceptable filename extension for the given content. Empirically probed
// on conformance-dev (PVE 9.2.2) 2026-09-08 and 2026-09-13:
//
//	vztmpl (PVE container-template): .tar | .tar.zst | .tar.xz | .tar.gz
//	iso      (PVE cdrom/vmdk image):  .iso | .img
//	import   (PVE 9 disk image):      .qcow2 | .vmdk | .raw
//
// PVE 9.2 rejects any other extension with HTTP 400 "wrong file
// extension", so parse-time validation fails closed early (before
// planning) instead of hitting the PVE API.
//
// The CTT/ISO/DiskImage schema Validate() MUST call this; it is the
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
	case "import":
		// PVE 9 `import` content pool (probed on conformance-dev 2026-09-13):
		// .qcow2 | .vmdk | .raw. NOTE: .img is ISO-pool only and .qcow (v1) is
		// rejected — both 400 "invalid filename or wrong extension".
		if strings.HasSuffix(lc, ".qcow2") {
			return true, ".qcow2"
		}
		if strings.HasSuffix(lc, ".vmdk") {
			return true, ".vmdk"
		}
		if strings.HasSuffix(lc, ".raw") {
			return true, ".raw"
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
