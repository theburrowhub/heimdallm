package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/heimdallm/daemon/internal/store"
)

// mergeTrackingEntry is the API shape of one tracked PR.
//
// It joins the PR row so the client gets title, URL and author without a second
// request, and it always carries the check summary — the listing renders its
// warning straight from these counts.
type mergeTrackingEntry struct {
	PRID   int64  `json:"pr_id"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	Author string `json:"author,omitempty"`

	Phase       string `json:"phase"`
	HeadSHA     string `json:"head_sha,omitempty"`
	BaseRef     string `json:"base_ref,omitempty"`
	HeadRef     string `json:"head_ref,omitempty"`
	BlockReason string `json:"block_reason,omitempty"`
	BlockDetail string `json:"block_detail,omitempty"`

	IsAuthor   bool `json:"is_author"`
	IsAssignee bool `json:"is_assignee"`
	Excluded   bool `json:"excluded"`

	ChecksRequiredFailing int `json:"checks_required_failing"`
	ChecksRequiredPending int `json:"checks_required_pending"`

	AutoMergeArmedAt string `json:"auto_merge_armed_at,omitempty"`
	AutoMergeMethod  string `json:"auto_merge_method,omitempty"`
	PreRebaseSHA     string `json:"pre_rebase_sha,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	CooldownUntil    string `json:"cooldown_until,omitempty"`
	EvaluatedAt      string `json:"evaluated_at,omitempty"`
	MergedAt         string `json:"merged_at,omitempty"`

	// Decision is the full explainable decision, including every check with its
	// state, whether it is required, the app that ran it and a link to the log.
	// Present on the detail endpoint; omitted from the listing to keep it small.
	Decision json.RawMessage `json:"decision,omitempty"`
}

// buildMergeTrackingEntry converts a store row, optionally including the full
// decision payload.
func (srv *Server) buildMergeTrackingEntry(row *store.MergeTracking, withDecision bool) mergeTrackingEntry {
	e := mergeTrackingEntry{
		PRID:                  row.PRID,
		Repo:                  row.Repo,
		Number:                row.Number,
		Phase:                 row.Phase,
		HeadSHA:               row.HeadSHA,
		BaseRef:               row.BaseRef,
		HeadRef:               row.HeadRef,
		BlockReason:           row.BlockReason,
		BlockDetail:           row.BlockDetail,
		IsAuthor:              row.IsAuthor,
		IsAssignee:            row.IsAssignee,
		Excluded:              row.Excluded,
		ChecksRequiredFailing: row.ChecksRequiredFailing,
		ChecksRequiredPending: row.ChecksRequiredPending,
		AutoMergeMethod:       row.AutoMergeMethod,
		PreRebaseSHA:          row.PreRebaseSHA,
		LastError:             row.LastError,
	}
	e.AutoMergeArmedAt = formatOptionalTime(row.AutoMergeArmedAt)
	e.CooldownUntil = formatOptionalTime(row.CooldownUntil)
	e.EvaluatedAt = formatOptionalTime(row.EvaluatedAt)
	e.MergedAt = formatOptionalTime(row.MergedAt)

	if pr, err := srv.store.GetPR(row.PRID); err == nil && pr != nil {
		e.Title = pr.Title
		e.URL = pr.URL
		e.Author = pr.Author
	}
	if withDecision && row.DecisionJSON != "" {
		// Stored as JSON we produced ourselves; pass it through rather than
		// round-tripping it through a struct that would have to be kept in
		// sync with the evaluator.
		if json.Valid([]byte(row.DecisionJSON)) {
			e.Decision = json.RawMessage(row.DecisionJSON)
		}
	}
	return e
}

// handleListMergeTracking serves GET /merge-tracking.
//
// Served entirely from the local store: the reconciler already persisted every
// decision, so opening the tab costs no GitHub budget and is instant.
func (srv *Server) handleListMergeTracking(w http.ResponseWriter, r *http.Request) {
	rows, err := srv.store.ListMergeTracking()
	if err != nil {
		slog.Error("handleListMergeTracking: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]mergeTrackingEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, srv.buildMergeTrackingEntry(row, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetMergeTracking serves GET /merge-tracking/{prID}, including the full
// per-check breakdown the PR detail view renders.
func (srv *Server) handleGetMergeTracking(w http.ResponseWriter, r *http.Request) {
	prID, ok := mergeTrackingID(w, r)
	if !ok {
		return
	}
	row, err := srv.store.GetMergeTracking(prID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, srv.buildMergeTrackingEntry(row, true))
}

// handleEvaluateMergeTracking serves POST /merge-tracking/{prID}/evaluate.
//
// With ?dry_run=true it re-reads GitHub and records the decision without
// acting, which is the honest answer to "why is this stuck?". Without it, the
// PR becomes due immediately and the next cycle acts on it.
func (srv *Server) handleEvaluateMergeTracking(w http.ResponseWriter, r *http.Request) {
	prID, ok := mergeTrackingID(w, r)
	if !ok {
		return
	}
	if srv.mergeTrackEvaluateFn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "merge tracking not configured",
		})
		return
	}
	dryRun := r.URL.Query().Get("dry_run") == "true"
	if err := srv.mergeTrackEvaluateFn(r.Context(), prID, dryRun); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	row, err := srv.store.GetMergeTracking(prID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, srv.buildMergeTrackingEntry(row, true))
}

// handleExcludeMergeTracking serves POST /merge-tracking/{prID}/exclude and
// /include: a per-PR opt-out that needs no config edit.
func (srv *Server) handleExcludeMergeTracking(excluded bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prID, ok := mergeTrackingID(w, r)
		if !ok {
			return
		}
		if err := srv.store.SetMergeTrackingExcluded(prID, excluded); err != nil {
			slog.Error("handleExcludeMergeTracking: store error", "pr_id", prID, "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		status := "included"
		if excluded {
			status = "excluded"
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	}
}

func mergeTrackingID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	prID, err := strconv.ParseInt(chi.URLParam(r, "prID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return prID, true
}

// formatOptionalTime renders a timestamp as RFC3339, or the empty string for
// the zero value so absent timestamps stay absent in the JSON rather than
// appearing as year 1.
func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
