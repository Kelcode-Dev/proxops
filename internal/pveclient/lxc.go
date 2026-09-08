package pveclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// LXCConfig mirrors the PVE LXC container config response.
type LXCConfig map[string]any

// LXCStatus mirrors /nodes/{n}/lxc/{cid}/status-current.
type LXCStatus struct {
	CID      int      `json:"cid"`
	Status   string   `json:"status"` // running | stopped
	Maxmem   int64    `json:"maxmem"`
	MemUtil  *float64 `json:"mem"`
	DiskUtil *float64 `json:"disk"`
	Uptime   *float64 `json:"uptime"`
}

// IsStopped reports whether the container is not running.
func (s LXCStatus) IsStopped() bool { return s.Status == "stopped" }

// LXC bundles the /nodes/{n}/lxc/{cid} endpoints. Mutations return the task
// UPID where PVE is asynchronous.
type LXC struct {
	c *Client
}

// LXC returns an LXC handle.
func (c *Client) LXC() *LXC { return &LXC{c: c} }

func lxcBase(node string, cid int) string {
	return "nodes/" + node + "/lxc/" + strconv.Itoa(cid)
}

// Create submits a ctcreate. start=true boots the container after creation.
func (l *LXC) Create(ctx context.Context, node string, params url.Values, start bool) (string, error) {
	if start {
		params.Set("start", "true")
	}
	return l.c.Do(ctx, http.MethodPost, node, "nodes/"+node+"/lxc", params, nil)
}

// Get retrieves a container's full config.
func (l *LXC) Get(ctx context.Context, node string, cid int) (LXCConfig, error) {
	out := LXCConfig{}
	if _, err := l.c.Do(ctx, http.MethodGet, node, lxcBase(node, cid)+"/config", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Update performs a config change on a container.
//
// PVE verb: **PUT**, not POST. Probed on the conformance-dev PVE 9.2.2
// cluster 2026-09-08: `POST /nodes/{n}/lxc/{id}/config` returns
// `501 Method ... not implemented` while `PUT .../config` returns
// 200 (task UPID or null when nothing changed). LXC *power* ops
// (start/stop/reboot/shutdown) remain POST; only config-set moved to PUT
// in PVE 9.x.
func (l *LXC) Update(ctx context.Context, node string, cid int, params url.Values) (string, error) {
	return l.c.Do(ctx, http.MethodPut, node, lxcBase(node, cid)+"/config", params, nil)
}

// Start boots a stopped container.
func (l *LXC) Start(ctx context.Context, node string, cid int) (string, error) {
	return l.c.Do(ctx, http.MethodPost, node, lxcBase(node, cid)+"/status/start", nil, nil)
}

// Stop halts a running container.
func (l *LXC) Stop(ctx context.Context, node string, cid int) (string, error) {
	return l.c.Do(ctx, http.MethodPost, node, lxcBase(node, cid)+"/status/stop", nil, nil)
}

// Status returns the live container status.
func (l *LXC) Status(ctx context.Context, node string, cid int) (LXCStatus, error) {
	var out LXCStatus
	if _, err := l.c.Do(ctx, http.MethodGet, node, lxcBase(node, cid)+"/status/current", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Delete removes a container, stopping it first if it is running.
func (l *LXC) Delete(ctx context.Context, node string, cid int) (string, error) {
	if s, err := l.Status(ctx, node, cid); err == nil && !s.IsStopped() {
		if _, err := l.Stop(ctx, node, cid); err != nil {
			return "", err
		}
	}
	return l.c.Do(ctx, http.MethodDelete, node, lxcBase(node, cid), nil, nil)
}

// IsTemplate reports whether a container is flagged as a PVE template
// (config has "template" == "1").
func (l *LXC) IsTemplate(ctx context.Context, node string, cid int) (bool, error) {
	cfg, err := l.Get(ctx, node, cid)
	if err != nil {
		return false, err
	}
	v, ok := cfg["template"]
	if !ok {
		return false, nil
	}
	switch t := v.(type) {
	case string:
		return t == "1", nil
	case int:
		return t == 1, nil
	}
	return false, nil
}

// MarkTemplate promotes a stopped container to a template
// (POST /lxc/{cid}/template).
func (l *LXC) MarkTemplate(ctx context.Context, node string, cid int) (string, error) {
	return l.c.Do(ctx, http.MethodPost, node, lxcBase(node, cid)+"/template", nil, nil)
}

// UnmarkTemplate demotes a template back to a regular container
// (POST /lxc/{cid}/untemplate).
func (l *LXC) UnmarkTemplate(ctx context.Context, node string, cid int) (string, error) {
	return l.c.Do(ctx, http.MethodPost, node, lxcBase(node, cid)+"/untemplate", nil, nil)
}

// Clone clones a template into a new container id. PVE requires the source to
// be stopped or a template. Returns the PVE task UPID.
func (l *LXC) Clone(ctx context.Context, node string, src, dst int, params url.Values) (string, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("newid", strconv.Itoa(dst))
	// clone is POST /lxc/{src}/clone (async; returns a task UPID)
	return l.c.Do(ctx, http.MethodPost, node,
		"nodes/"+node+"/lxc/"+strconv.Itoa(src)+"/clone", params, nil)
}

// CTTemplate promotes a stopped, non-template container to a template when it
// is not one yet. Idempotent: a container that is already a template returns
// an empty upid string and nil error. PVE's /lxc/{cid}/template is only valid
// on stopped containers — callers must stop first when necessary.
func (l *LXC) CTTemplate(ctx context.Context, node string, cid int) (string, error) {
	ok, err := l.IsTemplate(ctx, node, cid)
	if err != nil {
		return "", err
	}
	if ok {
		return "", nil // already a template; no-op
	}
	return l.MarkTemplate(ctx, node, cid)
}

// CloneTemplate creates a new template from a source (usually a CTTemplate).
// This is the M4 CTTemplate reconcile primitive: desired is "a template
// exists with cid X", and we get it by cloning from a source id + marking the
// result as a template.
func (l *LXC) CloneTemplate(ctx context.Context, node string, src, dst int, params url.Values) (string, error) {
	up, err := l.Clone(ctx, node, src, dst, params)
	if err != nil {
		return up, err
	}
	return l.MarkTemplate(ctx, node, dst)
}
