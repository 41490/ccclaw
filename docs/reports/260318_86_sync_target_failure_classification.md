# 260318_86_sync_target_failure_classification

## 背景

- 关联 Issue: [#86](https://github.com/41490/ccclaw/issues/86)
- 父 Issue: [#81](https://github.com/41490/ccclaw/issues/81)
- 现场样本: [#82](https://github.com/41490/ccclaw/issues/82)
- 前置报告:
  - [260318_81_issue82_min_response_tracking](./260318_81_issue82_min_response_tracking.md)
  - [260318_85_report_issue_sync_decouple](./260318_85_report_issue_sync_decouple.md)

前两轮已经把 `FINALIZING` 的可见性补齐，但 `sync_target` 的失败分类仍有一个根因缺口：

- `status` / reporter 已能显示 failure class
- 但 `vcs.SyncRepo()` 仍主要返回普通文本
- `network / auth / protection / unknown` 还要在 finalize 层继续猜字符串

这会带来两个问题：

1. 自动策略无法稳定区分“该重试”还是“应立刻停下等人工”
2. `status --json`、alerts、Issue 回帖虽然有分类字段，但根因来源不够可信

## 本次实现

### 1. `vcs.SyncRepo()` 下沉结构化远端同步错误

本次在 [jj.go](/opt/src/ccclaw/src/internal/vcs/jj.go) 增加：

- `ErrSyncNetwork`
- `ErrSyncAuth`
- `ErrSyncProtection`
- `ErrSyncUnknown`
- `SyncCommandError`
- `SyncRetryError`

处理方式调整为：

- `fetch/push` 失败后先做 capability mismatch 判定
- 再做远端同步分类
- 只有 `network` 类继续按 `maxRetry` 重试
- `auth/protection/unknown` 在首轮就停止，不再无意义重试
- 重试耗尽时通过 `SyncRetryError` 保留底层分类，不再把根因压扁成一条字符串

这样 finalize 层可直接 `errors.Is()` 判断，不必继续从 `err.Error()` 里反推根因。

### 2. finalize 状态增加 `failure_mode`

本次在 [slot_store.go](/opt/src/ccclaw/src/internal/adapters/storage/slot_store.go) 新增：

- `FinalizeFailureModeRetry`
- `FinalizeFailureModePause`
- `RepoSlot.FailureMode`

在 [runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go) 中：

- `network` 映射到 `retry`
- `capability/auth/protection/conflict/unknown` 映射到 `pause`
- 当自动重试超过阈值后，会从 `retry` 升级成 `pause`

因此 `status --json` 不只知道“失败类型”，也知道“接下来系统会自动重试还是等人工处理”。

### 3. hints / alerts / reporter 三侧统一带出“处理策略”

本次同步更新：

- [runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go)
  - `buildFinalizeHints()` 按 `network/version/conflict/protection/auth/config/unknown` 输出更具体建议
  - `buildFinalizeFailureEventDetail()` 新增 `处理策略`
- [runtime.go](/opt/src/ccclaw/src/internal/app/runtime.go)
  - `status --json.slots.items[]` 新增 `failure_mode`
  - 人类可读 `status` 表格新增 `MODE_HINT`
- [reporter.go](/opt/src/ccclaw/src/internal/adapters/reporter/reporter.go)
  - `ReportFinalizing()` 新增 `处理策略`

现在三侧语义一致：

- `status` 看 `failure_class + failure_mode`
- `alerts` 看 `失败类型 + 处理策略`
- Issue 回帖也能直接看到 `自动重试` 或 `需人工介入`

### 4. 新增覆盖典型错误样本的单测

本次补充测试覆盖：

- `branch protection` 首轮停止，不继续重试
- `authentication failed` 首轮停止，不继续重试
- `network timeout` 重试耗尽后仍保留 `network` 分类
- `unknown` 失败不再混入网络重试
- `status --json` 输出 `failure_mode`
- finalize warning event / reporter 回帖包含 `处理策略`

主要测试文件：

- [jj_test.go](/opt/src/ccclaw/src/internal/vcs/jj_test.go)
- [runtime_test.go](/opt/src/ccclaw/src/internal/app/runtime_test.go)
- [reporter_test.go](/opt/src/ccclaw/src/internal/adapters/reporter/reporter_test.go)

## 验证

执行：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

结果通过。

## 取舍

### 1. `unknown` 当前先按 `pause` 处理

原因：

- 当前已把常见 transient network 特征独立出去
- 剩余无法归类的错误继续自动重试，噪音风险高于收益

因此本轮选择：

- `unknown` 首轮显式暴露
- 交给人工复现与补充样本

后续若现场样本足够稳定，再拆成新的结构化类别。

### 2. `auth` 与 `protection` 仍分开保留

Issue 最低标准把它们合并为一类即可，但本轮仍维持细分，原因是：

- `auth` 主要看凭据、仓库访问权限、remote 配置
- `protection` 主要看 branch rules 与交付路径是否需要改走 PR

两者停止重试的策略一致，但排障动作不同，拆开更利于后续自动化。

## 结论

#86 本轮完成后，`sync_target` 失败分类已经从“上层文本猜测”推进到“`vcs` 根因结构化输出”，并贯通到：

- finalize policy
- status JSON / 人类可读 status
- alerts
- reporter finalize 回帖

这意味着系统现在不仅能告诉用户“同步失败了”，还能更稳定地说明：

- 失败属于哪一类
- 系统接下来会自动重试还是停下等待人工
- 用户下一步应该优先检查什么
