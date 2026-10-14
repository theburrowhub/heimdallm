package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/flows"
)

func TestAssistHandlerMapsErrors(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{flows.ErrAssistBusy, http.StatusConflict},
		{&flows.AssistError{Status: http.StatusUnprocessableEntity, Message: "bad", Issues: []config.FlowIssue{{Node: "a1", Message: "x"}}}, http.StatusUnprocessableEntity},
		{&flows.AssistError{Status: http.StatusGatewayTimeout, Message: "slow"}, http.StatusGatewayTimeout},
	}
	for _, tc := range cases {
		srv, _ := newPatchServer(t)
		srv.SetFlowAssist(func(context.Context, flows.AssistRequest) (flows.AssistResult, error) {
			return flows.AssistResult{}, tc.err
		})
		rec := doReq(t, srv, http.MethodPost, "/flows/assist", `{"agent":"claude","instruction":"x"}`)
		if rec.Code != tc.code {
			t.Fatalf("%v -> %d (%s)", tc.err, rec.Code, rec.Body)
		}
	}
}

func TestAssistHandlerOK(t *testing.T) {
	srv, _ := newPatchServer(t)
	srv.SetFlowAssist(func(_ context.Context, req flows.AssistRequest) (flows.AssistResult, error) {
		return flows.AssistResult{Plan: "p:" + req.Instruction, Agent: req.Agent}, nil
	})
	rec := doReq(t, srv, http.MethodPost, "/flows/assist", `{"agent":"claude","instruction":"night codex"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "p:night codex") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAssistHandlerUnwired(t *testing.T) {
	srv, _ := newPatchServer(t)
	rec := doReq(t, srv, http.MethodPost, "/flows/assist", `{"agent":"claude","instruction":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", rec.Code)
	}
}

// A draft takes far longer than the daemon's 60 s WriteTimeout; the handler
// must lift the deadline for its own response (scaled down here).
func TestAssistHandlerOutlivesServerWriteTimeout(t *testing.T) {
	srv, _ := newPatchServer(t)
	srv.SetFlowAssist(func(context.Context, flows.AssistRequest) (flows.AssistResult, error) {
		time.Sleep(600 * time.Millisecond)
		return flows.AssistResult{Plan: "slow but delivered"}, nil
	})
	ts := httptest.NewUnstartedServer(srv.Router())
	ts.Config.WriteTimeout = 200 * time.Millisecond
	ts.Start()
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/flows/assist", "application/json", strings.NewReader(`{"agent":"claude","instruction":"x"}`))
	if err != nil {
		t.Fatalf("response lost to the write timeout: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "slow but delivered") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}
