// M13.2: structured PCI passthrough for VMs.
//
// PVE exposes host devices through the `hostpciN` config keys. On PVE 9.2.2
// (probe-verified on conformance-dev VM 9100) the wire grammar is:
//
//	hostpci<N>=<bdf>[,pcie=1|0][,x-vga=1|0][,rombar=0|1][,mdev=<name>]
//
// where:
//   - <N> is an integer slot index (0..99 observed; PVE does not reject
//     hostpciN for N>9 but PVE only uses up to ~8 in practice).
//   - <bdf> accepts forms `[domain:]bus:slot[.func]` — a five-hex-digit
//     domain was accepted (PVE is permissive about the domain width), and a
//     short `bus:slot.func` form too. PVE rejects any value that does not
//     match the BDF regex (`hostpci0.host` error).
//   - The schema accepts `pcie`, `x-vga`, `rombar`, `mdev`. PVE rejects
//     `boot`, `dimmable`, `sub-vfid`, `legacy-irr-qworkaround` with
//     "property is not defined in schema" — proxops does not model those.
//   - A runtime gate on PVE 9.2 fails the task (exitstatus sentence) when
//     the device is not in a PVE PCI pool: "only root can set 'hostpci<N>'
//     config for non-mapped devices". The config write still returns HTTP
//     200 + a task UPID before the task runs, and PVE does NOT persist the
//     hostpciN entry in /config when the task fails: on re-read, the key is
//     absent. This means on a dev cluster without PCI pools, ProxOps cannot
//     round-trip a hostpci device — only the wire grammar can be exercised.
//   - `delete=hostpci<N>` against an absent key is a no-op (HTTP 200, OK).
//
// ProxOps therefore models only the fields that PVE 9.2.2 accepts AND that
// operators commonly set on Talos / K8s GPU nodes:
//
//	hostpci0=0000:17:00,pcie=1
//
// Additional PVE options (x-vga, rombar, mdev) remain in GAPS.md as
// out of scope. The structured schema below is deliberately NARROW — no
// raw hostpci string pass-through, no unmodelled option passthrough.
package schema

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PCI bdf regex: PVE 9.2.2 accepts `domain:bus:slot[.func]` where:
//   - domain is 2–5 hex digits
//   - bus is 2 hex digits
//   - slot is 2 hex digits
//   - func is an optional single hex digit 0..7
//
// ProxOps's schema enforces this on parse.
var pciBDFRe = regexp.MustCompile(`^[0-9a-fA-F]{2,5}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}(\.[0-7])?$`)

// PCI slot regex: `hostpci<N>` where N is non-negative decimal. The
// PVE schema itself enforces an int in `hostpci<digits>`; proxops
// mirrors that and caps at 999 (PVE supports up to 1000 in practice).
var pciSlotRe = regexp.MustCompile(`^hostpci([0-9]{1,3})$`)

// PCIDevice is one PVE host PCI device (a `hostpci<N>` config key).
//
// ProxOps deliberately does NOT model PVE's `x-vga`, `rombar`, `mdev`,
// `boot`, `dimmable`, `sub-vfid`, `legacy-irr-qworkaround` tokens on PVE
// 9.2.2: only `pcie` is first-class. Operators who need one of the others
// use `spec.extra` with the PVE wire token and accept that ProxOps will
// not compare it on drift (documented in GAPS.md).
type PCIDevice struct {
	// Slot is the PVE `hostpciN` slot name (e.g. "hostpci0").
	// Required, validated against PVE's hostpci grammar.
	Slot string `yaml:"slot" json:"slot"`
	// Device is the PVE PCI BDF (e.g. "0000:17:00"). Required.
	Device string `yaml:"device" json:"device"`
	// PCIe requests PVE's `pcie=1|0` token. nil = not owned (proxops does
	// not send the token; PVE's default applies).
	PCIe *bool `yaml:"pcie,omitempty" json:"pcie,omitempty"`
}

// ValidatePCIDevices enforces the PCI passthrough rules on a slice of
// PCIDevice. Deterministic, no PVE round-trip performed here.
func ValidatePCIDevices(ref string, devices []PCIDevice) error {
	seenSlots := map[string]bool{}
	sort.SliceStable(devices, func(i, j int) bool {
		pi, _ := pciSlotNumber(devices[i].Slot)
		pj, _ := pciSlotNumber(devices[j].Slot)
		return pi < pj
	})
	for i := range devices {
		d := &devices[i]
		_, ok := pciSlotNumber(d.Slot)
		if !ok {
			// pciSlotRe bounds N to \d{1,3} (0..999), so a 4+ digit slot is
			// rejected by the grammar; keep the cap explicit in the message.
			return fmt.Errorf("%s: spec.hardware.pci-devices slot %q is not hostpciN (N a non-negative integer 0-999)", ref, d.Slot)
		}
		if seenSlots[d.Slot] {
			return fmt.Errorf("%s: duplicate PCI slot %q", ref, d.Slot)
		}
		seenSlots[d.Slot] = true

		devNorm := strings.ToLower(strings.TrimSpace(d.Device))
		// Normalise in place for deterministic wire output: lowercase hex.
		d.Device = devNorm
		if !pciBDFRe.MatchString(devNorm) {
			return fmt.Errorf("%s: spec.hardware.pci-devices[%s].device %q is not a PVE PCI BDF (want domain:bus:slot[.func], e.g. \"0000:17:00\")", ref, d.Slot, d.Device)
		}
	}
	return nil
}

// pciSlotNumber returns the numeric <N> from a hostpciN slot and ok.
func pciSlotNumber(slot string) (int, bool) {
	m := pciSlotRe.FindStringSubmatch(slot)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// pciDeviceWire renders the PVE wire form for one hostpciN config key.
// The token order is fixed: <bdf>[,pcie=N].
func pciDeviceWire(d PCIDevice) string {
	bdf := strings.ToLower(strings.TrimSpace(d.Device))
	if d.PCIe == nil {
		return bdf
	}
	if *d.PCIe {
		return bdf + ",pcie=1"
	}
	return bdf + ",pcie=0"
}

// PvePCIDeviceFromPVE parses one PVE `hostpciN` report value into a
// PCIDevice. Returns (dev, ok) where ok=false when the value does not
// match the proxops-owned BDF grammar (in which case the caller should
// surface a gap).
func PvePCIDeviceFromPVE(slot string, raw any) (PCIDevice, bool) {
	if !pciSlotRe.MatchString(slot) {
		return PCIDevice{}, false
	}
	v := pveStr(raw)
	if v == "" {
		return PCIDevice{}, false
	}
	toks := strings.Split(v, ",")
	dev := PCIDevice{Slot: slot}
	for i, ts := range toks {
		ts = strings.TrimSpace(ts)
		if ts == "" {
			continue
		}
		if i == 0 {
			if !pciBDFRe.MatchString(strings.ToLower(ts)) {
				return PCIDevice{}, false
			}
			dev.Device = strings.ToLower(ts)
			continue
		}
		// PVE 9.2.2 accepts: pcie, x-vga, rombar, mdev.
		// Only pcie is proxops-owned; the rest are recorded as PVE-owned
		// tokens and surfaced (the caller handles the gap).
		if strings.HasPrefix(ts, "pcie=") {
			val := strings.TrimPrefix(ts, "pcie=")
			switch val {
			case "1":
				t := true
				dev.PCIe = &t
			case "0":
				f := false
				dev.PCIe = &f
			default:
				return PCIDevice{}, false
			}
			continue
		}
		// x-vga / rombar / mdev / anything else: not modelled; the caller
		// surfaces these as PVE-owned gaps.
		return dev, true
	}
	return dev, true
}

// PvePCIDevicesFromPVE extracts all hostpciN PCI devices from a PVE /config
// report in deterministic slot-numeric order. The slice is empty when PVE
// reports no hostpciN keys. Any hostpciN value that does not parse as a
// proxops-owned BDF+pcie token (e.g. an x-vga-only entry with no BDF,
// PVE-side mdev/pool references that PVE 9.2 in fact rejects) is dropped
// here and surfaced by the caller as a PVE-owned gap.
func PvePCIDevicesFromPVE(current map[string]any) []PCIDevice {
	seen := map[string]PCIDevice{}
	for k, v := range current {
		if !PvePCIKeyIsOwned(k) {
			continue
		}
		dev, ok := PvePCIDeviceFromPVE(k, v)
		if !ok {
			continue
		}
		seen[k] = dev
	}
	out := make([]PCIDevice, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		ni, _ := pciSlotNumber(out[i].Slot)
		nj, _ := pciSlotNumber(out[j].Slot)
		if ni != nj {
			return ni < nj
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

// PvePCIKeyIsOwned returns true when a PVE /config key is a hostpciN slot
// that proxops models (used for gap detection on adopt). PVE may report
// hostpci keys even when no proxops-managed hostpciN exists — a PVE-side
// passthrough that proxops is not asked to own.
func PvePCIKeyIsOwned(k string) bool {
	return pciSlotRe.MatchString(k)
}

// pciDeviceEqual compares two PVE-side hostpciN values on the tokens that
// proxops owns. PVE-owned tokens (x-vga, rombar, mdev) are ignored here
// (proxops does not compare them on drift).
func hostpciTokensEqual(cur string, want PCIDevice) bool {
	curToks := hostpciTokenMap(cur)
	wantToks := hostpciTokenMap(pciDeviceWire(want))
	if len(curToks) != len(wantToks) {
		return false
	}
	for k, v := range wantToks {
		if curToks[k] != v {
			return false
		}
	}
	return true
}

func hostpciTokenMap(raw string) map[string]string {
	out := map[string]string{}
	for _, ts := range strings.Split(raw, ",") {
		ts = strings.TrimSpace(ts)
		if ts == "" {
			continue
		}
		if eq := strings.IndexByte(ts, '='); eq >= 0 {
			k := strings.ToLower(strings.TrimSpace(ts[:eq]))
			v := strings.Trim(ts[eq+1:], "\" ")
			out[k] = v
		} else {
			// Bare first token is the device BDF.
			out["host"] = strings.ToLower(ts)
		}
	}
	return out
}

// PVESSHCiWireDecode decodes one PVE sshkeys wire token back to the raw
// OpenSSH public-key line. PVE's wire form is percent-encoded
// (probed PVE 9.2.2: spaces are %20, @ is %40; Go's url.PathUnescape
// matches that grammar exactly — a raw + is never emitted, base64's + is
// percent-encoded to %2B).
func PVESSHCiWireDecode(wire string) (string, error) {
	return url.PathUnescape(wire)
}

// sshKeyIdentity extracts (type, blob, comment) from one OpenSSH public-key
// line. The 3rd column is the free-form comment and is NOT part of the
// identity (two keys that differ only in comment are the same key for
// dedup, naming, and drift purposes).
func sshKeyIdentity(line string) (typ, blob, comment string, ok bool) {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) < 2 {
		return "", "", "", false
	}
	typ = parts[0]
	blob = parts[1]
	if len(blob) < 16 {
		return "", "", "", false
	}
	if len(parts) >= 3 {
		comment = strings.Join(parts[2:], " ")
	}
	return typ, blob, comment, true
}

// SSHKeyFingerprint is a stable, deterministic identifier for a public SSH
// key (type + material, comment excluded): the first 8 bytes of
// sha256("sshpki\x00<type>\x00<blob>") rendered as 16 lowercase hex chars.
// Used by adoption to dedupe identical keys across resources and to seed
// `--adopt-secrets` reference names (`adopted-<fingerprint>`). The key
// material itself (public — low sensitivity) does NOT appear in generated
// names or reports.
func SSHKeyFingerprint(line string) string {
	typ, blob, _, ok := sshKeyIdentity(line)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte("sshpki\x00" + typ + "\x00" + blob))
	const lower = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 0; i < 8; i++ {
		out[i*2] = lower[sum[i]>>4]
		out[i*2+1] = lower[sum[i]&0x0f]
	}
	return string(out)
}
