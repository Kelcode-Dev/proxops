---
applyTo: "**/*.go"
---

# Go implementation rules

Read `AGENTS.md` and the relevant project docs before changing behaviour.

- Follow existing package boundaries, naming, typing, and error-handling
  patterns.
- Prefer small, explicit changes over broad refactors.
- Reuse existing config, schema, PVE client, planner, executor, and adoption
  abstractions.
- Keep pure logic pure where the existing architecture does so.
- Return actionable errors without exposing credentials or sensitive data.
- Add regression tests for changed behaviour.
- Do not weaken safety checks, ownership checks, or fail-closed behaviour just
  to make a test pass.
- Avoid `nolint` directives unless the exception is genuinely unavoidable and
  documented.
- Keep exported APIs minimal and intentional.

For Go changes, run the repository quality gates that apply to the change:
`go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`,
and `golangci-lint run`.
