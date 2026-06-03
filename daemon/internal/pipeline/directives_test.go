package pipeline

import "testing"

func TestParseDirective(t *testing.T) {
	const bot = "heimdallm"
	cases := []struct {
		name              string
		body              string
		wantOK            bool
		wantVerb, wantPay string
	}{
		{"remember", "@heimdallm remember: unauth endpoints are fine", true, "remember", "unauth endpoints are fine"},
		{"remember scoped", "@heimdallm remember(repo): rule X", true, "remember", "rule X"},
		{"forget", "@heimdallm forget: 12", true, "forget", "12"},
		{"list", "@heimdallm list", true, "list", ""},
		{"case-insensitive mention+verb", "@HeimdallM REMEMBER: y", true, "remember", "y"},
		{"leading whitespace", "   @heimdallm list", true, "list", ""},
		{"not a directive", "looks good to me", false, "", ""},
		{"mention without verb", "@heimdallm hello there", false, "", ""},
		{"remember without payload", "@heimdallm remember:", false, "", ""},
		{"unknown verb", "@heimdallm frobnicate: x", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, _, payload, ok := parseDirective(tc.body, bot)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if ok && (verb != tc.wantVerb || payload != tc.wantPay) {
				t.Fatalf("got verb=%q payload=%q; want verb=%q payload=%q", verb, payload, tc.wantVerb, tc.wantPay)
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
