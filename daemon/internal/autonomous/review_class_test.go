package autonomous

import "testing"

func TestClassifyReview(t *testing.T) {
	cases := []struct {
		name    string
		reviews []ReviewInput
		want    ReviewDecision
	}{
		{"changes requested", []ReviewInput{{State: "CHANGES_REQUESTED"}}, DecisionFix},
		{"commented only", []ReviewInput{{State: "COMMENTED", Body: "nit: rename"}}, DecisionFix},
		{"approved clean", []ReviewInput{{State: "APPROVED", Body: "LGTM"}}, DecisionMergeGate},
		{"approved with unresolved comments", []ReviewInput{{State: "APPROVED", Body: "LGTM", UnresolvedComments: 2}}, DecisionFix},
		{"approved with actionable body", []ReviewInput{{State: "APPROVED", Body: "please rename foo before merge"}}, DecisionFix},
		{"no human reviews", []ReviewInput{}, DecisionWait},
		{"latest approved supersedes older changes", []ReviewInput{{State: "CHANGES_REQUESTED"}, {State: "APPROVED", Body: "LGTM"}}, DecisionMergeGate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyReview(tc.reviews); got != tc.want {
				t.Errorf("ClassifyReview = %v, want %v", got, tc.want)
			}
		})
	}
}
