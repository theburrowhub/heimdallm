package flows

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/quota"
)

type fakeQuotas map[string]quota.Provider

func (f fakeQuotas) Get(_ context.Context, agent string) quota.Provider {
	if p, ok := f[agent]; ok {
		return p
	}
	return quota.Provider{Agent: agent, Error: quota.ErrNotConfigured}
}

func provider(windows ...quota.Window) quota.Provider {
	return quota.Provider{Available: true, Windows: windows}
}

// The example from the feature request: Claude while it has quota, Copilot
// on weekday mornings, Codex otherwise.
func exampleFlow() config.FlowConfig {
	return config.FlowConfig{Name: "Example", Rules: map[string]config.FlowRule{
		"10": {Agent: "claude", Quota: map[string]config.QuotaCondition{
			"a": {Agent: "claude", Window: "session", Op: "below", Percent: 50},
			"b": {Agent: "claude", Window: "weekly", Op: "below", Percent: 50},
		}},
		"20": {Agent: "copilot", Schedule: map[string]config.ScheduleCondition{
			"a": {Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: "08:00", To: "15:00", TZ: "Europe/Madrid"},
		}},
		"99": {Agent: "codex"},
	}}
}

func madrid(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestEvaluate_RequestExample(t *testing.T) {
	all := func(string) bool { return true }
	low := fakeQuotas{"claude": provider(quota.Window{Kind: "session", UsedPercent: 20}, quota.Window{Kind: "weekly", UsedPercent: 30})}
	high := fakeQuotas{"claude": provider(quota.Window{Kind: "session", UsedPercent: 70}, quota.Window{Kind: "weekly", UsedPercent: 30})}
	thursday10 := madrid(t, "2026-10-08 10:00")
	saturday10 := madrid(t, "2026-10-10 10:00")

	cases := []struct {
		name string
		at   time.Time
		q    fakeQuotas
		want string
	}{
		{"claude has quota on a weekday morning", thursday10, low, "claude,copilot,codex"},
		{"claude spent on a weekday morning", thursday10, high, "copilot,codex"},
		{"claude spent on Saturday", saturday10, high, "codex"},
		{"claude quota unknown fails closed", saturday10, fakeQuotas{}, "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Evaluate(context.Background(), "example", exampleFlow(), tc.at, tc.q, all)
			if got := strings.Join(d.Candidates, ","); got != tc.want {
				t.Errorf("candidates = %s, want %s\n%+v", got, tc.want, d.Rules)
			}
			if d.FlowID != "example" || d.FlowName != "Example" || len(d.Rules) != 3 || d.Rules[2].Key != "99" {
				t.Errorf("decision = %+v", d)
			}
		})
	}
}

func TestEvaluate_AvailabilityAnyAndDedup(t *testing.T) {
	f := config.FlowConfig{Rules: map[string]config.FlowRule{
		"1":  {Agent: "claude", Match: "any", Quota: map[string]config.QuotaCondition{"a": {Agent: "claude", Window: "session", Op: "above", Percent: 90}}, Schedule: map[string]config.ScheduleCondition{"a": {From: "00:00", To: "23:59"}}},
		"2":  {Agent: "copilot"},
		"10": {Agent: "claude"},
	}}
	d := Evaluate(context.Background(), "x", f, time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local), fakeQuotas{}, func(a string) bool { return a != "copilot" })
	if strings.Join(d.Candidates, ",") != "claude" {
		t.Errorf("candidates = %v", d.Candidates)
	}
	if !d.Rules[1].Matched || d.Rules[1].Available || !strings.Contains(strings.Join(d.Rules[1].Reasons, ";"), "not installed") {
		t.Errorf("unavailable rule = %+v", d.Rules[1])
	}
	if d := Evaluate(context.Background(), "x", config.FlowConfig{Rules: map[string]config.FlowRule{"1": {Agent: "claude", Match: "any", Schedule: map[string]config.ScheduleCondition{"a": {From: "01:00", To: "02:00"}}}}}, time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local), nil, nil); len(d.Candidates) != 0 {
		t.Errorf("any with no holding condition = %v", d.Candidates)
	}
}

func TestScheduleAcrossMidnight(t *testing.T) {
	night := config.ScheduleCondition{Days: []string{"fri"}, From: "22:00", To: "06:00", TZ: "UTC"}
	cases := map[string]bool{
		"2026-10-09T23:00:00Z": true,  // Friday night
		"2026-10-10T03:00:00Z": true,  // Saturday early: the window started Friday
		"2026-10-10T07:00:00Z": false, // after the window
		"2026-10-10T23:00:00Z": false, // Saturday night: not a Friday window
		"2026-10-09T03:00:00Z": false, // Friday early belongs to Thursday's window
	}
	for at, want := range cases {
		ts, _ := time.Parse(time.RFC3339, at)
		if got, why := scheduleHolds(night, ts); got != want {
			t.Errorf("%s: holds=%v want %v (%s)", at, got, want, why)
		}
	}
	// No days = every day; bad tz falls back to local without panicking.
	if ok, _ := scheduleHolds(config.ScheduleCondition{From: "00:00", To: "23:59", TZ: "Nowhere/Bad"}, time.Now()); !ok && time.Now().Format("15:04") != "23:59" {
		t.Error("an every-day window must hold")
	}
}

func TestQuotaConditions(t *testing.T) {
	q := fakeQuotas{
		"gemini":     provider(quota.Window{Kind: "model", Label: "gemini-2.5-pro", UsedPercent: 80}, quota.Window{Kind: "model", Label: "gemini-2.5-flash", UsedPercent: 10}),
		"openrouter": provider(quota.Window{Kind: "credit", UsedPercent: 30}),
		"copilot":    {Agent: "copilot", Error: quota.ErrExpired},
	}
	cases := []struct {
		c    config.QuotaCondition
		want bool
		why  string
	}{
		{config.QuotaCondition{Agent: "gemini", Window: "model:gemini-2.5-pro", Op: "above", Percent: 50}, true, "used 80%"},
		{config.QuotaCondition{Agent: "gemini", Window: "model:gemini-2.5-flash", Op: "above", Percent: 50}, false, "does not hold"},
		{config.QuotaCondition{Agent: "gemini", Window: "any", Op: "below", Percent: 50}, false, "used 80%"},
		{config.QuotaCondition{Agent: "openrouter", Window: "credit", Op: "below", Percent: 50}, true, "holds"},
		{config.QuotaCondition{Agent: "openrouter", Window: "weekly", Op: "below", Percent: 50}, false, "no weekly window"},
		{config.QuotaCondition{Agent: "copilot", Window: "monthly", Op: "below", Percent: 50}, false, "quota unknown (expired)"},
		{config.QuotaCondition{Agent: "claude", Window: "session", Op: "below", Percent: 50}, false, "not_configured"},
	}
	for _, tc := range cases {
		got, why := quotaHolds(context.Background(), tc.c, q)
		if got != tc.want || !strings.Contains(why, tc.why) {
			t.Errorf("%+v = %v (%s), want %v containing %q", tc.c, got, why, tc.want, tc.why)
		}
	}
	if ok, why := quotaHolds(context.Background(), config.QuotaCondition{Agent: "x"}, nil); ok || !strings.Contains(why, "unknown") {
		t.Errorf("nil reader = %v %s", ok, why)
	}
	unavailable := fakeQuotas{"x": {Agent: "x"}}
	if ok, why := quotaHolds(context.Background(), config.QuotaCondition{Agent: "x", Window: "session"}, unavailable); ok || !strings.Contains(why, "unavailable") {
		t.Errorf("unavailable without error = %v %s", ok, why)
	}
}
