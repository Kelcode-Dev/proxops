# pveconform example manifest set
#
# A small, realistic stack to try against a development PVE cluster
# (node `pve01` in these examples). To use:
#
#   1. Copy this directory into your manifest repo (git).
#   2. Have a PVE storage with an ISO pool on `pve01` (e.g. the default
#      `local`/`iso` dir, or a dedicated storage with content=iso).
#   3. Create PVE CT 200 on pve01 (any stopped base container) — it is the
#      clone source that `golden-base.ctt.yaml` needs. pveconform never
#      invents sources.
#   4. Point pveconform at the repo (see ../config/pveconform.yaml), then
#      `pveconform diff` → `pveconform apply`.
#
# Files:
#   golden-base.ctt.yaml     CTTemplate: clone PVE CT 200 -> pinned 1000, mark
#                            as template
#   base-iso.yaml            ISO: ensure talos installer is on `local` storage
#   cache-vm.yaml            VM: talos worker (mirrors the golden schema test
#                            sample)
#   cache-ct.yaml            LXC: cache container
#   app-vm.depends-on.yaml   VM: shows the proxops/depends-on annotation
#
# PVE id-space note: all kinds with a numeric PVE id share ONE per-node
# integer pool. These examples pin well-separated ids to keep that obvious to
# readers: 1000 (CTT dest), 200 (CT source, external), 142 (VM), 400 (VM),
# 9000 (LXC). The PVE id of the clone SOURCE is fixed by PVE (you cannot
# rename it in place) — that is why the manifest pins both source and dest.
