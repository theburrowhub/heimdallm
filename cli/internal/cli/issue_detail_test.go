package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

const issueDetailTestToken = "issue-detail-token"

func TestIssueDetailCommandRendersCompleteIssue(t *testing.T) {
	server := newIssueDetailServer(t, 17, http.StatusOK, `{
  "issue": {
    "id": 17,
    "repo": "acme/platform",
    "number": 81,
    "title": "Handle offline startup",
    "body": "first body line\nsecond body line",
    "author": "carol",
    "assignees": ["dave", "erin"],
    "labels": ["bug", "help wanted"],
    "state": "open",
    "created_at": "2026-06-02T09:15:00Z",
    "dismissed": true
  },
  "reviews": [
    {
      "cli_used": "codex",
      "summary": "ready to implement\nwith safeguards",
      "triage": {"severity": "high", "owner": "platform", "score": 4},
      "suggestions": ["add a regression test", {"kind": "docs", "text": "update runbook"}],
      "action_taken": "auto_implement",
      "pr_created": 77,
      "created_at": "2026-06-03T14:45:00Z"
    }
  ]
}`)

	out, err := executeIssueDetailCommand(t, server.URL, "issue", "17")
	if err != nil {
		t.Fatalf("issue command returned an error: %v", err)
	}

	assertIssueOutputContains(t, out,
		"Issue #81 — Handle offline startup",
		"Repo:", "acme/platform",
		"Author:", "carol",
		"State:", "open",
		"Created:", "2026-06-02 09:15",
		"Labels:", "bug, help wanted",
		"Assignees:", "dave, erin",
		"Dismissed:", "yes",
		"Body", "first body line", "second body line",
		"══ Triage 1 ══",
		"Action:", "auto_implement",
		"PR Created:", "#77",
		"Date:", "2026-06-03 14:45",
		"CLI:", "codex",
		"Summary", "ready to implement", "with safeguards",
		"Classification",
		"severity:", "high",
		"owner:", "platform",
		"score:", "4",
		"Suggestions",
		"1. add a regression test",
		`"kind": "docs"`,
		`"text": "update runbook"`,
	)
}

func TestIssueDetailCommandReportsNoTriages(t *testing.T) {
	server := newIssueDetailServer(t, 9, http.StatusOK, `{
  "issue": {
    "id": 9,
    "repo": "acme/api",
    "number": 9,
    "title": "Clarify health response",
    "author": "bob",
    "state": "closed",
    "created_at": "2026-05-04T12:00:00Z"
  },
  "reviews": []
}`)

	out, err := executeIssueDetailCommand(t, server.URL, "issue", "9")
	if err != nil {
		t.Fatalf("issue command returned an error: %v", err)
	}

	assertIssueOutputContains(t, out,
		"Issue #9 — Clarify health response",
		"acme/api",
		"No triages yet.",
	)
	assertIssueOutputOmits(t, out, "══ Triage", "Classification", "Suggestions")
}

func TestIssueDetailCommandJSONOutput(t *testing.T) {
	server := newIssueDetailServer(t, 22, http.StatusOK, `{
  "issue": {
    "id": 22,
    "repo": "acme/web",
    "number": 105,
    "title": "Expose build version",
    "state": "open",
    "created_at": "2026-07-01T08:30:00Z"
  },
  "reviews": [
    {
      "action_taken": "review_only",
      "summary": "safe change",
      "created_at": "2026-07-01T09:00:00Z"
    }
  ]
}`)

	out, err := executeIssueDetailCommand(t, server.URL, "issue", "--json", "22")
	if err != nil {
		t.Fatalf("issue --json returned an error: %v", err)
	}

	var detail api.IssueDetail
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatalf("issue --json emitted invalid JSON: %v\noutput:\n%s", err, out)
	}
	if detail.Issue.ID != 22 || detail.Issue.Number != 105 || detail.Issue.Repo != "acme/web" {
		t.Fatalf("unexpected issue in JSON output: %+v", detail.Issue)
	}
	if len(detail.Reviews) != 1 || detail.Reviews[0].ActionTaken != "review_only" {
		t.Fatalf("unexpected reviews in JSON output: %+v", detail.Reviews)
	}
}

func TestIssueDetailCommandRejectsInvalidID(t *testing.T) {
	out, err := executeIssueDetailCommand(t, "http://127.0.0.1:1", "issue", "not-a-number")
	if err == nil {
		t.Fatal("issue command accepted an invalid ID")
	}
	if !strings.Contains(err.Error(), "invalid issue ID") || !strings.Contains(err.Error(), "invalid syntax") {
		t.Fatalf("invalid-ID error = %q", err)
	}
	if out != "" {
		t.Fatalf("invalid issue ID produced stdout: %q", out)
	}
}

func TestIssueDetailCommandReportsHTTPError(t *testing.T) {
	server := newIssueDetailServer(t, 31, http.StatusInternalServerError, "database unavailable")

	out, err := executeIssueDetailCommand(t, server.URL, "issue", "31")
	if err == nil {
		t.Fatal("issue command succeeded after an HTTP 500")
	}
	if !strings.Contains(err.Error(), "fetching issue") || !strings.Contains(err.Error(), "HTTP 500: database unavailable") {
		t.Fatalf("HTTP error = %q", err)
	}
	if out != "" {
		t.Fatalf("failed issue request produced stdout: %q", out)
	}
}

func TestFormatJSONStringArray(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "missing", raw: nil, want: ""},
		{name: "invalid JSON", raw: json.RawMessage(`{"broken"`), want: ""},
		{name: "wrong item type", raw: json.RawMessage(`["alice", 7]`), want: ""},
		{name: "empty array", raw: json.RawMessage(`[]`), want: ""},
		{name: "string array", raw: json.RawMessage(`["alice", "bob"]`), want: "alice, bob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatJSONStringArray(tt.raw); got != tt.want {
				t.Fatalf("formatJSONStringArray(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDetailPrintHelpersIgnoreEmptyAndInvalidJSON(t *testing.T) {
	tests := []struct {
		name  string
		print func()
	}{
		{name: "array empty string", print: func() { printJSONArray("Items", "") }},
		{name: "array empty JSON", print: func() { printJSONArray("Items", "[]") }},
		{name: "array null", print: func() { printJSONArray("Items", "null") }},
		{name: "array malformed", print: func() { printJSONArray("Items", "[") }},
		{name: "triage empty string", print: func() { printTriageMap("") }},
		{name: "triage empty object", print: func() { printTriageMap("{}") }},
		{name: "triage null", print: func() { printTriageMap("null") }},
		{name: "triage malformed", print: func() { printTriageMap("{") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := captureIssueStdout(t, func() error {
				tt.print()
				return nil
			})
			if err != nil {
				t.Fatalf("print helper returned an error: %v", err)
			}
			if out != "" {
				t.Fatalf("print helper produced output for empty/invalid input: %q", out)
			}
		})
	}
}

func newIssueDetailServer(t *testing.T, id int, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("request method = %s, want GET", r.Method)
		}
		if want := fmt.Sprintf("/issues/%d", id); r.URL.Path != want {
			t.Errorf("request path = %q, want %q", r.URL.Path, want)
		}
		if got := r.Header.Get("X-Heimdallm-Token"); got != issueDetailTestToken {
			t.Errorf("X-Heimdallm-Token = %q, want %q", got, issueDetailTestToken)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func executeIssueDetailCommand(t *testing.T, host string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")

	cmd := NewRootCmd("test")
	cmd.SetArgs(append([]string{"--host", host, "--token", issueDetailTestToken}, args...))
	return captureIssueStdout(t, cmd.Execute)
}

func captureIssueStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()

	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*.txt")
	if err != nil {
		t.Fatalf("creating stdout capture: %v", err)
	}

	original := os.Stdout
	os.Stdout = stdout
	defer func() {
		os.Stdout = original
	}()

	runErr := run()
	os.Stdout = original
	if err := stdout.Close(); err != nil {
		t.Fatalf("closing stdout capture: %v", err)
	}

	data, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatalf("reading stdout capture: %v", err)
	}
	return string(data), runErr
}

func assertIssueOutputContains(t *testing.T, output string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(output, fragment) {
			t.Errorf("output does not contain %q:\n%s", fragment, output)
		}
	}
}

func assertIssueOutputOmits(t *testing.T, output string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(output, fragment) {
			t.Errorf("output unexpectedly contains %q:\n%s", fragment, output)
		}
	}
}
