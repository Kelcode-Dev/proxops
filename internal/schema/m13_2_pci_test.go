// M13.2: structured PCI passthrough schema tests.
//
// Pins:
//   - Parse/Validate of spec.hardware.pci-devices (BDF, slot, pcie).
//   - Wire render on create (hostpci0=0000:17:00,pcie=1) — the reference estate's
//     real shape MUST round-trip.
//   - Deterministic slot ordering on the wire.
//   - Drift: add / change hostpci entries; converged = no action.
//   - DriftAnomalies: a live hostpciN slot NOT in the manifest is a
//     "live-only PCI" anomaly (proxops never auto-strips a PVE-side PCI).
//   - Fail-closed: bad BDF, wrong slot, duplicate slots are rejected.
//   - PVE-side-only tokens (x-vga/rombar/mdev) are surfaced as a GAPS.md
//     limitation: ProxOps does not model them; a converging write replaces
//     the slot value so the unmodelled token drops (documented).
package schema_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// livePCI returns a PVE /config-shaped map converged against
// pciManifest's non-PCI fields (memory/cpu/cores/disks/nic/tags). Tests
// overlay hostpciN entries via the variadic overlay keys.
func livePCI(vmid int, overlay ...string) map[string]any {
	m := map[string]any{
		"memory": int64(1024),
		"cpu":    "host",
		"cores":  int64(1),
		"tags":   []any{"proxops"},
		"scsi0":  fmt.Sprintf("local-lvm:vm-%d-disk-0,size=4G", vmid),
		"net0":   "virtio=AA:BB:CC:00:00:01,bridge=vmbr0",
	}
	for _, b := range overlay {
		eq := strings.IndexByte(b, '=')
		if eq > 0 {
			m[b[:eq]] = b[eq+1:]
		}
	}
	return m
}

var (
	boolTrue  = true
	boolFalse = false
)

func pciManifest(vmid int, pcs []schema.PCIDevice) *schema.VM {
	v := schema.NewVM()
	v.Metadata.Name = fmt.Sprintf("pci-vm-%d", vmid)
	v.Spec.VMID = vmid
	v.Spec.Node = "pve01"
	v.Spec.CPU = schema.Cpu{Type: "host", Cores: 1}
	v.Spec.Memory = "1GiB"
	v.Spec.Disks = []schema.Disk{{Storage: "local-lvm", Size: "4GiB"}}
	v.Spec.NICs = []schema.NIC{{Model: "virtio", Bridge: "vmbr0"}}
	v.Spec.Hardware.PCIDevices = pcs
	return v
}

// TestPCI_ManifestWireRoundTrip pins the reference estate's actual shape:
//   hostpci0=0000:17:00,pcie=1
// MUST adopt cleanly, render cleanly, and read back cleanly with zero drift.
func TestPCI_ManifestWireRoundTrip(t *testing.T) {
	v := pciManifest(435, []schema.PCIDevice{{
		Slot:   "hostpci0",
		Device: "0000:17:00",
		PCIe:   &boolTrue,
	}})
	if err := v.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if p["hostpci0"] != "0000:17:00,pcie=1" {
		t.Fatalf("hostpci0 wire = %q, want \"0000:17:00,pcie=1\"", p["hostpci0"])
	}
	upd, stop, changed := v.Drift(livePCI(435, "hostpci0=0000:17:00,pcie=1"))
	if changed {
		t.Fatalf("Drift: changed=true, want false (reference-estate shape round-trip); live=%v", livePCI(435, "hostpci0=0000:17:00,pcie=1"))
	}
	if stop {
		t.Errorf("Drift: stopRequired=true on a converged PCI VM; want false")
	}
	if len(upd) != 0 {
		t.Errorf("Drift: update params = %v, want empty", upd)
	}
	if anoms := v.DriftAnomalies(livePCI(435, "hostpci0=0000:17:00,pcie=1")); len(anoms) != 0 {
		t.Errorf("DriftAnomalies on converged live: %v, want none", anoms)
	}
}

// TestPCI_AddRequiresStop pins that adding a new hostpciN device to a
// running VM requires stop (PCI topology hot-swap is not relied upon on
// PVE 9.2; conservative: stop).
func TestPCI_AddRequiresStop(t *testing.T) {
	v := pciManifest(9101, []schema.PCIDevice{{
		Slot:   "hostpci0",
		Device: "0000:17:00",
		PCIe:   &boolTrue,
	}})
	upd, stop, changed := v.Drift(livePCI(9101))
	if !changed {
		t.Fatalf("Drift: changed=false on add; want true")
	}
	if got := upd["hostpci0"]; got != "0000:17:00,pcie=1" {
		t.Errorf("Drift hostpci0 = %q, want 0000:17:00,pcie=1", got)
	}
	if !stop {
		t.Errorf("Drift: stopRequired=false on PCI add; proxops conservatively stops the VM for hostpciN writes")
	}
}

// TestPCI_LiveOnlyIsAnomaly pins that a live hostpciN slot NOT in the
// manifest is surfaced as a non-destructive anomaly, and ProxOps does NOT
// write that slot (doing so would strip a physical GPU off a running VM).
func TestPCI_LiveOnlyIsAnomaly(t *testing.T) {
	v := pciManifest(9102, nil)
	live := livePCI(9102, "hostpci0=0000:17:00,pcie=1")
	anoms := v.DriftAnomalies(live)
	if len(anoms) != 1 || !strings.Contains(anoms[0], "hostpci0") {
		t.Fatalf("DriftAnomalies = %v, want 1 entry naming hostpci0", anoms)
	}
	upd, stop, changed := v.Drift(live)
	if changed {
		t.Errorf("Drift vs live-only hostpci: changed=true; want false (no write); live=%v", live)
	}
	if _, had := upd["hostpci0"]; had {
		t.Errorf("Drift wrote hostpci0=%v for a live-only slot; proxops must not strip one", upd["hostpci0"])
	}
	if stop {
		t.Errorf("Drift: stopRequired=true on live-only anomaly; want false")
	}
}

// TestPCI_PveSideTokenDrifts pins the GAPS.md limitation: PVE reports
// extra tokens (x-vga/rombar/mdev) that ProxOps does not model. PVE
// replaces the whole hostpciN value on a config write, so the unmodelled
// token DROPS when proxops converges the slot to its BDF+pcie value.
func TestPCI_PveSideTokenDrifts(t *testing.T) {
	v := pciManifest(9103, []schema.PCIDevice{{
		Slot:   "hostpci0",
		Device: "0000:17:00",
		PCIe:   &boolTrue,
	}})
	live := livePCI(9103, "hostpci0=0000:17:00,x-vga=1,pcie=1")
	upd, stop, changed := v.Drift(live)
	if !changed {
		t.Fatalf("Drift: changed=false on PVE-side x-vga token; want true (proxops writes BDF+pcie; x-vga drops — see GAPS.md)")
	}
	if upd["hostpci0"] != "0000:17:00,pcie=1" {
		t.Errorf("Drift hostpci0 = %q, want \"0000:17:00,pcie=1\" (x-vga NOT owned)", upd["hostpci0"])
	}
	if !stop {
		t.Errorf("Drift: stopRequired=false on PVE-side-token convergence; conservatively stop")
	}
}

// TestPCI_BDFGrammar pins accepted + rejected BDF
// forms (matches PVE 9.2.2 wire grammar — probe-verified on conformance-dev).
func TestPCI_BDFGrammar(t *testing.T) {
	good := []string{"0000:17:00", "00:1a:00", "00000:17:00", "1234:01:00.1", "0000:17:00.7"}
	for _, bdf := range good {
		v := pciManifest(9104, []schema.PCIDevice{{Slot: "hostpci0", Device: bdf}})
		if err := v.Validate(); err != nil {
			t.Errorf("good BDF %q: Validate() = %v, want nil", bdf, err)
		}
	}
	bad := []string{"not-a-bdf", "0000g:17:00", "0000:17:00.8", "17:00", "0000:17", ""}
	for _, bdf := range bad {
		v := pciManifest(9104, []schema.PCIDevice{{Slot: "hostpci0", Device: bdf}})
		if err := v.Validate(); err == nil {
			t.Errorf("bad BDF %q: Validate() = nil, want error", bdf)
		}
	}
}

// TestPCI_SlotGrammar pins slot acceptance/rejection + duplicates.
func TestPCI_SlotGrammar(t *testing.T) {
	good := []string{"hostpci0", "hostpci1", "hostpci99", "hostpci999"}
	for _, s := range good {
		v := pciManifest(9105, []schema.PCIDevice{{Slot: s, Device: "0000:17:00"}})
		if err := v.Validate(); err != nil {
			t.Errorf("good slot %q: Validate() = %v, want nil", s, err)
		}
	}
	bad := []string{"pci0", "hostpci", "hostpci1000", "hostpciabc"}
	for _, s := range bad {
		v := pciManifest(9105, []schema.PCIDevice{{Slot: s, Device: "0000:17:00"}})
		if err := v.Validate(); err == nil {
			t.Errorf("bad slot %q: Validate() = nil, want error", s)
		}
	}
	v := pciManifest(9105, []schema.PCIDevice{
		{Slot: "hostpci0", Device: "0000:17:00"},
		{Slot: "hostpci0", Device: "0000:17:01"},
	})
	if err := v.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate slots: err = %v, want duplicate error", err)
	}
}

// TestPCI_WireOrdering pins that Validate re-orders the slice by slot
// number (deterministic wire output independent of manifest order).
func TestPCI_WireOrdering(t *testing.T) {
	v := pciManifest(9106, []schema.PCIDevice{
		{Slot: "hostpci1", Device: "0000:73:00", PCIe: &boolTrue},
		{Slot: "hostpci0", Device: "0000:17:00", PCIe: &boolFalse},
	})
	if err := v.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v.Spec.Hardware.PCIDevices[0].Slot != "hostpci0" {
		t.Errorf("Validate did not re-order by slot; first = %q", v.Spec.Hardware.PCIDevices[0].Slot)
	}
	if v.Spec.Hardware.PCIDevices[1].Slot != "hostpci1" {
		t.Errorf("Validate re-order wrong; second = %q", v.Spec.Hardware.PCIDevices[1].Slot)
	}
	p, _ := v.ToCreateParams()
	if p["hostpci0"] != "0000:17:00,pcie=0" {
		t.Errorf("hostpci0 wire = %q, want 0000:17:00,pcie=0", p["hostpci0"])
	}
	if p["hostpci1"] != "0000:73:00,pcie=1" {
		t.Errorf("hostpci1 wire = %q, want 0000:73:00,pcie=1", p["hostpci1"])
	}
}

// TestPCI_LowercaseBDF pins that an uppercase manifest BDF normalises to
// lowercase on the wire (PVE 9.2.2 accepts either case; determinism = one
// canonical form).
func TestPCI_LowercaseBDF(t *testing.T) {
	v := pciManifest(9107, []schema.PCIDevice{{Slot: "hostpci0", Device: "0000:AB:CD"}})
	if err := v.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v.Spec.Hardware.PCIDevices[0].Device != "0000:ab:cd" {
		t.Errorf("BDF not lower-cased in place: %q", v.Spec.Hardware.PCIDevices[0].Device)
	}
	p, _ := v.ToCreateParams()
	if p["hostpci0"] != "0000:ab:cd" {
		t.Errorf("wire BDF case preserved: %q, want 0000:ab:cd", p["hostpci0"])
	}
}

// TestPCI_PvePCIKeyIsOwned pins the gap-detection predicate.
func TestPCI_PvePCIKeyIsOwned(t *testing.T) {
	cases := map[string]bool{
		"hostpci0":    true,
		"hostpci9":    true,
		"hostpci999":  true,
		"hostpci1000": false,
		"pci0":        false,
		"":            false,
		"hostpciabc":  false,
	}
	for in, want := range cases {
		if got := schema.PvePCIKeyIsOwned(in); got != want {
			t.Errorf("PvePCIKeyIsOwned(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestPCI_AdoptParsesReferenceShape pins that adoption's PVE-side parse of
// the the reference estate wire form produces EXACTLY the structured form ProxOps can
// round-trip (PvePCIDeviceFromPVE + PvePCIDevicesFromPVE).
func TestPCI_AdoptParsesReferenceShape(t *testing.T) {
	dev, ok := schema.PvePCIDeviceFromPVE("hostpci0", "0000:17:00,pcie=1")
	if !ok {
		t.Fatalf("PvePCIDeviceFromPVE: !ok for the reference estate shape")
	}
	if dev.Slot != "hostpci0" {
		t.Fatalf("dev.Slot = %q, want hostpci0", dev.Slot)
	}
	if dev.Device != "0000:17:00" {
		t.Fatalf("dev.Device = %q, want lower-cased 0000:17:00", dev.Device)
	}
	if dev.PCIe == nil || !*dev.PCIe {
		t.Fatalf("dev.PCIe = %v, want *true", dev.PCIe)
	}
	devs := schema.PvePCIDevicesFromPVE(map[string]any{
		"hostpci2": "0000:73:00",
		"hostpci0": "0000:17:00,pcie=1",
	})
	if len(devs) != 2 {
		t.Fatalf("PvePCIDevicesFromPVE = %d, want 2; got %+v", len(devs), devs)
	}
	if devs[0].Slot != "hostpci0" || devs[1].Slot != "hostpci2" {
		t.Fatalf("slot ordering not deterministic: %v", devs)
	}
	if devs[0].PCIe == nil || !*devs[0].PCIe {
		t.Fatalf("devs[0].PCIe = %v, want true (pcie=1 token)", devs[0].PCIe)
	}
	if devs[1].PCIe != nil {
		t.Fatalf("devs[1].PCIe = %v, want nil (no pcie token)", devs[1].PCIe)
	}
}
