package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

// partitionComments splits comments into those created before and after the cutoff timestamp.
func partitionComments(comments []github.Comment, cutoff time.Time) (before, after []github.Comment) {
	for _, c := range comments {
		if c.CreatedAt.After(cutoff) {
			after = append(after, c)
		} else {
			before = append(before, c)
		}
	}
	return
}

// reviewIssue is the minimal struct for deserializing previous review issues from JSON.
type reviewIssue struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// buildReviewContext creates a structured prompt section for re-reviews.
// Returns empty string for first-time reviews (no previous review exists).
func buildReviewContext(prevIssuesJSON, prevSeverity string, lastReviewAt time.Time, comments []github.Comment, botLogin, prAuthor string) string {
	if prevIssuesJSON == "" && lastReviewAt.IsZero() {
		return ""
	}

	var b strings.Builder

	b.WriteString("IMPORTANT: This is a RE-REVIEW. You previously reviewed this PR.\n")
	b.WriteString("Your previous findings and the discussion since then are shown below.\n")
	b.WriteString("- Do NOT repeat findings that the author has addressed (check the diff for changes)\n")
	b.WriteString("- Only re-flag a finding if the code is STILL unchanged despite the feedback\n")
	b.WriteString("- Focus on NEW changes since the last review\n\n")

	var issues []reviewIssue
	if prevIssuesJSON != "" {
		json.Unmarshal([]byte(prevIssuesJSON), &issues)
	}

	if len(issues) > 0 {
		b.WriteString(fmt.Sprintf("## Your previous review (severity: %s)\n\n", prevSeverity))
		b.WriteString("Previous findings:\n")
		for _, iss := range issues {
			b.WriteString(fmt.Sprintf("- [%s] %s:%d — %s\n", strings.ToUpper(iss.Severity), iss.File, iss.Line, iss.Description))
		}
		b.WriteString("\n")
	}

	_, afterComments := partitionComments(comments, lastReviewAt)

	var newDiscussion []github.Comment
	for _, c := range afterComments {
		if !strings.EqualFold(c.Author, botLogin) {
			newDiscussion = append(newDiscussion, c)
		}
	}

	if len(newDiscussion) > 0 {
		b.WriteString("## Discussion since last review\n\n")
		for _, c := range newDiscussion {
			if c.File != "" {
				b.WriteString(fmt.Sprintf("@%s (%s:%d): %s\n", c.Author, c.File, c.Line, c.Body))
			} else {
				b.WriteString(fmt.Sprintf("@%s: %s\n", c.Author, c.Body))
			}
		}
		b.WriteString("\n")

		// Inject unresolved concern signal so the AI explicitly considers it.
		signals := ExtractCommentSignals(newDiscussion, prAuthor)
		if signals.UnresolvedConcerns > 0 {
			b.WriteString(fmt.Sprintf(
				"NOTE: There are %d unresolved reviewer concern(s) in the discussion above. "+
					"Verify whether the current diff addresses them. If a concern about a real defect "+
					"remains unaddressed, reflect this in your severity assessment.\n\n",
				signals.UnresolvedConcerns))
		}
	}

	return b.String()
}
