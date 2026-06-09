package main

import (
	"testing"
)

func TestHistoryTreeBranchesAfterRestore(t *testing.T) {
	store := newHistoryStore(t.TempDir(), true)
	if !store.enabled {
		t.Skip("git not available")
	}
	user := authUser{Name: "Tester", Email: "tester@example.com"}

	v1, err := store.recordSave("notes/a.md", "one\n", user, "client-a")
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	// A different client must not coalesce into v1.
	v2, err := store.recordSave("notes/a.md", "two\n", user, "client-b")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if v1 == v2 {
		t.Fatal("expected distinct commits for distinct saves")
	}

	// Saving identical content must not create a new node.
	again, err := store.recordSave("notes/a.md", "two\n", user, "client-c")
	if err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	if again != v2 {
		t.Fatalf("unchanged content created new commit %s", again)
	}

	// Rapid saves by the same client coalesce into one node.
	v3, err := store.recordSave("notes/a.md", "three\n", user, "client-b")
	if err != nil {
		t.Fatalf("third save: %v", err)
	}
	nodes, err := store.list("notes/a.md")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected v2 coalesced away, got %d nodes", len(nodes))
	}

	// Restore v1, then save: history must branch, not rewind.
	content, err := store.restore("notes/a.md", v1)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if content != "one\n" {
		t.Fatalf("restored content = %q", content)
	}
	v4, err := store.recordSave("notes/a.md", "four\n", user, "client-d")
	if err != nil {
		t.Fatalf("save after restore: %v", err)
	}

	nodes, err = store.list("notes/a.md")
	if err != nil {
		t.Fatalf("list after branch: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes (v1, v3, v4), got %d", len(nodes))
	}
	byID := map[string]historyNode{}
	for _, node := range nodes {
		byID[node.ID] = node
	}
	if byID[v4].Parent != v1 {
		t.Fatalf("v4 parent = %s, want %s", byID[v4].Parent, v1)
	}
	if byID[v3].Parent != v1 {
		t.Fatalf("v3 parent = %s, want %s", byID[v3].Parent, v1)
	}
	if !byID[v4].Current {
		t.Fatal("v4 should be the current version")
	}

	// Old content is still retrievable, and foreign ids are rejected.
	old, err := store.contentAt("notes/a.md", v3)
	if err != nil || old != "three\n" {
		t.Fatalf("contentAt(v3) = %q, %v", old, err)
	}
	if _, err := store.contentAt("notes/other.md", v3); err == nil {
		t.Fatal("expected error for id from another file")
	}
}
