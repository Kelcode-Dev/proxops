# Getting started

ProxOps is repository-first: the ProxOps GitOps repository **is** the
configuration. This page takes you from a fresh clone to your first
`diff` and `apply`.

## 1. Install ProxOps

```sh
git clone <this repository> && cd <repo>
make build          # -> bin/proxops
```

Alternatively, install the latest main-branch build directly (Go is
required; ProxOps is a single static-friendly binary):

```sh
go install github.com/GizzmoShifu/proxmox-operator/cmd/proxops@main
```

**Current limitation (pre-1.0):** no versioned release binaries or tags are
published yet, so `go install` at a semantic version (e.g. `@v0.1.0`) will
fail until the first clean pre-release tag is cut. Build from source.

**Installing on a PVE host** (the canonical target — it has no Go toolchain
and ProxOps runs there as the agent):

```sh
make cross   # -> dist/proxops-linux-amd64, a static binary
# copy it onto each PVE node, e.g.:
#   scp dist/proxops-linux-amd64 root@pve-node:/usr/local/bin/proxops
#   chmod +x /usr/local/bin/proxops
```

SOPS-backed credentials also require the `sops` binary on PATH (age
backend; see [SOPS & credentials](sops-credentials.md)).

## 2. Create your ProxOps GitOps repository

Start from the template that ships with the ProxOps source
(`examples/`):

```sh
mkdir my-gitops && cd my-gitops
cp -r /path/to/proxops/examples/* .
git init -b main && git add -A && git commit -m "initial proxops gitops repo"
```

The template is a complete repository skeleton with one cluster
(`example`) and resources of every kind — see
[Repository layout](repo-layout.md). Edit it:

```
proxops.yaml                   # optional process-wide config
clusters/example/config.yaml   # <-- point this at YOUR PVE cluster
clusters/example/secrets.sops.yaml   # <-- re-encrypt with YOUR secrets
clusters/example/resources.yaml      # (already composes the example resources)
vm/ lxc/ iso/ ctt/ templatevm/ templatect/ diskimage/
```

`clusters/example/config.yaml` must declare exactly one
`pve.clusters` entry named after its directory:

```yaml
pve:
  clusters:
    example:
      base-url: https://pve.example:8006   # your PVE endpoint (one host)
      nodes: [pve01, pve02]                # your cluster's allowlist
      secrets-file: secrets.sops.yaml
      secrets:
        pve:
          user: proxops-user
          token-id: proxops-token-id
          token: proxops-token
```

## 3. Credentials (SOPS, the recommended path)

Generate a private age key **outside** the repository:

```sh
age-keygen -o ~/.local/share/proxops/example.age   # private key, 0600-like
```

Encrypt the cluster's SOPS file against its public recipient (never
commit the private key):

```sh
PUB=$(grep 'public key:' ~/.local/share/proxops/example.age | awk '{print $3}')
sops -e --input-type yaml --output-type yaml --age "$PUB" \
  /tmp/example-secrets-plain.yaml > clusters/example/secrets.sops.yaml
shred -u /tmp/example-secrets-plain.yaml   # plaintext never survives
```

The plaintext shape (see [SOPS & credentials](sops-credentials.md)):

```yaml
secrets:
  proxops-user: root@pam
  proxops-token-id: proxops
  proxops-token: <the PVE API token value>
```

**Simpler for a dev cluster:** skip SOPS entirely — delete the
`secrets-file`/`secrets` block from `config.yaml` and supply
`PROXOPS_PVE_USER` + `PROXOPS_PVE_TOKEN` in your shell environment.

## 4. First `diff`

```sh
cd my-gitops
export SOPS_AGE_KEY_FILE=~/.local/share/proxops/example.age   # only for SOPS
proxops diff
```

From inside the repository, ProxOps discovers it (no `--config`, no work
tree path), loads `proxops.yaml` + `clusters/example/config.yaml`,
decrypts the SOPS credentials into memory, and renders exactly what it
would change on PVE — including would-be deletes — without writing
anything.

## 5. First `apply`

```sh
proxops apply --dry-run    # same plan through the full pipeline
proxops apply              # converge
proxops status             # verify convergence
```

A second `diff`/`apply` on a converged cluster must be zero actions
(idempotency is a design requirement, not an aspiration).

## 6. Run it continuously

```sh
proxops run            # watch mode: poll the work tree, reconcile on change
```

## Where things go wrong

| Symptom | First check |
|---|---|
| `no ProxOps GitOps repository found` | `pwd` must be inside the repository (or pass `--git-path <path>` / `PROXOPS_GIT_PATH=<path>`) |
| `… declares … which does not match its directory name` | `clusters/<dir>/config.yaml` must name `pve.clusters.<dir>` exactly |
| `… no effective PVE user after credential resolution` | SOPS file / age identity / reference block — see [SOPS & credentials](sops-credentials.md#fail-closed) |
| `cycle aborted` at PVE read | endpoint unreachable or token/allowlist wrong; check `logs` and `clusters/<c>/config.yaml` |
| `legacy layout: <file> sits directly under the <kind>/ kind root` | move the manifest to `<kind>/base/` or `<kind>/<cluster>/` and list it in `clusters/<cluster>/resources.yaml` |
