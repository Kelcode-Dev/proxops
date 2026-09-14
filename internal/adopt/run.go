package adopt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// PVEBookkeepingKeys are PVE /config report fields that PVE generates and
// manages on its own; they cannot be sent back as create form-parameters and
// are excluded from the gap report.
var PVEBookkeepingKeys = map[string]bool{
	"digest":  true,
	"meta":    true,
	"vmgenid": true,
	"smbios1": true,
	"uuid":    true,
	// PVE infers ostype from the installed OS / ostemplate; it is not a
	// create form-value proxops can (or wants to) own, so treating it
	// as bookkeeping keeps it out of both the owned surface and the gap
	// report. (probe: every prod-a VM reports ostype=l26 etc.)
	"ostype":     true,
	"ostemplate": true,
}

// sensitiveGapFields are PVE /config keys whose VALUES carry credentials or
// PII. adopt reports the field NAME as a gap (so the operator knows proxops
// does not model it) but must NEVER echo its value: sshkeys embeds public
// keys + usernames, cipassword is the cloud-init root password placeholder.
// The gap value is substituted with a fixed marker.
var sensitiveGapFields = map[string]bool{
	"sshkeys":    true,
	"cipassword": true,
	"password":   true,
	"sshkey":     true,
	"keyfile":    true,
	"secret":     true,
}

// redactGapValue substitutes a PII/credential-carrying PVE value with a
// fixed redaction marker so it can never reach stdout, logs, or a committed
// gap report.
func redactGapValue(field, value string) string {
	if sensitiveGapFields[field] {
		return "<redacted>"
	}
	return value
}

// gapNoteFor augments the default "proxops does not model this" gap
// note with a specific meaning for a handful of well-known PVE fields
// (so the audit is more useful than a generic note). Unknown fields keep
// the caller-supplied default.
func gapNoteFor(field, fallback string) string {
	switch field {
	case "cmode":
		return "PVE console-mode token (tty/vga/none); proxops does not model the LXC console mode"
	case "tty":
		return "PVE tty count; proxops does not model LXC tty count"
	case "console":
		return "PVE console enable (=1); proxops LXCOptions.Console is tri-state and captures it via /config PUT"
	case "cpulimit":
		return "PVE CPU rate limit (0 = unbounded); proxops does not model LXC cpulimit"
	case "cpuunits":
		return "PVE CPU weight (share of a 1024-unit pool); proxops does not model LXC cpuunits"
	case "features":
		return "PVE 9.x composite feature tokens (nesting=1, etc.); proxops captures nesting via LXCOptions.Nesting"
	case "lxc":
		return "PVE LXC raw `lxc.` config-line passthrough (AppArmor profile, cgroups, ...); proxops does not model it"
	case "template":
		return "PVE template-flag VMs are out of scope (recorded in Skipped)"
	case "ipconfig0":
		return "PVE VM cloud-init ipconfig0; proxops does not model VM cloud-init networking"
	case "nameserver":
		return "PVE VM cloud-init nameserver; proxops does not model VM cloud-init DNS"
	case "cicustom":
		return "PVE cloud-init custom files; proxops does not model VM cloud-init cicustom"
	case "ciuser":
		return "PVE cloud-init username; proxops does not model VM cloud-init ciuser"
	case "ciupgrade":
		return "PVE cloud-init upgrade mode; proxops does not model it"
	case "sshkeys":
		return "PVE cloud-init SSH public keys; proxops does not model VM cloud-init sshkeys (value redacted)"
	case "cipassword":
		return "PVE cloud-init root password; proxops does not model it (value redacted)"
	case "kvm":
		return "PVE KVM nested-virt enable; proxops does not model it"
	case "balloon":
		return "PVE balloon-MiB / auto-balance setting; proxops does not model it"
	}
	return fallback
}

// pveTemplateValue reports whether PVE's `template` config token is set
// (PVE reports it as the number 1 or the string "1"; JSON may decode either
// depending on the PVE build).
func pveTemplateValue(v any) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "1" || strings.EqualFold(x, "true")
	default:
		n, _ := strconv.ParseFloat(fmt.Sprintf("%v", x), 64)
		return n == 1
	}
}

// artifactNames maps a PVE artifact volid to the generated manifest name, so
// cross-references (VM→ISO cdrom, LXC→CTTemplate ostemplate) resolve inside a
// single cluster's composition.
type artifactNames struct {
	iso map[string]string // "storage:iso/filename" → name
	vzt map[string]string // "storage:vztmpl/filename" → name
}

// artifactSpec is one discovered storage artifact (a set of nodes where the
// same filename exists on the same storage).
type artifactSpec struct {
	storage  string
	filename string
	nodes    map[string]bool
}

func (s *artifactSpec) addNode(n string) {
	if s.nodes == nil {
		s.nodes = map[string]bool{}
	}
	s.nodes[n] = true
}

func (s *artifactSpec) sortedNodes() []string {
	out := make([]string, 0, len(s.nodes))
	for n := range s.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// adoptContext threads the per-run state (git root, pve client, discovered
// artifact names, volume-size listings, result).
type adoptContext struct {
	pve     *pveclient.Client
	cluster string
	gitRoot string
	res     *Result
	names   *artifactNames
	// volumeSizes: node -> storage -> volid -> bytes, accumulated lazily by
	// nodeVolumeSizes.
	volumeSizes map[string]map[string]map[string]int64
}

// Run performs a read-only adopt of one cluster:
//
//  1. node enumeration (the configured allowlist; otherwise PVE's cluster
//     node list),
//  2. artifact discovery (every iso + vztmpl on every allowed node/storage),
//  3. VM discovery + manifest generation,
//  4. LXC discovery + manifest generation,
//  5. gap collection (PVE keys proxops does not model),
//  6. zero-writes assertion (the read-only invariant).
//
// Generated files land under <kind>/<cluster>/ in the git root. Run never
// edits clusters/<cluster>/resources.yaml.
func Run(ctx context.Context, pve *pveclient.Client, cluster string, allowedNodes []string, gitRoot string) (*Result, error) {
	if strings.TrimSpace(cluster) == "" {
		return nil, fmt.Errorf("adopt: cluster name required")
	}
	if !ValidClusterName(cluster) {
		return nil, fmt.Errorf("adopt: %q is not a valid cluster name (lowercase alnum + '-'); cluster names are the GitOps composition identity", cluster)
	}
	writesBefore := pve.WritesPerformed()
	res := &Result{Cluster: cluster, PVE: pve}
	ac := &adoptContext{pve: pve, cluster: cluster, gitRoot: gitRoot, res: res, names: &artifactNames{iso: map[string]string{}, vzt: map[string]string{}}, volumeSizes: map[string]map[string]map[string]int64{}}

	nodes := append([]string{}, allowedNodes...)
	if len(nodes) == 0 {
		liveNodes, nErr := pve.Nodes(ctx)
		if nErr != nil {
			return nil, fmt.Errorf("adopt: cluster node listing: %w (configure pve.clusters.%s.nodes to enumerate without querying /cluster/nodes)", nErr, cluster)
		}
		nodes = liveNodes
	}
	sort.Strings(nodes)
	res.logf("nodes: %v", nodes)

	if aErr := ac.adoptArtifacts(ctx, nodes); aErr != nil {
		return nil, aErr
	}

	// VMs.
	for _, node := range nodes {
		entries, lErr := pve.VMList(ctx, node)
		if lErr != nil {
			return nil, fmt.Errorf("adopt: VM listing on %s: %w", node, lErr)
		}
		for _, e := range entries {
			if err := ac.adoptVM(ctx, node, e); err != nil {
				return nil, err
			}
		}
	}

	// LXCs.
	for _, node := range nodes {
		entries, lErr := pve.LXCList(ctx, node)
		if lErr != nil {
			return nil, fmt.Errorf("adopt: LXC listing on %s: %w", node, lErr)
		}
		for _, e := range entries {
			if err := ac.adoptLXC(ctx, node, e); err != nil {
				return nil, err
			}
		}
	}

	res.Gaps = dedupGaps(res.Gaps)
	sort.Slice(res.Wrote, func(i, j int) bool { return res.Wrote[i].Path < res.Wrote[j].Path })
	// Incomplete / Skipped are keyed by path; sort them too, because PVE's
	// per-node listing order is not guaranteed stable across calls and the
	// adopt log must be byte-identical for idempotency.
	sort.Strings(res.Incomplete)
	sort.Slice(res.Skipped, func(i, j int) bool {
		if res.Skipped[i].Node != res.Skipped[j].Node {
			return res.Skipped[i].Node < res.Skipped[j].Node
		}
		return res.Skipped[i].ID < res.Skipped[j].ID
	})

	// Read-only assertion: no PVE write endpoint may have been hit.
	if got := pve.WritesPerformed(); got != writesBefore {
		return nil, fmt.Errorf("adopt: PVE write endpoints were called (writes went %d → %d); adoption must be read-only", writesBefore, got)
	}
	return res, nil
}

// logf buffers one log line (in-order).
func (r *Result) logf(format string, args ...any) {
	r.Logs = append(r.Logs, fmt.Sprintf(format, args...))
}

// validClusterName re-exports config.ValidClusterName without an import
// cycle (adopt must not import config; the proxops CLI validates config
// before it ever reaches adopt).
func ValidClusterName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			continue
		case c >= '0' && c <= '9':
			if i == 0 {
				// no leading digits-only constraint, but no leading '-'
				continue
			}
		case c == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
			continue
		default:
			return false
		}
	}
	return true
}

// adoptArtifacts walks every allowed node's storage backends, generating one
// ISO manifest per distinct iso filename and one CTTemplate manifest per
// distinct vztmpl filename. A filename present on several allowed nodes
// becomes one manifest with spec.nodes covering all of them (PVE's natural
// multi-node placement shape).
func (ac *adoptContext) adoptArtifacts(ctx context.Context, nodes []string) error {
	isoSpecs := map[string]*artifactSpec{}
	vztSpecs := map[string]*artifactSpec{}

	for _, node := range nodes {
		storages, sErr := ac.pve.StorageList(ctx, node)
		if sErr != nil {
			return fmt.Errorf("adopt: storage listing on %s: %w", node, sErr)
		}
		for _, storage := range storages {
			entries, cErr := ac.pve.Storage().Content(ctx, node, storage)
			if cErr != nil {
				return fmt.Errorf("adopt: storage content on %s/%s: %w", node, storage, cErr)
			}
			for _, e := range entries {
				switch e.Content {
				case "iso":
					fn := pveclient.VolidFilename(e.Volid, "iso")
					sp := isoSpecs[fn]
					if sp == nil {
						sp = &artifactSpec{storage: storage, filename: fn}
						isoSpecs[fn] = sp
					}
					sp.addNode(node)
				case "vztmpl":
					fn := pveclient.VolidFilename(e.Volid, "vztmpl")
					sp := vztSpecs[fn]
					if sp == nil {
						sp = &artifactSpec{storage: storage, filename: fn}
						vztSpecs[fn] = sp
					}
					sp.addNode(node)
				}
			}
		}
	}

	isoFns := mapKeys(isoSpecs)
	vztFns := mapKeys(vztSpecs)
	for _, fn := range isoFns {
		sp := isoSpecs[fn]
		name := nameForPVE(fn, -1, "iso")
		ac.names.iso[sp.storage+":iso/"+sp.filename] = name
		isoDoc := &schema.ISO{
			APIVersion: schema.APIVersion,
			Kind:       schema.KindISO,
			Metadata:   schema.Metadata{Name: name},
			Spec:       schema.ISOSpec{Nodes: sp.sortedNodes(), Storage: sp.storage, Filename: sp.filename, URL: placeholderURL(sp.storage, sp.filename)},
		}
		if wErr := ac.writeManifest(schema.KindISO, isoDoc, name); wErr != nil {
			return wErr
		}
		// The download URL is a proxops-owned field that PVE does not
		// report: adopt cannot know where the file came from. The
		// placeholder keeps the schema valid; the URL is recorded here as
		// an open gap for the operator to close.
		ac.res.Gaps = append(ac.res.Gaps, Gap{
			Kind:  schema.KindISO,
			Node:  sp.sortedNodes()[0],
			Field: "spec.url",
			Value: placeholderURL(sp.storage, sp.filename),
			Note:  "PVE does not record where a file was downloaded from; spec.url is a placeholder — set a real URL so the artifact can be re-seeded on a fresh node",
		})
	}
	for _, fn := range vztFns {
		sp := vztSpecs[fn]
		name := nameForPVE(fn, -1, "ctt")
		ac.names.vzt[sp.storage+":vztmpl/"+sp.filename] = name
		cttDoc := &schema.CTTemplate{
			APIVersion: schema.APIVersion,
			Kind:       schema.KindCTTemplate,
			Metadata:   schema.Metadata{Name: name},
			Spec:       schema.CTTSpec{Nodes: sp.sortedNodes(), Storage: sp.storage, Filename: sp.filename, URL: placeholderURL(sp.storage, sp.filename)},
		}
		if wErr := ac.writeManifest(schema.KindCTTemplate, cttDoc, name); wErr != nil {
			return wErr
		}
		ac.res.Gaps = append(ac.res.Gaps, Gap{
			Kind:  schema.KindCTTemplate,
			Node:  sp.sortedNodes()[0],
			Field: "spec.url",
			Value: placeholderURL(sp.storage, sp.filename),
			Note:  "PVE does not record where a vztmpl was downloaded from; spec.url is a placeholder — set a real URL so the template can be re-seeded on a fresh node",
		})
	}
	ac.res.logf("artifacts: %d ISOs, %d CTTemplates", len(isoFns), len(vztFns))
	return nil
}

// writeManifest marshals doc and writes <kind>/<cluster>/<name>.yaml under
// the git root, recording it on res. When the doc is a schema.Resource, it is
// Validate()-checked: a manifest that cannot be composed (an unknown live
// value proxops could not recover) still gets written (the operator
// should see the generated shape to fix it), but it is ADDED TO
// res.Incomplete and the summary says not to list it in resources.yaml yet.
// Output is 2-space YAML (schema.YAMLOut) so generated manifests pass the
// GitOps repo's .yamllint.yaml rule (indentation: spaces: 2).
func (ac *adoptContext) writeManifest(kind schema.Kind, doc any, name string) error {
	yb, err := schema.YAMLOut(doc)
	if err != nil {
		return fmt.Errorf("adopt: marshal %s %s: %w", kind, name, err)
	}
	relPath := kindPath(ac.cluster, kind, name)
	if wErr := os.MkdirAll(filepath.Dir(filepath.Join(ac.gitRoot, relPath)), 0o755); wErr != nil {
		return fmt.Errorf("adopt: mkdir for %s: %w", relPath, wErr)
	}
	if wErr := os.WriteFile(filepath.Join(ac.gitRoot, relPath), yb, 0o644); wErr != nil {
		return fmt.Errorf("adopt: write %s: %w", relPath, wErr)
	}
	ac.res.Wrote = append(ac.res.Wrote, WroteManifest{
		Cluster: ac.cluster,
		Kind:    kind,
		Path:    relPath,
		Content: string(yb),
	})
	if r, ok := doc.(interface{ Validate() error }); ok {
		if verr := r.Validate(); verr != nil {
			ac.res.Incomplete = append(ac.res.Incomplete, fmt.Sprintf("%s: %v", relPath, verr))
		}
	}
	return nil
}

func mapKeys(m map[string]*artifactSpec) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func placeholderURL(storage, filename string) string {
	return "https://placeholder.invalid/" + storage + "/" + filename
}

// kindPath returns the relative path <kind>/<cluster>/<name>.yaml.
func kindPath(cluster string, k schema.Kind, name string) string {
	dir := "vm"
	switch k {
	case schema.KindLXC:
		dir = "lxc"
	case schema.KindISO:
		dir = "iso"
	case schema.KindCTTemplate:
		dir = "ctt"
	// M11: TemplateVM manifests live under the templatevm/ kind root
	// (mirrors composition.rootDirs; a TemplateVM proxops-manifest is
	// NOT placed under vm/ because it is a distinct resource kind
	// with its own parse/route/plan/exec/adopt paths).
	case schema.KindTemplateVM:
		dir = "templatevm"
	}
	return filepath.ToSlash(filepath.Join(dir, cluster, name+".yaml"))
}

// sanitizeName turns an arbitrary PVE filename into a valid proxops
// metadata.name (lowercase alnum + '-', <= 63 chars, no leading
// '-'). PVE's "test-vm-01" passes through; "ISO 13" becomes "iso-13";
// "debian-13.6.0-amd64-netinst.iso" becomes "debian-13-6-0-amd64-netinst".
func sanitizeName(s string) string {
	var b strings.Builder
	prevDash := true
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(byte(c))
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
		}
		prevDash = true
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "unnamed"
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// nodeVolumeSizes lazily fetches one node's storage content listing so the
// LXC adopt path can recover rootfs/mp sizes that PVE's /config does not
// report. Returns a volid → bytes map for the node.
func (ac *adoptContext) nodeVolumeSizes(ctx context.Context, node string) (map[string]int64, error) {
	if m, ok := ac.volumeSizes[node]; ok {
		out := map[string]int64{}
		for st, vols := range m {
			for vid, sz := range vols {
				out[vid] = sz
			}
			_ = st
		}
		return out, nil
	}
	nodeVols := map[string]map[string]int64{}
	storages, sErr := ac.pve.StorageList(ctx, node)
	if sErr != nil {
		return nil, sErr
	}
	for _, storage := range storages {
		entries, cErr := ac.pve.Storage().Content(ctx, node, storage)
		if cErr != nil {
			return nil, cErr
		}
		for _, e := range entries {
			if nodeVols[storage] == nil {
				nodeVols[storage] = map[string]int64{}
			}
			nodeVols[storage][e.Volid] = e.Size
		}
	}
	ac.volumeSizes[node] = nodeVols
	out := map[string]int64{}
	for _, vols := range nodeVols {
		for vid, sz := range vols {
			out[vid] = sz
		}
	}
	return out, nil
}
