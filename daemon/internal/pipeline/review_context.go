package pipeline

import (
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
