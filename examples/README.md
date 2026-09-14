# ProxOps example manifest set

A small, realistic stack to try against a development PVE cluster
(nodes `pve01` + `pve02` in these examples). To use:

1. Copy this directory into your manifest repo (git).
2. Ensure PVE storage on each node has `content` including `iso`, `vztmpl`
   and `import` (e.g. a `local` dir storage with ISO + container-template +
   disk-image pools, or a dedicated storage).
3. Point ProxOps at the repo (see `../config/proxops.yaml`), then
   `proxops diff` → `proxops apply`.

The examples are arranged in ProxOps's multi-cluster GitOps shape:

```
clusters/
  example/
    resources.yaml      # the "example" cluster's composition (NOT a
                        # Kustomization — ProxOps has no Kustomize
                        # semantics)
iso/
  base/
    base-iso.yaml       # reusable BASE ISO: a PVE `iso` storage artifact
ctt/
  base/
    golden-base.yaml    # reusable BASE CTTemplate: a PVE `vztmpl` storage
                        # artifact (downloadable, no numeric PVE id, no
                        # clone, no mark-template step; LXCs are
                        # bootstrapped from it by PVE at create time via
                        # `ostemplate`)
diskimage/
  base/
    debian-13-cloud.yaml # reusable BASE DiskImage: a PVE 9 `import` storage
                        # artifact (qcow2/vmdk/raw). A VM disk references it
                        # via `spec.disks[].image` and ProxOps seeds the
                        # disk at create with PVE's `import-from` form — the
                        # way to boot a real VM from a cloud image without a
                        # template.
vm/
  example/
    talos-worker-01.yaml  # cluster-specific VM: Talos worker with pinned
                          # hardware, options, and the three-state CD/DVD
                          # documented in its `hardware:` comment
    app-vm.yaml           # cluster-specific VM: shows the optional
                          # `proxops/depends-on` annotation escape
                          # hatch combined with a structured `cdrom.iso`
                          # reference
    cloudinit-vm.yaml     # cluster-specific VM: the end-to-end cloud-init
                          # shape — a DiskImage-seeded disk + cloud-init
                          # drive + ci-user/ssh-keys/nameservers/ipconfig
                          # (validated live on PVE 9.2)
lxc/
  example/
    cache-01.yaml         # cluster-specific LXC: cache container pinned to
                          # pve01; references `golden-base` via
                          # `spec.template` — a structured `LXC → CTTemplate`
                          # dependency is inferred; the planner downloads the
                          # file first
templatevm/
  example/
    almalinux-tpl.yaml    # cluster-specific TemplateVM: a qemu VM promoted to
                          # a PVE template (template=1); state must be stopped
```

- **base resources** under `<kind>/base/` are reusable: any cluster may list
  the very same file in its `clusters/<cluster>/resources.yaml`, and the
  file is not owned by any single cluster.
- **cluster-specific resources** under `<kind>/<cluster>/` belong to that
  cluster's composition; another cluster can only reference them if it lists
  them explicitly.
- **The cluster's config** lives at `clusters/<cluster>/config.yaml`; the
  ProxOps process can read its PVE endpoints + credentials either from
  that cluster-local file (recommended, SOPS-backed — see
  docs/OPERATIONS.md) or from a root bootstrap config (see
  `config/proxops.yaml` for the template). The GitOps composition file
  `resources.yaml` is the per-cluster Git boundary.
  The parser merges both edge sets before the planner schedules creates.

PVE id-space note: the kinds that DO carry a numeric PVE id (VM, LXC,
TemplateVM) share ONE per-node integer pool. These examples pin
well-separated ids to keep that obvious to readers: 142 (VM), 400
(annotation-example VM), 410 (cloud-init VM), 900 (TemplateVM), 9000 (LXC).
The artifact kinds (`ISO`, `CTTemplate`, `DiskImage`) have **no** numeric
PVE id — their only identity is `(node, storage, filename)` on the PVE side
and `metadata.name` on the ProxOps side.
