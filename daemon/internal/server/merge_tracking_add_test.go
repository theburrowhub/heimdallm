package server_test

// POST /prs/add routes through the review pipeline, which refuses a PR the
// authenticated account authored — and Heimdallm authenticates as the
// operator's own account, so that is every PR they open. Merge tracking exists
// for precisely those PRs and had no way to add one: pasting a link into the
// only button available produced a `self_authored` skip and nothing else.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/store"
)

func postJSON(t *testing.T, srv *server.Server, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// wireAdd gives the server the two callbacks the endpoint needs and reports
// what they were asked to do.
type addCalls struct {
	fetched  []string
	enrolled []int64
}

func wireAdd(srv *server.Server, s *store.Store, calls *addCalls, enrolErr error) {
	srv.SetAddPRFn(func(repo string, number int) (*store.PR, error) {
		calls.fetched = append(calls.fetched, repo)
		now := time.Now().UTC()
		id, err := s.UpsertPR(&store.PR{
			GithubID: int64(9000 + number), Repo: repo, Number: number,
			Title: "Mine", Author: "octocat", State: "open",
			URL: "https://github.com/" + repo + "/pull/1", UpdatedAt: now, FetchedAt: now,
		})
		if err != nil {
			return nil, err
		}
		return &store.PR{ID: id, Repo: repo, Number: number}, nil
	})
	srv.SetMergeTrackEnrolFn(func(prID int64, repo string, number int) error {
		calls.enrolled = append(calls.enrolled, prID)
		if enrolErr != nil {
			return enrolErr
		}
		_, err := s.EnsureMergeTracking(prID, repo, number)
		return err
	})
}

func TestHandleAddMergeTracking_EnrolsWithoutTriggeringAReview(t *testing.T) {
	srv, s, _ := newMergeTrackingServer(t)
	var calls addCalls
	wireAdd(srv, s, &calls, nil)
	// The review trigger is deliberately left unwired: touching it would panic,
	// which is the assertion that this path never reaches the review pipeline.

	code, body := postJSON(t, srv, "/merge-tracking/add",
		`{"url":"https://github.com/acme/widgets/pull/42"}`)
	if code != http.StatusAccepted && code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if len(calls.fetched) != 1 || calls.fetched[0] != "acme/widgets" {
		t.Errorf("fetched = %v, want the PR validated against GitHub first", calls.fetched)
	}
	if len(calls.enrolled) != 1 {
		t.Fatalf("enrolled = %v, want exactly one enrolment", calls.enrolled)
	}

	var entry map[string]any
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if entry["repo"] != "acme/widgets" {
		t.Errorf("response = %s, want the tracked row so the tab can render it", body)
	}
}

// A typo must not leave a repository monitored forever, so the PR is validated
// against GitHub before anything is written.
func TestHandleAddMergeTracking_ReportsAFetchFailureWithoutEnrolling(t *testing.T) {
	srv, s, _ := newMergeTrackingServer(t)
	var calls addCalls
	wireAdd(srv, s, &calls, nil)
	srv.SetAddPRFn(func(string, int) (*store.PR, error) {
		return nil, errors.New("404 Not Found")
	})

	code, body := postJSON(t, srv, "/merge-tracking/add",
		`{"url":"https://github.com/acme/widgets/pull/42"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if len(calls.enrolled) != 0 {
		t.Error("nothing may be enrolled when the PR could not be read")
	}
	if !strings.Contains(string(body), "404") {
		t.Errorf("body = %s, want GitHub's own complaint", body)
	}
}

// The daemon refuses a repo with merge tracking switched off, and the dialog
// shows that sentence verbatim.
func TestHandleAddMergeTracking_SurfacesAnEnrolmentRefusal(t *testing.T) {
	srv, s, _ := newMergeTrackingServer(t)
	var calls addCalls
	wireAdd(srv, s, &calls, errors.New("merge tracking is disabled for acme/widgets"))

	code, body := postJSON(t, srv, "/merge-tracking/add",
		`{"url":"https://github.com/acme/widgets/pull/42"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if !strings.Contains(string(body), "disabled for acme/widgets") {
		t.Errorf("body = %s, want the reason", body)
	}
}

func TestHandleAddMergeTracking_RejectsJunk(t *testing.T) {
	srv, s, _ := newMergeTrackingServer(t)
	var calls addCalls
	wireAdd(srv, s, &calls, nil)

	for name, body := range map[string]string{
		"not json":   `nope`,
		"not a URL":  `{"url":"nope"}`,
		"not github": `{"url":"https://gitlab.com/a/b/pull/1"}`,
		"not a PR":   `{"url":"https://github.com/acme/widgets/issues/42"}`,
		"empty":      `{"url":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := postJSON(t, srv, "/merge-tracking/add", body)
			if code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", code)
			}
		})
	}
	if len(calls.fetched) != 0 {
		t.Error("a malformed request must not reach GitHub")
	}
}

// Without the callbacks wired the endpoint says so rather than panicking.
func TestHandleAddMergeTracking_UnwiredAnswers503(t *testing.T) {
	srv, _, _ := newMergeTrackingServer(t)
	code, _ := postJSON(t, srv, "/merge-tracking/add",
		`{"url":"https://github.com/acme/widgets/pull/42"}`)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
}

// Discovery intersects with the monitored list, so a PR whose repository is not
// monitored would be enrolled and then dropped on the very next cycle.
func TestHandleAddMergeTracking_MonitorsTheRepository(t *testing.T) {
	srv, s, _ := newMergeTrackingServer(t)
	var calls addCalls
	wireAdd(srv, s, &calls, nil)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[ai]\nprimary = \"claude\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	srv.SetConfigPath(cfgPath)
	srv.SetReloadFn(func() error { return nil })

	code, body := postJSON(t, srv, "/merge-tracking/add",
		`{"url":"https://github.com/acme/widgets/pull/42"}`)
	if code != http.StatusAccepted && code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}

	written, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(written), "acme/widgets") {
		t.Errorf("config = %s, want the repository monitored", written)
	}
}
