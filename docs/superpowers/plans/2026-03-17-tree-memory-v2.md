# 树型记忆 v2 实施计划

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现置信度驱动的动态 kb 上下文系统——`ccclaw recall` 命令在任务完成后生成仅含相关记忆的 `kb/context.md`，取代当前全量 CLAUDE.md 加载模式。

**Architecture:** 新增 `src/internal/recall/` 包负责节点评分与 context.md 生成；`ccclaw recall` cobra 命令装配参数与调用；journal timer 末尾自动触发冷启动 recall；sevolver 新增候选技能识别。

**Tech Stack:** Go 1.25 (module `github.com/41490/ccclaw`), Cobra CLI, `encoding/json`, `gopkg.in/yaml.v3`, GitHub CLI (`gh`), systemd oneshot timers.

---

## 文件变更总览

| 操作 | 文件 | 说明 |
|------|------|------|
| 重命名 | `src/dist/kb/skills/deprecated/` → `archived/` | 目录迁移 |
| 修改 | `src/internal/sevolver/skill_updater.go` | deprecated→archived 常量+函数名+路径 |
| 修改 | `src/internal/memory/index.go` | 跳过 archived 目录 |
| 修改 | `src/internal/config/config.go` | 新增 KBConfig + ContextMaxLines |
| 新建 | `src/internal/recall/nodes.go` | MemoryNode 数据模型，load/save/rebuild |
| 新建 | `src/internal/recall/scorer.go` | 置信度评分 + 生命周期分类 |
| 新建 | `src/internal/recall/context.go` | context.md 内容生成器 |
| 新建 | `src/internal/recall/nodes_test.go` | nodes 单测 |
| 新建 | `src/internal/recall/scorer_test.go` | scorer 单测 |
| 新建 | `src/internal/recall/context_test.go` | context 单测 |
| 新建 | `src/internal/app/recall.go` | Runtime.Recall() 方法 |
| 新建 | `src/cmd/ccclaw/recall.go` | cobra recall 命令装配 |
| 修改 | `src/internal/app/runtime.go` | Journal() 末尾追加冷启动 recall |
| 新建 | `src/internal/sevolver/candidate_detector.go` | 候选技能识别 |
| 修改 | `src/internal/sevolver/sevolver.go` | 追加 candidateScanWindowDays + 调用候选检测 |
| 修改 | `src/dist/kb/CLAUDE.md` | 精简根索引 |
| 更新 | `src/dist/.gitignore` 或根 `.gitignore` | 排除 context.md + nodes.jsonl |

---

## Chunk 1: deprecated → archived 迁移

**Files:**
- Rename: `src/dist/kb/skills/deprecated/` → `src/dist/kb/skills/archived/`
- Modify: `src/internal/sevolver/skill_updater.go:19-20,51,187-201`
- Modify: `src/internal/memory/index.go:59-62`
- Modify: `src/internal/sevolver/skill_updater_test.go` (如有 "deprecated" 路径引用)
- Modify: `src/internal/memory/index_test.go` (如有 "deprecated" 路径引用)

- [ ] **Step 1.1: 重命名 src/dist/kb/skills/deprecated**

```bash
cd /opt/src/ccclaw/src/dist/kb/skills
ls deprecated/ 2>/dev/null && git mv deprecated archived || mkdir -p archived
```

若目录不存在则直接创建：
```bash
mkdir -p /opt/src/ccclaw/src/dist/kb/skills/archived
```

- [ ] **Step 1.2: 更新 skill_updater.go 常量**

文件：`src/internal/sevolver/skill_updater.go`

旧：
```go
skillStatusDormant    = "dormant"
skillStatusDeprecated = "deprecated"
```

新：
```go
skillStatusDormant   = "dormant"
skillStatusArchived  = "archived"
```

同时更新 `isDeprecatedSkillDir`（第 49 行附近）：

旧：
```go
func isDeprecatedSkillDir(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.HasSuffix(clean, "/skills/deprecated") || strings.Contains(clean, "/skills/deprecated/")
}
```

新：
```go
func isArchivedSkillDir(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.HasSuffix(clean, "/skills/archived") || strings.Contains(clean, "/skills/archived/")
}
```

更新调用方（全文搜索替换 `isDeprecatedSkillDir` → `isArchivedSkillDir`）。

- [ ] **Step 1.3: 更新 skill_updater.go ArchiveDeprecated、MarkDeprecated 与生命周期动作**

**1. 重命名 `ArchiveDeprecated` → `ArchiveSkill`，更新内部路径**（第 186 行附近，包级函数，非方法）：

旧：
```go
func ArchiveDeprecated(kbDir, skillFile string) (string, error) {
    ...
    target := filepath.Join(skillsRoot, "deprecated", rel)
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        return "", fmt.Errorf("创建 deprecated 目录失败: %w", err)
    }
    if err := os.Rename(skillFile, target); err != nil {
        return "", fmt.Errorf("迁移 deprecated skill 失败: %w", err)
    }
    ...
}
```

新：
```go
func ArchiveSkill(kbDir, skillFile string) (string, error) {
    ...
    target := filepath.Join(skillsRoot, "archived", rel)
    if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
        return "", fmt.Errorf("创建 archived 目录失败: %w", err)
    }
    if err := os.Rename(skillFile, target); err != nil {
        return "", fmt.Errorf("迁移 archived skill 失败: %w", err)
    }
    ...
}
```

**2. 重命名 `MarkDeprecated` → `MarkArchived`，更新内部状态写入**（第 103 行附近）：

旧：
```go
func MarkDeprecated(skillFile string) error {
    ...
    meta.Status = skillStatusDeprecated
    ...
}
```

新：
```go
func MarkArchived(skillFile string) error {
    ...
    meta.Status = skillStatusArchived
    ...
}
```

**3. 更新 `processSkillLifecycle` 中的调用**（第 238 行附近）：

旧：
```go
case inactiveDays >= 28:
    if err := MarkDeprecated(path); err != nil {
        return err
    }
    target, err := ArchiveDeprecated(kbDir, path)
    if err != nil {
        return err
    }
    actions = append(actions, skillLifecycleAction{Path: target, Status: skillStatusDeprecated})
```

新：
```go
case inactiveDays >= 28:
    if err := MarkArchived(path); err != nil {
        return err
    }
    target, err := ArchiveSkill(kbDir, path)
    if err != nil {
        return err
    }
    actions = append(actions, skillLifecycleAction{Path: target, Status: skillStatusArchived})
```

全局搜索替换以确保无遗漏：
```bash
grep -rn "ArchiveDeprecated\|MarkDeprecated\|skillStatusDeprecated" /opt/src/ccclaw/src/
```

- [ ] **Step 1.4: 更新 memory/index.go**

文件：`src/internal/memory/index.go:59-62`

旧：
```go
func isDeprecatedSkillsDir(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.HasSuffix(clean, "/skills/deprecated") || clean == "skills/deprecated"
}
```

新：
```go
func isArchivedSkillsDir(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.HasSuffix(clean, "/skills/archived") || clean == "skills/archived"
}
```

更新调用方（第 38 行）：
```go
if isArchivedSkillsDir(path) {
```

- [ ] **Step 1.5: 更新测试文件中的 "deprecated" 路径引用**

```bash
grep -rn "deprecated" /opt/src/ccclaw/src/internal/sevolver/ /opt/src/ccclaw/src/internal/memory/ --include="*_test.go"
```

将测试中所有 `skills/deprecated` 路径替换为 `skills/archived`。

- [ ] **Step 1.6: 运行受影响包的测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/sevolver/... ./internal/memory/... -v 2>&1 | tail -30
```

期望：所有测试 PASS。

- [ ] **Step 1.7: Commit**

```bash
cd /opt/src/ccclaw
git add src/dist/kb/skills/ src/internal/sevolver/skill_updater.go src/internal/memory/index.go
git add src/internal/sevolver/skill_updater_test.go src/internal/memory/index_test.go
git commit -m "refactor(#77): rename deprecated→archived in skills lifecycle"
```

---

## Chunk 2: Config — context_max_lines

**Files:**
- Modify: `src/internal/config/config.go`
- Modify: `src/internal/config/config_test.go`

- [ ] **Step 2.1: 写失败测试**

在 `src/internal/config/config_test.go` 中新增（使用满足 Validate() 的完整 TOML fixture）：

```go
// minimalValidTOML 返回一份满足所有必填字段的最小 config TOML。
func minimalValidTOML(extraSections ...string) string {
	base := `
[github]
control_repo = "test/repo"

[paths]
app_dir    = "/tmp/ccclaw-test"
home_repo  = "/tmp/ccclaw-repo"
var_dir    = "/tmp/ccclaw-var"
log_dir    = "/tmp/ccclaw-log"
kb_dir     = "/tmp/ccclaw-kb"
env_file   = "/tmp/ccclaw.env"

[executor]
command = ["claude"]
`
	for _, s := range extraSections {
		base += "\n" + s
	}
	return base
}

func TestKBConfigContextMaxLinesDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(minimalValidTOML()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.KB.ContextMaxLines != 256 {
		t.Fatalf("expected ContextMaxLines=256, got %d", cfg.KB.ContextMaxLines)
	}
}

func TestKBConfigContextMaxLinesConfigurable(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(minimalValidTOML(`
[kb]
context_max_lines = 128
`)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.KB.ContextMaxLines != 128 {
		t.Fatalf("expected ContextMaxLines=128, got %d", cfg.KB.ContextMaxLines)
	}
}
```

> 若 `config_test.go` 中已存在相同签名的 `minimalValidTOML` helper，复用它并追加 `extraSections` 参数，或使用现有 helper 的等效 TOML。

- [ ] **Step 2.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/config/... -run TestKBConfig -v 2>&1 | tail -20
```

期望：FAIL（字段不存在）。

- [ ] **Step 2.3: 实现 KBConfig**

在 `src/internal/config/config.go` 的 `Config` struct 末尾追加字段：

```go
type Config struct {
	DefaultTarget string          `mapstructure:"default_target" toml:"default_target"`
	GitHub        GitHubConfig    `mapstructure:"github" toml:"github"`
	Paths         PathsConfig     `mapstructure:"paths" toml:"paths"`
	Executor      ExecutorConfig  `mapstructure:"executor" toml:"executor"`
	Scheduler     SchedulerConfig `mapstructure:"scheduler" toml:"scheduler"`
	Approval      ApprovalConfig  `mapstructure:"approval" toml:"approval"`
	KB            KBConfig        `mapstructure:"kb" toml:"kb"`          // 新增
	Targets       []TargetConfig  `mapstructure:"targets" toml:"targets"`
}
```

新增 `KBConfig` struct（紧接在 `ApprovalConfig` 定义之后）：

```go
type KBConfig struct {
	ContextMaxLines int `mapstructure:"context_max_lines" toml:"context_max_lines"`
}
```

在 `Load()` 函数中，找到 `v.SetDefault` 调用集群（第 135–154 行附近），在其末尾追加：

```go
v.SetDefault("kb.context_max_lines", 256)
```

这与现有所有 `v.SetDefault` 调用的位置和方式保持一致，确保在 `v.UnmarshalExact` 前生效。

- [ ] **Step 2.4: 运行确认测试通过**

```bash
cd /opt/src/ccclaw/src && go test ./internal/config/... -run TestKBConfig -v 2>&1 | tail -20
```

期望：PASS。

- [ ] **Step 2.5: 运行全量 config 测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/config/... -v 2>&1 | tail -30
```

期望：全部 PASS。

- [ ] **Step 2.6: Commit**

```bash
cd /opt/src/ccclaw
git add src/internal/config/config.go src/internal/config/config_test.go
git commit -m "feat(#77): add KBConfig.ContextMaxLines (default 256)"
```

---

## Chunk 3: recall 内部包

**Files:**
- Create: `src/internal/recall/nodes.go`
- Create: `src/internal/recall/scorer.go`
- Create: `src/internal/recall/context.go`
- Create: `src/internal/recall/nodes_test.go`
- Create: `src/internal/recall/scorer_test.go`
- Create: `src/internal/recall/context_test.go`

### Task 3.1: nodes.go — 数据模型

- [ ] **Step 3.1.1: 写失败测试**

创建 `src/internal/recall/nodes_test.go`：

```go
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

	// 第一次写入
	if err := SaveNodes(path, []MemoryNode{{ID: "skill:a", Tags: []string{"x"}, UseCount: 1, LastUsed: "2026-03-01", Status: "active"}}); err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	// 第二次写入（覆写）
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
	// 创建一个带 frontmatter 的 skill 文件
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
	// 创建一个 journal 文件
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
```

- [ ] **Step 3.1.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -v 2>&1 | tail -20
```

期望：编译失败（包不存在）。

- [ ] **Step 3.1.3: 实现 nodes.go**

创建 `src/internal/recall/nodes.go`：

```go
package recall

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MemoryNode 表示 kb/ 下一条记忆条目的置信度元数据。
type MemoryNode struct {
	ID       string   `json:"id"`        // "<type>:<relative_path_from_kb_root>"
	Tags     []string `json:"tags"`      // 标签，用于 tag_match 评分
	UseCount int      `json:"use_count"` // 被命中次数
	LastUsed string   `json:"last_used"` // YYYY-MM-DD
	Status   string   `json:"status"`    // active|dormant|archived|candidate
	Score    float64  `json:"score"`     // 上次 recall 计算的缓存值
}

// LoadNodes 从 JSONL 文件加载节点列表。文件不存在时返回空列表（不报错）。
func LoadNodes(path string) ([]MemoryNode, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开 nodes.jsonl 失败: %w", err)
	}
	defer f.Close()

	var nodes []MemoryNode
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var n MemoryNode
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			return nil, fmt.Errorf("解析 nodes.jsonl 行失败: %w", err)
		}
		nodes = append(nodes, n)
	}
	return nodes, scanner.Err()
}

// SaveNodes 原子覆写 nodes.jsonl（临时文件 + rename）。
func SaveNodes(path string, nodes []MemoryNode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 memory 目录失败: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	for _, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("序列化节点失败: %w", err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("写入节点失败: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	return os.Rename(tmp, path)
}

// RebuildNodes 扫描 kbRoot 下所有记忆文件重建节点列表。
// skill 节点从 frontmatter 恢复 use_count/last_used；其他节点 use_count 归零。
func RebuildNodes(kbRoot string) ([]MemoryNode, error) {
	var nodes []MemoryNode
	dirs := []struct {
		subdir   string
		nodeType string
	}{
		{"skills/L1", "skill"},
		{"skills/L2", "skill"},
		{"journal", "journal"},
		{"designs", "design"},
		{"assay", "assay"},
	}
	for _, d := range dirs {
		dir := filepath.Join(kbRoot, d.subdir)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".md" {
				return nil
			}
			rel, err := filepath.Rel(kbRoot, path)
			if err != nil {
				return err
			}
			id := d.nodeType + ":" + filepath.ToSlash(rel)
			node := buildNode(id, path, d.nodeType)
			nodes = append(nodes, node)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("扫描 %s 失败: %w", d.subdir, err)
		}
	}
	return nodes, nil
}

type skillFrontmatter struct {
	Keywords []string `yaml:"keywords"`
	UseCount int      `yaml:"use_count"`
	LastUsed string   `yaml:"last_used"`
	Status   string   `yaml:"status"`
}

func buildNode(id, path, nodeType string) MemoryNode {
	node := MemoryNode{
		ID:     id,
		Status: "active",
	}
	data, err := os.ReadFile(path)
	if err != nil {
		node.Tags = tagsFromPath(id)
		return node
	}
	content := string(data)
	if nodeType == "skill" {
		if fm, ok := parseFrontmatter(content); ok {
			node.UseCount = fm.UseCount
			node.LastUsed = fm.LastUsed
			if fm.Status != "" {
				node.Status = fm.Status
			}
			node.Tags = fm.Keywords
		}
	}
	if len(node.Tags) == 0 {
		node.Tags = tagsFromPath(id)
	}
	if node.LastUsed == "" {
		info, err := os.Stat(path)
		if err == nil {
			node.LastUsed = info.ModTime().Format("2006-01-02")
		}
	}
	return node
}

func parseFrontmatter(content string) (*skillFrontmatter, bool) {
	if !strings.HasPrefix(content, "---") {
		return nil, false
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return nil, false
	}
	return &fm, true
}

func tagsFromPath(id string) []string {
	// id = "type:path/components/file.md"
	parts := strings.Split(id, ":")
	if len(parts) < 2 {
		return nil
	}
	segments := strings.Split(filepath.ToSlash(parts[1]), "/")
	var tags []string
	for _, seg := range segments {
		seg = strings.TrimSuffix(seg, ".md")
		seg = strings.TrimSuffix(seg, ".CLAUDE")
		if seg == "" || seg == "skills" || seg == "L1" || seg == "L2" || seg == "journal" || seg == "designs" || seg == "assay" {
			continue
		}
		tags = append(tags, seg)
	}
	return tags
}
```

- [ ] **Step 3.1.4: 运行节点测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -run "TestSave|TestLoad|TestRebuild" -v 2>&1 | tail -30
```

期望：PASS。

### Task 3.2: scorer.go — 置信度评分

- [ ] **Step 3.2.1: 写失败测试**

创建 `src/internal/recall/scorer_test.go`：

```go
package recall

import (
	"testing"
	"time"
)

func TestScoreActiveSkillHighUsage(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	input := ScoreInput{
		UseCount: 10,
		LastUsed: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), // 2天前
		TaskTags: []string{"git", "commit"},
		NodeTags: []string{"git", "commit", "gpg"},
		Now:      now,
	}
	score := Score(input)
	// use_count_norm = min(10/20, 1) = 0.5 → *0.4 = 0.20
	// recency = 1 - 2/90 = 0.978 → *0.4 = 0.391
	// tag_match = 2/2 = 1.0 → *0.2 = 0.20
	// total ≈ 0.791
	if score < 0.7 || score > 0.9 {
		t.Fatalf("expected score in [0.7,0.9], got %f", score)
	}
}

func TestScoreColdStartHasZeroTagMatch(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	withTags := ScoreInput{
		UseCount: 5,
		LastUsed: time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC),
		TaskTags: []string{"git"},
		NodeTags: []string{"git"},
		Now:      now,
	}
	cold := ScoreInput{
		UseCount: 5,
		LastUsed: time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC),
		TaskTags: nil, // cold start: no task tags
		NodeTags: []string{"git"},
		Now:      now,
	}
	scoreWithTags := Score(withTags)
	scoreCold := Score(cold)
	if scoreCold >= scoreWithTags {
		t.Fatalf("cold score (%f) should be less than tagged score (%f)", scoreCold, scoreWithTags)
	}
}

func TestClassifyActive(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	lastUsed := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	got := Classify(0.7, lastUsed, now)
	if got != "active" {
		t.Fatalf("expected active, got %q", got)
	}
}

func TestClassifyDormant(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	lastUsed := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	got := Classify(0.45, lastUsed, now)
	if got != "dormant" {
		t.Fatalf("expected dormant, got %q", got)
	}
}

func TestClassifyArchived(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	oldDate := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC) // >180天前
	got := Classify(0.1, oldDate, now)
	if got != "archived" {
		t.Fatalf("expected archived, got %q", got)
	}
}

func TestClassifyDormantNotArchivedIfRecent(t *testing.T) {
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) // 44天前，score低但<180天
	got := Classify(0.1, recent, now)
	if got != "dormant" {
		t.Fatalf("expected dormant (not archived), got %q", got)
	}
}
```

- [ ] **Step 3.2.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -run TestScore -run TestClassify -v 2>&1 | tail -20
```

期望：FAIL（函数未定义）。

- [ ] **Step 3.2.3: 实现 scorer.go**

创建 `src/internal/recall/scorer.go`：

```go
package recall

import (
	"math"
	"time"
)

// ScoreInput 包含评分所需的全部输入。TaskTags 为空时等同于冷启动模式（tag_match=0）。
type ScoreInput struct {
	UseCount int
	LastUsed time.Time
	TaskTags []string
	NodeTags []string
	Now      time.Time
}

// Score 计算单个节点的置信度分数。
//
//	score = (use_count_norm × 0.4) + (recency × 0.4) + (tag_match × 0.2)
//	use_count_norm = min(use_count / 20, 1.0)
//	recency        = max(0, 1 - Δdays / 90)
//	tag_match      = |NodeTags ∩ TaskTags| / max(len(TaskTags), 1)   冷启动时=0
func Score(input ScoreInput) float64 {
	useNorm := math.Min(float64(input.UseCount)/20.0, 1.0)

	recency := 0.0
	if !input.LastUsed.IsZero() {
		days := input.Now.Sub(input.LastUsed).Hours() / 24.0
		recency = math.Max(0, 1.0-days/90.0)
	}

	tagMatch := 0.0
	if len(input.TaskTags) > 0 {
		nodeSet := make(map[string]struct{}, len(input.NodeTags))
		for _, t := range input.NodeTags {
			nodeSet[t] = struct{}{}
		}
		var hits int
		for _, t := range input.TaskTags {
			if _, ok := nodeSet[t]; ok {
				hits++
			}
		}
		tagMatch = float64(hits) / float64(len(input.TaskTags))
	}

	return useNorm*0.4 + recency*0.4 + tagMatch*0.2
}

// Classify 根据分数和最后使用日期确定节点状态。
//
//	score ≥ 0.6                     → "active"
//	0.3 ≤ score < 0.6               → "dormant"
//	score < 0.3 且 Δdays > 180      → "archived"
//	score < 0.3 且 Δdays ≤ 180      → "dormant"
func Classify(score float64, lastUsed time.Time, now time.Time) string {
	switch {
	case score >= 0.6:
		return "active"
	case score >= 0.3:
		return "dormant"
	default:
		days := now.Sub(lastUsed).Hours() / 24.0
		if days > 180 {
			return "archived"
		}
		return "dormant"
	}
}
```

- [ ] **Step 3.2.4: 运行评分测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -run TestScore -run TestClassify -v 2>&1 | tail -20
```

期望：PASS。

### Task 3.3: context.go — context.md 生成器

- [ ] **Step 3.3.1: 写失败测试**

创建 `src/internal/recall/context_test.go`：

```go
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
	// 创建大量节点以触发截断
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
```

- [ ] **Step 3.3.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -run TestBuildContext -v 2>&1 | tail -20
```

期望：FAIL。

- [ ] **Step 3.3.3: 实现 context.go**

创建 `src/internal/recall/context.go`：

```go
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
		rel := relativeID(n.ID)
		label := nodeLabel(n.ID)
		linkPath := filepath.ToSlash(rel)
		sb.WriteString(fmt.Sprintf("- [%s](%s) score=%.2f tags=[%s]\n",
			label, linkPath, n.Score, strings.Join(n.Tags, ",")))
	}
	sb.WriteString("\n")
}

// relativeID returns the path portion of an id (e.g. "skill:foo/bar" → "foo/bar").
func relativeID(id string) string {
	if idx := strings.Index(id, ":"); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// nodeLabel extracts a human-readable label from an ID.
func nodeLabel(id string) string {
	rel := relativeID(id)
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
	// 保留 end marker
	endMarker := "<!-- ccclaw:context:end -->"
	truncated := lines[:maxLines-2]
	truncated = append(truncated, "<!-- context truncated -->", endMarker)
	return strings.Join(truncated, "\n")
}
```

- [ ] **Step 3.3.4: 运行 context 测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -run TestBuildContext -v 2>&1 | tail -20
```

期望：PASS。

- [ ] **Step 3.3.5: 运行 recall 包全量测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... -v 2>&1 | tail -30
```

期望：所有测试 PASS。

- [ ] **Step 3.3.6: Commit**

```bash
cd /opt/src/ccclaw
git add src/internal/recall/
git commit -m "feat(#77): add recall internal package (nodes/scorer/context)"
```

---

## Chunk 4: ccclaw recall 命令

**Files:**
- Create: `src/internal/app/recall.go`
- Create: `src/cmd/ccclaw/recall.go`
- Modify: `src/cmd/ccclaw/main.go` (注册命令)

### Task 4.1: Runtime.Recall() 方法

- [ ] **Step 4.1.1: 写失败测试**

在 `src/internal/app/` 下新增 `recall_test.go`（集成测试，使用 tempdir）：

```go
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/config"
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

	// 参照现有 runtime_test.go 中内联 &Runtime{} 的模式，log=nil 是安全的（logWithLevel 有 nil 检查）
	rt := &Runtime{
		cfg: &config.Config{
			KB: config.KBConfig{ContextMaxLines: 256},
		},
		memRoot:  kbRoot,
		ghCache:  map[string]*ghclient.Client{},
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
```

> `ghclient` 是 `github.com/41490/ccclaw/internal/adapters/github` 的别名。检查现有 `runtime_test.go` 中的 import 别名并保持一致。`memory` 是 `github.com/41490/ccclaw/internal/memory`。

- [ ] **Step 4.1.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/app/... -run TestRecallCold -v 2>&1 | tail -20
```

期望：FAIL（`Recall` 未定义）。

- [ ] **Step 4.1.3: 在 recall 包中导出 RelativeID（必须先于 4.1.4 完成）**

在 `src/internal/recall/context.go` 中将：
```go
func relativeID(id string) string {
```
改为：
```go
func RelativeID(id string) string {
```
同时将包内所有 `relativeID(` 调用改为 `RelativeID(`。

```bash
cd /opt/src/ccclaw/src && go test ./internal/recall/... 2>&1 | grep -E "FAIL|ok"
```
期望：ok（现有测试继续通过）。

- [ ] **Step 4.1.4: 实现 recall.go**

创建 `src/internal/app/recall.go`：

```go
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/recall"
)

// RecallOptions 控制 recall 行为。
type RecallOptions struct {
	IssueNum int      // > 0 时从 GitHub Issue 提取 tags
	Tags     []string // 显式追加的 task tags
	Cold     bool     // 冷启动模式，tag_match=0
	Rebuild  bool     // 强制重建 nodes.jsonl
	Now      time.Time
}

// Recall 执行置信度评分，生成 kb/context.md。
func (rt *Runtime) Recall(opts RecallOptions, out io.Writer) error {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	kbRoot := rt.memRoot
	nodesPath := filepath.Join(kbRoot, "memory", "nodes.jsonl")
	contextPath := filepath.Join(kbRoot, "context.md")
	maxLines := rt.cfg.KB.ContextMaxLines
	if maxLines <= 0 {
		maxLines = 256
	}

	// 1. 加载或重建 nodes.jsonl
	nodes, err := rt.loadOrRebuildNodes(nodesPath, kbRoot, opts.Rebuild, out)
	if err != nil {
		return err
	}

	// 2. 若有 --issue N，提取 task tags（带超时，失败降级到 cold）
	taskTags := append([]string{}, opts.Tags...)
	if opts.IssueNum > 0 && !opts.Cold {
		issueTags, err := rt.fetchIssueTags(opts.IssueNum)
		if err != nil {
			rt.logWarning("recall", "获取 Issue tags 失败，降级到冷启动", "issue", opts.IssueNum, "error", err)
			_, _ = fmt.Fprintf(out, "警告: 获取 Issue #%d tags 失败，降级冷启动: %v\n", opts.IssueNum, err)
			opts.Cold = true
		} else {
			taskTags = append(taskTags, issueTags...)
		}
	}

	// 3. 计算每个节点的 score 并更新 status
	scored := make([]recall.MemoryNode, len(nodes))
	for i, n := range nodes {
		if n.Status == "candidate" || n.Status == "archived" {
			scored[i] = n
			continue
		}
		lastUsed, _ := time.ParseInLocation("2006-01-02", n.LastUsed, time.Local)
		scoreInput := recall.ScoreInput{
			UseCount: n.UseCount,
			LastUsed: lastUsed,
			NodeTags: n.Tags,
			Now:      opts.Now,
		}
		if !opts.Cold {
			scoreInput.TaskTags = taskTags
		}
		s := recall.Score(scoreInput)
		n.Score = s
		n.Status = recall.Classify(s, lastUsed, opts.Now)
		scored[i] = n
	}

	// 4. 生成 context.md
	triggerDesc := "cold:journal-timer"
	if !opts.Cold {
		if opts.IssueNum > 0 {
			triggerDesc = fmt.Sprintf("recall:issue=%d", opts.IssueNum)
		} else {
			triggerDesc = "recall:manual"
		}
	}
	ctxContent := recall.BuildContext(recall.ContextInput{
		Nodes:       scored,
		TaskTags:    taskTags,
		Cold:        opts.Cold,
		TriggerDesc: triggerDesc,
		KBRoot:      kbRoot,
		MaxLines:    maxLines,
		Now:         opts.Now,
	})
	if err := os.MkdirAll(filepath.Dir(contextPath), 0o755); err != nil {
		return fmt.Errorf("创建 kb 目录失败: %w", err)
	}
	if err := atomicWriteFile(contextPath, []byte(ctxContent)); err != nil {
		return fmt.Errorf("写入 context.md 失败: %w", err)
	}

	// 5. 被命中的节点 use_count+1，last_used 更新
	for i, n := range scored {
		if n.Score >= 0.3 && n.Status != "archived" && n.Status != "candidate" {
			scored[i].UseCount++
			scored[i].LastUsed = opts.Now.Format("2006-01-02")
		}
	}

	// 6. 归档 status=archived 的节点对应文件
	for _, n := range scored {
		if n.Status == "archived" {
			rt.archiveMemoryNode(n, kbRoot)
		}
	}

	// 7. 原子写回 nodes.jsonl
	if err := recall.SaveNodes(nodesPath, scored); err != nil {
		return fmt.Errorf("写入 nodes.jsonl 失败: %w", err)
	}

	_, _ = fmt.Fprintf(out, "recall 完成: context.md 已更新 (%s)\n", contextPath)
	return nil
}

func (rt *Runtime) loadOrRebuildNodes(nodesPath, kbRoot string, forceRebuild bool, out io.Writer) ([]recall.MemoryNode, error) {
	if forceRebuild {
		_, _ = fmt.Fprintf(out, "WARN: 重建 nodes.jsonl（use_count 对非 skill 条目将归零）\n")
		return recall.RebuildNodes(kbRoot)
	}
	nodes, err := recall.LoadNodes(nodesPath)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		_, _ = fmt.Fprintf(out, "WARN: nodes.jsonl 不存在，自动重建（use_count 对非 skill 条目将归零）\n")
		return recall.RebuildNodes(kbRoot)
	}
	return nodes, nil
}

func (rt *Runtime) fetchIssueTags(issueNum int) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx // client.GetIssue 内部使用 defaultTimeout，此处超时作为上层保护
	client := rt.clientForRepo(rt.cfg.GitHub.ControlRepo)
	issue, err := client.GetIssue(issueNum)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, label := range issue.Labels {
		if strings.TrimSpace(label.Name) != "" {
			tags = append(tags, strings.ToLower(strings.ReplaceAll(label.Name, " ", "-")))
		}
	}
	// 从 body 的 keywords 行提取（格式：keywords: foo, bar）
	for _, line := range strings.Split(issue.Body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "keywords:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				for _, kw := range strings.Split(parts[1], ",") {
					kw = strings.TrimSpace(kw)
					if kw != "" {
						tags = append(tags, strings.ToLower(kw))
					}
				}
			}
		}
	}
	return tags, nil
}

func (rt *Runtime) archiveMemoryNode(n recall.MemoryNode, kbRoot string) {
	rel := recall.RelativeID(n.ID)
	src := filepath.Join(kbRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return
	}
	// 仅归档 skills 路径（保守策略）
	if !strings.HasPrefix(rel, "skills/") {
		return
	}
	archivedRel := strings.Replace(rel, "skills/L1/", "skills/archived/", 1)
	archivedRel = strings.Replace(archivedRel, "skills/L2/", "skills/archived/", 1)
	dst := filepath.Join(kbRoot, filepath.FromSlash(archivedRel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		rt.logWarning("recall", "创建 archived 目录失败", "error", err)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		rt.logWarning("recall", "归档 skill 失败", "src", src, "error", err)
	}
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4.1.5: 运行 Runtime.Recall 测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/app/... -run TestRecallCold -v 2>&1 | tail -20
```

期望：PASS。

### Task 4.2: cobra 命令装配

- [ ] **Step 4.2.1: 创建 recall.go 命令文件**

创建 `src/cmd/ccclaw/recall.go`：

```go
package main

import (
	"strings"

	"github.com/41490/ccclaw/internal/app"
	"github.com/spf13/cobra"
)

func addRecallCommand(rootCmd *cobra.Command, configPath, envFile *string) {
	var (
		issueNum int
		tags     string
		cold     bool
		rebuild  bool
	)
	cmd := &cobra.Command{
		Use:   "recall",
		Short: "生成 kb/context.md（置信度评分，只加载相关记忆）",
		Long: `recall 扫描 kb/memory/nodes.jsonl，按置信度评分后生成 kb/context.md。
在任务完成后调用以更新下次会话的上下文。

示例：
  ccclaw recall --issue 77
  ccclaw recall --tags memory,architecture
  ccclaw recall --cold
  ccclaw recall --rebuild`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 参照 sevolver.go 的模式：使用 NewRuntime（内部自行加载 config）
			rt, err := app.NewRuntime(*configPath, *envFile)
			if err != nil {
				return err
			}
			var tagList []string
			for _, t := range strings.Split(tags, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tagList = append(tagList, t)
				}
			}
			return rt.Recall(app.RecallOptions{
				IssueNum: issueNum,
				Tags:     tagList,
				Cold:     cold,
				Rebuild:  rebuild,
			}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&issueNum, "issue", 0, "从 GitHub Issue 提取 task tags（指定 Issue 编号）")
	cmd.Flags().StringVar(&tags, "tags", "", "显式指定 task tags（逗号分隔）")
	cmd.Flags().BoolVar(&cold, "cold", false, "冷启动模式，tag_match=0")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "强制重建 nodes.jsonl（丢失后恢复用）")
	rootCmd.AddCommand(cmd)
}
```

- [ ] **Step 4.2.2: 在 main.go 注册命令**

在 `src/cmd/ccclaw/main.go` 中，找到 `addSevolverCommand` 调用处，在其后追加：

```go
addRecallCommand(rootCmd, &configPath, &envFile)
```

- [ ] **Step 4.2.3: 编译验证**

```bash
cd /opt/src/ccclaw/src && go build ./cmd/ccclaw/... 2>&1
```

期望：编译成功（无输出）。

- [ ] **Step 4.2.5: 冒烟测试**

```bash
/opt/src/ccclaw/src/ccclaw recall --help 2>&1 | head -15
```

期望：显示 recall 命令帮助文本。

- [ ] **Step 4.2.6: 运行全量测试**

```bash
cd /opt/src/ccclaw/src && go test ./... 2>&1 | grep -E "FAIL|ok" | tail -30
```

期望：无 FAIL。

- [ ] **Step 4.2.7: Commit**

```bash
cd /opt/src/ccclaw
git add src/internal/app/recall.go src/cmd/ccclaw/recall.go src/cmd/ccclaw/main.go
git add src/internal/recall/context.go  # RelativeID 导出变更
git commit -m "feat(#77): add ccclaw recall command"
```

---

## Chunk 5: journal 命令冷启动集成

**Files:**
- Modify: `src/internal/app/runtime.go` (Journal 函数末尾)

- [ ] **Step 5.1: 写集成测试**

在现有 `runtime_test.go` 中追加（参照 `TestJournalWritesDailyFile` 的构建模式）：

```go
func TestJournalTriggersRecallCold(t *testing.T) {
	tmpDir := t.TempDir()
	varDir := filepath.Join(tmpDir, "var")
	kbRoot := filepath.Join(tmpDir, "kb")

	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{ControlRepo: "41490/ccclaw"},
			Paths: config.PathsConfig{
				HomeRepo: tmpDir,
				VarDir:   varDir,
				KBDir:    kbRoot,
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: filepath.Join(tmpDir, "target"),
			}},
			KB: config.KBConfig{ContextMaxLines: 256},
		},
		store:    store,
		memRoot:  kbRoot,
		ghCache:  map[string]*github.Client{},
		memCache: map[string]*memory.Index{},
		syncRepo: func(repoPath, message string, paths []string, maxRetry int) error {
			return nil // 测试中不需要真实同步
		},
	}

	var buf strings.Builder
	if err := rt.Journal(time.Now(), &buf); err != nil {
		t.Fatalf("Journal failed: %v", err)
	}

	contextPath := filepath.Join(kbRoot, "context.md")
	if _, err := os.Stat(contextPath); os.IsNotExist(err) {
		t.Error("Journal should have created context.md via cold recall")
	}
}
```

> `github` = `github.com/41490/ccclaw/internal/adapters/github`（检查文件顶部现有 import 别名并保持一致）。

- [ ] **Step 5.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/app/... -run TestJournalTriggersRecall -v 2>&1 | tail -20
```

期望：FAIL（context.md 未被创建）。

- [ ] **Step 5.3: 修改 Journal() 末尾**

> **前置条件**：确认 Chunk 4 已完成，`rt.Recall` 方法和 `RecallOptions` 类型已存在于 `src/internal/app/recall.go`，否则此步会编译失败。

在 `src/internal/app/runtime.go` 的 `Journal` 函数中，精确定位：

```go
	rt.logInfo("journal", "日报生成完成", "day", day.Format("2006-01-02"), "files", len(writtenPaths))
	return nil
```

将末尾的 `return nil` **替换**为（保留 `rt.logInfo` 行不变，只替换 `return nil`）：

```go
	// 冷启动 recall：刷新 kb/context.md
	if recallErr := rt.Recall(RecallOptions{Cold: true}, out); recallErr != nil {
		rt.logWarning("journal", "recall 冷启动失败（非阻塞）", "error", recallErr)
		_, _ = fmt.Fprintf(out, "警告: recall 冷启动失败: %v\n", recallErr)
	}
	return nil
```

- [ ] **Step 5.4: 运行测试确认通过**

```bash
cd /opt/src/ccclaw/src && go test ./internal/app/... -run TestJournalTriggersRecall -v 2>&1 | tail -20
```

期望：PASS。

- [ ] **Step 5.5: 运行全量 app 测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/app/... 2>&1 | grep -E "FAIL|ok"
```

期望：ok（无 FAIL）。

- [ ] **Step 5.6: Commit**

```bash
cd /opt/src/ccclaw
git add src/internal/app/runtime.go
git commit -m "feat(#77): journal cold recall — trigger recall --cold after journal"
```

---

## Chunk 6: sevolver 候选技能识别

**Files:**
- Create: `src/internal/sevolver/candidate_detector.go`
- Modify: `src/internal/sevolver/sevolver.go` (追加常量 + 调用候选检测)
- Create: `src/internal/sevolver/candidate_detector_test.go`

- [ ] **Step 6.1: 写失败测试**

创建 `src/internal/sevolver/candidate_detector_test.go`：

```go
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

	// 检查候选文件是否存在
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
```

- [ ] **Step 6.2: 运行确认测试失败**

```bash
cd /opt/src/ccclaw/src && go test ./internal/sevolver/... -run TestDetectCandidates -v 2>&1 | tail -20
```

期望：FAIL（函数未定义）。

- [ ] **Step 6.3: 实现 candidate_detector.go**

创建 `src/internal/sevolver/candidate_detector.go`：

```go
package sevolver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const candidateScanWindowDays = 14 // 独立于 scanWindowDays=7，不影响现有 sevolver 日报

var actionPattern = regexp.MustCompile(`(?:发布|执行|完成|运行|触发|处理)\s+([a-zA-Z_\-]+(?:\s+[a-zA-Z_\-]+)?)`)

type patternHit struct {
	Pattern string
	Sources []string
	Count   int
}

// DetectCandidates 扫描 journalDir 最近 candidateScanWindowDays 天，
// 识别重复操作模式（≥3次）并在 candidatesDir 生成草稿文件。
// 返回新建草稿数量。
func DetectCandidates(journalDir, candidatesDir string, now time.Time) (int, error) {
	since := dateFloor(now).AddDate(0, 0, -candidateScanWindowDays)
	files, err := collectJournalFiles(journalDir, since)
	if err != nil {
		return 0, err
	}

	// 统计操作模式出现次数
	patternSources := map[string][]string{}
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			matches := actionPattern.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				pattern := strings.ToLower(strings.TrimSpace(m[1]))
				if pattern == "" {
					continue
				}
				patternSources[pattern] = appendUniq(patternSources[pattern], file.Path)
			}
		}
	}

	// 筛选出现 ≥3 次的模式
	var hits []patternHit
	for pattern, sources := range patternSources {
		if len(sources) >= 3 {
			hits = append(hits, patternHit{Pattern: pattern, Sources: sources, Count: len(sources)})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Count > hits[j].Count })

	created := 0
	for _, hit := range hits {
		slug := strings.ReplaceAll(hit.Pattern, " ", "-")
		dirName := fmt.Sprintf("%s-%s", now.Format("20060102"), slug)
		draftDir := filepath.Join(candidatesDir, dirName)
		draftPath := filepath.Join(draftDir, "CLAUDE.md")

		// 若草稿已存在则跳过
		if _, err := os.Stat(draftPath); err == nil {
			continue
		}
		if err := os.MkdirAll(draftDir, 0o755); err != nil {
			return created, fmt.Errorf("创建候选目录失败: %w", err)
		}
		content := buildCandidateDraft(hit, now)
		if err := os.WriteFile(draftPath, []byte(content), 0o644); err != nil {
			return created, fmt.Errorf("写入候选草稿失败: %w", err)
		}
		created++
	}
	return created, nil
}

func buildCandidateDraft(hit patternHit, now time.Time) string {
	keywords := strings.Split(hit.Pattern, " ")
	var sourcesYAML strings.Builder
	for _, s := range hit.Sources {
		sourcesYAML.WriteString(fmt.Sprintf("  - %s\n", s))
	}
	var keywordsYAML strings.Builder
	for _, k := range keywords {
		keywordsYAML.WriteString(fmt.Sprintf("  - %s\n", k))
	}
	return fmt.Sprintf(`---
name: %s
status: candidate
detected_at: %s
source_journal:
%sexternal_match_keywords:
%s# ⚠️ 禁止自动安装。请人工核实后移入 L1/ 或 L2/
---

## 草稿内容（sevolver 自动识别）

操作模式：%s（在 %d 个日志文件中出现 %d 次）

## 匹配的外部技能（需人工核实，禁止自动安装）

搜索关键词：%s

建议搜索来源：
- superpowers skills：Skill tool 搜索 %s
- 确认后操作：手动安装并删除本草稿，或补全后移入 L1/ 或 L2/
- 若废弃：标注 status: rejected，无需删除
`,
		hit.Pattern,
		now.Format("2006-01-02"),
		sourcesYAML.String(),
		keywordsYAML.String(),
		hit.Pattern,
		len(hit.Sources),
		hit.Count,
		strings.Join(keywords, ", "),
		keywords[0],
	)
}

func appendUniq(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
```

- [ ] **Step 6.4: 追加 candidateScanWindowDays 到 sevolver.go 并调用**

在 `src/internal/sevolver/sevolver.go` 的 `Run` 函数末尾（在 `return result, nil` 之前）追加：

```go
	// 候选技能识别（独立于日报窗口，扫描最近 14 天）
	candidatesDir := filepath.Join(cfg.KBDir, "skills", "candidates")
	candidateCount, candidateErr := DetectCandidates(journalDir, candidatesDir, now)
	if candidateErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("候选技能检测失败: %v", candidateErr))
	} else if candidateCount > 0 {
		_, _ = fmt.Fprintf(out, "发现 %d 个候选技能草稿，请查看 %s\n", candidateCount, candidatesDir)
	}
```

> `journalDir` 与 `now` 使用 `Run()` 函数开头已解析的局部变量，而非 `cfg.JournalDir`/`cfg.Now`（后者可能为空/零值）。

- [ ] **Step 6.5: 运行候选检测测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/sevolver/... -run TestDetectCandidates -v 2>&1 | tail -20
```

期望：PASS。

- [ ] **Step 6.6: 运行全量 sevolver 测试**

```bash
cd /opt/src/ccclaw/src && go test ./internal/sevolver/... 2>&1 | grep -E "FAIL|ok"
```

期望：ok。

- [ ] **Step 6.7: 运行全量测试**

```bash
cd /opt/src/ccclaw/src && go test ./... 2>&1 | grep -E "FAIL|ok"
```

期望：无 FAIL。

- [ ] **Step 6.8: Commit**

```bash
cd /opt/src/ccclaw
git add src/internal/sevolver/candidate_detector.go src/internal/sevolver/candidate_detector_test.go
git add src/internal/sevolver/sevolver.go
git commit -m "feat(#77): sevolver candidate skill detection (14-day window)"
```

---

## Chunk 7: kb 目录与 .gitignore 清理

**Files:**
- Modify: `src/dist/kb/CLAUDE.md`
- Modify: `src/dist/.gitignore` 或根 `.gitignore`

- [ ] **Step 7.1: 精简 kb/CLAUDE.md**

仅修改 `src/dist/kb/CLAUDE.md` 的 `<!-- ccclaw:managed:start -->` ... `<!-- ccclaw:managed:end -->` 区块内容，**保留** `<!-- ccclaw:user:start -->` ... `<!-- ccclaw:user:end -->` 区块原样不动（upgrade 保留用户内容的约束）：

```markdown
<!-- ccclaw:managed:start -->
本目录是 `ccclaw` 的长期记忆根。

当前上下文（由 `ccclaw recall` 生成）见 `context.md`。

全量记忆：
- `journal/` — 逐日运行日志
- `designs/` — 设计决策
- `assay/` — 实验验证
- `skills/` — 可复用技巧（L1/原子、L2/组合、candidates/候选、archived/归档）
- `memory/nodes.jsonl` — 置信度元数据（本机状态，不入库）
<!-- ccclaw:managed:end -->
```

- [ ] **Step 7.2: 更新 .gitignore**

目标：防止开发时 `kb/context.md` 和 `kb/memory/nodes.jsonl` 被意外提交到 dist 树。

在 `src/dist/` 下检查是否有 `.gitignore`：

```bash
ls /opt/src/ccclaw/src/dist/.gitignore 2>/dev/null || echo "not found"
```

若存在，先检查是否已有相关条目，再追加：
```bash
grep -q "context.md" /opt/src/ccclaw/src/dist/.gitignore || echo "kb/context.md" >> /opt/src/ccclaw/src/dist/.gitignore
grep -q "nodes.jsonl" /opt/src/ccclaw/src/dist/.gitignore || echo "kb/memory/nodes.jsonl" >> /opt/src/ccclaw/src/dist/.gitignore
```

若不存在，检查根目录 `.gitignore`：
```bash
grep -n "context.md\|nodes.jsonl" /opt/src/ccclaw/.gitignore 2>/dev/null || echo "not found"
```

在合适的 .gitignore 中追加上述两行（check-before-add）。

> 注：这两个文件在安装后位于 `~/.ccclaw/kb/`（git repo 外），此处 .gitignore 仅防止 dist 开发阶段误提交。

- [ ] **Step 7.3: Commit**

```bash
cd /opt/src/ccclaw
git add src/dist/kb/CLAUDE.md
git add .gitignore  # 或 src/dist/.gitignore
git commit -m "chore(#77): simplify kb/CLAUDE.md, gitignore context.md + nodes.jsonl"
```

---

## 验收检查

完成全部 Chunk 后执行：

- [ ] **全量测试通过**
  ```bash
  cd /opt/src/ccclaw/src && go test ./... 2>&1 | grep -E "FAIL|ok"
  ```
  期望：无 FAIL。

- [ ] **ccclaw recall --help 可用**
  ```bash
  cd /opt/src/ccclaw/src && go run ./cmd/ccclaw recall --help
  ```

- [ ] **ccclaw recall --cold 可在 kb 目录生成 context.md**
  ```bash
  ccclaw recall --cold && head -5 ~/.ccclaw/kb/context.md
  ```

- [ ] **回复 Issue #77 验收报告**
  ```bash
  gh issue comment 77 --repo 41490/ccclaw --body "## 树型记忆 v2 实施完成\n\n..."
  ```
