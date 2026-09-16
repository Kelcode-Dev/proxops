// Package mock implements a stateful in-memory PVE API server for tests.
//
// It is a PVE-shape fake:
//   - token auth via Authorization: PVEAPIToken=<value>
//   - ticket auth via POST /access/ticket + PVEAuthCookie
//   - async tasks: create/update/power/delete/clone/template/download all
//     return a task UPID; /nodes/{n}/tasks/{upid}/status settles after the
//     configured number of polls
//   - per-node qemu+lxc objects (PVE's id space is shared within a node).
//     PVE reports "no such vm" as HTTP 500 for unknown ids.
//   - per-node, per-storage ISO listing + download
//   - /cluster/resources lists objects with "type" = "qm" or "lxc"
//
// The mock is plain HTTP; the pveclient points BaseURL at the mock root.
package mock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config controls mock behavior.
type Config struct {
	Token          string
	TicketUser     string
	TicketPassword string
	// TaskTicks: polls until a task settles; 0/neg → 1.
	TaskTicks int
}

// VM is one object record for a PVE node.
type VM struct {
	ID     int
	Kind   string // "qemu" | "lxc"
	Config map[string]string
	Status string // "running" | "stopped"
}

// Server is the stateful fake PVE.
type Server struct {
	mu        sync.Mutex
	cfg       Config
	tickets   map[string]bool
	objs      map[string]map[int]VM                 // node -> id -> record
	isos      map[string]map[string]map[string]bool // node -> storage -> filename
	templates map[string]map[string]map[string]bool // node -> storage -> filename (vztmpl pool)
	// images: node -> storage -> filename for PVE 9's `import` content pool
	// (qcow2/vmdk/raw disk images). DiskImage artifacts live here.
	images map[string]map[string]map[string]bool
	// lvmVols: node -> storage -> volid ("local-lvm:vm-100-disk-0") -> PVE
	// data volume (size + PVE `content` label). Surfaces in the bare
	// storage content listing, mirroring PVE's LVM content pool; adopt
	// resolves LXC rootfs/mp sizes from it.
	lvmVols map[string]map[string]map[string]lvmVol
	// clusterNodes: pinned /cluster/nodes list (SetClusterNodes). When nil,
	// the mock derives the list from object placements (clusterNodesAuto).
	clusterNodes     []string
	clusterNodesAuto []string
	tasks            map[string]*task
	taskSeq          int

	created, updated, deleted, cloned, tpl, untpl, isl, tdl int

	// downloadFail makes POST /storage/{s}/download return HTTP 500 so a test
	// can exercise the executor's in-cycle dependency deferral (failed
	// prerequisite → dependant deferred). Set via SetDownloadFail.
	downloadFail bool

	// seenMethods records every HTTP method the mock served, in order. Used
	// by tests to independently prove that a caller (adopt) issued zero
	// writes (POST/PUT/DELETE) — defense-in-depth on top of the
	// pveclient.Client.WritesPerformed counter.
	seenMethods []string

	ts *httptest.Server
}

type task struct{ ticks int }

// lvmVol is one PVE data volume on a storage pool.
type lvmVol struct {
	Size    int64
	Content string // PVE `content` label: rootdir | images | ...
}

// New starts the mock and returns it.
func New(cfg Config) *Server {
	if cfg.TaskTicks <= 0 {
		cfg.TaskTicks = 1
	}
	s := &Server{
		cfg:       cfg,
		tickets:   map[string]bool{},
		objs:      map[string]map[int]VM{},
		isos:      map[string]map[string]map[string]bool{},
		templates: map[string]map[string]map[string]bool{},
		images:    map[string]map[string]map[string]bool{},
		lvmVols:   map[string]map[string]map[string]lvmVol{},
		tasks:     map[string]*task{},
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL returns the mock root ("http://127.0.0.1:PORT").
func (s *Server) URL() string { return s.ts.URL }

// WritesObserved returns the number of POST/PUT/DELETE requests the mock
// has served since construction. A caller that is expected to be read-only
// (adopt, dry-plan, verification paths) MUST observe 0; tests asserting
// this are double-proofing on top of pveclient.Client.WritesPerformed.
func (s *Server) WritesObserved() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.seenMethods {
		if m == http.MethodPost || m == http.MethodPut || m == http.MethodDelete {
			n++
		}
	}
	return n
}

// Methods returns a copy of every HTTP method the mock served, in order
// (GET, POST, ...). Adopt must see GET-only.
func (s *Server) Methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.seenMethods...)
}

// Close stops the mock.
func (s *Server) Close() { s.ts.Close() }

// SetDownloadFail toggles forced HTTP 500 on POST /storage/{s}/download. Used
// to exercise the executor's failed-prerequisite deferral path.
func (s *Server) SetDownloadFail(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloadFail = fail
}

// --- test setup / introspection ---

// PreloadVM seeds a qemu object.
func (s *Server) PreloadVM(node string, vmid int, cfg map[string]string, status string) {
	s.set(node, vmid, "qemu", cfg, status)
}

// PreloadLXC seeds an lxc object.
func (s *Server) PreloadLXC(node string, cid int, cfg map[string]string, status string) {
	s.set(node, cid, "lxc", cfg, status)
}

// PreloadCT is an alias of PreloadLXC (M3 naming).

// PreloadVMTemplate seeds a qemu object with the PVE template flag set
// (M11: the TemplateVM adopt/test path — PVE 9.2 qm objects marked as
// templates report "template" = "1").
func (s *Server) PreloadVMTemplate(node string, vmid int, cfg map[string]string) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	if _, ok := cfg["template"]; !ok {
		cfg = map[string]string{}
		for k, v := range cfg {
			cfg[k] = v
		}
		cfg["template"] = "1"
	}
	s.set(node, vmid, "qemu", cfg, "stopped")
}

// PreloadCT is an alias of PreloadLXC (M3 naming).
func (s *Server) PreloadCT(node string, cid int, cfg map[string]string, status string) {
	s.set(node, cid, "lxc", cfg, status)
}

// PreloadCTTemplate seeds an lxc object with the template flag set.
func (s *Server) PreloadCTTemplate(node string, cid int, cfg map[string]string) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	if _, ok := cfg["template"]; !ok {
		cfg["template"] = "1"
	}
	s.set(node, cid, "lxc", cfg, "stopped")
}

// PreloadLVMVolume records a PVE data volume (LVM CT rootfs/mp or generic
// disk) on a node storage, so its size + PVE `content` label appear in the
// bare storage content listing. Mirrors PVE's local-lvm content pool shape.
func (s *Server) PreloadLVMVolume(node, storage, volid string, sizeBytes int64, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if content == "" {
		content = "rootdir"
	}
	if s.lvmVols[node] == nil {
		s.lvmVols[node] = map[string]map[string]lvmVol{}
	}
	if s.lvmVols[node][storage] == nil {
		s.lvmVols[node][storage] = map[string]lvmVol{}
	}
	s.lvmVols[node][storage][volid] = lvmVol{Size: sizeBytes, Content: content}
	s.addNodeLocked(node)
}

// SetClusterNodes pins the /cluster/nodes list served by the mock. When
// unset, the mock derives the list from object placements.
func (s *Server) SetClusterNodes(nodes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusterNodes = append([]string{}, nodes...)
}

// addNodeLocked records a node for the auto-derived /cluster/nodes list
// (used only when SetClusterNodes has not pinned one). Caller holds lock.
func (s *Server) addNodeLocked(node string) {
	if s.clusterNodes != nil {
		return
	}
	for _, n := range s.clusterNodesAuto {
		if n == node {
			return
		}
	}
	s.clusterNodesAuto = append(s.clusterNodesAuto, node)
}

// nodeClusterNamesLocked returns the effective /cluster/nodes list.
// Caller holds lock.
func (s *Server) nodeClusterNamesLocked() []string {
	if s.clusterNodes != nil {
		return s.clusterNodes
	}
	return s.clusterNodesAuto
}

// PreloadISO adds an already-present ISO on a storage.
func (s *Server) PreloadISO(node, storage, filename string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addNodeLocked(node)
	if s.isos[node] == nil {
		s.isos[node] = map[string]map[string]bool{}
	}
	if s.isos[node][storage] == nil {
		s.isos[node][storage] = map[string]bool{}
	}
	s.isos[node][storage][filename] = true
}

// PreloadTemplate adds an already-present vztmpl template on a storage.
func (s *Server) PreloadTemplate(node, storage, filename string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addNodeLocked(node)
	if s.templates[node] == nil {
		s.templates[node] = map[string]map[string]bool{}
	}
	if s.templates[node][storage] == nil {
		s.templates[node][storage] = map[string]bool{}
	}
	s.templates[node][storage][filename] = true
}

// PreloadDiskImage adds an already-present import-pool disk image on a storage.
func (s *Server) PreloadDiskImage(node, storage, filename string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addNodeLocked(node)
	if s.images[node] == nil {
		s.images[node] = map[string]map[string]bool{}
	}
	if s.images[node][storage] == nil {
		s.images[node][storage] = map[string]bool{}
	}
	s.images[node][storage][filename] = true
}

func (s *Server) set(node string, id int, kind string, cfg map[string]string, status string) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objs[node] == nil {
		s.objs[node] = map[int]VM{}
	}
	s.addNodeLocked(node)
	s.objs[node][id] = VM{ID: id, Kind: kind, Config: cfg, Status: status}
}

// object is the kind-agnostic accessor core. PVE's numeric id space is treated
// as shared per node in this mock, mirroring M3's contract so existing e2e
// tests (which call VMExists for LXC) keep working.
func (s *Server) object(node string, id int) (VM, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.objs[node][id]
	return v, ok
}

// VMConfig returns the live config of an object (nil if absent).
func (s *Server) VMConfig(node string, vmid int) map[string]string {
	if v, ok := s.object(node, vmid); ok {
		return v.Config
	}
	return nil
}

// CTConfig is an alias of VMConfig for the LXC case (kind-agnostic).
func (s *Server) CTConfig(node string, cid int) map[string]string { return s.VMConfig(node, cid) }

// SetVMConfigField overwrites one config key on a live object, simulating
// out-of-band PVE-side drift for tests.
func (s *Server) SetVMConfigField(node string, vmid int, key, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objs[node][vmid]; ok {
		if o.Config == nil {
			o.Config = map[string]string{}
		}
		o.Config[key] = val
		s.objs[node][vmid] = o
	}
}

// LXCConfig is an alias of VMConfig for the LXC case.
func (s *Server) LXCConfig(node string, cid int) map[string]string { return s.VMConfig(node, cid) }

// VMStatus returns the object status (false if absent).
func (s *Server) VMStatus(node string, vmid int) (string, bool) {
	if v, ok := s.object(node, vmid); ok {
		return v.Status, true
	}
	return "", false
}

// LXCStatus is an alias of VMStatus for the LXC case.
func (s *Server) LXCStatus(node string, cid int) (string, bool) { return s.VMStatus(node, cid) }

// VMExists reports object presence.
func (s *Server) VMExists(node string, vmid int) bool {
	_, ok := s.object(node, vmid)
	return ok
}

// CTExists is an alias of VMExists for the LXC case.
func (s *Server) CTExists(node string, cid int) bool { return s.VMExists(node, cid) }

// LXCExists is an alias of VMExists for the LXC case.
func (s *Server) LXCExists(node string, cid int) bool { return s.VMExists(node, cid) }

// VMIsTemplate (M11) reports whether a qemu object on the node is flagged a
// PVE template (template=1). Mirrors CTIsTemplate for the qm kind.
func (s *Server) VMIsTemplate(node string, vmid int) bool {
	v, ok := s.object(node, vmid)
	return ok && v.Kind == "qemu" && v.Config["template"] == "1"
}

// CTIsTemplate reports whether an lxc object on the node is flagged a template.
func (s *Server) CTIsTemplate(node string, cid int) bool {
	v, ok := s.object(node, cid)
	return ok && v.Config["template"] == "1"
}

// ISOExists reports whether a filename is present on a node-storage ISO pool.
func (s *Server) ISOExists(node, storage, filename string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.isos[node][storage]; st != nil {
		return st[filename]
	}
	return false
}

// TemplateExists reports whether a vztmpl filename is present on a node-storage pool.
func (s *Server) TemplateExists(node, storage, filename string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.templates[node][storage]; st != nil {
		return st[filename]
	}
	return false
}

// ISOs lists the filenames on a node-storage.
func (s *Server) ISOs(node, storage string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for f := range s.isos[node][storage] {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// --- routing ---

func (s *Server) split(s2 string) (h, t string, ok bool) {
	i := strings.IndexByte(s2, '/')
	if i < 0 {
		return s2, "", false
	}
	return s2[:i], s2[i+1:], true
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api2/json/")

	// Record the method so tests can independently prove zero writes
	// (defense-in-depth on top of the pveclient counter).
	s.mu.Lock()
	s.seenMethods = append(s.seenMethods, r.Method)
	s.mu.Unlock()

	switch rest {
	case "access/ticket":
		s.handleTicket(w, r)
		return
	case "version":
		writeOK(w, map[string]any{"version": "8.2", "release": "mock"})
		return
	case "cluster/nodes":
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.mu.Lock()
		names := s.nodeClusterNamesLocked()
		s.mu.Unlock()
		out := make([]map[string]any, 0, len(names))
		for _, n := range names {
			out = append(out, map[string]any{"node": n, "status": "online"})
		}
		writeOK(w, out)
		return
	}

	if !s.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "authentication error")
		return
	}

	switch {
	case rest == "cluster/resources":
		s.handleClusterResources(w, r)
		return
	case strings.HasPrefix(rest, "nodes/"):
		node, after, ok := s.split(rest[len("nodes/"):])
		if !ok {
			writeErr(w, http.StatusNotFound, "malformed node path")
			return
		}
		kind, rest2, ok := s.split(after)
		if !ok {
			kind, rest2 = after, ""
		}
		switch kind {
		case "qemu", "lxc":
			// Bare listing: GET /nodes/{n}/qemu or /nodes/{n}/lxc.
			// PVE's node object listing carries the object id under
			// `vmid` for both kinds (confirmed on PVE 9.2 /nodes/{n}/lxc;
			// the per-node id space is shared). adopt consumes it.
			if rest2 == "" && r.Method == http.MethodGet {
				s.handleObjectListing(w, node, kind)
				return
			}
			s.objectRoute(w, r, node, kind, rest2)
		case "tasks":
			s.taskRoute(w, r, node, rest2)
		case "storage":
			s.storageRoute(w, r, node, after)
		default:
			writeErr(w, http.StatusNotFound, "unknown kind "+kind)
		}
		return
	default:
		writeErr(w, http.StatusNotFound, "unknown endpoint /"+rest)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "PVEAPIToken=") {
		return s.cfg.Token != "" && strings.TrimPrefix(hdr, "PVEAPIToken=") == s.cfg.Token
	}
	if c, err := r.Cookie("PVEAuthCookie"); err == nil && c.Value != "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.tickets[c.Value]
	}
	return false
}

func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_ = r.ParseForm()
	user := r.PostFormValue("user")
	if user == "" {
		user = r.PostFormValue("username")
	}
	pass := r.PostFormValue("password")
	if user != s.cfg.TicketUser || pass != s.cfg.TicketPassword {
		writeErr(w, http.StatusUnauthorized, "authentication error")
		return
	}
	t := fmt.Sprintf("ticket-%d", time.Now().UnixNano())
	s.mu.Lock()
	s.tickets[t] = true
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "PVEAuthCookie", Value: t, Path: "/api2/json"})
	http.SetCookie(w, &http.Cookie{Name: "X-Userid", Value: user, Path: "/api2/json"})
	w.Header().Set("Set-X-Userid", user)
	writeOK(w, map[string]any{"ticket": t, "CSRFToken": "mock", "valid_until": time.Now().Add(2 * time.Hour).Unix()})
}

func (s *Server) handleClusterResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	type row struct {
		node string
		v    VM
	}
	s.mu.Lock()
	var rs []row
	for nod, m := range s.objs {
		for _, v := range m {
			rs = append(rs, row{nod, v})
		}
	}
	s.mu.Unlock()
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].node != rs[j].node {
			return rs[i].node < rs[j].node
		}
		if rs[i].v.Kind != rs[j].v.Kind {
			return rs[i].v.Kind < rs[j].v.Kind
		}
		return rs[i].v.ID < rs[j].v.ID
	})
	out := make([]map[string]any, 0, len(rs))
	for _, rw := range rs {
		typ := "qm"
		if rw.v.Kind == "lxc" {
			typ = "lxc"
		}
		out = append(out, map[string]any{
			"node":   rw.node,
			"vmid":   rw.v.ID,
			"type":   typ,
			"status": rw.v.Status,
			"name":   rw.v.Config["name"],
			"tags":   rw.v.Config["tags"],
		})
	}
	writeOK(w, out)
}

// handleObjectListing serves GET /nodes/{n}/qemu and GET /nodes/{n}/lxc.
// Rows carry vmid/name/status/tags (PVE shape); sorted by vmid for stable
// tests + adopt.
func (s *Server) handleObjectListing(w http.ResponseWriter, node, kind string) {
	s.mu.Lock()
	ids := make([]int, 0, len(s.objs[node]))
	for id, v := range s.objs[node] {
		if v.Kind == kind {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		v := s.objs[node][id]
		out = append(out, map[string]any{
			"vmid":   id,
			"name":   v.Config["name"],
			"status": v.Status,
			"tags":   v.Config["tags"],
		})
	}
	s.mu.Unlock()
	writeOK(w, out)
}

// handleNodeStorageListings serves GET /nodes/{n}/storage (PVE's storage
// backend listing keyed on `storage`, not `id`). Seed set: local +
// local-lvm (standard PVE install), plus any storage ids artifacts,
// templates, or LVM volumes have been preloaded on for that node.
func (s *Server) handleNodeStorageListings(w http.ResponseWriter, node string) {
	s.mu.Lock()
	ids := map[string]bool{"local": true, "local-lvm": true}
	for st := range s.isos[node] {
		ids[st] = true
	}
	for st := range s.templates[node] {
		ids[st] = true
	}
	for st := range s.images[node] {
		ids[st] = true
	}
	for st := range s.lvmVols[node] {
		ids[st] = true
	}
	s.mu.Unlock()
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	sort.Strings(idList)
	out := make([]map[string]any, 0, len(idList))
	for _, id := range idList {
		out = append(out, map[string]any{
			"storage": id,
			"type":    "dir",
			"status":  "ok",
			"content": "iso,vztmpl,rootdir,images,backup",
		})
	}
	writeOK(w, out)
}

// parsePVEBool parses PVE 9.2's boolean form-value grammar: 1/0, yes/no,
// on/off (case-insensitive). ok=false for anything else — notably the
// Go-style "true"/"false", which real PVE rejects with
// "type check ('boolean') failed" (probed on conformance-dev 2026-09-13).
func parsePVEBool(s string) (val bool, ok bool) {
	switch strings.ToLower(s) {
	case "1", "yes", "on":
		return true, true
	case "0", "no", "off":
		return false, true
	}
	return false, false
}

// normalizePVEDrive mirrors PVE's create→report transformation for a drive
// property string. PVE allocates a volume name and rewrites the create form:
//
//   - "local-lvm:8"                  → "local-lvm:vm-<id>-disk-<n>,size=8G"
//   - "local-lvm:0,import-from=X"    → "local-lvm:vm-<id>-disk-<n>,size=<s>G"
//     (the import-from option is create-time bookkeeping and is NOT re-reported;
//     PVE derives the size from the image's virtual size — the mock uses a fixed
//     3G, matching the debian-13 genericcloud image probed on conformance-dev
//     2026-09-13.)
//
// Only the disk-slot keys PVE allocates volumes for are transformed; cdrom /
// cloudinit / other values pass through with PVE's report shape applied where
// the mock can model it.
// isCreateFormVolume reports whether a form-value is PVE's CREATE form of a
// storage-backed volume: "<pool>:cloudinit..." or "<pool>:<size>" (a bare
// number after the pool). The LIVE form ("<pool>:vm-N-disk-0,size=...") is
// not create-form and is safe to restate.
func isCreateFormVolume(val string) bool {
	ci := strings.IndexByte(val, ':')
	if ci <= 0 {
		return false
	}
	rest := val[ci+1:]
	if strings.HasPrefix(rest, "cloudinit") || strings.Contains(rest, ":cloudinit") {
		return true
	}
	// Bare size token (e.g. "local-lvm:8") — create form. A volume name
	// ("vm-9100-disk-0") is not numeric.
	head := rest
	if j := strings.IndexByte(head, ','); j >= 0 {
		head = head[:j]
	}
	if head == "" {
		return false
	}
	for _, c := range head {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// hasLiveVolume reports whether a config value is a live allocated volume
// (non-empty, not the "none" form).
func hasLiveVolume(val string) bool {
	t := strings.TrimSpace(val)
	return t != "" && t != "none" && !strings.HasPrefix(t, "none,")
}

// renameCloneVolume rewrites a PVE volume reference for a full clone:
// "local-lvm:vm-<src>-disk-0[,opts]" -> "local-lvm:vm-<dst>-disk-0[,opts]"
// and "local-lvm:vm-<src>-cloudinit[,opts]" -> the destination's cloudinit
// volume. A base-template volume ("base-<src>-disk-N") becomes a fresh
// per-destination volume (full=1 semantics: independent data).
func renameCloneVolume(val string, src, dst int) string {
	ci := strings.IndexByte(val, ':')
	if ci <= 0 {
		return val
	}
	pool, rest := val[:ci], val[ci+1:]
	srcStr := strconv.Itoa(src)
	dstStr := strconv.Itoa(dst)
	switch {
	case strings.HasPrefix(rest, "vm-"+srcStr+"-"):
		rest = "vm-" + dstStr + "-" + rest[len("vm-"+srcStr+"-"):]
	case strings.HasPrefix(rest, "base-"+srcStr+"-"):
		// full clone: the base volume is copied into a fresh own volume.
		rest = "vm-" + dstStr + "-" + rest[len("base-"+srcStr+"-"):]
	}
	return pool + ":" + rest
}

// regenCloneMAC replaces a NIC string's MAC with a deterministic
// per-destination one, mirroring PVE assigning a fresh MAC to a clone.
func regenCloneMAC(val string, dst int) string {
	parts := strings.Split(val, ",")
	for i, p := range parts {
		if strings.HasPrefix(p, "virtio=") || strings.HasPrefix(p, "e1000=") ||
			strings.HasPrefix(p, "rtl8139=") || strings.HasPrefix(p, "vmxnet3=") {
			eq := strings.IndexByte(p, '=')
			parts[i] = p[:eq+1] + fmt.Sprintf("52:54:00:%02X:%02X:%02X", (dst>>16)&0xff, (dst>>8)&0xff, dst&0xff)
			break
		}
	}
	return strings.Join(parts, ",")
}

func normalizePVEDrive(id int, key, val string) string {
	// Only real disk slots (scsiN/virtioN/sataN) get PVE's volume-name +
	// size normalization; cdrom/cloudinit values are reported verbatim.
	if !strings.HasPrefix(key, "scsi") && !strings.HasPrefix(key, "virtio") && !strings.HasPrefix(key, "sata") {
		return val
	}
	ci := strings.IndexByte(val, ':')
	if ci <= 0 {
		return val
	}
	pool, rest := val[:ci], val[ci+1:]
	// import-from create form: "<pool>:0,import-from=<volid>[,opts...]".
	if !strings.HasPrefix(rest, "0,import-from=") {
		return val
	}
	tail := rest[len("0,import-from="):]
	opts := ""
	// The volid itself may contain ':' (storage:import path) but never a ',',
	// so the first ',' after import-from= starts the option list.
	if j := strings.IndexByte(tail, ','); j >= 0 {
		opts = tail[j:]
	}
	return pool + ":vm-" + strconv.Itoa(id) + "-disk-0,size=3G" + opts
}

// objectRoute handles /nodes/{n}/{qemu|lxc}/[id[/action]].
func (s *Server) objectRoute(w http.ResponseWriter, r *http.Request, node, kind, rest string) {
	// bare: POST create
	if rest == "" {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			idStr := r.PostFormValue("vmid")
			if idStr == "" {
				idStr = r.PostFormValue("ctid")
			}
			id, err := strconv.Atoi(idStr)
			if err != nil || id <= 0 {
				writeErr(w, http.StatusBadRequest, "vmid or ctid required")
				return
			}
			s.mu.Lock()
			if s.objs[node] == nil {
				s.objs[node] = map[int]VM{}
			}
			if _, busy := s.objs[node][id]; busy {
				s.mu.Unlock()
				writeErr(w, http.StatusConflict, "vm id "+strconv.Itoa(id)+" already exists")
				return
			}
			cfg := map[string]string{}
			for k, vs := range r.PostForm {
				// PVE's create form doesn't include "vmid"/"ctid"/"start".
				if k == "vmid" || k == "ctid" || k == "start" || k == "newid" || k == "full" {
					continue
				}
				if len(vs) > 0 {
					cfg[k] = normalizePVEDrive(id, k, vs[0])
				}
			}
			// PVE 9.2 boolean form-values: 1/0/yes/no/on/off. The Go-style
			// "true" is REJECTED with 400 "type check ('boolean') failed -
			// got 'true'" (probed on conformance-dev 2026-09-13 — this is
			// exactly the bug that made every `state: started` create fail
			// against real PVE while passing against the mock).
			if sv := r.PostForm.Get("start"); sv != "" {
				if _, ok := parsePVEBool(sv); !ok {
					s.mu.Unlock()
					writeErr(w, http.StatusBadRequest, "Parameter verification failed.\n")
					return
				}
			}
			kkind := "qemu"
			if kind == "lxc" {
				kkind = "lxc"
			}
			status := "stopped"
			if sv := r.PostForm.Get("start"); sv != "" {
				if b, ok := parsePVEBool(sv); ok && b {
					status = "running"
				}
			}
			s.objs[node][id] = VM{ID: id, Kind: kkind, Config: cfg, Status: status}
			upid := s.newTaskLocked(node, "create")
			s.created++
			s.mu.Unlock()
			writeOK(w, upid)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// /id or /id/action
	idStr, after, ok := s.split(rest)
	if !ok {
		idStr, after = rest, ""
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad object id "+idStr)
		return
	}

	// Look up object
	rec, okOb := s.object(node, id)
	kindStr := rec.Kind
	if !okOb {
		writeErr(w, http.StatusInternalServerError, "no such vm "+strconv.Itoa(id))
		return
	}

	// PVE convention: VM actions are under /{id}/{action}; LXC likewise.
	// GET config: /{id} → returns the config as a flat object.
	if after == "" {
		if r.Method == http.MethodGet {
			s.mu.Lock()
			rec := s.objs[node][id]
			out := map[string]any{"vmid": id}
			for k, v := range rec.Config {
				out[k] = v
			}
			s.mu.Unlock()
			writeOK(w, out)
			return
		}
		// PVE 9.x: DELETE /nodes/{n}/qemu/{id} and DELETE /nodes/{n}/lxc/{id}
		// both perform the destroy (the old POST /qemu/{id}/vmdelete and POST
		// /lxc/{id}/ctdestroy endpoints are "not implemented" in PVE 9.2).
		if r.Method == http.MethodDelete {
			s.delete(w, r, node, id)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// PVE power/status endpoints sit under "status/..."; flatten them so the
	// action switch sees "start"/"stop"/"shutdown"/"reboot"/"current"
	// (TrimPrefix is a no-op when the prefix is absent).
	action := strings.TrimPrefix(after, "status/")

	// Actions:
	switch action {
	case "config":
		if r.Method == http.MethodGet {
			s.mu.Lock()
			rec := s.objs[node][id]
			out := map[string]any{"vmid": rec.ID}
			for k, v := range rec.Config {
				out[k] = v
			}
			s.mu.Unlock()
			writeOK(w, out)
			return
		}
		// PVE 9.x verbs: POST /qemu/{id}/config (VM) and PUT /lxc/{id}/config
		// (LXC). The mock store is kind-agnostic here, so accept both — this
		// mirrors PVE and keeps e2e LXC-update tests exercising the real verb.
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			_ = r.ParseForm()
			s.mu.Lock()
			rec := s.objs[node][id]
			if rec.Config == nil {
				rec.Config = map[string]string{}
			}
			// PVE's `delete=k1,k2,...` form-value removes keys in the SAME
			// request that sets others — but PVE 9.2 REJECTS setting and
			// deleting the same key in one request (probe-verified: 400
			// "you can't use '-ciuser' and '-delete ciuser' at the same
			// time"). Deleting an ABSENT key is tolerated. The mock mirrors
			// both rules so the clone path's delete= list is exercised
			// honestly.
			var dels []string
			if dv := r.PostForm.Get("delete"); dv != "" {
				dels = strings.Split(dv, ",")
			}
			for _, k := range dels {
				k = strings.TrimSpace(k)
				if k == "" {
					continue
				}
				if _, setting := r.PostForm[k]; setting {
					s.mu.Unlock()
					writeErr(w, http.StatusBadRequest,
						"Parameter verification failed.\n")
					return
				}
				delete(rec.Config, k)
			}
			for k, vs := range r.PostForm {
				if k == "delete" || len(vs) == 0 {
					continue
				}
				// PVE 9.2 realism (M12, probe-verified): writing the
				// CREATE form of a storage-backed volume ("<pool>:cloudinit"
				// / "<pool>:<size>") over a slot that already holds a live
				// volume makes the config task fail with
				// "lvcreate ... already exists". The clone path must never
				// restate an inherited cloud-init drive; the mock enforces
				// that so e2e tests catch the regression.
				if isCreateFormVolume(vs[0]) && hasLiveVolume(rec.Config[k]) {
					s.mu.Unlock()
					writeErr(w, http.StatusInternalServerError,
						"lvcreate error: volume already exists\n")
					return
				}
				rec.Config[k] = vs[0]
			}
			s.objs[node][id] = rec
			upid := s.newTaskLocked(node, "update")
			s.updated++
			s.mu.Unlock()
			writeOK(w, upid)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")

	case "template":
		// PVE mark-template: POST /{qemu|lxc}/{id}/template → template=1.
		// PVE reports template objects "stopped".
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			s.mu.Lock()
			rec := s.objs[node][id]
			if rec.Config == nil {
				rec.Config = map[string]string{}
			}
			rec.Config["template"] = "1"
			rec.Status = "stopped"
			s.objs[node][id] = rec
			upid := s.newTaskLocked(node, "template")
			s.tpl++
			s.mu.Unlock()
			writeOK(w, upid)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")

	case "untemplate":
		// PVE 9.2 wire (M11 probe 2026-09-11, conformance-dev VM 9100):
		//   - /lxc/{id}/untemplate  -> 200 + UPID
		//   - /qemu/{id}/untemplate -> HTTP 501 "not implemented"
		// The mock mirrors PVE 9.2 exactly: LXC untemplates, qemUs
		// reject. This locks e2e tests that pin proxops's
		// fail-closed VM<->TemplateVM mismatch rule in plan.PlanActions.
		if kindStr != "lxc" {
			writeErr(w, http.StatusNotImplemented,
				"Method '"+r.Method+" /nodes/"+node+"/"+kindStr+"/"+strconv.Itoa(id)+"/untemplate' not implemented")
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodDelete {
			s.mu.Lock()
			rec := s.objs[node][id]
			if rec.Config != nil {
				delete(rec.Config, "template")
			}
			s.objs[node][id] = rec
			upid := s.newTaskLocked(node, "untemplate")
			s.untpl++
			s.mu.Unlock()
			writeOK(w, upid)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")

	case "clone":
		// PVE clone: POST /{qemu|lxc}/{id}/clone with newid. M12 pins the
		// qemu path (qemu templates are the proxops TemplateVM clone
		// source). The mock mirrors PVE 9.2.2 clone semantics probed on
		// conformance-dev:
		//   - the destination keeps the SOURCE KIND (a qm clone is a qm);
		//   - the template flag is NOT inherited (a clone is a normal VM);
		//   - `name=` sets the destination name (PVE does not copy the
		//     source's name);
		//   - `start=` is REJECTED ("property is not defined in schema");
		//   - an existing destination id fails with 500 "config file
		//     already exists" (PVE's real shape, not 409);
		//   - full=1 allocates fresh volumes named vm-<newid>-disk-N and a
		//     fresh cloudinit volume, and PVE regenerates the MAC.
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "clone is a POST")
			return
		}
		_ = r.ParseForm()
		if r.PostForm.Get("start") != "" {
			writeErr(w, http.StatusBadRequest,
				"Parameter verification failed.\n")
			return
		}
		newidStr := r.PostFormValue("newid")
		newid, err2 := strconv.Atoi(newidStr)
		if err2 != nil || newid <= 0 {
			writeErr(w, http.StatusBadRequest, "newid required")
			return
		}
		s.mu.Lock()
		if s.objs[node] == nil {
			s.objs[node] = map[int]VM{}
		}
		if _, busy := s.objs[node][newid]; busy {
			s.mu.Unlock()
			writeErr(w, http.StatusInternalServerError,
				"unable to create VM "+strconv.Itoa(newid)+": config file already exists\n")
			return
		}
		src := s.objs[node][id]
		outCfg := map[string]string{}
		for k, v := range src.Config {
			outCfg[k] = v
		}
		// A clone is a normal VM: the template flag never carries over.
		delete(outCfg, "template")
		// PVE does not copy the source name; the clone's name= wins.
		delete(outCfg, "name")
		if nm := r.PostFormValue("name"); nm != "" {
			outCfg["name"] = nm
		}
		// Full clone: fresh per-vmid volume names + regenerated MAC.
		for k, v := range outCfg {
			switch {
			case strings.HasPrefix(k, "scsi") || strings.HasPrefix(k, "virtio") || strings.HasPrefix(k, "sata"):
				outCfg[k] = renameCloneVolume(v, id, newid)
			case strings.HasPrefix(k, "ide") || k == "efidisk0" || k == "tpm0":
				outCfg[k] = renameCloneVolume(v, id, newid)
			case strings.HasPrefix(k, "net"):
				outCfg[k] = regenCloneMAC(v, newid)
			}
		}
		kind := src.Kind
		if kind == "" {
			kind = "qemu"
		}
		s.objs[node][newid] = VM{ID: newid, Kind: kind, Config: outCfg, Status: "stopped"}
		upid := s.newTaskLocked(node, "clone")
		s.cloned++
		s.mu.Unlock()
		writeOK(w, upid)
		return

	case "start":
		s.power(w, r, node, id, "start")
	case "stop":
		s.power(w, r, node, id, "stop")
	case "shutdown":
		s.power(w, r, node, id, "shutdown")
	case "reboot":
		s.power(w, r, node, id, "reboot")

	case "current":
		// PVE GET /lxc/{id}/status/current returns "status": "stopped".
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.mu.Lock()
		rec := s.objs[node][id]
		key := "vmid"
		if kindStr == "lxc" {
			key = "cid"
		}
		out := map[string]any{
			key:      id,
			"status": rec.Status,
		}
		if mem, ok2 := rec.Config["memory"]; ok2 {
			if n, e := strconv.Atoi(mem); e == nil {
				out["maxmem"] = int64(n) * 1024
			}
		}
		s.mu.Unlock()
		writeOK(w, out)

	case "vmdelete":
		if r.Method == http.MethodDelete || r.Method == http.MethodPost {
			s.delete(w, r, node, id)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")

	case "resize":
		_ = r.ParseForm()
		m := r.PostFormValue("memory")
		if m == "" {
			writeErr(w, http.StatusBadRequest, "memory required")
			return
		}
		s.mu.Lock()
		rec := s.objs[node][id]
		if rec.Config == nil {
			rec.Config = map[string]string{}
		}
		rec.Config["memory"] = m
		s.objs[node][id] = rec
		upid := s.newTaskLocked(node, "resize")
		s.mu.Unlock()
		writeOK(w, upid)

	default:
		writeErr(w, http.StatusNotFound, "unknown action "+after)
	}
}

func (s *Server) power(w http.ResponseWriter, r *http.Request, node string, id int, verb string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.mu.Lock()
	rec, ok := s.objs[node][id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "no such vm "+strconv.Itoa(id))
		return
	}
	switch verb {
	case "start":
		if rec.Status == "running" {
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "vm "+strconv.Itoa(id)+" is already running")
			return
		}
		rec.Status = "running"
	case "stop", "shutdown":
		if rec.Status == "stopped" {
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "vm "+strconv.Itoa(id)+" is already stopped")
			return
		}
		rec.Status = "stopped"
	case "reboot":
		rec.Status = "stopped"
	}
	s.objs[node][id] = rec
	upid := s.newTaskLocked(node, "power-"+verb)
	s.mu.Unlock()
	writeOK(w, upid)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, node string, id int) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.objs[node][id]
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no such vm "+strconv.Itoa(id))
		return
	}
	if rec.Status == "running" {
		writeErr(w, http.StatusBadRequest, "vm "+strconv.Itoa(id)+" must be stopped before deletion")
		return
	}
	delete(s.objs[node], id)
	upid := s.newTaskLocked(node, "delete")
	s.deleted++
	writeOK(w, upid)
}

// storageRoute handles /nodes/{n}/storage/{sid}[/{sub}[/{content}]].
// PVE 9.2 contract (probed live on conformance-dev; see docs/OPERATIONS.md):
//   - GET  /nodes/{n}/storage/{sid}                      → storage info
//   - GET  /nodes/{n}/storage/{sid}/content              → bare listing
//   - GET  /nodes/{n}/storage/{sid}/content/{iso|vztmpl} → PVE 9.2 quirk:
//     500 "unable to
//     parse directory
//     volume name"
//   - POST /nodes/{n}/storage/{sid}/download-url         → PVE 9.2 artifact
//     download (url,
//     filename, content)
//   - POST /nodes/{n}/storage/{sid}/download             → PVE 9.2 realism:
//     501 "Method not
//     implemented" (the
//     PVE 8-era path;
//     renamed upstream)
func (s *Server) storageRoute(w http.ResponseWriter, r *http.Request, node, rest string) {
	// GET /nodes/{n}/storage (no backend id) -> storage backend listing.
	// This arrives when serve()'s split() finds no second segment, in which
	// case rest == "" is handled there; the `after == "storage"` case
	// covers requests of the form /nodes/{n}/storage that the serve()
	// switch misclassifies.
	if rest == "storage" && r.Method == http.MethodGet {
		s.handleNodeStorageListings(w, node)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] != "storage" {
		writeErr(w, http.StatusNotFound, "expected /storage/{id}/...")
		return
	}
	sid := parts[1]

	switch {
	// GET /storage/{sid}/content/iso OR /content/vztmpl → PVE 9.2 quirk:
	// dir storage's `content/{type}` 500 with "unable to parse directory
	// volume name '{type}'". The mock replicates this so tests exercise
	// the correct bare-content path.
	case len(parts) == 4 && parts[2] == "content":
		writeErr(w, http.StatusInternalServerError,
			fmt.Sprintf("unable to parse directory volume name '%s'\n", parts[3]))

	// GET /storage/{sid}/content → bare listing (works on PVE 9.2). Returns
	// every entry across all content pools, tagged with the entry's
	// `content` field so clients can filter.
	case len(parts) == 3 && parts[2] == "content":
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out := make([]map[string]any, 0, 8)
		s.mu.Lock()
		if sts := s.isos[node][sid]; sts != nil {
			for f := range sts {
				out = append(out, map[string]any{
					"volid":   sid + ":iso/" + f,
					"content": "iso",
					"format":  "iso",
					"size":    int64(1024 * 1024),
				})
			}
		}
		if sts := s.templates[node][sid]; sts != nil {
			for f := range sts {
				out = append(out, map[string]any{
					"volid":   sid + ":vztmpl/" + f,
					"content": "vztmpl",
					"format":  "tzst",
					"size":    int64(64 * 1024 * 1024),
				})
			}
		}
		// PVE 9 `import` content pool (qcow2/vmdk/raw disk images).
		if sts := s.images[node][sid]; sts != nil {
			for f := range sts {
				out = append(out, map[string]any{
					"volid":   sid + ":import/" + f,
					"content": "import",
					"format":  "qcow2",
					"size":    int64(330 * 1024 * 1024),
				})
			}
		}
		// LVM/disk volumes: PVE lists these under LVM storage with
		// `content` of "rootdir" (CT allocation) or "images" (VM disks).
		// adopt uses these to recover CT rootfs/mp sizes.
		if vols := s.lvmVols[node][sid]; vols != nil {
			for vid, v := range vols {
				out = append(out, map[string]any{
					"volid":   vid,
					"content": v.Content,
					"format":  "raw",
					"size":    v.Size,
				})
			}
		}
		s.mu.Unlock()
		// Deterministic order for content listings.
		sort.Slice(out, func(i, j int) bool {
			vi, _ := out[i]["volid"].(string)
			vj, _ := out[j]["volid"].(string)
			return vi < vj
		})
		writeOK(w, out)

	// PVE 9.2 contract (probed live on conformance-dev): the artifact
	// download endpoint is POST /storage/{sid}/download-url with form
	// params url, filename, content ("iso" or "vztmpl"). The old PVE 8-era
	// path /storage/{sid}/download returns 501 "Method ... not implemented"
	// on PVE 9.2 dir storage. The mock mirrors both so that regression
	// tests catch future flips.
	case len(parts) == 3 && parts[2] == "download-url":
		if r.Method != http.MethodPost {
			// PVE returns 501 for GET /download-url.
			writeErr(w, http.StatusNotImplemented, "Method 'GET /nodes/"+node+"/storage/"+sid+"/download-url' not implemented")
			return
		}
		s.mu.Lock()
		fail := s.downloadFail
		s.mu.Unlock()
		if fail {
			// 400 → pveclient Do() returns immediately (no transient retry),
			// so tests that exercise failed-prerequisite deferral stay fast.
			writeErr(w, http.StatusBadRequest, "download failed (mock forced failure)")
			return
		}
		_ = r.ParseForm()
		filename := r.PostFormValue("filename")
		if filename == "" {
			writeErr(w, http.StatusBadRequest, "filename required for download")
			return
		}
		content := r.PostFormValue("content")
		// PVE 9.2 validates the filename extension against the content pool
		// (probed on conformance-dev 2026-09-13): import accepts
		// .qcow2/.vmdk/.raw; iso accepts .iso/.img; vztmpl accepts the tar
		// family. Anything else is 400 "invalid filename or wrong extension".
		if content == "import" {
			lc := strings.ToLower(filename)
			if !strings.HasSuffix(lc, ".qcow2") && !strings.HasSuffix(lc, ".vmdk") && !strings.HasSuffix(lc, ".raw") {
				writeErr(w, http.StatusBadRequest, "invalid filename or wrong extension")
				return
			}
		}
		s.mu.Lock()
		switch content {
		case "vztmpl":
			if s.templates[node] == nil {
				s.templates[node] = map[string]map[string]bool{}
			}
			if s.templates[node][sid] == nil {
				s.templates[node][sid] = map[string]bool{}
			}
			s.templates[node][sid][filename] = true
			s.tdl++
		case "import":
			if s.images[node] == nil {
				s.images[node] = map[string]map[string]bool{}
			}
			if s.images[node][sid] == nil {
				s.images[node][sid] = map[string]bool{}
			}
			s.images[node][sid][filename] = true
			s.tdl++
		default: // "" | "iso"
			if s.isos[node] == nil {
				s.isos[node] = map[string]map[string]bool{}
			}
			if s.isos[node][sid] == nil {
				s.isos[node][sid] = map[string]bool{}
			}
			s.isos[node][sid][filename] = true
			s.isl++
		}
		upid := s.newTaskLocked(node, "download")
		s.mu.Unlock()
		writeOK(w, upid)

	// Legacy PVE 8-era /download path. PVE 9.2 dir storage returns 501
	// "Method 'POST /nodes/.../storage/.../download' not implemented" —
	// the endpoint was renamed /download-url. The mock mirrors this
	// realism so any regression to the old path pokes 501 on the mock
	// exactly as it would against PVE 9.2.
	case len(parts) == 3 && parts[2] == "download":
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusNotImplemented,
				fmt.Sprintf("Method '%s /nodes/%s/storage/%s/download' not implemented", r.Method, node, sid))
			return
		}
		writeErr(w, http.StatusNotImplemented,
			fmt.Sprintf("Method 'POST /nodes/%s/storage/%s/download' not implemented — PVE 9.2 moved artifact download to /download-url", node, sid))

	// GET /storage/{sid} → storage info (also catches longer unknown tails
	// conservatively: only the bare form matches, the rest 404).
	case len(parts) == 2:
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeOK(w, map[string]any{
			"storage": sid,
			"node":    node,
			"active":  true,
			"content": []string{"iso", "vztmpl"},
			"type":    "dir",
			"format":  "file",
			"path":    "/var/lib/vz/template",
		})

	default:
		writeErr(w, http.StatusNotFound, "unknown storage endpoint: /"+rest)
	}
}

// taskRoute handles /nodes/{n}/tasks/{upid}/status.
func (s *Server) taskRoute(w http.ResponseWriter, r *http.Request, node, rest string) {
	upid, sub, ok := s.split(rest)
	if !ok || sub != "status" {
		writeErr(w, http.StatusNotFound, "expected /tasks/{upid}/status")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_ = node
	s.mu.Lock()
	t, ok := s.tasks[upid]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusInternalServerError, "no such task "+upid)
		return
	}
	var out map[string]any
	if t.ticks > 0 {
		t.ticks--
		out = map[string]any{"status": "running"}
	} else {
		out = map[string]any{"status": "stopped", "exitstatus": "OK", "progress": -1}
		delete(s.tasks, upid)
	}
	s.mu.Unlock()
	writeOK(w, out)
}

func (s *Server) newTaskLocked(node, kind string) string {
	s.taskSeq++
	upid := fmt.Sprintf("%s@mock!%s-%d-%d", node, kind, s.taskSeq, time.Now().UnixNano()%1000)
	s.tasks[upid] = &task{ticks: s.cfg.TaskTicks}
	return upid
}

// --- envelope helpers ---

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": msg})
}
