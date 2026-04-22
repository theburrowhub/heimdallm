package store

import (
	"fmt"
	"time"
)

// Agent (stored as "prompts" in the UI) defines a named review profile.
// Either Instructions or Prompt should be set:
//   - Instructions: plain text injected into the default template (simple mode)
//   - Prompt: full custom template with {placeholders} (advanced mode)
//
// CLIFlags: optional extra flags passed to the AI binary (e.g. --model claude-opus-4-6)
//
// Issue triage (IssuePrompt / IssueInstructions) and auto_implement
// (ImplementPrompt / ImplementInstructions) follow the same prompt-vs-
// instructions split as the PR review fields above. A non-empty *Prompt
// field fully replaces the built-in template; *Instructions is injected
// into the default template instead.
type Agent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CLI          string    `json:"cli"`          // claude | gemini | codex (overrides global)
	Prompt       string    `json:"prompt"`       // full template (advanced); empty = use instructions
	Instructions string    `json:"instructions"` // what to focus on (simple mode)
	CLIFlags     string    `json:"cli_flags"`    // extra CLI args
	// Per-category active flags. Each pipeline (PR review, issue triage,
	// auto-implement) picks whichever agent has its flag set; the three
	// activations are independent — a single agent can be active for one,
	// two, or all three categories, and the three tabs in the UI activate
	// them separately.
	IsDefaultPR    bool      `json:"is_default_pr"`
	IsDefaultIssue bool      `json:"is_default_issue"`
	IsDefaultDev   bool      `json:"is_default_dev"`
	CreatedAt      time.Time `json:"created_at"`

	// Issue triage prompt customization (mirrors Prompt/Instructions for PRs).
	IssuePrompt       string `json:"issue_prompt"`       // full custom template for issue triage
	IssueInstructions string `json:"issue_instructions"` // plain text injected into default issue template

	// Auto_implement prompt customization (mirrors IssuePrompt/IssueInstructions for triage).
	ImplementPrompt       string `json:"implement_prompt"`       // full custom template for code generation
	ImplementInstructions string `json:"implement_instructions"` // plain text injected into default implement template
}

// IsDefault returns true when the agent is active for any category. Kept as a
// convenience for callers that don't care which specific category is active
// (e.g. the "is any prompt configured at all?" kind of check).
func (a Agent) IsDefault() bool {
	return a.IsDefaultPR || a.IsDefaultIssue || a.IsDefaultDev
}

// AgentCategory identifies which pipeline a default-agent lookup is for. The
// string values match the JSON naming used over the HTTP API and the Flutter
// client, so they can be threaded through without per-layer translation.
type AgentCategory string

const (
	AgentCategoryPR    AgentCategory = "pr"
	AgentCategoryIssue AgentCategory = "issue"
	AgentCategoryDev   AgentCategory = "dev"
)

// defaultColumn returns the SQL column name backing the given category flag.
// Centralising the mapping here keeps the category-per-flag relationship in
// one place and gives the store-layer filters a single source of truth.
func (c AgentCategory) defaultColumn() string {
	switch c {
	case AgentCategoryPR:
		return "is_default_pr"
	case AgentCategoryIssue:
		return "is_default_issue"
	case AgentCategoryDev:
		return "is_default_dev"
	}
	return ""
}

const agentColumns = "id, name, cli, prompt, instructions, cli_flags, is_default_pr, is_default_issue, is_default_dev, created_at, issue_prompt, issue_instructions, implement_prompt, implement_instructions"

func (s *Store) ListAgents() ([]*Agent, error) {
	// Sort "most-active first": agents active in more categories appear
	// higher than single-category ones, inactive ones last. Matches how
	// the UI wants to present them (the currently-most-impactful agent at
	// the top of the list).
	rows, err := s.db.Query(
		"SELECT " + agentColumns + " FROM agents " +
			"ORDER BY (is_default_pr + is_default_issue + is_default_dev) DESC, name ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) UpsertAgent(a *Agent) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	// INSERT + up to three clear-other-actives UPDATEs must be atomic —
	// a crash partway through would leave multiple agents marked active
	// for the same category, violating the "at most one active per
	// category" invariant the rest of the daemon relies on.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin upsert agent: %w", err)
	}
	defer tx.Rollback() // no-op after successful Commit
	if _, err := tx.Exec(`
		INSERT INTO agents (id, name, cli, prompt, instructions, cli_flags, is_default_pr, is_default_issue, is_default_dev, created_at, issue_prompt, issue_instructions, implement_prompt, implement_instructions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, cli=excluded.cli, prompt=excluded.prompt,
			instructions=excluded.instructions, cli_flags=excluded.cli_flags,
			is_default_pr=excluded.is_default_pr,
			is_default_issue=excluded.is_default_issue,
			is_default_dev=excluded.is_default_dev,
			issue_prompt=excluded.issue_prompt, issue_instructions=excluded.issue_instructions,
			implement_prompt=excluded.implement_prompt, implement_instructions=excluded.implement_instructions
	`, a.ID, a.Name, a.CLI, a.Prompt, a.Instructions, a.CLIFlags,
		boolToInt(a.IsDefaultPR), boolToInt(a.IsDefaultIssue), boolToInt(a.IsDefaultDev),
		a.CreatedAt.UTC().Format(time.RFC3339),
		a.IssuePrompt, a.IssueInstructions,
		a.ImplementPrompt, a.ImplementInstructions,
	); err != nil {
		return fmt.Errorf("store: upsert agent: %w", err)
	}
	// Clear the flag only on the categories this upsert claimed, so
	// activating a triage-only preset doesn't nuke the user's PR-review
	// active agent as a side effect.
	if a.IsDefaultPR {
		if _, err := tx.Exec("UPDATE agents SET is_default_pr=0 WHERE id != ?", a.ID); err != nil {
			return fmt.Errorf("store: clear default pr: %w", err)
		}
	}
	if a.IsDefaultIssue {
		if _, err := tx.Exec("UPDATE agents SET is_default_issue=0 WHERE id != ?", a.ID); err != nil {
			return fmt.Errorf("store: clear default issue: %w", err)
		}
	}
	if a.IsDefaultDev {
		if _, err := tx.Exec("UPDATE agents SET is_default_dev=0 WHERE id != ?", a.ID); err != nil {
			return fmt.Errorf("store: clear default dev: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit upsert agent: %w", err)
	}
	return nil
}

func (s *Store) DeleteAgent(id string) error {
	_, err := s.db.Exec("DELETE FROM agents WHERE id = ?", id)
	return err
}

// DefaultAgentFor returns the single agent whose flag for `category` is set,
// or (nil, sql.ErrNoRows) when no agent is active for that category. The
// pipeline callers treat "no active agent" as "use the built-in default
// template", matching the existing zero-config behaviour.
func (s *Store) DefaultAgentFor(category AgentCategory) (*Agent, error) {
	col := category.defaultColumn()
	if col == "" {
		return nil, fmt.Errorf("store: unknown agent category %q", category)
	}
	row := s.db.QueryRow(
		"SELECT " + agentColumns + " FROM agents WHERE " + col + "=1 LIMIT 1",
	)
	return scanAgent(row)
}

type agentScanner interface {
	Scan(dest ...any) error
}

func scanAgent(s agentScanner) (*Agent, error) {
	var a Agent
	var isDefaultPR, isDefaultIssue, isDefaultDev int
	var createdAt string
	if err := s.Scan(&a.ID, &a.Name, &a.CLI, &a.Prompt, &a.Instructions,
		&a.CLIFlags,
		&isDefaultPR, &isDefaultIssue, &isDefaultDev,
		&createdAt,
		&a.IssuePrompt, &a.IssueInstructions,
		&a.ImplementPrompt, &a.ImplementInstructions); err != nil {
		return nil, err
	}
	a.IsDefaultPR = isDefaultPR == 1
	a.IsDefaultIssue = isDefaultIssue == 1
	a.IsDefaultDev = isDefaultDev == 1
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
