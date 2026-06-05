# Large-PR diff fallback via List Pull Request Files (#506)

**Date:** 2026-06-05
**Issue:** [#506 — Gestionar PRs mastodónticas](https://github.com/theburrowhub/heimdallm/issues/506)
**Status:** Approved

## Problem

When a PR touches more than 300 files, GitHub's diff endpoint
(`GET /repos/{repo}/pulls/{n}` with `Accept: application/vnd.github.v3.diff`)
returns `406 Not Acceptable`:

```
Review failed: pipeline: fetch diff: github: fetch diff: status 406:
{"message":"Sorry, the diff exceeded the maximum number of files (300).
Consider using 'List pull requests files' API or locally cloning the repository instead."}
```

`pipeline.Run` aborts at the fetch-diff step (`pipeline.go`), so Heimdallm
never reviews these PRs at all. Example: freepik-company/ai-bumblebee-proxy#1147.

## Context that shapes the design

- The diff has exactly one consumer: `PRContext.Diff`, embedded in the review
  prompt. Nothing else parses it.
- `executor.BuildPromptFromTemplate` already truncates the diff at 32 KB
  (`maxDiffBytes`), so the prompt never sees more than that regardless of
  how complete the fetched diff is.
- The review agent *usually* runs with `WorkDir` set to a local worktree of
  the repo (acquired via `repoctx`, `ModeRead`) — but repoctx is best-effort:
  on acquire failure `main.go` clears `aiCfg.LocalDir` and the review still
  runs without a checkout. `FetchDiff(repo, number)` cannot know which case
  applies, so the reconstructed diff must NOT claim a checkout exists.

These three facts make the diff *advisory, lossy-by-design context* — unlike
PR comments (#516/#518) or timeline events (#519), which feed correctness
decisions and therefore fail hard on truncation.

## Decision

**Option A from the issue: fall back to the List Pull Request Files API.**
Rejected alternatives: local `git diff` in the worktree (more plumbing across
layers; the review worktree is a shallow `ModeRead` clone without guaranteed
full history) and prompt-note-only (review quality would depend entirely on
the agent exploring on its own).

## Design

All changes live in `daemon/internal/github/client.go`. `FetchDiff` keeps its
signature; the `pipeline.DiffFetcher` interface and all callers are untouched.

```
FetchDiff(repo, number)
  ├─ GET /pulls/N (Accept: diff) → 200: return diff      (existing path, unchanged)
  ├─ 406                          → fetchDiffViaFilesAPI (new fallback)
  └─ any other non-200            → error                (existing path, unchanged)
```

Any 406 triggers the fallback (GitHub uses it for "diff too large" on this
endpoint); there is no message sniffing.

### fetchDiffViaFilesAPI

- Paginates `GET /repos/{repo}/pulls/{n}/files?per_page=100&page=K` reusing
  the shared pagination constants (`perPage`, `maxPaginationPages`) from
  #519/#518.
- **Dedicated page-body ceiling** `maxFilesPageBytes = 2 × maxDiffBodyBytes`
  (20 MiB). The generic `maxPaginatedPageBytes` (5 MiB) is NOT reused: files
  pages embed per-file `patch` strings, so a 100-file page can plausibly
  exceed 5 MiB even when the final reconstruction would truncate safely at
  10 MiB — a truncated JSON page would fail to decode and abort the review,
  the exact failure mode this design removes. 20 MiB is generous because
  GitHub omits the `patch` field entirely for very large file diffs, which
  bounds realistic page sizes.
- Emits, per file, unified-diff-shaped headers followed by the `patch` field:
  - `diff --git a/<previous_filename|filename> b/<filename>`
  - `--- a/<old>` / `+++ b/<new>`, with `/dev/null` for added/removed files
  - renames use `previous_filename` on the `a/` side
- Files without `patch` (binary or oversized): a stub line
  `(patch unavailable — +<additions>/-<deletions>)`.
- The reconstructed diff starts with a neutral comment line: the diff was
  rebuilt via the List Pull Request Files API because the PR exceeds
  GitHub's 300-file diff limit, and may be incomplete. It makes no claim
  about a local checkout (the review path runs with or without one, and
  the client cannot tell which).

### Limits and error handling

| Condition | Behavior |
|---|---|
| Appending a file would push the reconstruction over `maxDiffBodyBytes` (10 MB) | skip it, stop appending and paginating, add a generic truncation note (no exact omitted count — we stop paginating rather than walk the rest just to count), `slog.Warn`. Checked on the projected size so the result never exceeds the ceiling, even for a single oversized patch |
| ≥3,000 files read (GitHub's documented hard cap on this endpoint) | append a note that GitHub caps file listings at 3,000 files and some files may be missing, `slog.Warn`. Conservative: a PR with exactly 3,000 files gets the note too — the client cannot distinguish "exactly at cap" from "capped" without extra API calls (`changed_files` is not parsed in our `PullRequest` model), and a spurious note on an at-cap PR is harmless |
| Pagination cap hit (50 pages) | return the partial diff with a truncation note, `slog.Warn` — deliberately NOT an error (see below) |
| HTTP error mid-pagination | propagate as error |
| Decode error with the page read at the `maxFilesPageBytes` ceiling | self-inflicted truncation: return the diff accumulated so far with a truncation note, `slog.Warn` with bytes read — NOT an error (same advisory-context rationale as the cap) |
| Decode error below the ceiling | genuine upstream corruption: propagate as error, including `N bytes read` (pattern from #518) |

**Why cap-hit ≠ error here (unlike #519):** the diff is advisory context that
the prompt truncates to 32 KB anyway; failing hard would re-create the exact
problem this issue fixes (review aborts on big PRs). Comments and timeline
events drive dedup/re-review correctness decisions, which is why those
fetchers fail closed. This rationale is documented inline in the code.
GitHub also hard-caps this endpoint at 3,000 files (30 pages), so the
50-page cap is unreachable in practice.

## Testing (TDD)

1. `TestFetchDiff_FallsBackToFilesAPIOn406` — diff endpoint returns 406; files
   endpoint serves 2 pages (full + short). Asserts: reconstructed diff
   contains per-file headers, patch bodies, rename handling, and the leading
   agent note; files endpoint hit exactly twice.
2. `TestFetchDiff_FilesAPIStubsMissingPatch` — a file without `patch` yields
   the stub line instead of dropping the file silently.
3. `TestFetchDiff_NonDiffErrorsStillPropagate` — 404/500 from the diff
   endpoint do NOT trigger the fallback and surface as errors (existing
   contract).
4. `TestFetchDiff_FilesAPITruncatesAtMaxDiffBodyBytes` — accumulated
   reconstruction crossing 10 MB stops appending and carries the
   `diff truncated, N files omitted` note.
5. `TestFetchDiff_FilesAPIWarnsAtGitHubFileCap` — reading 3,000 files
   appends the GitHub-cap note.
6. `TestFetchDiff_FilesAPIHandlesLargePages` — a single files page >5 MiB
   (over the generic paginated ceiling, under `maxFilesPageBytes`) decodes
   and reconstructs successfully.
7. `TestFetchDiff_FilesAPIPageAtCeilingDegradesGracefully` — a page
   truncated at `maxFilesPageBytes` returns the diff accumulated so far
   with a truncation note instead of an error.
8. Existing `TestFetchDiff*` tests pass unchanged (200 path untouched).

## Out of scope

- Recovering file content beyond GitHub's 3,000-file files-API cap (the
  fallback returns the capped diff plus the warning note defined above;
  fetching the remainder would require cloning, the rejected alternative).
- Local `git diff` computation (rejected alternative; can be revisited if
  the files API proves insufficient).
- Changes to the prompt template or the 32 KB prompt truncation.
