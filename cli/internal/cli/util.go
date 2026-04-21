package cli

// truncate returns s truncated to at most maxLen runes, appending "..." if
// truncated. This avoids corrupting multi-byte UTF-8 characters that
// byte-indexed slicing would cause.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "\u2026"
}
