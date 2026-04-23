package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultHost = "http://localhost:7842"

type Client struct {
	Host       string
	Token      string
	HTTPClient *http.Client
}

func New(host, token string) *Client {
	if host == "" {
		host = DefaultHost
	}
	return &Client{
		Host:  strings.TrimRight(host, "/"),
		Token: token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, c.Host+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("X-Heimdallm-Token", c.Token)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (c *Client) do(method, path string) ([]byte, error) {
	req, err := c.newRequest(method, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Health checks daemon connectivity.
func (c *Client) Health() error {
	_, err := c.do("GET", "/health")
	return err
}

// PR types matching daemon API responses.

type Review struct {
	ID                int64     `json:"id"`
	PRID              int64     `json:"pr_id"`
	CLIUsed           string    `json:"cli_used"`
	Summary           string    `json:"summary"`
	Issues            string    `json:"issues"`
	Suggestions       string    `json:"suggestions"`
	Severity          string    `json:"severity"`
	CreatedAt         time.Time `json:"created_at"`
	GitHubReviewID    int64     `json:"github_review_id"`
	GitHubReviewState string    `json:"github_review_state"`
	HeadSHA           string    `json:"head_sha"`
}

type PR struct {
	ID           int64     `json:"id"`
	GithubID     int64     `json:"github_id"`
	Repo         string    `json:"repo"`
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	URL          string    `json:"url"`
	State        string    `json:"state"`
	UpdatedAt    time.Time `json:"updated_at"`
	FetchedAt    time.Time `json:"fetched_at"`
	Dismissed    bool      `json:"dismissed"`
	LatestReview *Review   `json:"latest_review,omitempty"`
}

type IssueReview struct {
	ID          int64           `json:"id"`
	IssueID     int64           `json:"issue_id"`
	CLIUsed     string          `json:"cli_used"`
	Summary     string          `json:"summary"`
	Triage      json.RawMessage `json:"triage"`
	Suggestions json.RawMessage `json:"suggestions"`
	ActionTaken string          `json:"action_taken"`
	PRCreated   int             `json:"pr_created"`
	CreatedAt   time.Time       `json:"created_at"`
}

type Issue struct {
	ID           int64           `json:"id"`
	GithubID     int64           `json:"github_id"`
	Repo         string          `json:"repo"`
	Number       int             `json:"number"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	Author       string          `json:"author"`
	Assignees    json.RawMessage `json:"assignees"`
	Labels       json.RawMessage `json:"labels"`
	State        string          `json:"state"`
	CreatedAt    time.Time       `json:"created_at"`
	FetchedAt    time.Time       `json:"fetched_at"`
	Dismissed    bool            `json:"dismissed"`
	LatestReview *IssueReview    `json:"latest_review,omitempty"`
}

type RepoCount struct {
	Repo  string `json:"repo"`
	Count int    `json:"count"`
}

type DayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type ReviewTimingStats struct {
	SampleCount    int     `json:"sample_count"`
	AvgSeconds     float64 `json:"avg_seconds"`
	MedianSeconds  float64 `json:"median_seconds"`
	MinSeconds     float64 `json:"min_seconds"`
	MaxSeconds     float64 `json:"max_seconds"`
	BucketFast     int     `json:"bucket_fast"`
	BucketMedium   int     `json:"bucket_medium"`
	BucketSlow     int     `json:"bucket_slow"`
	BucketVerySlow int     `json:"bucket_very_slow"`
}

type Stats struct {
	TotalReviews       int               `json:"total_reviews"`
	BySeverity         map[string]int    `json:"by_severity"`
	ByCLI              map[string]int    `json:"by_cli"`
	TopRepos           []RepoCount       `json:"top_repos"`
	ReviewsLast7Days   []DayCount        `json:"reviews_last_7_days"`
	AvgIssuesPerReview float64           `json:"avg_issues_per_review"`
	ReviewTiming       ReviewTimingStats `json:"review_timing"`
	ActivityCount24h   int               `json:"activity_count_24h"`
}

type ActivityEntry struct {
	ID         int64          `json:"id"`
	TS         string         `json:"ts"`
	Org        string         `json:"org"`
	Repo       string         `json:"repo"`
	ItemType   string         `json:"item_type"`
	ItemNumber int            `json:"item_number"`
	ItemTitle  string         `json:"item_title"`
	Action     string         `json:"action"`
	Outcome    string         `json:"outcome"`
	Details    map[string]any `json:"details"`
}

type ActivityResponse struct {
	Entries   []ActivityEntry `json:"entries"`
	Count     int             `json:"count"`
	Truncated bool            `json:"truncated"`
}

type SSEEvent struct {
	Type string
	Data string
}

// ListPRs fetches all PRs with their latest review.
func (c *Client) ListPRs() ([]PR, error) {
	data, err := c.do("GET", "/prs")
	if err != nil {
		return nil, err
	}
	var prs []PR
	if err := json.Unmarshal(data, &prs); err != nil {
		return nil, fmt.Errorf("parsing PRs: %w", err)
	}
	return prs, nil
}

// ListIssues fetches all issues with their latest review.
func (c *Client) ListIssues() ([]Issue, error) {
	data, err := c.do("GET", "/issues")
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("parsing issues: %w", err)
	}
	return issues, nil
}

// GetConfig returns the daemon's running configuration.
func (c *Client) GetConfig() (map[string]any, error) {
	data, err := c.do("GET", "/config")
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

// GetStats returns review statistics.
func (c *Client) GetStats() (*Stats, error) {
	data, err := c.do("GET", "/stats")
	if err != nil {
		return nil, err
	}
	var stats Stats
	if err := json.Unmarshal(data, &stats); err != nil {
		return nil, fmt.Errorf("parsing stats: %w", err)
	}
	return &stats, nil
}

// GetActivity returns the activity log.
func (c *Client) GetActivity() (*ActivityResponse, error) {
	data, err := c.do("GET", "/activity")
	if err != nil {
		return nil, err
	}
	var resp ActivityResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing activity: %w", err)
	}
	return &resp, nil
}

// TriggerPRReview queues a review for the given PR ID.
func (c *Client) TriggerPRReview(id int64) error {
	_, err := c.do("POST", fmt.Sprintf("/prs/%d/review", id))
	return err
}

// TriggerIssueReview queues a review for the given issue ID.
func (c *Client) TriggerIssueReview(id int64) error {
	_, err := c.do("POST", fmt.Sprintf("/issues/%d/review", id))
	return err
}

// DismissIssue hides an issue from the pipeline, stopping retries until undismissed.
func (c *Client) DismissIssue(id int64) error {
	_, err := c.do("POST", fmt.Sprintf("/issues/%d/dismiss", id))
	return err
}

// UndismissIssue restores a previously dismissed issue, allowing the pipeline to retry it.
func (c *Client) UndismissIssue(id int64) error {
	_, err := c.do("POST", fmt.Sprintf("/issues/%d/undismiss", id))
	return err
}

// PRDetail is the response from GET /prs/{id}.
type PRDetail struct {
	PR      PR       `json:"pr"`
	Reviews []Review `json:"reviews"`
}

// IssueDetail is the response from GET /issues/{id}.
type IssueDetail struct {
	Issue   Issue         `json:"issue"`
	Reviews []IssueReview `json:"reviews"`
}

// GetPR fetches a single PR with all its reviews.
func (c *Client) GetPR(id int64) (*PRDetail, error) {
	data, err := c.do("GET", fmt.Sprintf("/prs/%d", id))
	if err != nil {
		return nil, err
	}
	var detail PRDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		return nil, fmt.Errorf("parsing PR detail: %w", err)
	}
	return &detail, nil
}

// GetIssue fetches a single issue with all its reviews.
func (c *Client) GetIssue(id int64) (*IssueDetail, error) {
	data, err := c.do("GET", fmt.Sprintf("/issues/%d", id))
	if err != nil {
		return nil, err
	}
	var detail IssueDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		return nil, fmt.Errorf("parsing issue detail: %w", err)
	}
	return &detail, nil
}

// StreamEvents opens an SSE connection and sends events to the provided channel.
// It blocks until the context is cancelled, the connection is closed, or an error occurs.
func (c *Client) StreamEvents(ctx context.Context, events chan<- SSEEvent) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.Host+"/events", nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("X-Heimdallm-Token", c.Token)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{} // no timeout for SSE
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SSE HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType string
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		} else if line == "" && eventType != "" {
			eventData := strings.Join(dataLines, "\n")
			select {
			case events <- SSEEvent{Type: eventType, Data: eventData}:
			case <-ctx.Done():
				return ctx.Err()
			}
			eventType = ""
			dataLines = nil
		}
	}

	if err := scanner.Err(); err != nil {
		// Context cancellation causes the read to fail; don't treat as error.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SSE stream error: %w", err)
	}
	return nil
}
