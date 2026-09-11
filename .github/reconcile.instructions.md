---
applyTo: "**/internal/secrets/**/*.go,**/*secret*.go,**/*credential*.go,**/internal/config/**/*.go"
---

# Security and secret-handling rules

Credentials, tokens, passwords, SSH material, and private keys are sensitive.

Never:
- print secrets
- log secrets
- put secrets in errors
- persist decrypted secrets unnecessarily
- include secrets in fixtures, snapshots, generated output, or tests
- weaken secret validation to keep execution moving

Reuse the existing secret/configuration abstractions.

When changing secret handling:
- inspect current precedence and validation logic first
- maintain fail-closed behaviour
- test missing, malformed, conflicting, and invalid-secret cases
- verify that secret values cannot reach user-visible output
- avoid introducing a second secret mechanism

Do not add credentials to resource schemas unless the architecture explicitly
requires a secure representation.
