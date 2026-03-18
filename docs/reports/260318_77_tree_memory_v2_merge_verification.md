# 260318_77_tree_memory_v2_merge_verification

对应 Issue：[#77](https://github.com/41490/ccclaw/issues/77)

- 报告日期：2026-03-18
- 关联基线：[#79](https://github.com/41490/ccclaw/issues/79)
- 检查分支：`feat/77-tree-memory-v2`
- 合并目标：`main`

## 背景

根据 [#79](https://github.com/41490/ccclaw/issues/79) 的收口记要，当前主线必须满足两条前提后才适合继续并入功能分支：

1. `stream-json` 已落实“结果优先”语义，不能因未知普通帧把已成功 result 的任务误翻失败
2. 失败诊断必须直接给出真实行号与帧类型，而不是继续误导成 `hook_started` 前缀

本轮目标不是重新实现 #79，而是确认 `feat/77-tree-memory-v2` 在准备并回 `main` 时，**没有绕开或回退上述基线**。

## 当前代码状态

检查时间点的提交关系如下：

- `origin/main`：`394d290` `fix(#79): 补齐 stream result 优先与失败诊断`
- 本地 `main`：`f5a625f` `chore: add .worktrees/ to .gitignore for local worktree isolation`
- `feat/77-tree-memory-v2`：`ce7a011`，基于本地 `main` 继续叠加 #77 的 recall / tree-memory v2 相关提交

结论：

1. `feat/77-tree-memory-v2` 的祖先链已经包含 `394d290`
2. 因此该分支**已继承 #79 的结果优先与精确失败诊断修复**
3. #77 的新增改动集中在 recall、context 生成、journal 冷启动 recall、sevolver 候选技能识别与 `kb/CLAUDE.md` 收敛，不覆盖 `#79` 修复文件

## 验证结果

在 `feat/77-tree-memory-v2/src` 执行：

```bash
go test ./...
```

结果：全量通过。

覆盖含义：

1. `internal/executor` 仍通过，说明 #79 的 stream contract / result-first 行为未被回退
2. `internal/app` 通过，说明 #77 新增 recall 逻辑与 runtime 集成至少在现有单测口径下可工作
3. `internal/recall` 与 `internal/sevolver` 通过，说明树型记忆 v2 的核心新增路径可编译、可回归

## 工作区现场

检查时 `feat/77-tree-memory-v2` worktree 存在以下未跟踪本地产物：

- `src/ccclaw`
- `src/internal/app/context.md`
- `src/internal/app/memory/nodes.jsonl`

它们分别属于本地编译产物与 recall 运行态生成文件，不应进入版本库，也不应影响合并判断。本轮提交与合并均只纳入显式暂存文件，这些产物未进入版本控制。

## 合并判断

基于本轮检查，`feat/77-tree-memory-v2` 满足并回 `main` 的条件：

1. 不缺失 #79 基线修复
2. #77 新增改动已通过现有全量测试
3. 未跟踪本地产物已与版本文件隔离，不阻断本轮提交与快进合并

因此本轮采用：

1. 先在 `feat/77-tree-memory-v2` 补充本报告并提交
2. 再将 `main` 快进到该分支头部
3. 最后推送 `main` 到远端

## 备注

若后续需要对 `ccclaw recall` 做真实运行态验收，建议补一轮针对 `src/dist/kb/` 的集成验证；本报告只覆盖“是否可安全并回主线”的代码与测试检查，不替代发布验收。
