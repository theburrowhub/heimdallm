package config

import (
	"testing"
	"time"
)

func TestFirstSeenMap_Marshal_Unmarshal(t *testing.T) {
	m := FirstSeenMap{
		"org/a": time.Unix(1000, 0),
		"org/b": time.Unix(2000, 0),
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := ParseFirstSeen(raw)
	if err != nil {
		t.Fatal(err)
	}

	if !decoded["org/a"].Equal(time.Unix(1000, 0)) ||
		!decoded["org/b"].Equal(time.Unix(2000, 0)) {
		t.Fatalf("roundtrip failed: %+v", decoded)
	}
}

func TestFirstSeenMap_Mark_IsIdempotent(t *testing.T) {
	m := FirstSeenMap{}
	m.Mark("a/b", time.Unix(1000, 0))
	m.Mark("a/b", time.Unix(2000, 0)) // second call ignored

	got, ok := m["a/b"]
	if !ok {
		t.Fatal("expected key to exist")
	}
	if !got.Equal(time.Unix(1000, 0)) {
		t.Fatalf("second Mark should be a no-op: got %v", got)
	}
}

func TestParseFirstSeen_EmptyAndInvalid(t *testing.T) {
	m, err := ParseFirstSeen("")
	if err != nil || len(m) != 0 {
		t.Fatalf("empty should decode to empty map, got %v / %v", m, err)
	}
	if _, err := ParseFirstSeen("not json"); err == nil {
		t.Fatal("invalid JSON should return error")
	}
}
