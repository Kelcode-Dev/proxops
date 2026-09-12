# GAPS — PVE configuration pveconform does not fully model, reconcile, or adopt

This is a **living compatibility backlog**: PVE configuration and features that
pveconform encounters (during reconciliation, drift, or `adopt`) but does not
currently fully model/reconcile/adopt. Each entry records enough to prioritise
future implementation.

It is source-controlled project documentation, **not runtime state**: pveconform
never writes to this file. `adopt` surfaces per-run gaps in its report (the
`Gaps` list + `Incomplete` manifests); the operator reviews and (if warranted)
adds or updates entries here.

## Statuses

| Status | Meaning |
|---|---|
| `discovered` | seen in live PVE / adoption output; not yet analysed |
| `investigated` | analysed: pveconform deliberately does not model it today, impact understood |
| `planned` | a milestone will implement it |
| `deliberate` | pveconform will NOT model this (by design) |

## Conventions

- **PVE field**: the wire key as PVE reports it (or the API form-value pveconform
  would need).
- **Priority**: `low` / `medium` / `high` — weighted by data-loss risk, drift
  flapping risk, and adoption-fidelity impact.

## Gaps

### VM: live-only disk properties beyond pool/size/iothread

- **Resource/area**: VM disks (`scsi*`, `virtio*, `sata*`)
- **PVE configuration/API field**: drive property options other than
  `iothread` — e.g. `discard=on`, `ssd=1`, `mbcache=force`, `aio`
- **Status**: discovered
- **Priority**: low
- **What is unsupported**: pveconform's owned disk surface is pool + size +
  iothread + slot + controller. PVE reports additional drive options; adopted
  manifests do not carry them, and `Drift` ignores them.
- **Impact/risk**: none today (the option is not written back, so it neither
  flaps nor mutates). An adopted VM is incomplete; an in-place PVE change of
  `discard=` would not be visible to pveconform.
- **Discovery source**: live conformance-dev `/qemu/{id}/config` reports during
  M8 adopt; `schema.parseDiskInfo`.
- **Notes**: adding a structured `Disk.Options` (map of passthrough tokens) is a
  natural M9 candidate. Until then such options are treated as PVE-owned.

### LXC: `ostemplate` is unrecoverable after create

- **Resource/area**: LXC root source (`spec.template`)
- **PVE configuration/API field**: `ostemplate` (create-only form-value)
- **Status**: investigated
- **Priority**: medium
- **What is unsupported**: PVE does not persist the ostemplate a container was
  booted from; `GET /nodes/{n}/lxc/{id}/config` omits it. pveconform can adopt
  every other owned LXC field, but `spec.template` requires a human.
- **Impact/risk**: adoption output is explicit about this (a Gap + an
  `Incomplete` manifest that `resources.yaml` must not list until the operator
  sets `spec.template`); no silent loss. A re-created LXC (after manual delete)
  would need the template again, which a manifest-less round-trip cannot supply.
- **Discovery source**: M8 live adopt of conformance-dev CT 9200; PVE API
  documentation (`ostemplate` is a /lxc create parameter).
- **Notes**: the operator's review step is part of the M8 contract
  (docs/OPERATIONS.md → "Adopting existing PVE objects").

### LXC: container mount points with pre-existing paths

- **Resource/area**: LXC mount points (`mp*`)
- **PVE configuration/API field**: `mpN=<path>:/<path>` (bind mounts) vs
  `mpN=<pool>:<size>` (allocated volumes)
- **Status**: discovered
- **Priority**: low
- **What is unsupported**: pveconform models only *allocated* LVM/dir volumes
  (`mpN=<pool>:<size>,mp=<mountpoint>`). PVE bind-mounts of a host path
  (`mp0=/mnt/share:/srv/data`) are a different shape not owned by pveconform.
- **Impact/risk**: a bind mount on a live LXC is not adoptable and not
  reconciled; it will not flap or be removed (pveconform never owns it).
- **Discovery source**: PVE pct.conf(5) mpN grammar; M8 schema review.
- **Notes**: a future `LXCBinding` shape (path + read-only + optional
  bind-options) would close this.

### ISO / CTTemplate: download URLs are not part of PVE's state

- **Resource/area**: ISO, CTTemplate (artifact kinds)
- **PVE configuration/API field**: (none — PVE has no concept of
  `spec.url` for an already-present file)
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: pveconform's artifact manifests carry `spec.url`
  (plus optional `spec.checksum`) as the *download source* pveconform uses when
  the file is absent on a node. PVE does not report where a present file came
  from, so `adopt` emits `spec.url: https://placeholder.invalid/...` and marks
  the URL as a Gap.
- **Impact/risk**: an adopted ISO/CTTemplate is *placement-faithful* (presence
  on the observed nodes) but *re-seed-incomplete*: on a fresh node pveconform
  would fail the download until the operator replaces the placeholder URL.
  This is deliberate: inventing a URL from a PVE name would be guesswork.
- **Discovery source**: M8 live adopt; PVE storage content listing shape.
- **Notes**: the operator's review step replaces the placeholder URL before the
  artifact is listed in `clusters/<cluster>/resources.yaml`.

## Deliberately not modelled (out of scope by design)

These are *deliberate* non-goals recorded so future readers know they were
considered:

- **VM replication** (`repl1`, `replN`, and the replication `schedule`): pveconform
  does not model PVE replication. It is a PVE-side DR feature with its own target
  + schedule + failover semantics; pveconform's ownership model (idempotent
  converge-to-git) does not map to it. Discovery source: known PVE API, recorded
  during M8 as a pre-existing gap. Status: `deliberate`.
- **PVE storage resources** (creating/disabling storage backends): the
  P9 scope control keeps pveconform off storage administration; it only reads
  storage (content listing, downloads). Status: `deliberate` for M8+.
- **PVE firewall / netfilter**: per-VM/per-LXC firewalls and the cluster
  firewall are untouched. pveconform models NIC `firewall=1` toggle only.
  Status: `deliberate`.
- **HA resources, pools, users, roles, SDN**: out of the GitOps model. Status:
  `deliberate`.

## M9 (SOPS-backed cluster configuration) — new gaps / deliberate limitations

Entries added during the M9 implementation pass:

- **External `sops` binary dependency (`internal/secrets`)**
  - **Resource/area**: configuration layer
  - **PVE configuration/API field**: n/a
  - **Status**: `deliberate`
  - **What is unsupported**: pveconform shells out to `sops --decrypt`
    (age backend); it does NOT link the SOPS Go module. When a cluster
    references `secrets-file`, `sops` + `age` MUST be on PATH on the host
    pveconform runs on; otherwise `ErrSOPSBinaryMissing` is surfaced at
    agent construction. No KMS or GCP/Azure/Ali/Huawei/PGP backends are
    supported — only `age`.
  - **Discovery source**: M9 design decision (task §16). The Go module
    `github.com/getsops/sops/v3` would pull ~160 transitive deps; the SOPS
    CLI is already mandatory on any host where SOPS-encrypted secrets are
    managed (the operator encrypts with it). Documented in
    `docs/OPERATIONS.md` § "Per-cluster SOPS secrets (M9)".

- **SOPS identity (age private key) lifecycle / rotation**
  - **Resource/area**: credentials
  - **Status**: `planned` (out of M9 scope, task §20 "automatic key rotation"
    and "automatic secret rotation" explicitly excluded)
  - **What is unsupported**: pveconform does NOT rotate SOPS recipients.
    Rotation is an operator workflow: add new recipient to SOPS command
    line, re-encrypt the file, commit both. The private age key has no
    built-in "grace period / revoke" — it is whatever the operator's
    SOPS_AGE_KEY_FILE points at.
  - **Discovery source**: task §16 "do not add automatic key rotation"
    and task §20 (SCOPE CONTROL).

- **No SOPS file watching / hot reload of credentials**
  - **Resource/area**: runtime
  - **Status**: `planned`
  - **What is unsupported**: SOPS decryption happens once at agent
    construction. A rotated PVE token in the SOPS file is not picked up
    without a process restart. Same as M8 env-var credentials: the
    operator's job is to `systemctl restart pveconform` after rotating.
    `poll-interval` re-diffs git + PVE but does NOT re-decrypt secrets.
  - **Discovery source**: M9 design decision; no reason to watch a
    SOPS file during a reconcile cycle when the same behaviour applies
    to env vars.

- **SOPS `git-token` conflict handling**
  - **Resource/area**: git source
  - **Status**: `investigated`
  - **What is unsupported**: If TWO cluster SOPS files both name
    `git.token` keys that resolve to DIFFERENT values, pveconform fails
    closed with "git token conflict" at agent construction. This is
    deliberate: the pveconform git source is single, one worktree, one
    fetch token — two different values would mean "clone two different
    git repos" which is out of scope. When two clusters share the same
    git token value, that value is used for all.
  - **Discovery source**: M9 implementation of `app.EffectiveGitToken`.

- **No per-cluster CA pinning via SOPS**
  - **Resource/area**: TLS
  - **Status**: `deliberate`
  - **What is unsupported**: `pve.ca-file` is a per-pve level setting,
    shared across clusters. To pin a different CA per cluster, the
    operator must use M8 style (a global config that does not reference
    SOPS for that cluster). SOPS does not (today) have a "ca-file"
    credential field. If multi-CA pinning becomes necessary, add a
    `PVECredentialKeys.CAFile` + SOPS `pve-ca-file` entry and resolve it
    alongside the user/token fields.
  - **Discovery source**: M9 design decision; task §20 "PVE Storage
    resources (out of scope)" implies CA pinning is a TLS concern, not
    a PVE API concern, and the M8 global CA is sufficient for most
    deployments.

## M10 (real prod-a adoption) — new gaps / PVE-9.2 wire findings

Entries discovered while reverse-engineering the **production** prod-a
PVE cluster (2026-09-10, PVE 9.2.2 on node `pve01`). Probe work happened on
the disposable conformance-dev cluster (never on prod-a, which is a
read-only target for adoption).

- **LXC: `keyctl` / `fuse` are adoptable but NOT convergable on PVE 9.x**
  - **Resource/area**: LXC options (`spec.options.keyctl`, `spec.options.fuse`)
  - **PVE configuration/API field**: `keyctl`, `fuse`
  - **Status**: `investigated`
  - **Priority**: low
  - **What is unsupported**: PVE 9.2's LXC create AND /config-PUT schema both
    REJECT `keyctl=` and `fuse=` as top-level form-values (HTTP 400/403,
    "property is not defined in schema"), and the PVE 9.x `features` composite
    only recognizes a `nesting=<0|1>` token — `features=keyctl=1` /
    `features=fuse=1` are 403. Yet PVE's /config report CAN carry these keys
    (a container created via pct/webUI sets them). pveconform therefore
    **adopts** `keyctl`/`fuse` into `spec.options` (faithful capture) but
    **cannot converge** a desired=on onto a live=off container — Drift
    surfaces a non-destructive anomaly instead of submitting a 400-guaranteed
    write.
  - **Impact/risk**: none today (the option is not written back). An adopted
    LXC that has keyctl/fuse on PVE is represented; a manifest that asks to
    enable them on a new container fails closed at create-params
    (`ToCreateParams` error names the blocked option).
  - **Discovery source**: M10 probe (disposable CTs 9881/9882 on conformance-dev,
    both destroyed; 403 at create). Pinned: `internal/schema/lxc_wire_regressions_test.go`
    (`TestLXCToCreateParams_KeyctlTrueBlocks`,
    `TestLXCDrift_KeyctlTrueLiveOffIsAnomalyNoWrite`).
  - **Notes**: closing this requires a PVE-side `pct set` step (out of
    pveconform's API surface). If PVE ever adds a wire form, drop the
    fail-closed + anomaly and wire the token through ToCreateParams/Drift.

- **LXC: `unprivileged` is create-only on PVE 9.x**
  - **Resource/area**: LXC options (`spec.options.unprivileged`)
  - **PVE configuration/API field**: `unprivileged`
  - **Status**: `investigated`
  - **Priority**: low
  - **What is unsupported**: PVE 9.2's /config-PUT returns HTTP 500 when
    given `unprivileged=0` or `=1` (probe: conformance-dev CT 9200). The
    flag is LXC-create-time-only. pveconform adopts it faithfully
    (`*bool`; an explicit 0 is owned, not dropped) but Drift cannot flip it —
    a divergent `unprivileged` surfaces as a non-destructive anomaly that
    names "a recreate is required to converge".
  - **Impact/risk**: none today (never written back). Adoption output is
    faithful; only a recreate path would ever change it.
  - **Discovery source**: M10 probe (PUT /lxc/9200/config unprivileged=0 → 500).
    Pinned: `TestLXCDrift_UnprivilegedTrueLiveOffIsAnomalyNoWrite`.
  - **Notes**: prod-a LXC 111 (nfs-server) reports `unprivileged=0` —
    the adopted manifest pins it via pointer-bool; Drift will not touch it
    (untagged until the operator lists it, and even then the anomaly is
    non-destructive).

- **LXC: `nesting` rides the PVE 9.x `features=` composite (not a top-level key)**
  - **Resource/area**: LXC options (`spec.options.nesting`)
  - **PVE configuration/API field**: `nesting` (PVE 8.x) / `features=nesting=0|1` (PVE 9.x)
  - **Status**: `investigated`
  - **Priority**: low
  - **What is unsupported (as a top-level wire token)**: PVE 9.2 rejects
    top-level `nesting=` on both /lxc create (400) and /config-PUT (400). The
    accepted form is the composite `features=nesting=<0|1>` (probe: create
    with `features=nesting=1` → 200 + report re-echoes the token; PUT
    `features=nesting=0` → 200). pveconform's owned `spec.options.nesting`
    maps to that composite on both the create and drift paths; top-level
    `nesting=` is NEVER emitted.
  - **Impact/risk**: none today (converged through the composite). prod-a
    LXC 203 (seaweedfs-01) reports `features=nesting=1` — adopted faithfully.
  - **Discovery source**: M10 probe (disposable CT 9880, destroyed; conformance-dev
    CT 9200 PUT probe). Pinned: `TestLXCToCreateParams_NestingAsComposite`,
    `TestLXCDrift_NestingEmitsFeaturesNotTopLevel`.
  - **Notes**: the PVE 9.x `features` composite today carries only `nesting`
    in pveconform's known grammar (keyctl/fuse tokens → 403, see first M10
    entry). If PVE adds new feature tokens, `Drift` would need to merge
    them (a bare `features=nesting=X` write today cannot clobber anything
    because no other PVE-owned feature token is known).

- **VM: cloud-init volume on a non-IDE slot is PVE-owned, not a pveconform disk**
  - **Resource/area**: VM disks
  - **PVE configuration/API field**: `<slot>=...-cloudinit,media=cdrom` where
    `<slot>` is `scsi*`/`virtio*`/`sata*`
  - **Status**: `investigated`
  - **Priority**: medium
  - **What is unsupported**: pveconform's cloud-init model is IDE-only
    (`spec.hardware.cloud-init` → `ide2`/`ide3`). PVE 9.2 can place a
    cloud-init cdrom volume on a data-slot bus instead — every
    prod-a k8s VM (100/101/102/120) + the template VM (999) report
    `scsi1=vm_disks:vm-NNN-cloudinit,media=cdrom,size=4M` on top of
    `scsi0=...-disk-1` (the root disk). pveconform cannot recreate such a
    slot (its create-time cloud-init form goes to ide2), so adoption
    **excludes** `media=cdrom`-shuffled slots from `spec.disks` and reports
    each one as a gap (`PveDiskMedia` detection is slot-agnostic:
    `media=cdrom` or a `*-cloudinit` PVE-assigned volume name).
  - **Impact/risk**: none today (the slot is never written; Drift surfaces it
    as a non-destructive live-only anomaly, matching the M7 live-only-disk
    guard). An adopted manifest is *disk-faithful* except for the PVE-owned
    cloud-init slot (which is documented in the gap report + GAPS.md).
  - **Discovery source**: M10 live prod-a `/qemu/{id}/config` (VM 100 –
    "app-prod-a" scsi1 + VM 999 scsi1, the latter a template VM see
    next entry). Pinned: `TestAdopt_CloudInitOnSATAAlsoExcluded` (slot
    agnosticity), `TestAdopt_PlainDataDiskStillAdopted` (the exclusion is
    narrow: no `media=cdrom` token → the disk IS adopted).
  - **Notes**: PVE-side, these slots were created by Talos/k8s provision
    tooling's `qm` usage (cloud-init on `scsi1`). A future
    `spec.hardware.cloud-init.slot` escape hatch (a cloud-init model that
    targets a data bus) would close this; until then such PVE objects are
    *documented*, not re-created.

- **VM: PVE template VMs (`template=1`) are out of adoption scope**
  - **Resource/area**: VM
  - **PVE configuration/API field**: `template=1`
  - **Status**: `investigated` (CLOSED by M11)
  - **Priority**: n/a
  - **What was unsupported**: pveconform had no "template VM" resource kind and
    must not claim ownership of a clone source (a pveconform VM manifest's
    disks are the clone *data*; converging one would risk re-creating the
    template's own disk on the first apply → data loss). M10's adopt therefore
    **skipped** PVE-template VMs: no manifest was written, the live object was
    recorded on `Result.Skipped` (census stays complete), and a gap named the
    skip.
  - **M11 resolution**: pveconform now has `kind: TemplateVM` (first-class
    resource). Adopt produces `kind: TemplateVM` manifests for PVE objects
    with `template=1` under `templatevm/<cluster>/` — replacing M10's
    skip+census behaviour. The M10 data-loss guard still holds on
    the `kind: VM` side: a pveconform `kind: VM` desired against a
    PVE-side `template=1` at the same `(node, vmid)` is a non-destructive
    anomaly, never a write. PVE 9.2 has no `/qemu/{id}/untemplate` endpoint
    (probe: `HTTP 501 "not implemented"` on conformance-dev 2026-09-11),
    so a kind-flip on the PVE side is an operator's manual step. Pinned:
    `TestAdopt_TemplateVMsAdoptedAsTemplateVMManifest` (M11 successor) +
    `TestE2E_VMDesiredButPVEIsTemplateSurfacesAnomaly` +
    `TestE2E_VMDesiredAgainstLiveNonTemplateMarksIt`.
  - **Discovery source**: M10 live prod-a: VM 999 `tpl-almalinux-10`
    (`template=1`, `scsi0=vm_disks:base-999-disk-1`), the Talos/almalinux
    clone source for every prod-a VM.

- **LXC: `ostype` is PVE-inferred bookkeeping, not an owned field**
  - **Resource/area**: LXC + VM
  - **PVE configuration/API field**: `ostype`
  - **Status**: `investigated`
  - **Priority**: low
  - **What is unsupported**: PVE infers `ostype` from the installed content /
    ostemplate — it is not a create/update form-value an operator controls.
    M10 moved `ostype` from the gap surface to `PVEBookkeepingKeys` (together
    with `digest`, `meta`, `vmgenid`, `smbios1`, `uuid`, `ostemplate`), so it
    no longer surfaces as a "pveconform does not model" finding.
  - **Impact/risk**: none (purely a gap-report noise-reduction change).
  - **Discovery source**: M10 live prod-a (every VM + LXC reports
    `ostype=`; none is a pveconform form-value).
  - **Notes**: no schema change — just adopt's bookkeeping-key list.

- **LXC: `cmode` / `tty` / `console` / `cpulimit` / `cpuunits` / raw `lxc.` — not modelled**
  - **Resource/area**: LXC options + raw config
  - **PVE configuration/API field**: `cmode`, `tty`, `console`, `cpulimit`,
    `cpuunits`, `lxc.`
  - **Status**: `discovered`
  - **Priority**: low
  - **What is unsupported**: these PVE LXC fields are not in pveconform's
    LXCOptions / LXCExtra model. prod-a reports several of them
    (110: `cmode=tty`, `tty=2`, `cpulimit=0`, `cpuunits=1024`; 111: also
    `lxc = [['lxc.apparmor.profile','unconfined']]`; 110/111/203: `console=1`).
    M10 **partially** closes this: `console` is now adopted + convergable
    (top-level create-accepted + /config-PUT-accepted); `cmode`, `tty`,
    `cpulimit`, `cpuunits`, and raw `lxc.` lines remain unmodelled (each
    surfaces as a named gap).
  - **Impact/risk**: none today (no write path). Adoption reports them;
    operators see them in the gap set. A future `LXCOptions.TTYCount` /
    `LXCOptions.CPULimit` (and an `LXC.LxcConf` raw escape) would close
    cmode/tty/cpulimit/cpuunits/lxc — deliberately deferred.
  - **Discovery source**: M10 live prod-a /lxc/{110,111,203}/config.
    Pinned (console adopted): `TestAdopt_ZeroWritesOnProdFixtureEquivalent`.
  - **Notes**: `cpulimit` and `cpuunits` are PVE 9.x CPU-weight knobs
    (default 0/1024 are PVE's "no limit / default weight"); pveconform does
    not adopt its PVE defaults, so they always surface on non-default live
    values.

- **VM: cloud-init on pveconform-VMs — no `ciuser`/`cipassword`/`sshkeys`/`ipconfig`/`nameserver` model**
  - **Resource/area**: VM cloud-init fields (top-level PVE keys on the same
    object as a pveconform VM)
  - **PVE configuration/API field**: `ciuser`, `cipassword`, `sshkeys`,
    `ipconfig0`, `nameserver`, `cicustom`, `ciupgrade`
  - **Status**: `discovered` (CLOSED by M11 for the non-secret subset;
    remains `discovered` for `cipassword`/`cicustom`/`ciupgrade`)
  - **Priority**: low
  - **What is unsupported**: pveconform's VM model does not carry
    cloud-init user credentials / SSH keys / static-ip or DNS fields at
    the top level (its only cloud-init surface is an ide2/ide3 volume).
    prod-a's k8s VMs report all of these (5 VMs each: 100/101/102/120/
    999). M10 adds **redaction** (not adoption): `sshkeys` and `cipassword`
    gap values are emitted as `<redacted>` — the field name still reports
    ("pveconform does not model this") but the value NEVER reaches stdout,
    logs, the gap report, or any generated manifest.

  **M11 resolution**: pveconform now models `ciuser`, `nameserver`,
  `searchdomain`, `ipconfig<N>`, and (redacted) `sshkeys` under
  `spec: cloud-init-data`. `cipassword` and `cicustom` remain PII / PVE-side
  — they continue to surface as gaps with the M10 `<redacted>` value rule.
  `ciupgrade` remains out-of-model. Pinned: `TestVMSpecCloudInitData_*`
  (schema layer, 7 sub-tests) + e2e `TestE2ETemplateVMCreateMarksAndIsIdempotent`.
  - **Impact/risk**: none (no write path; PII is redacted at the source,
    the adopt layer). The operator's review step (or a future
    `VM.CloudInit` model) would close these.
  - **Discovery source**: M10 live prod-a /qemu/{100,101,102,120,999}/
    /config. Pinned: `TestAdopt_ZeroWritesOnProdFixtureEquivalent`
    (sentinel "hunter2" + "AAAAB3NzaC1yc2E" must not appear in the report;
    `sshkeys`/`cipassword` gap values must contain `<redacted>`), and
    `TestAgent_GeneratedOutputContainsNoCredentialMaterial`
    (pveconform's own SOPS credential sentinels must not appear in Result
    text, logs, warnings, skipped, incomplete, or any generated manifest).
  - **Notes**: `ipconfig0` and `nameserver` are PVE cloud-init's own
    static-IP model; pveconform's VM networking is the `netN` data-slot
    property list (model+bridge+MAC+vlan+rate+firewall) — IP on a VM is PVE
    cloud-init-owned, not pveconform net-owned.

- **LXC: static `ip=`/`gw=` on LXC netX — M10 adds adoption, PVE 9.2 create-time
  probe confirmed convergent**
  - **Resource/area**: LXC networks
  - **PVE configuration/API field**: `ip=<addr/prefix>`, `gw=<addr>` inside
    the `netX` property string
  - **Status**: `investigated` (CLOSED by M10)
  - **Priority**: n/a (closed)
  - **What was unsupported**: pveconform's LXCNetwork did not carry
    `ip=`/`gw=`. M10 adds `LXCNetwork.Ip` / `LXCNetwork.Gw` (tri-string,
    both omitempty; nil/empty = PVE decides) on the wire form, and
    `parseLXCNetFields` / `PveLXCNetworksFromPVE` capture them on the
    report form. PVE 9.2 create + /config-PUT both accept the
    `netX=...,ip=...,gw=...` form (probe: disposable CT 9876 on
    conformance-dev, created with `ip=192.168.3.100/24,gw=192.168.3.1` and
    confirmed the report re-echoes both, then destroyed).
  - **Impact/risk**: none (new convergent surface).
  - **Discovery source**: M10 live prod-a /lxc/{110,111,203}/config
    (all three carry `ip=192.168.192.1XX/18,gw=192.168.192.5`). Pinned:
    `TestAdopt_ZeroWritesOnProdFixtureEquivalent` (asserts the adopted
    LXC 111 has ip/gw captured, NOT a gap).
  - **Notes**: `hwaddr` (PVE-assigned MAC) + `type` (PVE normalizes to
    veth) remain intentionally NOT adopted (same M8 rule: PVE-owned values
    are captured as gaps + not wired into spec.networks; a pinned MAC on
    the manifest is respected, random MACs are omitted).
## M11 (cloud-init data + TemplateVM kind) — new closed gaps / PVE-9.2 wire findings

M11 closes the two M10/M11-era gaps above (PVE template-VMs out of adoption
scope; VM cloud-init user/ssh/nameserver/static-ip). New entries below record
what M11 pins.

- **PVE 9.2 wire: `/qemu/{id}/untemplate` does not exist**
  - **Resource/area**: VM / TemplateVM
  - **PVE configuration/API field**: `POST /nodes/{n}/qemu/{id}/untemplate`
  - **Status**: `investigated` (wire finding, pinned)
  - **Priority**: n/a
  - **What is unsupported**: PVE 9.2's `/qemu/{id}/untemplate` endpoint is
    *not implemented* — probe on conformance-dev PVE 9.2.2 (VM 9100,
    2026-09-11): `HTTP 501 "Method 'POST /nodes/pve-dev-01/qemu/9100/untemplate'
    not implemented"`. In contrast, `/lxc/{id}/untemplate` IS implemented
    (LXC-side M3 behaviour). pveconform must therefore not attempt a
    kind-flip on the PVE side: the planner converts a `kind: VM` desired
    against a PVE-side `template=1` into a non-destructive anomaly.
  - **Impact/risk**: none (the anomaly is non-destructive; PVE-side kind-flip
    stays a manual operator step via `qm` from a PVE host).
  - **Discovery source**: M11 disposable VM 9100 probe on conformance-dev.
    Pinned: `internal/pveclient/mock/mock.go` (mock 501 for
    POST /qemu/{id}/untemplate) + `TestE2E_VMDesiredButPVEIsTemplateSurfacesAnomaly`.

- **PVE 9.2 wire: `DELETE /qemu/{id}` works on a template VM**
  - **Resource/area**: TemplateVM
  - **Status**: `investigated`
  - **What is new**: a pveconform-owned `TemplateVM` that is no longer
    desired IS pruned via `DELETE /qemu/{id}`; PVE does not reject the
    delete because the object is a template (probe on conformance-dev,
    2026-09-11). The executor's pre-delete stop stays conservative:
    templates PVE-side are always "stopped", so the STOP step on an already-
    stopped template VM is a no-op.
  - **Discovery source**: same disposable VM 9100 probe.

- **PVE 9.2 wire: `POST /qemu` `cloud-init data fields` are create-and-config-
  put-accepted**
  - **Resource/area**: VM / TemplateVM / Cloud-Init Data
  - **PVE configuration/API field**: `ciuser`, `sshkeys`, `nameserver`,
    `searchdomain`, `ipconfig<N>`
  - **Status**: `investigated`
  - **What is pinned**: PVE 9.2 accepts these fields as `POST /qemu`
    create form-values AND as `POST /qemu/{id}/config` update form-values,
    in both directions:
      - `POST ciuser=X + ipconfigN=Y` → task exit=OK, /config report shows
        `"ciuser": "X"` / `"ipconfig0": "Y"`.
      - `POST ciuser= + ipconfigN= + nameserver= + searchdomain=` (empty
        strings) → task exit=OK, PVE stores a whitespace placeholder
        (`" "`) NOT `""`, and subsequent /config report reflects that.
    pveconform's semantics: empty desired ⇒ not owned ⇒ no write; set
    desired ⇒ write on create-or-drift.
  - **Impact/risk**: none (the empty-vs-nonempty desired distinction
    prevents pveconform from overwriting a PVE-side value).
  - **Discovery source**: M11 disposable VM 9100 probe. Pinned:
    `TestVMSpecCloudInitData_EmptyNotOwned` + `TestVMSpecCloudInitData_Drift/empty-desired-no-write`.

- **PVE 9.2 wire: ssh-keys sentinel semantics**
  - **Resource/area**: VM cloud-init data
  - **What is pinned**: pveconform's `spec.cloud-init-data.ssh-keys`
    supports exactly two shapes:
      - real keys → pveconform writes the CSV verbatim on create/drift.
      - `["*"]` (the M10 redacted sentinel) → pveconform does NOT write
        `sshkeys` on create/drift, letting PVE keep its live value.
      - Mixed real + `"*"` → `Validate()` fails closed (ambiguous: does
        pveconform write? does it leave alone?).
  - **Impact/risk**: none (the mixed shape is refused at parse time).
  - **Pinned**: `TestVMSpecCloudInitData_SentinelSSHKeysNotOwned` +
    `TestVMSpecCloudInitData_MixedSentinelFailsClosed`.
