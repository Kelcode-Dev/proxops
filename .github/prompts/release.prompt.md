---
description: Prepare ProxOps for a release
---

Prepare the repository for a ProxOps release.

Read `AGENTS.md` and `.github/instructions/proxops.instructions.md` first.

Inspect:

- Git status and recent commits
- versioning and tag behaviour
- CI and release workflows
- build/cross-build behaviour
- public documentation and install instructions
- known gaps
- accidental secrets or sensitive generated material

Run the applicable release gates:

- `go build ./...`
- `go vet ./...`
- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `golangci-lint run`
- `mkdocs build --strict`

Also validate examples, configuration, release artifact generation, checksums,
and version output where those facilities exist.

Verify that the repository contains no accidental credentials, private keys,
plaintext secrets, credential-bearing URLs, or private environment data that
should not be public.

Do not make unrelated cleanup or feature changes.

Do not push, publish, change repository visibility, create releases, or create
release tags unless explicitly requested.

Report:

- release readiness
- current HEAD
- version/tag behaviour
- tests and validation performed
- release artifacts/checksums verified
- security/privacy audit result
- documentation/install-path status
- remaining concrete blockers
- commits made
- exact owner actions still required
