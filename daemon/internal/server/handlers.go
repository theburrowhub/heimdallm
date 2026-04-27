package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/nats-io/nats.go"
)

// Server holds the HTTP router, SSE broker, store, and optional pipeline.
type Server struct {
	store                *store.Store
	broker               *sse.Broker
	natsConn             *nats.Conn // when set, handleSSE reads from NATS instead of broker
	pipeline             *pipeline.Pipeline
	router               chi.Router
	httpServer           *http.Server
	reloadFn             func() error
	shutdownFn           func()
	triggerReviewFn      func(prID int64) error
	triggerIssueReviewFn func(issueID int64) error
	triggerPromoteFn     func(issueID int64) error
	meFn                 func() (string, error)
	// configFn returns the current running config as a JSON-serializable map.
	configFn func() map[string]any
	// repoMetaFns fetch repo metadata from GitHub for autocomplete.
	fetchLabelsFn        func(repo string) ([]string, error)
	fetchCollaboratorsFn func(repo string) ([]string, error)
	// apiToken is required on all state-mutating requests (POST/PUT/DELETE).
	// Empty string disables authentication (should not happen in production).
	apiToken  string
	reviewSem chan struct{} // counting semaphore for concurrent review triggers
	// configPath is the path to config.toml. Required for PATCH/DELETE
	// endpoints that read-merge-write the TOML file.
	configPath string
	// tomlMu serialises TOML read-merge-write operations.
	tomlMu sync.Mutex
}

// Options holds optional configuration for the Server.
type Options struct {
	// MaxConcurrentReviews limits how many POST /prs/{id}/review goroutines
	// can run simultaneously. 0 means use the default (5).
	MaxConcurrentReviews int
}

const defaultMaxConcurrentReviews = 5

// New creates a new Server. p may be nil if the pipeline is not yet configured.
// apiToken must be the value returned by LoadOrCreateAPIToken — it is required
// on all mutating endpoints to prevent cross-process/browser config poisoning.
func New(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline, apiToken string) *Server {
	return NewWithOptions(s, broker, p, apiToken, Options{})
}

// NewWithOptions creates a Server with configurable options.
func NewWithOptions(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline, apiToken string, opts Options) *Server {
	max := opts.MaxConcurrentReviews
	if max <= 0 {
		max = defaultMaxConcurrentReviews
	}
	srv := &Server{
		store:     s,
		broker:    broker,
		pipeline:  p,
		apiToken:  apiToken,
		reviewSem: make(chan struct{}, max),
	}
	srv.router = srv.buildRouter()
	return srv
}

// SetNATSConn enables NATS-based SSE. When set, handleSSE subscribes to
// NATS events instead of the in-memory broker, removing the 10-subscriber limit.
func (srv *Server) SetNATSConn(conn *nats.Conn) {
	srv.natsConn = conn
}

// sensitiveGETPaths lists GET paths that require authentication even though they
// are read-only. This includes endpoints that expose internal config, agent
// data, activity metadata, GitHub username, PR list, and review stats.
// Only /health remains public (used for health checks by the launcher).
var sensitiveGETPaths = []string{
	"/activity",
	"/config",
	"/agents",
	"/events",
	"/logs/stream",
	"/me",     // exposes GitHub username
	"/prs",    // exposes PR titles, repos, authors
	"/stats",  // exposes review activity metadata
	"/issues", // covers /issues and /issues/{id}
	"/repos",  // covers /repos/{name}/labels and /repos/{name}/collaborators
}

// authMiddleware rejects:
//   - POST/PUT/PATCH/DELETE requests without a valid X-Heimdallm-Token header.
//   - GET requests to /config or /agents without a valid token (these endpoints
//     return local directory paths, CLI flags, and agent configs that should not
//     be readable by arbitrary browser tabs — see security issue #3).
func (srv *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if srv.apiToken != "" {
			needsAuth := false
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				needsAuth = true
			case http.MethodGet:
				for _, p := range sensitiveGETPaths {
					if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
						needsAuth = true
						break
					}
				}
			}
			if needsAuth {
				token := r.Header.Get("X-Heimdallm-Token")
				// Constant-time comparison to prevent timing attacks.
				if !hmac.Equal([]byte(token), []byte(srv.apiToken)) {
					http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// SetReloadFn wires the config-reload callback called by POST /reload.
func (srv *Server) SetReloadFn(fn func() error) { srv.reloadFn = fn }

// SetShutdownFn wires the daemon shutdown callback called by POST /shutdown.
func (srv *Server) SetShutdownFn(fn func()) { srv.shutdownFn = fn }

// SetTriggerReviewFn wires the review-trigger callback called by POST /prs/{id}/review.
func (srv *Server) SetTriggerReviewFn(fn func(prID int64) error) { srv.triggerReviewFn = fn }

// SetTriggerIssueReviewFn wires the issue-review-trigger callback called by POST /issues/{id}/review.
func (srv *Server) SetTriggerIssueReviewFn(fn func(issueID int64) error) {
	srv.triggerIssueReviewFn = fn
}

// SetTriggerPromoteFn wires the promote callback called by POST /issues/{id}/promote.
// The callback must run the auto_implement pipeline for the given issue regardless
// of its current classification (review_only → develop promotion).
func (srv *Server) SetTriggerPromoteFn(fn func(issueID int64) error) {
	srv.triggerPromoteFn = fn
}

// SetMeFn wires the authenticated-user callback called by GET /me.
func (srv *Server) SetMeFn(fn func() (string, error)) { srv.meFn = fn }

// SetConfigFn wires the callback that returns the live config for GET /config.
func (srv *Server) SetConfigFn(fn func() map[string]any) { srv.configFn = fn }

// SetRepoMetaFns wires GitHub metadata fetchers for autocomplete endpoints.
func (srv *Server) SetRepoMetaFns(labels func(string) ([]string, error), collabs func(string) ([]string, error)) {
	srv.fetchLabelsFn = labels
	srv.fetchCollaboratorsFn = collabs
}

// SetConfigPath sets the path to config.toml for PATCH/DELETE handlers.
func (srv *Server) SetConfigPath(path string) { srv.configPath = path }

// patchTOML is the shared read-merge-write pipeline for all TOML-mutating
// endpoints. mutateFn receives the current TOML as a map and must apply
// its changes in-place. On success, returns the full live config for the
// response body.
func (srv *Server) patchTOML(mutateFn func(m map[string]any) error) (map[string]any, error) {
	srv.tomlMu.Lock()
	defer srv.tomlMu.Unlock()

	m, err := config.ReadTOMLMap(srv.configPath)
	if err != nil {
		return nil, err
	}
	if err := mutateFn(m); err != nil {
		return nil, err
	}
	if err := config.ValidateMap(m); err != nil {
		return nil, err
	}
	if err := config.AtomicWriteTOML(srv.configPath, m); err != nil {
		return nil, err
	}
	if srv.reloadFn != nil {
		if err := srv.reloadFn(); err != nil {
			return nil, fmt.Errorf("config reload after write: %w", err)
		}
	}
	if srv.configFn != nil {
		return srv.configFn(), nil
	}
	return m, nil
}

// Router returns the underlying http.Handler for use in tests or embedding.
func (srv *Server) Router() http.Handler {
	return srv.router
}

// Start binds the HTTP server to the given port and begins serving.
func (srv *Server) Start(port int, bindAddr string) error {
	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	srv.httpServer = &http.Server{
		Addr:         addr,
		Handler:      srv.router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return srv.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server.
func (srv *Server) Shutdown(ctx context.Context) error {
	if srv.httpServer == nil {
		return nil
	}
	return srv.httpServer.Shutdown(ctx)
}

func (srv *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(srv.authMiddleware)
	r.Get("/health", srv.handleHealth)
	r.Get("/me", srv.handleMe)
	r.Get("/prs", srv.handleListPRs)
	r.Get("/prs/{id}", srv.handleGetPR)
	r.Post("/prs/{id}/review", srv.handleTriggerReview)
	r.Post("/prs/{id}/dismiss", srv.handleDismissPR)
	r.Post("/prs/{id}/undismiss", srv.handleUndismissPR)
	r.Get("/issues", srv.handleListIssues)
	r.Get("/issues/{id}", srv.handleGetIssue)
	r.Post("/issues/{id}/review", srv.handleTriggerIssueReview)
	r.Post("/issues/{id}/promote", srv.handlePromoteIssue)
	r.Post("/issues/{id}/dismiss", srv.handleDismissIssue)
	r.Post("/issues/{id}/undismiss", srv.handleUndismissIssue)
	r.Get("/repos/{name}/labels", srv.handleRepoLabels)
	r.Get("/repos/{name}/collaborators", srv.handleRepoCollaborators)
	r.Get("/activity", srv.handleActivity)
	r.Get("/stats", srv.handleStats)
	r.Get("/agents", srv.handleListAgents)
	r.Post("/agents", srv.handleUpsertAgent)
	r.Delete("/agents/{id}", srv.handleDeleteAgent)
	r.Get("/config", srv.handleGetConfig)
	r.Put("/config", srv.handlePutConfig)
	r.Patch("/config", srv.handlePatchConfig)
	r.Patch("/config/repos/{repo}", srv.handlePatchRepoConfig)
	r.Delete("/config/repos/{repo}/*", srv.handleDeleteRepoField)
	r.Post("/reload", srv.handleReload)
	r.Post("/shutdown", srv.handleShutdown)
	r.Get("/events", srv.handleSSE)
	r.Get("/logs/stream", srv.handleLogsStream)
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (srv *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (srv *Server) handleListPRs(w http.ResponseWriter, r *http.Request) {
	var states []string
	if s := r.URL.Query().Get("state"); s != "" {
		states = strings.Split(s, ",")
	}
	prs, err := srv.store.ListPRs(states...)
	if err != nil {
		slog.Error("handleListPRs: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	type prWithReview struct {
		*store.PR
		LatestReview *store.Review `json:"latest_review,omitempty"`
	}
	result := make([]prWithReview, 0, len(prs))
	for _, pr := range prs {
		rev, _ := srv.store.LatestReviewForPR(pr.ID)
		result = append(result, prWithReview{PR: pr, LatestReview: rev})
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleGetPR(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pr, err := srv.store.GetPR(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	reviews, _ := srv.store.ListReviewsForPR(id)
	writeJSON(w, http.StatusOK, map[string]any{"pr": pr, "reviews": reviews})
}

func (srv *Server) handleDismissPR(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := srv.store.DismissPR(id); err != nil {
		slog.Error("handleDismissPR: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

func (srv *Server) handleUndismissPR(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := srv.store.UndismissPR(id); err != nil {
		slog.Error("handleUndismissPR: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "undismissed"})
}

func (srv *Server) handleTriggerReview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if srv.triggerReviewFn == nil {
		http.Error(w, "review trigger not configured", http.StatusServiceUnavailable)
		return
	}
	// Acquire semaphore slot (non-blocking). Returns 429 if all slots are taken.
	select {
	case srv.reviewSem <- struct{}{}:
	default:
		http.Error(w, `{"error":"too many concurrent reviews — try again later"}`, http.StatusTooManyRequests)
		return
	}
	go func() {
		defer func() { <-srv.reviewSem }()
		if err := srv.triggerReviewFn(id); err != nil {
			slog.Error("trigger review failed", "pr_id", id, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "review queued"})
}

func (srv *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if srv.configFn != nil {
		writeJSON(w, http.StatusOK, srv.configFn())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// validConfigKeys is the exhaustive allowlist of keys that callers are
// permitted to PERSIST via PUT /config. Any key outside this set (and outside
// readOnlyConfigKeys below) is rejected with HTTP 400 to prevent arbitrary
// data injection into the configs table (security issue #4).
var validConfigKeys = map[string]struct{}{
	"poll_interval":  {},
	"repositories":   {},
	"ai_primary":     {},
	"ai_fallback":    {},
	"review_mode":    {},
	"retention_days": {},
	"issue_tracking": {},
}

// readOnlyConfigKeys are keys that GET /config returns (so the web UI can
// render them) but that PUT /config must not persist. The SvelteKit page
// round-trips the entire GET payload as a forward-compat safeguard, so
// rejecting these would 400 every save. We accept them at the endpoint
// boundary and drop them before SetConfig — still a strict allowlist, no
// arbitrary keys permitted.
//
// Why each key is here:
//   - non_monitored : Flutter-UI managed list of disabled repos, not a
//     web-UI concern.
//   - repo_overrides: per-repo AI config lives in [ai.repos.<name>] or
//     the Flutter app; no write-path through this endpoint.
//   - agent_configs : per-CLI agent tuning lives in [ai.agents.<name>]
//     or /agents endpoints; no write-path here.
//   - server_port   : bootstrap-only (changing the listening port mid-
//     flight would drop every in-flight connection). Its numeric-range
//     pre-check still runs so clients get feedback on bad values.
var readOnlyConfigKeys = map[string]struct{}{
	"non_monitored":  {},
	"repo_overrides": {},
	"agent_configs":  {},
	"server_port":    {},
}

// validPollIntervals is the allowlist of permitted poll_interval values.
var validPollIntervals = map[string]struct{}{
	"1m":  {},
	"5m":  {},
	"30m": {},
	"1h":  {},
}

// validReviewModes is the allowlist of permitted review_mode values.
var validReviewModes = map[string]struct{}{
	"single": {},
	"multi":  {},
}

func (srv *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	for k := range body {
		_, writable := validConfigKeys[k]
		_, readOnly := readOnlyConfigKeys[k]
		if !writable && !readOnly {
			http.Error(w, fmt.Sprintf("unknown config key: %q", k), http.StatusBadRequest)
			return
		}
	}

	// Validate value formats per key.
	if v, ok := body["poll_interval"]; ok {
		s, isStr := v.(string)
		if !isStr {
			http.Error(w, "poll_interval must be a string", http.StatusBadRequest)
			return
		}
		if _, valid := validPollIntervals[s]; !valid {
			http.Error(w, "poll_interval must be one of: 1m, 5m, 30m, 1h", http.StatusBadRequest)
			return
		}
	}
	if v, ok := body["retention_days"]; ok {
		n, isNum := v.(float64) // JSON numbers decode as float64
		if !isNum || n < 0 || n > 3650 {
			http.Error(w, "retention_days must be between 0 and 3650", http.StatusBadRequest)
			return
		}
	}
	if v, ok := body["server_port"]; ok {
		n, isNum := v.(float64)
		if !isNum || n < 1024 || n > 65535 {
			http.Error(w, "server_port must be between 1024 and 65535", http.StatusBadRequest)
			return
		}
	}
	if v, ok := body["review_mode"]; ok {
		s, isStr := v.(string)
		if !isStr {
			http.Error(w, "review_mode must be a string", http.StatusBadRequest)
			return
		}
		if _, valid := validReviewModes[s]; !valid {
			http.Error(w, "review_mode must be one of: single, multi", http.StatusBadRequest)
			return
		}
	}
	if v, ok := body["issue_tracking"]; ok {
		// Round-trip through JSON to decode into the typed struct. This
		// rejects malformed payloads (e.g. the client sent a string or
		// array by mistake) before we ever hit the store, so a single bad
		// request cannot persist a value that breaks the next reload.
		raw, err := json.Marshal(v)
		if err != nil {
			http.Error(w, "issue_tracking must be a JSON object", http.StatusBadRequest)
			return
		}
		var it config.IssueTrackingConfig
		if err := json.Unmarshal(raw, &it); err != nil {
			http.Error(w, fmt.Sprintf("issue_tracking: %v", err), http.StatusBadRequest)
			return
		}
		if err := config.ValidateIssueTracking(it); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	for k, v := range body {
		// Read-only keys were accepted above to avoid 400s on UI saves that
		// round-trip the GET payload, but they must not land in the store.
		if _, readOnly := readOnlyConfigKeys[k]; readOnly {
			continue
		}
		var val string
		switch typed := v.(type) {
		case string:
			val = typed
		default:
			// Arrays, numbers, booleans: serialize as JSON so they round-trip correctly.
			// fmt.Sprintf("%v", v) would produce "[item1 item2]" for arrays, which
			// cannot be parsed back — see security issue M-2.
			b, err := json.Marshal(v)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid value for key %q", k), http.StatusBadRequest)
				return
			}
			val = string(b)
		}
		if _, err := srv.store.SetConfig(k, val); err != nil {
			slog.Error("config: set", "key", k, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (srv *Server) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"PATCH not available — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if err := config.ContainsNull(patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "null values not allowed in PATCH — use DELETE to remove fields",
		})
		return
	}
	config.NormalizeNumbers(patch)

	result, err := srv.patchTOML(func(m map[string]any) error {
		merged := config.DeepMerge(m, patch)
		for k, v := range merged {
			m[k] = v
		}
		for k := range m {
			if _, ok := merged[k]; !ok {
				delete(m, k)
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("PATCH /config failed", "err", err)
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		} else {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handlePatchRepoConfig(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"PATCH not available — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	repo, err := url.PathUnescape(chi.URLParam(r, "repo"))
	if err != nil || repo == "" {
		http.Error(w, `{"error":"invalid repo parameter"}`, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if err := config.ContainsNull(patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "null values not allowed in PATCH — use DELETE to remove fields",
		})
		return
	}
	config.NormalizeNumbers(patch)

	globalPatch := map[string]any{
		"ai": map[string]any{
			"repos": map[string]any{
				repo: patch,
			},
		},
	}

	result, err := srv.patchTOML(func(m map[string]any) error {
		merged := config.DeepMerge(m, globalPatch)
		for k, v := range merged {
			m[k] = v
		}
		for k := range m {
			if _, ok := merged[k]; !ok {
				delete(m, k)
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("PATCH /config/repos failed", "repo", repo, "err", err)
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		} else {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleDeleteRepoField(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"DELETE not available — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	repo, err := url.PathUnescape(chi.URLParam(r, "repo"))
	if err != nil || repo == "" {
		http.Error(w, `{"error":"invalid repo parameter"}`, http.StatusBadRequest)
		return
	}
	field := chi.URLParam(r, "*")
	if field == "" {
		http.Error(w, `{"error":"field path required"}`, http.StatusBadRequest)
		return
	}
	// Build the full path: ai → repos → <repo> → <field segments>
	segments := append([]string{"ai", "repos", repo}, strings.Split(field, "/")...)

	result, err := srv.patchTOML(func(m map[string]any) error {
		config.DeleteNestedKey(m, segments)
		// Idempotent: not finding the key is not an error
		return nil
	})
	if err != nil {
		slog.Error("DELETE /config/repos field failed", "repo", repo, "field", field, "err", err)
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		} else {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if srv.reloadFn == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if err := srv.reloadFn(); err != nil {
		slog.Error("reload failed", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	slog.Info("config reloaded via API")
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (srv *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if srv.shutdownFn == nil {
		http.Error(w, `{"error":"shutdown not available"}`, http.StatusServiceUnavailable)
		return
	}
	slog.Info("shutdown requested via API")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutdown queued"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go srv.shutdownFn()
}

func (srv *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// When NATS is available, subscribe directly — no subscriber limit.
	if srv.natsConn != nil {
		srv.handleSSEViaNATS(w, r, flusher)
		return
	}

	// Fallback: use the in-memory broker (legacy path, max 10 subscribers).
	ch := srv.broker.Subscribe()
	if ch == nil {
		http.Error(w, "too many SSE connections", http.StatusServiceUnavailable)
		return
	}
	defer srv.broker.Unsubscribe(ch)

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, event.Format())
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (srv *Server) handleSSEViaNATS(w http.ResponseWriter, r *http.Request, flusher http.Flusher) {
	// Core NATS subscription (not JetStream) — ephemeral fan-out, no persistence.
	ch := make(chan *nats.Msg, 64)
	sub, err := srv.natsConn.ChanSubscribe("heimdallm.events.>", ch)
	if err != nil {
		slog.Error("SSE: NATS subscribe failed", "err", err)
		http.Error(w, "SSE subscription failed", http.StatusInternalServerError)
		return
	}
	defer sub.Unsubscribe()

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Keep-alive ticker prevents proxies/load balancers from closing idle connections.
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case msg := <-ch:
			eventType := strings.TrimPrefix(msg.Subject, "heimdallm.events.")
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(msg.Data))
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (srv *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := srv.store.ListAgents()
	if err != nil {
		slog.Error("handleListAgents: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []*store.Agent{}
	}
	writeJSON(w, http.StatusOK, agents)
}

func (srv *Server) handleUpsertAgent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	var a store.Agent
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if a.ID == "" || a.Name == "" {
		http.Error(w, "id and name are required", http.StatusBadRequest)
		return
	}
	// Validate CLI name against the executor allowlist (claude, gemini, codex).
	if a.CLI != "" {
		if err := executor.ValidateCLIName(a.CLI); err != nil {
			http.Error(w, fmt.Sprintf("invalid cli: %v", err), http.StatusBadRequest)
			return
		}
	}
	// Validate CLIFlags to reject dangerous flags before persisting.
	if a.CLIFlags != "" {
		if err := executor.ValidateExtraFlags(a.CLIFlags); err != nil {
			http.Error(w, fmt.Sprintf("invalid cli_flags: %v", err), http.StatusBadRequest)
			return
		}
	}
	if err := srv.store.UpsertAgent(&a); err != nil {
		slog.Error("handleUpsertAgent: store error", "id", a.ID, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (srv *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := srv.store.DeleteAgent(id); err != nil {
		slog.Error("handleDeleteAgent: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (srv *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if srv.meFn == nil {
		writeJSON(w, http.StatusOK, map[string]string{"login": ""})
		return
	}
	login, err := srv.meFn()
	if err != nil {
		slog.Error("handleMe: fetch authenticated user", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"login": login})
}

// ── Issue endpoints ──────────────────────────────────────────────────────────

// issueResponse wraps a store.Issue for JSON serialization, parsing the
// Assignees/Labels JSON strings into proper arrays so the API consumer
// receives []string instead of a JSON-encoded string.
type issueResponse struct {
	ID           int64                `json:"id"`
	GithubID     int64                `json:"github_id"`
	Repo         string               `json:"repo"`
	Number       int                  `json:"number"`
	Title        string               `json:"title"`
	Body         string               `json:"body"`
	Author       string               `json:"author"`
	Assignees    json.RawMessage      `json:"assignees"`
	Labels       json.RawMessage      `json:"labels"`
	State        string               `json:"state"`
	CreatedAt    time.Time            `json:"created_at"`
	FetchedAt    time.Time            `json:"fetched_at"`
	Dismissed    bool                 `json:"dismissed"`
	LatestReview *issueReviewResponse `json:"latest_review,omitempty"`
}

// issueReviewResponse wraps a store.IssueReview, parsing Triage/Suggestions
// JSON strings into structured objects.
type issueReviewResponse struct {
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

func toIssueResponse(iss *store.Issue, rev *store.IssueReview) issueResponse {
	resp := issueResponse{
		ID: iss.ID, GithubID: iss.GithubID, Repo: iss.Repo,
		Number: iss.Number, Title: iss.Title, Body: iss.Body,
		Author: iss.Author, State: iss.State,
		Assignees: json.RawMessage(iss.Assignees),
		Labels:    json.RawMessage(iss.Labels),
		CreatedAt: iss.CreatedAt, FetchedAt: iss.FetchedAt,
		Dismissed: iss.Dismissed,
	}
	if rev != nil {
		resp.LatestReview = toIssueReviewResponse(rev)
	}
	return resp
}

func toIssueReviewResponse(r *store.IssueReview) *issueReviewResponse {
	return &issueReviewResponse{
		ID: r.ID, IssueID: r.IssueID, CLIUsed: r.CLIUsed,
		Summary:     r.Summary,
		Triage:      json.RawMessage(r.Triage),
		Suggestions: json.RawMessage(r.Suggestions),
		ActionTaken: r.ActionTaken, PRCreated: r.PRCreated,
		CreatedAt: r.CreatedAt,
	}
}

func (srv *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	var states []string
	if s := r.URL.Query().Get("state"); s != "" {
		states = strings.Split(s, ",")
	}
	issues, err := srv.store.ListIssues(states...)
	if err != nil {
		slog.Error("handleListIssues: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]issueResponse, 0, len(issues))
	for _, iss := range issues {
		rev, _ := srv.store.LatestIssueReview(iss.ID)
		result = append(result, toIssueResponse(iss, rev))
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	iss, err := srv.store.GetIssue(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	reviews, _ := srv.store.ListIssueReviews(id)
	reviewResps := make([]*issueReviewResponse, 0, len(reviews))
	for _, rev := range reviews {
		reviewResps = append(reviewResps, toIssueReviewResponse(rev))
	}
	issResp := toIssueResponse(iss, nil)
	writeJSON(w, http.StatusOK, map[string]any{"issue": issResp, "reviews": reviewResps})
}

func (srv *Server) handleDismissIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := srv.store.DismissIssue(id); err != nil {
		slog.Error("handleDismissIssue: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

func (srv *Server) handleUndismissIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := srv.store.UndismissIssue(id); err != nil {
		slog.Error("handleUndismissIssue: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "undismissed"})
}

func (srv *Server) handleTriggerIssueReview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if srv.triggerIssueReviewFn == nil {
		http.Error(w, "issue review trigger not configured", http.StatusServiceUnavailable)
		return
	}
	// Shared semaphore with PR reviews — intentional. Both review types spawn
	// AI CLI processes, which are the real concurrency bottleneck. A single
	// global cap prevents overloading the machine with concurrent CLI invocations.
	select {
	case srv.reviewSem <- struct{}{}:
	default:
		http.Error(w, `{"error":"too many concurrent reviews — try again later"}`, http.StatusTooManyRequests)
		return
	}
	go func() {
		defer func() { <-srv.reviewSem }()
		if err := srv.triggerIssueReviewFn(id); err != nil {
			slog.Error("trigger issue review failed", "issue_id", id, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "review queued"})
}

// handlePromoteIssue promotes a review_only-classified issue to auto_implement,
// triggering the full develop pipeline immediately without waiting for a label
// change on GitHub. It shares the same semaphore as review and triage so total
// concurrent AI processes stay bounded.
func (srv *Server) handlePromoteIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if srv.triggerPromoteFn == nil {
		http.Error(w, "promote trigger not configured", http.StatusServiceUnavailable)
		return
	}
	select {
	case srv.reviewSem <- struct{}{}:
	default:
		http.Error(w, `{"error":"too many concurrent reviews — try again later"}`, http.StatusTooManyRequests)
		return
	}
	go func() {
		defer func() { <-srv.reviewSem }()
		if err := srv.triggerPromoteFn(id); err != nil {
			slog.Error("promote issue failed", "issue_id", id, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "promote queued"})
}

func (srv *Server) handleRepoLabels(w http.ResponseWriter, r *http.Request) {
	repo, _ := url.PathUnescape(chi.URLParam(r, "name"))
	if srv.fetchLabelsFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	labels, err := srv.fetchLabelsFn(repo)
	if err != nil {
		slog.Error("handleRepoLabels: fetch error", "repo", repo, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if labels == nil {
		labels = []string{}
	}
	writeJSON(w, http.StatusOK, labels)
}

func (srv *Server) handleRepoCollaborators(w http.ResponseWriter, r *http.Request) {
	repo, _ := url.PathUnescape(chi.URLParam(r, "name"))
	if srv.fetchCollaboratorsFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	collabs, err := srv.fetchCollaboratorsFn(repo)
	if err != nil {
		slog.Error("handleRepoCollaborators: fetch error", "repo", repo, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if collabs == nil {
		collabs = []string{}
	}
	writeJSON(w, http.StatusOK, collabs)
}

func (srv *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var repos, orgs []string
	if raw := r.URL.Query().Get("repos"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				repos = append(repos, s)
			}
		}
	}
	if raw := r.URL.Query().Get("orgs"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				orgs = append(orgs, s)
			}
		}
	}
	stats, err := srv.store.ComputeStats(repos, orgs)
	if err != nil {
		slog.Error("handleStats: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleActivity returns rows from activity_log matching the query.
// Query params (all optional, combined with AND):
//
//	date=YYYY-MM-DD                  — single day in daemon local TZ
//	from=YYYY-MM-DD & to=YYYY-MM-DD  — inclusive range in daemon local TZ
//	org=... (repeatable)             — org filter
//	repo=... (repeatable)            — repo filter (full slug "org/name")
//	action=review|triage|implement|promote|error (repeatable)
//	limit=N (default 500, max 5000)
//
// Default when neither date nor from/to is supplied: today in daemon local TZ.
// Returns 503 when activity_log.enabled = false.
func (srv *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	// 503 when explicitly disabled. When configFn is unset (tests that
	// don't set one) we assume enabled.
	if srv.configFn != nil {
		m := srv.configFn()
		if v, ok := m["activity_log_enabled"]; ok {
			if enabled, ok := v.(bool); ok && !enabled {
				httpJSONErr(w, http.StatusServiceUnavailable, "activity log disabled")
				return
			}
		}
	}

	q := r.URL.Query()

	date := q.Get("date")
	from := q.Get("from")
	to := q.Get("to")
	if date != "" && (from != "" || to != "") {
		httpJSONErr(w, http.StatusBadRequest, "date cannot be combined with from/to")
		return
	}
	if (from == "") != (to == "") {
		httpJSONErr(w, http.StatusBadRequest, "from and to must be supplied together")
		return
	}

	loc := time.Now().Location() // daemon local TZ
	parseDay := func(s string) (time.Time, error) {
		return time.ParseInLocation("2006-01-02", s, loc)
	}

	var start, end time.Time
	switch {
	case date != "":
		d, err := parseDay(date)
		if err != nil {
			httpJSONErr(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
		start = d
		end = d.Add(24 * time.Hour) // exclusive upper bound: start of next day
	case from != "":
		f, err := parseDay(from)
		if err != nil {
			httpJSONErr(w, http.StatusBadRequest, "from must be YYYY-MM-DD")
			return
		}
		t2, err := parseDay(to)
		if err != nil {
			httpJSONErr(w, http.StatusBadRequest, "to must be YYYY-MM-DD")
			return
		}
		if t2.Before(f) {
			httpJSONErr(w, http.StatusBadRequest, "to must not be before from")
			return
		}
		start = f
		end = t2.Add(24 * time.Hour) // exclusive upper bound: start of day after `to`
	default:
		now := time.Now()
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		end = start.Add(24 * time.Hour) // exclusive upper bound: start of tomorrow
	}

	limit := 500
	if ls := q.Get("limit"); ls != "" {
		n, err := strconv.Atoi(ls)
		if err != nil || n < 1 || n > 5000 {
			httpJSONErr(w, http.StatusBadRequest, "limit must be 1..5000")
			return
		}
		limit = n
	}

	entries, truncated, err := srv.store.ListActivity(store.ActivityQuery{
		From:    start,
		To:      end,
		Orgs:    q["org"],
		Repos:   q["repo"],
		Actions: q["action"],
		Limit:   limit,
	})
	if err != nil {
		slog.Error("activity: list failed", "err", err)
		httpJSONErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	type entryOut struct {
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
	out := make([]entryOut, 0, len(entries))
	for _, a := range entries {
		var details map[string]any
		if a.DetailsJSON != "" {
			_ = json.Unmarshal([]byte(a.DetailsJSON), &details)
		}
		if details == nil {
			details = map[string]any{}
		}
		out = append(out, entryOut{
			ID:         a.ID,
			TS:         a.Timestamp.In(loc).Format(time.RFC3339),
			Org:        a.Org,
			Repo:       a.Repo,
			ItemType:   a.ItemType,
			ItemNumber: a.ItemNumber,
			ItemTitle:  a.ItemTitle,
			Action:     a.Action,
			Outcome:    a.Outcome,
			Details:    details,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":   out,
		"count":     len(out),
		"truncated": truncated,
	})
}

// httpJSONErr writes a JSON {"error": msg} body with the given HTTP status.
func httpJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// DaemonLogFileName is the name of the on-disk log file the daemon writes
// alongside heimdallm.db inside the resolved data directory. Shared between
// the writer (cmd/heimdallm setupLogging) and the reader (daemonLogPath
// below) so the two sides cannot drift.
const DaemonLogFileName = "heimdallm.log"

// daemonLogPath returns the path to the daemon log file the /logs stream
// tails. Priority (matches cmd/heimdallm/main.go's dataDir() ordering, which
// is the directory setupLogging writes to):
//
//  1. $HEIMDALLM_DATA_DIR/heimdallm.log — explicit override (native or Docker).
//  2. /data/heimdallm.log — Docker convention, used when /data exists as a
//     directory (the compose file mounts the heimdallm-data volume there).
//  3. macOS: ~/Library/Logs/heimdallm/heimdallm-daemon-error.log — LaunchAgent
//     convention; the plist redirects stderr there so the file pre-exists
//     without setupLogging having to write it.
//  4. Linux/other: $XDG_STATE_HOME/heimdallm/heimdallm.log, fallback
//     ~/.local/share/heimdallm/heimdallm.log.
//
// See #75 — previously (1) and (2) did not exist and the endpoint always
// returned "file not found" under Docker because stderr was redirected to
// `docker logs`, never to a file.
func daemonLogPath() string {
	if v := os.Getenv("HEIMDALLM_DATA_DIR"); v != "" {
		return filepath.Join(v, DaemonLogFileName)
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return filepath.Join("/data", DaemonLogFileName)
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "heimdallm", "heimdallm-daemon-error.log")
	default:
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			return filepath.Join(xdg, "heimdallm", DaemonLogFileName)
		}
		return filepath.Join(home, ".local", "share", "heimdallm", DaemonLogFileName)
	}
}

func (srv *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	logPath := daemonLogPath()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	emit := func(line string) {
		escaped, _ := json.Marshal(line)
		fmt.Fprintf(w, "event: log_line\ndata: {\"line\":%s}\n\n", escaped)
		flush()
	}

	// Check if file exists.
	f, err := os.Open(logPath)
	if err != nil {
		emit(fmt.Sprintf("(log file not found at %s — daemon may be running in dev mode or log path differs)", logPath))
		return
	}
	defer f.Close()

	// Read last 300 lines.
	for _, line := range tailLines(f, 300) {
		emit(line)
	}

	// Get current offset.
	offset, _ := f.Seek(0, io.SeekCurrent)

	// Polling loop.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			f2, err := os.Open(logPath)
			if err != nil {
				continue
			}
			// Detect rotation: if the on-disk file is now smaller than
			// our saved offset, the rotator renamed the old file and
			// re-created a fresh one. Reset to 0 so we stream the new
			// content from the beginning rather than stalling at an
			// offset that points past EOF. See #77.
			if stat, err := f2.Stat(); err == nil && stat.Size() < offset {
				offset = 0
			}
			f2.Seek(offset, io.SeekStart) //nolint:errcheck
			scanner := bufio.NewScanner(f2)
			for scanner.Scan() {
				line := scanner.Text()
				if line != "" {
					emit(line)
				}
			}
			offset, _ = f2.Seek(0, io.SeekCurrent)
			f2.Close()
		}
	}
}

// tailLines returns up to n lines from the end of f.
// Reads the file in reverse chunks to avoid loading the entire file into memory.
func tailLines(f *os.File, n int) []string {
	const chunkSize = 4096
	stat, err := f.Stat()
	if err != nil {
		return nil
	}
	size := stat.Size()
	if size == 0 {
		return nil
	}

	var lines []string
	var partial string
	pos := size

	for len(lines) < n && pos > 0 {
		readSize := int64(chunkSize)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		buf := make([]byte, readSize)
		f.ReadAt(buf, pos) //nolint:errcheck
		chunk := string(buf) + partial
		parts := strings.Split(chunk, "\n")
		partial = parts[0]
		for i := len(parts) - 1; i >= 1; i-- {
			if parts[i] != "" {
				lines = append([]string{parts[i]}, lines...)
			}
		}
	}
	if partial != "" {
		lines = append([]string{partial}, lines...)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// Seek file to end of last line read.
	f.Seek(size, io.SeekStart) //nolint:errcheck
	return lines
}
