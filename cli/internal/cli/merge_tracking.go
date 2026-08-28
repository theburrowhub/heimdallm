package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// newMergesCmd lists the PRs merge tracking is watching.
func newMergesCmd() *cobra.Command {
	var repo string
	var jsonOutput bool
	var blockedOnly bool

	cmd := &cobra.Command{
		Use:   "merges",
		Short: "List your PRs and what is blocking each merge",
		Long: "Lists the open pull requests you authored or are assigned to, " +
			"with the merge-readiness state Heimdallm recorded for each.\n\n" +
			"PRs blocked by CI are listed first and marked, because those are " +
			"the ones that need you.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			entries, err := c.ListMergeTracking()
			if err != nil {
				return fmt.Errorf("fetching merge tracking: %w", err)
			}

			if repo != "" {
				filtered := entries[:0]
				for _, e := range entries {
					if strings.EqualFold(e.Repo, repo) {
						filtered = append(filtered, e)
					}
				}
				entries = filtered
			}
			if blockedOnly {
				filtered := entries[:0]
				for _, e := range entries {
					if !e.Terminal() && e.BlockReason != "" {
						filtered = append(filtered, e)
					}
				}
				entries = filtered
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No pull requests tracked.")
				return nil
			}
			printMergeTable(entries)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Filter by repository (owner/name)")
	cmd.Flags().BoolVar(&blockedOnly, "blocked", false, "Only PRs that are not merging")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output raw JSON")
	return cmd
}

// newMergeDetailCmd shows the per-check breakdown for one tracked PR.
func newMergeDetailCmd() *cobra.Command {
	var jsonOutput bool
	var recheck bool

	cmd := &cobra.Command{
		Use:   "merge <pr-id>",
		Short: "Show why a PR is or is not merging, check by check",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid PR id %q", args[0])
			}
			c := clientFromContext(cmd.Context())

			var entry *api.MergeTrackingEntry
			if recheck {
				// Always a dry run: this command answers a question, it does
				// not authorise a merge the operator did not configure.
				entry, err = c.EvaluateMergeTracking(prID, true)
			} else {
				entry, err = c.GetMergeTracking(prID)
			}
			if err != nil {
				return fmt.Errorf("fetching merge tracking for PR %d: %w", prID, err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(entry)
			}
			printMergeDetail(entry)
			return nil
		},
	}
	cmd.Flags().BoolVar(&recheck, "recheck", false, "Re-read the PR from GitHub before reporting")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output raw JSON")
	return cmd
}

// printMergeTable renders the listing. Rows blocked by CI carry a marker and
// their full detail text, untruncated: the whole value of the line is the name
// of the check that is failing.
func printMergeTable(entries []api.MergeTrackingEntry) {
	fmt.Printf("%-28s %-6s %-18s %s\n", "REPO", "PR", "STATE", "BLOCKED BY")
	for _, e := range entries {
		marker := "  "
		switch {
		case e.ChecksRequiredFailing > 0:
			marker = "! "
		case e.ChecksRequiredPending > 0:
			marker = "~ "
		}
		blocked := e.BlockDetail
		if blocked == "" {
			blocked = humanMergeReason(e.BlockReason)
		}
		if e.Terminal() {
			blocked = ""
		}
		fmt.Printf("%s%-26s %-6d %-18s %s\n",
			marker, truncate(e.Repo, 26), e.Number, e.Phase, blocked)
	}
	fmt.Println()
	fmt.Println("  ! required check failing    ~ required check running")
}

// printMergeDetail renders one PR's full breakdown.
func printMergeDetail(e *api.MergeTrackingEntry) {
	fmt.Printf("%s#%d  %s\n", e.Repo, e.Number, e.Title)
	if e.URL != "" {
		fmt.Println(e.URL)
	}
	fmt.Println()
	fmt.Printf("State:   %s\n", e.Phase)
	if e.HeadRef != "" {
		fmt.Printf("Branch:  %s → %s\n", e.HeadRef, e.BaseRef)
	}
	if e.BlockReason != "" {
		detail := e.BlockDetail
		if detail == "" {
			detail = humanMergeReason(e.BlockReason)
		}
		fmt.Printf("Blocked: %s\n", detail)
	}
	if e.PreRebaseSHA != "" {
		fmt.Printf("Before Heimdallm rewrote this branch it was at %s\n", e.PreRebaseSHA)
	}
	if e.LastError != "" {
		fmt.Printf("Error:   %s\n", e.LastError)
	}

	if e.Decision == nil {
		fmt.Println("\nHeimdallm has not evaluated this PR yet.")
		return
	}

	s := e.Decision.ChecksSummary
	fmt.Println()
	fmt.Println(mergeChecksHeadline(s))

	if len(e.Decision.Checks) == 0 {
		return
	}
	fmt.Println()
	for _, c := range e.Decision.Checks {
		req := " "
		if c.Required {
			req = "*"
		}
		label := c.Name
		if c.App != "" {
			label += " (" + c.App + ")"
		}
		fmt.Printf("  %s %s %-44s %s\n", checkStateGlyph(c.State), req, truncate(label, 44), c.URL)
	}
	fmt.Println()
	fmt.Println("  * required    ✓ passed  ✕ failed  … running  – skipped")
}

// mergeChecksHeadline mirrors the sentence the GUI shows, so an operator gets
// the same explanation whichever surface they are on.
func mergeChecksHeadline(s api.MergeChecksSummary) string {
	switch {
	case s.Truncated:
		return "This PR has more checks than Heimdallm can read in one pass, so its merge state cannot be confirmed."
	case len(s.MissingRequired) > 0:
		return fmt.Sprintf("Waiting for required checks that have not run: %s",
			strings.Join(s.MissingRequired, ", "))
	case s.RequiredFailing > 0:
		return fmt.Sprintf("This PR cannot be merged: %d of the %d required checks %s failing.",
			s.RequiredFailing, s.RequiredTotal, plural(s.RequiredFailing, "is", "are"))
	case s.RequiredPending > 0:
		return fmt.Sprintf("Waiting on %d required %s. The PR merges on its own once they pass.",
			s.RequiredPending, plural(s.RequiredPending, "check", "checks"))
	case s.OptionalFailing > 0 && s.RequiredTotal > 0:
		return fmt.Sprintf("All %d required checks passed. %d optional %s failing, which does not block the merge.",
			s.RequiredTotal, s.OptionalFailing, plural(s.OptionalFailing, "check is", "checks are"))
	case s.Total == 0:
		return "This PR has no checks configured."
	default:
		return fmt.Sprintf("All %d checks passed.", s.Total)
	}
}

func checkStateGlyph(state string) string {
	switch state {
	case "success":
		return "✓"
	case "failure":
		return "✕"
	case "pending":
		return "…"
	default:
		return "–"
	}
}

// humanMergeReason renders a stable reason code as a sentence. The codes are
// internal identifiers; showing `blocked_by_protection` to an operator is
// showing them our enum.
func humanMergeReason(reason string) string {
	switch reason {
	case "":
		return ""
	case "draft":
		return "draft — Heimdallm never acts on drafts"
	case "conflicts":
		return "conflicts with the base branch"
	case "behind_base":
		return "behind the base branch"
	case "changes_requested":
		return "a reviewer requested changes"
	case "review_required":
		return "an approving review is required"
	case "insufficient_approvals":
		return "not enough approvals for the current commit"
	case "pending_reviewers":
		return "waiting on requested reviewers"
	case "unresolved_threads":
		return "unresolved review conversations"
	case "checks_failing":
		return "a required check is failing"
	case "checks_pending":
		return "required checks are still running"
	case "required_check_missing":
		return "a required check has not reported"
	case "mergeability_unknown":
		return "GitHub is still computing mergeability"
	case "blocked_by_protection":
		return "blocked by branch protection"
	case "in_merge_queue":
		return "in the merge queue"
	case "merge_queue_configured":
		return "the base branch uses a merge queue"
	case "cross_fork":
		return "the head branch lives in another fork"
	case "insufficient_permission":
		return "no write access to this repository"
	case "merge_method_not_allowed":
		return "the configured merge method is disabled for this repo"
	case "automerge_waiting":
		return "auto-merge armed — waiting for GitHub"
	case "head_sha_moved":
		return "a commit landed while Heimdallm was working"
	case "cooldown":
		return "cooling down after a failed attempt"
	case "attempt_cap_reached":
		return "attempt limit reached for this commit"
	case "excluded":
		return "excluded from automation"
	case "disabled":
		return "automation disabled for this repository"
	default:
		return strings.ReplaceAll(reason, "_", " ")
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
