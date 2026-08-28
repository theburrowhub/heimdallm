package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Merge-tracking phases. Persisted verbatim, so these strings are part of the
// on-disk contract and of the HTTP/SSE payloads.
const (
	// MergePhaseIdle means the PR is tracked with nothing in flight.
	MergePhaseIdle = "idle"
	// MergePhaseBlocked means the last evaluation found a reason not to act.
	MergePhaseBlocked = "blocked"
	// MergePhaseUpdating means an update-branch or local rebase is in flight.
	MergePhaseUpdating = "updating"
	// MergePhaseResolving means the conflict-resolution agent is running.
	MergePhaseResolving = "resolving"
	// MergePhaseAutoMergeArmed means GitHub's native auto-merge is enabled and
	// we are waiting for GitHub to act. This is the hand-off state: a later
	// tick promotes it to a direct merge if GitHub has not merged by then.
	MergePhaseAutoMergeArmed = "auto_merge_armed"
	// MergePhaseMerging means a direct merge request is in flight.
	MergePhaseMerging = "merging"
	// MergePhaseMerged is terminal and set once GitHub reports the PR merged.
	MergePhaseMerged = "merged"
	// MergePhaseAbandoned is terminal for PRs that will never be actionable
	// (closed without merging, no longer ours, insufficient permission).
	MergePhaseAbandoned = "abandoned"
)

// Attempt counter kinds accepted by BumpMergeTrackingAttempt.
const (
	MergeAttemptUpdate   = "update"
	MergeAttemptConflict = "conflict"
	MergeAttemptMerge    = "merge"
	MergeAttemptUnknown  = "unknown_wait"
	// MergeAttemptArm counts failed attempts to arm GitHub's native auto-merge.
	// Without a counter of its own, an arming failure would back off by a flat
	// minute forever instead of growing.
	MergeAttemptArm = "arm"
)

// inFlightMergePhases are the phases that represent work already running. A
// claim refuses to start a second action while the PR sits in one of them.
var inFlightMergePhases = []string{MergePhaseUpdating, MergePhaseResolving, MergePhaseMerging}

// terminalMergePhases never come back on their own.
var terminalMergePhases = []string{MergePhaseMerged, MergePhaseAbandoned}

// MergeTracking is the persisted merge-readiness state of one tracked PR.
//
// It lives in its own table rather than as columns on `prs` for two reasons:
// UpsertPR rewrites the PR row on every poll and would have to learn to
// preserve each of these fields, and the lifecycle here (per-head-SHA attempt
// counters, cooldowns, an armed auto-merge) is independent of the PR row's.
type MergeTracking struct {
	PRID   int64  `json:"pr_id"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	NodeID string `json:"node_id,omitempty"`

	Phase   string `json:"phase"`
	HeadSHA string `json:"head_sha,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	HeadRef string `json:"head_ref,omitempty"`

	IsAuthor   bool `json:"is_author"`
	IsAssignee bool `json:"is_assignee"`
	Excluded   bool `json:"excluded"`

	AutoMergeArmedAt time.Time `json:"auto_merge_armed_at,omitempty"`
	AutoMergeHeadSHA string    `json:"auto_merge_head_sha,omitempty"`
	AutoMergeMethod  string    `json:"auto_merge_method,omitempty"`

	BlockReason string `json:"block_reason,omitempty"`
	BlockDetail string `json:"block_detail,omitempty"`
	// DecisionJSON is the full serialised decision, including the per-check
	// breakdown. Persisting it is what lets the listing and the PR detail view
	// explain a blocked merge without a second call to GitHub.
	DecisionJSON string `json:"-"`

	ChecksRequiredFailing int `json:"checks_required_failing"`
	ChecksRequiredPending int `json:"checks_required_pending"`

	UnknownWaits     int `json:"unknown_waits"`
	ArmAttempts      int `json:"arm_attempts"`
	UpdateAttempts   int `json:"update_attempts"`
	ConflictAttempts int `json:"conflict_attempts"`
	MergeAttempts    int `json:"merge_attempts"`

	// PreRebaseSHA is the head SHA recorded immediately before a rebase or a
	// force-push, so a human can recover with `git reset --hard <sha>` if the
	// agent's resolution turns out to be wrong.
	PreRebaseSHA string `json:"pre_rebase_sha,omitempty"`

	LastAttemptAt  time.Time `json:"last_attempt_at,omitempty"`
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at,omitempty"`
	MergedAt       time.Time `json:"merged_at,omitempty"`
	TerminalReason string    `json:"terminal_reason,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// InFlight reports whether an action is currently running for this PR.
func (m *MergeTracking) InFlight() bool {
	if m == nil {
		return false
	}
	for _, p := range inFlightMergePhases {
		if m.Phase == p {
			return true
		}
	}
	return false
}

// Terminal reports whether this PR has reached a state it will not leave.
func (m *MergeTracking) Terminal() bool {
	if m == nil {
		return false
	}
	for _, p := range terminalMergePhases {
		if m.Phase == p {
			return true
		}
	}
	return false
}

// AutoMergeArmedFor reports whether native auto-merge is armed for headSHA.
// An armed state anchored to a different commit is stale — a push cleared it —
// and must not license a direct merge.
//
// The anchor is auto_merge_head_sha, deliberately NOT the phase: the phase is
// rewritten by every evaluation, so reading it here meant that recording a
// decision silently disarmed the row, and a PR whose auto-merge GitHub already
// held would be re-anchored on every cycle and never promoted.
func (m *MergeTracking) AutoMergeArmedFor(headSHA string) bool {
	if m == nil || headSHA == "" {
		return false
	}
	return m.AutoMergeHeadSHA != "" && m.AutoMergeHeadSHA == headSHA
}

const mergeTrackingColumns = "pr_id, repo, number, node_id, phase, head_sha, base_ref, head_ref, " +
	"is_author, is_assignee, excluded, " +
	"auto_merge_armed_at, auto_merge_head_sha, auto_merge_method, " +
	"block_reason, block_detail, decision_json, " +
	"checks_required_failing, checks_required_pending, " +
	"unknown_waits, arm_attempts, update_attempts, conflict_attempts, merge_attempts, " +
	"pre_rebase_sha, last_attempt_at, cooldown_until, last_error, " +
	"evaluated_at, merged_at, terminal_reason, updated_at"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMergeTracking(row rowScanner) (*MergeTracking, error) {
	var (
		m                                              MergeTracking
		nodeID, headSHA, baseRef, headRef              sql.NullString
		armedAt, armedSHA, armedMethod                 sql.NullString
		blockReason, blockDetail, decisionJSON         sql.NullString
		preRebase, lastAttempt, cooldown, lastErr      sql.NullString
		evaluatedAt, mergedAt, terminalReason, updated sql.NullString
		isAuthor, isAssignee, excluded                 int
	)
	err := row.Scan(
		&m.PRID, &m.Repo, &m.Number, &nodeID, &m.Phase, &headSHA, &baseRef, &headRef,
		&isAuthor, &isAssignee, &excluded,
		&armedAt, &armedSHA, &armedMethod,
		&blockReason, &blockDetail, &decisionJSON,
		&m.ChecksRequiredFailing, &m.ChecksRequiredPending,
		&m.UnknownWaits, &m.ArmAttempts, &m.UpdateAttempts, &m.ConflictAttempts, &m.MergeAttempts,
		&preRebase, &lastAttempt, &cooldown, &lastErr,
		&evaluatedAt, &mergedAt, &terminalReason, &updated,
	)
	if err != nil {
		return nil, err
	}
	m.NodeID = nodeID.String
	m.HeadSHA = headSHA.String
	m.BaseRef = baseRef.String
	m.HeadRef = headRef.String
	m.IsAuthor = isAuthor != 0
	m.IsAssignee = isAssignee != 0
	m.Excluded = excluded != 0
	m.AutoMergeArmedAt = parseStoredTime(armedAt.String)
	m.AutoMergeHeadSHA = armedSHA.String
	m.AutoMergeMethod = armedMethod.String
	m.BlockReason = blockReason.String
	m.BlockDetail = blockDetail.String
	m.DecisionJSON = decisionJSON.String
	m.PreRebaseSHA = preRebase.String
	m.LastAttemptAt = parseStoredTime(lastAttempt.String)
	m.CooldownUntil = parseStoredTime(cooldown.String)
	m.LastError = lastErr.String
	m.EvaluatedAt = parseStoredTime(evaluatedAt.String)
	m.MergedAt = parseStoredTime(mergedAt.String)
	m.TerminalReason = terminalReason.String
	m.UpdatedAt = parseStoredTime(updated.String)
	return &m, nil
}

// parseStoredTime turns a stored RFC3339 string back into a time. An empty or
// unparseable value yields the zero time, which every caller treats as "unset".
func parseStoredTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(sqliteTimeFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// formatStoredTime renders a time for storage, mapping the zero time to the
// empty string so "unset" round-trips.
func formatStoredTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(sqliteTimeFormat)
}

// ErrMergeTrackingNotFound means no row exists for the PR.
var ErrMergeTrackingNotFound = errors.New("store: merge tracking row not found")

// EnsureMergeTracking returns the existing row for prID, creating an idle one
// if there is none. Safe to call on every poll.
//
// An abandoned row is revived, because discovery only ever offers open PRs the
// user still owns: the PR was reopened, include_assigned was turned back on, or
// the snapshot that abandoned it was a transient GitHub failure. Without this
// the row would be re-discovered every cycle and never evaluated again, and the
// UI would show a PR Heimdallm can see as "not tracked" forever. A merged row
// is left alone — that outcome does not come back.
func (s *Store) EnsureMergeTracking(prID int64, repo string, number int) (*MergeTracking, error) {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO merge_tracking (pr_id, repo, number, phase, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(pr_id) DO UPDATE SET
		    repo = excluded.repo,
		    number = excluded.number,
		    phase = CASE WHEN merge_tracking.phase = 'abandoned'
		                 THEN 'idle' ELSE merge_tracking.phase END,
		    terminal_reason = CASE WHEN merge_tracking.phase = 'abandoned'
		                          THEN '' ELSE merge_tracking.terminal_reason END,
		    cooldown_until = CASE WHEN merge_tracking.phase = 'abandoned'
		                          THEN '' ELSE merge_tracking.cooldown_until END,
		    last_error = CASE WHEN merge_tracking.phase = 'abandoned'
		                      THEN '' ELSE merge_tracking.last_error END
	`, prID, repo, number, MergePhaseIdle, now.Format(sqliteTimeFormat))
	if err != nil {
		return nil, fmt.Errorf("store: ensure merge tracking: %w", err)
	}
	return s.GetMergeTracking(prID)
}

// GetMergeTracking retrieves the row for prID.
func (s *Store) GetMergeTracking(prID int64) (*MergeTracking, error) {
	row := s.db.QueryRow("SELECT "+mergeTrackingColumns+" FROM merge_tracking WHERE pr_id = ?", prID)
	m, err := scanMergeTracking(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMergeTrackingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get merge tracking: %w", err)
	}
	return m, nil
}

// ListMergeTracking returns every tracked row, newest evaluation first.
// Rows blocked by failing or pending required checks sort to the top: a merge
// held up by CI is what the operator needs to see first.
func (s *Store) ListMergeTracking() ([]*MergeTracking, error) {
	rows, err := s.db.Query(`
		SELECT ` + mergeTrackingColumns + `
		FROM merge_tracking
		ORDER BY
			(checks_required_failing > 0) DESC,
			(checks_required_pending > 0) DESC,
			phase = 'merged' ASC,
			updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list merge tracking: %w", err)
	}
	defer rows.Close()
	return collectMergeTracking(rows)
}

// ListMergeTrackingDue returns the rows eligible for evaluation now: not
// terminal, not excluded, not already in flight, and past any cooldown.
// Ordered so the longest-waiting rows go first, capped at limit.
func (s *Store) ListMergeTrackingDue(now time.Time, limit int) ([]*MergeTracking, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT `+mergeTrackingColumns+`
		FROM merge_tracking
		WHERE excluded = 0
		  AND phase NOT IN ('merged','abandoned','updating','resolving','merging')
		  AND (cooldown_until = '' OR cooldown_until <= ?)
		ORDER BY evaluated_at ASC, pr_id ASC
		LIMIT ?
	`, now.UTC().Format(sqliteTimeFormat), limit)
	if err != nil {
		return nil, fmt.Errorf("store: list merge tracking due: %w", err)
	}
	defer rows.Close()
	return collectMergeTracking(rows)
}

func collectMergeTracking(rows *sql.Rows) ([]*MergeTracking, error) {
	var out []*MergeTracking
	for rows.Next() {
		m, err := scanMergeTracking(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan merge tracking: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate merge tracking: %w", err)
	}
	return out, nil
}

// UpdateMergeTrackingIdentity refreshes the fields that come straight from the
// PR snapshot and carry no decision semantics.
func (s *Store) UpdateMergeTrackingIdentity(prID int64, nodeID, baseRef, headRef string, isAuthor, isAssignee bool) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET node_id = ?, base_ref = ?, head_ref = ?, is_author = ?, is_assignee = ?, updated_at = ?
		WHERE pr_id = ?
	`, nodeID, baseRef, headRef, boolToInt(isAuthor), boolToInt(isAssignee),
		time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: update merge tracking identity: %w", err)
	}
	return nil
}

// ResetMergeTrackingForNewHead re-anchors the row to a new head SHA.
//
// Every attempt counter, cooldown and recorded error described the previous
// commit, so they are cleared: a push is a fresh start. The armed auto-merge is
// cleared too unless it was armed for exactly this SHA — GitHub keeps
// auto-merge across pushes, but our licence to merge directly was granted for a
// commit that no longer exists.
func (s *Store) ResetMergeTrackingForNewHead(prID int64, headSHA string, at time.Time) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET head_sha = ?,
		    unknown_waits = 0,
		    arm_attempts = 0,
		    update_attempts = 0,
		    conflict_attempts = 0,
		    merge_attempts = 0,
		    cooldown_until = '',
		    last_error = '',
		    pre_rebase_sha = '',
		    auto_merge_armed_at  = CASE WHEN auto_merge_head_sha = ? THEN auto_merge_armed_at  ELSE '' END,
		    auto_merge_method    = CASE WHEN auto_merge_head_sha = ? THEN auto_merge_method    ELSE '' END,
		    phase = CASE
		              WHEN phase = 'auto_merge_armed' AND auto_merge_head_sha = ? THEN phase
		              WHEN phase IN ('merged','abandoned') THEN phase
		              WHEN phase IN ('updating','resolving','merging') THEN phase
		              ELSE 'idle'
		            END,
		    auto_merge_head_sha  = CASE WHEN auto_merge_head_sha = ? THEN auto_merge_head_sha  ELSE '' END,
		    updated_at = ?
		WHERE pr_id = ?
	`, headSHA, headSHA, headSHA, headSHA, headSHA, at.UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: reset merge tracking for new head: %w", err)
	}
	return nil
}

// ClaimMergeTrackingAction atomically moves the row into an in-flight phase,
// but only if it is still anchored to headSHA, is not already in flight, and is
// past its cooldown. Returns false when another tick — or another daemon, or
// this one before a restart — already owns the action.
//
// This is the persistent single-flight guard. An in-memory lock would be lost
// on restart and would not span two daemons pointed at the same database.
func (s *Store) ClaimMergeTrackingAction(prID int64, headSHA, phase string, now time.Time) (bool, error) {
	if headSHA == "" {
		return false, fmt.Errorf("store: claim merge tracking: head sha is required")
	}
	res, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = ?, last_attempt_at = ?, updated_at = ?
		WHERE pr_id = ?
		  AND head_sha = ?
		  AND excluded = 0
		  AND phase NOT IN ('updating','resolving','merging','merged','abandoned')
		  AND (cooldown_until = '' OR cooldown_until <= ?)
	`, phase, now.UTC().Format(sqliteTimeFormat), now.UTC().Format(sqliteTimeFormat),
		prID, headSHA, now.UTC().Format(sqliteTimeFormat))
	if err != nil {
		return false, fmt.Errorf("store: claim merge tracking action: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claim merge tracking rowsaffected: %w", err)
	}
	return n == 1, nil
}

// ReleaseMergeTrackingAction moves the row out of an in-flight phase, applying
// a cooldown and an optional error. Always call it from a defer after a
// successful claim, or a crash mid-action would leave the PR stuck until the
// next daemon start reconciles it.
func (s *Store) ReleaseMergeTrackingAction(prID int64, phase string, cooldownUntil time.Time, lastErr string) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = ?, cooldown_until = ?, last_error = ?, updated_at = ?
		WHERE pr_id = ?
	`, phase, formatStoredTime(cooldownUntil), lastErr,
		time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: release merge tracking action: %w", err)
	}
	return nil
}

// BlockMergeTracking parks a row with an explicit reason and cooldown.
//
// Distinct from ReleaseMergeTrackingAction, which only moves the phase: a block
// discovered DURING an action — the head moved between evaluation and merge,
// say — has a reason the earlier RecordMergeTrackingDecision could not know,
// and the UI needs it as much as any other block.
func (s *Store) BlockMergeTracking(prID int64, reason, detail string, cooldownUntil time.Time) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = ?, block_reason = ?, block_detail = ?, cooldown_until = ?, updated_at = ?
		WHERE pr_id = ?
	`, MergePhaseBlocked, reason, detail, formatStoredTime(cooldownUntil),
		time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: block merge tracking: %w", err)
	}
	return nil
}

// ArmNativeAutoMerge records that GitHub's auto-merge is enabled for headSHA.
func (s *Store) ArmNativeAutoMerge(prID int64, headSHA, method string, at time.Time) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = CASE WHEN phase IN ('updating','resolving','merging') THEN phase ELSE ? END,
		    auto_merge_armed_at = ?, auto_merge_head_sha = ?, auto_merge_method = ?,
		    last_error = '', updated_at = ?
		WHERE pr_id = ?
	`, MergePhaseAutoMergeArmed, at.UTC().Format(sqliteTimeFormat), headSHA, method,
		at.UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: arm native auto-merge: %w", err)
	}
	return nil
}

// ClearNativeAutoMerge forgets a previously armed auto-merge. Called when
// GitHub reports no auto-merge request any more (someone disabled it, or a
// push cleared it), so our state self-heals from GitHub rather than drifting.
func (s *Store) ClearNativeAutoMerge(prID int64) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET auto_merge_armed_at = '', auto_merge_head_sha = '', auto_merge_method = '',
		    phase = CASE WHEN phase = 'auto_merge_armed' THEN 'idle' ELSE phase END,
		    updated_at = ?
		WHERE pr_id = ?
	`, time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: clear native auto-merge: %w", err)
	}
	return nil
}

// RecordMergeTrackingDecision persists the outcome of one evaluation.
//
// The phase and the cooldown are written only when no action is in flight. An
// evaluation is derived from a snapshot read before the claim, so a concurrent
// one — the manual evaluate endpoint, or a second daemon on the same database —
// would otherwise write its stale 'idle' over a live 'merging' and release the
// single-flight lock mid-action. Everything the UI explains a block with is
// still refreshed either way.
func (s *Store) RecordMergeTrackingDecision(prID int64, d MergeDecisionRecord) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = CASE WHEN phase IN ('updating','resolving','merging') THEN phase ELSE ? END,
		    head_sha = ?, block_reason = ?, block_detail = ?, decision_json = ?,
		    checks_required_failing = ?, checks_required_pending = ?,
		    evaluated_at = ?,
		    cooldown_until = CASE WHEN phase IN ('updating','resolving','merging')
		                          THEN cooldown_until ELSE ? END,
		    updated_at = ?
		WHERE pr_id = ?
	`, d.Phase, d.HeadSHA, d.BlockReason, d.BlockDetail, d.DecisionJSON,
		d.ChecksRequiredFailing, d.ChecksRequiredPending,
		d.At.UTC().Format(sqliteTimeFormat), formatStoredTime(d.CooldownUntil),
		d.At.UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: record merge tracking decision: %w", err)
	}
	return nil
}

// MergeDecisionRecord is the write payload for RecordMergeTrackingDecision.
// A struct rather than a dozen positional parameters, because every field is
// optional-ish and a positional call would be unreadable at the call site.
type MergeDecisionRecord struct {
	Phase                 string
	HeadSHA               string
	BlockReason           string
	BlockDetail           string
	DecisionJSON          string
	ChecksRequiredFailing int
	ChecksRequiredPending int
	CooldownUntil         time.Time
	At                    time.Time
}

// BumpMergeTrackingAttempt increments one attempt counter and applies a
// cooldown plus the error that caused it.
func (s *Store) BumpMergeTrackingAttempt(prID int64, kind string, cooldownUntil time.Time, lastErr string) error {
	var column string
	switch kind {
	case MergeAttemptUpdate:
		column = "update_attempts"
	case MergeAttemptConflict:
		column = "conflict_attempts"
	case MergeAttemptMerge:
		column = "merge_attempts"
	case MergeAttemptUnknown:
		column = "unknown_waits"
	case MergeAttemptArm:
		column = "arm_attempts"
	default:
		return fmt.Errorf("store: bump merge tracking attempt: unknown kind %q", kind)
	}
	// The column name comes from the closed switch above, never from caller
	// input, so interpolating it cannot inject SQL.
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET `+column+` = `+column+` + 1,
		    cooldown_until = ?, last_error = ?, last_attempt_at = ?, updated_at = ?
		WHERE pr_id = ?
	`, formatStoredTime(cooldownUntil), lastErr,
		time.Now().UTC().Format(sqliteTimeFormat), time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: bump merge tracking attempt: %w", err)
	}
	return nil
}

// ClearMergeTrackingUnknownWaits zeroes the unknown-mergeability counter once
// GitHub answers with a real state. Without this the counter would only ever
// reset on a push, and a long-lived PR whose base moves often would eventually
// trip the cap on a head GitHub is perfectly willing to compute.
func (s *Store) ClearMergeTrackingUnknownWaits(prID int64) error {
	_, err := s.db.Exec(
		"UPDATE merge_tracking SET unknown_waits = 0, updated_at = ? WHERE pr_id = ? AND unknown_waits != 0",
		time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: clear unknown waits: %w", err)
	}
	return nil
}

// SetMergeTrackingPreRebaseSHA records the pre-rewrite head so a bad
// resolution can be recovered by hand.
func (s *Store) SetMergeTrackingPreRebaseSHA(prID int64, sha string) error {
	_, err := s.db.Exec(
		"UPDATE merge_tracking SET pre_rebase_sha = ?, updated_at = ? WHERE pr_id = ?",
		sha, time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: set pre-rebase sha: %w", err)
	}
	return nil
}

// MarkMergeTrackingMerged sets the terminal merged state.
func (s *Store) MarkMergeTrackingMerged(prID int64, at time.Time) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = ?, merged_at = ?, block_reason = '', block_detail = '', last_error = '',
		    cooldown_until = '', updated_at = ?
		WHERE pr_id = ?
	`, MergePhaseMerged, at.UTC().Format(sqliteTimeFormat),
		at.UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: mark merge tracking merged: %w", err)
	}
	return nil
}

// MarkMergeTrackingAbandoned sets the terminal non-actionable state.
func (s *Store) MarkMergeTrackingAbandoned(prID int64, reason string, at time.Time) error {
	_, err := s.db.Exec(`
		UPDATE merge_tracking
		SET phase = ?, terminal_reason = ?, cooldown_until = '', updated_at = ?
		WHERE pr_id = ?
	`, MergePhaseAbandoned, reason, at.UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: mark merge tracking abandoned: %w", err)
	}
	return nil
}

// SetMergeTrackingExcluded opts a single PR out of (or back into) automation
// without touching the repo-level config.
func (s *Store) SetMergeTrackingExcluded(prID int64, excluded bool) error {
	_, err := s.db.Exec(
		"UPDATE merge_tracking SET excluded = ?, updated_at = ? WHERE pr_id = ?",
		boolToInt(excluded), time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: set merge tracking excluded: %w", err)
	}
	return nil
}

// ClearMergeTrackingCooldown makes a row due immediately. Backs the manual
// "re-evaluate now" action in the UI and CLI.
func (s *Store) ClearMergeTrackingCooldown(prID int64) error {
	_, err := s.db.Exec(
		"UPDATE merge_tracking SET cooldown_until = '', updated_at = ? WHERE pr_id = ?",
		time.Now().UTC().Format(sqliteTimeFormat), prID)
	if err != nil {
		return fmt.Errorf("store: clear merge tracking cooldown: %w", err)
	}
	return nil
}

// PruneMergeTracking deletes rows whose PR is gone or which reached a terminal
// state before the cutoff. Keeps the table bounded without an extra scheduler.
func (s *Store) PruneMergeTracking(before time.Time) (int, error) {
	res, err := s.db.Exec(`
		DELETE FROM merge_tracking
		WHERE pr_id NOT IN (SELECT id FROM prs)
		   OR (phase IN ('merged','abandoned') AND updated_at != '' AND updated_at <= ?)
	`, before.UTC().Format(sqliteTimeFormat))
	if err != nil {
		return 0, fmt.Errorf("store: prune merge tracking: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune merge tracking rowsaffected: %w", err)
	}
	return int(n), nil
}
