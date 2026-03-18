package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildUsesFrontmatterMetadataForMatching(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "skills", "git-conflict-resolve", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	content := `---
name: git-conflict-resolve
description: 处理 git 合并冲突的最小操作卡
trigger: 当遇到 merge conflict 时使用
keywords: [git, merge, conflict, resolve]
---
# 不应覆盖 frontmatter 名称

这里是很长的正文细节。
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文档失败: %v", err)
	}

	idx, err := Build(root)
	if err != nil {
		t.Fatalf("构建索引失败: %v", err)
	}
	matches := idx.Match([]string{"git conflict"}, 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %#v", matches)
	}
	doc := matches[0]
	if doc.Title != "git-conflict-resolve" {
		t.Fatalf("unexpected title: %#v", doc)
	}
	if doc.Summary != "处理 git 合并冲突的最小操作卡" {
		t.Fatalf("unexpected summary: %#v", doc)
	}
	if got := strings.Join(doc.Keywords, ","); got != "git,merge,conflict,resolve" {
		t.Fatalf("unexpected keywords: %q", got)
	}
}

func TestBuildFallsBackToBodySummaryWithoutFrontmatter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "skills", "summary.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	content := `# L1 汇总

本目录记录高频技巧。

- git
- shell
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文档失败: %v", err)
	}

	idx, err := Build(root)
	if err != nil {
		t.Fatalf("构建索引失败: %v", err)
	}
	matches := idx.Match([]string{"高频 技巧"}, 5)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %#v", matches)
	}
	if matches[0].Title != "L1 汇总" {
		t.Fatalf("unexpected title: %#v", matches[0])
	}
	if matches[0].Summary != "本目录记录高频技巧。" {
		t.Fatalf("unexpected summary: %#v", matches[0])
	}
}

func TestBuildExcludesDeprecatedSkills(t *testing.T) {
	root := t.TempDir()
	activePath := filepath.Join(root, "skills", "L1", "foo", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(activePath), 0o755); err != nil {
		t.Fatalf("创建活动 skill 目录失败: %v", err)
	}
	if err := os.WriteFile(activePath, []byte(`---
name: foo
description: 活动 skill
keywords: [foo]
---
# foo
`), 0o644); err != nil {
		t.Fatalf("写入活动 skill 失败: %v", err)
	}

	archivedPath := filepath.Join(root, "skills", "archived", "L1", "old", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(archivedPath), 0o755); err != nil {
		t.Fatalf("创建 archived skill 目录失败: %v", err)
	}
	if err := os.WriteFile(archivedPath, []byte(`---
name: old
description: 已归档 skill
keywords: [old]
---
# old
`), 0o644); err != nil {
		t.Fatalf("写入 archived skill 失败: %v", err)
	}

	idx, err := Build(root)
	if err != nil {
		t.Fatalf("构建索引失败: %v", err)
	}

	if hits := idx.Match([]string{"foo"}, 10); len(hits) != 1 {
		t.Fatalf("expected active skill to remain indexed, got %#v", hits)
	}
	if hits := idx.Match([]string{"old"}, 10); len(hits) != 0 {
		t.Fatalf("archived skills should be excluded, got %#v", hits)
	}
}
