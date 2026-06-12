package autonomous

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeMerger struct {
	called bool
	method string
}

func (f *fakeMerger) MergePR(repo string, number int, method string) error {
	f.called = true
	f.method = method
	return nil
}

func TestMergeGate_DisabledSkips(t *testing.T) {
	m := &fakeMerger{}
	g := NewMergeGate(m, false, "squash")
	res, err := g.Run(context.Background(), "a/b", 7)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.called {
		t.Errorf("merge must NOT be called when disabled")
	}
	if res != MergeSkippedDisabled {
		t.Errorf("want MergeSkippedDisabled, got %v", res)
	}
}

func TestMergeGate_EnabledMerges(t *testing.T) {
	m := &fakeMerger{}
	g := NewMergeGate(m, true, "squash")
	res, err := g.Run(context.Background(), "a/b", 7)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !m.called || m.method != "squash" {
		t.Errorf("want squash merge call, got called=%v method=%q", m.called, m.method)
	}
	if res != MergeDone {
		t.Errorf("want MergeDone, got %v", res)
	}
}

func TestMergeGate_DefaultsMethod(t *testing.T) {
	m := &fakeMerger{}
	g := NewMergeGate(m, true, "")
	if _, err := g.Run(context.Background(), "a/b", 7); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if m.method != "squash" {
		t.Errorf("empty method should default to squash, got %q", m.method)
	}
}

func TestMergeGate_PropagatesError(t *testing.T) {
	g := NewMergeGate(errMerger{}, true, "squash")
	_, err := g.Run(context.Background(), "a/b", 7)
	if err == nil {
		t.Fatalf("want error propagated from merger")
	}
	if !strings.Contains(err.Error(), "autonomous: merge gate:") {
		t.Errorf("error must be wrapped, got %q", err.Error())
	}
}

type errMerger struct{}

func (errMerger) MergePR(string, int, string) error { return errors.New("405 not mergeable") }
