package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// renameRequestTimeout caps the reconciler call so a stuck SQLite
// writer or slow GitHub probe in the same daemon can't make the admin
// endpoint hang indefinitely.
const renameRequestTimeout = 30 * time.Second

type repoRenameRequest struct {
	OldRepo string `json:"old_repo"`
	NewRepo string `json:"new_repo"`
}

// handleAdminRepoRename manually triggers the rename reconciler for an
// (old_repo, new_repo) pair. Mirror of the path the rename probe takes
// on a canonical-name mismatch — intended for emergencies (operator
// has just renamed a repo on GitHub and does not want to wait for the
// next probe tick) and for tests/integration. Idempotent: re-running
// with the same pair after the reconciler has already committed is a
// no-op at the store layer and silently returns 200.
func (srv *Server) handleAdminRepoRename(w http.ResponseWriter, r *http.Request) {
	if srv.repoRenameFn == nil {
		httpJSONErr(w, http.StatusServiceUnavailable, "rename trigger not available")
		return
	}
	var req repoRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpJSONErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.OldRepo = strings.TrimSpace(req.OldRepo)
	req.NewRepo = strings.TrimSpace(req.NewRepo)
	if req.OldRepo == "" || req.NewRepo == "" {
		httpJSONErr(w, http.StatusBadRequest, "old_repo and new_repo are required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), renameRequestTimeout)
	defer cancel()
	if err := srv.repoRenameFn(ctx, req.OldRepo, req.NewRepo); err != nil {
		slog.Error("POST /admin/repo-rename failed",
			"old_repo", req.OldRepo, "new_repo", req.NewRepo, "err", err)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			httpJSONErr(w, http.StatusGatewayTimeout, "rename timed out")
			return
		}
		// Validation errors (empty/identical/malformed slugs) surface
		// as 400 so an operator typo doesn't trip a 500. The reconciler
		// wraps its own ErrInvalidRepoSlug into the returned error, so
		// we use a simple substring match — no need to import the
		// rename package here for one sentinel check.
		if strings.Contains(err.Error(), "invalid repo slug") {
			// httpJSONErr escapes the message so a slug with a quote/backslash
			// can't break the JSON body. See #552.
			httpJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSONErr(w, http.StatusInternalServerError, "rename failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"old_repo": req.OldRepo,
		"new_repo": req.NewRepo,
	})
}
