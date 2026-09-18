# Repository layout

A ProxOps GitOps repository is organised around the **GitOps composition
model** ([Architecture → Multi-cluster
composition](ARCHITECTURE.md#multi-cluster-composition)):

```
proxops.yaml                    # OPTIONAL process-wide config (see
                                #   "Reference: configuration model")
clusters/
  <cluster>/
    config.yaml                 # cluster-local ProxOps config: this cluster's
                                #   PVE endpoint + node allowlist + SOPS
                                #   reference. Declares exactly one entry,
                                #   pve.clusters.<cluster>, named after its
                                #   directory.
    secrets.sops.yaml           # SOPS/age-encrypted PVE + git credentials
                                #   (referenced by config.yaml; see "SOPS &
                                #   credentials"). Private age key lives
                                #   OUTSIDE the repository.
    resources.yaml              # composition: the explicit list of resource
                                #   files this cluster consumes
<kind>/
  base/…                        # reusable definitions, shared by reference
  <cluster>/…                   # cluster-specific definitions
```

where `<kind>` is one of `vm`, `lxc`, `iso`, `ctt`, `templatevm`,
`diskimage`, `templatect`.

Resource manifests (under one of the kind roots) do NOT carry credentials —
a secret is never a spec field on a resource.

A manifest file is only reconciled if some cluster's
`clusters/<cluster>/resources.yaml` lists it; the cluster boundary is the
safety model. Resources placed directly under a kind root (a flat
`vm/foo.yaml` layout) are a **validation error** — ProxOps never invents a
default cluster.
Every manifest has this envelope:

```yaml
apiVersion: proxops/v1alpha1   # only supported value
kind: VM | LXC | CTTemplate | ISO | TemplateVM | DiskImage | TemplateCT
metadata:
  name: human-readable-name      # required, [a-z0-9](-[a-z0-9])*
  labels:                        # optional, free-form
    role: workers
  annotations:                   # optional, proxops/* hints
---
spec: {...}                      # kind-specific (below)
```

`metadata.name` is the ProxOps identity within a kind. PVE identity is
additionally pinned by `spec` fields (below) — **the agent never invents PVE
ids** for objects that have one. The kinds that have **no** PVE numeric id
(ISO, CTTemplate, and DiskImage — all *storage artifacts*) are identified on
PVE by `(node, storage, filename)` and on ProxOps by `metadata.name`.

The four kinds that carry a PVE numeric id (VM, LXC, TemplateVM and
TemplateCT) share PVE's per-node integer pool: a `spec.vmid` collision
inside one cluster's composition is a parse error. A TemplateVM and a VM can
never claim the same `(node, vmid)` (PVE lists both as `type="qm"`); a
TemplateCT and an LXC can never claim the same `(node, vmid)` (PVE lists both
as `type="lxc"`).

---


## Dependency model

### Structured references (inferred + escape hatch)

ProxOps builds a **dependency graph** from two sources and schedules
creates topologically so prerequisites finish **before** their dependants:

1. **Structured references (inferred, preferred).** The schema knows about
   these cross-kind edges:
   - `VM.spec.hardware.cdrom.iso` → an `ISO`
   - `VM.spec.disks[].image` → a `DiskImage`
   - `VM.spec.clone` → a `TemplateVM` (the VM is provisioned by cloning the
     template; the template must exist and be marked before the clone runs)
   - `LXC.spec.template` → a `CTTemplate`
   - `TemplateVM.spec.hardware.cdrom.iso` → an `ISO` (TemplateVM re-uses the
     VM surface, so the same edge applies)

   These are detected automatically — you do **not** have to repeat them with
   an annotation. The planner downloads the ISO / disk image / container
   template before it creates the VM / LXC that references it.
2. **`depends-on` annotation (escape hatch).** For relationships that cannot
   be expressed in a structured field:

   ```yaml
   metadata:
     name: app-vm
     annotations:
       proxops/depends-on: "ISO:app-iso,VM:other-vm"
   ```

   The value is a comma-separated list of `Kind:name` refs.

Behaviour:

- **Unknown references fail closed** — a `cdrom.iso`, `template`, or
  `depends-on` target that is not defined *in the cluster's own
  composition* aborts that cluster's cycle. There is **no cross-cluster
  dependency resolution**: a VM in cluster `A` referencing an ISO that
  only cluster `B` lists is a parse error for `A`. (A dependency becomes
  valid only when the target is in the referencing cluster's index — which
  is always the case for a shared base, since both clusters list the same
  file.)
- **Cycles fail closed** — a dependency cycle aborts the whole cycle.
- **Deterministic order** — creates are ordered by topological *level*
  (artifacts at level 0, the VM/LXC that reference them at level 1, and so
  on), then node + id, so plans are stable across cycles.
- **A failed prerequisite defers dependants in-cycle** — if a prerequisite's
  PVE download fails this cycle, dependants are not attempted; they are
  retried on the next cycle (reconciliation is re-derived from live state each
  cycle; there is no persistent state).
- **Prune order respects reverse dependencies** — dependants are deleted
  before prerequisites, so an ISO/template is never pruned out from under a
  live VM/LXC that references it.

---

