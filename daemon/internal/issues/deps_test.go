package issues

import (
	"reflect"
	"testing"
)

func TestParseDependencies_SameRepoBullets(t *testing.T) {
	body := `Summary line.

## Depends on
- #42
- #57
`
	got := ParseDependencies(body, "org/repo")
	want := []IssueRef{{Repo: "org/repo", Number: 42}, {Repo: "org/repo", Number: 57}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDependencies_CrossRepo(t *testing.T) {
	body := `## Depends on
- other-org/shared#1
- #2
`
	got := ParseDependencies(body, "org/repo")
	want := []IssueRef{{Repo: "other-org/shared", Number: 1}, {Repo: "org/repo", Number: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDependencies_NoSection(t *testing.T) {
	if got := ParseDependencies("# Just a title\n\nSome prose.", "org/repo"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestParseDependencies_EmptySection(t *testing.T) {
	if got := ParseDependencies("## Depends on\n\nBody continues.", "org/repo"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestParseDependencies_HeadingCaseAndColon(t *testing.T) {
	cases := []string{
		"## Depends On:\n- #1",
		"## depends on\n- #1",
		"### Depends on\n- #1",
	}
	for _, body := range cases {
		got := ParseDependencies(body, "org/repo")
		want := []IssueRef{{Repo: "org/repo", Number: 1}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body %q: got %v, want %v", body, got, want)
		}
	}
}

func TestParseDependencies_StopsAtNextHeading(t *testing.T) {
	body := `## Depends on
- #1

## Unrelated
- #2
`
	got := ParseDependencies(body, "org/repo")
	want := []IssueRef{{Repo: "org/repo", Number: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (should not cross into next heading)", got, want)
	}
}

func TestParseDependencies_MultipleRefsPerBullet(t *testing.T) {
	body := "## Depends on\n- Blocked by #1 and other-org/r#2\n"
	got := ParseDependencies(body, "org/repo")
	want := []IssueRef{{Repo: "org/repo", Number: 1}, {Repo: "other-org/r", Number: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDependencies_IgnoresNonBulletLinesInSection(t *testing.T) {
	body := `## Depends on

Some prose, ignored: #999.

- #1
`
	got := ParseDependencies(body, "org/repo")
	want := []IssueRef{{Repo: "org/repo", Number: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (prose refs should not be captured)", got, want)
	}
}

func TestParseDependencies_MarkdownLinkAsBullet(t *testing.T) {
	// GitHub auto-links `#42` already, but users pasting a fully-linked
	// reference like `[#42](https://github.com/…)` must still parse.
	body := "## Depends on\n- [#42](https://github.com/org/repo/issues/42)\n"
	got := ParseDependencies(body, "org/repo")
	want := []IssueRef{{Repo: "org/repo", Number: 42}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
