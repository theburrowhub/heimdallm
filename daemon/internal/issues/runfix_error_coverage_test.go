package issues_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/issues"
)

func TestPipelineRunFixSurfacesEveryExternalFailureBoundary(t *testing.T) {
	tests := []struct {
		name       string
		exec       *fakeExec
		git        *fakeGit
		wantDetail string
	}{
		{
			name:       "detect CLI",
			exec:       &fakeExec{detectErr: errors.New("cli unavailable")},
			git:        &fakeGit{},
			wantDetail: "detect CLI",
		},
		{
			name:       "checkout head",
			exec:       &fakeExec{detectCLI: "claude"},
			git:        &fakeGit{checkoutErr: errors.New("fetch rejected")},
			wantDetail: "checkout",
		},
		{
			name:       "execute agent",
			exec:       &fakeExec{detectCLI: "claude", rawErr: errors.New("agent failed")},
			git:        &fakeGit{},
			wantDetail: "execute claude",
		},
		{
			name:       "inspect worktree",
			exec:       &fakeExec{detectCLI: "claude"},
			git:        &fakeGit{hasChangesErr: errors.New("git status failed")},
			wantDetail: "git status",
		},
		{
			name:       "commit changes",
			exec:       &fakeExec{detectCLI: "claude"},
			git:        &fakeGit{hasChanges: true, commitErr: errors.New("hook rejected")},
			wantDetail: "commit",
		},
		{
			name:       "push head",
			exec:       &fakeExec{detectCLI: "claude"},
			git:        &fakeGit{hasChanges: true, pushErr: errors.New("remote rejected")},
			wantDetail: "push heimdallm/issue-99",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := issues.New(&fakeStore{}, &fakeGH{}, tc.exec, tc.git, &fakeBroker{}, nil)
			_, err := pipeline.RunFix(context.Background(), fixRequestFixture())
			if err == nil || !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("RunFix error = %v, want detail %q", err, tc.wantDetail)
			}
		})
	}
}
