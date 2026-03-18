package sevolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectCandidatesGeneratesDraft(t *testing.T) {
	kbRoot := t.TempDir()
	journalDir := filepath.Join(kbRoot, "journal")

	// 创建 3 个包含相同操作模式的 journal 文件（14天内）
	for i, day := range []string{"2026-03-10", "2026-03-12", "2026-03-14"} {
		dir := filepath.Join(journalDir, "26", fmt.Sprintf("%02d", 3))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("## %s\n\n发布 ccclaw release\n执行 release ccclaw\n完成 release 操作\n", day)
		path := filepath.Join(dir, fmt.Sprintf("2026.03.%02d.user.ccclaw_log.md", 10+i*2))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	candidatesDir := filepath.Join(kbRoot, "skills", "candidates")
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	created, err := DetectCandidates(journalDir, candidatesDir, now)
	if err != nil {
		t.Fatalf("DetectCandidates failed: %v", err)
	}
	if created == 0 {
		t.Fatal("expected at least 1 candidate draft to be created")
	}

	entries, err := os.ReadDir(candidatesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no candidate directories created")
	}
	var draftContent string
	for _, e := range entries {
		if e.IsDir() {
			data, err := os.ReadFile(filepath.Join(candidatesDir, e.Name(), "CLAUDE.md"))
			if err == nil {
				draftContent = string(data)
				break
			}
		}
	}
	if !strings.Contains(draftContent, "status: candidate") {
		t.Errorf("draft missing 'status: candidate': %s", draftContent)
	}
	if !strings.Contains(draftContent, "⚠️") {
		t.Errorf("draft missing warning about no auto-install: %s", draftContent)
	}
}
