package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/heimdallr/daemon/internal/pipeline"
	"github.com/heimdallr/daemon/internal/sse"
	"github.com/heimdallr/daemon/internal/store"
)

// Server holds the HTTP router, SSE broker, store, and optional pipeline.
type Server struct {
	store      *store.Store
	broker     *sse.Broker
	pipeline   *pipeline.Pipeline
	router     chi.Router
	httpServer *http.Server
	reloadFn        func() error
	triggerReviewFn func(prID int64) error
	meFn            func() (string, error)
	// configFn returns the current running config as a JSON-serializable map.
	configFn func() map[string]any
}

// New creates a new Server. p may be nil if the pipeline is not yet configured.
func New(s *store.Store, broker *sse.Broker, p *pipeline.Pipeline) *Server {
	srv := &Server{store: s, broker: broker, pipeline: p}
	srv.router = srv.buildRouter()
	return srv
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
		Addr:    addr,
		Handler: srv.router,
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

func (srv *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	for k, v := range body {
		val := fmt.Sprintf("%v", v)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := srv.broker.Subscribe()
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []*store.Agent{}
	}
	writeJSON(w, http.StatusOK, agents)
}

func (srv *Server) handleUpsertAgent(w http.ResponseWriter, r *http.Request) {
	var a store.Agent
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if a.ID == "" || a.Name == "" {
		http.Error(w, "id and name are required", http.StatusBadRequest)
		return
	}
	if err := srv.store.UpsertAgent(&a); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (srv *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := srv.store.DeleteAgent(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"login": login})
}

func (srv *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := srv.store.ComputeStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
