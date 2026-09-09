package pveclient

import (
	"context"
	"net/http"
)

// This file adds the read-only "list" surfaces needed by `adopt` (reverse
// engineering live PVE into pveconform YAML):
//
//   - the PVE node list (adopt scans only the configured node allowlist);
//   - per-node VM / LXC listings;
//   - the per-node storage backends and, via Storage, their content;
//   - a write counter so tests can prove adopt performed zero PVE writes.
//
// No write endpoint is added here. adoption is a GET-only workflow.

// VMListEntry is one row of GET /nodes/{n}/qemu.
type VMListEntry struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Tags   string `json:"tags"`
}

// LXCListEntry is one row of GET /nodes/{n}/lxc.
//
// PVE's LXC listing uses the shared PVE object-id namespace: the key is
// `vmid` (even though some LXC config endpoints use `cid` elsewhere). The
// mock PVE mirrors this so pveconform can consume one shape.
type LXCListEntry struct {
	CID    int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Tags   string `json:"tags"`
}

// Nodes lists the PVE cluster's node names.
// GET /cluster/nodes (read-only).
func (c *Client) Nodes(ctx context.Context) ([]string, error) {
	var out []struct {
		Node string `json:"node"`
	}
	if _, err := c.Do(ctx, http.MethodGet, gatewayLabel, "cluster/nodes", nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for _, n := range out {
		names = append(names, n.Node)
	}
	return names, nil
}

// VMList lists the qemu VMs on one node.
// GET /nodes/{n}/qemu (read-only). PVE returns one entry per VM with
// vmid, name, status and tags.
func (c *Client) VMList(ctx context.Context, node string) ([]VMListEntry, error) {
	var out []VMListEntry
	if _, err := c.Do(ctx, http.MethodGet, node, "nodes/"+node+"/qemu", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LXCList lists the Linux containers on one node.
// GET /nodes/{n}/lxc (read-only). PVE returns one entry per CT with
// `vmid` + `name`, `status`, `tags`.
func (c *Client) LXCList(ctx context.Context, node string) ([]LXCListEntry, error) {
	var out []LXCListEntry
	if _, err := c.Do(ctx, http.MethodGet, node, "nodes/"+node+"/lxc", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StorageList lists the storage backends configured on one node.
// GET /nodes/{n}/storage (read-only). Each entry's `storage` key is the
// PVE storage id used in volume ids (e.g. "local", "local-lvm"). PVE also
// emits an `id` alias on some builds; the `storage` key is the canonical
// one.
func (c *Client) StorageList(ctx context.Context, node string) ([]string, error) {
	var out []struct {
		Storage string `json:"storage"`
		ID      string `json:"id"`
	}
	if _, err := c.Do(ctx, http.MethodGet, node, "nodes/"+node+"/storage", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out))
	seen := map[string]bool{}
	for _, s := range out {
		id := s.Storage
		if id == "" {
			id = s.ID
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// WritesPerformed returns the number of PVE write requests the client has
// issued since construction (one count per POST/PUT/DELETE attempt, not per
// retry). A purely read-only caller — `adopt` — must observe 0, and tests
// pin that.
func (c *Client) WritesPerformed() uint64 {
	return c.writesMonotonic.Load()
}
