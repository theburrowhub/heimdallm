package github

import "strings"

// TokenRouter selects the appropriate GitHub token based on the repository
// owner (org). Organisations that block classic PATs can use a fine-grained
// token while others fall back to the default.
type TokenRouter struct {
	defaultToken string
	orgTokens    map[string]string // lowercase org → token
}

// NewTokenRouter builds a router. orgTokens maps org slugs (case-insensitive)
// to their dedicated tokens. Repos in unlisted orgs use defaultToken.
func NewTokenRouter(defaultToken string, orgTokens map[string]string) *TokenRouter {
	norm := make(map[string]string, len(orgTokens))
	for k, v := range orgTokens {
		norm[strings.ToLower(k)] = v
	}
	return &TokenRouter{defaultToken: defaultToken, orgTokens: norm}
}

// ForRepo returns the token for an "org/repo" string.
func (r *TokenRouter) ForRepo(repo string) string {
	if i := strings.IndexByte(repo, '/'); i > 0 {
		if tok, ok := r.orgTokens[strings.ToLower(repo[:i])]; ok {
			return tok
		}
	}
	return r.defaultToken
}

// ForOrg returns the token for an org slug.
func (r *TokenRouter) ForOrg(org string) string {
	if tok, ok := r.orgTokens[strings.ToLower(org)]; ok {
		return tok
	}
	return r.defaultToken
}

// Default returns the fallback token.
func (r *TokenRouter) Default() string {
	return r.defaultToken
}
