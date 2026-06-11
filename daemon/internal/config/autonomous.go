package config

// AutonomousConfig configures the fully-unattended end-to-end mode. It is
// resolved per repo via AutonomousForRepo (repo > org > global). Safety is
// delegated to the circuit-breaker family (see CircuitBreakerForRepo); there
// is intentionally no per-day task cap.
type AutonomousConfig struct {
	Enabled         bool   `toml:"enabled"`           // master switch / kill-switch
	AutoMerge       bool   `toml:"auto_merge"`        // merge gate; built but OFF by default
	MergeMethod     string `toml:"merge_method"`      // squash|merge|rebase (used only when AutoMerge)
	TakeOthersTasks bool   `toml:"take_others_tasks"` // enable cascade bucket 3
	ReassignOnTake  bool   `toml:"reassign_on_take"`  // add bot as assignee on others' tasks
	DevMaxTurns     int    `toml:"dev_max_turns"`     // 0 = no practical cap for development
	DevEffort       string `toml:"dev_effort"`        // agent effort for development
	DevTimeout      string `toml:"dev_timeout"`       // generous development timeout (e.g. "45m")

	Orgs  map[string]AutonomousOverride `toml:"orgs"`  // per-org overrides ([autonomous.orgs."org"])
	Repos map[string]AutonomousOverride `toml:"repos"` // per-repo overrides ([autonomous.repos."org/repo"])
}

// AutonomousOverride is the per-org / per-repo override shape. Pointer fields
// are nil when unset (inherit); set fields replace the inherited value.
type AutonomousOverride struct {
	Enabled         *bool  `toml:"enabled,omitempty"`
	AutoMerge       *bool  `toml:"auto_merge,omitempty"`
	MergeMethod     string `toml:"merge_method,omitempty"`
	TakeOthersTasks *bool  `toml:"take_others_tasks,omitempty"`
	ReassignOnTake  *bool  `toml:"reassign_on_take,omitempty"`
	DevMaxTurns     *int   `toml:"dev_max_turns,omitempty"`
	DevEffort       string `toml:"dev_effort,omitempty"`
	DevTimeout      string `toml:"dev_timeout,omitempty"`
}

// AutonomousForRepo resolves autonomous config for a repo: repo > org > global.
func (c *Config) AutonomousForRepo(repo string) AutonomousConfig {
	out := c.Autonomous
	if org := repoOrg(repo); org != "" && c.Autonomous.Orgs != nil {
		if o, ok := c.Autonomous.Orgs[org]; ok {
			applyAutonomousOverride(&out, o)
		}
	}
	if c.Autonomous.Repos != nil {
		if r, ok := c.Autonomous.Repos[repo]; ok {
			applyAutonomousOverride(&out, r)
		}
	}
	return out
}

func applyAutonomousOverride(out *AutonomousConfig, o AutonomousOverride) {
	if o.Enabled != nil {
		out.Enabled = *o.Enabled
	}
	if o.AutoMerge != nil {
		out.AutoMerge = *o.AutoMerge
	}
	if o.MergeMethod != "" {
		out.MergeMethod = o.MergeMethod
	}
	if o.TakeOthersTasks != nil {
		out.TakeOthersTasks = *o.TakeOthersTasks
	}
	if o.ReassignOnTake != nil {
		out.ReassignOnTake = *o.ReassignOnTake
	}
	if o.DevMaxTurns != nil {
		out.DevMaxTurns = *o.DevMaxTurns
	}
	if o.DevEffort != "" {
		out.DevEffort = o.DevEffort
	}
	if o.DevTimeout != "" {
		out.DevTimeout = o.DevTimeout
	}
}

// applyAutonomousDefaults fills zero-value scalars with safe defaults.
func (c *Config) applyAutonomousDefaults() {
	if c.Autonomous.MergeMethod == "" {
		c.Autonomous.MergeMethod = "squash"
	}
	if c.Autonomous.DevEffort == "" {
		c.Autonomous.DevEffort = "high"
	}
	if c.Autonomous.DevTimeout == "" {
		c.Autonomous.DevTimeout = "45m"
	}
}
