package bus

import "encoding/json"

// DiscoveryMsg is published by the discovery poller on SubjDiscoveryRepos.
type DiscoveryMsg struct {
	Repos []string `json:"repos"`
}

// PRReviewMsg is published by the PR poller on SubjPRReview.
// The Nats-Msg-Id for dedup is "{GithubID}:{HeadSHA}".
type PRReviewMsg struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	GithubID int64  `json:"github_id"`
	HeadSHA  string `json:"head_sha"`
}

// PRPublishMsg is published by the review worker on SubjPRPublish
// after a review completes, consumed by the publish worker.
type PRPublishMsg struct {
	ReviewID int64 `json:"review_id"`
}

// IssueMsg is published on issue workflow subjects. The subject distinguishes
// triage, refinement, and implement; the payload is the same.
type IssueMsg struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	GithubID int64  `json:"github_id"`
}

// StateCheckMsg is published by the state poller on SubjStateCheck.
type StateCheckMsg struct {
	Type     string `json:"type"` // "pr" or "issue"
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	GithubID int64  `json:"github_id"`
}

// EventMsg is the generic envelope for SSE bridge events published
// on heimdallm.events.* subjects. Data uses map[string]any to match
// the existing sseData() pattern in main.go.
type EventMsg struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Encode marshals any message type to JSON bytes.
func Encode(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Decode unmarshals JSON bytes into the target message type.
func Decode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
