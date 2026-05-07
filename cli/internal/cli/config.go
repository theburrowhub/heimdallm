package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	cfgSectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C3AED"))
	cfgKeyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
)

func newConfigCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the daemon's running configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			cfg, err := c.GetConfig()
			if err != nil {
				return fmt.Errorf("fetching config: %w", err)
			}

			if jsonFlag {
				b, err := json.MarshalIndent(cfg, "", "  ")
				if err != nil {
					return fmt.Errorf("formatting config: %w", err)
				}
				fmt.Println(string(b))
				return nil
			}

			printHumanConfig(cfg)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Output raw JSON")
	return cmd
}

func printHumanConfig(cfg map[string]any) {
	type section struct {
		name  string
		lines []string
	}
	sections := []section{
		{"Server", cfgServerLines(cfg)},
		{"Repositories", cfgRepoLines(cfg)},
		{"AI", cfgAILines(cfg)},
		{"Organizations", cfgOrgLines(cfg)},
		{"Issue Tracking", cfgIssueTrackingLines(cfg)},
		{"Discovery", cfgDiscoveryLines(cfg)},
	}

	first := true
	for _, s := range sections {
		if len(s.lines) == 0 {
			continue
		}
		if !first {
			fmt.Println()
		}
		first = false
		fmt.Println(cfgSectionStyle.Render(s.name))
		fmt.Println(cfgSectionStyle.Render(strings.Repeat("─", len(s.name))))
		for _, l := range s.lines {
			fmt.Println(l)
		}
	}
}

// --- section builders ---

func cfgServerLines(cfg map[string]any) []string {
	var out []string
	out = cfgKV(out, "Port", cfg["server_port"])
	out = cfgKV(out, "Poll interval", cfg["poll_interval"])
	out = cfgKV(out, "Retention days", cfg["retention_days"])
	out = cfgKV(out, "Activity log", cfg["activity_log_enabled"])
	out = cfgKV(out, "Log retention days", cfg["activity_log_retention_days"])
	return out
}

func cfgRepoLines(cfg map[string]any) []string {
	var out []string
	out = cfgStringList(out, "Monitored", cfg["repositories"])
	out = cfgStringList(out, "Local dir base", cfg["local_dir_base"])

	overrides, _ := cfg["repo_overrides"].(map[string]any)
	detected, _ := cfg["local_dirs_detected"].(map[string]any)

	all := make(map[string]bool)
	for k := range overrides {
		all[k] = true
	}
	for k := range detected {
		all[k] = true
	}
	repos := make([]string, 0, len(all))
	for k := range all {
		repos = append(repos, k)
	}
	sort.Strings(repos)

	for _, repo := range repos {
		var sub []string
		if ro, ok := overrides[repo].(map[string]any); ok {
			sub = cfgSubKV(sub, "Primary", ro["primary"])
			sub = cfgSubKV(sub, "Fallback", ro["fallback"])
			sub = cfgSubKV(sub, "Review mode", ro["review_mode"])
			sub = cfgSubKV(sub, "Local dir", ro["local_dir"])
			sub = cfgSubList(sub, "PR reviewers", ro["pr_reviewers"])
			sub = cfgSubKV(sub, "PR assignee", ro["pr_assignee"])
			sub = cfgSubList(sub, "PR labels", ro["pr_labels"])
			sub = cfgSubKV(sub, "PR draft", ro["pr_draft"])
		}
		if d, ok := detected[repo]; ok && !cfgEmpty(d) {
			sub = cfgSubKV(sub, "Local dir (auto)", d)
		}
		if len(sub) > 0 {
			out = append(out, fmt.Sprintf("  %s", cfgKeyStyle.Render(repo)))
			out = append(out, sub...)
		}
	}
	return out
}

func cfgAILines(cfg map[string]any) []string {
	var out []string
	out = cfgKV(out, "Primary", cfg["ai_primary"])
	out = cfgKV(out, "Fallback", cfg["ai_fallback"])
	out = cfgKV(out, "Review mode", cfg["review_mode"])
	out = cfgKV(out, "Issue prompt", cfg["issue_prompt"])
	out = cfgKV(out, "Implement prompt", cfg["implement_prompt"])

	if pm, ok := cfg["pr_metadata"].(map[string]any); ok {
		out = cfgStringList(out, "PR reviewers", pm["reviewers"])
		out = cfgStringList(out, "PR labels", pm["labels"])
		out = cfgKV(out, "PR assignee", pm["pr_assignee"])
		out = cfgKV(out, "PR draft", pm["pr_draft"])
	}

	agents, _ := cfg["agent_configs"].(map[string]any)
	for _, name := range cfgSortedKeys(agents) {
		ac, ok := agents[name].(map[string]any)
		if !ok {
			continue
		}
		var sub []string
		sub = cfgSubKV(sub, "Model", ac["model"])
		sub = cfgSubKV(sub, "Max turns", ac["max_turns"])
		sub = cfgSubKV(sub, "Approval mode", ac["approval_mode"])
		sub = cfgSubKV(sub, "Extra flags", ac["extra_flags"])
		sub = cfgSubKV(sub, "Prompt", ac["prompt"])
		sub = cfgSubKV(sub, "Effort", ac["effort"])
		sub = cfgSubKV(sub, "Permission mode", ac["permission_mode"])
		if len(sub) > 0 {
			out = append(out, fmt.Sprintf("  %s", cfgKeyStyle.Render("Agent: "+name)))
			out = append(out, sub...)
		}
	}
	return out
}

func cfgOrgLines(cfg map[string]any) []string {
	overrides, _ := cfg["org_overrides"].(map[string]any)
	if len(overrides) == 0 {
		return nil
	}
	var out []string
	for _, org := range cfgSortedKeys(overrides) {
		ov, ok := overrides[org].(map[string]any)
		if !ok {
			continue
		}
		var sub []string
		sub = cfgSubKV(sub, "Primary", ov["primary"])
		sub = cfgSubKV(sub, "Fallback", ov["fallback"])
		sub = cfgSubKV(sub, "Review mode", ov["review_mode"])
		sub = cfgSubKV(sub, "Prompt", ov["prompt"])
		sub = cfgSubKV(sub, "Issue prompt", ov["issue_prompt"])
		sub = cfgSubKV(sub, "Implement prompt", ov["implement_prompt"])
		sub = cfgSubKV(sub, "Local dir", ov["local_dir"])
		sub = cfgSubKV(sub, "Triage owner", ov["triage_owner"])
		sub = cfgSubKV(sub, "Clone dir", ov["clone_dir"])
		sub = cfgSubKV(sub, "Auto-promote triage", ov["auto_promote_triage"])
		sub = cfgSubKV(sub, "Auto-promote refinement", ov["auto_promote_refinement"])
		sub = cfgSubKV(sub, "Generate PR description", ov["generate_pr_description"])
		sub = cfgSubList(sub, "PR reviewers", ov["pr_reviewers"])
		sub = cfgSubKV(sub, "PR assignee", ov["pr_assignee"])
		sub = cfgSubList(sub, "PR labels", ov["pr_labels"])
		sub = cfgSubKV(sub, "PR draft", ov["pr_draft"])
		if it, ok := ov["issue_tracking"].(map[string]any); ok {
			sub = cfgIssueTrackingSubLines(sub, it)
		}
		if len(sub) > 0 {
			out = append(out, fmt.Sprintf("  %s", cfgKeyStyle.Render(org)))
			out = append(out, sub...)
		}
	}
	return out
}

type cfgIssueTrackingLine struct {
	label string
	key   string
	list  bool
}

var cfgIssueTrackingLineSpecs = []cfgIssueTrackingLine{
	{label: "Enabled", key: "enabled"},
	{label: "Develop enabled", key: "develop_enabled"},
	{label: "Filter mode", key: "filter_mode"},
	{label: "Default action", key: "default_action"},
	{label: "Organizations", key: "organizations", list: true},
	{label: "Assignees", key: "assignees", list: true},
	{label: "Develop labels", key: "develop_labels", list: true},
	{label: "Review only labels", key: "review_only_labels", list: true},
	{label: "Skip labels", key: "skip_labels", list: true},
	{label: "Blocked labels", key: "blocked_labels", list: true},
	{label: "Promote to label", key: "promote_to_label"},
}

func cfgIssueTrackingSubLines(out []string, m map[string]any) []string {
	for _, spec := range cfgIssueTrackingLineSpecs {
		if spec.list {
			out = cfgSubList(out, spec.label, m[spec.key])
		} else if _, ok := m[spec.key]; ok {
			out = cfgSubKVPresent(out, spec.label, m[spec.key])
		} else {
			out = cfgSubKV(out, spec.label, m[spec.key])
		}
	}
	return out
}

func cfgIssueTrackingLines(cfg map[string]any) []string {
	m, ok := cfg["issue_tracking"].(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for _, spec := range cfgIssueTrackingLineSpecs {
		if spec.list {
			out = cfgStringList(out, spec.label, m[spec.key])
		} else {
			out = cfgKV(out, spec.label, m[spec.key])
		}
	}
	return out
}

func cfgDiscoveryLines(cfg map[string]any) []string {
	var out []string
	out = cfgStringList(out, "Non-monitored", cfg["non_monitored"])
	return out
}

// --- formatting helpers ---

func cfgEmpty(val any) bool {
	if val == nil {
		return true
	}
	switch v := val.(type) {
	case string:
		return v == ""
	case float64:
		return v == 0
	case bool:
		return !v
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func cfgFmtVal(val any) string {
	if f, ok := val.(float64); ok && f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", val)
}

func cfgKV(lines []string, key string, val any) []string {
	if cfgEmpty(val) {
		return lines
	}
	padded := fmt.Sprintf("%-22s", key+":")
	return append(lines, fmt.Sprintf("  %s %s", cfgKeyStyle.Render(padded), cfgFmtVal(val)))
}

func cfgSubKV(lines []string, key string, val any) []string {
	if cfgEmpty(val) {
		return lines
	}
	padded := fmt.Sprintf("%-20s", key+":")
	return append(lines, fmt.Sprintf("    %s %s", cfgKeyStyle.Render(padded), cfgFmtVal(val)))
}

func cfgSubKVPresent(lines []string, key string, val any) []string {
	if val == nil {
		return lines
	}
	if s, ok := val.(string); ok && s == "" {
		return lines
	}
	padded := fmt.Sprintf("%-20s", key+":")
	return append(lines, fmt.Sprintf("    %s %s", cfgKeyStyle.Render(padded), cfgFmtVal(val)))
}

func cfgStringList(lines []string, key string, val any) []string {
	arr, ok := val.([]any)
	if !ok || len(arr) == 0 {
		return lines
	}
	lines = append(lines, fmt.Sprintf("  %s", cfgKeyStyle.Render(key+":")))
	for _, item := range arr {
		lines = append(lines, fmt.Sprintf("    • %v", item))
	}
	return lines
}

func cfgSubList(lines []string, key string, val any) []string {
	arr, ok := val.([]any)
	if !ok || len(arr) == 0 {
		return lines
	}
	lines = append(lines, fmt.Sprintf("    %s", cfgKeyStyle.Render(key+":")))
	for _, item := range arr {
		lines = append(lines, fmt.Sprintf("      • %v", item))
	}
	return lines
}

func cfgSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
