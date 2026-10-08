package store

import (
	"fmt"
	"strings"
	"time"
)

// ReviewCountFilter narrows ReviewTimesSince to one review budget scope.
// Empty fields do not filter; Org and Repo are mutually exclusive in
// practice (a scope is one or the other).
type ReviewCountFilter struct {
	Org  string // reviews of any repo under "org/"
	Repo string // reviews of exactly "owner/name"
	CLI  string // reviews produced by this agent (cli_used)
}

// ReviewTimesSince returns the created_at of every review matching filter at
// or after since, oldest first. Review budgets count these to decide whether
// a new review fits in a rolling window and, when it does not, when the
// oldest one will age out.
//
// Rows recorded for a peer instance's review (cli_used = "peer") are excluded:
// no agent ran for them, so they spent none of this daemon's budget.
func (s *Store) ReviewTimesSince(filter ReviewCountFilter, since time.Time) ([]time.Time, error) {
	var (
		where = []string{"r.created_at >= ?", "r.cli_used != 'peer'"}
		args  = []any{since.UTC().Format(sqliteTimeFormat)}
	)
	if filter.Repo != "" {
		where = append(where, "p.repo = ?")
		args = append(args, filter.Repo)
	}
	if filter.Org != "" {
		// Escape LIKE metacharacters: org slugs cannot contain them today, but
		// the filter must not depend on that.
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filter.Org)
		where = append(where, `p.repo LIKE ? ESCAPE '\'`)
		args = append(args, esc+"/%")
	}
	if filter.CLI != "" {
		where = append(where, "r.cli_used = ?")
		args = append(args, filter.CLI)
	}
	rows, err := s.db.Query(`
		SELECT r.created_at FROM reviews r
		JOIN prs p ON r.pr_id = p.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY r.created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: review times since: %w", err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("store: review times since: scan: %w", err)
		}
		t, err := time.Parse(sqliteTimeFormat, raw)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
