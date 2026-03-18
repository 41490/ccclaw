# 260318_83_finalize_followup_recovery_probe

## 背景

- 关联 Issue: [#83](https://github.com/41490/ccclaw/issues/83)
- 前置报告: [260318_83_finalizing_visibility](./260318_83_finalizing_visibility.md)

上一轮已经解决了 `FINALIZING` 首轮失败不可见的问题，但仍有三个缺口：

1. `status` / slot 层没有显式 failure class，只能靠错误文本猜类型
2. finalize 失败恢复后，GitHub 外部只看到最终 DONE，不知道中间失败已经自动恢复
3. `jj/git` 兼容问题仍然要等到真正执行同步时才暴露，缺少前置诊断

## 本次实现

### 1. finalize failure 显式分类入 slot/status/reporter

本次在 `RepoSlot` 增加：

- `failure_class`
- `last_failure_step`
- `last_failure_class`
- `recovery_reported_at`

并补充分类枚举：

- `network`
- `conflict`
- `auth`
- `protection`
- `version_mismatch`
- `config`
- `issue_reporting`
- `unknown`

这样 `status --json` 与人类可读状态都可以直接看到当前 finalize failure class，不必再从 `last_error` 里猜。

同时：

- reporter 回帖新增 `失败类型`
- warning event detail 也写出 `失败类型`

结果是：

- slot/status 有结构化字段
- event/reporter 有清晰文本
- 三侧都能对齐到同一分类

### 2. finalize 自动恢复后补一条短回帖

本次新增 finalize recovery 回帖逻辑：

- 只在此前确实发生过 finalize failure 时触发
- 只回帖一次
- 不阻断最终 DONE 回帖

对应行为：

- 当 `sync_target` / `sync_home` 之类的收尾失败后续恢复成功
- 在最终 DONE 回帖前，先追加一条短说明：
  - 哪个步骤恢复了
  - 属于哪类 failure

同时写入一条 `EventUpdated`：

- `收尾恢复完成：此前 <step> 的失败类型 <class> 已恢复...`

这样 GitHub 观察者能看到“失败 -> 恢复 -> DONE”的完整外部闭环，而不是只看到最终成功。

### 3. `jj/git` 兼容前置探测

本次把探测下沉到 `src/internal/vcs/jj.go` 的同步入口，在真正开始 fetch/push 前先检查：

- `jj --version`
- `git --version`

当前规则：

- 若远端同步场景下检测到 git 版本低于 `2.41.0`
- 则直接返回 `ErrGitTooOld`
- 错误信息内显式带出当前 `git/jj` 版本与升级建议

这样像 `#82` 这种环境兼容问题，会在进入真实 fetch/rebase/push 前直接被标成 `version_mismatch`，而不是继续混进普通 retry。

## 测试

本次新增/更新测试覆盖：

- `status --json` 输出 `failure_class`
- 首轮 finalize failure 回帖包含 `失败类型`
- `version_mismatch` 分类会进入 `pause`
- finalize recovery 只回帖一次，并写入恢复事件
- `SyncRepo` 在 git 版本过低时会前置失败，不再继续 fetch

验证命令：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

结果通过。

## 取舍

### 1. recovery 回帖采用 best-effort

原因：

- 它是增强外部可见性的补充信号
- 不应反过来阻断最终 DONE 交付

因此当前实现是：

- recovery comment 失败会记录日志
- 但不会让任务重新卡死在 `FINALIZING`

### 2. version mismatch 先按 git 最低版本落规则

当前现场证据指向：

- `jj 0.39.0`
- `git 2.39.5`
- 失败提示明确要求 `2.41.0`

所以本次先落一个明确、稳定、可解释的门槛。后续若 jj 版本矩阵需要更细粒度管理，再把它扩展成 `jj/git` 对照表。

## 结论

本轮 follow-up 完成后，`FINALIZING` 链路已经同时具备：

- 首轮失败可见
- 失败类型可见
- 恢复成功可见
- 兼容性问题前置可见

相较上一轮，这次补齐的是“从首次失败到恢复成功”的完整可观测闭环。 
