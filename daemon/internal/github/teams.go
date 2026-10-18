package github

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Team review requests: `review-requested:<login>` search matches PRs whose
// review was requested from the user OR from a team the user belongs to.
// Every "is the user still requested?" guard therefore has to accept a pending
// request to one of the user's teams too, which needs the memberships.

const (
	// teamCacheTTL is how long a successful membership lookup is trusted.
	// Team membership changes rarely; a day keeps this at one query per day.
	teamCacheTTL = 24 * time.Hour
	// teamCacheFailTTL retries a failed lookup sooner, so a transient error
	// does not hide team requests for a whole day.
	teamCacheFailTTL = 5 * time.Minute
)

type teamEntry struct {
	slugs     map[string]struct{} // lowercased
	fetchedAt time.Time
	ttl       time.Duration
}

type teamCache struct {
	mu   sync.Mutex
	orgs map[string]teamEntry // key: lowercased org login
}

// ReviewerIdentity is who counts as "requested": the login itself, or any of
// its teams in the repository's organization.
type ReviewerIdentity struct {
	Login string
	// Teams are the user's team slugs in the repository owner's organization,
	// lowercased.
	Teams map[string]struct{}
	// Org is the repository owner the Teams belong to.
	Org string
}

// RequestedFor reports whether the identity has a pending review request on
// pr — directly or through one of its teams.
func (pr *PullRequest) RequestedFor(id ReviewerIdentity) bool {
	if pr == nil {
		return false
	}
	if pr.ReviewRequestedFor(id.Login) {
		return true
	}
	return teamRequested(pr.RequestedTeams, id)
}

// RequestedFor is the PRSnapshot counterpart of PullRequest.RequestedFor.
func (s *PRSnapshot) RequestedFor(id ReviewerIdentity) bool {
	if s == nil {
		return false
	}
	return s.ReviewRequestedFor(id.Login) || teamRequested(s.RequestedTeams, id)
}

// RequestedFor is the PRHeadInfo counterpart of PullRequest.RequestedFor.
func (info PRHeadInfo) RequestedFor(id ReviewerIdentity) bool {
	return info.ReviewRequestedFor(id.Login) || teamRequested(info.RequestedTeams, id)
}

func teamRequested(teams []Team, id ReviewerIdentity) bool {
	if len(id.Teams) == 0 {
		return false
	}
	for _, t := range teams {
		// A team request without an org (Pulls API) belongs to the repository
		// owner, which is the org the identity's teams were resolved for.
		if t.Org != "" && id.Org != "" && !strings.EqualFold(t.Org, id.Org) {
			continue
		}
		if _, ok := id.Teams[strings.ToLower(t.Slug)]; ok {
			return true
		}
	}
	return false
}

// ReviewerIdentityFor returns login's identity for repo: the login plus its
// teams in the repository owner's organization (looked up once and cached).
func (c *Client) ReviewerIdentityFor(login, repo string) ReviewerIdentity {
	id := ReviewerIdentity{Login: login}
	owner, _, err := splitRepo(repo)
	if err != nil || login == "" {
		return id
	}
	id.Org = owner
	c.PrefetchUserTeams(login, []string{owner})
	key := strings.ToLower(owner)
	c.teams.mu.Lock()
	if e, ok := c.teams.orgs[key]; ok {
		id.Teams = e.slugs
	}
	c.teams.mu.Unlock()
	return id
}

// PrefetchUserTeams resolves login's team memberships for every org whose
// cached entry is missing or expired, all orgs in one batched query.
// Organizations the token cannot read (missing read:org, or a personal
// account) resolve to no teams.
func (c *Client) PrefetchUserTeams(login string, orgs []string) {
	if login == "" {
		return
	}
	now := time.Now()
	seen := map[string]struct{}{}
	var fields []batchField
	c.teams.mu.Lock()
	if c.teams.orgs == nil {
		c.teams.orgs = make(map[string]teamEntry)
	}
	for _, org := range orgs {
		key := strings.ToLower(strings.TrimSpace(org))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if e, ok := c.teams.orgs[key]; ok && now.Sub(e.fetchedAt) < e.ttl {
			continue
		}
		fields = append(fields, batchField{
			Key: key,
			Vars: []batchVar{
				{Name: "org", Type: "String!", Value: org},
				{Name: "user", Type: "String!", Value: login},
			},
			Body: `organization(login: $org) { teams(first: 100, userLogins: [$user]) { nodes { slug } } }`,
		})
	}
	c.teams.mu.Unlock()
	if len(fields) == 0 {
		return
	}

	results, failed, err := c.runBatch(graphQLOptions{}, "", fields)
	c.teams.mu.Lock()
	defer c.teams.mu.Unlock()
	for _, f := range fields {
		entry := teamEntry{slugs: map[string]struct{}{}, fetchedAt: now, ttl: teamCacheTTL}
		raw, ok := results[f.Key]
		switch {
		case err != nil:
			entry.ttl = teamCacheFailTTL
		case !ok:
			// A NOT_FOUND/FORBIDDEN organization is a definitive "no teams"
			// (personal account, or no read:org); anything else retries soon.
			var be *BatchItemError
			if fe, bad := failed[f.Key]; bad && !(asBatchItemError(fe, &be) && be.forbiddenOrNotFound()) {
				entry.ttl = teamCacheFailTTL
			}
		default:
			var org struct {
				Teams struct {
					Nodes []struct {
						Slug string `json:"slug"`
					} `json:"nodes"`
				} `json:"teams"`
			}
			if string(raw) != "null" {
				if jerr := json.Unmarshal(raw, &org); jerr != nil {
					entry.ttl = teamCacheFailTTL
				}
			}
			for _, n := range org.Teams.Nodes {
				entry.slugs[strings.ToLower(n.Slug)] = struct{}{}
			}
		}
		c.teams.orgs[f.Key] = entry
	}
	if err != nil {
		slog.Warn("github: team membership lookup failed, team review requests ignored for now", "err", err)
	}
}

func asBatchItemError(err error, target **BatchItemError) bool {
	be, ok := err.(*BatchItemError)
	if ok {
		*target = be
	}
	return ok
}

func (e *BatchItemError) forbiddenOrNotFound() bool {
	if e == nil || len(e.Types) == 0 {
		return false
	}
	for _, t := range e.Types {
		if t != "NOT_FOUND" && t != "FORBIDDEN" {
			return false
		}
	}
	return true
}
