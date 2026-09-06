package pveclient_test

import (
	"context"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient/mock"
)

// newClientToken wires a pveclient with API-token auth at the mock root.
func newClientToken(m *mock.Server, tokenValue string) *pveclient.Client {
	o := pveclient.Options{
		PVE: pveclient.PVEParams{
			User:       "root@pam",
			Auth:       "token",
			TokenValue: tokenValue,
		},
		HTTPTimeout: 2 * time.Second,
		BaseURL:     m.URL(),
	}
	c, err := pveclient.New(o, slog.Default())
	if err != nil {
		panic(err)
	}
	return c
}

// newClientTicket wires a pveclient with ticket auth against the mock.
func newClientTicket(m *mock.Server, user, pass string) *pveclient.Client {
	o := pveclient.Options{
		PVE: pveclient.PVEParams{
			User:     user,
			Auth:     "ticket",
			Password: pass,
		},
		HTTPTimeout: 2 * time.Second,
		BaseURL:     m.URL(),
	}
	c, err := pveclient.New(o, slog.Default())
	if err != nil {
		panic(err)
	}
	return c
}

// TestTokenAuthReads: PVEAPIToken is accepted; config read returns stored fields.
func TestTokenAuthReads(t *testing.T) {
	m := mock.New(mock.Config{Token: "root@pam!pveconform=deadbeef"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{"name": "present", "memory": "4096"}, "stopped")

	c := newClientToken(m, "root@pam!pveconform=deadbeef")
	cfg, err := c.VM().Get(context.Background(), "pve", 100)
	if err != nil {
		t.Fatalf("Get(100): %v", err)
	}
	if cfg["name"] != "present" {
		t.Errorf("cfg[name] = %v, want present", cfg["name"])
	}
}

// TestWrongTokenRejected: a token that does not match the mock's 401s.
func TestWrongTokenRejected(t *testing.T) {
	m := mock.New(mock.Config{Token: "the-right-token"})
	defer m.Close()
	c := newClientToken(m, "root@pam!bogus=nope")

	_, err := c.ClusterResources(context.Background())
	if err == nil {
		t.Fatal("expected auth failure")
	}
}

// TestTicketAuthFlow: password exchange yields a cookie that authenticates reads.
func TestTicketAuthFlow(t *testing.T) {
	m := mock.New(mock.Config{TicketUser: "u", TicketPassword: "p"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{"name": "via-ticket"}, "stopped")

	c := newClientTicket(m, "u", "p")
	if _, err := c.VM().Get(context.Background(), "pve", 100); err != nil {
		t.Fatalf("Get via ticket: %v", err)
	}
}

// TestTicketAuthWrongPassword: bad password fails at the ticket exchange.
func TestTicketAuthWrongPassword(t *testing.T) {
	m := mock.New(mock.Config{TicketUser: "u", TicketPassword: "p"})
	defer m.Close()
	c := newClientTicket(m, "u", "WRONG")

	_, err := c.ClusterResources(context.Background())
	if err == nil {
		t.Fatal("expected ticket exchange failure")
	}
}

// TestVMCreateThenRead: async create returns a UPID; after the task settles the
// VM is present with the stored config.
func TestVMCreateThenRead(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok", TaskTicks: 2})
	defer m.Close()
	c := newClientToken(m, "tok")

	params := url.Values{
		"vmid":   {"101"},
		"name":   {"new-vm"},
		"memory": {"8192"},
		"cores":  {"2"},
		"scsi0":  {"local:vm-101-disk-0,size=50G"},
	}
	upid, err := c.VM().Create(context.Background(), "pve", params, false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if upid == "" {
		t.Fatal("Create returned empty UPID")
	}

	w := pveclient.NewTaskWaiter(c, slog.Default())
	w.SetInterval(25 * time.Millisecond)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := w.Wait(sctx, pveclient.TaskID{Node: "pve", UPID: upid})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !st.IsOK() {
		t.Fatalf("task not OK: %+v", st)
	}

	cfg, err := c.VM().Get(context.Background(), "pve", 101)
	if err != nil {
		t.Fatalf("Get(101): %v", err)
	}
	if cfg["name"] != "new-vm" {
		t.Errorf("cfg[name] = %v, want new-vm", cfg["name"])
	}
}

// TestCreateDuplicateFails: creating an existing VM id errors.
func TestCreateDuplicateFails(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{"name": "orig"}, "stopped")
	c := newClientToken(m, "tok")

	_, err := c.VM().Create(context.Background(), "pve", url.Values{"vmid": {"100"}}, false)
	if err == nil {
		t.Fatal("expected duplicate-create failure")
	}
}

// TestGetAbsentVMNotFound: "no such vm" maps to IsNotFound.
func TestGetAbsentVMNotFound(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	c := newClientToken(m, "tok")

	_, err := c.VM().Get(context.Background(), "pve", 4242)
	if err == nil {
		t.Fatal("expected error for absent VM")
	}
	if !pveclient.IsNotFound(err) {
		t.Errorf("expected IsNotFound, got: %v", err)
	}
}

// TestUpdateConfig: POST config lands in stored state and completes the task.
func TestUpdateConfig(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{"name": "x"}, "stopped")
	c := newClientToken(m, "tok")

	upid, err := c.VM().Update(context.Background(), "pve", 100, url.Values{"memory": {"16384"}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	w := pveclient.NewTaskWaiter(c, slog.Default())
	w.SetInterval(10 * time.Millisecond)
	if _, err := w.Wait(context.Background(), pveclient.TaskID{Node: "pve", UPID: upid}); err != nil {
		t.Fatalf("Wait update task: %v", err)
	}
	if got := m.VMConfig("pve", 100)["memory"]; got != "16384" {
		t.Errorf("stored memory = %q, want 16384", got)
	}
}

// TestStartStopTransitions: start/stop update PVE status; already-X is rejected.
func TestStartStopTransitions(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{}, "stopped")
	c := newClientToken(m, "tok")

	if _, err := c.VM().Start(context.Background(), "pve", 100); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st, _ := m.VMStatus("pve", 100); st != "running" {
		t.Errorf("status = %q, want running", st)
	}
	// start again → PVE rejects
	if _, err := c.VM().Start(context.Background(), "pve", 100); err == nil {
		t.Error("expected 'already running' error")
	}
	if _, err := c.VM().Stop(context.Background(), "pve", 100); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st, _ := m.VMStatus("pve", 100); st != "stopped" {
		t.Errorf("status = %q, want stopped", st)
	}
}

// TestVMStatusCurrent: status-current reflects power state + config memory.
func TestVMStatusCurrent(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{"memory": "4096"}, "running")
	c := newClientToken(m, "tok")

	st, err := c.VM().Status(context.Background(), "pve", 100)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.VMID != 100 || st.Status != "running" {
		t.Errorf("status = %+v", st)
	}
}

// TestDeleteRunningVMRefused: PVE refuses to delete a running VM (no force in MVP).
func TestDeleteRunningVMRefused(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	m.PreloadVM("pve", 100, map[string]string{}, "running")
	c := newClientToken(m, "tok")

	if _, err := c.VM().Delete(context.Background(), "pve", 100); err == nil {
		t.Fatal("expected delete-refused error for running VM")
	}
	// stop then delete → gone
	c.VM().Stop(context.Background(), "pve", 100)
	if _, err := c.VM().Delete(context.Background(), "pve", 100); err != nil {
		t.Fatalf("Delete after stop: %v", err)
	}
	if m.VMExists("pve", 100) {
		t.Fatal("VM still present after delete")
	}
}

// TestLXCCreateAndUpdate: container lifecycle mirrors VMs.
func TestLXCCreateAndUpdate(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	c := newClientToken(m, "tok")

	_, err := c.LXC().Create(context.Background(), "pve", url.Values{
		"vmid":   {"200"},
		"name":   {"ct-200"},
		"memory": {"2048"},
	}, false)
	if err != nil {
		t.Fatalf("LXC Create: %v", err)
	}
	if !m.CTExists("pve", 200) {
		t.Fatal("LXC 200 not present after create")
	}
	if _, err := c.LXC().Update(context.Background(), "pve", 200, url.Values{"description": {"updated"}}); err != nil {
		t.Fatalf("LXC Update: %v", err)
	}
	if got := m.CTConfig("pve", 200)["description"]; got != "updated" {
		t.Errorf("description = %q, want updated", got)
	}
}

// TestTaskTimeout: waiting on a long-lived task with a short deadline yields a
// TaskTimeoutError, not a failure.
func TestTaskTimeout(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok", TaskTicks: 1000})
	defer m.Close()
	c := newClientToken(m, "tok")
	m.PreloadVM("pve", 100, map[string]string{}, "stopped")

	upid, err := c.VM().Update(context.Background(), "pve", 100, url.Values{"name": {"x"}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	w := pveclient.NewTaskWaiter(c, slog.Default())
	w.SetInterval(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_, err = w.Wait(ctx, pveclient.TaskID{Node: "pve", UPID: upid})
	if !pveclient.IsTimeout(err) {
		t.Fatalf("expected TaskTimeoutError, got: %v", err)
	}
}

// TestClusterResourcesListings: preloaded VMs appear per node in the listing.
func TestClusterResourcesListings(t *testing.T) {
	m := mock.New(mock.Config{Token: "tok"})
	defer m.Close()
	m.PreloadVM("n1", 100, map[string]string{"name": "a"}, "stopped")
	m.PreloadVM("n2", 101, map[string]string{"name": "b"}, "running")
	c := newClientToken(m, "tok")

	res, err := c.ClusterResources(context.Background())
	if err != nil {
		t.Fatalf("ClusterResources: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d resources, want 2: %+v", len(res), res)
	}
}
