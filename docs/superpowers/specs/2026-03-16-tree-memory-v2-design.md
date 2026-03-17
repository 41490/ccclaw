# 树型记忆 v2 设计规范

- Issue: #77
- 作者: CCoder
- 日期: 2026-03-16
- 状态: 草稿

---

## 背景与目标

当前 kb/ 下所有 CLAUDE.md 在 Claude Code 启动时全量加载，无论与当前任务的相关性，造成 token 浪费。

三个目标：
1. 长期记忆所有事件、约定、决策（永不删除）
2. 只提醒与当前任务最相关的信息（不是全部）
3. 定期从记忆中识别技能需求，生成草稿指向外部技能，人工确认

五项决策：
- 方案 B：置信度驱动的动态上下文
- 衰减：接受不删除，逐步归档
- 技能识别：生成草稿 + 外部技能指针，绝对不自动安装
- 触发时机：ccclaw 命令触发，在任务完成后
- 冷启动：journal timer（01:01:42）阶段（sysNOTA 称为 "dreamclearner 时期"），一周摘要 + top-4 skills；若 `ccclaw dream`（02:42）实现后可迁移

---

## 整体架构

```
                    ccclaw 任务完成
                          │
                          ▼
                   ccclaw recall --issue N
                          │
                    ┌─────┴─────┐
                    │  scorer   │  读取 memory/nodes.jsonl
                    │           │  计算置信度 score
                    └─────┬─────┘
                          │ top-K (score ≥ 0.3)
                          ▼
                   kb/context.md  ←── 唯一动态加载文件

每天 01:01:42 journal timer（冷启动）
                          │
                    ┌─────┴──────┐
                    │ 冷启动摘要  │  ccclaw recall --cold
                    │            │  只用 recency + use_count
                    └─────┬──────┘
                          │
              kb/context.md（冷启动版：本周摘要 + top-4 skills）

sevolver 每天 22:00 扫描（现有）
                          │
             新增候选扫描（独立窗口 14 天）
                          │
              kb/skills/candidates/<slug>/CLAUDE.md
              （草稿 + 外部技能指针，人工确认）
```

---

## 目录结构变更

```
kb/
├── CLAUDE.md           ← 极简根索引（只说明 context.md 是入口）
├── context.md          ← 动态生成（recall 或 journal timer 后刷新）[不入库]
├── memory/             ← 新增：置信度元数据
│   ├── nodes.jsonl     ← 每条记忆节点的置信度记录 [不入库]
│   └── tags.md         ← 全局标签索引（辅助 task_tag_match）[入库]
├── journal/            ← 不变（全量事件流）
├── designs/            ← 不变（设计决策）
├── assay/              ← 不变（实验验证）
└── skills/
    ├── CLAUDE.md       ← 不变
    ├── L1/             ← 不变（原子技巧）
    ├── L2/             ← 不变（组合工作流）
    ├── candidates/     ← 新增：sevolver 识别的技能草稿候选区
    └── archived/       ← 原 deprecated/，重命名，归档但永不删除
```

**kb/CLAUDE.md 变更**：移除冗长目录说明，只保留：
```
当前上下文见 context.md（由 ccclaw recall 生成）。
全量记忆见 journal/ designs/ assay/ skills/。
```

**.gitignore 新增**：
```
kb/context.md
kb/memory/nodes.jsonl
```

---

## 置信度模型

### 评分公式

```
score = (use_count_norm × 0.4) + (recency × 0.4) + (tag_match × 0.2)

use_count_norm = min(use_count / 20, 1.0)
recency        = max(0, 1 - Δdays / 90)     # 90天线性衰减至0
tag_match      = |memory.tags ∩ task.tags| / max(|task.tags|, 1)
                 # 冷启动（--cold）时 task.tags=[] → tag_match=0 → score 只靠前两维
```

### 生命周期阈值（替换现有基于天数的机制）

新阈值基于 score，替换 `skill_updater.go` 中现有的 `inactiveDays >= 28 → deprecated`、`inactiveDays >= 14 → dormant` 逻辑：

```
score ≥ 0.6                        → active   → 进入 context.md
0.3 ≤ score < 0.6                  → dormant  → 存储，不进 context.md
score < 0.3 且 Δdays > 180 天      → archived → 移入 archived/，永不删除
```

> **注**：此变更会使 skills 的生命周期从"纯时间驱动"改为"置信度驱动"。
> 现有 `use_count/last_used/status` frontmatter 字段保留不变，sevolver 评分时从 frontmatter 读取后写入 nodes.jsonl。

### use_count 来源

- **skills**：sevolver 现有 `use_count` frontmatter 字段，读取后同步到 nodes.jsonl
- **journal/designs/assay**：每次被 recall 命中并写入 context.md 时 +1（recall 执行时更新 nodes.jsonl）

### tags 来源（无需手动标注）

优先级：
1. frontmatter `keywords:` 字段（skills 已有）
2. 文件路径关键词（如 `stream`、`release`、`debug`）
3. signal-rules.md 中定义的关键词

无法提取 tags 时：`tags: []`，该节点 tag_match 始终为 0，仅靠 use_count + recency 得分——此为预期行为，不影响正确性。

---

## memory/nodes.jsonl 格式

每行一条记忆节点记录，原子覆写（参照 `skill_updater.go` 的 `WriteAtomically` 模式，写临时文件再 rename）：

```jsonl
{"id":"skill:L1/git-sign","tags":["git","commit"],"use_count":5,"last_used":"2026-03-14","status":"active","score":0.82}
{"id":"skill:L2/release-flow","tags":["release","dist"],"use_count":12,"last_used":"2026-03-16","status":"active","score":0.91}
{"id":"design:26/tree-memory","tags":["memory","architecture"],"use_count":0,"last_used":"2026-03-16","status":"active","score":0.40}
{"id":"journal:26/03/16","tags":["release","stream"],"use_count":0,"last_used":"2026-03-16","status":"active","score":0.40}
```

字段说明：
- `id`：`<type>:<relative_path_from_kb_root>`，type 为 skill/design/journal/assay/candidate
- `tags`：见上方 tags 来源规则
- `status`：active / dormant / archived / candidate（candidate 不参与 context.md 生成）
- `score`：每次 recall 运行时重新计算写入，缓存值仅供调试

### 节点生命周期

```
首次扫描到文件 → 创建节点（use_count=0, status=active）
recall 命中    → use_count+1, last_used 更新, score 重算
score 降至 dormant 阈值 → status=dormant
score 降至 archived 阈值（+180天） → 文件移入 archived/, status=archived
archived 节点保留在 nodes.jsonl，status=archived，永不删除，不参与 context.md
```

### 冷重建（nodes.jsonl 丢失时）

运行 `ccclaw recall --rebuild`：
- 扫描所有 skills frontmatter，重建 skill 节点（use_count 从 frontmatter 读取，保留历史值）
- 扫描 journal/designs/assay 文件，创建节点（use_count 归零，score 只靠 recency）
- 输出告警：`WARN: nodes.jsonl rebuilt from scratch, use_count for non-skill memories reset to 0`

---

## context.md 格式

### 普通 recall（有任务信号）

```markdown
<!-- ccclaw:context:generated:2026-03-16T14:30 -->
<!-- ccclaw:context:trigger:recall:issue=77:tags=memory,architecture -->

# 当前上下文

## 相关技能（top-4，按置信度）
- [release-flow](skills/L2/release-flow/) score=0.91 tags=[release,dist]
- [stream-json-debug](skills/L1/stream-json-debug/) score=0.71 tags=[stream,debug]

## 近期关键事件（近7天）
- 2026-03-16: 发布 26.03.16.1742，本机缓存升级验收通过
  → [journal/26/03/16/...](journal/26/03/16/...)（路径相对 kb/ 根）
- 2026-03-15: 修复 stream 收口兼容 Claude system
  → [journal/26/03/15/...](journal/26/03/15/...)

## 活跃设计
- [树型记忆 v2](designs/26/tree-memory-v2.md) — 待实现

## 待决策
- Issue #77: rethink 树型记忆（进行中）

<!-- ccclaw:context:end -->
```

### 冷启动 recall（--cold，无任务信号）

```markdown
<!-- ccclaw:context:generated:2026-03-17T01:02 -->
<!-- ccclaw:context:trigger:cold:journal-timer -->

# 本周上下文摘要（dreamclearner 冷启动）

## Top-4 技能（use_count + recency，无 tag 权重）
- [release-flow](skills/L2/release-flow/) score=0.88
- [stream-json-debug](skills/L1/stream-json-debug/) score=0.72
- [sevolver-debug](skills/L1/sevolver-debug/) score=0.61
- [kb-summary](skills/L1/kb-summary/) score=0.55

## 本周摘要（近7天）
- 执行任务 N 个（Issue #X, #Y）
- 关键变更：release 26.03.16.1742 发布，本机缓存升级验收
- 待处理：Issue #77（树型记忆重构）

<!-- ccclaw:context:end -->
```

**规则**：
- 所有链接路径相对于 `kb/` 根
- context.md 不超过 `context_max_lines`（默认 256 行，可配置），超出时截断保留 top-K
- 每次 recall 完整覆写（临时文件 + rename，原子操作）
- context.md 不入库（.gitignore）

---

## ccclaw recall 命令

### 接口

```bash
ccclaw recall [--issue N] [--tags tag1,tag2] [--cold] [--rebuild]
```

- `--issue N`：从 Issue labels/body 提取 tags → 补充到 task.tags
- `--tags`：显式指定当前任务标签（可与 --issue 叠加）
- `--cold`：冷启动模式，tag_match=0，只用 recency + use_count，生成冷启动版 context.md
- `--rebuild`：冷重建 nodes.jsonl（丢失恢复用）

### 执行流程

```
1. 加载 memory/nodes.jsonl
   - 不存在 → 自动触发 rebuild 逻辑，输出 WARN（同 --rebuild 显式调用，行为一致）
2. 若有 --issue N：gh issue view N（超时 10s），提取 labels/body 关键词 → task.tags
   - 超时或网络失败 → 降级到 --cold 模式继续，不中断 recall
3. 计算每个节点的 score（--cold 时 tag_match=0）
4. 按 score 降序，取 score ≥ 0.3 的 top-10
5. 按节点类型分组：skills / journal / designs（冷启动版加"本周摘要"段）
6. 生成 context.md（原子写入）
7. 被命中的节点：use_count+1，last_used 更新
8. 按阈值更新所有节点 status（active/dormant/archived）
9. archived 节点：移动对应文件到 archived/（若文件仍在原位）
10. 原子覆写 memory/nodes.jsonl
```

### 集成点

- 任务完成后由 ccclaw 自动调用：`ccclaw recall --issue $ISSUE_NUM`（作为任务 post-hook）
- 无 Issue 信号时（直接调用）：`ccclaw recall` 无参数，降级到 `--cold`

---

## 冷启动：journal timer 集成

**集成到现有 `ccclaw-journal.timer`（01:01:42）**：

journal 命令执行完成后，追加调用 `ccclaw recall --cold`：

```
journal 命令流程（现有）:
    1. 扫描 journal/，生成日报摘要
    2. 更新 sevolver gap 记录
    （新增）
    3. ccclaw recall --cold → 刷新 kb/context.md
```

> **注**：若未来 `ccclaw dream`（02:42）实现（见 `docs/superpowers/plans/2026-03-15-dream-consolidation.md`），可将步骤 3 迁移到 dream 命令，并在 dream 中叠加目标 3 的技能草稿生成。

---

## sevolver 技能识别增强

### 新增常量（与现有 scanWindowDays=7 独立）

```go
// sevolver.go
const (
    scanWindowDays          = 7   // 现有：sevolver 日报扫描窗口（不变）
    candidateScanWindowDays = 14  // 新增：技能候选模式识别窗口
)
```

### 触发条件

每天 22:00 sevolver 运行时，在现有日报逻辑之后追加：
- 扫描 journal/ 最近 14 天（`candidateScanWindowDays`）
- 同一 verb+object 模式出现 ≥3 次 → 生成候选草稿
- 模式提取：基于 signal-rules.md 中已定义的关键词聚类（不引入 NLP 依赖）

### 候选草稿格式

`kb/skills/candidates/YYYYMMDD-<slug>/CLAUDE.md`：

```markdown
---
name: <slug>
status: candidate
detected_at: 2026-03-16
source_journal:
  - journal/26/03/14/...
  - journal/26/03/15/...
  - journal/26/03/16/...
external_match_keywords:
  - keyword1
  - keyword2
# ⚠️ 禁止自动安装。请人工核实后移入 L1/ 或 L2/
---

## 草稿内容（sevolver 自动提取）

（从 journal 中提取的操作步骤摘要）

## 匹配的外部技能（需人工核实，禁止自动安装）

搜索关键词：keyword1, keyword2

建议搜索来源：
- superpowers skills：Skill tool 搜索 keyword1
- claude plugins：搜索 keyword2

确认后操作：
- 若匹配：手动安装，并删除本草稿
- 若不匹配：手动补全，移入 L1/ 或 L2/
- 若废弃：标注 status: rejected，无需删除
```

候选草稿在 nodes.jsonl 中记录为 `status: candidate`，不参与 context.md 生成。

---

## deprecated/ → archived/ 迁移

**重命名操作**：`git mv kb/skills/deprecated kb/skills/archived`

**需同步修改的代码位置**（含函数名与字符串字面量，完整列表）：

| 文件 | 位置（约） | 内容 |
|------|-----------|------|
| `src/internal/sevolver/skill_updater.go` | 第 20 行 | 常量 `skillStatusDeprecated` → `skillStatusArchived` |
| `src/internal/sevolver/skill_updater.go` | 第 51 行 | `isDeprecatedSkillDir` 函数及路径字符串 `"/skills/deprecated"` |
| `src/internal/sevolver/skill_updater.go` | 第 187–201 行 | 函数 `ArchiveDeprecated` 改名 + 内部路径字符串 `"deprecated"` 全部替换 |
| `src/internal/memory/index.go` | 第 59–62 行 | `isDeprecatedSkillsDir` 函数及路径字符串 |

```go
// 旧
skillStatusDeprecated = "deprecated"
// 新
skillStatusArchived = "archived"
```

---

## 兼容性与升级路径

### 升级时保留

- `kb/**/CLAUDE.md` 用户区块（`<!-- ccclaw:user:start -->` ... `<!-- ccclaw:user:end -->`）不变
- skills frontmatter 中 `use_count/last_used/status/gap_signals/gap_escalations` 不变
- `deprecated/` 目录重命名为 `archived/`（内容不修改）

### 新增文件（升级时创建，不覆盖）

- `kb/memory/nodes.jsonl`：首次 recall 时自动创建
- `kb/context.md`：首次 recall 或 journal timer 后创建
- `kb/skills/candidates/`：目录创建，内容由 sevolver 填充

### .gitignore 新增

```
kb/context.md
kb/memory/nodes.jsonl
```

---

## 实现范围

| 组件 | 变更类型 | 说明 |
|------|----------|------|
| `ccclaw recall` | 新增命令 | 核心置信度评分与 context.md 生成 |
| `ccclaw-sevolver` | 增强 | 候选技能识别（新增 `candidateScanWindowDays=14`，不改 `scanWindowDays=7`）|
| `ccclaw-journal` | 增强 | 执行后调用 `ccclaw recall --cold` |
| `kb/CLAUDE.md` | 精简 | 移除冗余说明，指向 context.md |
| `kb/memory/` | 新增目录 | nodes.jsonl（不入库）+ tags.md（入库）|
| `kb/skills/candidates/` | 新增目录 | 技能草稿候选区 |
| `kb/skills/archived/` | 重命名 | deprecated/ → archived/（3处代码同步）|
| `.gitignore` | 更新 | 排除 context.md + nodes.jsonl |
| `src/internal/sevolver/skill_updater.go` | 修改 | deprecated→archived 常量 + 新生命周期阈值 |
| `src/internal/memory/index.go` | 修改 | deprecated→archived 路径 |

---

## 已确认决策（2026-03-17）

1. **nodes.jsonl 丢失恢复**：接受。skill use_count 从 frontmatter 恢复；journal/designs/assay use_count 归零，score 只靠 recency。

2. **context.md 大小上限**：可配置，默认 **256 行**。配置项：`[kb] context_max_lines = 256`。

3. **recall --cold 集成点**：集成到 **journal 命令末尾步骤**（不作为独立 post-hook）。
