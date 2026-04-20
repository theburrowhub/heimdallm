package issues

import (
	"regexp"
	"strconv"
	"strings"
)

// IssueRef is a reference to another issue/PR by repo + number. Used by the
// dependency parser and the promotion orchestrator.
type IssueRef struct {
	Repo   string
	Number int
}

// dependsHeading matches a `## Depends on` heading (case insensitive,
// optional trailing colon, optional trailing whitespace). The check runs
// per-line so leading whitespace is explicitly disallowed — Markdown
// headings don't nest, a leading space would invalidate the heading on
// GitHub too.
var dependsHeading = regexp.MustCompile(`^##+\s+depends\s+on\s*:?\s*$`)

// nextHeading signals the end of the Depends-on section: any subsequent
// `#`-prefixed heading, same level or deeper. We stop *before* that line.
var nextHeading = regexp.MustCompile(`^#+\s`)

// bulletLine matches `- `, `* `, `• ` with optional leading whitespace.
// The bullet content (after the marker) is what we scan for issue refs.
var bulletLine = regexp.MustCompile(`^\s*[-*•]\s+(.*)$`)

// issueRef extracts `owner/repo#N` OR `#N` from arbitrary text. We use a
// single alternation so multiple refs on one bullet all get captured, e.g.
// `- #1 and owner/other#2`.
var issueRef = regexp.MustCompile(`(?:([\w.-]+/[\w.-]+))?#(\d+)`)

// ParseDependencies scans body for the `## Depends on` section and returns
// the issue references inside. Same-repo refs (`#N`) inherit defaultRepo;
// cross-repo refs (`owner/repo#N`) keep their explicit repo. Unknown or
// missing section → nil.
//
// The parser is forgiving: malformed bullets are skipped, not an error;
// multiple refs per bullet are all captured; headings other than `##
// Depends on` interrupt the section.
func ParseDependencies(body, defaultRepo string) []IssueRef {
	inSection := false
	var refs []IssueRef

	for _, line := range strings.Split(body, "\n") {
		lower := strings.ToLower(line)

		if !inSection {
			if dependsHeading.MatchString(lower) {
				inSection = true
			}
			continue
		}

		// Leaving the section on any subsequent heading. Both checks run
		// on the lower-cased line so the "exclude re-entering our own
		// heading" branch is comparing apples to apples — `#` is ASCII
		// so the case fold is a no-op for the heading markers but makes
		// the intent obvious.
		if nextHeading.MatchString(lower) && !dependsHeading.MatchString(lower) {
			break
		}

		m := bulletLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, rm := range issueRef.FindAllStringSubmatch(m[1], -1) {
			n, err := strconv.Atoi(rm[2])
			if err != nil || n <= 0 {
				continue
			}
			repo := rm[1]
			if repo == "" {
				repo = defaultRepo
			}
			refs = append(refs, IssueRef{Repo: repo, Number: n})
		}
	}
	return refs
}
