package recall

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadNodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.jsonl")

	nodes := []MemoryNode{
		{ID: "skill:skills/L1/git-sign/CLAUDE.md", Tags: []string{"git", "commit"}, UseCount: 5, LastUsed: "2026-03-14", Status: "active", Score: 0.82},
		{ID: "journal:journal/26/03/16/log.md", Tags: []string{"release"}, UseCount: 0, LastUsed: "2026-03-16", Status: "active", Score: 0.40},
	}

	if err := SaveNodes(path, nodes); err != nil {
		t.Fatalf("SaveNodes failed: %v", err)
	}

	loaded, err := LoadNodes(path)
	if err != nil {
		t.Fatalf("LoadNodes failed: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(loaded))
	}
	if loaded[0].ID != nodes[0].ID {
		t.Fatalf("expected ID %q, got %q", nodes[0].ID, loaded[0].ID)
	}
	if loaded[0].UseCount != 5 {
		t.Fatalf("expected UseCount=5, got %d", loaded[0].UseCount)
	}
}

func TestLoadNodesReturnsEmptyForMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.jsonl")
	nodes, err := LoadNodes(path)
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected empty slice, got %d nodes", len(nodes))
	}
}

func TestSaveNodesIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.jsonl")

	if err := SaveNodes(path, []MemoryNode{{ID: "skill:a", Tags: []string{"x"}, UseCount: 1, LastUsed: "2026-03-01", Status: "active"}}); err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	if err := SaveNodes(path, []MemoryNode{{ID: "skill:b", Tags: []string{"y"}, UseCount: 2, LastUsed: "2026-03-10", Status: "active"}}); err != nil {
		t.Fatalf("second save failed: %v", err)
	}
	nodes, err := LoadNodes(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "skill:b" {
		t.Fatalf("expected overwrite, got %+v", nodes)
	}
}

func TestRebuildFromKBRoot(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "L1", "git-sign")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "CLAUDE.md"), []byte(`---
name: git-sign
keywords: [git, commit, gpg]
use_count: 3
last_used: "2026-03-10"
status: active
---
# git-sign
`), 0o644); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(root, "journal", "26", "03")
	if err := os.MkdirAll(journalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, "2026.03.16.user.ccclaw_log.md"), []byte("# journal\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes, err := RebuildNodes(root)
	if err != nil {
		t.Fatalf("RebuildNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected ≥2 nodes, got %d: %v", len(nodes), nodes)
	}
	var skillNode *MemoryNode
	for i := range nodes {
		if nodes[i].ID == "skill:skills/L1/git-sign/CLAUDE.md" {
			skillNode = &nodes[i]
		}
	}
	if skillNode == nil {
		t.Fatal("skill node not found")
	}
	if skillNode.UseCount != 3 {
		t.Fatalf("expected UseCount=3, got %d", skillNode.UseCount)
	}
}
