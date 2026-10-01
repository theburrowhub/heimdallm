package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestMergeTrackingEntry_DecodesTheMyPRsFields(t *testing.T) {
	var e api.MergeTrackingEntry
	if err := json.Unmarshal([]byte(`{"pr_id":1,"repo":"a/b","number":1,"phase":"blocked",
		"attention":"action","stale":true,"last_activity_at":"2026-09-26T09:00:00Z",
		"terminal_at":"2026-09-30T10:00:00Z"}`), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Attention != "action" || !e.Stale {
		t.Errorf("attention/stale not decoded: %+v", e)
	}
	if e.LastActivityAt == nil || !e.LastActivityAt.Equal(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("last_activity_at = %v", e.LastActivityAt)
	}
	if e.TerminalAt == nil {
		t.Error("terminal_at not decoded")
	}
	if !e.NeedsOperator() {
		t.Error("an action PR needs its owner")
	}
}

func TestMergeTrackingEntry_AttentionLabelAndNeedsOperator(t *testing.T) {
	cases := []struct {
		e     api.MergeTrackingEntry
		label string
		needs bool
	}{
		{api.MergeTrackingEntry{Phase: "blocked", Attention: "action"}, "needs you", true},
		{api.MergeTrackingEntry{Phase: "idle", Attention: "ready"}, "ready to merge", true},
		{api.MergeTrackingEntry{Phase: "blocked", Attention: "waiting", Stale: true}, "waiting, stale", true},
		{api.MergeTrackingEntry{Phase: "blocked", Attention: "waiting"}, "waiting", false},
		{api.MergeTrackingEntry{Phase: "blocked"}, "waiting", false},
		{api.MergeTrackingEntry{Phase: "blocked", Attention: "action", Excluded: true}, "excluded", false},
		{api.MergeTrackingEntry{Phase: "merged", Attention: "ready"}, "", false},
	}
	for _, c := range cases {
		if got := c.e.AttentionLabel(); got != c.label {
			t.Errorf("%+v label = %q, want %q", c.e, got, c.label)
		}
		if got := c.e.NeedsOperator(); got != c.needs {
			t.Errorf("%+v needs = %v, want %v", c.e, got, c.needs)
		}
	}
}

func TestSortMyPRs_GroupsLikeTheGUIAndKeepsDaemonOrderInside(t *testing.T) {
	entries := []api.MergeTrackingEntry{
		{Number: 1, Phase: "merged"},
		{Number: 2, Phase: "blocked", Attention: "waiting"},
		{Number: 3, Phase: "blocked", Attention: "action"},
		{Number: 4, Phase: "idle", Attention: "ready"},
		{Number: 5, Phase: "blocked", Attention: "action"},
		{Number: 6, Phase: "blocked", Attention: "action", Excluded: true},
	}
	api.SortMyPRs(entries)
	got := make([]int, len(entries))
	for i, e := range entries {
		got[i] = e.Number
	}
	want := []int{3, 5, 4, 2, 6, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
