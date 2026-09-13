package schema

import (
	"fmt"
	"strings"
)

// This file holds the artifact-reference resolver: it turns the structured
// references that VM (spec.hardware.cdrom.iso) and LXC (spec.template) carry
// into concrete PVE storage volumes, and validates that the referencing
// object's node is one of the referenced artifact's declared nodes.
//
// Resolution is performed exactly once per desired index (by parse, and
// re-run by the planner as a defensive gate) and mutates the resources in
// place: a VM gets its cdromVolid populated, an LXC gets its
// ostemplateVolid populated. ToCreateParams and Drift read those resolved
// fields, so the owned-field projection stays correct after resolution.

// ResolveArtifactRefs validates every structured artifact reference in
// resources and fills the resolved PVE volume fields. It fails closed when a
// reference is unknown, or when the referencing object's node is not declared
// as a placement node of the artifact.
//
// The function is idempotent: re-running it on the same index yields the same
// resolved volumes.
func ResolveArtifactRefs(resources []Resource) error {
	if len(resources) == 0 {
		return nil
	}
	byRef := make(map[Ref]Resource, len(resources))
	for _, r := range resources {
		byRef[r.Ref()] = r
	}
	for _, r := range resources {
		switch v := r.(type) {
		case *VM:
			if err := v.resolveCDrom(byRef); err != nil {
				return err
			}
			if err := v.resolveDiskImages(byRef); err != nil {
				return err
			}
		case *LXC:
			if err := v.resolveTemplate(byRef); err != nil {
				return err
			}
		case *TemplateVM:
			// M11: a TemplateVM re-uses the VM surface (embedded), so the
			// cdrom.iso artifact edge resolves exactly like a VM's. The
			// embedded-field type switch above does NOT catch *TemplateVM,
			// so we route it explicitly through the VM helper.
			if err := v.resolveCDrom(byRef); err != nil {
				return err
			}
			if err := v.resolveDiskImages(byRef); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveCDrom binds the VM's spec.hardware.cdrom.iso reference to the
// referenced ISO's PVE volid and records the IDE slot PVE will wire it at.
// Three states:
//   - iso empty            → cdromManaged=false (pveconform does NOT own the slot)
//   - iso = CDROMNone      → cdromManaged=true, cdromVolid="none"
//   - iso = <ISO name>     → cdromManaged=true, cdromVolid="<storage>:iso/<filename>[,media=X]"
//
// When iso=<ISO name>, pveconform also validates that the ISO is
// placed on the VM's node (the ISO must be downloaded before the VM's
// cdrom is attached at create time).
func (v *VM) resolveCDrom(byRef map[Ref]Resource) error {
	isoName := strings.TrimSpace(v.Spec.Hardware.Cdrom.Iso)
	if isoName == "" || isoName == CDROMNone {
		// Unmanaged or detach ("none") — no ISO artifact to resolve.
		v.cdromVolid = ""
		return nil
	}
	isoRes, ok := byRef[Ref{Kind: KindISO, Name: isoName}]
	if !ok {
		return fmt.Errorf("%s: spec.hardware.cdrom.iso references unknown ISO %q", v.Ref(), isoName)
	}
	iso, ok := isoRes.(*ISO)
	if !ok {
		return fmt.Errorf("%s: spec.hardware.cdrom.iso references %s which is not an ISO", v.Ref(), isoName)
	}
	if !containsString(iso.Nodes(), v.Spec.Node) {
		return fmt.Errorf("%s: spec.hardware.cdrom.iso %q is not placed on node %s (ISO nodes: %v)",
			v.Ref(), isoName, v.Spec.Node, iso.Nodes())
	}
	v.cdromVolid = iso.Spec.Storage + ":iso/" + iso.Spec.Filename
	return nil
}

// resolveTemplate binds the LXC's spec.template reference to the referenced
// CTTemplate's PVE vztmpl volid.
func (l *LXC) resolveTemplate(byRef map[Ref]Resource) error {
	tplName := l.Spec.Template
	if tplName == "" {
		// Validate() already rejects an LXC without a template.
		l.ostemplateVolid = ""
		return nil
	}
	isoRes, ok := byRef[Ref{Kind: KindCTTemplate, Name: tplName}]
	if !ok {
		return fmt.Errorf("%s: spec.template references unknown CTTemplate %q", l.Ref(), tplName)
	}
	ctt, ok := isoRes.(*CTTemplate)
	if !ok {
		return fmt.Errorf("%s: spec.template references %s which is not a CTTemplate", l.Ref(), tplName)
	}
	if !containsString(ctt.Nodes(), l.Spec.Node) {
		return fmt.Errorf("%s: spec.template %q is not placed on node %s (CTTemplate nodes: %v)",
			l.Ref(), tplName, l.Spec.Node, ctt.Nodes())
	}
	// PVE's vztmpl volid on dir storage is "<storage>:vztmpl/<filename>".
	l.ostemplateVolid = ctt.Spec.Storage + ":vztmpl/" + ctt.Spec.Filename
	return nil
}

// resolveDiskImages binds every spec.disks[].image reference to the
// referenced DiskImage's PVE import-pool volid, and validates that the image
// is placed on the VM's node (the image must be downloaded before the VM's
// create can import from it).
func (v *VM) resolveDiskImages(byRef map[Ref]Resource) error {
	for i := range v.Spec.Disks {
		d := &v.Spec.Disks[i]
		imgName := strings.TrimSpace(d.Image)
		if imgName == "" {
			d.imageVolid = ""
			continue
		}
		res, ok := byRef[Ref{Kind: KindDiskImage, Name: imgName}]
		if !ok {
			return fmt.Errorf("%s: spec.disks[%d].image references unknown DiskImage %q", v.Ref(), i, imgName)
		}
		di, ok := res.(*DiskImage)
		if !ok {
			return fmt.Errorf("%s: spec.disks[%d].image references %s which is not a DiskImage", v.Ref(), i, res.Ref())
		}
		if !containsString(di.Nodes(), v.Spec.Node) {
			return fmt.Errorf("%s: spec.disks[%d].image %q is not placed on node %s (DiskImage nodes: %v)",
				v.Ref(), i, imgName, v.Spec.Node, di.Nodes())
		}
		d.imageVolid = di.Spec.Storage + ":import/" + di.Spec.Filename
	}
	return nil
}

// containsString reports whether s is in the slice.
func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
