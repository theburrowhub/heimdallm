package mergetrack

import (
	"sort"
	"strings"

	gh "github.com/heimdallm/daemon/internal/github"
)

// summariseChecks classifies every check on the head commit and orders the list
// for presentation.
//
// Two things here are load-bearing beyond the counting:
//
// Required-ness comes from two sources, and both matter. GitHub's
// isRequired(pullRequestNumber:) covers the normal case, but a context listed in
// requiredStatusCheckContexts that has never reported does not appear in the
// rollup at all — no row, nothing red, nothing to notice. Those are collected
// into MissingRequired, because a merge waiting on a check that never started
// is otherwise invisible.
//
// The ordering puts required failures first, then required pending, then
// optional failures, then everything else, alphabetically within each group.
// The listing and the check table both render in this order, so the thing
// blocking the merge is the first row a reader sees.
func summariseChecks(st *gh.MergeStatus) ([]gh.CheckContext, ChecksSummary) {
	var required []string
	if st.Protection != nil {
		required = st.Protection.RequiredStatusCheckContexts
	}
	requiredByName := make(map[string]struct{}, len(required))
	for _, name := range required {
		if n := strings.TrimSpace(name); n != "" {
			requiredByName[n] = struct{}{}
		}
	}

	checks := make([]gh.CheckContext, 0, len(st.Checks))
	seen := make(map[string]struct{}, len(st.Checks))
	summary := ChecksSummary{Truncated: st.ChecksTruncated}

	for _, c := range st.Checks {
		// A nameless check would render as a blank row and could not be acted
		// on. The GitHub decoder already drops these; this is the guard at the
		// layer that actually produces the display list.
		if strings.TrimSpace(c.Name) == "" {
			continue
		}
		// A context named by branch protection is required even if GitHub's
		// isRequired said otherwise (it can lag a rule change).
		if _, ok := requiredByName[c.Name]; ok {
			c.Required = true
		}
		seen[c.Name] = struct{}{}
		checks = append(checks, c)

		summary.Total++
		if !c.Required {
			if c.State == gh.CheckStateFailure {
				summary.OptionalFailing++
			}
			continue
		}
		summary.RequiredTotal++
		switch c.State {
		case gh.CheckStateSuccess, gh.CheckStateNeutral:
			summary.RequiredSuccess++
		case gh.CheckStatePending:
			summary.RequiredPending++
		case gh.CheckStateFailure:
			summary.RequiredFailing++
		}
	}

	// Required contexts that never reported.
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; !ok {
			summary.MissingRequired = append(summary.MissingRequired, name)
			summary.RequiredTotal++
		}
	}
	sort.Strings(summary.MissingRequired)

	sortChecksForDisplay(checks)
	return checks, summary
}

// checkGroup ranks a check for display: lower sorts first.
func checkGroup(c gh.CheckContext) int {
	switch {
	case c.Required && c.State == gh.CheckStateFailure:
		return 0
	case c.Required && c.State == gh.CheckStatePending:
		return 1
	case !c.Required && c.State == gh.CheckStateFailure:
		return 2
	case c.Required:
		return 3
	default:
		return 4
	}
}

func sortChecksForDisplay(checks []gh.CheckContext) {
	sort.SliceStable(checks, func(i, j int) bool {
		gi, gj := checkGroup(checks[i]), checkGroup(checks[j])
		if gi != gj {
			return gi < gj
		}
		return strings.ToLower(checks[i].Name) < strings.ToLower(checks[j].Name)
	})
}

// requiredCheckNames returns the names of required checks in the given state,
// ordered as displayed, for use in a block's Detail text.
func requiredCheckNames(checks []gh.CheckContext, state gh.CheckState) []string {
	var out []string
	for _, c := range checks {
		if c.Required && c.State == state {
			out = append(out, checkLabel(c))
		}
	}
	return out
}

// checkLabel renders a check for human text: the name, plus the app that runs
// it when known, because "build" alone is ambiguous in a repo with several CI
// providers.
func checkLabel(c gh.CheckContext) string {
	if strings.TrimSpace(c.App) == "" {
		return c.Name
	}
	return c.Name + " (" + c.App + ")"
}
