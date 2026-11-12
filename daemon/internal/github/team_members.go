package github

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Team lookups for arbitrary logins (never_approve_exempt_authors accepts
// "org/team" entries) and the org listing behind the GUI's autocomplete of
// that list. teamCache in teams.go is a different thing: one user's own teams.

const (
	// memberCacheTTL bounds how stale a membership answer may be, so a person
	// added to or removed from a team takes effect within the hour.
	memberCacheTTL = time.Hour
	// memberCacheFailTTL retries a failed lookup sooner.
	memberCacheFailTTL = 5 * time.Minute
	// principalsCacheTTL: the listing only feeds autocomplete suggestions.
	principalsCacheTTL = time.Hour
	// maxPrincipalPages caps each org listing at 2000 teams and 2000 members.
	maxPrincipalPages = 20
)

type memberEntry struct {
	member    bool
	err       error
	fetchedAt time.Time
}

type memberCache struct {
	mu sync.Mutex
	m  map[string]memberEntry // key: lowercased org/team/login
}

// Principal is an autocomplete suggestion for never_approve_exempt_authors:
// a GitHub login (Kind "user") or an org team written "org/slug" (Kind "team").
type Principal struct {
	Value string `json:"value"`
	Kind  string `json:"kind"`
}

type principalsEntry struct {
	list      []Principal
	err       error
	fetchedAt time.Time
}

type principalsCache struct {
	mu   sync.Mutex
	orgs map[string]*principalsSlot // key: lowercased org
}

// principalsSlot serializes one org's fetch: concurrent cold callers for the
// same org make one round of requests, and other orgs never wait on it.
type principalsSlot struct {
	mu    sync.Mutex
	entry principalsEntry
	set   bool
}

// IsTeamMember reports whether login is an active member of org/team (a
// pending invitation does not count). Answers are cached per org/team/login.
func (c *Client) IsTeamMember(org, team, login string) (bool, error) {
	key := strings.ToLower(org + "/" + team + "/" + login)
	c.members.mu.Lock()
	if e, ok := c.members.m[key]; ok {
		ttl := memberCacheTTL
		if e.err != nil {
			ttl = memberCacheFailTTL
		}
		if time.Since(e.fetchedAt) < ttl {
			c.members.mu.Unlock()
			return e.member, e.err
		}
	}
	c.members.mu.Unlock()

	member, err := c.fetchTeamMembership(org, team, login)
	now := time.Now()
	c.members.mu.Lock()
	if c.members.m == nil {
		c.members.m = make(map[string]memberEntry)
	}
	// Drop expired answers so the map tracks recent authors, not every author
	// the daemon has ever seen. Writes happen at most hourly per key.
	for k, e := range c.members.m {
		if now.Sub(e.fetchedAt) >= memberCacheTTL {
			delete(c.members.m, k)
		}
	}
	c.members.m[key] = memberEntry{member: member, err: err, fetchedAt: now}
	c.members.mu.Unlock()
	return member, err
}

func (c *Client) fetchTeamMembership(org, team, login string) (bool, error) {
	path := fmt.Sprintf("/orgs/%s/teams/%s/memberships/%s",
		url.PathEscape(org), url.PathEscape(team), url.PathEscape(login))
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return false, fmt.Errorf("github: team membership: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))

	switch resp.StatusCode {
	case http.StatusOK:
		var m struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return false, fmt.Errorf("github: decode team membership: %w", err)
		}
		return m.State == "active", nil
	case http.StatusNotFound:
		// GitHub answers 404 both for "not a member" and for a team that does
		// not exist or that the token cannot see (no read:org). Tell the
		// latter apart so a typo or a missing scope shows up in the log.
		if !c.teamVisible(org, team) {
			slog.Warn("github: team in never_approve_exempt_authors not found or not readable (token needs read:org)",
				"team", org+"/"+team)
		}
		return false, nil
	default:
		return false, fmt.Errorf("github: team membership: status %d: %s",
			resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
}

func (c *Client) teamVisible(org, team string) bool {
	resp, err := c.do("GET", fmt.Sprintf("/orgs/%s/teams/%s", url.PathEscape(org), url.PathEscape(team)),
		"application/vnd.github+json")
	if err != nil {
		return true // unknown: do not claim the team is missing
	}
	resp.Body.Close()
	if _, limited := rateLimitDelay(resp); limited {
		return true
	}
	return resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden
}

// OrgPrincipals lists org's teams (as "org/slug") and members, sorted, cached
// for an hour. An org the token cannot list (a personal account, or no
// read:org) yields an empty list, not an error.
func (c *Client) OrgPrincipals(org string) ([]Principal, error) {
	key := strings.ToLower(org)
	c.principals.mu.Lock()
	if c.principals.orgs == nil {
		c.principals.orgs = make(map[string]*principalsSlot)
	}
	slot, ok := c.principals.orgs[key]
	if !ok {
		slot = &principalsSlot{}
		c.principals.orgs[key] = slot
	}
	c.principals.mu.Unlock()

	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.set {
		ttl := principalsCacheTTL
		if slot.entry.err != nil {
			ttl = memberCacheFailTTL
		}
		if time.Since(slot.entry.fetchedAt) < ttl {
			return slot.entry.list, slot.entry.err
		}
	}
	list, err := c.fetchOrgPrincipals(org)
	slot.entry = principalsEntry{list: list, err: err, fetchedAt: time.Now()}
	slot.set = true
	return list, err
}

func (c *Client) fetchOrgPrincipals(org string) ([]Principal, error) {
	base := "/orgs/" + url.PathEscape(org)
	teams, err := c.listOrgNames(base+"/teams", func(it orgListItem) string { return it.Slug })
	if err != nil {
		return nil, err
	}
	members, err := c.listOrgNames(base+"/members", func(it orgListItem) string { return it.Login })
	if err != nil {
		return nil, err
	}
	out := make([]Principal, 0, len(teams)+len(members))
	for _, slug := range teams {
		out = append(out, Principal{Value: org + "/" + slug, Kind: "team"})
	}
	for _, login := range members {
		out = append(out, Principal{Value: login, Kind: "user"})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Value) < strings.ToLower(out[j].Value) })
	return out, nil
}

type orgListItem struct {
	Login string `json:"login"`
	Slug  string `json:"slug"`
}

// listOrgNames pages through an org listing (100 per page, at most
// maxPrincipalPages). 404, or a 403 that is not a rate limit, means the org is
// not listable: empty, no error. A rate-limited 403 is an error, so it gets
// the short failure TTL instead of an hour of empty suggestions.
func (c *Client) listOrgNames(path string, name func(orgListItem) string) ([]string, error) {
	var out []string
	for page := 1; page <= maxPrincipalPages; page++ {
		resp, err := c.do("GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: list %s: %w", path, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		_, limited := rateLimitDelay(resp)
		if resp.StatusCode == http.StatusNotFound || (resp.StatusCode == http.StatusForbidden && !limited) {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("github: list %s: status %d: %s", path, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
		}
		var items []orgListItem
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("github: decode %s: %w", path, err)
		}
		for _, it := range items {
			if n := name(it); n != "" {
				out = append(out, n)
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}
