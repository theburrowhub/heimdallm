package mergetrack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// Fingerprinting: each due row first costs one aliased field of a batched
// query that returns a hash of everything its merge decision depends on. Only
// rows whose hash moved since their last decision pay the full merge-status
// query. Pending checks used to be re-queried in full every 45 seconds; now an
// unchanged one costs a fraction of a GraphQL point.

// fingerprintRevalidate forces a full evaluation of an unchanged row at least
// this often, so time-based rules (staleness notices) still run.
const fingerprintRevalidate = time.Hour

// Fingerprinter is the optional Gateway capability behind fingerprinting.
type Fingerprinter interface {
	MergeFingerprints(refs []gh.PRRef) map[string]string
}

// FingerprintStore is the optional StateStore capability behind it.
type FingerprintStore interface {
	MergeTrackingFingerprint(prID int64) (string, error)
	SetMergeTrackingFingerprint(prID int64, fp string) error
	DeferMergeTracking(prID int64, until time.Time) error
}

func (r *Reconciler) fingerprintStore() FingerprintStore {
	fs, _ := r.st.(FingerprintStore)
	return fs
}

// fingerprints fetches the GitHub-side fingerprint of every due row in one
// batch. Nil when either side lacks the capability.
func (r *Reconciler) fingerprints(due []*store.MergeTracking) map[string]string {
	fp, ok := r.gw.(Fingerprinter)
	if !ok || r.fingerprintStore() == nil || len(due) == 0 {
		return nil
	}
	refs := make([]gh.PRRef, 0, len(due))
	for _, row := range due {
		refs = append(refs, gh.PRRef{Repo: row.Repo, Number: row.Number})
	}
	return fp.MergeFingerprints(refs)
}

// decisionFingerprint combines the GitHub-side fingerprint with the row's
// effective configuration: a config change must re-decide even when GitHub
// did not move. "" means no fingerprint (evaluate in full).
func (r *Reconciler) decisionFingerprint(row *store.MergeTracking, fps map[string]string) string {
	remote := fps[gh.PRKey(row.Repo, row.Number)]
	if remote == "" {
		return ""
	}
	cfg, err := json.Marshal(r.cfgFor(row.Repo))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(remote+"\n"), cfg...))
	return hex.EncodeToString(sum[:])
}

// unchangedSinceDecision reports whether row's last decision still stands: the
// fingerprint matches the one recorded with it, the decision was a resting
// "nothing to do" (a pending action, an armed auto-merge or a wait for
// GitHub's mergeability computation can change with time alone), and it is
// recent enough not to need a periodic revalidation.
func (r *Reconciler) unchangedSinceDecision(row *store.MergeTracking, fp string, now time.Time) bool {
	fs := r.fingerprintStore()
	if fp == "" || fs == nil || row.DecisionJSON == "" || row.EvaluatedAt.IsZero() {
		return false
	}
	if row.Phase != store.MergePhaseIdle && row.Phase != store.MergePhaseBlocked {
		return false
	}
	if now.Sub(row.EvaluatedAt) >= fingerprintRevalidate {
		return false
	}
	var last Decision
	if err := json.Unmarshal([]byte(row.DecisionJSON), &last); err != nil || last.Action != ActionNone {
		return false
	}
	stored, err := fs.MergeTrackingFingerprint(row.PRID)
	return err == nil && stored == fp
}

// deferUnchanged reschedules an unchanged row with the same spacing its last
// decision chose.
func (r *Reconciler) deferUnchanged(row *store.MergeTracking, now time.Time) {
	interval := row.CooldownUntil.Sub(row.EvaluatedAt)
	if interval <= 0 {
		interval = r.defaultCooldown
	}
	if interval <= 0 {
		interval = time.Minute
	}
	if err := r.fingerprintStore().DeferMergeTracking(row.PRID, now.Add(interval)); err != nil {
		slog.Warn("mergetrack: defer unchanged row", "pr_id", row.PRID, "err", err)
	}
}

// recordFingerprint stores the fingerprint a full evaluation ran against. A
// run that acted or failed records none: its outcome is not a resting
// decision, so the next pass must evaluate in full.
func (r *Reconciler) recordFingerprint(prID int64, fp string, acted bool, evalErr error) {
	fs := r.fingerprintStore()
	if fs == nil {
		return
	}
	if acted || evalErr != nil {
		fp = ""
	}
	if err := fs.SetMergeTrackingFingerprint(prID, fp); err != nil {
		slog.Warn("mergetrack: record fingerprint", "pr_id", prID, "err", err)
	}
}
