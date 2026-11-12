package pipeline

import (
	"log/slog"
	"regexp"
	"strings"
)

// teamSlugRe bounds both halves of an "org/team" entry before they reach a
// GitHub URL: letters, digits, '-' and '_' only (no dots, so no "..").
var teamSlugRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// neverApproveExempt reports whether login is exempt from the never-approve
// gate: it is listed itself, or it is an active member of a listed "org/team"
// (optionally written "@org/team", as in CODEOWNERS). Malformed team entries
// and lookup failures fail closed: not exempt.
//
// Kept apart from authorAllowed on purpose: instruction_authors uses that
// matcher, and teams must not widen who may set standing instructions.
func (p *Pipeline) neverApproveExempt(entries []string, login string) bool {
	if authorAllowed(entries, login) {
		return true
	}
	for _, e := range entries {
		org, team, isTeam := strings.Cut(strings.TrimLeft(strings.TrimSpace(e), "@"), "/")
		if !isTeam {
			continue
		}
		if !teamSlugRe.MatchString(org) || !teamSlugRe.MatchString(team) {
			slog.Warn("pipeline: ignoring malformed team in never_approve_exempt_authors", "entry", e)
			continue
		}
		if p.teams == nil {
			continue
		}
		member, err := p.teams.IsTeamMember(org, team, login)
		if err != nil {
			slog.Warn("pipeline: team lookup failed, author not exempted", "team", org+"/"+team, "login", login, "err", err)
			continue
		}
		if member {
			return true
		}
	}
	return false
}
