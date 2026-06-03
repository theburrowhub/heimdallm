package pipeline

import "testing"

func TestParseDirective(t *testing.T) {
	const bot = "heimdallm"
	cases := []struct {
		name                        string
		body                        string
		wantOK                      bool
		wantVerb, wantScope, wantPay string
	}{
		{"remember", "@heimdallm remember: unauth endpoints are fine", true, "remember", "repo", "unauth endpoints are fine"},
		{"remember scoped", "@heimdallm remember(repo): rule X", true, "remember", "repo", "rule X"},
		{"forget", "@heimdallm forget: 12", true, "forget", "repo", "12"},
		{"list", "@heimdallm list", true, "list", "repo", ""},
		{"case-insensitive mention+verb", "@HeimdallM REMEMBER: y", true, "remember", "repo", "y"},
		{"leading whitespace", "   @heimdallm list", true, "list", "repo", ""},
		{"not a directive", "looks good to me", false, "", "", ""},
		{"mention without verb", "@heimdallm hello there", false, "", "", ""},
		{"remember without payload", "@heimdallm remember:", false, "", "", ""},
		{"unknown verb", "@heimdallm frobnicate: x", false, "", "", ""},
		{"remember scoped non-default", "@heimdallm remember(global): rule X", true, "remember", "global", "rule X"},
		{"list scoped", "@heimdallm list(global)", true, "list", "global", ""},
		{"no word boundary", "@heimdallmremember: x", false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, scope, payload, ok := parseDirective(tc.body, bot)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if ok && (verb != tc.wantVerb || scope != tc.wantScope || payload != tc.wantPay) {
				t.Fatalf("got verb=%q scope=%q payload=%q; want verb=%q scope=%q payload=%q",
					verb, scope, payload, tc.wantVerb, tc.wantScope, tc.wantPay)
			}
		})
	}
}

func TestAuthorAllowed(t *testing.T) {
	allow := []string{"Alice", "@bob"}
	if !authorAllowed(allow, "alice") || !authorAllowed(allow, "@BOB") {
		t.Error("alice/bob should be allowed")
	}
	if authorAllowed(allow, "mallory") || authorAllowed(allow, "") || authorAllowed(nil, "alice") {
		t.Error("unexpected allow")
	}
}
