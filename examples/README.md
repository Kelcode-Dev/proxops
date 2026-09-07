# pveconform example manifest set

A small, realistic stack to try against a development PVE cluster
(nodes `pve01` + `pve02` in these examples). To use:

1. Copy this directory into your manifest repo (git).
2. Ensure PVE storage on each node has `content` including `iso` and `vztmpl`
   (e.g. a `local` dir storage with ISO + container-template pools, or a
   dedicated storage).
3. Point pveconform at the repo (see `../config/pveconform.yaml`), then
   `pveconform diff` → `pveconform apply`.

Files:

- `golden-base.ctt.yaml` — CTTemplate: a PVE `vztmpl` **storage artifact**
  (downloaded from `spec.url` into `spec.filename` on every `spec.nodes`
  pair). No numeric PVE id, no clone, no mark-template step. New LXCs are
  bootstrapped from this file by PVE itself at create time (`ostemplate`);
  pveconform owns only the storage artifact, never the LXC.
- `base-iso.yaml` — ISO: ensures the Talos installer image is present on
  `local` storage on every declared node, downloading from `spec.url` if it
  is not. No numeric PVE id.
- `cache-vm.yaml` — VM: Talos worker with pinned hardware, options, and the
  three-state CD/DVD documented in its `hardware:` comment (unmanaged by
  default so no ISO is required to create this VM).
- `cache-ct.yaml` — LXC: cache container pinned to `pve01`. References
  `golden-base` via `spec.template` — a structured `LXC → CTTemplate`
  dependency is inferred; the planner downloads the file first.
- `app-vm.depends-on.yaml` — VM: shows the optional `proxops/depends-on`
  annotation escape hatch combined with a structured `cdrom.iso` reference.
  The parser merges both edge sets before the planner schedules creates.

PVE id-space note: the kinds that DO carry a numeric PVE id (VM, LXC) share
ONE per-node integer pool. These examples pin well-separated ids to keep that
obvious to readers: 142 (VM), 400 (annotation-example VM), 9000 (LXC). The
artifact kinds (`ISO`, `CTTemplate`) have **no** numeric PVE id — their only
identity is `(node, storage, filename)` on the PVE side and `metadata.name`
on the pveconform side.
