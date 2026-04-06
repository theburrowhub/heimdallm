package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/heimdallr/daemon/internal/executor"
	"github.com/heimdallr/daemon/internal/pipeline"
	"github.com/heimdallr/daemon/internal/sse"
	"github.com/heimdallr/daemon/internal/store"
)

// Server holds the HTTP router, SSE broker, store, and optional pipeline.
type Server struct {
	store           *store.Store
	broker          *sse.Broker
	pipeline        *pipeline.Pipeline
	router          chi.Router
	httpServer      *http.Server
	reloadFn        func() error
	triggerReviewFn func(prID int64) error
	meFn            func() (string, error)
	// configFn returns the current running config as a JSON-serializable map.
	configFn func() map[string]any
	// apiToken is required on all state-mutating requests (POST/PUT/DELETE).
	// Empty string disables authentication (should not happen in production).
	apiToken string
}

// New creates a new Server. p may be nil if the pipeline is not yet configured.
// apiToken must be the value returned by LoadOrCreateAPIToken — it is required
// on all mutating endpoints to prevent cross-process/browser config poisoning.
func New(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline, apiToken string) *Server {
	srv := &Server{store: s, broker: broker, pipeline: p, apiToken: apiToken}
	srv.router = srv.buildRouter()
	return srv
}

// sensitiveGETPaths lists GET paths that require authentication even though they
// are read-only. This includes endpoints that expose internal config, agent
// data, or activity metadata (SSE stream).
// /health, /me, /prs, /prs/{id}, and /stats are considered safe to read without
// a token as they expose only minimal public-facing information.
var sensitiveGETPaths = []string{"/config", "/agents", "/events", "/logs/stream"}

// authMiddleware rejects:
//   - POST/PUT/PATCH/DELETE requests without a valid X-Heimdallr-Token header.
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
				token := r.Header.Get("X-Heimdallr-Token")
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

// SetTriggerReviewFn wires the review-trigger callback called by POST /prs/{id}/review.
func (srv *Server) SetTriggerReviewFn(fn func(prID int64) error) { srv.triggerReviewFn = fn }

// SetMeFn wires the authenticated-user callback called by GET /me.
func (srv *Server) SetMeFn(fn func() (string, error)) { srv.meFn = fn }

// SetConfigFn wires the callback that returns the live config for GET /config.
func (srv *Server) SetConfigFn(fn func() map[string]any) { srv.configFn = fn }

// Router returns the underlying http.Handler for use in tests or embedding.
func (srv *Server) Router() http.Handler {
	return srv.router
}

// Start binds the HTTP server to the given port and begins serving.
func (srv *Server) Start(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
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
	r.Get("/stats", srv.handleStats)
	r.Get("/agents", srv.handleListAgents)
	r.Post("/agents", srv.handleUpsertAgent)
	r.Delete("/agents/{id}", srv.handleDeleteAgent)
	r.Get("/config", srv.handleGetConfig)
	r.Put("/config", srv.handlePutConfig)
	r.Post("/reload", srv.handleReload)
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
	prs, err := srv.store.ListPRs()
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
	go func() {
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

// validConfigKeys is the exhaustive allowlist of keys that callers are permitted
// to write via PUT /config. Any key outside this set is rejected with HTTP 400
// to prevent arbitrary data injection into the configs table (security issue #4).
var validConfigKeys = map[string]struct{}{
	"server_port":    {},
	"poll_interval":  {},
	"repositories":   {},
	"ai_primary":     {},
	"ai_fallback":    {},
	"review_mode":    {},
	"retention_days": {},
}

func (srv *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	for k := range body {
		if _, ok := validConfigKeys[k]; !ok {
			http.Error(w, fmt.Sprintf("unknown config key: %q", k), http.StatusBadRequest)
			return
		}
	}
	for k, v := range body {
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

func (srv *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

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

func (srv *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := srv.store.ComputeStats()
	if err != nil {
		slog.Error("handleStats: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (srv *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "heimdallr", "heimdallr-daemon-error.log")

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
		emit("(log file not found — daemon may be running in dev mode)")
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
