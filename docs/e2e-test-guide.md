# End-to-End Test Guide

This guide walks you through verifying the full Heimdallm review pipeline: daemon detects a PR, invokes an AI CLI, and publishes a review on GitHub.

## Prerequisites

| Requirement | Details |
|---|---|
| **GitHub Token** | A `GITHUB_TOKEN` with `repo` scope for a user/bot account |
| **AI Authentication** | At least one: `GEMINI_API_KEY`, `ANTHROPIC_API_KEY` (or `CLAUDE_CODE_OAUTH_TOKEN`), or `OPENAI_API_KEY` |
| **Monitored Repo** | A repo listed in `HEIMDALLM_REPOSITORIES` where the token user has write access |
| **Open PR** | The token user must be **explicitly requested as a reviewer** on the PR |
| **Collaborator** | GitHub does not allow requesting yourself as reviewer — another account must do it |

## PR Selection Logic

Heimdallm only reviews PRs matching ALL of these conditions:

```
GitHub Search: is:pr is:open review-requested:<token-user>
  → repo in HEIMDALLM_REPOSITORIES?
  → not dismissed in local DB?
  → not already reviewed (or PR updated since last review + 30s)?
  → not already in-flight?
  → RUN REVIEW
```

## Step-by-Step

### 1. Prepare the Environment

```bash
cp docker/.env.example docker/.env
```

Edit `docker/.env`:

```env
GITHUB_TOKEN=ghp_your_real_token
HEIMDALLM_AI_PRIMARY=gemini
HEIMDALLM_REPOSITORIES=yourorg/yourrepo
HEIMDALLM_POLL_INTERVAL=1m

# Pick one (or more) AI API keys:
GEMINI_API_KEY=your_gemini_key
# ANTHROPIC_API_KEY=your_anthropic_key
# CLAUDE_CODE_OAUTH_TOKEN=your_oauth_token    # alternative to ANTHROPIC_API_KEY
# OPENAI_API_KEY=your_openai_key
```

### 2. Create a Test PR

In one of your monitored repos:

```bash
git checkout -b test/heimdallm-e2e
echo "// test change" >> some_file.go
git add . && git commit -m "test: heimdallm e2e verification"
git push -u origin test/heimdallm-e2e
gh pr create --title "test: Heimdallm E2E verification" --body "This PR is for testing Heimdallm automated reviews. Will be closed after verification."
```

### 3. Request Review

From a **different GitHub account** (collaborator, bot, or org member):

```bash
gh pr edit <PR_NUMBER> --add-reviewer <token-user>
```

Or use CODEOWNERS/branch protection to auto-assign reviewers.

### 4. Start the Daemon

```bash
# Creates a unique, isolated Compose project for this invocation.
make test-e2e
```

Do not invoke `docker-compose.test.yml` directly. The runner generates a
project name such as `heimdallm-test-local-<pid>-<random>`, unique container
names, per-project data and config volumes, and free loopback-only host ports.
This means a normal `make up` stack and other test runs can stay active without
sharing containers, networks, ports, or persistent data.

The runner prints the exact project name and daemon URL after startup. Keep it
running while following the remaining steps; press Ctrl+C when finished.

### 5. Observe the Pipeline

Watch the logs for these stages in order:

| Stage | Log Pattern | Meaning |
|---|---|---|
| 1. Startup | `daemon started` | Service is up |
| 2. Poll | `github: PRs to review` | GitHub API polled |
| 3. Detection | `pipeline: reviewing PR` | PR matched filters |
| 4. CLI | `pipeline: using CLI` | AI CLI selected |
| 5. Execution | `executor: run <cli>` | CLI invoked with prompt |
| 6. Storage | `pipeline: review stored` | Result saved to SQLite |
| 7. Publish | `pipeline: review published` | Review posted to GitHub |

### 6. Verify via API

While the daemon is running, in another terminal:

```bash
# Copy the per-run URL printed by make test-e2e.
BASE='http://127.0.0.1:<printed-port>'

# The runner also prints its exact `docker compose --project-name ...` command.
# Copy it into this terminal and append the exec arguments:
<printed-compose-command> exec -T heimdallm cat /data/api_token
API_TOKEN='<trimmed output from the previous command>'
AUTH_HEADER="X-Heimdallm-Token: $API_TOKEN"

# Health check
curl -s "$BASE/health" | jq

# Authenticated user
curl -s -H "$AUTH_HEADER" "$BASE/me" | jq

# Detected PRs
curl -s -H "$AUTH_HEADER" "$BASE/prs" | jq

# Review stats
curl -s -H "$AUTH_HEADER" "$BASE/stats" | jq

# Stream SSE events (Ctrl+C to stop)
curl -N -H "$AUTH_HEADER" "$BASE/events"
```

### 7. Verify on GitHub

Check the PR on GitHub — you should see a review comment from the token user with:

- A summary of the changes
- A list of issues found (if any)
- Suggestions for improvement
- A severity rating (low/medium/high)

### 8. Advanced: Manual Review Trigger

Reuse the per-run `BASE`, `API_TOKEN`, and `AUTH_HEADER` values from step 6:

```bash
# Get PR ID from /prs
PR_ID=$(curl -s -H "$AUTH_HEADER" "$BASE/prs" | jq '.[0].id')

# Trigger manual review
curl -X POST $BASE/prs/$PR_ID/review -H "X-Heimdallm-Token: $API_TOKEN"

# Dismiss a PR (stop auto-reviewing)
curl -X POST $BASE/prs/$PR_ID/dismiss -H "X-Heimdallm-Token: $API_TOKEN"

# Undismiss (re-enable auto-reviewing)
curl -X POST $BASE/prs/$PR_ID/undismiss -H "X-Heimdallm-Token: $API_TOKEN"
```

### 9. Clean Up

Press Ctrl+C in the runner terminal. Its EXIT trap verifies the private marker
created for that process before it can call `down -v`; if the project name or
marker does not match, cleanup fails closed and Docker is not invoked. The
verified cleanup removes only that run's containers, network, and named
volumes. The normal development/production project is never targeted.

If cleanup fails, the runner preserves its private marker and cleanup log and
prints two copyable, run-scoped commands: one to retry the exact
`down -v --remove-orphans`, and another to remove the marker, log, and private
state directory **after** Docker cleanup succeeds. The latter deliberately
uses `rm -f` for the two known files followed by `rmdir`, rather than a
recursive delete, so unexpected contents are never removed silently.

If the process is killed with SIGKILL, Docker resources can remain because no
shell trap can run. Their project and container names retain the
`heimdallm-test-*` prefix for inspection; confirm the exact Compose project
labels before removing only that run. Do not use an unscoped `down -v`.

Finally, close the test PR:

```bash
gh pr close <PR_NUMBER> -d  # -d deletes the branch too
```

## Gemini CLI Authentication for Docker

Three options for authenticating the Gemini CLI inside Docker:

### Option A: API Key (Simplest)

Set `GEMINI_API_KEY` in `docker/.env`. Get a key from https://aistudio.google.com/apikey

### Option B: OAuth Token Mount

If you've already authenticated `gemini` on your host:

1. Ensure `~/.gemini/` exists with OAuth tokens
2. Uncomment in `docker/docker-compose.test.yml`:
   ```yaml
   volumes:
     - ~/.gemini:/home/heimdallm/.gemini:ro
   ```
   The test imports authentication state read-only. Any OAuth refresh is
   confined to that run and is not written back to the host.

### Option C: Vertex AI + Service Account

For production/CI environments:

1. Create a GCP service account with Vertex AI permissions
2. Mount the JSON key file and set `GOOGLE_APPLICATION_CREDENTIALS`

## Claude Code Authentication for Docker

Two options for authenticating Claude Code inside Docker:

### Option A: API Key (Simplest)

Set `ANTHROPIC_API_KEY` in `docker/.env`. Get a key from https://console.anthropic.com/settings/keys

### Option B: OAuth Token (Subscription)

For Max/Pro/Team subscription users:

1. On your host, run `claude setup-token` to generate a long-lived token
2. Set `CLAUDE_CODE_OAUTH_TOKEN` in `docker/.env`

> **Note:** Do not enable `bare = true` in `config.toml` when using OAuth tokens — bare mode disables OAuth and requires an API key.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `401: Bad credentials` | Invalid GitHub token | Regenerate token with `repo` scope |
| No PRs detected | Token user not requested as reviewer | Have collaborator add you as reviewer |
| PR detected but no review | AI CLI auth failed | Check API key / OAuth mount |
| `executor: run <cli>: exit status 1` | CLI runtime error | Check stderr in logs for details |
| Claude Code auth error with OAuth token | `bare = true` enabled in config | Remove `bare = true`, or switch to `ANTHROPIC_API_KEY` |
| `pipeline: review already exists` | PR already reviewed recently | Wait for PR to be updated, or use `/prs/<id>/review` to force |
