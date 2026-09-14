---
description: Prepare ProxOps for a release
---

Prepare the repository for a release.

Inspect current Git state, recent commits, versioning, CI, docs, and known
gaps. Check for accidental secrets or sensitive generated material.

Run the applicable quality gates:

`go build ./...`
`go vet ./...`
`go test ./...`
`go test -race ./...`
`golangci-lint run`

Also run applicable documentation/configuration validation.

Do not make unrelated cleanup changes. Do not push or publish unless
explicitly requested.

Report release readiness, remaining risks, tests/checks run, and commits made.
