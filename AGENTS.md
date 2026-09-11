# AGENTS.md

## Project identity

PVE Conform is a standalone, stateless GitOps daemon/operator for
Proxmox VE.

The fundamental model is:

- Git represents desired state.
- Proxmox VE represents observed state.
- Reconciliation is deterministic and idempotent.
- The process may manage multiple independent PVE clusters.

This repository contains the **PVE Conform software**. It is not the
separate infrastructure/GitOps repository that supplies manifests.

Do not introduce Kubernetes, Kustomize, Terraform, or another orchestration
model unless explicitly requested.

## Engineering principles

Prefer:

- existing abstractions over parallel implementations
- small, coherent changes over broad refactors
- explicit behaviour over implicit magic
- evidence over assumptions
- deterministic output and ordering
- reversible and conservative operations
- fail-closed behaviour where correctness or safety is uncertain

Do not "simplify" away safety checks, ownership boundaries, compatibility
handling, or error states merely to make an implementation smaller.

## Read before changing behaviour

Before changing non-trivial behaviour, inspect the relevant existing code,
tests, and documentation.

The authoritative project documents are:

- `docs/ARCHITECTURE.md` — system architecture and data flow
- `docs/SCHEMA.md` — resource schema and semantics
- `docs/OPERATIONS.md` — operational behaviour and runbooks
- `docs/GAPS.md` — compatibility backlog and deliberate limitations

Do not copy large amounts of those documents into agent instructions.
Read them when their subject is relevant and update them when the project's
documented contract changes.

## Safety

### Production

Production infrastructure is read-only by default.

Do not mutate production while:

- discovering or inspecting behaviour
- adopting existing resources
- investigating API semantics
- developing or debugging a change
- running compatibility probes
- validating a proposed design

Production mutation requires explicit authorisation from the user.

Do not silently turn a read-only operation into an apply, reconcile, prune,
delete, restart, or reconfiguration operation.

When a task is explicitly read-only, verify that the implementation really
performs no writes.

### Disposable infrastructure

Use the disposable `conformance-dev` environment for destructive or
lifecycle testing.

Automated test VMIDs are:

`9100–9199`

Keep disposable guests within the established project limits:

- up to 4 vCPU
- up to 8 GiB RAM
- approximately 20 GiB disk

Do not modify the underlying PVE cluster configuration merely to facilitate
a test.

Never use a production resource as a convenient test fixture.

### Fail closed

When behaviour is unknown, ambiguous, unsupported, lossy, or unsafe:

- investigate where practical
- otherwise reject, defer, or explicitly report it

Do not invent Proxmox semantics.

Do not silently discard state that could cause destructive drift or an
incorrect desired state.

## Proxmox API discipline

Treat the Proxmox API as an external contract.

When changing PVE-facing behaviour:

1. Inspect the existing PVE client and affected code.
2. Inspect existing tests and relevant documentation.
3. Check whether the behaviour is already established elsewhere in the code.
4. Verify uncertain wire behaviour against disposable PVE where practical.
5. Encode confirmed behaviour in tests when it is regression-sensitive.

Do not infer API parameter names from UI labels.

Do not assume two similarly named PVE fields have the same wire grammar.

When PVE version differences matter, preserve the compatibility boundary
explicitly.

Prefer the exact endpoint, method, request shape, and response semantics
established by evidence.

## Reconciliation and resource ownership

Reconciliation must be conservative, deterministic, and idempotent.

Preserve these invariants:

- desired state is explicit
- live state is observed from PVE
- plans are derived from current desired/live state
- failed actions remain failed; they are not reported as converged
- dependencies are respected
- destructive operations are bounded
- ownership checks are enforced before deletion
- unowned resources are not deleted merely because they are absent from the
  desired set
- unexpected or potentially destructive live state is surfaced rather than
  silently normalised

Do not weaken ownership or prune safety to make a test or implementation
convenient.

A second reconciliation of an already-converged resource should not invent
additional work.

## Resource model and schema

The resource schema is a project-level contract.

When adding or changing a resource kind or field, consider all relevant
surfaces:

- schema/model
- validation
- parsing
- PVE wire translation
- live-state loading
- planning/drift detection
- execution
- adoption/reverse translation
- ownership behaviour
- dependency handling
- tests
- documentation

Do not add a field solely because PVE exposes it. Determine whether it is
meaningful, stable, mutable, recoverable, and safe to reconcile.

Keep unsupported or lossy PVE state explicit.

Use `docs/SCHEMA.md` as the source of truth for the resource contract and
`docs/GAPS.md` for known compatibility limitations.

## Adoption

Adoption is a reverse translation from live Proxmox state into the PVE
Conform resource model.

Adoption must be:

- read-only with respect to PVE unless explicitly authorised otherwise
- deterministic
- faithful to recoverable state
- explicit about unsupported or unrecoverable state
- safe with respect to ownership
- free of secret leakage

Do not make adoption "look complete" by guessing values PVE does not retain.

When a live property cannot be represented safely, preserve the project's
existing gap/incomplete reporting behaviour.

See the adoption sections of `docs/ARCHITECTURE.md`, `docs/OPERATIONS.md`,
and `docs/GAPS.md` before changing adoption behaviour.

## Secrets

Credentials and private keys are security-sensitive.

Never:

- commit plaintext credentials
- print credentials
- log credentials
- include credentials in errors
- emit credentials in generated output
- persist decrypted secrets unnecessarily

Use the repository's existing secret mechanism and precedence rules.

When extending secret handling, reuse the established abstraction rather
than introducing a second mechanism.

Fail closed on missing, malformed, undecryptable, or ambiguous secrets.

See the relevant configuration and security documentation before changing
secret handling.

## Testing

Use the least expensive test that proves the behaviour, but prefer real PVE
evidence when the behaviour is specifically about PVE semantics.

Use unit tests for:

- parsing
- validation
- deterministic transformations
- pure planning logic
- error classification
- security invariants

Use `conformance-dev` for:

- real PVE API behaviour
- lifecycle operations
- wire-format compatibility
- create/update/delete semantics
- adoption round-trips
- destructive regression tests

Clean up disposable resources after tests.

When a real PVE bug or quirk causes a regression, add a test that pins the
confirmed behaviour.

Never modify production merely to increase test coverage.

## Change discipline

Before editing:

- inspect the current implementation
- understand the existing abstraction
- inspect relevant tests
- identify the safety boundary
- identify the authoritative documentation
- check for existing equivalent behaviour

Then make the smallest coherent change that solves the task.

Avoid unrelated:

- refactors
- renames
- formatting churn
- dependency changes
- cleanups

Do not duplicate an existing mechanism because it is locally faster.

## Git workflow

Use meaningful Conventional Commits as coherent work is completed.

Examples:

- feat(vm): add cloud-init configuration handling
- fix(plan): prevent live disk replacement on size drift
- test(adopt): cover template VM discovery
- docs(schema): document cloud-init fields
- refactor(config): simplify cluster credential resolution
- chore(ci): update golangci-lint configuration

Guidelines:

- Keep implementation and directly related tests/docs together.
- Prefer several coherent commits when the work naturally has several
  milestones.
- Do not create one giant final commit for a multi-stage change.
- Do not squash meaningful history merely for neatness.
- Inspect the diff and status before committing.
- Do not push unless the user explicitly asks.
- Do not commit secrets, private keys, credentials, or unrelated local work.
- Do not rewrite or discard the user's existing commits or uncommitted work
  unless explicitly requested.

When reporting work, include the commit hashes created during the task.

## Quality gates

For Go changes, run the applicable repository checks before declaring work
complete:

```text
go build ./...
go vet ./...
go test ./...
go test -race ./...
golangci-lint run
```

For documentation or configuration changes, run the repository's applicable
validation tools.

Do not claim a check passed unless it actually ran successfully.

If a relevant check cannot be run, state that explicitly.

## Determinism

Determinism is a design requirement, not merely a test concern.

Preserve stable ordering for:

- resource traversal
- generated output
- plans
- adoption results
- status/report output
- dependency processing
- multi-cluster processing

Avoid timestamps, random identifiers, map iteration order, or PVE-assigned
values appearing in generated output when they are not part of desired state.

When testing determinism, compare output byte-for-byte where practical.

## Error handling

Errors must be useful without becoming a data-leak mechanism.

Where possible, an error should identify:

- the operation
- the affected resource
- the failure class
- the safe next step

Never include secret values in errors.

Do not convert partial failure into success.

Do not hide a meaningful compatibility gap simply to produce cleaner output.

## Documentation discipline

Documentation describes the contract; code and tests implement it.

Update the relevant document when a change alters:

- architecture
- resource schema
- operational behaviour
- supported/unsupported PVE behaviour
- security guarantees
- compatibility expectations

Prefer adding a concise explanation to the authoritative document rather than
duplicating the same detail across many files.

## Agent workflow

For a non-trivial task:

### Before implementation

1. Read the relevant project documentation.
2. Inspect the affected implementation and tests.
3. Identify safety and ownership boundaries.
4. Decide whether real PVE behaviour must be verified.
5. Identify the smallest coherent implementation path.

### During implementation

1. Reuse existing abstractions.
2. Make focused changes.
3. Add regression tests for changed behaviour.
4. Use `conformance-dev` for real PVE compatibility/lifecycle tests.
5. Keep production read-only unless explicitly authorised.
6. Keep security-sensitive data out of logs and generated files.
7. Commit coherent milestones where appropriate.

### Before completion

1. Run applicable quality gates.
2. Inspect `git diff` and `git status`.
3. Check for accidental secrets or sensitive generated material.
4. Confirm tests and documentation match the implementation.
5. Confirm no unauthorised production mutation occurred.
6. Report changes, tests, known limitations, and commit hashes.

## When uncertain

Do not guess.

Use this order of evidence:

1. Existing code and abstractions.
2. Existing project documentation.
3. Existing tests.
4. Real disposable PVE behaviour.
5. Official Proxmox/API documentation.
6. Conservative fail-closed behaviour.

When evidence conflicts, surface the conflict and resolve it deliberately rather
than choosing the most convenient interpretation.
