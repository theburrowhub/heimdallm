package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/heimdallm/daemon/internal/instances"
)

// SaveInstanceState upserts the last observed condition of one instance.
//
// Implements instances.StateStore. The prober calls this on every probe, so it
// is a single idempotent upsert keyed on instance_id rather than an append-only
// log: the hub cares about the current state, and a row per probe per instance
// would grow without bound for information nobody reads.
func (s *Store) SaveInstanceState(st instances.State) error {
	if st.InstanceID == "" {
		return fmt.Errorf("store: instance state needs an instance_id")
	}
	lastSeen := ""
	if !st.LastSeenAt.IsZero() {
		lastSeen = st.LastSeenAt.UTC().Format(sqliteTimeFormat)
	}
	_, err := s.db.Exec(`
		INSERT INTO instance_state (
			instance_id, name, reachable, status, version, role, remote_instance_id,
			uptime_seconds, last_seen_at, last_error, consecutive_failures, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET
			name                 = excluded.name,
			reachable            = excluded.reachable,
			status               = excluded.status,
			version              = excluded.version,
			role                 = excluded.role,
			remote_instance_id   = excluded.remote_instance_id,
			uptime_seconds       = excluded.uptime_seconds,
			last_seen_at         = excluded.last_seen_at,
			last_error           = excluded.last_error,
			consecutive_failures = excluded.consecutive_failures,
			updated_at           = excluded.updated_at
	`,
		st.InstanceID, st.Name, boolToInt(st.Reachable), st.Status, st.Version, st.Role,
		st.RemoteInstanceID, st.UptimeSeconds, lastSeen, st.LastError,
		st.ConsecutiveFailures, time.Now().UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("store: save instance state %q: %w", st.InstanceID, err)
	}
	return nil
}

// LoadInstanceStates returns every persisted instance state, in id order.
// Implements instances.StateStore.
func (s *Store) LoadInstanceStates() ([]instances.State, error) {
	rows, err := s.db.Query(`
		SELECT instance_id, name, reachable, status, version, role, remote_instance_id,
		       uptime_seconds, last_seen_at, last_error, consecutive_failures
		FROM instance_state ORDER BY instance_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list instance states: %w", err)
	}
	defer rows.Close()

	var out []instances.State
	for rows.Next() {
		var (
			st        instances.State
			reachable int
			lastSeen  string
		)
		if err := rows.Scan(
			&st.InstanceID, &st.Name, &reachable, &st.Status, &st.Version, &st.Role,
			&st.RemoteInstanceID, &st.UptimeSeconds, &lastSeen, &st.LastError,
			&st.ConsecutiveFailures,
		); err != nil {
			return nil, fmt.Errorf("store: scan instance state: %w", err)
		}
		st.Reachable = reachable != 0
		// A legacy or never-probed row has no timestamp; a zero time is the
		// right answer, not a hard error that would blank the whole listing.
		if lastSeen != "" {
			if parsed, perr := time.Parse(sqliteTimeFormat, lastSeen); perr == nil {
				st.LastSeenAt = parsed
			}
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// DeleteInstanceState removes an instance's observed state, used when it is
// deregistered so a stale row cannot reappear in the UI.
func (s *Store) DeleteInstanceState(instanceID string) error {
	if _, err := s.db.Exec("DELETE FROM instance_state WHERE instance_id = ?", instanceID); err != nil {
		return fmt.Errorf("store: delete instance state %q: %w", instanceID, err)
	}
	return nil
}

// ClaimDispatch records that op for (targetKey, headSHA) was routed to
// instanceID, and reports whether this call is the one that claimed it.
//
// Returns false when the same operation was already dispatched for the same
// commit, which is how the hub avoids sending duplicate work when a caller
// retries or two GUI clients click at once. A new head SHA is a genuinely new
// operation and claims cleanly.
//
// This is NOT a distributed lock. It coordinates the hub with itself; what
// keeps two daemons off the same repo is the router's ownership partition.
func (s *Store) ClaimDispatch(op, targetKey, headSHA, instanceID string) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO instance_dispatch (op, target_key, head_sha, instance_id, dispatched_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(op, target_key, head_sha) DO NOTHING
	`, op, targetKey, headSHA, instanceID, time.Now().UTC().Format(sqliteTimeFormat))
	if err != nil {
		return false, fmt.Errorf("store: claim dispatch %s/%s: %w", op, targetKey, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claim dispatch %s/%s: %w", op, targetKey, err)
	}
	return affected > 0, nil
}

// DispatchTarget returns the instance a previous dispatch went to, if any. The
// hub uses it to report "already handled by X" instead of silently doing
// nothing when a duplicate arrives.
func (s *Store) DispatchTarget(op, targetKey, headSHA string) (string, bool, error) {
	var instanceID string
	err := s.db.QueryRow(
		"SELECT instance_id FROM instance_dispatch WHERE op = ? AND target_key = ? AND head_sha = ?",
		op, targetKey, headSHA,
	).Scan(&instanceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("store: read dispatch %s/%s: %w", op, targetKey, err)
	}
	return instanceID, true, nil
}

// PruneDispatches deletes dispatch records older than cutoff. The ledger only
// needs to cover the window in which a duplicate could plausibly arrive; it is
// not an audit log, and the activity log already records what actually ran.
func (s *Store) PruneDispatches(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(
		"DELETE FROM instance_dispatch WHERE dispatched_at < ?",
		cutoff.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return 0, fmt.Errorf("store: prune dispatches: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune dispatches: %w", err)
	}
	return n, nil
}
