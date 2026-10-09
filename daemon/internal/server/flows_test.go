package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/server"
)

const exampleFlowJSON = `{"name":"Weekday copilot","rules":{
 "10":{"agent":"claude","quota":{"a":{"agent":"claude","window":"session","op":"below","percent":50}}},
 "20":{"agent":"copilot","schedule":{"a":{"days":["mon","fri"],"from":"08:00","to":"15:00","tz":"Europe/Madrid"}}},
 "99":{"agent":"codex"}}}`

func doReq(t *testing.T, srv *server.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func TestPutAndDeleteFlow(t *testing.T) {
	srv, cfgPath := newPatchServer(t)

	if w := doReq(t, srv, http.MethodPut, "/flows/weekday", exampleFlowJSON); w.Code != 200 {
		t.Fatalf("put = %d %s", w.Code, w.Body)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	f := cfg.AI.Flows["weekday"]
	if f.Name != "Weekday copilot" || len(f.Rules) != 3 || f.Rules["20"].Schedule["a"].TZ != "Europe/Madrid" || f.Rules["10"].Quota["a"].Percent != 50 {
		t.Fatalf("stored flow = %+v", f)
	}

	// Replacing drops rules the editor removed.
	if w := doReq(t, srv, http.MethodPut, "/flows/weekday", `{"name":"Only codex","rules":{"1":{"agent":"codex"}}}`); w.Code != 200 {
		t.Fatalf("replace = %d %s", w.Code, w.Body)
	}
	cfg, _ = config.Load(cfgPath)
	if len(cfg.AI.Flows["weekday"].Rules) != 1 {
		t.Fatalf("replace must drop old rules: %+v", cfg.AI.Flows["weekday"])
	}

	// Select it globally and for a repo, then delete: the selections go too.
	if w := doReq(t, srv, http.MethodPatch, "/config", `{"ai":{"flow":"weekday"}}`); w.Code != 200 {
		t.Fatalf("select = %d %s", w.Code, w.Body)
	}
	if w := doReq(t, srv, http.MethodPatch, "/config/repos/acme%2Fapi", `{"flow":"weekday"}`); w.Code != 200 {
		t.Fatalf("select repo = %d %s", w.Code, w.Body)
	}
	if w := doReq(t, srv, http.MethodDelete, "/flows/weekday", ""); w.Code != 200 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	cfg, _ = config.Load(cfgPath)
	if len(cfg.AI.Flows) != 0 || cfg.AI.Flow != "" || cfg.AI.Repos["acme/api"].Flow != "" {
		t.Fatalf("after delete = flows %v flow %q repo %q", cfg.AI.Flows, cfg.AI.Flow, cfg.AI.Repos["acme/api"].Flow)
	}
	if w := doReq(t, srv, http.MethodDelete, "/flows/weekday", ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d", w.Code)
	}
}

func TestPutFlowValidation(t *testing.T) {
	srv, _ := newPatchServer(t)
	cases := map[string][2]string{
		"bad id":        {"/flows/Bad%20Id", `{"rules":{"1":{"agent":"claude"}}}`},
		"default id":    {"/flows/default", `{"rules":{"1":{"agent":"claude"}}}`},
		"bad json":      {"/flows/x", `{`},
		"unknown field": {"/flows/x", `{"rules":{"1":{"agent":"claude"}},"extra":1}`},
		"no rules":      {"/flows/x", `{"name":"x"}`},
		"unknown agent": {"/flows/x", `{"rules":{"1":{"agent":"rm -rf"}}}`},
		"bad window":    {"/flows/x", `{"rules":{"1":{"agent":"claude","quota":{"a":{"agent":"claude","window":"hourly","op":"below","percent":5}}}}}`},
		"bad percent":   {"/flows/x", `{"rules":{"1":{"agent":"claude","quota":{"a":{"agent":"claude","window":"session","op":"below","percent":500}}}}}`},
		"bad op":        {"/flows/x", `{"rules":{"1":{"agent":"claude","quota":{"a":{"agent":"claude","window":"session","op":"near","percent":5}}}}}`},
		"bad time":      {"/flows/x", `{"rules":{"1":{"agent":"claude","schedule":{"a":{"from":"8am","to":"15:00"}}}}}`},
		"same times":    {"/flows/x", `{"rules":{"1":{"agent":"claude","schedule":{"a":{"from":"08:00","to":"08:00"}}}}}`},
		"bad day":       {"/flows/x", `{"rules":{"1":{"agent":"claude","schedule":{"a":{"days":["funday"],"from":"08:00","to":"09:00"}}}}}`},
		"bad tz":        {"/flows/x", `{"rules":{"1":{"agent":"claude","schedule":{"a":{"from":"08:00","to":"09:00","tz":"Mars/Base"}}}}}`},
		"bad match":     {"/flows/x", `{"rules":{"1":{"agent":"claude","match":"some"}}}`},
	}
	for name, c := range cases {
		if w := doReq(t, srv, http.MethodPut, c[0], c[1]); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := doReq(t, srv, http.MethodDelete, "/flows/Bad%20Id", ""); w.Code != http.StatusBadRequest {
		t.Errorf("delete bad id = %d", w.Code)
	}
	if w := doReq(t, srv, http.MethodPatch, "/config", `{"ai":{"flow":"ghost"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("selecting a missing flow = %d %s", w.Code, w.Body)
	}
}

func TestFlowsReadEndpoints(t *testing.T) {
	srv, _ := setupServer(t)
	for _, p := range []string{"/flows", "/quotas"} {
		if w := doReq(t, srv, http.MethodGet, p, ""); w.Code != http.StatusServiceUnavailable {
			t.Errorf("unwired %s = %d", p, w.Code)
		}
	}
	if w := doReq(t, srv, http.MethodPost, "/flows/simulate", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired simulate = %d", w.Code)
	}
	if w := doReq(t, srv, http.MethodPut, "/flows/x", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("put without config path = %d", w.Code)
	}
	if w := doReq(t, srv, http.MethodDelete, "/flows/x", ``); w.Code != http.StatusServiceUnavailable {
		t.Errorf("delete without config path = %d", w.Code)
	}

	var got server.FlowSimulation
	srv.SetFlowFns(
		func() any { return map[string]any{"selected": "default"} },
		func(_ context.Context, req server.FlowSimulation) (any, error) {
			got = req
			if req.Flow == "ghost" {
				return nil, errors.New(`no flow "ghost"`)
			}
			return map[string]any{"candidates": []string{"codex"}}, nil
		},
	)
	srv.SetQuotasFn(func(context.Context) any { return []map[string]any{{"agent": "claude"}} })
	if w := doReq(t, srv, http.MethodGet, "/flows", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "default") {
		t.Errorf("flows = %d %s", w.Code, w.Body)
	}
	if w := doReq(t, srv, http.MethodGet, "/quotas", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "claude") {
		t.Errorf("quotas = %d %s", w.Code, w.Body)
	}
	w := doReq(t, srv, http.MethodPost, "/flows/simulate", `{"repo":"acme/api","at":"2026-10-08T10:00:00Z"}`)
	if w.Code != 200 || got.Repo != "acme/api" || got.At.IsZero() || !strings.Contains(w.Body.String(), "codex") {
		t.Errorf("simulate = %d %s %+v", w.Code, w.Body, got)
	}
	for body, code := range map[string]int{
		`{`:                 http.StatusBadRequest,
		`{"flow":"Bad Id"}`: http.StatusBadRequest,
		`{"repo":"../etc"}`: http.StatusBadRequest,
		`{"flow":"ghost"}`:  http.StatusNotFound,
	} {
		if w := doReq(t, srv, http.MethodPost, "/flows/simulate", body); w.Code != code {
			t.Errorf("simulate %s = %d, want %d", body, w.Code, code)
		}
	}
}

func TestFlowEndpointsRequireAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	for _, c := range [][2]string{{http.MethodGet, "/flows"}, {http.MethodGet, "/quotas"}, {http.MethodPost, "/flows/simulate"}, {http.MethodPut, "/flows/x"}, {http.MethodDelete, "/flows/x"}} {
		if w := doReq(t, srv, c[0], c[1], `{}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d", c[0], c[1], w.Code)
		}
	}
}

func TestPutFlowWriteFailure(t *testing.T) {
	srv, cfgPath := newPatchServer(t)
	if err := os.Chmod(cfgPath, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o600) })
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfgPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if w := doReq(t, srv, http.MethodPut, "/flows/x", `{"rules":{"1":{"agent":"claude"}}}`); w.Code != http.StatusInternalServerError {
		t.Errorf("unreadable config = %d %s", w.Code, w.Body)
	}
}
