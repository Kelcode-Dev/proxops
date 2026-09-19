# Adoption (PVE → YAML)

`proxops adopt` reverse-engineers live
PVE objects into ProxOps YAML. It is READ-ONLY with respect to PVE —
it performs only GET requests and asserts zero PVE writes at the end of
the run — and requires an explicit cluster:

```sh
cd <gitops repo>
export SOPS_AGE_KEY_FILE=~/.local/share/proxops/<cluster>.age   # outside the repo
proxops adopt --cluster conformance-dev
```

(Pre-M13.1 invocation still works: `proxops adopt --cluster <name> --config clusters/<name>/config.yaml`.)

What it does:

- uses that cluster's configured endpoint + node allowlist (or, without an
  allowlist, PVE's `/cluster/nodes` listing); only allowlisted nodes are
  ever read — this is the cluster-isolation guarantee;
- writes one manifest per live object under `<kind>/<cluster>/` in the git
  work tree (VM, LXC, ISO, CTTemplate, TemplateVM, TemplateCT); `import`
  content (DiskImage) is not adopted — see [Compatibility / Gaps](GAPS.md);
- surfaces **unsupported PVE configuration explicitly** (a `gap` line per
  live key ProxOps does not model; `INCOMPLETE` for generated manifests
  missing a value PVE cannot re-report, e.g. the LXC `ostemplate`). PVE
  *template* VMs (`template=1` on a `type=qm` object) are adopted as
  `kind: TemplateVM` manifests under `templatevm/<cluster>/`, and PVE
  *template* containers (`template=1` on a `type=lxc` object) as
  `kind: TemplateCT` manifests under `templatect/<cluster>/`, both with the
  full lifecycle owned (create + mark, config drift, prune). PVE-side
  `sshkeys` in the template's cloud-init are redacted to the `["*"]`
  sentinel so the operator fills in the real key(s) before apply;
  `cipassword` / `cicustom` stay as gap lines. A ProxOps `kind: VM` (or
  `kind: LXC`) desired against a live PVE-side template at the same
  `(node, vmid)` is surfaced as a non-destructive anomaly (no kind-flip
  write).
- **redacts sensitive PVE fields** in the gap report: `sshkeys` and
  `cipassword` values are emitted as `<redacted>` (the field name still
  reports, so the operator knows ProxOps does not model it);
- prints the exact `resources.yaml` lines to add. It does NOT modify
  `clusters/<cluster>/resources.yaml` — listing the generated files is a
  deliberate, reviewable operator step.

## M13.2 — cloud-init secrets (SSH keys + cipassword census + `--adopt-secrets`)

Adoption of cloud-init secret material is governed by these rules:

### `sshkeys` (recoverable public keys)
- **SOPS-backed cluster + exact match**: if the cluster's
  `secrets.sops.yaml` carries a `cloud-init.ssh-keys.<name>` entry whose
  value is the live PVE `sshkeys` line **verbatim**, the generated
  manifest uses `ssh-key-refs: [cloud-init.ssh-keys.<name>]` (the ref,
  never the key). The SOPS name is the **existing** operator-defined name
  (reused, not re-named).
- **SOPS-backed cluster + no match**: the manifest keeps the M10
  `ssh-keys: ["*"]` sentinel; a `sshkeys` census line is emitted
  (`adopt --adopt-secrets` would import the key under a new
  deterministic name — see below).
- **Non-SOPS cluster**: the manifest keeps the `["*"]` sentinel; the PII
  is redacted in the gap report.
- **All-or-nothing partial match (fail-closed)**: if the live `sshkeys`
  field has TWO or more keys, ONE is in SOPS and ONE is not, the manifest
  uses the `["*"]` sentinel (not a partial `ssh-key-refs` list) — a
  mixed manifest would reject `Validate()` and the operator's intent is
  ambiguous. This matches the PVE wire (one urlencoded multi-line value,
  not one key per ref).

### `cipassword` (masked by PVE; unrecoverable)
- PVE reports `cipassword` as `**********` whether or not the live value
  is set (probe-verified PVE 9.2.2: presence ≠ value; read-back does not
  disclose the plaintext). `adopt` **cannot** reverse-translate a
  `cipassword` into a SOPS ref: the material is irrecoverable.
- The adopt census reports how many VMs carry a live `cipassword` value
  (PVI: operators should manually map each such VM to a
  `ci-password-ref` + SOPS entry after review). `cipassword` itself
  stays a `<redacted>` gap.

### `proxops adopt --adopt-secrets` (the flag)
- **Adds** live `sshkeys` that are NOT already in the cluster's
  `cloud-init.ssh-keys` SOPS block to that block, under a **deterministic**
  new name `adopted-<digest>` where `<digest>` is 16 lowercase hex chars:
  the first 8 bytes of `sha256("sshpki\u0000<type>\u0000<blob>")` (the
  key's OpenSSH type + base64 body; the comment column is EXCLUDED — so
  the same key under different labels dedupes to one SOPS entry, and
  rename-safe SOPS merges are stable). The manifest then references it as
  `cloud-init.ssh-keys.adopted-<digest>`.
  PVE reports the live `sshkeys` percent-encoded (PVE 9.2's own encoding:
  space as `%20`, newline as `%0A`); adopt decodes it to plain key lines
  before matching against the SOPS doc (plain lines), so the SOPS entry
  value is a **plain** OpenSSH public-key line, never the PVE-encoded form.
- **Re-encrypts** the SOPS file **atomically** and **in place**: a sidecar
  `<file>.proxops-tmp` is written, `sops --encrypt` (age backend,
  recipients read back from the on-disk SOPS metadata so no operator is
  locked out), and `rename(2)` swaps it into place. All existing SOPS
  blocks (M9 `secrets:` flat map + any other `cloud-init.` material) are
  preserved byte-for-byte.
- **Fails closed** on any SOPS write failure: the original `.sops.yaml`
  is never truncated or left half-written (the rename is atomic). The
  operator can re-run `--adopt-secrets` idempotently — a key that is
  already in the SOPS doc is NOT re-added.
- The adopt run with `--adopt-secrets` still emits **zero PVE writes**
  (the census is read-only); the SOPS file is the only thing written, and
  only when there is something to import.
- **Plain `adopt`** (no flag) NEVER writes the SOPS file: the SOPS
  doc + manifest + git commit remain the operator's reviewable step.

### PII guarantees
No PVE sshkeys or cipassword material (plaintext) is ever written to the
gap report, the `INCOMPLETE` / `SKIPPED` census, manifest YAML (except the
SOPS-matched `ssh-key-refs` dot-paths), or the CLI stdout. The SOPS
merge step only writes the encrypted `sops.sops.yaml`; the private age
key is never logged. `Result.NewSSHKeys` (in-memory map of SOPS name →
plaintext key) is ONLY handed to the encrypt step; it is not
serialised anywhere else.

## Determinism

Two `adopt` runs against an unchanged PVE produce **byte-identical**
manifests and gap reports: the manifest set, filenames, field ordering,
gap ordering, and the INCOMPLETE/SKIPPED lists are all total-ordered. No
timestamps, no PVE-assigned randomness, no credentials appear in the
output. A second run therefore produces no meaningless git diff.

## Production safety expectations
When the adopted cluster is a production PVE:

- `adopt` is the **only** proxops command safe to run against it
  unattended: it issues GETs to `/cluster/nodes`,
  `/nodes/{n}/{qemu,lxc}`, `/nodes/{n}/storage`,
  `/nodes/{n}/storage/{s}/content`, `/nodes/{n}/qemu/{v}/config`,
  `/nodes/{n}/lxc/{c}/config` — and nothing else. Post-run, the client's
  write counter must read 0 or the run aborts.
- **Do NOT run `apply` / `run` / a normal reconcile cycle against a
  freshly adopted production cluster.** Adoption output is reviewed,
  completed (INCOMPLETE resources), and composed into
  `resources.yaml` by a human first; only then is `diff` used to verify
  zero unexpected drift.
- The generated manifests are stripped of ProxOps's ownership tag from
  `spec.tags` (adopt never invents tags); the tag is re-appended at create
  time, and on the first reconcile ProxOps claims the live object by
  adding that tag. Untagged live objects are never modified or deleted.

## Round-trip verification

The acceptance round-trip is:

```
PVE -> adopt -> YAML -> (human review) -> clusters/<cluster>/resources.yaml
     -> proxops diff -> zero unexpected drift
```

Every remaining drift line must map to a documented expectation:

- `update ... config drift` on every adopted VM / LXC / TemplateVM /
  TemplateCT: the ownership-tag claim
  (PVE objects carry no `proxops` tag; ProxOps adds one when it
  manages an object). This is expected and is the first write the operator
  consciously approves — it is not applied by `diff`.
- `anomaly ... live-only disk slot scsiN=...-cloudinit,media=cdrom` on VMs
  whose cloud-init volume sits on a non-IDE slot (PVE 9.x places cloud-init
  on `scsi1` when `ide2` is not used): ProxOps does not own non-IDE
  cdrom slots; adopt documents them as a gap and leaves them PVE-managed.
- `skipped (no proxops tag)` for LXC resources whose `spec.template`
  could not be recovered (INCOMPLETE). PVE-side `template=1` objects are
  adopted as `kind: TemplateVM` under `templatevm/<cluster>/` (qemu) or
  `kind: TemplateCT` under `templatect/<cluster>/` (container).

Live-only data disks are preserved: adopt interrogates the PVE /config
report, so a live second data disk is represented in the adopted manifest
rather than silently dropped. PVE cloud-init volumes on non-IDE slots, by
contrast, are PVE-owned and are explicitly reported. See [Compatibility / Gaps](GAPS.md) for
the gap backlog (the source of new entries is exactly this adopt report).
