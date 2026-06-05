# Auto-versioned hero badge via release-please extra-files (#514)

**Date:** 2026-06-05
**Issue:** [#514 — docs(landing): auto-version the hero badge from release pipeline](https://github.com/theburrowhub/heimdallm/issues/514)
**Status:** Approved

## Problem

PR #513 (landing refresh for 0.6.23) hardcoded a version string in the
`hero-badge` of `docs/index.html`; reviewers flagged that it goes silently
stale on every release, so the version was removed as a stopgap. This issue
restores the version on the landing page without manual edits per release.

## Decision

Use release-please's **`extra-files` generic updater**
([docs](https://github.com/googleapis/release-please/blob/main/docs/customizing.md#updating-arbitrary-files)):
files listed in `extra-files` get any semver on lines annotated with
`x-release-please-version` replaced with the new version, **inside the
release PR release-please already opens**.

Rejected alternatives:

- **Step in `release.yml` that commits the bump to main** — a direct
  workflow push to main conflicts with branch protection (everything lands
  via reviewed PRs in this repo); `extra-files` rides the normal release PR
  instead, with zero workflow changes.
- **Client-side JS fetching `releases/latest`** — depends on the GitHub API
  on every visit (rate limits, version flash, failure fallback).
- **Build-time injection** — GitHub Pages serves `main:/docs` statically;
  there is no build step to hook.

The updating commit is the same one that creates the tag, so badge and
release can never drift.

## Changes (2 files)

1. **`release-please-config.json`** — add inside `packages["."]` (tied to
   the single versioned component, not floating at the root):

   ```json
   "extra-files": [
     { "type": "generic", "path": "docs/index.html" }
   ]
   ```

2. **`docs/index.html`** — re-seed the badge with the current manifest
   version (`0.6.23`) and annotate the line:

   ```html
   <div class="hero-badge">⚡ v0.6.23 · macOS · Linux · Docker · Free · Open Source</div><!-- x-release-please-version -->
   ```

   The `v` stays as a visual prefix; release-please replaces only the
   semver `0.6.23`. The annotated line contains exactly one semver, so the
   generic updater cannot match anything unintended. No other file is
   annotated.

## Acceptance criteria (from the issue)

| Criterion | How it is met |
|---|---|
| Releasing a new tag updates the badge automatically | The release-please release PR carries the `docs/index.html` bump |
| No manual `docs/index.html` edit per release | The generic updater rewrites the annotated line every release |
| Identical behavior on GitHub Pages (static from `main:/docs`) | Only HTML content changes; no build step, no JS |

## Verification

Config + HTML have no unit-test surface; verification is:

1. `jq . release-please-config.json` — valid JSON.
2. **Key check — dry-run with a real token**:
   `release-please release-pr --dry-run` against the repo. Since 0.6.23
   there are `fix` commits (#521/#522/#523), so it must propose 0.6.24 and
   list `docs/index.html` in the changeset with the badge bumped to the
   proposed version.
3. Definitive confirmation on the next real release PR.

## Out of scope

- Versioning anything else on the landing page.
- Changes to `release.yml` or the release pipeline.
