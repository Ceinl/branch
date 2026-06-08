package main

import (
	"os"
	"path/filepath"
	"testing"
)

func testApp(t *testing.T) *app {
	t.Helper()
	root := t.TempDir()
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return &app{root: root, rootReal: rootReal, token: "test-token"}
}

func TestResolveRejectsPathEscape(t *testing.T) {
	a := testApp(t)
	outside := filepath.Join(filepath.Dir(a.root), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	if _, _, err := a.resolveExisting("../outside.md"); err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

func TestResolveRejectsEscapingSymlink(t *testing.T) {
	a := testApp(t)
	outside := filepath.Join(filepath.Dir(a.root), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	link := filepath.Join(a.root, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, _, err := a.resolveExisting("link.md"); err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
}

func TestAtomicWriteCreatesFile(t *testing.T) {
	a := testApp(t)
	path, _, err := a.resolveWritable("notes/doc.md")
	if err == nil {
		t.Fatal("expected missing parent to fail before creating directories")
	}

	if err := os.Mkdir(filepath.Join(a.root, "notes"), 0755); err != nil {
		t.Fatal(err)
	}
	path, _, err = a.resolveWritable("notes/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("# Hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Hello\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}
