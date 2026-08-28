package server

import (
	"encoding/json"
	"fmt"
	"io"
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

// handleAddMergeTracking serves POST /merge-tracking/add.
//
// The review pipeline refuses a PR the authenticated account authored — and
// since Heimdallm authenticates as the operator's own account, that is every PR
// they open. POST /prs/add routes through that pipeline, so it was the wrong
// door for the one feature that exists precisely for the operator's own PRs:
// pasting a link there produced a `self_authored` skip and nothing else.
//
// This endpoint is the right door. It stores the PR, makes sure its repository
// is monitored, enrols it in merge tracking and stops. No review is triggered
// and no self-author guard applies. Whether the PR is really the operator's is
// decided where it belongs — by the reconciler's next evaluation, against
// GitHub's own view of author and assignees.
//
// Body: {"url": "https://github.com/owner/repo/pull/123"}.
func (srv *Server) handleAddMergeTracking(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	repo, number, err := parsePRURL(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if srv.addPRFn == nil || srv.mergeTrackEnrolFn == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "merge tracking not configured",
		})
		return
	}

	// Validate against GitHub before touching config: a typo must not leave a
	// repository monitored forever.
	pr, err := srv.addPRFn(repo, number)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("fetch PR %s#%d: %v", repo, number, err),
		})
		return
	}

	// Discovery intersects with the monitored list, so a PR in an unmonitored
	// repo would be enrolled now and dropped on the next cycle.
	if srv.configPath != "" {
		if _, err := srv.patchTOML(func(m map[string]any) error {
			addRepoToTOMLMap(m, repo)
			return nil
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("add repo to config: %v", err),
			})
			return
		}
	}

	if err := srv.mergeTrackEnrolFn(pr.ID, repo, number); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("enrol %s#%d: %v", repo, number, err),
		})
		return
	}

	row, err := srv.store.GetMergeTracking(pr.ID)
	if err != nil {
		// Enrolled but unreadable: report the add as done rather than failing
		// an operation that already took effect.
		slog.Warn("handleAddMergeTracking: read back row", "pr_id", pr.ID, "err", err)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "pr enrolled", "pr": pr})
		return
	}
	writeJSON(w, http.StatusAccepted, srv.buildMergeTrackingEntry(row, true))
}

// handleEvaluateMergeTracking serves POST /merge-tracking/{prID}/evaluate.
//
// With ?dry_run=true it re-reads GitHub and records the decision without
// acting, which is the honest answer to "why is this stuck?". Without it, the
// PR becomes due immediately and the next cycle acts on it.
//
// The acting path deliberately does NOT run the action inside the request: an
// arm, a merge or a half-hour conflict-resolution agent run has no business
// being bound to an HTTP connection, where a client disconnect would cancel it
// mid-rebase. Clearing the cooldown hands the work to the reconciler, which
// owns the claim, the work gate and the retry accounting.
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
	if r.URL.Query().Get("dry_run") == "true" {
		if err := srv.mergeTrackEvaluateFn(r.Context(), prID, true); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	} else if err := srv.store.ClearMergeTrackingCooldown(prID); err != nil {
		slog.Error("handleEvaluateMergeTracking: clear cooldown", "pr_id", prID, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
