package executor

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// diffFileHeader starts each file section of a unified git diff.
var diffFileHeader = regexp.MustCompile(`(?m)^diff --git `)

// Bounds on the file names the truncation note lists. The names are PR-author
// input, so how much of them reaches the prompt is capped.
const (
	maxTruncatedNames      = 20
	maxTruncatedNamesBytes = 1024
	maxTruncatedNameBytes  = 256
)

// DiffSectionPath extracts the post-image path ("b/...") of a file section of
// a unified git diff, given the text after its "diff --git " header prefix. It
// falls back to the pre-image path for deletions.
func DiffSectionPath(section string) string {
	firstLine, _, _ := strings.Cut(section, "\n")
	// "a/old b/new"; paths with spaces are rare enough that splitting on the
	// last " b/" is the pragmatic choice git's own porcelain makes.
	if i := strings.LastIndex(firstLine, " b/"); i >= 0 {
		return firstLine[i+3:]
	}
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[0], "a/")
}

// truncateDiff caps diff at limit bytes. It keeps the same leading content a
// plain byte cut would, so no review sees less code, but it cuts on a line
// boundary (a rune boundary for a single over-long line) and ends with a note
// naming the file shown only in part and the files not shown at all. Without
// those names the model cannot tell a symbol it never saw from one that does
// not exist. A diff that is not in git's format keeps the generic marker.
func truncateDiff(diff string, limit int) string {
	if len(diff) <= limit {
		return diff
	}
	cut := strings.LastIndexByte(diff[:limit], '\n') + 1
	if cut == 0 {
		cut = limit
		for cut > 0 && !utf8.RuneStart(diff[cut]) {
			cut--
		}
	}
	kept := diff[:cut]
	if !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	return kept + truncationNote(diff, cut)
}

// truncationNote describes what a cut at byte offset cut leaves out of diff.
func truncationNote(diff string, cut int) string {
	locs := diffFileHeader.FindAllStringIndex(diff, -1)
	if len(locs) == 0 {
		return "... (diff truncated)"
	}
	var partial string
	var omitted []string
	for i, loc := range locs {
		end := len(diff)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		name := DiffSectionPath(diff[loc[1]:end])
		switch {
		case loc[0] >= cut:
			omitted = append(omitted, name)
		case end > cut:
			partial = name
		}
	}
	var parts []string
	if partial != "" {
		parts = append(parts, clipName(partial)+" is shown only in part")
	}
	if len(omitted) > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) not shown: %s", len(omitted), listNames(omitted)))
	}
	if len(parts) == 0 { // a header with no path
		return "... (diff truncated)"
	}
	return "... (diff truncated: " + strings.Join(parts, "; ") + ")"
}

// listNames joins as many names as the caps allow and counts the rest. A
// clipped name always fits the byte cap, so at least one name is listed.
func listNames(names []string) string {
	var b strings.Builder
	shown := 0
	for _, n := range names {
		n = clipName(n)
		if shown == maxTruncatedNames || b.Len()+len(n) > maxTruncatedNamesBytes {
			break
		}
		if shown > 0 {
			b.WriteString(", ")
		}
		b.WriteString(n)
		shown++
	}
	if rest := len(names) - shown; rest > 0 {
		fmt.Fprintf(&b, " and %d more", rest)
	}
	return b.String()
}

// clipName shortens an over-long path on a rune boundary.
func clipName(name string) string {
	if len(name) <= maxTruncatedNameBytes {
		return name
	}
	cut := maxTruncatedNameBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut] + "…"
}
