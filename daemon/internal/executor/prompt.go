package executor

import "fmt"

const maxDiffBytes = 32 * 1024 // 32KB ~ 8k tokens

// BuildPrompt constructs the prompt sent to the AI CLI.
func BuildPrompt(title, author, diff string) string {
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n... (diff truncated)"
	}
	return fmt.Sprintf(`You are a senior software engineer performing a pull request code review.

PR Title: %s
Author: %s

Diff:
%s

Review the above diff and respond with ONLY valid JSON in this exact format (no markdown, no explanation):
{
  "summary": "brief overall assessment",
  "issues": [
    {"file": "filename", "line": 0, "description": "issue description", "severity": "low|medium|high"}
  ],
  "suggestions": ["suggestion 1", "suggestion 2"],
  "severity": "low|medium|high"
}

The top-level "severity" is the highest severity found. If no issues, return empty arrays and severity "low".`, title, author, diff)
}
