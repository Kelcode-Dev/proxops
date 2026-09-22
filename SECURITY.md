# Security Policy

ProxOps automates writes to Proxmox VE clusters. A ProxOps bug is therefore
an infrastructure-control bug, not just a code bug. Please treat
vulnerability reports accordingly.

## Supported versions

ProxOps is **pre-1.0**. We currently support only the latest `main` build
(the most recent commit / tag). Older commits are not supported and will not
receive security patches.

## Reporting a vulnerability

**Do not open a public issue for a security problem**, and never include
real credentials, tokens, or private-key material in an issue, commit, or
fork.

Please report security issues privately:

- **GitHub Private Vulnerability Reporting (PVR)**: see
  `Settings → Security → Private vulnerability reporting` in the
  repository. Enable / request it as the owner. Reports submitted there do
  not become public issues and reach maintainers with the repository's
  default security contacts.
- <!-- OWNER DECISION: add one concrete private reporting channel here, e.g.
     - **Email**: <security-contact@example.org>
     or a PGP-signed contact. Do NOT invent a contact that does not exist
     publicly in the project. -->

If PVR (or the alternate channel marked in the comment above) is not yet
enabled, an issue tagged `security` will still be triaged, but it **will be
made public per GitHub's normal process** — so the PVR path is preferred for
anything involving an actually exploitable write-path bug, a credential
leak, or a destructive-prune race.

## What is in scope

- **Reconciliation safety**: an unowned PVE object deleted or mutated
  because of a planner / prune bug; an unbounded or wrong-scope write
  reaching a resource that ProxOps does not own; the ownership-tag guard
  failing to classify a PVE object; a cluster-isolation failure (one
  cluster's reconcile touching another's endpoint / node).
- **Secret leakage**: a decrypted SOPS value, PVE API-token value, or
  private-key material appearing in `diff` / `status` / `apply` output, in
  logs, in an error message, in a generated manifest, in `adopt` output,
  in `--version` / `--help` / panic text, or on disk in a plaintext form
  other than the SOPS file itself (the encrypted document is expected to
  be committed).
- **Fail-closed bypass**: any code path that converts "unknown / missing /
  undecryptable secret" into "proceed with a value we did not verify".
- **PVE wire-grammar regression**: a change that causes a `POST` / `PUT`
  to PVE with malformed `delete=` / `import-from` / `start=` / `sshkeys=`
  values, particularly anything that could silently write the PVE 9.2
  `cipassword` masked sentinel back to PVE (documented in
  [docs/GAPS.md](docs/GAPS.md) under the M13.2 findings).

## What is out of scope

- **Lack of an authentication layer between user and PVE.** ProxOps assumes
  the host it runs on is already trusted and that the credentials it is
  configured with (SOPS-encrypted in the repository, or supplied via the
  environment) are the ones it should use. ProxOps does not authenticate the
  local user.
- **The PVE server's own security posture.**
- **The age private key used to decrypt SOPS files.** That key lives
  outside the repository on operator-controlled hosts; a loss of that key
  is an incident on the operator's host, not a ProxOps vulnerability.

## Disclosure timeline

Because ProxOps is pre-1.0 with a small maintainer base, we will:
- acknowledge a report within ~5 working days,
- confirm whether it falls inside scope within ~10 working days,
- aim to publish a fix or an interim mitigation for an in-scope report within
  ~30 days (or sooner, for anything that actively causes writes to PVE).

Public disclosure only happens after the maintainer(s) have confirmed
mitigation or fix. If you cannot wait that long, coordinate with us on the
private channel before disclosing.

## If you find a leaked credential in a public issue / commit / fork

- Do **not** reply publicly with the credential or with the value it
  decrypts to.
- Report it via PVR (preferred) with the commit/issue link, so the
  maintainers can rotate and note it privately.
- The affected secret should be considered compromised even if later
  deleted: rotate it (PVE API token, git token, age key) and remove any
  downstream material that was using it.
