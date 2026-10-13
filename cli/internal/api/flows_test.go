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
			_, _ = w.Write([]byte(`{"selected":"weekday","default_id":"default","agents":[{"id":"claude","installed":true,"write_capable":true}],
				"flows":{"weekday":{"name":"Weekday","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"a2"},"a2":{"type":"agent","agent":"codex"}}}}}`))
		case "/flows/simulate":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content type = %q", r.Header.Get("Content-Type"))
			}
			b, _ := io.ReadAll(r.Body)
			simBody = nil
			_ = json.Unmarshal(b, &simBody)
			_, _ = w.Write([]byte(`{"flow_id":"weekday","candidates":["codex"],"path":[{"node":"a1","type":"agent","agent":"claude","reasons":["x"]}]}`))
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
	if got := strings.Join(l.Flows["weekday"].Describe(), "|"); got != "a1: claude → fallback: a2|a2: codex → end" {
		t.Errorf("describe = %q", got)
	}
	if len(l.Agents) != 1 || l.Agents[0].ID != "claude" || !l.Agents[0].WriteCapable {
		t.Errorf("agents = %+v", l.Agents)
	}
	at := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	d, err := c.SimulateFlow("", "org/a", at)
	if err != nil || d.Candidates[0] != "codex" || len(d.Path) != 1 || d.Path[0].Node != "a1" {
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
	n := api.FlowNode{
		Type:  "condition",
		Match: "any",
		Yes:   "a1",
		Schedule: map[string]api.ScheduleCondition{
			"s1": {Days: []string{"mon", "fri"}, From: "08:00", To: "15:00", TZ: "Europe/Madrid"},
			"s2": {From: "22:00", To: "06:00"},
			"s3": {Days: []string{"sat", "sun"}, From: "00:00", To: "00:00"},
			"s4": {From: "07:00", To: "07:00"},
		},
		Quota: map[string]api.QuotaCondition{
			"q1": {Agent: "claude", Window: "weekly", Op: "above", Percent: 90.5},
			"q2": {Agent: "claude", Window: "session", Op: "below", Percent: 50},
		},
	}
	want := "if mon,fri 08:00-15:00 Europe/Madrid or every day 22:00-06:00 daemon time or sat,sun all day or always or claude weekly quota > 90.5% or claude session quota < 50% → yes: a1 / no: end"
	if got := n.Describe(); got != want {
		t.Errorf("Describe =\n%q\nwant\n%q", got, want)
	}
	n.Match = ""
	if got := n.Describe(); !strings.Contains(got, " and ") {
		t.Errorf("all-match node = %q", got)
	}
	unknown := api.FlowNode{Type: "condition", Quota: map[string]api.QuotaCondition{
		"q": {Agent: "claude", Window: "weekly", Op: "near", Percent: 50},
	}}
	if got := unknown.Describe(); !strings.Contains(got, "quota near 50%") {
		t.Errorf("an op the CLI does not know must be shown as sent: %q", got)
	}
	unknown.Quota["q"] = api.QuotaCondition{Agent: "claude", Window: "weekly", Percent: 50}
	if got := unknown.Describe(); !strings.Contains(got, "quota < 50%") {
		t.Errorf("an empty op is below, as the daemon evaluates it: %q", got)
	}
	if got := (api.FlowNode{Type: "agent", Agent: "codex\x1b[31m"}).Describe(); strings.Contains(got, "\x1b") || !strings.HasSuffix(got, " → end") {
		t.Errorf("control bytes kept: %q", got)
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

func TestFlowDescribeWalksGraph(t *testing.T) {
	f := api.Flow{Name: "n", Start: "c1", Nodes: map[string]api.FlowNode{
		"c1": {Type: "condition", Match: "any", Yes: "a1", No: "a2",
			Schedule: map[string]api.ScheduleCondition{"s1": {From: "22:00", To: "06:00"}}},
		"a1": {Type: "agent", Agent: "codex", Next: "a2"},
		"a2": {Type: "agent", Agent: "claude"},
		"x9": {Type: "agent", Agent: "gemini"},
	}}
	got := strings.Join(f.Describe(), "\n")
	want := "c1: if every day 22:00-06:00 daemon time → yes: a1 / no: a2\n" +
		"a1: codex → fallback: a2\n" +
		"a2: claude → end"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// A cycle or a dangling edge must not loop or panic.
	loop := api.Flow{Start: "a", Nodes: map[string]api.FlowNode{"a": {Type: "agent", Agent: "x", Next: "a"}, "b": {Type: "agent", Next: "zz"}}}
	if got := loop.Describe(); len(got) != 1 {
		t.Errorf("cycle = %v", got)
	}
}

func TestAssistFlowPostsAndDecodes(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flows/assist" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"plan":"p","agent":"claude","duration_ms":1500,"warnings":["w"],"flow":{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude"}}}}`))
	}))
	defer ts.Close()
	c := api.New(ts.URL, "tok")
	res, err := c.AssistFlow("claude", "use claude", &api.Flow{Name: "cur"})
	if err != nil || res.Plan != "p" || res.Flow.Start != "a1" || res.DurationMS != 1500 || len(res.Warnings) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if body["agent"] != "claude" || body["instruction"] != "use claude" || body["flow"] == nil {
		t.Fatalf("body %v", body)
	}
	if c.HTTPClient.Timeout != 30*time.Second {
		t.Errorf("AssistFlow must not change the shared client timeout, got %v", c.HTTPClient.Timeout)
	}
	body = nil
	if _, err := c.AssistFlow("claude", "x", nil); err != nil || body["flow"] != nil {
		t.Errorf("no current flow: %v body %v", err, body)
	}

	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"agent timed out"}`, http.StatusGatewayTimeout)
	}))
	defer fail.Close()
	if _, err := api.New(fail.URL, "").AssistFlow("claude", "x", nil); err == nil || !strings.Contains(err.Error(), "504") {
		t.Errorf("assist error = %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{oops`)) }))
	defer bad.Close()
	if _, err := api.New(bad.URL, "").AssistFlow("claude", "x", nil); err == nil {
		t.Error("malformed assist result must error")
	}
}

func TestPutFlow(t *testing.T) {
	var path, method string
	var body api.Flow
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method = r.URL.EscapedPath(), r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	err := api.New(ts.URL, "").PutFlow("a b", api.Flow{Name: "N", Start: "a1", Nodes: map[string]api.FlowNode{"a1": {Type: "agent", Agent: "claude"}}})
	if err != nil || method != http.MethodPut || path != "/flows/a%20b" || body.Start != "a1" {
		t.Fatalf("err=%v %s %s %+v", err, method, path, body)
	}
}
