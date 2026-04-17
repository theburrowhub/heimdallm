# AGENTS.md

Instructions for AI coding agents (Claude Code, Cursor, Copilot, …) and human
collaborators working on this repo. These rules override the agent's default
workflow — follow them exactly.

## Go tests must run in Docker

**Always use `make test-docker` for the Go daemon test suite.**

```bash
make test-docker
# narrow down to specific packages/tests:
make test-docker GO_TEST_ARGS="-run TestDiscovery ./internal/discovery/..."
```

### Why this is non-negotiable

`go test` compiles a transient binary named `<pkg>.test` in
`/var/folders/.../go-build/` (macOS) or `/tmp/go-build...` (Linux), then runs
it. These tests:

- open random local ports via `httptest.NewServer`,
- send HTTP requests with `Authorization: Bearer <token>` headers,
- exit within milliseconds.

That signature matches heuristics used by corporate EDR agents
(Elastic Security, CrowdStrike, SentinelOne, Defender for Endpoint, …).
The EDR will kill the process mid-test — surfaced to the user as
`signal: killed` — and send a **malware alert** to the security team, who
will then open a ticket with the developer. **This has already happened
once in this repo (2026-04-17).** We do not want to do it again.

Running the tests inside a Docker container means the ephemeral binaries
live in the container's tmpfs, not the host filesystem. The host EDR never
sees them.

### What `make test-docker` guarantees

Hardening details are inlined in the Makefile target. Summary:

| Concern | Guarantee |
|---|---|
| Image | Official `golang:1.21-alpine`, **pinned by SHA256 digest** (not a mutable tag) |
| Host filesystem access | Single mount: repo at `/src`, **read-only** (`:ro`) |
| Build cache | Redirected to `/tmp/heimdallm-gocache/` — does not touch `~/.cache` or `~/go` |
| Privileges | Runs as invoking user (`--user $(id -u):$(id -g)`) — no root in the container |
| Container persistence | `--rm` — destroyed on exit, no `*.test` artefacts on the host |
| Network | Default bridge (needed for `go mod download` on first run) |
| Ports | None exposed |

If IT/Security asks how we run Go tests on their laptops, point them at
this section and the `test-docker` target.

## When `go test` on the host is OK

Never, on a corporate laptop with an EDR, unless Security has explicitly
whitelisted `$GOCACHE` and `/var/folders/.../go-build/`. Raising a ticket
for that exception is preferable to bypassing the rule.

On personal machines, CI runners, or Docker-in-Docker sandboxes, `go test`
directly is fine. The CI workflow (`.github/workflows/tests.yml`) runs on
`macos-14` GitHub Actions runners and uses `setup-go` natively — that
environment has no EDR.

## Flutter tests

Flutter tests do not trip EDR heuristics (Dart VM, no ephemeral native
binaries). Run them normally:

```bash
cd flutter_app && flutter test
# or combined with Go tests (note: `make test` runs Go on the host):
make test
```

**Prefer `make test-docker && cd flutter_app && flutter test`** over
`make test` on EDR-protected endpoints.

## Conventional commits

`release-please` reads commit prefixes to bump semver and generate the
changelog. Stick to: `feat:`, `fix:`, `refactor:`, `docs:`, `chore:`,
`ci:`, `test:`. A `feat!:` or trailer `BREAKING CHANGE:` forces a major
bump.

## Do not

- Run `go test` / `go build` on the host on a corporate laptop with EDR.
- Stage `.DS_Store` in commits.
- Push directly to `main` — always open a PR.
- Skip hooks (`--no-verify`) without an explicit user instruction.
- Invent a different Docker image/tag for tests — the pinned digest in the
  Makefile is the canonical one. Bump it deliberately, in its own commit,
  when upgrading Go.
