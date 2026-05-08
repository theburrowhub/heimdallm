package issues

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	maxAffectedPaths       = 20
	maxContributorLogCount = 500
)

var (
	githubLoginRE                = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubNoreplyRE              = regexp.MustCompile(`(?i)^(?:\d+\+)?([A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)@users\.noreply\.github\.com$`)
	errShallowHistory            = errors.New("git history is shallow")
	validSeverities              = map[string]struct{}{"low": {}, "medium": {}, "high": {}, "critical": {}}
	validConfidences             = map[string]struct{}{"low": {}, "medium": {}, "high": {}}
	priorityLabelColorBySeverity = map[string]string{
		"critical": "B60205",
		"high":     "D93F0B",
		"medium":   "FBCA04",
		"low":      "0E8A16",
	}
)

type gitHistoryRunner interface {
	Output(ctx context.Context, dir string, args ...string) ([]byte, error)
}

type defaultGitHistoryRunner struct{}

func (defaultGitHistoryRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return captureGit(ctx, dir, nil, args...)
}

type contributorHit struct {
	login string
	count int
}

func enrichTriageResult(ctx context.Context, r *IssueReviewResult, workDir, triageOwner string, runner gitHistoryRunner) {
	if r == nil {
		return
	}
	r.Severity = normalizeSeverity(r.Severity)
	r.Triage.Severity = normalizeSeverity(firstNonEmpty(r.Triage.Severity, r.Severity))
	r.Triage.Category = strings.ToLower(strings.TrimSpace(r.Triage.Category))
	r.Triage.PriorityLabel = normalizePriorityLabel(r.Triage.PriorityLabel, r.Severity)
	r.Triage.SuggestedAssignee = normalizeGitHubLogin(r.Triage.SuggestedAssignee)
	r.Triage.TentativeAssignee = normalizeGitHubLogin(firstNonEmpty(r.Triage.TentativeAssignee, r.Triage.SuggestedAssignee))
	r.Triage.AssigneeConfidence = normalizeAssigneeConfidence(r.Triage.AssigneeConfidence)
	r.Triage.AffectedPaths = sanitizeAffectedPaths(r.Triage.AffectedPaths)

	assignee, source, diagnostic, evidence := resolveTriageAssignee(ctx, workDir, triageOwner, r.Triage, runner)
	r.Triage.AssignedAssignee = assignee
	r.Triage.AssignmentSource = source
	r.Triage.AssignmentDiagnostic = diagnostic
	if len(evidence) > 0 {
		r.Triage.AssigneeEvidence = evidence
	}
}

func resolveTriageAssignee(ctx context.Context, workDir, triageOwner string, triage Triage, runner gitHistoryRunner) (assignee, source, diagnostic string, evidence []string) {
	fallback := normalizeGitHubLogin(triageOwner)
	suggested := normalizeGitHubLogin(triage.SuggestedAssignee)
	confidence := normalizeAssigneeConfidence(triage.AssigneeConfidence)

	fallbackResult := func(reason string) (string, string, string, []string) {
		if fallback == "" {
			return "", "none", reason, nil
		}
		return fallback, "triage_owner", reason, []string{fmt.Sprintf("@%s configured as triage_owner fallback", fallback)}
	}

	if suggested == "" && fallback == "" {
		return "", "none", "no suggested_assignee or triage_owner available", nil
	}
	if !confidenceAllowsAssignment(confidence) {
		return fallbackResult("assignee confidence is low or missing")
	}
	if strings.TrimSpace(workDir) == "" {
		return fallbackResult("repo workdir unavailable; cannot verify assignee against git history")
	}
	if len(triage.AffectedPaths) == 0 {
		return fallbackResult("no affected_paths provided for git-history verification")
	}
	if runner == nil {
		runner = defaultGitHistoryRunner{}
	}

	contributors, err := contributorsForPaths(ctx, runner, workDir, triage.AffectedPaths)
	if err != nil {
		return fallbackResult(fmt.Sprintf("git-history verification unavailable: %v", err))
	}
	if len(contributors) == 0 {
		return fallbackResult("no GitHub-login-like contributors found in affected path history")
	}

	if suggested != "" {
		for _, c := range contributors {
			if strings.EqualFold(c.login, suggested) {
				return c.login, "suggested_assignee_verified", "suggested_assignee appears in affected path history", contributorEvidence(contributors)
			}
		}
		if fallback != "" {
			return fallbackResult(fmt.Sprintf("model suggested @%s, but that login was not found in affected path history", suggested))
		}
	}

	top := contributors[0]
	reason := "assigned top contributor from affected path history"
	if suggested != "" {
		reason = fmt.Sprintf("model suggested @%s, but that login was not found in affected path history; assigned top contributor instead", suggested)
	}
	return top.login, "history_top_contributor", reason, contributorEvidence(contributors)
}

func contributorsForPaths(ctx context.Context, runner gitHistoryRunner, workDir string, affectedPaths []string) ([]contributorHit, error) {
	if runner == nil {
		runner = defaultGitHistoryRunner{}
	}
	shallow, err := runner.Output(ctx, workDir, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, fmt.Errorf("inspect repository depth: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(string(shallow)), "true") {
		return nil, errShallowHistory
	}

	args := []string{"log", fmt.Sprintf("--max-count=%d", maxContributorLogCount), "--no-merges", "--format=%ae%x00%an", "--"}
	args = append(args, affectedPaths...)
	out, err := runner.Output(ctx, workDir, args...)
	if err != nil {
		return nil, fmt.Errorf("git log affected paths: %w", err)
	}

	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 2)
		email := strings.TrimSpace(parts[0])
		seenInCommit := map[string]struct{}{}
		for _, login := range contributorLoginsFromEmail(email) {
			if _, seen := seenInCommit[strings.ToLower(login)]; seen {
				continue
			}
			seenInCommit[strings.ToLower(login)] = struct{}{}
			counts[login]++
		}
	}
	hits := make([]contributorHit, 0, len(counts))
	for login, count := range counts {
		hits = append(hits, contributorHit{login: login, count: count})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].count == hits[j].count {
			return hits[i].login < hits[j].login
		}
		return hits[i].count > hits[j].count
	})
	return hits, nil
}

func contributorLoginsFromEmail(email string) []string {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	if m := githubNoreplyRE.FindStringSubmatch(email); len(m) == 2 {
		return []string{m[1]}
	}
	return nil
}

func contributorEvidence(contributors []contributorHit) []string {
	if len(contributors) == 0 {
		return nil
	}
	n := len(contributors)
	if n > 3 {
		n = 3
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("@%s: %d commits touching affected paths", contributors[i].login, contributors[i].count))
	}
	return out
}

func sanitizeAffectedPaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
		if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
			continue
		}
		clean := path.Clean(p)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "-") || clean == ".git" || strings.HasPrefix(clean, ".git/") {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
		if len(out) >= maxAffectedPaths {
			break
		}
	}
	return out
}

func normalizeGitHubLogin(login string) string {
	login = strings.TrimSpace(strings.TrimLeft(login, "@"))
	if login == "" || !githubLoginRE.MatchString(login) {
		return ""
	}
	return login
}

func normalizeAssigneeConfidence(confidence string) string {
	confidence = strings.ToLower(strings.TrimSpace(confidence))
	if _, ok := validConfidences[confidence]; !ok {
		return ""
	}
	return confidence
}

func confidenceAllowsAssignment(confidence string) bool {
	return confidence == "medium" || confidence == "high"
}

func normalizeSeverity(severity string) string {
	severity = strings.ToLower(strings.TrimSpace(severity))
	if _, ok := validSeverities[severity]; !ok {
		return "low"
	}
	return severity
}

func normalizePriorityLabel(label, severity string) string {
	severity = normalizeSeverity(severity)
	label = strings.ToLower(strings.TrimSpace(label))
	switch label {
	case "critical", "high", "medium", "low":
		return "priority: " + label
	case "priority: critical", "priority: high", "priority: medium", "priority: low":
		return label
	case "":
		return "priority: " + severity
	default:
		return "priority: " + severity
	}
}

func priorityLabelColor(severity string) string {
	severity = normalizeSeverity(severity)
	if color, ok := priorityLabelColorBySeverity[severity]; ok {
		return color
	}
	return priorityLabelColorBySeverity["low"]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
