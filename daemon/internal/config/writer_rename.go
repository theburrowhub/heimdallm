package config

import (
	"strings"
)

// RenameRepoInTOML rewrites the config TOML at `path` to move every
// reference to `oldRepo` over to `newRepo`. It is the persistence
// half of the #489 rename pipeline — the in-memory config is updated
// separately by the reconciler under cfgMu.
//
// Surfaces it touches:
//   - `[github] repositories = [...]` and `non_monitored = [...]`
//   - `[ai.repos."<oldRepo>"]` → `[ai.repos."<newRepo>"]`
//   - `[ai.orgs."<oldOrg>"]`   → `[ai.orgs."<newOrg>"]` *only when the
//     org component differs between old and new* (org rename case).
//
// All other keys in the TOML round-trip through `map[string]any` so
// operator-only sections that the daemon does not model survive the
// rewrite intact.
//
// If `oldRepo` is not present anywhere in the file the function still
// rewrites the TOML (byte-equivalent content) so callers do not need
// to special-case the no-op path; this keeps the contract uniform and
// the audit trail at the SQLite layer authoritative.
func RenameRepoInTOML(path, oldRepo, newRepo string) error {
	m, err := ReadTOMLMap(path)
	if err != nil {
		return err
	}

	oldOrg, newOrg := orgOf(oldRepo), orgOf(newRepo)

	// 1. github.repositories / github.non_monitored: rewrite slices in place.
	if gh, ok := m["github"].(map[string]any); ok {
		gh["repositories"] = replaceInTOMLList(gh["repositories"], oldRepo, newRepo)
		gh["non_monitored"] = replaceInTOMLList(gh["non_monitored"], oldRepo, newRepo)
	}

	// 2. ai.repos.<old> -> ai.repos.<new>.
	if ai, ok := m["ai"].(map[string]any); ok {
		if repos, ok := ai["repos"].(map[string]any); ok {
			if v, has := repos[oldRepo]; has {
				delete(repos, oldRepo)
				repos[newRepo] = v
			}
		}
		// 3. ai.orgs.<oldOrg> -> ai.orgs.<newOrg> only when the org changed.
		if oldOrg != "" && newOrg != "" && oldOrg != newOrg {
			if orgs, ok := ai["orgs"].(map[string]any); ok {
				if v, has := orgs[oldOrg]; has {
					delete(orgs, oldOrg)
					orgs[newOrg] = v
				}
			}
		}
	}

	return AtomicWriteTOML(path, m)
}

// replaceInTOMLList walks a `[]any` of strings and substitutes any
// match for `old` with `new`, preserving order and length. Returns the
// input unchanged when it is nil or not a list of strings; the caller
// must handle the original value type.
func replaceInTOMLList(raw any, old, new string) any {
	list, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, len(list))
	for i, item := range list {
		if s, ok := item.(string); ok && s == old {
			out[i] = new
			continue
		}
		out[i] = item
	}
	return out
}

// orgOf returns the owner segment of a `owner/name` slug, or "" when
// the input is malformed. The reconciler pre-validates inputs against
// the GitHub canonical response, so this helper is defensive — the
// empty return collapses the org-rename branch to a no-op.
func orgOf(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return ""
	}
	return parts[0]
}

// ApplyRename mutates the in-memory Config in place to reflect a
// repo rename `oldRepo`→`newRepo`. The caller must hold cfgMu while
// invoking this — every running goroutine that reads cfg.GitHub.* or
// cfg.AI.Repos / cfg.AI.Orgs takes cfgMu, so the mutation is safe
// only when serialised with those readers.
//
// Mirror of RenameRepoInTOML's surface coverage: Repositories,
// NonMonitored, AI.Repos[<repo>], and AI.Orgs[<org>] when the org
// component changed between old and new.
func (c *Config) ApplyRename(oldRepo, newRepo string) {
	c.GitHub.Repositories = replaceInStringSlice(c.GitHub.Repositories, oldRepo, newRepo)
	c.GitHub.NonMonitored = replaceInStringSlice(c.GitHub.NonMonitored, oldRepo, newRepo)
	if c.AI.Repos != nil {
		if v, ok := c.AI.Repos[oldRepo]; ok {
			delete(c.AI.Repos, oldRepo)
			c.AI.Repos[newRepo] = v
		}
	}
	oldOrg, newOrg := orgOf(oldRepo), orgOf(newRepo)
	if oldOrg != "" && newOrg != "" && oldOrg != newOrg && c.AI.Orgs != nil {
		if v, ok := c.AI.Orgs[oldOrg]; ok {
			delete(c.AI.Orgs, oldOrg)
			c.AI.Orgs[newOrg] = v
		}
	}
}

func replaceInStringSlice(in []string, old, new string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		if s == old {
			out[i] = new
		} else {
			out[i] = s
		}
	}
	return out
}
