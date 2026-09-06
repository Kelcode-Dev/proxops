package pveclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// VMConfig mirrors the PVE VM (qemu) config response. Keys are heterogeneous,
// so we keep a generic map and add typed helpers where the reconciler needs
// them (in M2/M3).
type VMConfig map[string]any

// VMStatus mirrors /nodes/{n}/qemu/{vmid}/status-current. Memory fields are in
// bytes.
type VMStatus struct {
	VMID     int      `json:"vmid"`
	Status   string   `json:"status"` // running | stopped | paused
	Maxmem   int64    `json:"maxmem"`
	CPUs     int      `json:"cpus"`
	CPUUtil  *float64 `json:"cpu"`
	MemUtil  *float64 `json:"mem"`
	DiskUtil *float64 `json:"disk"`
	NetRx    *float64 `json:"netin"`
	NetTx    *float64 `json:"netout"`
	Uptime   *float64 `json:"uptime"`
}

// IsStopped reports whether the VM is not running.
func (s VMStatus) IsStopped() bool { return s.Status == "stopped" }

// VM bundles the /nodes/{n}/qemu/{vmid} endpoints.
//
// Mutating operations return the PVE task UPID ("" when PVE performs the
// change synchronously) so callers can drive a TaskWaiter when they need to
// block on completion.
type VM struct {
	c *Client
}

// VM returns a VM handle.
func (c *Client) VM() *VM { return &VM{c: c} }

func vmBase(node string, vmid int) string {
	return "nodes/" + node + "/qemu/" + strconv.Itoa(vmid)
}

// Create submits a vmcreate. `params` carries PVE snake_case keys (name,
// memory, cpu, cores, net0, ...). start=true also boots the VM.
func (vm *VM) Create(ctx context.Context, node string, params url.Values, start bool) (string, error) {
	if start {
		params.Set("start", "true")
	}
	return vm.c.Do(ctx, http.MethodPost, node, "nodes/"+node+"/qemu", params, nil)
}

// Get retrieves a VM's full config. Not found → *APIError with IsNotFound.
func (vm *VM) Get(ctx context.Context, node string, vmid int) (VMConfig, error) {
	out := VMConfig{}
	if _, err := vm.c.Do(ctx, http.MethodGet, node, vmBase(node, vmid)+"/config", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Update performs a config change (POST /qemu/{v}/config). Must exclude vmid.
func (vm *VM) Update(ctx context.Context, node string, vmid int, params url.Values) (string, error) {
	return vm.c.Do(ctx, http.MethodPost, node, vmBase(node, vmid)+"/config", params, nil)
}

// Start boots a stopped VM.
func (vm *VM) Start(ctx context.Context, node string, vmid int) (string, error) {
	return vm.c.Do(ctx, http.MethodPost, node, vmBase(node, vmid)+"/status/start", nil, nil)
}

// Stop issues a hard stop.
func (vm *VM) Stop(ctx context.Context, node string, vmid int) (string, error) {
	return vm.c.Do(ctx, http.MethodPost, node, vmBase(node, vmid)+"/status/stop", nil, nil)
}

// Shutdown requests a graceful ACPI shutdown.
func (vm *VM) Shutdown(ctx context.Context, node string, vmid int) (string, error) {
	return vm.c.Do(ctx, http.MethodPost, node, vmBase(node, vmid)+"/status/shutdown", nil, nil)
}

// Reboot restarts the guest OS.
func (vm *VM) Reboot(ctx context.Context, node string, vmid int) (string, error) {
	return vm.c.Do(ctx, http.MethodPost, node, vmBase(node, vmid)+"/status/reboot", nil, nil)
}

// Status returns the live VM status.
func (vm *VM) Status(ctx context.Context, node string, vmid int) (VMStatus, error) {
	var out VMStatus
	if _, err := vm.c.Do(ctx, http.MethodGet, node, vmBase(node, vmid)+"/status/current", nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Delete removes a stopped VM. PVE 9.x exposes VM destruction as REST
// DELETE /nodes/{n}/qemu/{v} — the legacy POST /qemu/{v}/vmdelete endpoint
// is "not implemented" in PVE 9.2. The VM must already be stopped; the
// executor performs a pre-delete stop (PVE refuses to destroy a running
// object; we never send "force").
func (vm *VM) Delete(ctx context.Context, node string, vmid int) (string, error) {
	return vm.c.Do(ctx, http.MethodDelete, node, vmBase(node, vmid), nil, nil)
}

// Resize adjusts online RAM in MiB (POST /qemu/{v}/resize) — the same unit as
// PVE's static `memory` config field. Synchronous on PVE; returns an empty upID.
func (vm *VM) Resize(ctx context.Context, node string, vmid int, memoryMib int64) (string, error) {
	p := url.Values{"memory": {strconv.FormatInt(memoryMib, 10)}}
	return vm.c.Do(ctx, http.MethodPost, node, vmBase(node, vmid)+"/resize", p, nil)
}
