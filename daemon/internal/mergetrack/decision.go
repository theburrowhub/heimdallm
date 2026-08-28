package mergetrack

import (
	"fmt"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// Action is the next automation step for a tracked PR.
type Action string

const (
	// ActionNone means do nothing this cycle.
	ActionNone Action = "none"
	// ActionWait means GitHub is still computing; re-check soon without acting.
	ActionWait Action = "wait"
	// ActionArmAutoMerge means enable GitHub's native auto-merge.
	ActionArmAutoMerge Action = "arm_auto_merge"
	// ActionUpdateBranchRemote means ask GitHub to update the branch.
	ActionUpdateBranchRemote Action = "update_branch_remote"
	// ActionUpdateBranchLocal means rebase locally and force-push with lease,
	// used when GitHub refuses the remote update (typically linear history).
	ActionUpdateBranchLocal Action = "update_branch_local"
	// ActionResolveConflicts means run the configured agent on the conflicts.
	ActionResolveConflicts Action = "resolve_conflicts"
	// ActionMerge means merge directly, pinned to the evaluated head SHA.
	ActionMerge Action = "merge"
	// ActionMarkMerged means GitHub already merged it; record and stop.
	ActionMarkMerged Action = "mark_merged"
	// ActionAbandon means this PR will never be actionable.
	ActionAbandon Action = "abandon"
)

// Mutating reports whether the action writes to GitHub or to a git remote.
// Only mutating actions take the work gate and the persistent claim.
func (a Action) Mutating() bool {
	switch a {
	case ActionArmAutoMerge, ActionUpdateBranchRemote, ActionUpdateBranchLocal,
		ActionResolveConflicts, ActionMerge:
		return true
	default:
		return false
	}
}

// Block is one reason the PR is not mergeable, with the evidence behind it.
type Block struct {
	Reason Reason `json:"reason"`
	// Detail names the specifics — which check, which reviewer — because
	// "checks failing" is not actionable and "build (GitHub Actions) failing"
	// is.
	Detail string `json:"detail,omitempty"`
}

// ChecksSummary are the counts the listing needs to render its warning without
// walking the whole check list.
type ChecksSummary struct {
	Total           int `json:"total"`
	RequiredTotal   int `json:"required_total"`
	RequiredSuccess int `json:"required_success"`
	RequiredPending int `json:"required_pending"`
	RequiredFailing int `json:"required_failing"`
	OptionalFailing int `json:"optional_failing"`
	// MissingRequired are contexts branch protection demands that have not
	// reported at all. They block just as hard as a failure, and are easy to
	// miss because nothing red appears anywhere in the GitHub UI.
	MissingRequired []string `json:"missing_required,omitempty"`
	// Truncated means GitHub had more checks than we were willing to page
	// through. The evaluator never reports ready while this is set.
	Truncated bool `json:"truncated"`
}

// AnyProblem reports whether the checks warrant a warning in the listing.
func (s ChecksSummary) AnyProblem() bool {
	return s.RequiredFailing > 0 || s.RequiredPending > 0 || len(s.MissingRequired) > 0 || s.Truncated
}

// Evidence is the factual basis of a decision, kept separate from the blocks so
// the UI can show "what we saw" alongside "what we concluded".
type Evidence struct {
	HeadSHA          string `json:"head_sha,omitempty"`
	BaseRef          string `json:"base_ref,omitempty"`
	MergeStateStatus string `json:"merge_state_status,omitempty"`
	Mergeable        string `json:"mergeable,omitempty"`
	ReviewDecision   string `json:"review_decision,omitempty"`

	ApprovalsAtHead    int      `json:"approvals_at_head"`
	RequiredApprovals  int      `json:"required_approvals"`
	ChangesRequestedBy []string `json:"changes_requested_by,omitempty"`
	StaleApprovals     int      `json:"stale_approvals"`
	PendingReviewers   []string `json:"pending_reviewers,omitempty"`
	UnresolvedThreads  int      `json:"unresolved_threads"`

	AutoMergeArmedAt time.Time `json:"auto_merge_armed_at,omitempty"`
	InMergeQueue     bool      `json:"in_merge_queue"`
	// ProtectionUnreadable records that we could not read branch protection,
	// which is why some conclusions are stricter than GitHub's own.
	ProtectionUnreadable bool `json:"protection_unreadable"`
}

// Decision is the full explainable outcome of one evaluation.
type Decision struct {
	Action Action `json:"action"`
	// Ready means every merge requirement is satisfied for HeadSHA.
	Ready bool `json:"ready"`
	// Blocks is ordered: the first entry is the reason to show.
	Blocks []Block `json:"blocks,omitempty"`

	Evidence Evidence `json:"evidence"`
	// Checks is the full per-check breakdown, ordered for presentation:
	// required failures first, then required pending, then optional failures,
	// then the rest. The UI renders this directly.
	Checks        []gh.CheckContext `json:"checks,omitempty"`
	ChecksSummary ChecksSummary     `json:"checks_summary"`

	HeadSHA     string `json:"head_sha,omitempty"`
	MergeMethod string `json:"merge_method,omitempty"`
	// CooldownHint is how long to wait before the next evaluation. Zero means
	// "use the caller's default".
	CooldownHint time.Duration `json:"-"`
}

// PrimaryReason returns the reason to display, or ReasonNone when ready.
func (d Decision) PrimaryReason() Reason {
	if len(d.Blocks) == 0 {
		return ReasonNone
	}
	return d.Blocks[0].Reason
}

// PrimaryDetail returns the detail of the primary block.
func (d Decision) PrimaryDetail() string {
	if len(d.Blocks) == 0 {
		return ""
	}
	return d.Blocks[0].Detail
}

// Explain renders a one-line summary for logs, SSE and the audit trail.
func (d Decision) Explain() string {
	if d.Ready {
		return fmt.Sprintf("ready to merge at %s", shortSHA(d.HeadSHA))
	}
	if len(d.Blocks) == 0 {
		return "no action"
	}
	parts := make([]string, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		if b.Detail != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", b.Reason, b.Detail))
			continue
		}
		parts = append(parts, string(b.Reason))
	}
	return strings.Join(parts, "; ")
}

// Headline renders the plain-language sentence shown above the check table.
//
// It exists because a list of check states is not an explanation. "Esta PR no
// se puede mezclar: falla 1 de los 4 checks requeridos" tells the reader what
// to do next; a grid of red dots does not.
func (d Decision) Headline() string {
	s := d.ChecksSummary
	switch {
	case s.Truncated:
		return "This PR has more checks than Heimdallm can read in one pass, so its merge state cannot be confirmed."
	case len(s.MissingRequired) > 0:
		return fmt.Sprintf("Waiting for %s to report — branch protection requires %s but %s not run.",
			pluralise(len(s.MissingRequired), "a required check", "required checks"),
			joinNames(s.MissingRequired),
			pluralise(len(s.MissingRequired), "it has", "they have"))
	case s.RequiredFailing > 0:
		return fmt.Sprintf("This PR cannot be merged: %d of the %d required checks %s failing.",
			s.RequiredFailing, s.RequiredTotal, pluralise(s.RequiredFailing, "is", "are"))
	case s.RequiredPending > 0:
		return fmt.Sprintf("Waiting on %d required %s. The PR merges on its own once they pass.",
			s.RequiredPending, pluralise(s.RequiredPending, "check", "checks"))
	case s.OptionalFailing > 0 && s.RequiredTotal > 0:
		return fmt.Sprintf("All %d required checks passed. %d optional %s failing, which does not block the merge.",
			s.RequiredTotal, s.OptionalFailing, pluralise(s.OptionalFailing, "check is", "checks are"))
	case s.Total == 0:
		return "This PR has no checks configured."
	default:
		return fmt.Sprintf("All %d checks passed.", s.Total)
	}
}

// Input is everything Evaluate and Decide need besides the GitHub snapshot.
type Input struct {
	Cfg         config.MergeTrackingConfig
	ViewerLogin string
	State       store.MergeTracking
	Now         time.Time
	// TickStart is when the current reconciliation cycle began. It is what
	// makes "arm auto-merge, then merge on a later pass" precise: an auto-merge
	// armed during this very cycle must not be promoted to a direct merge in
	// the same cycle.
	TickStart time.Time
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func pluralise(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// joinNames renders up to three names, summarising the rest, so a headline
// stays a sentence instead of becoming a list dump.
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	case 3:
		return names[0] + ", " + names[1] + " and " + names[2]
	default:
		return fmt.Sprintf("%s, %s and %d more", names[0], names[1], len(names)-2)
	}
}
