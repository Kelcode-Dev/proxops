// Package mock implements a stateful in-memory PVE API server for tests.
//
// It is not a full PVE reimplementation — it implements the routes pveclient
// and the reconciler actually use, with PVE-compatible semantics:
//
//   - token auth via Authorization: PVEAPIToken=<value>
//   - ticket auth via POST /access/ticket then cookie (PVEAuthCookie)
//   - async task lifecycles (create / update / power / delete return a UPID;
//     /tasks/<upid>/status settles after a configured number of polls)
//   - per-node VM (qemu) and LXC container state; PVE-style "no such vm"
//     errors (HTTP 500 with that message) for unknown object IDs
//
// The mock is plain HTTP; tests point pveclient.Options.BaseURL at the mock
// root so no TLS is present.

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
	// Token, when set, authorizes requests via Authorization: PVEAPIToken=<Token>.
	Token string
	// TicketUser/TicketPassword authorize POST /access/ticket.
	TicketUser     string
	TicketPassword string
	// TaskTicks is the number of /status polls a task takes before settling to
	// exitstatus OK. 0 or negative → 1.
	TaskTicks int
}

// VM is one mock VM/CT record. The mock stores both under a single per-node
// VM is one mock record. VM and LXC share storage (PVE's per-node id space is
// unified), distinguished by Kind.
type VM struct {
	ID     int
	Kind   string // "qemu" or "lxc"
	Config map[string]string
	Status string // "running" | "stopped"
}

// Server is the stateful fake PVE.
type Server struct {
	mu      sync.Mutex
	cfg     Config
	tickets map[string]bool
	vms     map[string]map[int]*VM
	tasks   map[string]*uptask
	taskSeq int

	created, updated, deleted int

	ts *httptest.Server
}

type uptask struct{ ticks int }

// New starts the mock and returns it. Call Close to stop.
func New(cfg Config) *Server {
	if cfg.TaskTicks <= 0 {
		cfg.TaskTicks = 1
	}
	s := &Server{
		cfg:     cfg,
		tickets: map[string]bool{},
		vms:     map[string]map[int]*VM{},
		tasks:   map[string]*uptask{},
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL returns the mock root, e.g. "http://127.0.0.1:PORT".
func (s *Server) URL() string { return s.ts.URL }

// Close stops the mock server.
func (s *Server) Close() { s.ts.Close() }

// --- test setup/introspection helpers ---

func (s *Server) state(node string) map[int]*VM {
	m, ok := s.vms[node]
	if !ok {
		m = map[int]*VM{}
		s.vms[node] = m
	}
	return m
}

// PreloadVM seeds a VM on a node.
func (s *Server) PreloadVM(node string, vmid int, cfg map[string]string, status string) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state(node)[vmid] = &VM{ID: vmid, Kind: "qemu", Config: cfg, Status: status}
}

// PreloadCT seeds an LXC container on a node.
func (s *Server) PreloadCT(node string, cid int, cfg map[string]string, status string) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state(node)[cid] = &VM{ID: cid, Kind: "lxc", Config: cfg, Status: status}
}

// VMConfig returns the live config of a VM (nil if absent).
func (s *Server) VMConfig(node string, vmid int) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.vms[node][vmid]; ok {
		return v.Config
	}
	return nil
}

// VMStatus returns the current status of a VM, false if absent.
func (s *Server) VMStatus(node string, vmid int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.vms[node][vmid]; ok {
		return v.Status, true
	}
	return "", false
}

// VMExists tells whether a VM/CT is present on a node.
func (s *Server) VMExists(node string, vmid int) bool {
	_, ok := s.VMStatus(node, vmid)
	return ok
}

// CTExists is an alias of VMExists for the LXC case.
func (s *Server) CTExists(node string, cid int) bool { return s.VMExists(node, cid) }

// CTConfig is an alias of VMConfig for the LXC case.
func (s *Server) CTConfig(node string, cid int) map[string]string { return s.VMConfig(node, cid) }

// --- request routing ---

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api2/json/")

	switch {
	case rest == "access/ticket":
		s.handleTicket(w, r)
		return
	case rest == "version":
		writeData(w, map[string]any{"version": "8.2", "release": "mock"})
		return
	}

	if !s.authorized(r) {
		writeErr(w, 401, "authentication error")
		return
	}

	if rest == "cluster/resources" {
		s.handleClusterResources(w, r)
		return
	}
	if !strings.HasPrefix(rest, "nodes/") {
		writeErr(w, 404, "unknown endpoint /"+rest)
		return
	}
	node, after, ok := cutField(rest[len("nodes/"):])
	if !ok {
		writeErr(w, 404, "malformed node path")
		return
	}
	kind, rest2, ok := cutField(after)
	if !ok {
		// after was the complete path with no trailing slash: "qemu" or "lxc".
		kind, rest2 = after, ""
	}
	s.mu.Lock()
	// Lazily ensure node state exists.
	if _, ok := s.vms[node]; !ok {
		s.vms[node] = map[int]*VM{}
	}
	s.mu.Unlock()

	switch kind {
	case "qemu", "lxc":
		s.vmRoute(w, r, node, kind, rest2)
	case "tasks":
		s.taskRoute(w, rest2)
	default:
		writeErr(w, 404, "unknown kind "+kind)
	}
}

// cutField splits "head/tail" on the first '/'; ok is true when a slash was
// found (so tail may still be empty, e.g. "100/").
func cutField(s string) (head, tail string, ok bool) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
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
		writeErr(w, 405, "method not allowed")
		return
	}
	_ = r.ParseForm()
	user := r.PostFormValue("user")
	if user == "" {
		user = r.PostFormValue("username")
	}
	pass := r.PostFormValue("password")
	if user != s.cfg.TicketUser || pass != s.cfg.TicketPassword {
		writeErr(w, 401, "authentication error")
		return
	}
	tkt := fmt.Sprintf("ticket-%d", time.Now().UnixNano())
	s.mu.Lock()
	s.tickets[tkt] = true
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "PVEAuthCookie", Value: tkt, HttpOnly: true})
	writeData(w, map[string]any{
		"ticket":      tkt,
		"CSRFToken":   "csrf-" + tkt,
		"valid_until": time.Now().Add(2 * time.Hour).Unix(),
	})
}

func (s *Server) handleClusterResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	s.mu.Lock()
	type row struct {
		node string
		vm   *VM
	}
	var rows []row
	for n, m := range s.vms {
		for _, v := range m {
			rows = append(rows, row{n, v})
		}
	}
	s.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].node != rows[j].node {
			return rows[i].node < rows[j].node
		}
		return rows[i].vm.ID < rows[j].vm.ID
	})
	out := make([]map[string]any, 0, len(rows))
	for _, rw := range rows {
		// PVE's real /cluster/resources uses "type" with values "vm"/"ct".
		ptype := "vm"
		if rw.vm.Kind == "lxc" {
			ptype = "ct"
		}
		out = append(out, map[string]any{
			"node":   rw.node,
			"vmid":   rw.vm.ID,
			"type":   ptype,
			"status": rw.vm.Status,
			"name":   rw.vm.Config["name"],
			"tags":   rw.vm.Config["tags"],
		})
	}
	writeData(w, out)
}

func (s *Server) taskRoute(w http.ResponseWriter, rest string) {
	upid, tail, ok := cutField(rest)
	if !ok || tail != "status" {
		writeErr(w, 404, "expected /tasks/<upid>/status")
		return
	}
	s.mu.Lock()
	t, ok := s.tasks[upid]
	if !ok {
		s.mu.Unlock()
		writeErr(w, 500, "no such task: "+upid)
		return
	}
	var data map[string]any
	if t.ticks > 0 {
		t.ticks--
		data = map[string]any{"status": "running", "progress": 0.5}
	} else {
		data = map[string]any{"status": "stopped", "exitstatus": "OK", "progress": -1}
		delete(s.tasks, upid)
	}
	s.mu.Unlock()
	writeData(w, data)
}

// vmRoute handles /nodes/<n>/{qemu|lxc}[/<id>[/<action>]].
func (s *Server) vmRoute(w http.ResponseWriter, r *http.Request, node, kind, rest string) {
	isLXC := kind == "lxc"

	// POST /nodes/<n>/{qemu|lxc} → create (no id in path).
	if rest == "" {
		s.create(w, r, node, isLXC)
		return
	}

	idStr, after, ok := cutField(rest)
	if !ok {
		// /nodes/<n>/<kind>/<id> (no trailing action): GET config / DELETE whole.
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeErr(w, 400, "bad object id "+idStr)
			return
		}
		s.wholeObject(w, r, node, id, isLXC)
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeErr(w, 400, "bad object id "+idStr)
		return
	}

	switch {
	case after == "":
		s.wholeObject(w, r, node, id, isLXC)
	case after == "config":
		s.getObjectConfig(w, r, node, id, isLXC)
	case strings.HasPrefix(after, "status/"):
		s.getStatusOrPower(w, r, node, id, isLXC, strings.TrimPrefix(after, "status/"))
	case after == "vmdelete":
		s.deleteVM(w, r, node, id)
	default:
		writeErr(w, 404, "unknown action "+after)
	}
	_ = ok
}

// wholeObject handles GET config and DELETE for a bare /<kind>/<id>.
func (s *Server) wholeObject(w http.ResponseWriter, r *http.Request, node string, id int, isLXC bool) {
	switch r.Method {
	case http.MethodGet:
		s.getObjectConfig(w, r, node, id, isLXC)
	case http.MethodDelete:
		if !isLXC {
			// VMs use POST /vmdelete, not DELETE on the object.
			writeErr(w, 404, "use vmdelete for qemu objects")
			return
		}
		s.deleteObject(w, r, node, id)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// getObjectConfig GETs the object's config, or POSTs a config update.
func (s *Server) getObjectConfig(w http.ResponseWriter, r *http.Request, node string, id int, _ bool) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		v, ok := s.vms[node][id]
		if !ok {
			s.mu.Unlock()
			writeErr(w, 500, "no such vm "+strconv.Itoa(id))
			return
		}
		out := map[string]any{"vmid": id}
		for k, val := range v.Config {
			if k == "tags" || k == "args" {
				// PVE returns list-valued config fields as JSON arrays.
				out[k] = splitList(val)
				continue
			}
			out[k] = val
		}
		out["status"] = v.Status
		s.mu.Unlock()
		writeData(w, out)
	case http.MethodPost:
		_ = r.ParseForm()
		s.mu.Lock()
		v, ok := s.vms[node][id]
		if !ok {
			s.mu.Unlock()
			writeErr(w, 500, "no such vm "+strconv.Itoa(id))
			return
		}
		for key, vals := range r.PostForm {
			if v.Config == nil {
				v.Config = map[string]string{}
			}
			if len(vals) > 0 {
				v.Config[key] = vals[0]
			}
		}
		upid := s.newTaskLocked()
		s.updated++
		s.mu.Unlock()
		writeData(w, upid)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// getStatusOrPower GETs status/current, or POSTs a power transition.
func (s *Server) getStatusOrPower(w http.ResponseWriter, r *http.Request, node string, id int, isLXC bool, action string) {
	switch {
	case action == "current":
		s.mu.Lock()
		v, ok := s.vms[node][id]
		if !ok {
			s.mu.Unlock()
			writeErr(w, 500, "no such vm "+strconv.Itoa(id))
			return
		}
		out := map[string]any{"status": v.Status}
		if idKey := "vmid"; isLXC {
			out["cid"] = id
		} else {
			out[idKey] = id
		}
		if mem, ok := v.Config["memory"]; ok {
			if n, e := strconv.ParseInt(mem, 10, 64); e == nil {
				out["maxmem"] = n * 1024 // KiB → bytes, so status-current is byte-typed
			}
		}
		s.mu.Unlock()
		writeData(w, out)
	case action == "start", action == "stop", action == "shutdown", action == "reboot":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		s.mu.Lock()
		v, ok := s.vms[node][id]
		if !ok {
			s.mu.Unlock()
			writeErr(w, 500, "no such vm "+strconv.Itoa(id))
			return
		}
		switch action {
		case "start":
			if v.Status == "running" {
				s.mu.Unlock()
				writeErr(w, 400, "vm "+strconv.Itoa(id)+" is already running")
				return
			}
			v.Status = "running"
		case "stop", "shutdown":
			if v.Status == "stopped" {
				s.mu.Unlock()
				writeErr(w, 400, "vm "+strconv.Itoa(id)+" is already stopped")
				return
			}
			v.Status = "stopped"
		case "reboot":
			// no persistent state change in the mock
		}
		upid := s.newTaskLocked()
		s.updated++
		s.mu.Unlock()
		writeData(w, upid)
	default:
		writeErr(w, 404, "unknown status action "+action)
	}
}

// create implements POST /nodes/<n>/{qemu|lxc}.
func (s *Server) create(w http.ResponseWriter, r *http.Request, node string, isLXC bool) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	_ = r.ParseForm()
	idRaw := r.PostFormValue("vmid")
	if idRaw == "" {
		idRaw = r.PostFormValue("ctid")
	}
	id, err := strconv.Atoi(idRaw)
	if err != nil || id <= 0 {
		writeErr(w, 400, "vmid required")
		return
	}
	_ = isLXC

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.vms[node][id]; exists {
		writeErr(w, 409, "vm id "+strconv.Itoa(id)+" already exists")
		return
	}
	cfg := map[string]string{}
	for k, vals := range r.PostForm {
		if k == "start" {
			continue
		}
		if len(vals) > 0 {
			cfg[k] = vals[0]
		}
	}
	status := "stopped"
	if start := r.PostFormValue("start"); start == "true" || start == "1" {
		status = "running"
	}
	kindStr := "qemu"
	if isLXC {
		kindStr = "lxc"
	}
	s.vms[node][id] = &VM{ID: id, Kind: kindStr, Config: cfg, Status: status}
	upid := s.newTaskLocked()
	s.created++
	writeData(w, upid)
}

// deleteVM implements POST /nodes/<n>/qemu/<id>/vmdelete.
func (s *Server) deleteVM(w http.ResponseWriter, r *http.Request, node string, id int) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	s.deleteObject(w, r, node, id)
}

// deleteObject removes a stopped object, mirroring PVE's refuse-while-running rule.
func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request, node string, id int) {
	s.mu.Lock()
	v, ok := s.vms[node][id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, 500, "no such vm "+strconv.Itoa(id))
		return
	}
	if v.Status == "running" {
		s.mu.Unlock()
		writeErr(w, 400, "vm "+strconv.Itoa(id)+" must be stopped before deletion")
		return
	}
	delete(s.vms[node], id)
	upid := s.newTaskLocked()
	s.deleted++
	s.mu.Unlock()
	writeData(w, upid)
}

// newTaskLocked registers a task and returns its UPID. Callers must hold s.mu.
func (s *Server) newTaskLocked() string {
	s.taskSeq++
	upid := fmt.Sprintf("pve@mock!task-%d", s.taskSeq)
	s.tasks[upid] = &uptask{ticks: s.cfg.TaskTicks}
	return upid
}

// --- envelope helpers ---

// writeData sends a PVE success envelope: {"data": ...}.
func writeData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// writeErr sends a PVE error envelope: {"errors": "..."} with an HTTP status.
func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": msg})
}

// splitList splits a PVE list-valued config field into a []any. PVE accepts
// both space- and comma-separated tag lists; the mock stores whichever form
// the client POSTed and returns it as a JSON array (matching PVE GET config).
func splitList(s string) []any {
	if s == "" {
		return nil
	}
	// Prefer comma if present, else space.
	if strings.Contains(s, ",") {
		var out []any
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	var out []any
	for _, p := range strings.Fields(s) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
