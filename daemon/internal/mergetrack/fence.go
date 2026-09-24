package mergetrack

import "strings"

// Fence markers for text that came out of a repository — file paths, branch
// names, the PR title. Everything inside is data, never instructions.
const (
	untrustedRepoContentFenceOpen  = "── BEGIN UNTRUSTED REPOSITORY CONTENT ──"
	untrustedRepoContentFenceClose = "── END UNTRUSTED REPOSITORY CONTENT ──"
)

// untrustedFenceKeyword is the ASCII phrase that marks the fence. Sanitising
// against the keyword rather than the full decorated fence means homoglyph
// dashes (── vs -- vs ══) or quirky spacing cannot forge a terminator. It is
// all-ASCII so case-folding never shifts byte offsets.
const untrustedFenceKeyword = "untrusted repository content"

// fenceUntrustedRepoContent wraps repository-derived text in the untrusted
// fence, sanitising the body first so it cannot forge a terminator.
func fenceUntrustedRepoContent(body string) string {
	return untrustedRepoContentFenceOpen + "\n" +
		sanitiseUntrustedFreeText(body) + "\n" +
		untrustedRepoContentFenceClose
}

// sanitiseUntrustedFreeText is a fence-terminator defense, not general
// prompt-injection prevention: it only neutralises the keyword that delimits
// the untrusted region. Other techniques are addressed by the trust-boundary
// wording around the fence in buildConflictPrompt.
func sanitiseUntrustedFreeText(s string) string {
	for {
		idx := indexCaseInsensitiveASCII(s, untrustedFenceKeyword)
		if idx < 0 {
			return s
		}
		s = s[:idx] + "[fence redacted]" + s[idx+len(untrustedFenceKeyword):]
	}
}

// indexCaseInsensitiveASCII returns the byte offset of the first
// case-insensitive match of needle inside haystack. needle MUST be ASCII: the
// fold is byte-wise, so offsets stay valid around multibyte runes.
func indexCaseInsensitiveASCII(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	lowerNeedle := strings.ToLower(needle)
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			h := haystack[i+j]
			if h >= 'A' && h <= 'Z' {
				h += 'a' - 'A'
			}
			if h != lowerNeedle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
