package pipeline

import (
	"errors"
	"testing"
)

type fakeTeams struct {
	members map[string]bool // "org/team/login"
	err     error
	calls   []string
}

func (f *fakeTeams) IsTeamMember(org, team, login string) (bool, error) {
	f.calls = append(f.calls, org+"/"+team+"/"+login)
	return f.members[org+"/"+team+"/"+login], f.err
}

func TestNeverApproveExempt(t *testing.T) {
	teams := &fakeTeams{members: map[string]bool{"acme/core/carol": true}}
	p := &Pipeline{teams: teams}
	entries := []string{"@alice", "acme/core", "acme/../evil", "acme/"}

	for login, want := range map[string]bool{
		"Alice":   true, // listed login, case-insensitive
		"carol":   true, // member of acme/core
		"mallory": false,
	} {
		if got := p.neverApproveExempt(entries, login); got != want {
			t.Errorf("neverApproveExempt(%s) = %v, want %v", login, got, want)
		}
	}
	// Alice matched by login without a lookup; malformed entries never reach GitHub.
	for _, c := range teams.calls {
		if c != "acme/core/carol" && c != "acme/core/mallory" {
			t.Errorf("unexpected team lookup %q", c)
		}
	}

	if (&Pipeline{teams: &fakeTeams{err: errors.New("boom")}}).neverApproveExempt([]string{"@acme/core"}, "carol") {
		t.Error("lookup error must fail closed")
	}
	if (&Pipeline{}).neverApproveExempt([]string{"acme/core"}, "carol") {
		t.Error("without a checker team entries must not match")
	}
}
