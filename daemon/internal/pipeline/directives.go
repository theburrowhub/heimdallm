package pipeline

import (
	"fmt"
	"strings"

	"github.com/heimdallm/daemon/internal/store"
)

const (
	directiveRemember = "remember"
	directiveForget   = "forget"
	directiveList     = "list"
)

// parseDirective extracts a heimdallm instruction directive addressed to
// botLogin from a comment body. Returns ok=false when the comment is not a
// recognized directive. Grammar (case-insensitive, leading whitespace allowed):
//
//	@<botLogin> <verb>[(<scope>)][: <payload>]
//
// verb ∈ {remember, forget, list}; scope defaults to "repo". remember and
// forget require a non-empty payload; list takes none.
func parseDirective(body, botLogin string) (verb, scope, payload string, ok bool) {
	if botLogin == "" {
		return "", "", "", false
	}
	line := strings.TrimSpace(body)
	mention := "@" + botLogin
	if !strings.HasPrefix(strings.ToLower(line), strings.ToLower(mention)) {
		return "", "", "", false
	}
	raw := line[len(mention):]
	// Require a word boundary after the mention so a login that is a strict
	// prefix of the next token (e.g. "@heimdallmremember") does not match.
	if raw != "" && raw[0] != ' ' && raw[0] != '\t' && raw[0] != '(' {
		return "", "", "", false
	}
	rest := strings.TrimSpace(raw)

	head := rest
	if i := strings.Index(rest, ":"); i >= 0 {
		head = strings.TrimSpace(rest[:i])
		payload = strings.TrimSpace(rest[i+1:])
	}

	scope = "repo"
	if i := strings.Index(head, "("); i >= 0 && strings.HasSuffix(head, ")") {
		scope = strings.ToLower(strings.TrimSpace(head[i+1 : len(head)-1]))
		head = strings.TrimSpace(head[:i])
	}
	if scope == "" {
		scope = "repo"
	}

	verb = strings.ToLower(strings.TrimSpace(head))
	switch verb {
	case directiveRemember, directiveForget:
		if payload == "" {
			return "", "", "", false
		}
	case directiveList:
		if payload != "" {
			return "", "", "", false
		}
	default:
		return "", "", "", false
	}
	return verb, scope, payload, true
}

// authorAllowed mirrors config.RepoAI.MatchesInstructionAuthors but operates on
// the resolved allowlist slice threaded through RunOptions. It is duplicated
// here deliberately: the pipeline package does not import config (the codebase
// already shadows config<->pipeline types to avoid an import cycle — see
// config.ResolvedReviewGuards). Empty allowlist denies everyone.
func authorAllowed(allowlist []string, login string) bool {
	login = strings.ToLower(strings.TrimSpace(strings.TrimLeft(login, "@")))
	if login == "" {
		return false
	}
	for _, a := range allowlist {
		if strings.ToLower(strings.TrimSpace(strings.TrimLeft(a, "@"))) == login {
			return true
		}
	}
	return false
}

// formatStandingInstructions renders the persistent instruction set as a
// high-salience prompt section. Returns "" when there are none (the section is
// then omitted from the prompt entirely).
func formatStandingInstructions(items []store.RepoInstruction) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("PROJECT STANDING INSTRUCTIONS (set by repo maintainers — authoritative):\n")
	for _, it := range items {
		fmt.Fprintf(&b, "- %s\n", it.Instruction)
	}
	return b.String()
}
