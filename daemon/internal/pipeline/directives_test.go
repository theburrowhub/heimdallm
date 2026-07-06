package pipeline

import (
	"fmt"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

func TestParseDirective(t *testing.T) {
	const bot = "heimdallm"
	cases := []struct {
		name                         string
		body                         string
		wantOK                       bool
		wantVerb, wantScope, wantPay string
	}{
		{"remember", "@heimdallm remember: unauth endpoints are fine", true, "remember", "repo", "unauth endpoints are fine"},
		{"remember scoped", "@heimdallm remember(repo): rule X", true, "remember", "repo", "rule X"},
		{"forget", "@heimdallm forget: 12", true, "forget", "repo", "12"},
		{"list", "@heimdallm list", true, "list", "repo", ""},
		{"case-insensitive mention+verb", "@HeimdallM REMEMBER: y", true, "remember", "repo", "y"},
		{"leading whitespace", "   @heimdallm list", true, "list", "repo", ""},
		{"not a directive", "looks good to me", false, "", "", ""},
		{"mention without verb", "@heimdallm hello there", false, "", "", ""},
		{"remember without payload", "@heimdallm remember:", false, "", "", ""},
		{"unknown verb", "@heimdallm frobnicate: x", false, "", "", ""},
		{"remember scoped non-default", "@heimdallm remember(global): rule X", true, "remember", "global", "rule X"},
		{"list scoped", "@heimdallm list(global)", true, "list", "global", ""},
		{"no word boundary", "@heimdallmremember: x", false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, scope, payload, ok := parseDirective(tc.body, bot)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if ok && (verb != tc.wantVerb || scope != tc.wantScope || payload != tc.wantPay) {
				t.Fatalf("got verb=%q scope=%q payload=%q; want verb=%q scope=%q payload=%q",
					verb, scope, payload, tc.wantVerb, tc.wantScope, tc.wantPay)
			}
		})
	}
}

func TestAuthorAllowed(t *testing.T) {
	allow := []string{"Alice", "@bob"}
	if !authorAllowed(allow, "alice") || !authorAllowed(allow, "@BOB") {
		t.Error("alice/bob should be allowed")
	}
	if authorAllowed(allow, "mallory") || authorAllowed(allow, "") || authorAllowed(nil, "alice") {
		t.Error("unexpected allow")
	}
}

// fakeGH satisfies the pipeline's gh interface; only the methods used by
// processDirectives do real work.
type fakeGH struct {
	posted []string
}

func (f *fakeGH) FetchDiff(string, int) (string, error)               { return "", nil }
func (f *fakeGH) GetPRHeadSHA(string, int) (string, error)            { return "", nil }
func (f *fakeGH) FetchComments(string, int) ([]github.Comment, error) { return nil, nil }
func (f *fakeGH) SubmitReview(string, int, string, string) (int64, string, error) {
	return 0, "", nil
}
func (f *fakeGH) PostComment(_ string, _ int, body string) (time.Time, error) {
	f.posted = append(f.posted, body)
	return time.Time{}, nil
}

type nopNotifier struct{}

func (nopNotifier) Notify(string, string) {}

func TestProcessDirectives_RememberForgetDedup(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	gh := &fakeGH{}
	p := New(s, gh, nil, nopNotifier{})
	p.SetBotLogin("heimdallm")

	pr := &github.PullRequest{Repo: "org/repo", Number: 7}
	allow := []string{"alice"}

	comments := []github.Comment{
		{ID: 1, Author: "mallory", Body: "@heimdallm remember: evil"},
		{ID: 2, Author: "alice", Body: "@heimdallm remember: unauth endpoints are fine"},
	}
	p.processDirectives(pr, comments, allow)

	items, _ := s.ListRepoInstructions("org/repo")
	if len(items) != 1 || items[0].Instruction != "unauth endpoints are fine" {
		t.Fatalf("want 1 authorized instruction, got %+v", items)
	}
	if len(gh.posted) != 1 {
		t.Fatalf("want 1 ack, got %d: %v", len(gh.posted), gh.posted)
	}

	// Re-processing the same comments is a no-op (dedup via directive_marks).
	p.processDirectives(pr, comments, allow)
	items, _ = s.ListRepoInstructions("org/repo")
	if len(items) != 1 {
		t.Fatalf("dedup failed: got %d instructions", len(items))
	}
	if len(gh.posted) != 1 {
		t.Fatalf("dedup failed: got %d acks", len(gh.posted))
	}

	// forget removes it.
	forget := []github.Comment{{ID: 3, Author: "alice", Body: "@heimdallm forget: " + itoa(items[0].ID)}}
	p.processDirectives(pr, forget, allow)
	items, _ = s.ListRepoInstructions("org/repo")
	if len(items) != 0 {
		t.Fatalf("forget failed: %d remain", len(items))
	}
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }

func TestProcessDirectives_UnsupportedScopeRepliesNoStore(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	gh := &fakeGH{}
	p := New(s, gh, nil, nopNotifier{})
	p.SetBotLogin("heimdallm")
	pr := &github.PullRequest{Repo: "org/repo", Number: 9}

	p.processDirectives(pr, []github.Comment{
		{ID: 50, Author: "alice", Body: "@heimdallm remember(global): everywhere"},
	}, []string{"alice"})

	if items, _ := s.ListRepoInstructions("org/repo"); len(items) != 0 {
		t.Fatalf("unsupported scope must not store an instruction, got %d", len(items))
	}
	if len(gh.posted) != 1 {
		t.Fatalf("want 1 reply explaining unsupported scope, got %d", len(gh.posted))
	}
}

func TestProcessDirectives_UnauthorizedIsSilent(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	gh := &fakeGH{}
	p := New(s, gh, nil, nopNotifier{})
	p.SetBotLogin("heimdallm")
	pr := &github.PullRequest{Repo: "org/repo", Number: 9}

	// Unauthorized user, even with bad scope, gets NO reply (no info leak).
	p.processDirectives(pr, []github.Comment{
		{ID: 51, Author: "mallory", Body: "@heimdallm remember(global): everywhere"},
	}, []string{"alice"})

	if len(gh.posted) != 0 {
		t.Fatalf("unauthorized directive must get no reply, got %d", len(gh.posted))
	}
}

func TestReviewEvent_PersistedAndReused(t *testing.T) {
	// Unit-level guard: the decision helper + persistence contract.
	// Run path: COMMENT chosen when flag on and issues present.
	ev := ReviewEvent("medium", true, true)
	if ev != "COMMENT" {
		t.Fatalf("decision = %q, want COMMENT", ev)
	}
	// Publish reproduction: a stored event is used verbatim; empty falls back.
	if got := publishEventFor(&store.Review{Event: "COMMENT", Severity: "low"}); got != "COMMENT" {
		t.Errorf("publishEventFor(stored COMMENT) = %q, want COMMENT", got)
	}
	if got := publishEventFor(&store.Review{Event: "", Severity: "high"}); got != "REQUEST_CHANGES" {
		t.Errorf("publishEventFor(legacy high) = %q, want REQUEST_CHANGES", got)
	}
}
