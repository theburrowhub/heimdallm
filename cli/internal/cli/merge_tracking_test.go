package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// capture redirects stdout for the duration of fn and returns what was written.
// The print helpers write straight to stdout, which is the behaviour under test.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func entry(mut func(*api.MergeTrackingEntry)) api.MergeTrackingEntry {
	e := api.MergeTrackingEntry{
		PRID: 1, Repo: "acme/widgets", Number: 7,
		Title: "Add widget cache", URL: "https://github.com/acme/widgets/pull/7",
		Phase: "blocked", HeadRef: "feature", BaseRef: "main",
	}
	if mut != nil {
		mut(&e)
	}
	return e
}

// The marker is what makes a CI problem visible while scanning a list.
func TestPrintMergeTable_MarksCheckProblems(t *testing.T) {
	out := capture(t, func() {
		printMergeTable([]api.MergeTrackingEntry{
			entry(func(e *api.MergeTrackingEntry) {
				e.ChecksRequiredFailing = 1
				e.BlockReason = "checks_failing"
				e.BlockDetail = "1 required check is failing: build (GitHub Actions)"
			}),
			entry(func(e *api.MergeTrackingEntry) {
				e.Number = 8
				e.ChecksRequiredPending = 2
				e.BlockReason = "checks_pending"
			}),
			entry(func(e *api.MergeTrackingEntry) {
				e.Number = 9
				e.Phase = "idle"
			}),
		})
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected a header and three rows:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "! ") {
		t.Errorf("a failing check should be marked with !: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "~ ") {
		t.Errorf("a pending check should be marked with ~: %q", lines[2])
	}
	if strings.HasPrefix(lines[3], "!") || strings.HasPrefix(lines[3], "~") {
		t.Errorf("a clean row should carry no marker: %q", lines[3])
	}
	// The detail names the check; that is the whole value of the line.
	if !strings.Contains(out, "build (GitHub Actions)") {
		t.Errorf("the detail should be printed in full:\n%s", out)
	}
	if !strings.Contains(out, "acme/widgets#7") {
		t.Errorf("the row should identify the PR:\n%s", out)
	}
	// The legend explains the markers.
	if !strings.Contains(out, "required check failing") {
		t.Errorf("the legend should explain the markers:\n%s", out)
	}
}

// A reason code is an internal identifier; the table renders it as a sentence.
func TestPrintMergeTable_RendersAReasonWithNoDetail(t *testing.T) {
	out := capture(t, func() {
		printMergeTable([]api.MergeTrackingEntry{
			entry(func(e *api.MergeTrackingEntry) { e.BlockReason = "unresolved_threads" }),
		})
	})
	if !strings.Contains(out, "unresolved review conversations") {
		t.Errorf("the reason should render as a sentence:\n%s", out)
	}
	if strings.Contains(out, "unresolved_threads") {
		t.Errorf("the raw identifier must not be shown:\n%s", out)
	}
}

func TestPrintMergeTable_TerminalRowsShowNoBlocker(t *testing.T) {
	out := capture(t, func() {
		printMergeTable([]api.MergeTrackingEntry{
			entry(func(e *api.MergeTrackingEntry) {
				e.Phase = "merged"
				e.BlockReason = "already_merged"
			}),
		})
	})
	if strings.Contains(out, "Already merged") || strings.Contains(out, "already merged") {
		t.Errorf("a merged PR needs no blocker column:\n%s", out)
	}
	if !strings.Contains(out, "merged") {
		t.Errorf("the phase should still be shown:\n%s", out)
	}
}

func TestPrintMergeTable_RendersThePhaseAsAPhrase(t *testing.T) {
	out := capture(t, func() {
		printMergeTable([]api.MergeTrackingEntry{
			entry(func(e *api.MergeTrackingEntry) { e.Phase = "auto_merge_armed" }),
			entry(func(e *api.MergeTrackingEntry) { e.Number = 8; e.Phase = "abandoned" }),
		})
	})
	if !strings.Contains(out, "auto-merge on") {
		t.Errorf("auto_merge_armed should render as a phrase:\n%s", out)
	}
	if !strings.Contains(out, "not tracked") {
		t.Errorf("abandoned should render as a phrase:\n%s", out)
	}
}

func TestPrintMergeDetail_ShowsTheCheckBreakdown(t *testing.T) {
	e := entry(func(e *api.MergeTrackingEntry) {
		e.BlockReason = "checks_failing"
		e.BlockDetail = "1 required check is failing: build (GitHub Actions)"
		e.PreRebaseSHA = "abc123def"
		e.LastError = "connection reset"
		e.Decision = &api.MergeDecision{
			ChecksSummary: api.MergeChecksSummary{
				Total: 3, RequiredTotal: 2, RequiredFailing: 1, RequiredSuccess: 1,
			},
			Checks: []api.MergeCheck{
				{Name: "build", State: "failure", Required: true, App: "GitHub Actions", URL: "https://ci/build"},
				{Name: "lint", State: "success", Required: true},
				{Name: "coverage", State: "pending", Required: false},
			},
		}
	})
	out := capture(t, func() { printMergeDetail(&e) })

	for _, want := range []string{
		"acme/widgets#7", "Add widget cache",
		"feature", "main",
		"1 required check is failing: build (GitHub Actions)",
		"This PR cannot be merged: 1 of the 2 required checks is failing.",
		"build", "lint", "coverage",
		"https://ci/build",
		// The pre-rebase SHA is the recovery path after a force-push.
		"abc123def",
		"connection reset",
		"* required",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail should contain %q:\n%s", want, out)
		}
	}
	// Required checks are marked, optional ones are not.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "coverage") && strings.Contains(line, "*") {
			t.Errorf("an optional check must not be marked required: %q", line)
		}
	}
}

func TestPrintMergeDetail_UnevaluatedPRSaysSo(t *testing.T) {
	e := entry(nil)
	out := capture(t, func() { printMergeDetail(&e) })
	if !strings.Contains(out, "not evaluated") {
		t.Errorf("an unevaluated PR should say so:\n%s", out)
	}
}

func TestPrintMergeDetail_NoChecksStopsAfterTheHeadline(t *testing.T) {
	e := entry(func(e *api.MergeTrackingEntry) {
		e.Phase = "idle"
		e.BlockReason = ""
		e.Decision = &api.MergeDecision{}
	})
	out := capture(t, func() { printMergeDetail(&e) })
	if !strings.Contains(out, "no checks configured") {
		t.Errorf("a PR with no checks should say so:\n%s", out)
	}
	if strings.Contains(out, "* required") {
		t.Errorf("the legend belongs with a table, and there is none:\n%s", out)
	}
}

func TestMergeChecksHeadline_CoversEveryShape(t *testing.T) {
	cases := []struct {
		name    string
		summary api.MergeChecksSummary
		want    string
	}{
		{"truncated", api.MergeChecksSummary{Truncated: true}, "cannot be confirmed"},
		{"missing", api.MergeChecksSummary{MissingRequired: []string{"e2e", "smoke"}}, "e2e, smoke"},
		{"one failing", api.MergeChecksSummary{RequiredTotal: 2, RequiredFailing: 1}, "is failing"},
		{"two failing", api.MergeChecksSummary{RequiredTotal: 3, RequiredFailing: 2}, "are failing"},
		{"one pending", api.MergeChecksSummary{RequiredPending: 1}, "1 required check."},
		{"two pending", api.MergeChecksSummary{RequiredPending: 2}, "2 required checks."},
		{"optional red", api.MergeChecksSummary{RequiredTotal: 1, RequiredSuccess: 1, OptionalFailing: 1}, "does not block"},
		{"optional reds", api.MergeChecksSummary{RequiredTotal: 1, RequiredSuccess: 1, OptionalFailing: 2}, "checks are failing"},
		{"none", api.MergeChecksSummary{}, "no checks configured"},
		{"all green", api.MergeChecksSummary{Total: 5, RequiredTotal: 5, RequiredSuccess: 5}, "All 5 checks passed."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeChecksHeadline(tc.summary); !strings.Contains(got, tc.want) {
				t.Errorf("headline = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestCheckStateGlyph(t *testing.T) {
	for state, want := range map[string]string{
		"success": "✓", "failure": "✕", "pending": "…",
		"neutral": "–", "": "–", "surprise": "–",
	} {
		if got := checkStateGlyph(state); got != want {
			t.Errorf("glyph(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestHumanMergeReason_RendersEveryKnownCodeAsProse(t *testing.T) {
	// Every reason the daemon can persist must have a sentence, or the CLI
	// prints an internal identifier at a user.
	known := []string{
		"draft", "conflicts", "behind_base", "changes_requested", "review_required",
		"insufficient_approvals", "pending_reviewers", "unresolved_threads",
		"checks_failing", "checks_pending", "required_check_missing",
		"mergeability_unknown", "blocked_by_protection", "in_merge_queue",
		"merge_queue_configured", "cross_fork", "insufficient_permission",
		"merge_method_not_allowed", "automerge_waiting", "head_sha_moved",
		"cooldown", "attempt_cap_reached", "excluded", "disabled",
	}
	for _, reason := range known {
		got := humanMergeReason(reason)
		if got == "" {
			t.Errorf("%q has no sentence", reason)
		}
		if got == reason {
			t.Errorf("%q rendered as its own identifier", reason)
		}
	}
	if humanMergeReason("") != "" {
		t.Error("an empty reason renders as empty")
	}
	// An unknown code still has to read as words rather than as a snake_case
	// identifier.
	if got := humanMergeReason("some_future_reason"); got != "some future reason" {
		t.Errorf("unknown reason = %q, want it de-underscored", got)
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "check", "checks"); got != "check" {
		t.Errorf("got %q", got)
	}
	if got := plural(2, "check", "checks"); got != "checks" {
		t.Errorf("got %q", got)
	}
	if got := plural(0, "check", "checks"); got != "checks" {
		t.Errorf("got %q", got)
	}
}

// ── Commands ───────────────────────────────────────────────────────────────

func newMergeTestServer(t *testing.T, entries []api.MergeTrackingEntry, detail *api.MergeTrackingEntry) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/merge-tracking":
			_ = json.NewEncoder(w).Encode(entries)
		case strings.HasPrefix(r.URL.Path, "/merge-tracking/"):
			_ = json.NewEncoder(w).Encode(detail)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runCmd(t *testing.T, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd("test")
	root.SetArgs(append([]string{"--host", srv.URL}, args...))
	var err error
	out := capture(t, func() { err = root.Execute() })
	return out, err
}

func TestMergesCmd_ListsAndFilters(t *testing.T) {
	entries := []api.MergeTrackingEntry{
		entry(func(e *api.MergeTrackingEntry) {
			e.BlockReason = "checks_failing"
			e.ChecksRequiredFailing = 1
		}),
		entry(func(e *api.MergeTrackingEntry) {
			e.Number = 8
			e.Repo = "other/repo"
			e.Phase = "merged"
		}),
	}
	srv := newMergeTestServer(t, entries, nil)

	out, err := runCmd(t, srv, "merges")
	if err != nil {
		t.Fatalf("merges: %v", err)
	}
	if !strings.Contains(out, "acme/widgets#7") || !strings.Contains(out, "other/repo#8") {
		t.Errorf("both rows should be listed:\n%s", out)
	}

	out, err = runCmd(t, srv, "merges", "--repo", "acme/widgets")
	if err != nil {
		t.Fatalf("merges --repo: %v", err)
	}
	if strings.Contains(out, "other/repo") {
		t.Errorf("--repo should have filtered the other repo out:\n%s", out)
	}

	out, err = runCmd(t, srv, "merges", "--blocked")
	if err != nil {
		t.Fatalf("merges --blocked: %v", err)
	}
	if strings.Contains(out, "other/repo") {
		t.Errorf("--blocked should exclude the merged PR:\n%s", out)
	}
}

func TestMergesCmd_JSONOutputIsMachineReadable(t *testing.T) {
	srv := newMergeTestServer(t, []api.MergeTrackingEntry{entry(nil)}, nil)
	out, err := runCmd(t, srv, "merges", "--json")
	if err != nil {
		t.Fatalf("merges --json: %v", err)
	}
	var decoded []api.MergeTrackingEntry
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(decoded) != 1 || decoded[0].Repo != "acme/widgets" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestMergesCmd_EmptyListSaysSo(t *testing.T) {
	srv := newMergeTestServer(t, nil, nil)
	out, err := runCmd(t, srv, "merges")
	if err != nil {
		t.Fatalf("merges: %v", err)
	}
	if !strings.Contains(out, "No pull requests tracked") {
		t.Errorf("an empty list should say so:\n%s", out)
	}
}

func TestMergeCmd_ShowsTheDetail(t *testing.T) {
	detail := entry(func(e *api.MergeTrackingEntry) {
		e.Decision = &api.MergeDecision{
			ChecksSummary: api.MergeChecksSummary{Total: 1, RequiredTotal: 1, RequiredFailing: 1},
			Checks:        []api.MergeCheck{{Name: "build", State: "failure", Required: true}},
		}
	})
	srv := newMergeTestServer(t, nil, &detail)

	out, err := runCmd(t, srv, "merge", "1")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !strings.Contains(out, "build") || !strings.Contains(out, "cannot be merged") {
		t.Errorf("detail output incomplete:\n%s", out)
	}
}

func TestMergeCmd_RejectsANonNumericID(t *testing.T) {
	srv := newMergeTestServer(t, nil, nil)
	if _, err := runCmd(t, srv, "merge", "not-a-number"); err == nil {
		t.Fatal("a non-numeric PR id must be rejected")
	}
}

func TestMergeCmd_SurfacesFetchFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := runCmd(t, srv, "merges"); err == nil {
		t.Error("a failed listing must be reported")
	}
	if _, err := runCmd(t, srv, "merge", "1"); err == nil {
		t.Error("a failed detail fetch must be reported")
	}
}

// Regressions from the second review of PR #738.

// A repo with no required checks and a red optional one used to fall through
// to the default case and announce "All N checks passed" over a failure.
func TestMergeChecksHeadline_OptionalFailureWithNoRequiredChecks(t *testing.T) {
	got := mergeChecksHeadline(api.MergeChecksSummary{
		Total: 2, RequiredTotal: 0, OptionalFailing: 1,
	})
	if strings.Contains(got, "passed") {
		t.Errorf("headline = %q, must not claim everything passed", got)
	}
	if !strings.Contains(got, "optional") {
		t.Errorf("headline = %q, want the optional failure named", got)
	}
}

// The check-related reasons all get a sentence; leaving one to the underscore
// fallback reads as an identifier next to its three siblings.
func TestHumanMergeReason_CoversEveryCheckReason(t *testing.T) {
	for _, reason := range []string{
		"checks_failing", "checks_pending", "required_check_missing",
		"checks_unknown", "threads_unknown",
	} {
		got := humanMergeReason(reason)
		if got == "" || strings.Contains(got, "_") {
			t.Errorf("humanMergeReason(%q) = %q, want a sentence", reason, got)
		}
	}
}
