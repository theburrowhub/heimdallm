package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/heimdallm/daemon/internal/config"
)

// FlowSimulation is a POST /flows/simulate request: evaluate Flow (or, when
// empty, the flow Repo resolves to) at At (default now).
type FlowSimulation struct {
	Flow string    `json:"flow"`
	Repo string    `json:"repo"`
	At   time.Time `json:"at"`
}

// SetFlowFns wires the flow listing and simulation, which need the live
// config, the agent availability and the quota service that main owns.
func (srv *Server) SetFlowFns(list func() any, simulate func(ctx context.Context, req FlowSimulation) (any, error)) {
	srv.flowsListFn = list
	srv.flowSimulateFn = simulate
}

// SetQuotasFn wires GET /quotas.
func (srv *Server) SetQuotasFn(fn func(ctx context.Context) any) { srv.quotasFn = fn }

// handleQuotas returns every agent's remaining quota.
func (srv *Server) handleQuotas(w http.ResponseWriter, r *http.Request) {
	if srv.quotasFn == nil {
		http.Error(w, `{"error":"quotas not available"}`, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, srv.quotasFn(r.Context()))
}

// handleListFlows returns the configured flows (plus the default flow
// synthesised from primary/fallback) and which one is selected globally.
func (srv *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	if srv.flowsListFn == nil {
		http.Error(w, `{"error":"flows not available"}`, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, srv.flowsListFn())
}

// handleSimulateFlow evaluates a flow now (or at a given time) and explains
// every rule, so the operator can check a flow before relying on it.
func (srv *Server) handleSimulateFlow(w http.ResponseWriter, r *http.Request) {
	if srv.flowSimulateFn == nil {
		http.Error(w, `{"error":"flows not available"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req FlowSimulation
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.Flow != "" {
		if err := config.ValidateFlowID(req.Flow); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.Repo != "" {
		if err := config.ValidateRepoSlug(req.Repo); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	out, err := srv.flowSimulateFn(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// flowToMap converts a flow to the TOML map shape (rules keyed by order).
func flowToMap(f config.FlowConfig) (map[string]any, error) {
	buf, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}
	config.NormalizeNumbers(m)
	return m, nil
}

func childMap(m map[string]any, key string) map[string]any {
	child, ok := m[key].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[key] = child
	}
	return child
}

// handlePutFlow creates or replaces a flow. Replacing (not merging) is what
// lets the editor remove rules and conditions.
func (srv *Server) handlePutFlow(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"flows cannot be edited — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "id")
	if err := config.ValidateFlowID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if id == config.DefaultFlowID {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the default flow comes from primary/fallback; create a flow with another id",
		})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	var flow config.FlowConfig
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&flow); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid flow: " + err.Error()})
		return
	}
	if len(flow.Rules) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a flow needs at least one rule"})
		return
	}
	if err := config.ValidateFlow(id, flow); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	flowMap, err := flowToMap(flow)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	result, err := srv.patchTOML(func(m map[string]any) error {
		childMap(childMap(m, "ai"), "flows")[id] = flowMap
		return nil
	})
	srv.writeConfigResult(w, "PUT /flows", result, err)
}

// handleDeleteFlow removes a flow and every selection that pointed at it, so
// those repos fall back to their parent's flow instead of failing validation.
func (srv *Server) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"flows cannot be edited — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "id")
	if err := config.ValidateFlowID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := srv.patchTOML(func(m map[string]any) error {
		ai := childMap(m, "ai")
		flows := childMap(ai, "flows")
		if _, ok := flows[id]; !ok {
			return errFlowNotFound
		}
		delete(flows, id)
		if ai["flow"] == id {
			delete(ai, "flow")
		}
		for _, scope := range []string{"orgs", "repos"} {
			if entries, ok := ai[scope].(map[string]any); ok {
				for _, raw := range entries {
					if entry, ok := raw.(map[string]any); ok && entry["flow"] == id {
						delete(entry, "flow")
					}
				}
			}
		}
		return nil
	})
	if errors.Is(err, errFlowNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no flow %q", id)})
		return
	}
	srv.writeConfigResult(w, "DELETE /flows", result, err)
}

var errFlowNotFound = errors.New("flow not found")

func (srv *Server) writeConfigResult(w http.ResponseWriter, op string, result map[string]any, err error) {
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		slog.Error(op+" failed", "err", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
