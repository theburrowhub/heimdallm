package issues

import (
	"strings"

	"github.com/heimdallm/daemon/internal/github"
)

const (
	MarkerDone  = "<!-- heimdallm:done -->"
	MarkerSkip  = "<!-- heimdallm:skip -->"
	MarkerRetry = "<!-- heimdallm:retry -->"
)

// MarkerResult represents the effective control marker found in comments.
type MarkerResult int

const (
	MarkerNone        MarkerResult = iota
	MarkerResultDone               // issue processed, skip
	MarkerResultSkip               // permanent skip
	MarkerResultRetry              // force reprocess
)

// ScanMarkers scans a chronologically sorted comment slice for control markers
// and returns the effective result. The latest marker wins: if a comment
// contains retry after an earlier done or skip, the issue is reprocessed.
// Within a single comment, priority is retry > skip > done.
func ScanMarkers(comments []github.Comment) MarkerResult {
	var latest MarkerResult
	for _, c := range comments {
		switch {
		case strings.Contains(c.Body, MarkerRetry):
			latest = MarkerResultRetry
		case strings.Contains(c.Body, MarkerSkip):
			latest = MarkerResultSkip
		case strings.Contains(c.Body, MarkerDone):
			latest = MarkerResultDone
		}
	}
	return latest
}
