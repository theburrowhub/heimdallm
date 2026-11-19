package executor

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// fileSection renders one file of a git diff whose added lines repeat body.
func fileSection(path string, lines int, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -0,0 +1,%d @@\n", path, path, path, path, lines)
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "+%s %d\n", body, i)
	}
	return b.String()
}

// splitTruncated returns the kept prefix and the note of a truncated diff,
// checking the invariant every truncation must hold: the kept text is a prefix
// of the input that fits the cap and ends a line.
func splitTruncated(t *testing.T, diff, out string, limit int) (kept, note string) {
	t.Helper()
	i := strings.LastIndex(out, "... (diff truncated")
	if i < 0 {
		t.Fatalf("no truncation marker in %q", out)
	}
	kept, note = out[:i], out[i:]
	if !strings.HasPrefix(diff, kept) {
		t.Fatalf("kept text is not a prefix of the diff:\n%q", kept)
	}
	if len(kept) > limit {
		t.Fatalf("kept %d bytes, cap is %d", len(kept), limit)
	}
	if !strings.HasSuffix(kept, "\n") {
		t.Fatalf("kept text does not end a line: %q", kept[max(0, len(kept)-20):])
	}
	return kept, note
}

func TestTruncateDiff_UnderTheCapIsUnchanged(t *testing.T) {
	diff := fileSection("a.go", 3, "x")
	if got := truncateDiff(diff, len(diff)); got != diff {
		t.Fatalf("a diff at the cap changed:\n%q", got)
	}
}

func TestTruncateDiff_NamesThePartialFileAndTheFilesNotShown(t *testing.T) {
	diff := fileSection("a.go", 2, "short") + fileSection("pkg/b.go", 40, "a longer line of code") +
		fileSection("c.go", 2, "c") + fileSection("d.go", 2, "d")
	const limit = 400
	out := truncateDiff(diff, limit)
	kept, note := splitTruncated(t, diff, out, limit)
	if !strings.Contains(kept, "diff --git a/pkg/b.go") {
		t.Fatalf("the cut should land inside pkg/b.go, kept:\n%s", kept)
	}
	// A plain byte cut kept limit bytes; the line-boundary cut gives up at
	// most the one partial line.
	if lost := limit - len(kept); lost >= len("+a longer line of code 00\n") {
		t.Errorf("gave up %d bytes, more than one line", lost)
	}
	want := "... (diff truncated: pkg/b.go is shown only in part; 2 file(s) not shown: c.go, d.go)"
	if note != want {
		t.Errorf("note = %q\nwant  %q", note, want)
	}
}

func TestTruncateDiff_CutOnAFileBoundaryHasNoPartialFile(t *testing.T) {
	first := fileSection("a.go", 5, "x")
	diff := first + fileSection("b.go", 5, "y")
	out := truncateDiff(diff, len(first))
	kept, note := splitTruncated(t, diff, out, len(first))
	if kept != first {
		t.Fatalf("kept = %q, want the whole first file", kept)
	}
	if want := "... (diff truncated: 1 file(s) not shown: b.go)"; note != want {
		t.Errorf("note = %q, want %q", note, want)
	}
}

func TestTruncateDiff_CapsHowManyNamesItLists(t *testing.T) {
	diff := fileSection("big.go", 50, "line")
	for i := 0; i < 30; i++ {
		diff += fileSection(fmt.Sprintf("f%02d.go", i), 1, "z")
	}
	_, note := splitTruncated(t, diff, truncateDiff(diff, 300), 300)
	if !strings.Contains(note, "30 file(s) not shown: f00.go, ") || !strings.HasSuffix(note, "f19.go and 10 more)") {
		t.Errorf("note = %q, want 20 names and a count of the rest", note)
	}
}

func TestTruncateDiff_CapsTheBytesOfNamesItLists(t *testing.T) {
	diff := fileSection("big.go", 50, "line")
	for i := 0; i < 10; i++ {
		diff += fileSection(fmt.Sprintf("%s%d.go", strings.Repeat("d/", 100), i), 1, "z")
	}
	_, note := splitTruncated(t, diff, truncateDiff(diff, 300), 300)
	if len(note) > maxTruncatedNamesBytes+200 {
		t.Errorf("note is %d bytes, want the listed names capped near %d", len(note), maxTruncatedNamesBytes)
	}
	if !strings.Contains(note, "10 file(s) not shown: ") || !strings.Contains(note, " more)") {
		t.Errorf("note = %q, want some names and a count of the rest", note)
	}
}

func TestTruncateDiff_ClipsAnOverLongName(t *testing.T) {
	long := strings.Repeat("€", 200) + ".go" // 3-byte runes: byte 256 falls inside one
	diff := fileSection("big.go", 50, "line") + fileSection(long, 1, "z")
	_, note := splitTruncated(t, diff, truncateDiff(diff, 300), 300)
	if strings.Contains(note, long) || !strings.Contains(note, "…") {
		t.Errorf("note = %q, want the long name clipped", note)
	}
	if !utf8.ValidString(note) {
		t.Errorf("note is not valid UTF-8: %q", note)
	}
}

func TestListNames_AlwaysListsAClippedFirstName(t *testing.T) {
	names := []string{strings.Repeat("x", maxTruncatedNamesBytes+1)}
	// clipName brings it under the byte cap, so it is listed clipped.
	if got := listNames(names); !strings.HasSuffix(got, "…") {
		t.Errorf("listNames = %q, want the clipped name", got)
	}
	if got := listNames(nil); got != "" {
		t.Errorf("listNames(nil) = %q, want empty", got)
	}
}

func TestTruncateDiff_NonGitDiffKeepsTheGenericMarker(t *testing.T) {
	diff := strings.Repeat("some line of a plain patch\n", 20)
	out := truncateDiff(diff, 100)
	_, note := splitTruncated(t, diff, out, 100)
	if note != "... (diff truncated)" {
		t.Errorf("note = %q, want the generic marker", note)
	}
}

func TestTruncateDiff_SingleOverLongLineCutsOnARuneBoundary(t *testing.T) {
	diff := "+" + strings.Repeat("é", 100) // 2-byte runes, no newline
	out := truncateDiff(diff, 50)          // byte 50 falls inside a rune
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out)
	}
	kept, _, ok := strings.Cut(out, "\n... (diff truncated)")
	if !ok || !strings.HasPrefix(diff, kept) || len(kept) > 50 || len(kept) < 49 {
		t.Errorf("out = %q, want a rune-boundary prefix and the marker on its own line", out)
	}
}

func TestDiffSectionPath(t *testing.T) {
	for in, want := range map[string]string{
		"a/x.go b/x.go\nindex 1..2":    "x.go",
		"a/old.go b/new/name.go\n":     "new/name.go",
		"a/gone.go\ndeleted file mode": "gone.go",
		"":                             "",
	} {
		if got := DiffSectionPath(in); got != want {
			t.Errorf("DiffSectionPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncationNote_HeaderWithoutAPathKeepsTheGenericMarker(t *testing.T) {
	diff := "diff --git \n" + strings.Repeat("+x\n", 50)
	_, note := splitTruncated(t, diff, truncateDiff(diff, 40), 40)
	if note != "... (diff truncated)" {
		t.Errorf("note = %q, want the generic marker", note)
	}
}
