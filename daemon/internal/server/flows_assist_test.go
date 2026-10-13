package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
