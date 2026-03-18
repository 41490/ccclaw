# 260318_81_issue82_min_response_tracking

## 背景

- 关联 Issue：
  - [#81](https://github.com/41490/ccclaw/issues/81)
  - [#82](https://github.com/41490/ccclaw/issues/82)
- 本轮目标：
  - 追踪 `#82` 对应的 CCClaw 真实响应过程
  - 判断当前“核心最小响应流程”是否已经成立
  - 将关键证据链接回复到 `#81`

## 外部证据

- `#82` 任务入口：
  - <https://github.com/41490/ccclaw/issues/82>
- `#82` 审批评论：
  - <https://github.com/41490/ccclaw/issues/82#issuecomment-4082299700>
- `#81` 本轮追踪回帖：
  - <https://github.com/41490/ccclaw/issues/81#issuecomment-4082341706>
- 当前发布说明：
  - <https://github.com/41490/ccclaw/releases/tag/26.03.18.2026>

## 现场运行证据

### 1. systemd / ingest 时间线

根据本机 `journalctl --user -u ccclaw-ingest.service -u ccclaw-patrol.service`：

- `2026-03-18 08:55:17 EDT`
  - `Issue 已入队`
  - `issue=41490/ccclaw#82`
  - `state=NEW`
- `2026-03-18 08:55:23 EDT`
  - ingest 开始按仓调度执行
- `2026-03-18 08:56:46 EDT`
  - `stream-json 影子对账`
  - `task_id=41490/ccclaw#82#body`
  - `expected=done actual=done match=true`
- `2026-03-18 08:56:47 EDT`
  - ingest 结束，但任务未收口为 `DONE`

### 2. 本地产物已生成

本机存在以下产物：

- `~/.ccclaw/var/results/41490_ccclaw_82_body.stream.jsonl`
- `~/.ccclaw/var/results/41490_ccclaw_82_body.event.json`
- `~/.ccclaw/var/token-2026-W12.jsonl`

其中 `41490_ccclaw_82_body.event.json` 显示：

- `last_event = result`
- `task_state = FINALIZING`
- `current_step = sync_target`
- `result` 已包含针对 Issue #82 的最终答复正文

这说明 Claude 执行结果本身已经产出，不是“没跑起来”。

### 3. 运行态快照

`ccclaw status --json` 显示：

- `task_id = 41490/ccclaw#82#body`
- `state = FINALIZING`
- `slot.phase = finalize_failed`
- `current_step = sync_target`
- `report_issue = pending`
- `last_error = jj git push 重试耗尽: 拉取远端失败...`

`ccclaw stats --from 2026-03-18 --to 2026-03-18 --daily` 显示：

- 当日 `RUNS = 1`
- 唯一任务为 `41490/ccclaw#82`
- token 统计已落盘

### 4. GitHub 侧现状

截至本轮追踪结束：

- `#82` 仍为 `OPEN`
- `#82` 页面只有审批评论
- 尚未出现成功回帖
- 尚未出现 `/ccclaw [DONE]`

这说明最外层 GitHub 交付闭环没有完成。

## 根因定位

### 1. 现场可复现错误

本机执行：

```bash
jj --version
git --version
jj git fetch --remote origin
```

得到：

- `jj 0.39.0`
- `git version 2.39.5`
- `Error: Git does not recognize required option: porcelain (note: supported version is 2.41.0)`

这与历史现场记录一致，不是偶发网络抖动，而是稳定可复现的 `jj/git` 版本兼容问题。

### 2. 收尾顺序导致 GitHub 回帖被阻断

[src/internal/app/runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go) 中 `runFinalizeSteps()` 当前顺序是：

1. `sync_target`
2. `sync_home`
3. `report_issue`

因此只要 `sync_target` 失败，`report_issue` 就不会执行。

也就是说：

- 本地产物成功
- `stream-json` 对账成功
- token 统计成功

仍然可能因为前置 `jj` 同步失败而完全没有 Issue 回帖。

## 结论

### 1. 已成立的部分

`#82` 已证明以下“执行内核侧最小链路”成立：

- 非 maintain 成员 Issue 经审批后可入队
- `ingest` 能识别并调度任务
- `daemon + stream-json` 能完成执行
- 结果优先语义本轮未再误判为 `FAILED/DEAD`
- `result/event/token/status` 都能留下本地事实

### 2. 尚未成立的部分

当前“核心最小响应流程”**还没有完整成立**。

准确说法应是：

- 已成立到：
  - `审批 -> 入队 -> 执行 -> 本地产物落盘 -> FINALIZING`
- 尚未成立到：
  - `Issue 自动回帖 -> 追加 /ccclaw [DONE] -> DONE/关闭`

所以不能把 `#82` 视为一次完整通过的最小闭环验收。

## 优先优化建议

### 1. 将 `report_issue` 与 `sync_target/sync_home` 解耦

原因：

- 用户可见的第一成功标准是 GitHub 上有反馈
- 现在 target repo push 失败会把 GitHub 回帖一起阻断
- 导致外部观察者只能看到“没回应”，看不到“其实已执行完成”

### 2. 对 `jj/git` 兼容性做前置探测与明确降级

原因：

- 现场错误是稳定复现，不是暂时抖动
- 若仍按普通重试处理，会让任务长期停在 `FINALIZING`
- 还会延迟甚至抑制首轮失败说明回帖

### 3. 让 `FINALIZING` 失败更早暴露到 Issue

原因：

- 当前 `ReportFinalizing()` 受重试策略影响
- 像本轮这种环境兼容问题，Issue 页面不会第一时间留下失败说明
- 对使用者来说，这比显式失败更难排障

### 4. 细化 `sync_target` 失败分类

原因：

- 网络抖动
- branch protection
- 真实冲突
- `jj/git` 版本不兼容

这四类问题的处理动作完全不同，不应继续混在同一类 finalize failure 中。

## 本轮产出

- 已将关键证据和结论回帖到：
  - <https://github.com/41490/ccclaw/issues/81#issuecomment-4082341706>
