# SOPS & credentials

ProxOps's credential model in one sentence: **encrypted secrets live in the
repository, the private identity lives outside it, and every decrypted value
lives in process memory only.**

## The reference shape

`clusters/<cluster>/` carries this cluster's ProxOps configuration **and**
its encrypted credentials — the cluster-local, self-contained shape the
repository-first discovery path reads:

```
clusters/<cluster>/
  config.yaml               # PVE endpoint, node allowlist, secrets-file ref
  secrets.sops.yaml         # SOPS/age-encrypted PVE + git creds (committed)
  resources.yaml            # resource composition
```

**Why SOPS + age?** Mozilla SOPS with the `age` backend encrypts each
scalar individually and records the public age recipient in the file's
`sops:` metadata — the *public* key is safe to commit, while the *private*
key stays out of the repository. ProxOps shells out to the `sops`
executable (age backend) instead of linking the SOPS Go module: the module
pulls ~160 transitive dependencies (multi-cloud KMS, gRPC, vendor-SDK trees)
into a standalone single-binary tool. The `sops` binary the operator already
has for managing secrets is the deliberate, justified choice. A run that
declares **no** `secrets-file` never invokes `sops` at all.

**age key handling.** The private age key MUST live outside the ProxOps
GitOps repository. ProxOps spawns `sops --decrypt` inheriting its own
environment, so the operator supplies the key via the standard SOPS age
identity mechanism:

```sh
export SOPS_AGE_KEY_FILE=$HOME/.local/share/proxops/<cluster>.age
# SOPS_AGE_KEY / AGE_KEY_FILE are honoured by sops' age backend as well
```

Generate a key (development):

```sh
age-keygen -o ~/.local/share/proxops/conformance-dev.age
# the private key now lives ONLY in that file. Never commit it, never echo it.
```

The encrypted repository may contain the **public** age recipient — inside
`secrets.sops.yaml`'s `sops:` metadata. That is how additional operators are
granted access without re-encrypting. ProxOps never reads, writes, or manages
the private key itself; it only passes the operator's environment to `sops`.

**The security invariants that hold regardless of configuration:**

- PVE & git credentials are OUT of the plaintext manifests — they ride in a
  SOPS/age-encrypted `clusters/<cluster>/secrets.sops.yaml`.
- The public age recipient is in the SOPS file's `sops:` metadata and is
  safe to commit; the **private** age key MUST live outside the repository
  (standard SOPS age identity mechanism: `SOPS_AGE_KEY_FILE` /
  `SOPS_AGE_KEY` / `AGE_KEY_FILE` in the process environment).
- Decryption happens ONCE at agent construction, into an in-memory
  `SopsResolved` map tagged `json:"-" yaml:"-"`: it never round-trips
  through YAML/JSON status surfaces.
- Decrypted values appear in **none** of `diff`/`apply`/`status`/`run`
  stdout, the `/status` JSON, `/metrics`, log lines, error messages, or
  generated manifests.
- Missing, undecryptable, empty or mismatched credentials fail closed
  before any PVE call.

## Creating / updating a SOPS secret file

The plaintext must exist only in your editor and this shell session — never
in the work tree:

```sh
AGE_KEY=~/.local/share/proxops/conformance-dev.age
PUB=$(grep -o 'age1[a-z0-9]*' "$AGE_KEY" | head -1)   # the public recipient
# 1) write PLAINTEXT to a scratch file OUTSIDE the git worktree:
cat > /tmp/conformance-dev-secrets-plain.yaml <<'EOF'
secrets:
  proxops-user: root@pam
  proxops-token-id: proxops
  proxops-token: <PASTE PVE token uuid>
  proxops-password: ""
  proxops-git-token: <PASTE git fetch token>

# M13.2 — structured Cloud-Init material (optional blocks):
cloud-init:
  ssh-keys:            # name -> one OpenSSH public-key line
    main: "ssh-ed25519 AAAAC3... ops@host"  # referenced as cloud-init.ssh-keys.main
    github: "ssh-ed25519 BBBB... github@host"
  passwords:           # name -> PVE cipassword plaintext
    default: "<paste password>"            # referenced as cloud-init.passwords.default
EOF
# 2) encrypt against the public recipient (age backend only):
sops --encrypt --age "$PUB" --input-type yaml --output-type yaml \
  /tmp/conformance-dev-secrets-plain.yaml > \
  clusters/conformance-dev/secrets.sops.yaml
# 3) shred the plaintext and verify no cleartext leaked:
shred -u /tmp/conformance-dev-secrets-plain.yaml
grep -Rn "<PASTE" clusters/ && echo "LEAK: plaintext still in worktree"
git add clusters/conformance-dev/secrets.sops.yaml && git commit
```

For a production cluster / multi-operator: add each operator's public age
key to the recipient list (`sops --encrypt --age "PUB1,PUB2"`). Rotating an
operator = drop their key from the list, re-encrypt, commit. ProxOps has no
auto-rotation: re-encryption is the operator's step.

## Running ProxOps (repository-first)

From the repository root, no `--config` is required — ProxOps discovers the
repository and reads the optional `proxops.yaml` + every
`clusters/<name>/config.yaml`:

```sh
cd <gitops repo>
export SOPS_AGE_KEY_FILE=~/.local/share/proxops/conformance-dev.age   # outside the repo
proxops diff                          # read-only plan; SOPS decrypts in memory
proxops apply                         # converge
proxops status                        # convergence table
proxops run                           # daemon
proxops adopt --cluster conformance-dev   # PVE -> YAML, read-only
```

Advanced (pre-M13.1) forms still work for deployments that prefer a single
hand-edited config: `proxops diff --config clusters/<name>/config.yaml`
loads that cluster under the discovered repository; a URL-mode process
config (`git.url`) loads as-is without repository discovery; an explicit
`--git-path <path>` / `PROXOPS_GIT_PATH` pins the work tree for automation
and tests.

## Precedence

Per cluster, highest first:

```
SOPS-decrypted value referenced by pve.clusters.<c>.secrets   (explicit cluster secret)
  > PROXOPS_PVE_* / PROXOPS_GIT_TOKEN environment vars        (bootstrap)
  > global pve.* fields in the process-wide proxops.yaml      (bootstrap)
```

When a cluster's `secrets-file` is configured, ProxOps requires **every**
field referenced under that cluster's `secrets:` block to be present and
non-empty in the decrypted document — it does **not** silently fall back to
env/YAML for that field (fail closed; an empty/missing SOPS secret cannot
result in an unintended credential being used). For `pve.auth: token`, a
SOPS cluster additionally suppresses `pve.token-value` for that cluster's
PVE params: a global pre-composed `PROXOPS_PVE_TOKEN_VALUE` must never
shadow a cluster's SOPS reference. Clusters with no `secrets-file` keep the
plain env/YAML behaviour exactly.

## M13.2 — Cloud-Init SOPS material

Since M13.2 the SOPS document carries a **structured** `cloud-init` block
in addition to the flat `secrets:` credential mappings:

```
cloud-init:
  ssh-keys:       # name -> one OpenSSH public-key line
    main: "ssh-ed25519 AAAAC3... ops@host"
  passwords:      # name -> PVE cipassword plaintext
    default: "<paste password>"
```

Key names are operator-defined ([a-z0-9], up to 64 chars, no leading `-`).
VMs/TemplateVMs reference this material — never carry it inline — via
`spec.cloud-init-data.ssh-key-refs: [cloud-init.ssh-keys.<name>, ...]` (plural)
and `spec.cloud-init-data.ci-password-ref: cloud-init.passwords.<name>`
(singular). There is **no plaintext `ci-password` field** in the
ProxOps resource schema: the PVE `cipassword` value can only ever enter
the wire via a SOPS ref. See [Cloud-Init](cloudinit.md) for the full
semantics, mutual-exclusion rules, and Drift behaviour (PVE 9.2 masks
`cipassword` as `**********` on read-back; ProxOps only writes it when the
live value is ABSENT and never deletes it).

`proxops adopt` will, for a SOPS-backed cluster, replace the M10
`ssh-keys: ["*"]` adoption sentinel with `ssh-key-refs` when the live PVE
`sshkeys` is present verbatim in the SOPS doc — the committed manifest
then names the ref, not the key.

### `proxops adopt --adopt-secrets` (importing PVE-recoverable keys)

```sh
proxops adopt --cluster conformance-dev --adopt-secrets
```

Imports **PVE-recoverable** cloud-init material into the cluster's SOPS
file — currently SSH public keys: live keys the SOPS doc already carries
as `cloud-init.ssh-keys.<name>` (the manifest adopts that ref; name reused
as-is) plus live keys with **no** SOPS match, which receive a
deterministic `cloud-init.ssh-keys.adopted-<fingerprint>` name. The
fingerprint is the first 8 bytes of
`sha256("sshpki\u0000<type>\u0000<blob>")` in lowercase hex (16 chars) — the
OpenSSH comment column is deliberately EXCLUDED so the same key with a
different comment still dedupes to one SOPS entry.

Passwords are **never** auto-imported: PVE 9.2's read-back mask
(`**********`) is unrecoverable, so `adopt` only records a *census count*
(`PwResources`) and the operator maps them manually:
write a `cloud-init.passwords.<name>` value into the SOPS doc and point
the VM's `ci-password-ref` at it.

**Safety invariants of the import (all pinned by tests)**:

- **Plain `adopt` never writes the SOPS file.** Only `--adopt-secrets`
  does, and only when it actually found new material. A plain adopt run
  is byte-for-byte no-op on `secrets.sops.yaml`.
- **Atomic write.** The merge re-encrypts to a `<file>.proxops-tmp`
  sidecar and `rename(2)`s it into place; no plaintext is EVER written to
  disk. On encrypt failure the original file is untouched (the sidecar is
  unlinked).
- **Recipients preserved.** The age recipient list of the pre-existing
  encrypted file is read from its `sops:` metadata and re-used. Dropping
  one is an operator-lockout; the merge fails closed if no recipients
  are found.
- **No clobbering.** A merge with `Result.NewSSHKeys` whose name
  ALREADY exists in the SOPS doc does NOT overwrite the operator's value
  (no-op, summary says so). Unrelated blocks (`secrets:`,
  `cloud-init.passwords:`) survive byte-for-byte.
- **No new names → no write.** If every live key was already SOPS-backed,
  the cluster's SOPS file is not re-encrypted (git stays quiet).

The adopted manifests emitted in the SAME run reference the new
`cloud-init.ssh-keys.adopted-<fingerprint>` names, so the operator commits
the SOPS file **and** the new manifests together — the cycle after
commit is already resolvable (no fail-closed ref gap).

## Fail-closed guarantees

- Decryption happens **in memory only**: `sops --decrypt` stdout → parsed
  into an in-memory `SopsResolved` map → applied to PVE auth + git-fetch
  headers. ProxOps writes no decrypted file to disk, ever.
- Decrypted values appear in **none** of the normal output surfaces (see
  the invariants above).
- Unencrypted `secrets.sops.yaml` (no `sops:` metadata) → refused
  **before any PVE call** (`ErrUnencryptedSecrets`).
- Malformed SOPS document → `ErrMalformedDocument`.
- Missing age identity → `ErrNoIdentity`; wrong age identity →
  `ErrIdentityMismatch`.
- `sops` binary missing from PATH → `ErrSOPSBinaryMissing`.
- A SOPS-referenced key empty in the decoded document → fails closed with
  an error that names the missing reference, never the value.
- M13.2 cloud-init refs: a `spec.cloud-init-data.ssh-key-refs` / `ci-password-ref`
  entry that does not resolve against the cluster's `cloud-init` SOPS
  document fails closed BEFORE any PVE call, with an error that names the
  REF — never the resolved value (an empty SOPS ref is never
  interpreted as "no keys").
- M13.2 `adopt --adopt-secrets` SOPS merge errors surface as
  `adopt: SOPS merge failed …` and leave the on-disk SOPS file untouched
  (atomicity via the sidecar + rename, verified by test).
- The private age key file MUST live outside the git worktree.
- The `sops` binary is only located/inherited when a cluster actually names
  a `secrets-file` (a plain env-credential deployment never invokes sops).

## Limitations

- No automatic key or secret rotation: re-encryption is the operator's
  step on key change (a recipient can be added and the SOPS file
  re-encrypted without rotating the secret values, as long as the values
  haven't changed).
- One SOPS file per cluster: `secrets-file` resolves relative to the
  cluster's own config directory. Two clusters may point at the same file
  if they genuinely share secrets; ProxOps decrypts each reference exactly
  once per cluster.
- The SOPS document is only read at agent startup; ProxOps does not watch
  the file for changes (re-reconcile = restart the `proxops` daemon or
  invoke a new one-shot). The same applies to env credentials.
- SOPS supports KMS/PGP/GCP/Azure/Huawei/Ali backends; ProxOps uses `age`
  only. The operator is responsible for keeping the age key file available
  before the ProxOps process starts.
- No in-band audit log of SOPS decryption events. A ProxOps log line will
  say "resolve SOPS for cluster X" — that's all.

## Compatibility

Configurations with no `secrets-file` behave exactly like a plain
env-credential deployment (env-over-YAML-over-defaults). ProxOps does not
even locate the sops binary during config load or validation — only during
SOPS resolution at agent construction, and only for SOPS-referenced
clusters.
