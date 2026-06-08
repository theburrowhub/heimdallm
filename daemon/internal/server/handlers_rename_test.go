package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleAdminRepoRename_RequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	srv.SetRepoRenameFn(func(ctx context.Context, oldRepo, newRepo string) error {
		return nil
	})

	req := httptest.NewRequest("POST", "/admin/repo-rename",
		strings.NewReader(`{"old_repo":"acme/old","new_repo":"acme/new"}`))
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST without token = %d, want 401", w.Code)
	}
}

func TestHandleAdminRepoRename_DispatchesReconciler(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	var got [2]string
	srv.SetRepoRenameFn(func(ctx context.Context, oldRepo, newRepo string) error {
		got = [2]string{oldRepo, newRepo}
		return nil
	})

	req := httptest.NewRequest("POST", "/admin/repo-rename",
		strings.NewReader(`{"old_repo":"acme/old","new_repo":"acme/new"}`))
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got != [2]string{"acme/old", "acme/new"} {
		t.Errorf("dispatched pair = %v, want (acme/old, acme/new)", got)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %s, want status ok", w.Body.String())
	}
}

func TestHandleAdminRepoRename_RejectsEmptySlugs(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	srv.SetRepoRenameFn(func(ctx context.Context, oldRepo, newRepo string) error {
		t.Fatal("reconciler must not be called with empty slugs")
		return nil
	})

	for _, body := range []string{
		`{"old_repo":"","new_repo":"acme/new"}`,
		`{"old_repo":"acme/old","new_repo":""}`,
		`{"old_repo":"   ","new_repo":"acme/new"}`,
	} {
		req := httptest.NewRequest("POST", "/admin/repo-rename", strings.NewReader(body))
		req.Header.Set("X-Heimdallm-Token", "test-token")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q -> status %d, want 400", body, w.Code)
		}
	}
}

func TestHandleAdminRepoRename_SurfacesValidationErrorAs400(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	srv.SetRepoRenameFn(func(ctx context.Context, oldRepo, newRepo string) error {
		return errors.New("rename: invalid repo slug: expected owner/name shape")
	})

	req := httptest.NewRequest("POST", "/admin/repo-rename",
		strings.NewReader(`{"old_repo":"malformed","new_repo":"acme/new"}`))
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleAdminRepoRename_503WhenNotWired(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	// Intentionally do NOT call SetRepoRenameFn; admin endpoint must
	// surface the wiring gap as 503, not 500 or 404.
	req := httptest.NewRequest("POST", "/admin/repo-rename",
		strings.NewReader(`{"old_repo":"acme/old","new_repo":"acme/new"}`))
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
