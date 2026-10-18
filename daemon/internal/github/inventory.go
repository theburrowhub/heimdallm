package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The PR inventory is every open PR the authenticated user is on the hook
// for — review requested (directly or through a team), authored, assigned —
// fetched in ONE GraphQL request instead of one REST search per qualifier
// plus one GET /pulls/N per result.
//
// GraphQL search resolves its nodes live: the search index only decides which
// PRs match, while headRefOid, reviewRequests, state and updatedAt come from
// the PR itself. That makes each node as fresh as a Pulls API hydration, so
// the stale-index guard (is the bot still requested?) keeps working on the
// node without a second request.

// Inventory qualifiers, also the batch keys of the three aliased searches.
const (
	qualifierReviewRequested = "review-requested"
	qualifierAuthor          = "author"
	qualifierAssignee        = "assignee"
)

// maxInventoryPages bounds pagination per qualifier. GitHub search stops at
// 1,000 results (10 × 100).
const maxInventoryPages = 10

// prSnapshotMaxEntries bounds the hydrated-PR cache.
const prSnapshotMaxEntries = 2000

// prFragment selects everything the review worker, Tier 3 and merge tracking
// read from a PR. Kept to scalars plus two small connections so a page of 100
// PRs costs about two GraphQL points.
const prFragment = `
fragment HeimdallmPR on PullRequest {
  databaseId number title url state isDraft updatedAt
  headRefOid headRefName baseRefOid baseRefName
  author { login }
  repository { nameWithOwner }
  headRepository { nameWithOwner }
  assignees(first: 20) { nodes { login } }
  reviewRequests(first: 20) {
    nodes {
      requestedReviewer {
        __typename
        ... on User { login }
        ... on Bot { login }
        ... on Mannequin { login }
        ... on Team { slug organization { login } }
      }
    }
  }
}
`

// gqlPR is the decoded HeimdallmPR fragment.
type gqlPR struct {
	DatabaseID  int64     `json:"databaseId"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	State       string    `json:"state"`
	IsDraft     bool      `json:"isDraft"`
	UpdatedAt   time.Time `json:"updatedAt"`
	HeadRefOid  string    `json:"headRefOid"`
	HeadRefName string    `json:"headRefName"`
	BaseRefOid  string    `json:"baseRefOid"`
	BaseRefName string    `json:"baseRefName"`
	Author      *struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Typename     string `json:"__typename"`
				Login        string `json:"login"`
				Slug         string `json:"slug"`
				Organization *struct {
					Login string `json:"login"`
				} `json:"organization"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
}

// toPullRequest maps the node onto the REST-shaped PullRequest the rest of
// the daemon consumes. Repo is the base repository (where the PR lives); the
// head repository is kept on Head.Repo only for fork-aware callers.
func (g *gqlPR) toPullRequest(fetchedAt time.Time) *PullRequest {
	if g == nil || g.Number == 0 || g.Repository.NameWithOwner == "" {
		return nil
	}
	state := strings.ToLower(g.State)
	if state == "merged" {
		// The Pulls API reports merged PRs as "closed"; keep that contract.
		state = "closed"
	}
	pr := &PullRequest{
		ID:            g.DatabaseID,
		Number:        g.Number,
		Title:         g.Title,
		HTMLURL:       g.URL,
		State:         state,
		Draft:         g.IsDraft,
		UpdatedAt:     g.UpdatedAt,
		Head:          Branch{SHA: g.HeadRefOid, Ref: g.HeadRefName},
		Base:          Branch{SHA: g.BaseRefOid, Ref: g.BaseRefName, Repo: Repo{FullName: g.Repository.NameWithOwner}},
		RepositoryURL: "https://api.github.com/repos/" + g.Repository.NameWithOwner,
		Repo:          g.Repository.NameWithOwner,
		HydratedAt:    fetchedAt,
	}
	if g.HeadRepository != nil {
		pr.HeadRepoFullName = g.HeadRepository.NameWithOwner
	}
	if g.Author != nil {
		pr.User = User{Login: g.Author.Login}
	}
	for _, a := range g.Assignees.Nodes {
		if a.Login != "" {
			pr.Assignees = append(pr.Assignees, User{Login: a.Login})
		}
	}
	pr.RequestedReviewers = []User{}
	for _, n := range g.ReviewRequests.Nodes {
		r := n.RequestedReviewer
		if r == nil {
			continue
		}
		if r.Typename == "Team" {
			org := ""
			if r.Organization != nil {
				org = r.Organization.Login
			}
			if r.Slug != "" {
				pr.RequestedTeams = append(pr.RequestedTeams, Team{Slug: r.Slug, Org: org})
			}
			continue
		}
		if r.Login != "" {
			pr.RequestedReviewers = append(pr.RequestedReviewers, User{Login: r.Login})
		}
	}
	return pr
}

// PRInventory is one snapshot of the user's open PRs, split by the qualifier
// that matched. A PR can appear under several qualifiers.
type PRInventory struct {
	FetchedAt       time.Time
	ReviewRequested []*PullRequest
	Authored        []*PullRequest
	Assigned        []*PullRequest
	// Failed names the qualifiers GitHub could not answer; callers fall back
	// to the REST search for those.
	Failed map[string]error
}

type inventoryState struct {
	mu   sync.Mutex
	last *PRInventory
	// reuse is how old a snapshot may be for callers that accept a shared one
	// (merge tracking discovery). Zero disables reuse.
	reuse time.Duration
}

type snapshotCache struct {
	mu      sync.Mutex
	entries map[string]*PullRequest
	// missing records PRs a batched lookup found deleted or inaccessible.
	missing map[string]time.Time
}

func prKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + fmt.Sprint(number)
}

// rememberPRs records hydrated PRs so a consumer within its freshness window
// (the review worker, Tier 3) can skip its own GET /pulls/N.
func (c *Client) rememberPRs(prs []*PullRequest) {
	c.snapshots.mu.Lock()
	defer c.snapshots.mu.Unlock()
	if c.snapshots.entries == nil {
		c.snapshots.entries = make(map[string]*PullRequest)
	}
	for _, pr := range prs {
		if pr == nil || pr.Repo == "" || pr.HydratedAt.IsZero() {
			continue
		}
		key := prKey(pr.Repo, pr.Number)
		if prev, ok := c.snapshots.entries[key]; ok && prev.HydratedAt.After(pr.HydratedAt) {
			continue
		}
		c.snapshots.entries[key] = pr
	}
	if len(c.snapshots.entries) <= prSnapshotMaxEntries {
		return
	}
	// Over the bound: drop everything older than the newest batch. Entries are
	// only useful for a few minutes, so this loses nothing worth keeping.
	var newest time.Time
	for _, pr := range c.snapshots.entries {
		if pr.HydratedAt.After(newest) {
			newest = pr.HydratedAt
		}
	}
	for k, pr := range c.snapshots.entries {
		if pr.HydratedAt.Before(newest) {
			delete(c.snapshots.entries, k)
		}
	}
}

// RecentPR returns a copy of the hydrated PR recorded within maxAge, if any.
func (c *Client) RecentPR(repo string, number int, maxAge time.Duration) (*PullRequest, bool) {
	if maxAge <= 0 {
		return nil, false
	}
	c.snapshots.mu.Lock()
	pr, ok := c.snapshots.entries[prKey(repo, number)]
	c.snapshots.mu.Unlock()
	if !ok || time.Since(pr.HydratedAt) > maxAge {
		return nil, false
	}
	cp := *pr
	cp.RequestedReviewers = append([]User(nil), pr.RequestedReviewers...)
	cp.RequestedTeams = append([]Team(nil), pr.RequestedTeams...)
	cp.Assignees = append([]User(nil), pr.Assignees...)
	return &cp, true
}

// GetPRFresh returns a hydrated PR no older than maxAge from the snapshot
// cache, or fetches it with GET /pulls/N.
func (c *Client) GetPRFresh(repo string, number int, maxAge time.Duration) (*PullRequest, error) {
	if pr, ok := c.RecentPR(repo, number, maxAge); ok {
		return pr, nil
	}
	return c.GetPR(repo, number)
}

// SetInventoryReuse sets how old a shared inventory snapshot may be for
// FetchMergeTrackingPRs. The review poll always refreshes; merge tracking
// reuses its snapshot instead of spending its own searches.
func (c *Client) SetInventoryReuse(d time.Duration) {
	c.inventory.mu.Lock()
	c.inventory.reuse = d
	c.inventory.mu.Unlock()
}

// FetchPRInventory returns the user's open PRs. A snapshot younger than
// maxAge is returned as is; otherwise all three searches run as aliases of a
// single GraphQL request (plus one follow-up per qualifier with more than 100
// results).
func (c *Client) FetchPRInventory(maxAge time.Duration) (*PRInventory, error) {
	c.inventory.mu.Lock()
	defer c.inventory.mu.Unlock()
	if last := c.inventory.last; last != nil && maxAge > 0 && time.Since(last.FetchedAt) <= maxAge {
		return last, nil
	}
	username, err := c.AuthenticatedUser()
	if err != nil {
		return nil, fmt.Errorf("github: resolve user: %w", err)
	}
	inv, err := c.fetchInventory(username)
	if err != nil {
		return nil, err
	}
	c.inventory.last = inv
	var all []*PullRequest
	all = append(all, inv.ReviewRequested...)
	all = append(all, inv.Authored...)
	all = append(all, inv.Assigned...)
	c.rememberPRs(all)
	return inv, nil
}

func inventorySearchField(qualifier, username string, cursor string) batchField {
	query := fmt.Sprintf("is:pr is:open %s:%s", qualifier, username)
	vars := []batchVar{{Name: "q", Type: "String!", Value: query}}
	after := ""
	if cursor != "" {
		vars = append(vars, batchVar{Name: "after", Type: "String", Value: cursor})
		after = ", after: $after"
	}
	return batchField{
		Key:  qualifier,
		Vars: vars,
		Body: `search(query: $q, type: ISSUE, first: 100` + after + `) {
    pageInfo { hasNextPage endCursor }
    nodes { ...HeimdallmPR }
  }`,
	}
}

type gqlSearchPage struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Nodes []*gqlPR `json:"nodes"`
}

func (c *Client) fetchInventory(username string) (*PRInventory, error) {
	now := time.Now()
	inv := &PRInventory{FetchedAt: now, Failed: map[string]error{}}
	byQualifier := map[string]*[]*PullRequest{
		qualifierReviewRequested: &inv.ReviewRequested,
		qualifierAuthor:          &inv.Authored,
		qualifierAssignee:        &inv.Assigned,
	}
	cursors := map[string]string{}
	pending := []string{qualifierReviewRequested, qualifierAuthor, qualifierAssignee}
	for page := 0; page < maxInventoryPages && len(pending) > 0; page++ {
		fields := make([]batchField, 0, len(pending))
		for _, q := range pending {
			fields = append(fields, inventorySearchField(q, username, cursors[q]))
		}
		results, failed, err := c.runBatch(graphQLOptions{}, prFragment, fields)
		if err != nil {
			return nil, fmt.Errorf("github: PR inventory: %w", err)
		}
		var next []string
		for _, q := range pending {
			if ferr, bad := failed[q]; bad {
				inv.Failed[q] = ferr
				*byQualifier[q] = nil
				continue
			}
			var sp gqlSearchPage
			if err := json.Unmarshal(results[q], &sp); err != nil {
				inv.Failed[q] = fmt.Errorf("github: PR inventory (%s): decode: %w", q, err)
				*byQualifier[q] = nil
				continue
			}
			for _, node := range sp.Nodes {
				if pr := node.toPullRequest(now); pr != nil && pr.State == "open" {
					*byQualifier[q] = append(*byQualifier[q], pr)
				}
			}
			if sp.PageInfo.HasNextPage && sp.PageInfo.EndCursor != "" {
				cursors[q] = sp.PageInfo.EndCursor
				next = append(next, q)
			}
		}
		pending = next
	}
	if len(inv.Failed) == 3 {
		return nil, fmt.Errorf("github: PR inventory: %w", errors.Join(
			inv.Failed[qualifierReviewRequested], inv.Failed[qualifierAuthor], inv.Failed[qualifierAssignee]))
	}
	slog.Info("github: PR inventory",
		"review_requested", len(inv.ReviewRequested), "authored", len(inv.Authored),
		"assigned", len(inv.Assigned), "failed", len(inv.Failed))
	return inv, nil
}

// prsForQualifier returns the PRs for one qualifier from inv, falling back to
// the REST search when the inventory is unavailable (invErr) or that
// qualifier failed. The fallback keeps PR discovery working while GraphQL is
// rate limited but REST search still has budget.
func (c *Client) prsForQualifier(inv *PRInventory, invErr error, username, qualifier string) ([]*PullRequest, error) {
	err := invErr
	if err == nil {
		if _, failed := inv.Failed[qualifier]; !failed {
			switch qualifier {
			case qualifierReviewRequested:
				return inv.ReviewRequested, nil
			case qualifierAuthor:
				return inv.Authored, nil
			default:
				return inv.Assigned, nil
			}
		}
		err = inv.Failed[qualifier]
	}
	slog.Warn("github: PR inventory unavailable, falling back to REST search",
		"qualifier", qualifier, "err", err)
	return c.fetchByQualifier(username, qualifier, nil)
}

func (c *Client) inventoryReuse() time.Duration {
	c.inventory.mu.Lock()
	defer c.inventory.mu.Unlock()
	return c.inventory.reuse
}

// PRRef names one PR for a batched lookup.
type PRRef struct {
	Repo   string
	Number int
}

// PrefetchPRs reads every PR in refs live in batched GraphQL queries (50 per
// request by default) and records them in the snapshot cache, so the Tier 3
// state checks that follow are answered without one GET /pulls/N each. PRs
// GitHub reports as missing are recorded too, so their check removes them
// from the watch list. Errors are only logged: a PR without a fresh entry is
// fetched individually by its consumer, exactly as before.
func (c *Client) PrefetchPRs(refs []PRRef) {
	if len(refs) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(refs))
	fields := make([]batchField, 0, len(refs))
	for _, ref := range refs {
		owner, name, err := splitRepo(ref.Repo)
		if err != nil || ref.Number <= 0 {
			continue
		}
		key := prKey(ref.Repo, ref.Number)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		fields = append(fields, batchField{
			Key: key,
			Vars: []batchVar{
				{Name: "owner", Type: "String!", Value: owner},
				{Name: "name", Type: "String!", Value: name},
				{Name: "number", Type: "Int!", Value: ref.Number},
			},
			Body: `repository(owner: $owner, name: $name) { pullRequest(number: $number) { ...HeimdallmPR } }`,
		})
	}
	now := time.Now()
	results, failed, err := c.runBatch(graphQLOptions{}, prFragment, fields)
	if err != nil {
		slog.Warn("github: batched PR lookup failed, falling back to per-PR requests", "prs", len(fields), "err", err)
	}
	var prs []*PullRequest
	var missing []string
	for _, f := range fields {
		if ferr, bad := failed[f.Key]; bad {
			var be *BatchItemError
			if asBatchItemError(ferr, &be) && be.NotFound() {
				missing = append(missing, f.Key)
			}
			continue
		}
		raw, ok := results[f.Key]
		if !ok {
			continue
		}
		var repo struct {
			PullRequest *gqlPR `json:"pullRequest"`
		}
		if string(raw) == "null" {
			missing = append(missing, f.Key)
			continue
		}
		if json.Unmarshal(raw, &repo) != nil {
			continue
		}
		if repo.PullRequest == nil {
			missing = append(missing, f.Key)
			continue
		}
		if pr := repo.PullRequest.toPullRequest(now); pr != nil {
			prs = append(prs, pr)
		}
	}
	c.rememberPRs(prs)
	c.rememberMissing(missing, now)
	slog.Debug("github: batched PR lookup", "requested", len(fields), "found", len(prs), "missing", len(missing))
}

func (c *Client) rememberMissing(keys []string, at time.Time) {
	if len(keys) == 0 {
		return
	}
	c.snapshots.mu.Lock()
	defer c.snapshots.mu.Unlock()
	if c.snapshots.missing == nil {
		c.snapshots.missing = make(map[string]time.Time)
	}
	for _, k := range keys {
		c.snapshots.missing[k] = at
		delete(c.snapshots.entries, k)
	}
	for k, t := range c.snapshots.missing {
		if at.Sub(t) > time.Hour {
			delete(c.snapshots.missing, k)
		}
	}
}

// RecentPRSnapshot returns the Tier 3 view of a PR recorded within maxAge.
// A PR the batch found missing is reported as a 404 APIError, which the state
// worker treats as "stop watching".
func (c *Client) RecentPRSnapshot(repo string, number int, maxAge time.Duration) (*PRSnapshot, bool, error) {
	if maxAge <= 0 {
		return nil, false, nil
	}
	key := prKey(repo, number)
	c.snapshots.mu.Lock()
	missingAt, gone := c.snapshots.missing[key]
	c.snapshots.mu.Unlock()
	if gone && time.Since(missingAt) <= maxAge {
		return nil, true, &APIError{StatusCode: http.StatusNotFound, Body: "pull request not found"}
	}
	pr, ok := c.RecentPR(repo, number, maxAge)
	if !ok {
		return nil, false, nil
	}
	return snapshotFromPR(pr), true, nil
}

// GetPRSnapshotFresh is GetPRSnapshot served from the snapshot cache when a
// batched lookup read the PR within maxAge.
func (c *Client) GetPRSnapshotFresh(repo string, number int, maxAge time.Duration) (*PRSnapshot, error) {
	if snap, ok, err := c.RecentPRSnapshot(repo, number, maxAge); ok {
		return snap, err
	}
	return c.GetPRSnapshot(repo, number)
}

func snapshotFromPR(pr *PullRequest) *PRSnapshot {
	reviewers := make([]string, len(pr.RequestedReviewers))
	for i, u := range pr.RequestedReviewers {
		reviewers[i] = u.Login
	}
	return &PRSnapshot{
		State:              pr.State,
		Draft:              pr.Draft,
		Author:             pr.User.Login,
		UpdatedAt:          pr.UpdatedAt,
		HeadSHA:            pr.Head.SHA,
		RequestedReviewers: reviewers,
		RequestedTeams:     append([]Team(nil), pr.RequestedTeams...),
	}
}
