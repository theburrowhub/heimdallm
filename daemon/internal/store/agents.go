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
type Agent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CLI          string    `json:"cli"`          // claude | gemini | codex (overrides global)
	Prompt       string    `json:"prompt"`       // full template (advanced); empty = use instructions
	Instructions string    `json:"instructions"` // what to focus on (simple mode)
	CLIFlags     string    `json:"cli_flags"`    // extra CLI args
	IsDefault    bool      `json:"is_default"`
	CreatedAt    time.Time `json:"created_at"`

	// Issue triage prompt customization (mirrors Prompt/Instructions for PRs).
	IssuePrompt       string `json:"issue_prompt"`       // full custom template for issue triage
	IssueInstructions string `json:"issue_instructions"` // plain text injected into default issue template
}

const agentColumns = "id, name, cli, prompt, instructions, cli_flags, is_default, created_at, issue_prompt, issue_instructions"

func (s *Store) ListAgents() ([]*Agent, error) {
	rows, err := s.db.Query(
		"SELECT "+agentColumns+" FROM agents ORDER BY is_default DESC, name ASC",
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
	_, err := s.db.Exec(`
		INSERT INTO agents (id, name, cli, prompt, instructions, cli_flags, is_default, created_at, issue_prompt, issue_instructions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, cli=excluded.cli, prompt=excluded.prompt,
			instructions=excluded.instructions, cli_flags=excluded.cli_flags,
			is_default=excluded.is_default,
			issue_prompt=excluded.issue_prompt, issue_instructions=excluded.issue_instructions
	`, a.ID, a.Name, a.CLI, a.Prompt, a.Instructions, a.CLIFlags,
		boolToInt(a.IsDefault), a.CreatedAt.UTC().Format(time.RFC3339),
		a.IssuePrompt, a.IssueInstructions,
	)
	if err != nil {
		return fmt.Errorf("store: upsert agent: %w", err)
	}
	if a.IsDefault {
		_, err = s.db.Exec("UPDATE agents SET is_default=0 WHERE id != ?", a.ID)
		if err != nil {
			return fmt.Errorf("store: clear default: %w", err)
		}
	}
	return nil
}

func (s *Store) DeleteAgent(id string) error {
	_, err := s.db.Exec("DELETE FROM agents WHERE id = ?", id)
	return err
}

func (s *Store) DefaultAgent() (*Agent, error) {
	row := s.db.QueryRow(
		"SELECT "+agentColumns+" FROM agents WHERE is_default=1 LIMIT 1",
	)
	return scanAgent(row)
}

type agentScanner interface {
	Scan(dest ...any) error
}

func scanAgent(s agentScanner) (*Agent, error) {
	var a Agent
	var isDefault int
	var createdAt string
	if err := s.Scan(&a.ID, &a.Name, &a.CLI, &a.Prompt, &a.Instructions,
		&a.CLIFlags, &isDefault, &createdAt,
		&a.IssuePrompt, &a.IssueInstructions); err != nil {
		return nil, err
	}
	a.IsDefault = isDefault == 1
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
