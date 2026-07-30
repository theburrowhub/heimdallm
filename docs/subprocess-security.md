# AI subprocess security

Heimdallm runs third-party AI CLIs against repository content, including
untrusted pull-request diffs and issue text. The daemon therefore treats every
CLI launch as a credential boundary: selecting one provider must not disclose
the GitHub token, Heimdallm configuration, another provider's key, or unrelated
host credentials to that process.

## Environment contract

AI subprocess environments are built from an empty set. They receive a small
cross-platform runtime baseline (`PATH`, locale, temporary-directory and user
identity variables), headless-mode settings, and only the default credentials
for the selected CLI:

| CLI | Credentials exposed by default |
|---|---|
| Claude | `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` |
| Codex | `OPENAI_API_KEY`, `CODEX_API_KEY` |
| Gemini | `GEMINI_API_KEY`, `GOOGLE_API_KEY`, plus explicitly configured Vertex AI variables |
| OpenCode | `OPENROUTER_API_KEY` |

OpenCode also preserves the non-secret `OPENCODE_DISABLE_AUTOUPDATE` runtime
setting used by the container image.

The Vertex AI variables are `GOOGLE_APPLICATION_CREDENTIALS`,
`GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, and
`GOOGLE_GENAI_USE_VERTEXAI`. A credentials path must point to a file visible
inside the container; Heimdallm does not mount host service-account files
automatically.

Detection and `--help` probes do not receive provider credentials.

For full-repository Claude analysis, the process also stays in the isolated
temporary directory and receives the validated repository only through
`--add-dir` (or the compatible `--directory` form). Heimdallm always forces
Claude's upstream `--safe-mode`, which disables CLAUDE.md instructions, hooks,
plugins, skills, commands, agents, workflows, output styles, and MCP servers
while preserving authentication and managed policy. Support for that flag is
verified with the credential-free help probe; Claude Code older than 2.1.169
fails closed. A Claude CLI without a supported additional-directory option
also fails closed instead of running inside the repository.

For full-repository Gemini analysis, the process CWD remains inside the
temporary home and the validated repository is passed through Gemini's
include-directory option. Heimdallm also supplies a mode-`0600` system
settings override with `advanced.ignoreLocalEnv=true`, forces `--ignore-env`,
and omits Gemini's special `.gemini/.env` from the state projection. The
temporary CWD alone is trusted for the headless session; the repository stays
an included external directory. A repository or user-state `.env` therefore
cannot repopulate the sanitized parent environment. An older Gemini CLI
without these flags or a supported include-directory option fails closed
instead of falling back to an unsafe repository CWD.

OpenCode likewise starts from the isolated temporary directory and receives
the validated repository through its `run --dir` option. This prevents the
runtime itself from loading repository-local environment files before the CLI
has established its own policy. Heimdallm probes the managed
`--pure run --help` path and fails closed when the CLI does not support both
pure mode and `run --dir`. Pure mode prevents executable plugins declared by
the repository or user state from inheriting the selected provider credential.
Its managed
`OPENCODE_DISABLE_PROJECT_CONFIG=1` policy also prevents repository
`opencode.json`, `.opencode`, MCP, command, agent, and dependency-install
configuration from being loaded; operator configuration remains available
through the isolated global state bridge.

## Isolated home directories

Each execution gets a new mode-`0700` temporary `HOME`. Heimdallm projects only
the selected provider's state/authentication paths into that home:

- Claude: `.claude/.credentials.json` and `.claude.json`
- Codex: `.codex`
- Gemini: `oauth_creds.json`, `google_accounts.json`, `installation_id`, and
  legacy `user_id`, plus an input-only auth selector from `settings.json`
- OpenCode: `.config/opencode` and `.local/share/opencode`

Claude's two JSON files use mode-`0600` copy-in/copy-out instead of symlinks.
Changed state is validated, normally synchronized with an atomic replace before
the temporary home is removed, and rejected on concurrent modification or
symlink/device replacement. This preserves OAuth rotation, including when the
CLI exits with an error, without exposing persistent `.claude/settings.json`,
hooks, plugins, skills, or commands. Empty first-run `.claude.json` files are
accepted. Linux file bind mounts, which reject replacement with `EBUSY`, use a
narrowly scoped in-place fallback with mode `0600` and `fsync`. The
`.claude.json` file remains trusted
provider-owned state and can be updated by Claude itself; safe mode prevents
entries stored there from starting MCP processes in a Heimdallm run.

Gemini likewise uses copy-in/copy-out for only the four mutable token or
identifier files, preserving first-run creation and atomic rotations. Its user
`settings.json` is reduced to `selectedAuthType` and
`security.auth.selectedType` for headless login; it is input-only and never
copied back. `.env`, `GEMINI.md`, MCP tokens, settings, extensions, commands,
skills, agents, and policies are not projected. A read-only Docker OAuth mount
remains read-only: a refresh is usable for that execution but cannot be
persisted, and Heimdallm emits a warning instead of discarding a successful
review. Native and `make run-linux` read-write state persists rotations.

This preserves file-based login and session refresh without exposing unrelated
home-directory state such as `.ssh`, `.aws`, `.gitconfig`, or another CLI's
credentials. In Docker, an explicitly mounted read-only provider directory
(for example the documented Gemini OAuth mount) remains read-only. Values
stored only in `.gemini/.env` are intentionally not imported;
configure built-in Gemini variables in the daemon environment, or use an
explicit Gemini allowlist plus a private Compose overlay.

## Opting in additional variables

Some enterprise or CLI integrations need auxiliary environment variables
beyond the standard proxy and CA variables already present in the common
runtime baseline. Opt in exact variable names per CLI with a comma-separated
list:

```dotenv
HEIMDALLM_AI_GEMINI_ENV_ALLOWLIST=GOOGLE_CLOUD_QUOTA_PROJECT
HEIMDALLM_AI_OPENCODE_ENV_ALLOWLIST=CORPORATE_TENANT_ID
```

This is a names-only policy. The corresponding values must be present in the
daemon environment. Docker Compose forwards the documented provider variables;
custom variables must also be added to an operator-owned compose overlay:

```yaml
services:
  heimdallm:
    environment:
      CORPORATE_TENANT_ID: ${CORPORATE_TENANT_ID}
```

An allowlisted name that is absent emits an operator-visible warning without
logging a value. Missing optional provider-state paths emit an info breadcrumb
once per path, so first-run behavior is diagnosable without flooding logs.

The following classes are denied even when named in an allowlist:

- `GITHUB_TOKEN` and `GH_TOKEN`
- every `HEIMDALLM_*` variable
- every `GIT_*` variable
- managed home, state, and policy controls such as `HOME`, `PATH`,
  `CLAUDE_CODE_SAFE_MODE`, `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB`,
  `GEMINI_CLI_HOME`, Gemini settings/system-prompt/sandbox command overrides,
  OpenCode config path/content/directory overrides,
  `OPENCODE_DISABLE_PROJECT_CONFIG`, and `OPENCODE_PURE`
- dynamic-loader and shell-startup injection variables, including
  `LD_PRELOAD`, `DYLD_*`, `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `NODE_OPTIONS`,
  `NODE_PATH`, JVM agent options, `.NET` startup hooks, GTK/GIO module paths,
  and Python warning-import controls

Empty CSV elements are ignored, so a trailing or doubled comma does not disable
valid entries. Invalid names and wildcard patterns are rejected. Matching is
case-insensitive for the common runtime baseline while preserving the parent's
original spelling. Do not treat an allowlist as a convenient way to copy the
daemon's environment wholesale.
The permanent denylist is defense in depth, not an exhaustive catalogue of
every runtime's future injection variables. A name not present in the denylist
is not automatically safe: each explicit opt-in remains an operator trust
decision.
Provider credential boundaries are permanent for Claude, Codex, and Gemini.
OpenCode is the deliberate exception because it is a multi-provider client:
`OPENROUTER_API_KEY` is available by default, while an Anthropic, OpenAI, or
Gemini backend key requires its exact name in
`HEIMDALLM_AI_OPENCODE_ENV_ALLOWLIST`. Unnamed backend credentials remain
absent.

## SSH agent access is explicit

The base Docker deployment neither mounts the host SSH agent nor sets
`SSH_AUTH_SOCK`. Heimdallm's own `auto_implement` Git operations use HTTPS with
an ephemeral askpass helper, so they do not need the agent.

If an AI CLI itself must perform an SSH operation, opt the socket into that CLI
and add a private compose overlay. For Claude on macOS Docker Desktop:

```dotenv
# docker/.env
HEIMDALLM_AI_CLAUDE_ENV_ALLOWLIST=SSH_AUTH_SOCK
HEIMDALLM_SSH_AUTH_SOCK=/run/host-services/ssh-auth.sock
```

```yaml
# docker/docker-compose.ssh.yml — operator-owned; do not commit credentials
services:
  heimdallm:
    environment:
      SSH_AUTH_SOCK: /ssh-agent
    volumes:
      - type: bind
        source: ${HEIMDALLM_SSH_AUTH_SOCK}
        target: /ssh-agent
        read_only: true
```

Start with both files:

```bash
docker compose \
  --env-file docker/.env \
  -f docker/docker-compose.yml \
  -f docker/docker-compose.ssh.yml \
  up -d
```

On Linux, set `HEIMDALLM_SSH_AUTH_SOCK` to the host agent's actual socket.
Change the allowlist variable for Codex, Gemini, or OpenCode as appropriate.
Agent access lets the selected CLI request signatures with every key loaded in
that agent; enable it only when this broader authority is intentional.

## Automated Git subprocesses

Daemon-owned Git commands use a separate boundary from AI CLIs. Every command
gets a fresh mode-`0700` home, config root, temporary directory, and empty hooks
directory. The runner ignores inherited `GIT_*`, GitHub, loader, shell, global,
and system configuration; permits only an absolute trusted Git binary path;
and forces hooks, credential helpers, editors, pagers, signing, recursive
submodules, maintenance, external diff/text-conversion, and unsafe transport
protocols off. Repository and worktree config is audited before use, including
includes, filters, executable helpers, object alternates, and remote helper
settings. For Linux bind mounts whose host ownership differs from the daemon
UID, Git receives `safe.directory` only for the exact canonical worktree that
passed that audit; wildcard trust is never enabled.

Authenticated clone, fetch, push, and conditional branch deletion accept only
canonical GitHub HTTPS remotes. The token is supplied through an ephemeral
askpass helper, never in argv or a remote URL. For fetch and push, the
token-bearing Git process works against an empty temporary bare repository;
the checkout is imported or staged later by a tokenless process. Clone
materializes the worktree only after its authenticated `--no-checkout` phase,
and all commits and pushes explicitly disable hooks.

Corporate proxy and CA variables are the only host network settings forwarded
to Git. Error output is bounded and redacts the GitHub token, its encoded
representations, and proxy credentials.

AI CLI errors redact selected provider secrets, proxy credentials, and
explicitly opted-in values whose names have an exact secret suffix (`_TOKEN`,
`_SECRET`, `_PASSWORD`, `_API_KEY`, `_DATABASE_URL`, `_DSN`, and related
forms). Allowlisted URL values with user information are also redacted even
when their names are neutral. This avoids false positives such as
`TOKENIZER_MODE` or `COOKIE_POLICY`. Non-secret runtime selectors such as
Vertex project, location, mode, credential-file path, or a custom region remain
intact so diagnostics are not corrupted. Codex tool subprocesses do not receive
credential-bearing proxy URLs but retain `NO_PROXY`.

## Threat model and residual risk

Environment and home isolation reduce accidental disclosure and common prompt
injection paths. They are not a filesystem or operating-system sandbox.

The CLI still runs as the daemon's UID, in the same container or host account,
and can access any absolute path, mounted repository, device, socket, or network
destination available to that identity. A malicious or compromised CLI can
also exfiltrate its own authorized provider credential.

For strong isolation, run Heimdallm in a dedicated container or VM with:

- only the required repositories mounted, read-only unless implementation
  writes are needed;
- no Docker socket, cloud metadata credentials, host home, or unrelated
  volumes;
- a dedicated UID and narrowly scoped network egress;
- short-lived, least-privilege provider and GitHub credentials.

The GitHub token remains in the daemon because it is required for API and
HTTPS Git operations, but it is not passed to AI subprocesses.
