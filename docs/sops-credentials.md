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
