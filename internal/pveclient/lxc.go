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
func (l *LXC) Update(ctx context.Context, node string, cid int, params url.Values) (string, error) {
	return l.c.Do(ctx, http.MethodPost, node, lxcBase(node, cid)+"/config", params, nil)
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
