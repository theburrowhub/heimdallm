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
	ID           string `json:"id"`
	Name         string `json:"name"`
	CLI          string `json:"cli"`          // claude | gemini | codex (overrides global)
	Prompt       string `json:"prompt"`       // full template (advanced); empty = use instructions
	Instructions string `json:"instructions"` // what to focus on (simple mode)
	CLIFlags     string `json:"cli_flags"`    // extra CLI args
	// IsDefaultPR marks the agent the PR-review pipeline uses. At most one
	// agent carries it at a time.
	IsDefaultPR bool      `json:"is_default_pr"`
	CreatedAt   time.Time `json:"created_at"`
}

// IsDefault reports whether the agent is the active review profile.
func (a Agent) IsDefault() bool {
	return a.IsDefaultPR
}

// AgentCategory identifies which pipeline a default-agent lookup is for. The
// string values match the JSON naming used over the HTTP API and the Flutter
// client, so they can be threaded through without per-layer translation.
type AgentCategory string

const (
	AgentCategoryPR AgentCategory = "pr"
)

// defaultColumn returns the SQL column name backing the given category flag.
// Centralising the mapping here keeps the category-per-flag relationship in
// one place and gives the store-layer filters a single source of truth.
func (c AgentCategory) defaultColumn() string {
	switch c {
	case AgentCategoryPR:
		return "is_default_pr"
	}
	return ""
}

const agentColumns = "id, name, cli, prompt, instructions, cli_flags, is_default_pr, created_at"

func (s *Store) ListAgents() ([]*Agent, error) {
	// Active agent first, then alphabetical — how the UI presents them.
	rows, err := s.db.Query(
		"SELECT " + agentColumns + " FROM agents " +
			"ORDER BY is_default_pr DESC, name ASC",
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
	// INSERT + the clear-other-actives UPDATE must be atomic — a crash
	// partway through would leave multiple agents marked active, violating
	// the "at most one active agent" invariant the rest of the daemon
	// relies on.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin upsert agent: %w", err)
	}
	defer tx.Rollback() // no-op after successful Commit
	if _, err := tx.Exec(`
		INSERT INTO agents (id, name, cli, prompt, instructions, cli_flags, is_default_pr, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, cli=excluded.cli, prompt=excluded.prompt,
			instructions=excluded.instructions, cli_flags=excluded.cli_flags,
			is_default_pr=excluded.is_default_pr
	`, a.ID, a.Name, a.CLI, a.Prompt, a.Instructions, a.CLIFlags,
		boolToInt(a.IsDefaultPR),
		a.CreatedAt.UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("store: upsert agent: %w", err)
	}
	if a.IsDefaultPR {
		if _, err := tx.Exec("UPDATE agents SET is_default_pr=0 WHERE id != ?", a.ID); err != nil {
			return fmt.Errorf("store: clear default pr: %w", err)
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
	var isDefaultPR int
	var createdAt string
	if err := s.Scan(&a.ID, &a.Name, &a.CLI, &a.Prompt, &a.Instructions,
		&a.CLIFlags, &isDefaultPR, &createdAt); err != nil {
		return nil, err
	}
	a.IsDefaultPR = isDefaultPR == 1
	a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
