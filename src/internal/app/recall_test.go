package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/adapters/github"
	"github.com/41490/ccclaw/internal/config"
	"github.com/41490/ccclaw/internal/memory"
)

func TestRecallColdWritesContextMD(t *testing.T) {
	kbRoot := t.TempDir()
	// 创建最小 kb 结构
	skillDir := filepath.Join(kbRoot, "skills", "L1", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "CLAUDE.md"), []byte(`---
name: my-skill
keywords: [git]
use_count: 5
last_used: "2026-03-14"
status: active
---
`), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			KB: config.KBConfig{ContextMaxLines: 256},
		},
		memRoot:  kbRoot,
		ghCache:  map[string]*github.Client{},
		memCache: map[string]*memory.Index{},
	}

	err := rt.Recall(RecallOptions{Cold: true, Now: time.Date(2026, 3, 17, 1, 2, 0, 0, time.UTC)}, os.Stdout)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}

	contextPath := filepath.Join(kbRoot, "context.md")
	data, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("context.md not created: %v", err)
	}
	if !strings.Contains(string(data), "dreamclearner") {
		t.Errorf("cold context.md should contain 'dreamclearner'")
	}
}
