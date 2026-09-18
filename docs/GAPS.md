# GAPS — PVE configuration ProxOps does not fully model, reconcile, or adopt

This is a **living compatibility backlog**: PVE configuration and features
that ProxOps encounters (during reconciliation, drift, or `adopt`) but does
not currently fully model, reconcile, or adopt. Each entry records enough to
prioritise future implementation.

It is source-controlled project documentation, **not runtime state**:
ProxOps never writes to this file. `adopt` surfaces per-run gaps in its
report (the `Gaps` list + `Incomplete` manifests); the operator reviews and
(if warranted) adds or updates entries here.

Closed items are removed from this file — the implementation + its
regression tests are the record. (The project was previously named
`pveconform`; entries below describe current behaviour under the `proxops`
name. See OPERATIONS.md → "Ownership-tag migration" for the tag rename.)

## Statuses

| Status | Meaning |
|---|---|
| `discovered` | seen in live PVE / adoption output; not yet analysed |
| `investigated` | analysed: ProxOps deliberately does not model it today, impact understood |
| `planned` | a future milestone may implement it |
| `deliberate` | ProxOps will NOT model this (by design) |

## Conventions

- **PVE field**: the wire key as PVE reports it (or the API form-value
  ProxOps would need).
- **Priority**: `low` / `medium` / `high` — weighted by data-loss risk,
  drift flapping risk, and adoption-fidelity impact.

## Open gaps

### VM: cpu.flags is declarative-only

- **Resource/area**: VM CPU (`spec.cpu.flags`)
- **PVE field**: `args` (PVE's free-form QEMU argument string)
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: `spec.cpu.flags` parses and round-trips in the
  manifest but is **never sent on the wire** and is **not adopted** —
  ProxOps does not map it to PVE's `args`. A live `args=` value surfaces
  as a gap on adopt.
- **Impact/risk**: none (no write path). Setting `cpu.flags` has no effect
  on PVE; use `spec.extra.args` for real QEMU arguments.
- **Discovery source**: schema review (`internal/schema/vm.go` — `Flags`
  has no create/drift reference).

### LXC: `ostemplate` is unrecoverable after create

- **Resource/area**: LXC root source (`spec.template`)
- **PVE field**: `ostemplate` (create-only form-value)
- **Status**: investigated
- **Priority**: medium
- **What is unsupported**: PVE does not persist the ostemplate a container
  was booted from; `GET /nodes/{n}/lxc/{id}/config` omits it. ProxOps can
  adopt every other owned LXC field, but `spec.template` requires a human.
- **Impact/risk**: adoption output is explicit about this (a Gap + an
  `Incomplete` manifest that `resources.yaml` must not list until the
  operator sets `spec.template`); no silent loss.
- **Discovery source**: live adopt of conformance-dev CTs; PVE API
  documentation (`ostemplate` is a /lxc create parameter).
- **Notes**: the operator's review step is part of the adoption contract
    (docs/adopt.md).
### LXC: bind-mount writes require root@pam (fail closed under API tokens)

- **Resource/area**: LXC bind mounts (`spec.bind-mounts`, `mpN` host-path form)
- **PVE field**: `mpN=<host-path>,mp=<guest-path>[,ro=<0|1>]`
- **Status**: investigated
- **Priority**: medium
- **What is unsupported**: M13 made bind mounts a first-class declarative +
  adopted + drift-detected shape, but PVE 9.2 restricts bind-mount **writes**
  to `root@pam`: an API-token request is rejected with HTTP 403
  `mount point type bind is only allowed for root@pam` at BOTH create and
  `/config` PUT (probe-verified on conformance-dev). ProxOps therefore
  submits the declaration faithfully and lets PVE enforce the permission —
  with a token identity the write action **fails closed** (a failed action on
  `/status`, never a silent skip or false convergence). Read/adoption works
  with any token that can read the CT config.
- **Impact/risk**: a cluster whose credential is an API token cannot
  converge bind mounts; the operator must use a `root@pam` ticket credential
  (`pve.auth: ticket`) for those clusters, or manage binds on PVE by hand.
  ProxOps never re-points a live bind's host path automatically (host-data
  safety) — that divergence is a non-destructive anomaly regardless of auth.
- **Discovery source**: live conformance-dev probes (disposable CTs 9320/9321,
  destroyed); PVE pct.conf(5) mpN grammar.
- **Notes**: allocated `mpN` volumes (the other bind-vs-volume shape) ARE
  fully convergable with a token — only the host-path bind form is gated.

### LXC: `keyctl` / `fuse` are adoptable but NOT convergable on PVE 9.x

- **Resource/area**: LXC options (`spec.options.keyctl`, `spec.options.fuse`)
- **PVE field**: `keyctl`, `fuse`
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: PVE 9.2's LXC create AND /config-PUT schema both
  REJECT `keyctl=` and `fuse=` as top-level form-values (HTTP 400/403,
  "property is not defined in schema"), and the PVE 9.x `features`
  composite only recognizes a `nesting=<0|1>` token — `features=keyctl=1` /
  `features=fuse=1` are 403. Yet PVE's /config report CAN carry these keys
  (a container created via pct/webUI sets them). ProxOps therefore
  **adopts** `keyctl`/`fuse` into `spec.options` (faithful capture) but
  **cannot converge** a desired=on onto a live=off container — Drift
  surfaces a non-destructive anomaly instead of submitting a
  400-guaranteed write.
- **Impact/risk**: none today (the option is not written back). A manifest
  that asks to enable them on a new container fails closed at
  create-params (`ToCreateParams` error names the blocked option).
- **Discovery source**: probe (disposable CTs on conformance-dev, all
  destroyed; 403 at create). Pinned:
  `internal/schema/lxc_wire_regressions_test.go`
  (`TestLXCToCreateParams_KeyctlTrueBlocks`,
  `TestLXCDrift_KeyctlTrueLiveOffIsAnomalyNoWrite`).
- **Notes**: closing this requires a PVE-side `pct set` step (out of
  ProxOps's API surface). If PVE ever adds a wire form, drop the
  fail-closed + anomaly and wire the token through
  ToCreateParams/Drift.

### LXC: `unprivileged` is create-only on PVE 9.x

- **Resource/area**: LXC options (`spec.options.unprivileged`)
- **PVE field**: `unprivileged`
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: PVE 9.2's /config-PUT returns HTTP 500 when
  given `unprivileged=0` or `=1`. The flag is LXC-create-time-only.
  ProxOps adopts it faithfully (`*bool`; an explicit 0 is owned, not
  dropped) but Drift cannot flip it — a divergent `unprivileged` surfaces
  as a non-destructive anomaly that names "a recreate is required to
  converge".
- **Impact/risk**: none today (never written back). Adoption output is
  faithful; only a recreate path would ever change it.
- **Discovery source**: probe (PUT /lxc/9200/config unprivileged=0 → 500).
  Pinned: `TestLXCDrift_UnprivilegedTrueLiveOffIsAnomalyNoWrite`.

### LXC: `ttys` / `cmode` / `cpulimit` / `cpuunits` / raw `lxc.` — not modelled

- **Resource/area**: LXC options + raw config
- **PVE field**: `ttys`, `cmode`, `cpulimit`, `cpuunits`, `lxc.`
- **Status**: discovered
- **Priority**: low
- **What is unsupported**: these PVE LXC fields are not reconciled.
  `spec.options.ttys` parses but is **never sent** (PVE 9.2 rejects
  `ttys=` at create) and is **not adopted** — it surfaces as a gap.
  `cmode`, `cpulimit`, `cpuunits`, and raw `lxc.` lines are likewise
  unmodelled and surface as named gaps. (`console` IS owned + convergable;
  `cpuunits` is not mapped from `spec.cpu.units`, which is declarative-only.)
- **Impact/risk**: none today (no write path). Adoption reports them.
- **Discovery source**: live `/lxc/{id}/config` reports on adopted
  clusters. Pinned (console owned): the LXC wire-regression tests.
- **Notes**: `cpulimit`/`cpuunits` are PVE CPU-weight knobs (defaults 0 /
  1024); ProxOps does not adopt PVE defaults, so they surface on
  non-default live values.

### Artifacts: download URLs are not part of PVE's state

- **Resource/area**: ISO, CTTemplate, DiskImage (artifact kinds)
- **PVE field**: (none — PVE has no concept of `spec.url` for an
  already-present file)
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: ProxOps's artifact manifests carry `spec.url` as
  the *download source* used when the file is absent on a node. PVE does
  not report where a present file came from, so `adopt` emits
  `spec.url: https://placeholder.invalid/...` and marks the URL as a Gap.
  `adopt` also does not scan the `import` content pool at all, so a
  DiskImage-backed VM adopts as a plain disk (pool+size), losing the image
  provenance.
- **Impact/risk**: an adopted artifact is *placement-faithful* (presence on
  the observed nodes) but *re-seed-incomplete*: on a fresh node ProxOps
  would fail the download until the operator replaces the placeholder URL.
  This is deliberate: inventing a URL from a PVE name would be guesswork.
- **Discovery source**: live adopt; PVE storage content listing shape.
- **Notes**: the operator's review step replaces the placeholder URL before
  the artifact is listed in `clusters/<cluster>/resources.yaml`. A future
  `adopt` pass over `import` content would emit `kind: DiskImage`
  manifests with placeholder URLs.

### Artifacts: `spec.checksum` wire acceptance is not live-verified

- **Resource/area**: ISO, CTTemplate, DiskImage
- **PVE field**: `checksum` + `checksum_algorithm` form-values on
  `POST /nodes/{n}/storage/{s}/download-url`
- **Status**: discovered
- **Priority**: low
- **What is unsupported**: ProxOps sends `checksum` and
  `checksum_algorithm` on the download-url request when `spec.checksum` is
  set. Whether PVE 9.2's `download-url` endpoint accepts these exact
  parameter names (vs PVE's documented `checksum-algorithm` spelling) has
  **not been probed against live PVE** — the in-memory mock ignores them,
  so CI cannot catch a rejection. Treat checksum verification as
  best-effort until a live probe pins it.
- **Impact/risk**: if PVE rejects the parameter, the download task fails
  (surfaced as a failed action, retried next cycle) — it does not silently
  skip verification.
- **Discovery source**: code review (`internal/schema/ctt.go`,
  `iso.go`, `diskimage.go` `ToCreateParams`). A conformance-dev probe
  would close this.

### VM: cloud-init volume on a non-IDE slot is PVE-owned

- **Resource/area**: VM disks
- **PVE field**: `<slot>=...-cloudinit,media=cdrom` where `<slot>` is
  `scsi*`/`virtio*`/`sata*`
- **Status**: investigated
- **Priority**: medium
- **What is unsupported**: ProxOps's cloud-init model is IDE-only
  (`spec.hardware.cloud-init` → `ide2`/`ide3`). PVE 9.2 can place a
  cloud-init cdrom volume on a data-slot bus instead — some clusters report
  `scsi1=...:vm-NNN-cloudinit,media=cdrom` on top of `scsi0` (the root
  disk). ProxOps cannot recreate such a slot (its create-time cloud-init
  form goes to ide2), so adoption **excludes** `media=cdrom`-shuffled
  slots from `spec.disks` and reports each one as a gap (`PveDiskMedia`
  detection is slot-agnostic: `media=cdrom` or a `*-cloudinit`
  PVE-assigned volume name).
- **Impact/risk**: none today (the slot is never written; Drift surfaces it
  as a non-destructive live-only anomaly, matching the data-loss guard).
  An adopted manifest is *disk-faithful* except for the PVE-owned
  cloud-init slot (documented in the gap report + here).
- **Discovery source**: live `/qemu/{id}/config` reports. Pinned:
  `TestAdopt_CloudInitOnSATAAlsoExcluded`,
  `TestAdopt_PlainDataDiskStillAdopted`.
- **Notes**: a future `spec.hardware.cloud-init.slot` escape hatch (a
  cloud-init model that targets a data bus) would close this; until then
  such PVE objects are *documented*, not re-created.

### Cloud-init: `cipassword` / `cicustom` / `ciupgrade` are not modelled

- **Resource/area**: VM cloud-init data
- **PVE field**: `cipassword`, `cicustom`, `ciupgrade`
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: ProxOps models `ciuser`, `sshkeys` (redacted),
  `nameserver`, `searchdomain`, and `ipconfig<N>` under
  `spec.cloud-init-data`. `cipassword` (a secret) and `cicustom` (custom
  user/meta/network-data references) are **not** adopted or reconciled;
  `ciupgrade` is PVE-side-only. On adopt, `cipassword`/`sshkeys` gap
  values are emitted as `<redacted>` (the field name still reports, so the
  operator knows ProxOps does not model it; the value never reaches
  stdout, logs, the gap report, or any generated manifest).
- **Impact/risk**: none (no write path; PII is redacted at the source).
- **Discovery source**: live `/qemu/{id}/config` reports. Pinned:
  `TestAdopt_ZeroWritesOnProdFixtureEquivalent` (sentinel values must not
  appear in the report; `sshkeys`/`cipassword` gap values must contain
  `<redacted>`), `TestAgent_GeneratedOutputContainsNoCredentialMaterial`.

### Clone-backed VM: undeclared non-identity fields come from the template

- **Resource/area**: VM provisioning via `spec.clone`
- **PVE field**: any `/config` key a full clone copies that the VM manifest
  does not declare (hardware layout, `machine`/`bios`/`vga`, `scsihw`,
  `boot`, `onboot`/`startup`/`protection`, `efidisk0`/`tpm0`/`serial0`,
  the cloud-init DRIVE volume)
- **Status**: deliberate
- **Priority**: low
- **What is unsupported**: a full clone inherits the template's whole
  `/config`. ProxOps overwrites the fields the VM manifest declares and
  clears the inherited **cloud-init identity** keys it does not (ciuser /
  sshkeys / ipconfig<N> / nameserver / searchdomain / cipassword /
  cicustom), but it does NOT reset other undeclared fields to a
  fresh-create default — the template's hardware layout and operational
  posture survive, which is the point of cloning. A clone-backed VM also
  cannot declare `spec.disks` (the layout is inherited), so the disk
  controller (`scsihw`) is owned by the template.
- **Impact/risk**: an operator expecting a clone to "look like" a fresh VM
  with only a few overrides may be surprised that, e.g., the template's
  `onboot` or `boot` order persists. Identity (hostname / user / keys /
  static IP) is never leaked — that is enforced and convergent. The
  inherited `scsihw` can make an image with a mismatched initramfs panic on
  boot (see the wire finding below); set the controller on the template.
- **Discovery source**: live clone probe on conformance-dev (2026-09-15).
  Pinned: `TestClone_ConfigParams`, `TestClone_DriftClearsLeakedIdentity`,
  `TestClone_DriveNotRewritten`, `TestE2ECloneProvisionLifecycle`.

### LXC: `cpu.units` is declarative-only

- **Resource/area**: LXC CPU (`spec.cpu.units`)
- **PVE field**: `cpuunits`
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: `spec.cpu.units` parses but is **never sent**
  and **not adopted**; PVE's `cpuunits` surfaces as a gap.
- **Impact/risk**: none (no write path).
- **Discovery source**: code review (`internal/schema/lxc.go` — `Units`
  has no create/drift reference).

## Deliberately not modelled (out of scope by design)

These are *deliberate* non-goals recorded so future readers know they were
considered:

- **VM replication** (`repl1`, `replN`, and the replication `schedule`):
  ProxOps does not model PVE replication. It is a PVE-side DR feature with
  its own target + schedule + failover semantics; ProxOps's ownership
  model (idempotent converge-to-git) does not map to it. Status:
  `deliberate`.
- **PVE storage resources** (creating/disabling storage backends): scope
  control keeps ProxOps off storage administration; it only reads storage
  (content listing, downloads). Status: `deliberate`.
- **PVE firewall / netfilter**: per-VM/per-LXC firewalls and the cluster
  firewall are untouched. ProxOps models the NIC `firewall=1` toggle only.
  Status: `deliberate`.
- **HA resources, pools, users, roles, SDN**: out of the GitOps model.
  Status: `deliberate`.
- **`bootspeed` / `netboot`**: rejected on PVE 9.2 `/config`; not modelled.
  Status: `deliberate`.

## Credentials / runtime limitations

- **External `sops` binary dependency (`internal/secrets`)**
  - **Status**: `deliberate`
  - **What is unsupported**: ProxOps shells out to `sops --decrypt` (age
    backend); it does NOT link the SOPS Go module. When a cluster
    references `secrets-file`, `sops` + `age` MUST be on PATH on the host
    ProxOps runs on; otherwise `ErrSOPSBinaryMissing` is surfaced at agent
    construction. No KMS or GCP/Azure/Ali/Huawei/PGP backends are
    supported — only `age`.
  - **Notes**: the Go module `github.com/getsops/sops/v3` would pull ~160
    transitive deps; the SOPS CLI is already mandatory on any host where
    SOPS-encrypted secrets are managed. Documented in `docs/sops-credentials.md`.

- **SOPS identity (age private key) lifecycle / rotation**
  - **Status**: `planned`
  - **What is unsupported**: ProxOps does NOT rotate SOPS recipients.
    Rotation is an operator workflow: add the new recipient to the SOPS
    command line, re-encrypt the file, commit. The private age key has no
    built-in "grace period / revoke" — it is whatever the operator's
    `SOPS_AGE_KEY_FILE` points at.

- **No SOPS file watching / hot reload of credentials**
  - **Status**: `planned`
  - **What is unsupported**: SOPS decryption happens once at agent
    construction. A rotated PVE token in the SOPS file is not picked up
    without a process restart. Same as env-var credentials: the operator's
    job is to `systemctl restart proxops` after rotating.
    `poll-interval` re-diffs git + PVE but does NOT re-decrypt secrets.

- **SOPS `git-token` conflict handling**
  - **Status**: `investigated`
  - **What is unsupported**: if TWO cluster SOPS files both name
    `git.token` keys that resolve to DIFFERENT values, ProxOps fails
    closed with "git token conflict" at agent construction. This is
    deliberate: the ProxOps git source is single — one worktree, one fetch
    token — and two different values would mean "clone two different git
    repos", which is out of scope. When two clusters share the same git
    token value, that value is used for all.

- **No per-cluster CA pinning via SOPS**
  - **Status**: `deliberate`
  - **What is unsupported**: `pve.ca-file` is a per-`pve`-level setting,
    shared across clusters. To pin a different CA per cluster, the operator
    must use a global config that does not reference SOPS for that
    cluster. SOPS does not (today) have a "ca-file" credential field. If
    multi-CA pinning becomes necessary, add a SOPS `pve-ca-file` entry and
    resolve it alongside the user/token fields.

## PVE 9.2 wire findings (pinned, not gaps)

These are verified behaviours ProxOps handles correctly. They are recorded
here so a future change does not silently regress them; each is pinned by a
test.

- **`/qemu/{id}/untemplate` does not exist** — PVE 9.2 returns HTTP 501
  "not implemented" (probe on conformance-dev). ProxOps must not attempt a
  kind-flip on the PVE side: the planner converts a `kind: VM` desired
  against a PVE-side `template=1` into a non-destructive anomaly. Pinned:
  `internal/pveclient/mock/mock.go` (mock 501) +
  `TestE2E_VMDesiredButPVEIsTemplateSurfacesAnomaly`.
- **`DELETE /qemu/{id}` works on a template VM** — a ProxOps-owned
  `TemplateVM` that is no longer desired IS pruned via
  `DELETE /qemu/{id}`; PVE does not reject the delete because the object is
  a template. The executor's pre-delete stop is a no-op on an always-stopped
  template.
- **`start` is a boolean form-value** — PVE 9.2 rejects `start=true`
  ("type check ('boolean') failed"); ProxOps sends `start=1`. The mock
  mirrors the grammar (1/0/yes/no/on/off, else 400). Pinned:
  `internal/pveclient/start_wire_test.go`.
- **`sshkeys` must be percent-encoded** — PVE 9.2 declares `sshkeys` a
  urlencoded string; a raw value is rejected with 400 "invalid urlencoded
  string". ProxOps percent-encodes the value (keys joined `%0A`) on write
  and compares the DECODED key SET on drift. Pinned:
  `TestCloudInitSSHKeys_WireGrammar`, `TestCloudInitSSHKeys_DriftNoFlap`.
- **cloud-init drive needs an `images`-content storage** — a drive on a
  storage whose content list lacks `images` creates fine but FAILS AT
  START ("storage 'local' does not support content-type 'images'").
  Bootable cloud-init VMs must set `hardware.cloud-init.storage` to a pool
  that carries `images`. Pinned: `TestCloudInitDrive_SizeTokenTolerance`
  (size-token tolerance) + the DiskImage e2e.
- **`import-from` requires size token `0`** —
  `local-lvm:0,import-from=local:import/x.qcow2`; any other size → 400.
  PVE derives the volume size from the image and does NOT re-report
  `import-from`, so ProxOps compares only the POOL on a live image-seeded
  disk. Pinned: `TestDiskImage_Validate`, `TestVM_DiskImage_*`,
  `TestE2E_CloudInitVMFromDiskImage`.
- **artifact filename-extension contract** — `download-url` validates the
  extension against `content`: import accepts `.qcow2`/`.vmdk`/`.raw`;
  iso accepts `.iso`/`.img`; vztmpl accepts the tar family. Anything else
  is 400 "wrong file extension". Enforced at parse time. Pinned:
  `internal/schema/artifact_ext.go`.
- **`nesting` rides the `features=` composite** — PVE 9.2 rejects
  top-level `nesting=` on create (400) and PUT (400); the accepted form is
  `features=nesting=<0|1>`. ProxOps emits the composite on both paths and
  never a top-level `nesting=`. Pinned:
  `TestLXCToCreateParams_NestingAsComposite`,
  `TestLXCDrift_NestingEmitsFeaturesNotTopLevel`.
- **LXC static `ip=`/`gw=` inside `netX`** — PVE 9.2 create + PUT accept
  the `netX=...,ip=...,gw=...` form; ProxOps owns them end-to-end. Pinned:
  the LXC wire-regression tests.
- **full clone copies the template's cloud-init DATA** —
  `POST /qemu/{src}/clone` with `full=1` reproduces `ciuser`, `sshkeys`,
  `ipconfig<N>`, `nameserver`, `searchdomain`, `onboot`, `tags`, `boot` and
  the hardware layout verbatim on the clone's `/config` (probe on
  conformance-dev 2026-09-15). It does NOT copy `name` (the clone's own
  `name=` wins) and it REGENERATES `vmgenid` + `smbios1` (UUID). ProxOps
  therefore overwrites the declared cloud-init DATA and `delete=`s the
  undeclared inherited identity keys after every clone, so a clone never
  boots with the template's hostname / user / keys / static IP. Pinned:
  `TestClone_ConfigParams`, `TestClone_DriftClearsLeakedIdentity`,
  `TestE2ECloneProvisionLifecycle`.
- **`delete=` and setting the SAME key in one `/config` request is rejected**
  — PVE 9.2 returns 400 "you can't use '-ciuser' and '-delete ciuser' at the
  same time". Deleting an ABSENT-but-known key is tolerated (200); deleting
  an UNKNOWN key is 400 "unknown option". ProxOps keeps the clone's set map
  and delete list disjoint by construction. Pinned:
  `TestClone_ConfigParams` (disjointness assertion).
- **the clone endpoint rejects `start`** — `POST /qemu/{src}/clone` with
  `start=1` is 400 "property is not defined in schema". Power is a separate
  `status/start`, so a `state: started` clone-backed VM is powered on by the
  planner's normal power step on the following cycle. Pinned:
  `TestE2ECloneProvisionLifecycle`.
- **a clone of a missing source fails synchronously** — `POST
  /qemu/{src}/clone` for an absent source returns HTTP 500 "unable to find
  configuration file for VM N on node 'X'" with NO UPID; a clone onto an
  existing id returns 500 "unable to create VM N: config file already
  exists". ProxOps surfaces either as a failed action, never as convergence.
  Pinned: `internal/pveclient/mock/mock.go` (both shapes) +
  `TestE2ECloneMissingTemplateFailsClosed`.
- **re-sending the cloud-init DRIVE over a clone's live volume fails the
  task** — the clone inherits its own `<pool>:vm-<id>-cloudinit` volume; a
  `/config` write restating `ide2=<pool>:cloudinit` makes PVE `lvcreate` a
  volume that already exists and the task FAILS ("Logical Volume
  vm-<id>-cloudinit already exists", probe 2026-09-15). ProxOps writes the
  drive only into an EMPTY slot. The same lvcreate rule applies to any
  create-form volume written over a live one — the general data-loss guard.
  Pinned: `TestClone_DriveNotRewritten`, `TestClone_DrivePoolMismatchIsAnomaly`
  + the mock's lvcreate mirror.
- **cloud images may lack PVE's default SCSI driver** — the Ubuntu *minimal*
  cloud image's initramfs has no `lsi53c897a` (PVE's default `scsihw`), so a
  VM using it kernel-panics ("VFS: Unable to mount root fs on
  unknown-block(0,0)") unless `scsihw=virtio-scsi-single`. Verified on
  conformance-dev with PLAIN (non-clone) VMs, so it is an image property,
  not a clone defect. For a clone-backed VM the controller is owned by the
  TEMPLATE (a clone inherits `scsihw` and declares no `spec.disks`), so set
  `spec.disks[].controller: virtio-scsi-single` on the TemplateVM. Pinned:
  `TestE2ECloneProvisionLifecycle` (template carries the controller).

### M13 wire findings (Secure Boot, drive options, LXC mounts, TemplateCT)

- **Secure Boot is `pre-enrolled-keys`, NOT a `/security` endpoint** —
  PVE 9.2.2 has **no** `/nodes/{n}/qemu/{id}/security` endpoint (HTTP 501 on
  GET/PUT/POST) and rejects a `secure-boot=` token (400, both inline on
  `efidisk0` and top-level). The real wire form is the `pre-enrolled-keys=
  <0|1>` token on `efidisk0`: `enabled` → `=1`, `disabled` → `=0`. The
  toggle converges in place via the live drive form on stopped AND running
  VMs (key enrollment takes effect at the guest's next boot); an empty slot
  takes the create form. PVE auto-adds an `ms-cert=<2011|2023|2023k|2023w>`
  token to the report when keys are enrolled at create — ProxOps treats
  `ms-cert` as PVE-owned (preserved verbatim on live-form rewrites, never
  compared; a live-form write WITHOUT it drops it from the report). Pinned:
  `TestM13_SecureBoot_CreateWire`, `TestM13_SecureBoot_Drift`,
  `TestM13_SecureBoot_Adopt`.
- **drive options are bus-specific** — `discard=<ignore|on>` and
  `aio=<native|threads|io_uring>` are accepted inline on every bus;
  `ssd=<0|1>` is accepted on **scsi/sata/ide only** — PVE 9.2 rejects
  `ssd=` on virtio/nvme (400 "property is not defined in schema"), so
  ProxOps fails closed at `Validate`. `mbcache` is **not** in PVE 9's schema
  (400) and is not modelled. All three converge in place via the live drive
  form (volume id preserved). Pinned: `TestM13_DiskOptions_CreateWire`,
  `TestM13_DiskOptions_SSD_BusValidation`, `TestM13_DiskOptions_Drift`.
- **allocated `mpN` volumes share the rootfs disk counter** — PVE allocates
  `mp0` as `vm-<ctid>-disk-<n>` (the same counter as rootfs `disk-0`), and
  the live form (`mpN=<volid>,size=…,mp=…,ro=…`) toggles the guest path and
  option tokens in place. A create-form write over a LIVE mp volume
  RE-CREATES it (new volid) — the same data-loss guard as VM disks. Pinned:
  `TestM13_LXCMount_Drift`, `TestM13_LXCMount_OptionsAdopt`.
- **bind-mount writes are root@pam-only** — see the open gap above; the
  403 is PVE-enforced, ProxOps fails closed. Pinned:
  `TestM13_BindMount_CreateWire`, `TestM13_BindMount_Drift`.
- **`POST /lxc/{id}/template` is synchronous (NULL data)** — unlike the qemu
  mark (which returns a task UPID), the LXC mark returns `{"data":null}` with
  no UPID (probe-verified PVE 9.2.2). The executor treats the empty UPID as
  immediate success. Promotion renames the rootfs volume
  `vm-<ctid>-disk-0` → `base-<ctid>-disk-0`. `/lxc/{id}/untemplate` is 501
  (no demotion), same as the qemu side. Pinned:
  `TestE2ETemplateCTCreateMarksAndIsIdempotent`,
  `TestE2E_LXCDesiredButPVEIsTemplateCTSurfacesAnomaly`.
