package recall

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ContextInput 包含生成 context.md 所需的全部输入。
type ContextInput struct {
	Nodes       []MemoryNode
	TaskTags    []string
	Cold        bool
	TriggerDesc string // "recall:issue=77" 或 "cold:journal-timer"
	KBRoot      string
	MaxLines    int
	Now         time.Time
}

// BuildContext 生成 kb/context.md 的完整文本内容。
func BuildContext(input ContextInput) string {
	if input.MaxLines <= 0 {
		input.MaxLines = 256
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	trigger := input.TriggerDesc
	if trigger == "" {
		if input.Cold {
			trigger = "cold:journal-timer"
		} else {
			trigger = "recall"
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<!-- ccclaw:context:generated:%s -->\n", input.Now.Format("2006-01-02T15:04")))
	sb.WriteString(fmt.Sprintf("<!-- ccclaw:context:trigger:%s -->\n\n", trigger))

	if input.Cold {
		sb.WriteString("# 本周上下文摘要（dreamclearner 冷启动）\n\n")
	} else {
		sb.WriteString("# 当前上下文\n\n")
	}

	// 按 score 降序排列，只取 score ≥ 0.3 的节点
	candidates := make([]MemoryNode, 0, len(input.Nodes))
	for _, n := range input.Nodes {
		if n.Score >= 0.3 && n.Status != "archived" && n.Status != "candidate" {
			candidates = append(candidates, n)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	skills, journals, designs := groupNodes(candidates)

	if input.Cold {
		writeSection(&sb, "Top-4 技能（use_count + recency，无 tag 权重）", skills, 4, input.KBRoot)
	} else {
		writeSection(&sb, "相关技能", skills, 6, input.KBRoot)
	}
	writeSection(&sb, "近期关键事件", journals, 5, input.KBRoot)
	writeSection(&sb, "活跃设计", designs, 3, input.KBRoot)

	sb.WriteString("\n<!-- ccclaw:context:end -->\n")

	result := sb.String()
	return truncateLines(result, input.MaxLines)
}

func groupNodes(nodes []MemoryNode) (skills, journals, designs []MemoryNode) {
	for _, n := range nodes {
		switch {
		case strings.HasPrefix(n.ID, "skill:"):
			skills = append(skills, n)
		case strings.HasPrefix(n.ID, "journal:"):
			journals = append(journals, n)
		default:
			designs = append(designs, n)
		}
	}
	return
}

func writeSection(sb *strings.Builder, title string, nodes []MemoryNode, limit int, kbRoot string) {
	if len(nodes) == 0 {
		return
	}
	sb.WriteString(fmt.Sprintf("## %s\n", title))
	count := limit
	if count > len(nodes) {
		count = len(nodes)
	}
	for _, n := range nodes[:count] {
		rel := RelativeID(n.ID)
		label := nodeLabel(n.ID)
		linkPath := filepath.ToSlash(rel)
		sb.WriteString(fmt.Sprintf("- [%s](%s) score=%.2f tags=[%s]\n",
			label, linkPath, n.Score, strings.Join(n.Tags, ",")))
	}
	sb.WriteString("\n")
}

// RelativeID returns the path portion of an id (e.g. "skill:foo/bar" → "foo/bar").
func RelativeID(id string) string {
	if idx := strings.Index(id, ":"); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// nodeLabel extracts a human-readable label from an ID.
func nodeLabel(id string) string {
	rel := RelativeID(id)
	base := filepath.Base(rel)
	dir := filepath.Base(filepath.Dir(rel))
	if base == "CLAUDE.md" {
		return dir
	}
	return strings.TrimSuffix(base, ".md")
}

func truncateLines(content string, maxLines int) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	endMarker := "<!-- ccclaw:context:end -->"
	truncated := lines[:maxLines-2]
	truncated = append(truncated, "<!-- context truncated -->", endMarker)
	return strings.Join(truncated, "\n")
}
