package recall

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBuildContextIncludesHighScoreNodes(t *testing.T) {
	now := time.Date(2026, 3, 17, 10, 0, 0, 0, time.UTC)
	nodes := []MemoryNode{
		{ID: "skill:skills/L1/release-flow/CLAUDE.md", Tags: []string{"release"}, UseCount: 15, LastUsed: "2026-03-16", Status: "active", Score: 0.91},
		{ID: "skill:skills/L1/debug-stream/CLAUDE.md", Tags: []string{"debug", "stream"}, UseCount: 3, LastUsed: "2026-03-10", Status: "active", Score: 0.62},
		{ID: "journal:journal/26/03/16/log.md", Tags: []string{"release"}, UseCount: 0, LastUsed: "2026-03-16", Status: "active", Score: 0.40},
	}
	input := ContextInput{
		Nodes:       nodes,
		TaskTags:    []string{"release"},
		Cold:        false,
		TriggerDesc: "recall:issue=77",
		KBRoot:      "/tmp/kb",
		MaxLines:    256,
		Now:         now,
	}
	result := BuildContext(input)

	if !strings.Contains(result, "<!-- ccclaw:context:generated:") {
		t.Error("missing generated header")
	}
	if !strings.Contains(result, "release-flow") {
		t.Error("missing release-flow skill")
	}
	if !strings.Contains(result, "<!-- ccclaw:context:end -->") {
		t.Error("missing end marker")
	}
}

func TestBuildContextColdLabel(t *testing.T) {
	now := time.Date(2026, 3, 17, 1, 2, 0, 0, time.UTC)
	input := ContextInput{
		Nodes:       nil,
		Cold:        true,
		TriggerDesc: "cold:journal-timer",
		KBRoot:      "/tmp/kb",
		MaxLines:    256,
		Now:         now,
	}
	result := BuildContext(input)
	if !strings.Contains(result, "dreamclearner") {
		t.Error("cold context should mention dreamclearner")
	}
}

func TestBuildContextRespectsMaxLines(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	var nodes []MemoryNode
	for i := 0; i < 200; i++ {
		nodes = append(nodes, MemoryNode{
			ID:       fmt.Sprintf("skill:skills/L1/skill-%d/CLAUDE.md", i),
			Tags:     []string{"foo"},
			UseCount: 1, LastUsed: "2026-03-16", Status: "active", Score: 0.7,
		})
	}
	input := ContextInput{
		Nodes:    nodes,
		MaxLines: 50,
		Now:      now,
	}
	result := BuildContext(input)
	lines := strings.Split(result, "\n")
	if len(lines) > 55 { // 允许少量超出（header + end marker）
		t.Fatalf("expected ≤55 lines, got %d", len(lines))
	}
}
