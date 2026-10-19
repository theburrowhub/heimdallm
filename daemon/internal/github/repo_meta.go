package github

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Repository metadata (canonical name, archived) feeds two loops that used to
// spend one GET /repos/{r} per repository each: the archive check of Tier 1
// discovery and the hourly rename probe. Both now prefetch every repository in
// batched GraphQL queries and read the answers from here.

// repoMetaMaxAge is how long a prefetched answer serves IsRepoArchived and
// GetCanonicalFullName. Consumers prefetch right before they read, so this
// only has to cover one pass over their repositories.
const repoMetaMaxAge = 10 * time.Minute

type repoMeta struct {
	canonical string
	archived  bool
	fetchedAt time.Time
}

type repoMetaCache struct {
	mu      sync.Mutex
	entries map[string]repoMeta // key: lowercased owner/name as asked
}

// PrefetchRepoMeta reads the canonical name and archived flag of every repo in
// batched queries. Repositories GraphQL cannot resolve (deleted, renamed in a
// way GraphQL does not follow, inaccessible) are left out, so their consumer
// falls back to the REST lookup — which follows rename redirects — exactly as
// before. Errors are only logged for the same reason.
func (c *Client) PrefetchRepoMeta(repos []string) {
	seen := make(map[string]struct{}, len(repos))
	fields := make([]batchField, 0, len(repos))
	for _, repo := range repos {
		owner, name, err := splitRepo(repo)
		if err != nil {
			continue
		}
		key := strings.ToLower(repo)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		fields = append(fields, batchField{
			Key: key,
			Vars: []batchVar{
				{Name: "owner", Type: "String!", Value: owner},
				{Name: "name", Type: "String!", Value: name},
			},
			Body: `repository(owner: $owner, name: $name) { nameWithOwner isArchived }`,
		})
	}
	if len(fields) == 0 {
		return
	}
	now := time.Now()
	results, _, err := c.runBatch(graphQLOptions{}, "", fields)
	if err != nil {
		slog.Warn("github: batched repository lookup failed, falling back to per-repo requests", "repos", len(fields), "err", err)
	}
	c.repoMeta.mu.Lock()
	defer c.repoMeta.mu.Unlock()
	if c.repoMeta.entries == nil {
		c.repoMeta.entries = make(map[string]repoMeta)
	}
	for _, f := range fields {
		raw, ok := results[f.Key]
		if !ok || string(raw) == "null" {
			continue
		}
		var r struct {
			NameWithOwner string `json:"nameWithOwner"`
			IsArchived    bool   `json:"isArchived"`
		}
		if json.Unmarshal(raw, &r) != nil || r.NameWithOwner == "" {
			continue
		}
		c.repoMeta.entries[f.Key] = repoMeta{
			canonical: r.NameWithOwner,
			archived:  r.IsArchived,
			fetchedAt: now,
		}
	}
}

func (c *Client) cachedRepoMeta(repo string) (repoMeta, bool) {
	c.repoMeta.mu.Lock()
	defer c.repoMeta.mu.Unlock()
	m, ok := c.repoMeta.entries[strings.ToLower(repo)]
	if !ok || time.Since(m.fetchedAt) > repoMetaMaxAge {
		return repoMeta{}, false
	}
	return m, true
}
