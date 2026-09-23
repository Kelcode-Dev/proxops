---
applyTo: "**/*"
---

# ProxOps implementation rules

Read `AGENTS.md` and the relevant project documentation before changing
behaviour.

## General implementation

- Follow existing package boundaries, naming, typing, and error-handling
  patterns.
- Prefer small, explicit changes over broad refactors.
- Reuse existing config, schema, PVE client, planner, executor, adoption, and
  secret-handling abstractions.
- Keep exported APIs minimal and intentional.
- Add regression tests for changed behaviour.
- Do not weaken safety checks, ownership checks, validation, or fail-closed
  behaviour merely to make a test pass.
- Avoid `nolint` directives unless an exception is genuinely unavoidable and
  documented.

For Go changes, run the applicable quality gates:

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./...`
- `golangci-lint run`

## Documentation contract

Treat project documentation as an engineering contract.

Use:

- `docs/ARCHITECTURE.md` for architecture and data flow
- `docs/SCHEMA.md` for the resource overview and cross-cutting guarantees
- `docs/ref-vm.md`, `docs/ref-lxc.md`, `docs/reference-artifacts.md`, and
  `docs/cloudinit.md` for kind-level schema detail
- `docs/repo-layout.md` and `docs/reference-config.md` for repository/config
  behaviour
- `docs/OPERATIONS.md` for runtime/operator behaviour
- `docs/GAPS.md` for known compatibility limitations

When behaviour changes, update the authoritative document rather than
duplicating the same detail elsewhere. Distinguish current behaviour from
planned work, and do not document behaviour that code and tests do not support.

## Proxmox API and compatibility

Treat Proxmox VE as an external API contract.

Before changing PVE-facing behaviour:

1. Inspect the relevant code, tests, and documentation.
2. Determine whether the wire behaviour is already established.
3. When uncertain, verify against disposable `conformance-dev` only.
4. Record confirmed behaviour in regression tests and `docs/GAPS.md` where
   appropriate.

Do not:

- infer wire parameter names from UI labels
- silently translate unsupported or ambiguous state
- guess values that PVE does not retain
- write to production while investigating compatibility

Be explicit about endpoint, HTTP method, request fields, response shape,
failure modes, and PVE-version-specific behaviour.

## Schema and resource model

When changing a resource kind or field, consider the complete lifecycle:

- validation and parsing
- desired-to-PVE translation
- observation and drift comparison
- planning and execution
- adoption
- dependencies
- ownership
- documentation

Preserve established YAML naming and deterministic ordering. Reject conflicting
or ambiguous representations. Keep desired state separate from
PVE-owned/generated values. Do not add fields merely because PVE exposes them.

## Planning and reconciliation

Preserve these invariants:

- planning is derived from desired and observed state
- plan generation does not perform PVE writes
- execution follows established action ordering
- dependencies are honoured
- failed prerequisites defer dependants where required
- failed actions are not reported as converged
- destructive operations remain bounded and ownership-gated
- unsafe or unreconcilable live state is surfaced as an anomaly/error
- repeated convergence is idempotent

Do not introduce persistent reconciliation state unless the architecture
explicitly requires it.

Use `conformance-dev` only for destructive lifecycle testing.

## Adoption

Adoption is reverse engineering of observed PVE state into the ProxOps resource
model.

Preserve:

- production read-only behaviour unless explicitly authorised
- deterministic output
- faithful recovery of supported state
- explicit reporting of unsupported or unrecoverable state
- ownership safety
- secret redaction and non-disclosure

Never guess a value that PVE does not retain.

Production adoption must not silently become reconciliation or mutation.

## Secrets and security

Credentials, tokens, passwords, SSH material, and private keys are sensitive.

Never:

- print or log secrets
- include secrets in errors
- persist decrypted secrets unnecessarily
- include real secrets in fixtures, snapshots, generated output, or tests
- weaken secret validation to keep execution moving
- introduce a second secret mechanism without an architectural reason

When changing secret handling, preserve existing precedence and validation,
test missing/malformed/conflicting cases, and verify that secret values cannot
reach user-visible output.

For security-sensitive changes, prefer fail-closed behaviour and add regression
tests for the boundary being protected.
