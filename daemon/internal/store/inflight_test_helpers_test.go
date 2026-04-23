package store

import (
	"fmt"
	"time"
)

// InsertStaleInFlight is a test-only helper that seeds an in-flight row
// with a custom started_at so ClearStaleInFlight can be exercised
// deterministically. Kept in a _test.go file so production code cannot
// accidentally depend on it — external test packages (package store_test)
// can still call it because it lives in package store and is exported.
func (s *Store) InsertStaleInFlight(prID int64, headSHA string, startedAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT INTO reviews_in_flight (pr_id, head_sha, started_at) VALUES (?, ?, ?)",
		prID, headSHA, startedAt.UTC().Format(sqliteTimeFormat),
	)
	if err != nil {
		return fmt.Errorf("store: insert stale inflight: %w", err)
	}
	return nil
}
