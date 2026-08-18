package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

const lifecycleTestTimeout = 10 * time.Second

type lifecycleFixture struct {
	dataDir          string
	configPath       string
	configBody       string
	apiBaseURL       string
	listener         net.Listener
	result           <-chan int
	remoteRequest    <-chan string
	pollersRestarted <-chan struct{}
	prID             int64
	issueID          int64
	releaseUser      func()
	done             <-chan struct{}
}

func writeLifecycleConfig(t *testing.T, dataDir, localDirBase, pollInterval string) (string, string) {
	t.Helper()
	configPath := filepath.Join(dataDir, "config.toml")
	body := fmt.Sprintf(`[server]
port = 0
bind_addr = "127.0.0.1"
max_concurrent_workers = 1

[github]
poll_interval = %s
repositories = []
local_dir_base = [%s]

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
`, strconv.Quote(pollInterval), strconv.Quote(localDirBase))
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write lifecycle config: %v", err)
	}
	return configPath, body
}

func seedLifecycleStore(t *testing.T, dataDir string) (int64, int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(dataDir, "heimdallm.db"))
	if err != nil {
		t.Fatalf("open lifecycle store: %v", err)
	}
	now := time.Now().UTC()
	prID, err := s.UpsertPR(&store.PR{
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
	issueID, err := s.UpsertIssue(&store.Issue{
		GithubID:  202,
		Repo:      "org/repo",
		Number:    2,
		Title:     "test issue",
		Author:    "alice",
		State:     "open",
		CreatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		s.Close()
		t.Fatalf("seed issue: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close lifecycle store: %v", err)
	}
	return prID, issueID
}

func startLifecycleFixture(t *testing.T, blockAuthenticatedUser bool) (*lifecycleFixture, <-chan struct{}) {
	t.Helper()
	dataDir := t.TempDir()
	localDirBase := filepath.Join(dataDir, "repos")
	localRepo := filepath.Join(localDirBase, "repo")
	if err := os.MkdirAll(filepath.Join(localRepo, ".git"), 0o700); err != nil {
		t.Fatalf("create local repository marker: %v", err)
	}
	configPath, configBody := writeLifecycleConfig(t, dataDir, localDirBase, "1h")
	prID, issueID := seedLifecycleStore(t, dataDir)

	userRequested := make(chan struct{})
	releaseUser := make(chan struct{})
	var releaseUserOnce sync.Once
	releaseUserFn := func() { releaseUserOnce.Do(func() { close(releaseUser) }) }
	remoteRequests := make(chan string, 32)
	var userOnce sync.Once
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			userOnce.Do(func() { close(userRequested) })
			if blockAuthenticatedUser {
				<-releaseUser
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"login":"heimdallm-test"}`)
			return
		}
		select {
		case remoteRequests <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}))

	t.Setenv("HEIMDALLM_DATA_DIR", dataDir)
	t.Setenv("HEIMDALLM_CONFIG_PATH", configPath)
	t.Setenv("GITHUB_TOKEN", "lifecycle-test-token")
	originalArgs := os.Args
	originalLogger := slog.Default()
	originalVersion := version
	os.Args = []string{"heimdallm"}
	version = "lifecycle-test-version"

	listenerCh := make(chan net.Listener, 1)
	pollersRestarted := make(chan struct{}, 1)
	deps := processDependencies{
		newGitHubClient: func(token string, _ ...gh.Option) *gh.Client {
			return gh.NewClient(token, gh.WithBaseURL(fakeGitHub.URL))
		},
		listen: func(_ int, bindAddr string) (net.Listener, error) {
			// The production listener behavior is covered in package server. This
			// lifecycle harness always claims an ephemeral port so it cannot collide
			// with another test process using the default daemon port.
			ln, err := server.Listen(0, bindAddr)
			if err == nil {
				listenerCh <- ln
			}
			return ln, err
		},
		afterPollerRestart: func() { pollersRestarted <- struct{}{} },
	}
	result := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		result <- runProcessWithDependencies(true, deps)
		close(done)
	}()

	var listener net.Listener
	select {
	case listener = <-listenerCh:
	case <-time.After(lifecycleTestTimeout):
		releaseUserFn()
		fakeGitHub.Close()
		os.Args = originalArgs
		slog.SetDefault(originalLogger)
		version = originalVersion
		t.Fatal("daemon did not claim its test listener")
	}

	fixture := &lifecycleFixture{
		dataDir:          dataDir,
		configPath:       configPath,
		configBody:       configBody,
		apiBaseURL:       "http://" + listener.Addr().String(),
		listener:         listener,
		result:           result,
		remoteRequest:    remoteRequests,
		pollersRestarted: pollersRestarted,
		prID:             prID,
		issueID:          issueID,
		releaseUser:      releaseUserFn,
		done:             done,
	}
	t.Cleanup(func() {
		releaseUserFn()
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
		slog.SetDefault(originalLogger)
		version = originalVersion
	})
	return fixture, userRequested
}

func decodeHealth(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return body
}

func getHealth(t *testing.T, baseURL string) (*http.Response, map[string]any) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(lifecycleTestTimeout)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/health")
		if err == nil {
			return response, decodeHealth(t, response)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon health endpoint did not become reachable")
	return nil, nil
}

func waitForReadyHealth(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(lifecycleTestTimeout)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/health")
		if err == nil {
			body := decodeHealth(t, response)
			if body["status"] != "starting" {
				if response.Header.Get(server.HeaderDaemon) != "1" {
					t.Fatal("ready health response lost daemon identity header")
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon never left the starting state")
}

func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(lifecycleTestTimeout)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil && len(bytes.TrimSpace(body)) > 0 {
			return body
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s did not become readable", path)
	return nil
}

func doLifecycleRequest(t *testing.T, method, url, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("create %s %s: %v", method, url, err)
	}
	if token != "" {
		req.Header.Set("X-Heimdallm-Token", token)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response
}

func doLifecycleJSONRequest(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Heimdallm-Token", token)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response
}

func doUpdateLifecycleRequest(
	t *testing.T,
	method, baseURL, token, leaseID, expectedBootID string,
) (*http.Response, server.UpdatePreparationStatus) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL, nil)
	if err != nil {
		t.Fatalf("create %s %s: %v", method, baseURL, err)
	}
	req.Header.Set("X-Heimdallm-Token", token)
	req.Header.Set(server.HeaderUpdateLease, leaseID)
	if expectedBootID != "" {
		req.Header.Set(server.HeaderExpectedUpdateBootID, expectedBootID)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, baseURL, err)
	}
	defer response.Body.Close()
	var status server.UpdatePreparationStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatalf("decode %s %s response: %v", method, baseURL, err)
	}
	return response, status
}

func postWhenReviewSlotIsFree(t *testing.T, url, token string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(lifecycleTestTimeout)
	for time.Now().Before(deadline) {
		response := doLifecycleRequest(t, http.MethodPost, url, token)
		if response.StatusCode != http.StatusTooManyRequests {
			return response
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("review semaphore never became available")
	return nil
}

func waitForRemoteRequest(t *testing.T, requests <-chan string, want string) {
	t.Helper()
	deadline := time.After(lifecycleTestTimeout)
	for {
		select {
		case got := <-requests:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("fake GitHub never received %s", want)
		}
	}
}

func TestRunProcessPreparingJournalBlocksStatefulBootstrapBeforeConfirmation(t *testing.T) {
	dataDir := t.TempDir()
	localDirBase := filepath.Join(dataDir, "repos")
	if err := os.MkdirAll(localDirBase, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath, _ := writeLifecycleConfig(t, dataDir, localDirBase, "1h")
	const (
		leaseID = "018f6d3e-91aa-7a45-b2c0-1d8c3b4a5968"
		apiKey  = "0123456789abcdef0123456789abcdef"
	)
	if err := os.WriteFile(filepath.Join(dataDir, "api_token"), []byte(apiKey), 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(map[string]any{
		"schemaVersion":          1,
		"expectedVersion":        "recovery-next-version",
		"phase":                  "preparing",
		"leaseID":                leaseID,
		"daemonPID":              4242,
		"daemonBootID":           "boot-before-crash",
		"daemonVersion":          "recovery-test-version",
		"launchAgentWasLoaded":   true,
		"launchAgentWasDisabled": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dataDir, "app-update-recovery.json"),
		journal,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	expiredMarker := []byte(`{"lease_id":"` + leaseID + `","expires_at":"2000-01-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dataDir, "update-drain.json"), expiredMarker, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HEIMDALLM_DATA_DIR", dataDir)
	t.Setenv("HEIMDALLM_CONFIG_PATH", configPath)
	t.Setenv("GITHUB_TOKEN", "recovery-test-token")
	originalArgs := os.Args
	originalLogger := slog.Default()
	originalVersion := version
	os.Args = []string{"heimdallm"}
	version = "recovery-test-version"

	listenerCh := make(chan net.Listener, 1)
	result := make(chan int, 1)
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/user" {
			_, _ = io.WriteString(w, `{"login":"heimdallm-recovery-test"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}))
	deps := processDependencies{
		newGitHubClient: func(token string, _ ...gh.Option) *gh.Client {
			return gh.NewClient(token, gh.WithBaseURL(fakeGitHub.URL))
		},
		listen: func(_ int, bindAddr string) (net.Listener, error) {
			listener, listenErr := server.Listen(0, bindAddr)
			if listenErr == nil {
				listenerCh <- listener
			}
			return listener, listenErr
		},
	}
	go func() { result <- runProcessWithDependencies(true, deps) }()

	var listener net.Listener
	select {
	case listener = <-listenerCh:
	case <-time.After(lifecycleTestTimeout):
		os.Args = originalArgs
		slog.SetDefault(originalLogger)
		version = originalVersion
		fakeGitHub.Close()
		t.Fatal("recovery daemon did not expose its minimal listener")
	}
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-result:
		case <-time.After(3 * time.Second):
		}
		os.Args = originalArgs
		slog.SetDefault(originalLogger)
		version = originalVersion
		fakeGitHub.Close()
	})

	baseURL := "http://" + listener.Addr().String()
	response, body := getHealth(t, baseURL)
	if response.StatusCode != http.StatusServiceUnavailable || body["status"] != "starting" {
		t.Fatalf("recovery health = %d %#v, want starting 503", response.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "heimdallm.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stateful SQLite opened before bootstrap confirmation: %v", err)
	}

	prepareResponse, status := doUpdateLifecycleRequest(t, http.MethodPost,
		baseURL+"/update/prepare", apiKey, leaseID, "")
	if prepareResponse.StatusCode != http.StatusOK || !status.Sealed ||
		status.BootstrapAuthorized || status.LeaseID != leaseID {
		t.Fatalf("restored prepare status = %d %+v, want sealed unconfirmed owner",
			prepareResponse.StatusCode, status)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "heimdallm.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("minimal update handshake opened SQLite: %v", err)
	}

	sealResponse, sealedStatus := doUpdateLifecycleRequest(t, http.MethodPost,
		baseURL+"/update/seal", apiKey, leaseID, "")
	if sealResponse.StatusCode != http.StatusOK || !sealedStatus.Sealed {
		t.Fatalf("restored seal status = %d %+v, want sealed 200", sealResponse.StatusCode, sealedStatus)
	}
	confirmResponse, confirmedStatus := doUpdateLifecycleRequest(t, http.MethodPost,
		baseURL+"/update/confirm", apiKey, leaseID, status.BootID)
	if confirmResponse.StatusCode != http.StatusOK || !confirmedStatus.BootstrapAuthorized {
		t.Fatalf("bootstrap confirmation = %d %+v, want authorized 200",
			confirmResponse.StatusCode, confirmedStatus)
	}
	waitForReadyHealth(t, baseURL)
	if _, err := os.Stat(filepath.Join(dataDir, "heimdallm.db")); err != nil {
		t.Fatalf("stateful SQLite was not opened after bootstrap confirmation: %v", err)
	}

	foreignCancel, _ := doUpdateLifecycleRequest(t, http.MethodDelete,
		baseURL+"/update/prepare", apiKey, leaseID+"-foreign", status.BootID)
	if foreignCancel.StatusCode != http.StatusConflict {
		t.Fatalf("foreign cancellation status = %d, want 409", foreignCancel.StatusCode)
	}
	cancelResponse, cancelledStatus := doUpdateLifecycleRequest(t, http.MethodDelete,
		baseURL+"/update/prepare", apiKey, leaseID, status.BootID)
	if cancelResponse.StatusCode != http.StatusOK || cancelledStatus.State != "running" {
		t.Fatalf("verified cancellation = %d %+v, want running 200",
			cancelResponse.StatusCode, cancelledStatus)
	}
	shutdownResponse := doLifecycleRequest(t, http.MethodPost, baseURL+"/shutdown", apiKey)
	if shutdownResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("recovery daemon shutdown = %d, want 202", shutdownResponse.StatusCode)
	}
}

func TestRunProcessFullLifecycleStartingReloadManualOperationsAndAPIShutdown(t *testing.T) {
	fixture, userRequested := startLifecycleFixture(t, true)

	select {
	case <-userRequested:
	case <-time.After(lifecycleTestTimeout):
		t.Fatal("startup did not reach the authenticated-user request")
	}
	response, body := getHealth(t, fixture.apiBaseURL)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("starting health status = %d, want 503", response.StatusCode)
	}
	if got := response.Header.Get(server.HeaderDaemon); got != "1" {
		t.Fatalf("starting health daemon header = %q, want 1", got)
	}
	if body["status"] != "starting" || body["version"] != "lifecycle-test-version" {
		t.Fatalf("starting health body = %#v", body)
	}

	// Release the deliberately blocked /user response and let startup finish.
	fixture.releaseUser()
	waitForReadyHealth(t, fixture.apiBaseURL)
	apiToken := strings.TrimSpace(string(waitForFile(t, filepath.Join(fixture.dataDir, "api_token"))))
	if len(apiToken) < 32 {
		t.Fatalf("API token length = %d, want at least 32", len(apiToken))
	}

	// Exercise the normal poller-restart branch before shutdown. Waiting for its
	// completion keeps the subsequent shutdown deterministic.
	reloaded := strings.Replace(fixture.configBody, `poll_interval = "1h"`, `poll_interval = "2h"`, 1)
	if reloaded == fixture.configBody {
		t.Fatal("test config did not contain the poll interval to replace")
	}
	if err := os.WriteFile(fixture.configPath, []byte(reloaded), 0o600); err != nil {
		t.Fatalf("rewrite lifecycle config: %v", err)
	}
	response = doLifecycleRequest(t, http.MethodPost, fixture.apiBaseURL+"/reload", apiToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reload status = %d, want 200", response.StatusCode)
	}
	select {
	case <-fixture.pollersRestarted:
	case <-time.After(lifecycleTestTimeout):
		t.Fatal("pollers did not finish restarting")
	}

	// Invoke every callback whose context was changed from Background to the
	// daemon lifetime. The fake GitHub endpoint forces each operation to fail
	// before an AI executable can be launched.
	response = postWhenReviewSlotIsFree(t,
		fmt.Sprintf("%s/prs/%d/review", fixture.apiBaseURL, fixture.prID), apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("manual PR review status = %d, want 202", response.StatusCode)
	}
	waitForRemoteRequest(t, fixture.remoteRequest, "/repos/org/repo/pulls/1")

	response = postWhenReviewSlotIsFree(t, fixture.apiBaseURL+"/issues/999999/review", apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("manual issue review status = %d, want 202", response.StatusCode)
	}

	response = postWhenReviewSlotIsFree(t,
		fmt.Sprintf("%s/issues/%d/refine?force=true", fixture.apiBaseURL, fixture.issueID), apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("manual issue refinement status = %d, want 202", response.StatusCode)
	}
	waitForRemoteRequest(t, fixture.remoteRequest, "/repos/org/repo/issues/2")

	response = doLifecycleRequest(t, http.MethodPost,
		fmt.Sprintf("%s/issues/%d/promote", fixture.apiBaseURL, fixture.issueID), apiToken)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("manual issue promotion status = %d, want 500 from fake GitHub", response.StatusCode)
	}
	waitForRemoteRequest(t, fixture.remoteRequest, "/repos/org/repo/issues/2")

	// Exercise the complete live-daemon update protocol and every main-layer
	// mutation guard. Once prepare closes admission, these endpoints must defer
	// before touching repositories, GitHub, or an AI process.
	const updateLeaseID = "0190fdd2-f1f2-7d73-b2a4-79f988c14d57"
	var updateStatus server.UpdatePreparationStatus
	deadline := time.Now().Add(lifecycleTestTimeout)
	for {
		response, updateStatus = doUpdateLifecycleRequest(t, http.MethodPost,
			fixture.apiBaseURL+"/update/prepare", apiToken, updateLeaseID, "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("prepare live update status = %d, want 200", response.StatusCode)
		}
		if updateStatus.State == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live update drain did not become ready: %+v", updateStatus)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if updateStatus.BootID == "" {
		t.Fatal("live update status did not identify the daemon process")
	}

	foreignPrepare, _ := doUpdateLifecycleRequest(t, http.MethodPost,
		fixture.apiBaseURL+"/update/prepare", apiToken, updateLeaseID+"-foreign", "")
	if foreignPrepare.StatusCode != http.StatusConflict {
		t.Fatalf("foreign prepare status = %d, want 409", foreignPrepare.StatusCode)
	}
	prematureConfirm, _ := doUpdateLifecycleRequest(t, http.MethodPost,
		fixture.apiBaseURL+"/update/confirm", apiToken, updateLeaseID, updateStatus.BootID)
	if prematureConfirm.StatusCode != http.StatusConflict {
		t.Fatalf("confirmation before seal status = %d, want 409", prematureConfirm.StatusCode)
	}

	for _, endpoint := range []string{
		"/config/clones",
		"/config/clones/" + url.PathEscape("org/repo"),
	} {
		response = doLifecycleRequest(t, http.MethodDelete, fixture.apiBaseURL+endpoint, apiToken)
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("guarded DELETE %s status = %d, want 409", endpoint, response.StatusCode)
		}
	}
	response = doLifecycleJSONRequest(t, http.MethodPost, fixture.apiBaseURL+"/admin/repo-rename",
		apiToken, `{"old_repo":"org/repo","new_repo":"org/renamed"}`)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("guarded repo rename status = %d, want 409", response.StatusCode)
	}
	response = doLifecycleRequest(t, http.MethodPost,
		fmt.Sprintf("%s/issues/%d/promote", fixture.apiBaseURL, fixture.issueID), apiToken)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("guarded promotion status = %d, want 409", response.StatusCode)
	}
	response = postWhenReviewSlotIsFree(t,
		fmt.Sprintf("%s/prs/%d/review", fixture.apiBaseURL, fixture.prID), apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("guarded PR review status = %d, want 202", response.StatusCode)
	}
	response = postWhenReviewSlotIsFree(t,
		fmt.Sprintf("%s/issues/%d/review", fixture.apiBaseURL, fixture.issueID), apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("guarded issue review status = %d, want 202", response.StatusCode)
	}
	response = postWhenReviewSlotIsFree(t,
		fmt.Sprintf("%s/issues/%d/refine?force=true", fixture.apiBaseURL, fixture.issueID), apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("guarded issue refinement status = %d, want 202", response.StatusCode)
	}
	// The async callbacks return immediately on the closed admission gate. Let
	// the final goroutine release the shared review semaphore before reopening.
	time.Sleep(25 * time.Millisecond)

	foreignSeal, _ := doUpdateLifecycleRequest(t, http.MethodPost,
		fixture.apiBaseURL+"/update/seal", apiToken, updateLeaseID+"-foreign", "")
	if foreignSeal.StatusCode != http.StatusConflict {
		t.Fatalf("foreign seal status = %d, want 409", foreignSeal.StatusCode)
	}
	sealResponse, sealedStatus := doUpdateLifecycleRequest(t, http.MethodPost,
		fixture.apiBaseURL+"/update/seal", apiToken, updateLeaseID, "")
	if sealResponse.StatusCode != http.StatusOK || !sealedStatus.Sealed {
		t.Fatalf("seal live update = %d %+v, want sealed 200", sealResponse.StatusCode, sealedStatus)
	}
	prematureCancel, _ := doUpdateLifecycleRequest(t, http.MethodDelete,
		fixture.apiBaseURL+"/update/prepare", apiToken, updateLeaseID, updateStatus.BootID)
	if prematureCancel.StatusCode != http.StatusConflict {
		t.Fatalf("cancellation before bootstrap confirmation = %d, want 409", prematureCancel.StatusCode)
	}
	confirmResponse, confirmedStatus := doUpdateLifecycleRequest(t, http.MethodPost,
		fixture.apiBaseURL+"/update/confirm", apiToken, updateLeaseID, updateStatus.BootID)
	if confirmResponse.StatusCode != http.StatusOK || !confirmedStatus.BootstrapAuthorized {
		t.Fatalf("confirm live update = %d %+v, want authorized 200",
			confirmResponse.StatusCode, confirmedStatus)
	}
	cancelResponse, cancelledStatus := doUpdateLifecycleRequest(t, http.MethodDelete,
		fixture.apiBaseURL+"/update/prepare", apiToken, updateLeaseID, updateStatus.BootID)
	if cancelResponse.StatusCode != http.StatusOK || cancelledStatus.State != "running" {
		t.Fatalf("cancel live update = %d %+v, want running 200", cancelResponse.StatusCode, cancelledStatus)
	}

	response = doLifecycleRequest(t, http.MethodPost, fixture.apiBaseURL+"/shutdown", apiToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status = %d, want 202", response.StatusCode)
	}
	select {
	case code := <-fixture.result:
		if code != 0 {
			t.Fatalf("runProcessWithDependencies exit code = %d, want 0", code)
		}
	case <-time.After(lifecycleTestTimeout):
		t.Fatal("daemon did not finish after API shutdown")
	}
}

func TestRunProcessExitsWhenReadyListenerDies(t *testing.T) {
	fixture, _ := startLifecycleFixture(t, false)
	waitForReadyHealth(t, fixture.apiBaseURL)
	if err := fixture.listener.Close(); err != nil {
		t.Fatalf("close live listener: %v", err)
	}
	select {
	case code := <-fixture.result:
		if code != 1 {
			t.Fatalf("runProcessWithDependencies exit code = %d, want 1", code)
		}
	case <-time.After(lifecycleTestTimeout):
		t.Fatal("daemon did not finish after its ready listener died")
	}
}

func writeFakeLaunchctl(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake launchctl: %v", err)
	}
	return dir
}

func prepareCommandHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.heimdallm.daemon.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		t.Fatalf("create fake LaunchAgents: %v", err)
	}
	return home, plistPath
}

func TestRunProcessCommands(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })

	home, _ := prepareCommandHome(t)
	t.Setenv("HOME", home)
	t.Setenv("PATH", writeFakeLaunchctl(t, 0))
	os.Args = []string{"heimdallm", "install"}
	if code := runProcessWithDependencies(true, processDependencies{}); code != 0 {
		t.Fatalf("install exit code = %d, want 0", code)
	}
	os.Args = []string{"heimdallm", "uninstall"}
	if code := runProcessWithDependencies(true, processDependencies{}); code != 0 {
		t.Fatalf("uninstall exit code = %d, want 0", code)
	}
	os.Args = []string{"heimdallm", "version"}
	if code := runProcessWithDependencies(true, processDependencies{}); code != 0 {
		t.Fatalf("version exit code = %d, want 0", code)
	}

	failingHome, _ := prepareCommandHome(t)
	t.Setenv("HOME", failingHome)
	t.Setenv("PATH", writeFakeLaunchctl(t, 23))
	os.Args = []string{"heimdallm", "install"}
	if code := runProcessWithDependencies(true, processDependencies{}); code != 1 {
		t.Fatalf("failed install exit code = %d, want 1", code)
	}

	uninstallHome, uninstallPlist := prepareCommandHome(t)
	if err := os.Mkdir(uninstallPlist, 0o700); err != nil {
		t.Fatalf("create directory at plist path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uninstallPlist, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("make plist directory non-empty: %v", err)
	}
	t.Setenv("HOME", uninstallHome)
	t.Setenv("PATH", writeFakeLaunchctl(t, 0))
	os.Args = []string{"heimdallm", "uninstall"}
	if code := runProcessWithDependencies(true, processDependencies{}); code != 1 {
		t.Fatalf("failed uninstall exit code = %d, want 1", code)
	}
}

func TestRunProcessRejectsMalformedConfigAndReleasesLock(t *testing.T) {
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[server\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEIMDALLM_DATA_DIR", dataDir)
	t.Setenv("HEIMDALLM_CONFIG_PATH", configPath)
	originalArgs := os.Args
	originalLogger := slog.Default()
	os.Args = []string{"heimdallm"}
	t.Cleanup(func() {
		os.Args = originalArgs
		slog.SetDefault(originalLogger)
	})

	if code := run(); code != 1 {
		t.Fatalf("run malformed config = %d, want 1", code)
	}
	if code := run(); code != 1 {
		t.Fatalf("second run malformed config = %d, want 1 after lock release", code)
	}
}

type immediateErrorListener struct{}

func (immediateErrorListener) Accept() (net.Conn, error) { return nil, errors.New("accept failed") }
func (immediateErrorListener) Close() error              { return nil }
func (immediateErrorListener) Addr() net.Addr            { return testAddr("127.0.0.1:0") }

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

func TestRunProcessServeFailureCancelsStartup(t *testing.T) {
	dataDir := t.TempDir()
	configPath, _ := writeLifecycleConfig(t, dataDir, filepath.Join(dataDir, "repos"), "1h")
	t.Setenv("HEIMDALLM_DATA_DIR", dataDir)
	t.Setenv("HEIMDALLM_CONFIG_PATH", configPath)
	t.Setenv("GITHUB_TOKEN", "serve-failure-token")
	originalArgs := os.Args
	originalLogger := slog.Default()
	os.Args = []string{"heimdallm"}
	t.Cleanup(func() {
		os.Args = originalArgs
		slog.SetDefault(originalLogger)
	})

	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"login":"heimdallm-test"}`)
	}))
	defer fakeGitHub.Close()
	deps := processDependencies{
		newGitHubClient: func(token string, _ ...gh.Option) *gh.Client {
			return gh.NewClient(token, gh.WithBaseURL(fakeGitHub.URL))
		},
		listen: func(int, string) (net.Listener, error) { return immediateErrorListener{}, nil },
	}
	if code := runProcessWithDependencies(true, deps); code != 1 {
		t.Fatalf("run after listener failure = %d, want 1", code)
	}
}
