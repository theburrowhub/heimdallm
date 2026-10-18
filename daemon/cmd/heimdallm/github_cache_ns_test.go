package main

import "testing"

func TestGitHubCacheNamespace(t *testing.T) {
	a, b := githubCacheNamespace("token-a"), githubCacheNamespace("token-b")
	if a == b || len(a) != 16 {
		t.Fatalf("a=%q b=%q", a, b)
	}
	if githubCacheNamespace("token-a") != a {
		t.Fatal("namespace must be stable for a token")
	}
}
