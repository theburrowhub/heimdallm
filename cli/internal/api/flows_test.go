package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestFlowsClient(t *testing.T) {
	var seen []string
	var simBody map[string]any
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"no flow \"x\""}`))
			return
		}
		switch r.URL.Path {
		case "/flows":
			_, _ = w.Write([]byte(`{"selected":"weekday","default_id":"default","write_capable":["claude"],
				"flows":{"weekday":{"name":"Weekday","rules":{"20":{"agent":"codex"},"10":{"agent":"claude"}}}}}`))
		case "/flows/simulate":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content type = %q", r.Header.Get("Content-Type"))
			}
			b, _ := io.ReadAll(r.Body)
			simBody = nil
			_ = json.Unmarshal(b, &simBody)
			_, _ = w.Write([]byte(`{"flow_id":"weekday","candidates":["codex"],"rules":[{"key":"10","agent":"claude","reasons":["x"]}]}`))
		case "/quotas":
			_, _ = w.Write([]byte(`[{"agent":"claude","available":true,"windows":[{"kind":"session","used_percent":34}]}]`))
		}
	}))
	defer srv.Close()

	c := api.New(srv.URL, "tok")
	l, err := c.ListFlows()
	if err != nil || l.Selected != "weekday" {
		t.Fatalf("ListFlows = %+v, %v", l, err)
	}
	if got := l.Flows["weekday"].OrderedRuleKeys(); strings.Join(got, ",") != "10,20" {
		t.Errorf("rule order = %v", got)
	}
	at := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	d, err := c.SimulateFlow("", "org/a", at)
	if err != nil || d.Candidates[0] != "codex" {
		t.Fatalf("SimulateFlow = %+v, %v", d, err)
	}
	if simBody["repo"] != "org/a" || simBody["at"] != "2026-10-08T09:00:00Z" || simBody["flow"] != nil {
		t.Errorf("simulate body = %v", simBody)
	}
	if _, err := c.SimulateFlow("weekday", "", time.Time{}); err != nil || simBody["flow"] != "weekday" || simBody["at"] != nil {
		t.Errorf("simulate by flow: %v, body %v", err, simBody)
	}
	q, err := c.GetQuotas()
	if err != nil || q[0].Summary() != "5h 34%" {
		t.Fatalf("GetQuotas = %+v, %v", q, err)
	}
	want := "GET /flows,POST /flows/simulate,POST /flows/simulate,GET /quotas"
	if strings.Join(seen, ",") != want {
		t.Errorf("requests = %v", seen)
	}

	status = http.StatusNotFound
	if _, err := c.SimulateFlow("x", "", time.Time{}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("simulate error = %v", err)
	}
	if _, err := c.ListFlows(); err == nil {
		t.Error("ListFlows must fail on 404")
	}
	if _, err := c.GetQuotas(); err == nil {
		t.Error("GetQuotas must fail on 404")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{oops`))
	}))
	defer bad.Close()
	bc := api.New(bad.URL, "")
	if _, err := bc.ListFlows(); err == nil {
		t.Error("malformed flows must error")
	}
	if _, err := bc.SimulateFlow("a", "", time.Time{}); err == nil {
		t.Error("malformed decision must error")
	}
	if _, err := bc.GetQuotas(); err == nil {
		t.Error("malformed quotas must error")
	}
	if _, err := api.New("http://127.0.0.1:1", "").SimulateFlow("a", "", time.Time{}); err == nil {
		t.Error("unreachable daemon must error")
	}
}

func TestFlowDescriptions(t *testing.T) {
	r := api.FlowRule{
		Agent: "copilot",
		Match: "any",
		Schedule: map[string]api.ScheduleCondition{
			"s1": {Days: []string{"mon", "fri"}, From: "08:00", To: "15:00", TZ: "Europe/Madrid"},
			"s2": {From: "22:00", To: "06:00"},
		},
		Quota: map[string]api.QuotaCondition{
			"q1": {Agent: "claude", Window: "weekly", Op: "above", Percent: 90.5},
			"q2": {Agent: "claude", Window: "session", Op: "below", Percent: 50},
		},
	}
	want := "copilot when mon,fri 08:00-15:00 Europe/Madrid or every day 22:00-06:00 daemon time or claude weekly quota > 90.5% or claude session quota < 50%"
	if got := r.Describe(); got != want {
		t.Errorf("Describe =\n%q\nwant\n%q", got, want)
	}
	r.Match = ""
	if got := r.Describe(); !strings.Contains(got, " and ") {
		t.Errorf("all-match rule = %q", got)
	}
	unknown := api.FlowRule{Agent: "codex", Quota: map[string]api.QuotaCondition{
		"q": {Agent: "claude", Window: "weekly", Op: "near", Percent: 50},
	}}
	if got := unknown.Describe(); !strings.Contains(got, "quota near 50%") {
		t.Errorf("an op the CLI does not know must be shown as sent: %q", got)
	}
	unknown.Quota["q"] = api.QuotaCondition{Agent: "claude", Window: "weekly", Percent: 50}
	if got := unknown.Describe(); !strings.Contains(got, "quota < 50%") {
		t.Errorf("an empty op is below, as the daemon evaluates it: %q", got)
	}
	if got := (api.FlowRule{Agent: "codex\x1b[31m"}).Describe(); strings.Contains(got, "\x1b") || !strings.HasSuffix(got, " always") {
		t.Errorf("control bytes kept: %q", got)
	}
	keys := api.Flow{Rules: map[string]api.FlowRule{"b": {}, "a": {}, "100": {}, "9": {}, "010": {}, "10": {}}}.OrderedRuleKeys()
	if strings.Join(keys, ",") != "9,010,10,100,a,b" {
		t.Errorf("keys = %v", keys)
	}

	for _, tc := range []struct {
		q    api.AgentQuota
		want string
	}{
		{api.AgentQuota{Available: true}, "no limits"},
		{api.AgentQuota{}, "unknown"},
		{api.AgentQuota{Error: "expired"}, "unknown (expired)"},
		{api.AgentQuota{Available: true, Windows: []api.QuotaWindow{
			{Kind: "weekly", UsedPercent: 80}, {Kind: "monthly", UsedPercent: 1},
			{Kind: "model", Label: "gemini-2.5-pro", UsedPercent: 2}, {Kind: "credit", UsedPercent: 3},
		}}, "7d 80% · month 1% · gemini-2.5-pro 2% · credit 3%"},
	} {
		if got := tc.q.Summary(); got != tc.want {
			t.Errorf("Summary = %q, want %q", got, tc.want)
		}
	}
}
