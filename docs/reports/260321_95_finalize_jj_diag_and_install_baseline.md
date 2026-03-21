# 260321_95_finalize_jj_diag_and_install_baseline

## 背景

- 对应 Issue：[#95](https://github.com/41490/ccclaw/issues/95)
- 用户追加要求：
  - 用 `gh` 获取 `#95` 并开始实施
  - 学习 `onevcat-jj` Skill
  - 将 `jj` 与 `onevcat-jj` 一并纳入 CCClaw 安装与升级流程
  - 确保 CCClaw 主机后续日常本地版本管理统一以 `jj` 为事实基线

本次不是单点修补，而是同时收敛两条线：

1. `FINALIZING -> sync_target/sync_home` 的 `jj` 同步诊断、失败分类与短复查策略
2. 安装/升级流程中的 `jj` 基线与 `onevcat-jj` 受管 Skill 发放

## 外部参考

### Issue 决策

使用：

```bash
gh issue view 95 --repo 41490/ccclaw
```

确认 `#95` 目标集中在：

- 为 `jj` 收尾同步保留原始命令与输出
- 只在证据充分时归类为真实冲突
- 对未确认冲突的 `sync_home` 失败先短复查
- 明确区分业务成功、收尾状态与生命周期收口

### onevcat-jj Skill

参考：

- <https://github.com/onevcat/skills/tree/master/skills/onevcat-jj>
- <https://raw.githubusercontent.com/onevcat/skills/master/skills/onevcat-jj/SKILL.md>

吸收的关键原则：

- 在存在 `.jj/` 的仓库中，本地版本管理统一优先 `jj`
- `bookmark` 只在需要推送远端时使用
- 不把 `git add` / `git commit` / `git stash` 继续带入 `jj` 工作流
- 只有在 `conflicts()` 真实非空时才进入 `jj resolve`

## 实施内容

### 1. `jj` 同步改为可回放诊断

修改：

- `src/internal/vcs/jj.go`

新增：

- `SyncRepoWithReport`
- `SyncReport`
- `CommandDiagnostic`

效果：

- `fetch` / `rebase` / `push` / `st` / `log -r conflicts()` 等命令现在会记录：
  - 原始命令行
  - workdir
  - exit code
  - stdout
  - stderr
  - 起止时间与耗时

### 2. `rebase` 失败前先做二次确认

修改：

- `src/internal/vcs/jj.go`
- `src/internal/adapters/storage/slot_store.go`
- `src/internal/app/runtime_cycle.go`

新增分类：

- `conflict`
- `dirty_working_copy`
- `oversize_untracked`
- `unknown`

当前规则：

- `conflicts()` 非空才归类为真实冲突
- `jj st` 呈现工作区脏变更时归类为 `dirty_working_copy`
- `jj st` 呈现 `var/results/*.stream.jsonl`、`*.event.json`、`*.diag.txt` 等运行态噪音时归类为 `oversize_untracked`
- 证据不足时保守落到 `unknown`

因此系统不再把“`rebase` 失败 + 无冲突证据”一律错误引导到 `jj resolve`

### 3. `FINALIZING` 补齐任务级诊断落盘

修改：

- `src/internal/app/runtime.go`
- `src/internal/app/runtime_cycle.go`

新增落盘文件：

- `~/.ccclaw/var/results/<task>.finalize.event.json`
- `~/.ccclaw/var/results/<task>.finalize.diag.txt`

用途：

- JSON 保存结构化事件与 `SyncReport`
- 文本文件保留人工排查友好的逐条命令输出
- sidecar 新增 `finalize_event_file` / `finalize_diag_file`
- reporter / hints 可直接引用这些路径

### 4. `sync_home` 未确认冲突时先短复查

修改：

- `src/internal/app/runtime_cycle.go`

策略变化：

- `network` 仍按原有 retry 逻辑处理
- `sync_home + dirty_working_copy`
- `sync_home + oversize_untracked`
- `sync_home + unknown`

以上场景先进入短间隔 retry，而不是直接 1 小时 pause

### 5. 对外口径显式区分三层状态

修改：

- `src/internal/adapters/reporter/reporter.go`
- `src/internal/app/runtime.go`

新增口径：

- `业务执行: 成功`
- `收尾同步: 待复查/失败`
- `生命周期: 未写回 DONE`

`status` sidecar 视图也新增：

- `execution_result`
- `finalize_status`
- `lifecycle_state`

### 6. 将 onevcat-jj 受管化并纳入安装/升级

新增：

- `src/dist/kb/skills/L1/onevcat-jj/CLAUDE.md`

修改：

- `src/dist/install.sh`
- `src/dist/upgrade.sh`
- `README.md`
- `src/ops/examples/install-flow.md`

实现方式：

- 将外部 `onevcat-jj` Skill 本地化为 CCClaw 受管 Skill
- 通过现有 `dist/kb -> home_repo/kb` 机制随首装与升级统一发放
- 安装脚本在 `jj git init --colocate` 后，若发现 `origin/HEAD`，自动执行默认远端 bookmark 跟踪
- 安装流程文案明确：
  - `jj` 为必装基线
  - `onevcat-jj` 为默认发放 Skill
  - 后续本地版本管理统一走 `jj`

## 验证

执行：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

结果：

- 全量 Go 测试通过

另执行：

```bash
bash -n src/dist/install.sh
bash -n src/dist/upgrade.sh
```

结果：

- 安装与升级脚本语法检查通过

## 结论

本次已把 `#95` 的核心诉求真正落到实现：

- `FINALIZING` 的 `jj` 操作具备可回放证据
- 无真实 `conflicts()` 时不再误判为冲突
- `sync_home` 的未确认失败先短复查
- reporter / status 明确区分业务成功、收尾状态和生命周期收口

同时也把 `jj` 与 `onevcat-jj` 纳入 CCClaw 安装/升级基线，后续主机上的知识仓库与任务仓库都将以 `jj` 作为本地版本管理默认事实面。
