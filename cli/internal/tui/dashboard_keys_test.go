package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestEnterOpensDetailOnlyOnThePRsTab(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.prs = []api.PR{{ID: 1, Repo: "org/a", Number: 7, LatestReview: &api.Review{}}}
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	d.activeTab = tabActivity
	d.handleKey(enter)
	if d.showDetail {
		t.Fatal("enter outside the PRs tab must not open a detail view")
	}

	d.activeTab = tabPRs
	d.handleKey(enter)
	if !d.showDetail {
		t.Fatal("enter on a PR row must open its detail view")
	}
}

func TestEnterWithCursorPastTheListDoesNothing(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.activeTab = tabPRs
	d.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if d.showDetail {
		t.Fatal("enter on an empty PR list must not open a detail view")
	}
}

func TestRenderHelpPerTab(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	cases := []struct {
		tab     tab
		detail  bool
		want    string
		notWant string
	}{
		{tabPRs, false, "[enter]detail  [o]pen  [f]ilter repo", ""},
		{tabActivity, false, "[G]follow", "[enter]detail"},
		{tabConfig, false, "[1-7]jump", "[enter]detail"},
		{tabPRs, true, "[o]pen in browser", ""},
		{tabConfig, true, "[esc]close  [j/k]scroll", "[o]pen"},
	}
	for _, tc := range cases {
		d.activeTab = tc.tab
		d.showDetail = tc.detail
		got := d.renderHelp()
		if !strings.Contains(got, tc.want) {
			t.Errorf("tab %v detail=%v: help %q missing %q", tc.tab, tc.detail, got, tc.want)
		}
		if tc.notWant != "" && strings.Contains(got, tc.notWant) {
			t.Errorf("tab %v detail=%v: help %q must not contain %q", tc.tab, tc.detail, got, tc.notWant)
		}
		if strings.Contains(got, "issue") || strings.Contains(got, "[p]romote") {
			t.Errorf("tab %v: help still mentions issues: %q", tc.tab, got)
		}
	}
}
