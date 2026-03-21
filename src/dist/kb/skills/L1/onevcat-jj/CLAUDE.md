---
name: onevcat-jj
description: 在存在 .jj/ 的仓库中，所有本地版本管理操作统一优先使用 jj，而不是 git。
keywords:
  - jj
  - jujutsu
  - version-control
  - finalize
  - bookmark
---

# onevcat-jj

> 本 Skill 依据 <https://github.com/onevcat/skills/tree/master/skills/onevcat-jj> 适配为 CCClaw 受管知识卡片，供安装与升级自动发放。

## 适用条件

- 仓库存在 `.jj/`
- 仓库同时存在 `.git/` 与 `.jj/`，属于 colocated 模式
- 用户明确要求使用 `jj`
- 需要执行提交、改历史、切换工作、同步远端、处理冲突等版本管理动作

## 默认原则

- 本地版本管理统一使用 `jj`
- 远端协作面仍然是 GitHub / Git 分支与提交
- 非必要不在 `jj` 仓库里执行 `git add`、`git commit`、`git stash`、`git checkout`
- `bookmark` 只在需要推送远端时创建或调整

## 最小检查

```bash
test -d .jj && echo "jj repo"
jj log
```

若仓库同时有 `.jj/` 与 `.git/`，本地操作仍优先 `jj`。

## 初始化

在已有 Git 仓库上启用 colocated `jj`：

```bash
jj git init --colocate
```

若远端默认分支尚未被 `jj` 跟踪，补一条：

```bash
jj bookmark track main@origin
```

若远端仍为 `master`，将 `main` 换成 `master`。

## 常用命令

### 查看状态

```bash
jj log
jj diff
jj diff -r <change>
jj st
```

### 日常工作

```bash
jj describe -m "feat: ..."
jj new
jj new <change>
jj commit -m "feat: ..."
jj edit <change>
jj abandon
jj abandon <change>
```

### 整理历史

```bash
jj split
jj rebase -s <source> -d <destination>
jj rebase -d <destination>
jj undo
jj op log
jj op restore <operation-id>
```

### 远端同步

```bash
jj git fetch
jj bookmark track main@origin
jj bookmark create <name> -r @
jj bookmark set <name> -r <change>
jj git push
jj git push --bookmark <name>
jj git push --deleted
```

## 推荐工作流

### 开始下一个任务

```bash
jj new
jj describe -m "feat: ..."
```

### 被打断后切换紧急修复

```bash
jj new main
jj describe -m "fix: ..."
jj edit <之前的 change>
```

### 先做大改，后拆分

```bash
jj split
jj undo
```

### skeleton 规划

```bash
jj commit -m "refactor: ..."
jj commit -m "feat: ..."
jj commit -m "test: ..."
jj commit -m "docs: ..."
jj edit <第一个 change>
```

## FINALIZING 场景提示

- `ccclaw FINALIZING` 若触发 `jj rebase` 失败，先看 `jj log -r 'conflicts()'` 与 `jj st`
- 只有 `conflicts()` 非空时，才进入 `jj resolve`
- 若只是运行态文件、日志或 `var/results/*.stream.jsonl` 一类噪音，不要直接按冲突处理
- 若需要手工补推，可优先：

```bash
jj git fetch --remote origin
jj rebase -d main@origin
jj st
jj git push --remote origin --bookmark main
```

## 禁止事项

- 不在 `jj` 仓库里继续沿用 `git stash`
- 不把本地日常工作默认建成 Git 分支
- 不在未确认 `conflicts()` 前直接执行 `jj resolve`
- 不把运行态噪音误判成代码冲突

## 参考

- 官方文档：<https://jj-vcs.github.io/jj/>
- 原始 Skill：<https://github.com/onevcat/skills/tree/master/skills/onevcat-jj>
