package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/server"
)

const graphBody = `{"name":"Night","start":"a1","nodes":{
  "a1":{"type":"agent","agent":"claude","next":"a2","x":10,"y":20},
  "a2":{"type":"agent","agent":"codex"}}}`

func TestPutFlowStoresGraph(t *testing.T) {
	srv, path := newPatchServer(t)
	rec := doReq(t, srv, http.MethodPut, "/flows/night", graphBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := string(raw)
	for _, want := range []string{`start = "a1"`, `[ai.flows.night.nodes.a1]`, `next = "a2"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("config missing %q:\n%s", want, data)
		}
	}
}

func TestPutFlowRejectsLegacyRules(t *testing.T) {
	srv, _ := newPatchServer(t)
	rec := doReq(t, srv, http.MethodPut, "/flows/night", `{"name":"x","rules":{"10":{"agent":"claude"}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "node graphs") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestPutFlowRejectsEmptyGraph(t *testing.T) {
	srv, _ := newPatchServer(t)
	rec := doReq(t, srv, http.MethodPut, "/flows/night", `{"name":"x","start":"","nodes":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestPutFlowInvalidGraphIs422WithIssues(t *testing.T) {
	srv, _ := newPatchServer(t)
	body := `{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"a1"}}}`
	rec := doReq(t, srv, http.MethodPut, "/flows/night", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Errors []struct{ Node, Message string } `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Errors) == 0 || out.Errors[0].Node != "a1" {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestSimulateUnsavedGraphWithCycleIs422(t *testing.T) {
	srv, _ := newPatchServer(t)
	srv.SetFlowFns(func(context.Context) any { return map[string]any{} },
		func(context.Context, server.FlowSimulation) (any, error) {
			t.Fatal("must not evaluate an invalid graph")
			return nil, nil
		})
	body := `{"graph":{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"a2"},"a2":{"type":"agent","agent":"codex","next":"a1"}}}}`
	rec := doReq(t, srv, http.MethodPost, "/flows/simulate", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestSimulatePassesUnsavedGraph(t *testing.T) {
	srv, _ := newPatchServer(t)
	var got server.FlowSimulation
	srv.SetFlowFns(func(context.Context) any { return map[string]any{} },
		func(_ context.Context, req server.FlowSimulation) (any, error) {
			got = req
			return map[string]any{"ok": true}, nil
		})
	rec := doReq(t, srv, http.MethodPost, "/flows/simulate", `{"graph":`+graphBody+`}`)
	if rec.Code != http.StatusOK || got.Graph == nil || got.Graph.Start != "a1" {
		t.Fatalf("status %d graph %+v", rec.Code, got.Graph)
	}
}
