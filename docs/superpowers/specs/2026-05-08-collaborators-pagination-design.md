# Paginate `FetchCollaborators` against the GitHub API

**Date:** 2026-05-08
**Branch:** `fix/collaborators-pagination`
**Scope:** Bug fix in `daemon/internal/github/client.go`

## Problem

The Issue Triage assignee combobox in `flutter_app` lets the user pick a GitHub
login as the issue assignee. Its options come from a merge of two sources:

1. `_repoCollaborators` — fetched from `GET /api/repos/{repo}/collaborators`,
   which is served by the daemon's `Client.FetchCollaborators` against
   `GET /repos/{owner}/{repo}/collaborators` on GitHub.
2. `appConfig.knownGitHubUsers` — logins already saved in user config
   (existing PR reviewers, prior assignees, etc.).

For repositories in the `freepik-company` organization, several legitimate
collaborators (e.g. `vbuenog`, `dieloren`) do not appear in the combobox even
though they are members of the org and have repo access via team grants.

### Root cause

`Client.FetchCollaborators` (`daemon/internal/github/client.go:947`) issues a
single request to `?per_page=100` and returns whatever fits on page 1. There is
no pagination loop. For the representative repo `freepik-company/ai-platform-iqs-v3`
the full collaborator list contains 253 users, so 153 of them — including
`vbuenog` and `dieloren` — are silently dropped.

Verified with the `gh` CLI:
- `gh api repos/freepik-company/ai-platform-iqs-v3/collaborators?per_page=100`
  returns exactly 100 logins; `vbuenog` and `dieloren` are absent.
- The same request with `--paginate` returns 253 logins, with both users present.
- `gh api orgs/freepik-company/members/vbuenog` and `…/dieloren` both return
  `HTTP 204` (confirmed members).

## Goal

Return the complete list of collaborators from `Client.FetchCollaborators`,
regardless of how many pages GitHub splits the response into.

## Non-goals

- Caching collaborator lists. Today's behavior fetches on demand; that doesn't
  change here.
- Replacing the data source with `/orgs/{org}/members` (a previously discussed
  alternative — option B). Out of scope; the current "repo collaborators"
  semantics are kept.
- Auditing other GitHub list endpoints in the daemon for the same bug. Only
  `FetchCollaborators` is changed.
- Any UI changes in `flutter_app`. The combobox already merges and dedupes the
  list it receives.

## Design

### Pagination strategy

GitHub returns a `Link` header on paginated responses with a
`<url>; rel="next"` segment when there is another page. We follow that header
until it is absent. This is GitHub's documented mechanism and is robust to
filtered responses where a page may be shorter than `per_page` even though more
data follows.

Pseudocode:

```
url := "/repos/{repo}/collaborators?per_page=100"
for page := 0; page < maxPages; page++ {
    resp := c.do("GET", url, ...)
    // status check, decode, append logins
    next := parseNextLink(resp.Header.Get("Link"))
    if next == "" {
        return logins, nil
    }
    url = next
}
return nil, fmt.Errorf("github: collaborators pagination exceeded %d pages", maxPages)
```

The `next` URL returned by GitHub is absolute (e.g.
`https://api.github.com/repositories/.../collaborators?per_page=100&page=2`).
`Client.do` already accepts a path; we will need it to also accept absolute
URLs, or we strip the API base prefix before passing it. The simpler change:
extract just the path + query from the absolute `next` URL and pass that to
`c.do`. This keeps `c.do`'s contract unchanged.

### Safety bound

`maxPages` is set to **100** (i.e. up to 10,000 collaborators). A repo with
more collaborators than that is implausible; if we ever hit the cap, returning
an error is preferable to silently truncating — that is exactly the failure
mode we are removing.

### Error handling

- HTTP non-200 on any page → return error, same as today.
- `json.Unmarshal` failure on any page → return error.
- Malformed or absent `Link` header on a non-final page → treated as "no next",
  pagination stops. This matches GitHub's contract: absence of `rel="next"`
  means no more pages.
- `maxPages` exceeded → return error (`github: collaborators pagination exceeded N pages`).

### `Link` header parsing

A small helper, `parseNextLink(header string) string`, returns the URL whose
`rel` parameter equals `next`, or `""` if none. Uses standard string parsing
— no new dependency. Lives in the same file (`client.go`) as a private
function. Format reminder:

```
Link: <https://api.github.com/...&page=2>; rel="next", <https://api.github.com/...&page=4>; rel="last"
```

## Testing

All tests live in `daemon/internal/github/client_test.go` (or a new file in the
same package if that one doesn't exist) and use `httptest.NewServer` to stub
GitHub.

1. **Single page, no `Link` header.** Server returns one page of N<100 users
   with no `Link`. Assert the function returns exactly those logins. (Covers
   the small-repo path that already worked.)
2. **Multi-page traversal.** Server is configured with three pages: pages 1
   and 2 carry `Link: <…page=N+1>; rel="next"`, page 3 has no `Link`. Assert
   all logins from all pages are returned in order.
3. **Runaway cap.** Server always returns a `Link: rel="next"` pointing back
   to itself. Assert the function returns an error mentioning "pagination" and
   does not loop forever (test passes within a sane time budget).
4. **HTTP error mid-pagination.** Page 1 returns 200 with `rel="next"`, page 2
   returns 500. Assert the function returns an error and does not return a
   partial slice.
5. **`parseNextLink` unit cases.** Header with both `next` and `last`,
   header with only `last`, empty header, malformed header. Confirm correct URL
   extracted (or empty string).

No integration tests against real GitHub — keeps the suite hermetic, matches
the existing pattern in this package.

## Risk and rollback

- The change is additive in behavior: small repos still work the same way (one
  request, no `Link` header → loop exits after page 1). Risk is low.
- If a malformed `Link` header from GitHub somehow caused infinite loops, the
  `maxPages` cap converts that into a loud error rather than a hang.
- Rollback is a single-file revert of `daemon/internal/github/client.go`.

## Out of scope but worth noting

- The Flutter combobox does not surface fetch errors clearly — if
  `FetchCollaborators` fails, the user only sees `knownGitHubUsers`. That is a
  pre-existing behavior, unchanged here.
- The same pagination pattern likely applies to other GitHub list calls in
  this client (e.g. issue list, label list). Tracked separately; not addressed
  in this change to keep the diff focused.
