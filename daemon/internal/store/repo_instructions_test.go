package store_test

import (
	"testing"

	"github.com/heimdallm/daemon/internal/store"
)

func TestRepoInstructions_AddListDelete(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	id1, err := s.AddRepoInstruction("org/repo", "unauthenticated endpoints are intentional", "alice", 1001)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.AddRepoInstruction("org/repo", "ignore TODO comments", "alice", 1002); err != nil {
		t.Fatalf("add2: %v", err)
	}
	if _, err := s.AddRepoInstruction("org/other", "other repo rule", "bob", 1003); err != nil {
		t.Fatalf("add3: %v", err)
	}

	items, err := s.ListRepoInstructions("org/repo")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].ID != id1 || items[0].Instruction != "unauthenticated endpoints are intentional" || items[0].Author != "alice" || items[0].CommentID != 1001 {
		t.Fatalf("unexpected first item: %+v", items[0])
	}

	ok, err := s.DeleteRepoInstruction("org/repo", id1)
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	ok, err = s.DeleteRepoInstruction("org/repo", id1)
	if err != nil || ok {
		t.Fatalf("re-delete: want ok=false err=nil, got ok=%v err=%v", ok, err)
	}
	items, _ = s.ListRepoInstructions("org/repo")
	if len(items) != 1 {
		t.Fatalf("want 1 after delete, got %d", len(items))
	}
}

func TestDirectiveMarks_Idempotent(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	done, err := s.DirectiveProcessed(42)
	if err != nil || done {
		t.Fatalf("want not processed: done=%v err=%v", done, err)
	}
	if err := s.MarkDirectiveProcessed(42, "remember"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := s.MarkDirectiveProcessed(42, "remember"); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	done, err = s.DirectiveProcessed(42)
	if err != nil || !done {
		t.Fatalf("want processed: done=%v err=%v", done, err)
	}
}
