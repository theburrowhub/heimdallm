package pipeline

// SkipReason is the enumerated reason a PR was skipped by Evaluate.
// The empty string means "no skip; run the review".
type SkipReason string

const (
	SkipReasonNone         SkipReason = ""
	SkipReasonNotOpen      SkipReason = "not_open"
	SkipReasonDraft        SkipReason = "draft"
	SkipReasonSelfAuthored SkipReason = "self_authored"
)

// PRGate is the minimal PR view the guard evaluator needs. Callers synthesize
// this from whatever source they have (Tier 2 search results, Tier 3 snapshot)
// so this package does not need to import the scheduler or GitHub packages.
type PRGate struct {
	State  string
	Draft  bool
	Author string
}

// GateConfig controls which *policy* guards apply. Fields default to false;
// callers are expected to build this via config.ReviewGuards so defaults are
// applied once at the config edge.
//
// There is deliberately no SkipNotOpen toggle: closed/merged PRs are ALWAYS
// skipped because reviewing them cannot be a valid configuration — the review
// API rejects them and any AI tokens spent would be wasted. not_open is the
// correctness guard; the policy toggles below govern draft and self-author.
type GateConfig struct {
	SkipDrafts     bool
	SkipSelfAuthor bool
	// BotLogin is the daemon's own GitHub login. Empty disables the
	// self-author check regardless of SkipSelfAuthor — there is nothing safe
	// to compare against.
	BotLogin string
}

// Evaluate returns the first applicable skip reason, or SkipReasonNone.
// Priority order: not_open > draft > self_authored. not_open wins because it
// is the correctness guard — the other two are policy (configurable via
// GateConfig) while not_open is unconditional.
func Evaluate(pr PRGate, cfg GateConfig) SkipReason {
	if pr.State != "open" {
		return SkipReasonNotOpen
	}
	if cfg.SkipDrafts && pr.Draft {
		return SkipReasonDraft
	}
	if cfg.SkipSelfAuthor && cfg.BotLogin != "" && pr.Author == cfg.BotLogin {
		return SkipReasonSelfAuthored
	}
	return SkipReasonNone
}
