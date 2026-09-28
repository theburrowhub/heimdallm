package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/store"
)

// publishWorkerTestTimeout mirrors lifecycleTestTimeout but doubled: this
// fixture waits on an extra hop (the NATS publish-worker consuming the
// message PublishPending enqueues) beyond what the plain lifecycle fixture
// waits for.
const publishWorkerTestTimeout = 20 * time.Second

// startPublishWorkerFixture boots the real daemon (runProcessWithDependencies,
// the same entry point production uses) against a fake GitHub server, with one
// unpublished review already sitting in the store. It exists to close a real
// coverage gap: the NATS publish-worker closure in runProcessWithDependencies
// (the retry path PublishPending's queued rows take to actually reach GitHub)
// had no test anywhere touching its GitHub-submit statements — confirmed by
// diffing coverage against origin/main before this test was added — because
// every existing test drives either the producer side (PublishPending
// enqueueing to NATS) or the pipeline's own in-process Run/PublishPending
// paths, never the worker that consumes the queued message end-to-end.
//
// The review is stored with the legacy Event value "COMMENT", the exact
// scenario PublishEventFor's remap exists for (see pipeline.PublishEventFor):
// a review persisted before never_approve_with_issues stopped downgrading to
// COMMENT must still be retried as REQUEST_CHANGES, not resubmitted verbatim.
func startPublishWorkerFixture(t *testing.T) (submittedReviews <-chan submittedReviewBody, prID, reviewID int64) {
	t.Helper()
	dataDir := t.TempDir()
	localDirBase := filepath.Join(dataDir, "repos")
	if err := os.MkdirAll(localDirBase, 0o700); err != nil {
		t.Fatalf("create local dir base: %v", err)
	}

	configPath := filepath.Join(dataDir, "config.toml")
	const headSHA = "deadbeef1234"
	body := `[server]
port = 0
bind_addr = "127.0.0.1"
max_concurrent_workers = 1

[github]
poll_interval = "1h"
repositories = ["org/repo"]
local_dir_base = [` + strconv.Quote(localDirBase) + `]

[ai]
primary = "codex"
fallback = "claude"
repo_rename_check_interval = "0"

[activity_log]
enabled = true
retention_days = 1

[polling]
discovery_interval = "1h"
tier3_interval = "1h"

[retention]
max_days = 1
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write publish-worker config: %v", err)
	}

	s, err := store.Open(filepath.Join(dataDir, "heimdallm.db"))
	if err != nil {
		t.Fatalf("open publish-worker store: %v", err)
	}
	now := time.Now().UTC()
	prID, err = s.UpsertPR(&store.PR{
		GithubID:  101,
		Repo:      "org/repo",
		Number:    1,
		Title:     "test PR",
		Author:    "alice",
		URL:       "https://example.invalid/org/repo/pull/1",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		s.Close()
		t.Fatalf("seed PR: %v", err)
	}
	reviewID, err = s.InsertReview(&store.Review{
		PRID:           prID,
		CLIUsed:        "codex",
		Summary:        "A finding was raised.",
		Issues:         "[]",
		Suggestions:    "[]",
		Severity:       "medium",
		Event:          "COMMENT", // legacy value PublishEventFor must remap
		CreatedAt:      now,
		GitHubReviewID: 0, // unpublished — PublishPending picks it up
		HeadSHA:        headSHA,
	})
	if err != nil {
		s.Close()
		t.Fatalf("seed pending review: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close publish-worker store: %v", err)
	}

	reviews := make(chan submittedReviewBody, 4)
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			_, _ = io.WriteString(w, `{"login":"heimdallm-test"}`)
		case r.URL.Path == "/repos/org/repo/pulls/1" && r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"number":1,"state":"open","head":{"sha":%q}}`, headSHA)
		case r.URL.Path == "/repos/org/repo/pulls/1/reviews" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `[]`) // no peer review already published
		case r.URL.Path == "/repos/org/repo/pulls/1/reviews" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var decoded submittedReviewBody
			if err := json.Unmarshal(raw, &decoded); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case reviews <- decoded:
			default:
			}
			_, _ = io.WriteString(w, `{"id":999,"state":"CHANGES_REQUESTED"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"not found"}`)
		}
	}))

	t.Setenv("HEIMDALLM_DATA_DIR", dataDir)
	t.Setenv("HEIMDALLM_CONFIG_PATH", configPath)
	t.Setenv("GITHUB_TOKEN", "publish-worker-test-token")
	originalArgs := os.Args
	os.Args = []string{"heimdallm"}

	listenerCh := make(chan net.Listener, 1)
	deps := processDependencies{
		newGitHubClient: func(token string, _ ...gh.Option) *gh.Client {
			return gh.NewClient(token, gh.WithBaseURL(fakeGitHub.URL))
		},
		listen: func(_ int, bindAddr string) (net.Listener, error) {
			ln, err := server.Listen(0, bindAddr)
			if err == nil {
				listenerCh <- ln
			}
			return ln, err
		},
	}
	result := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		result <- runProcessWithDependencies(true, deps)
		close(done)
	}()

	var listener net.Listener
	var once sync.Once
	select {
	case listener = <-listenerCh:
	case <-time.After(publishWorkerTestTimeout):
		fakeGitHub.Close()
		os.Args = originalArgs
		t.Fatal("daemon did not claim its test listener")
	}

	t.Cleanup(func() {
		once.Do(func() {
			select {
			case <-done:
			default:
				_ = listener.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
				}
			}
			fakeGitHub.Close()
			os.Args = originalArgs
		})
	})

	baseURL := "http://" + listener.Addr().String()
	waitForReadyHealth(t, baseURL)

	return reviews, prID, reviewID
}

type submittedReviewBody struct {
	Body     string `json:"body"`
	Event    string `json:"event"`
	CommitID string `json:"commit_id"`
}

// TestRunProcess_PublishWorkerSubmitsPersistedEventForPendingReview drives a
// pending review through the real boot sequence — PublishPending enqueues it
// over NATS, and the publish-worker closure (not exercised by any other test)
// consumes it, fetches the live PR snapshot, and submits the review to
// GitHub. It must submit REQUEST_CHANGES, remapped from the row's legacy
// stored "COMMENT" event by PublishEventFor, anchored to the analysed commit,
// and with the plain body BuildGitHubBody produces — no downgrade note, since
// AnnotateBodyForEvent no longer exists.
func TestRunProcess_PublishWorkerSubmitsPersistedEventForPendingReview(t *testing.T) {
	submittedReviews, _, _ := startPublishWorkerFixture(t)

	select {
	case got := <-submittedReviews:
		if got.Event != "REQUEST_CHANGES" {
			t.Errorf("submitted event = %q, want REQUEST_CHANGES (legacy COMMENT remapped)", got.Event)
		}
		if got.CommitID != "deadbeef1234" {
			t.Errorf("submitted commit_id = %q, want the analysed HEAD SHA", got.CommitID)
		}
		if got.Body == "" {
			t.Error("submitted body is empty")
		}
		for _, stale := range []string{"Not approving", "posted as a comment", "never_approve_with_issues"} {
			if strings.Contains(got.Body, stale) {
				t.Errorf("submitted body still carries the retired downgrade note (%q): %q", stale, got.Body)
			}
		}
	case <-time.After(publishWorkerTestTimeout):
		t.Fatal("publish-worker never submitted the pending review to GitHub")
	}
}
