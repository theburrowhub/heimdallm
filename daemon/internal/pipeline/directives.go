package pipeline

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/heimdallm/daemon/internal/github"
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

// processDirectives scans comments for heimdallm instruction directives (#383).
// Authorized directives mutate the repo's persistent instruction set and are
// acknowledged with a reply; every processed directive comment is marked so it
// is applied at most once across poll cycles. All failures are logged and never
// abort the surrounding review.
func (p *Pipeline) processDirectives(pr *github.PullRequest, comments []github.Comment, allowlist []string) {
	botLogin := p.currentBotLogin()
	for _, c := range comments {
		// Dedup is keyed on the GitHub comment ID. Directives are normally
		// top-level issue comments; review-comment and issue-comment IDs are
		// separate GitHub sequences, so a cross-space ID collision could in
		// theory mark one directive as already-seen. The blast radius is a
		// single missed directive (recoverable by re-commenting), so we accept
		// the bare-ID key rather than a (kind, id) composite.
		if c.ID == 0 {
			continue // cannot dedup without a stable id
		}
		verb, scope, payload, ok := parseDirective(c.Body, botLogin)
		if !ok {
			continue
		}
		done, err := p.store.DirectiveProcessed(c.ID)
		if err != nil {
			slog.Warn("pipeline: directive dedup check failed", "err", err, "comment_id", c.ID)
			continue
		}
		if done {
			continue
		}
		switch {
		case !authorAllowed(allowlist, c.Author):
			// Silent for unauthorized users — no reply, so the bot does not
			// advertise the directive feature to non-maintainers. Marked
			// processed below so it is not re-evaluated every poll cycle.
			slog.Info("pipeline: ignoring directive from unauthorized author", "author", c.Author, "repo", pr.Repo)
		case scope != "repo":
			// Authorized user, but only repo scope is implemented. Reply so the
			// maintainer knows why nothing happened, then burn the comment.
			slog.Info("pipeline: ignoring directive with unsupported scope", "scope", scope, "repo", pr.Repo)
			p.reply(pr, fmt.Sprintf("⚠️ Only repo-scoped instructions are supported. Drop the scope (e.g. `@%s %s: …`) to apply it to %s.", botLogin, verb, pr.Repo))
		default:
			p.applyDirective(pr, botLogin, verb, payload, c)
		}
		p.markDirective(c.ID, verb)
	}
}

func (p *Pipeline) applyDirective(pr *github.PullRequest, botLogin, verb, payload string, c github.Comment) {
	switch verb {
	case directiveRemember:
		id, err := p.store.AddRepoInstruction(pr.Repo, payload, c.Author, c.ID)
		if err != nil {
			slog.Warn("pipeline: add repo instruction failed", "err", err, "repo", pr.Repo)
			return
		}
		p.reply(pr, fmt.Sprintf("✅ Remembered for %s (#%d): %s", pr.Repo, id, payload))
	case directiveForget:
		id, perr := strconv.ParseInt(strings.TrimSpace(payload), 10, 64)
		if perr != nil {
			p.reply(pr, fmt.Sprintf("⚠️ `forget` expects a numeric instruction id; got %q. Use `@%s list` to see ids.", payload, botLogin))
			return
		}
		removed, err := p.store.DeleteRepoInstruction(pr.Repo, id)
		if err != nil {
			slog.Warn("pipeline: delete repo instruction failed", "err", err, "repo", pr.Repo)
			return
		}
		if !removed {
			p.reply(pr, fmt.Sprintf("⚠️ No standing instruction #%d for %s.", id, pr.Repo))
			return
		}
		p.reply(pr, fmt.Sprintf("🗑️ Forgot #%d for %s.", id, pr.Repo))
	case directiveList:
		p.reply(pr, p.formatInstructionList(pr.Repo))
	}
}

func (p *Pipeline) reply(pr *github.PullRequest, body string) {
	if _, err := p.gh.PostComment(pr.Repo, pr.Number, body); err != nil {
		slog.Warn("pipeline: post directive reply failed", "err", err, "repo", pr.Repo, "pr", pr.Number)
	}
}

func (p *Pipeline) markDirective(commentID int64, verb string) {
	if err := p.store.MarkDirectiveProcessed(commentID, verb); err != nil {
		slog.Warn("pipeline: mark directive processed failed", "err", err, "comment_id", commentID)
	}
}

func (p *Pipeline) formatInstructionList(repo string) string {
	items, err := p.store.ListRepoInstructions(repo)
	if err != nil {
		slog.Warn("pipeline: list repo instructions failed", "err", err, "repo", repo)
		return "⚠️ Could not read standing instructions."
	}
	if len(items) == 0 {
		return fmt.Sprintf("No standing instructions for %s.", repo)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Standing instructions for %s:\n", repo)
	for _, it := range items {
		fmt.Fprintf(&b, "- #%d: %s\n", it.ID, it.Instruction)
	}
	return b.String()
}
