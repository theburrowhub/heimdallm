package main

import "testing"

func TestDefaultVersion(t *testing.T) {
	t.Parallel()

	if version != "dev" {
		t.Fatalf("default version = %q, want dev", version)
	}
}
