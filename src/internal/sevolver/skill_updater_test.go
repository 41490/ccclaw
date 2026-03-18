package sevolver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateSkillMetaUpdatesLastUsedAndUseCount(t *testing.T) {
	dir := t.TempDir()
	skillFile := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(skillFile, []byte(`---
name: my-skill
description: test
keywords: [foo]
---
content
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateSkillMeta(skillFile, SkillHit{
		SkillPath: "skills/L1/my-skill/CLAUDE.md",
		Date:      time.Date(2026, 3, 12, 12, 0, 0, 0, time.Local),
	}); err != nil {
		t.Fatalf("UpdateSkillMeta failed: %v", err)
	}

	payload, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{
		"last_used: \"2026-03-12\"",
		"use_count: 1",
		"status: active",
		"gap_signals: []",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}

	if err := UpdateSkillMeta(skillFile, SkillHit{
		SkillPath: "skills/L1/my-skill/CLAUDE.md",
		Date:      time.Date(2026, 3, 12, 18, 0, 0, 0, time.Local),
	}); err != nil {
		t.Fatalf("repeat UpdateSkillMeta failed: %v", err)
	}
	payload, err = os.ReadFile(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(payload), "use_count: 1") != 1 {
		t.Fatalf("repeat same-day hit should not re-increment: %q", string(payload))
	}
}

func TestProcessSkillLifecycleMarksDormantAndArchivesDeprecated(t *testing.T) {
	kbDir := t.TempDir()
	activePath := filepath.Join(kbDir, "skills", "L1", "active", "CLAUDE.md")
	oldPath := filepath.Join(kbDir, "skills", "L2", "legacy", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(activePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activePath, []byte(`---
name: active
last_used: "2026-02-26"
status: active
---
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte(`---
name: legacy
last_used: "2026-02-01"
status: dormant
---
`), 0o644); err != nil {
		t.Fatal(err)
	}

	actions, err := processSkillLifecycle(kbDir, time.Date(2026, 3, 12, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("processSkillLifecycle failed: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 lifecycle actions, got %#v", actions)
	}
	payload, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "status: dormant") {
		t.Fatalf("expected dormant status, got %q", string(payload))
	}
	archivedPath := filepath.Join(kbDir, "skills", "archived", "L2", "legacy", "CLAUDE.md")
	if _, err := os.Stat(archivedPath); err != nil {
		t.Fatalf("expected archived skill file, got %v", err)
	}
}
