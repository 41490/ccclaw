# 260318_83_finalizing_visibility

## 背景

- 关联 Issue: [#83](https://github.com/41490/ccclaw/issues/83)
- 父问题追踪: [260318_81_issue82_min_response_tracking](./260318_81_issue82_min_response_tracking.md)

`#82` 暴露出一个关键问题：Claude 执行结果已经产出，但因为 `sync_target` 收尾失败，任务停在 `FINALIZING`，Issue 页面首轮完全无痕迹。现有逻辑把 `ReportFinalizing()` 也纳入 finalize retry 抑制，导致“已执行但未交付”的事实对外不可见。

## 本次调整

### 1. 首轮 finalize_failed 强制回帖

调整 `src/internal/app/runtime_cycle.go`：

- 首次进入 `finalize_failed` 且尚无成功回帖记录时，不再受 retry 模式抑制
- 首轮回帖的职责从“错误升级通知”改为“可见性保障”
- 回帖成功后继续用 `LastReportedFailure` 按 failure key 去重

这保证了 Issue 观察者第一次就能看到：

- 执行结果已经产出
- 当前卡在哪个收尾步骤
- 系统下一步会自动重试，还是建议人工介入

### 2. warning/event 文案改为“执行已完成但收尾失败”

`status` 的 alerts 来自事件流，因此单改 reporter 不够。

本次将 finalize failure 的 warning detail 统一为：

- `执行结果已产出，收尾步骤 <step> 失败: <error>`

这样三侧口径保持一致：

- `status`：看到 `FINALIZING + finalize_failed + current_step + last_error`
- `events` / alerts：看到“执行已完成，但收尾步骤失败”
- reporter：Issue 页面看到同样的失败语义与下一步建议

### 3. reporter 文案区分“执行失败”与“交付收尾失败”

调整 `src/internal/adapters/reporter/reporter.go`：

- 标题改为“任务执行已完成，但交付收尾失败”
- 明确写出 `执行结果: 已产出`
- 失败步骤显示为 `sync_target` / `sync_home` / `report_issue`，并附中文语义
- 建议区块统一承载自动重试时间或人工介入提示

## 测试补充

新增并更新 `src/internal/app/runtime_test.go` 覆盖：

- 首轮 finalize failure 必须触发 Issue 回帖
- 同一 failure key 重试不重复刷屏
- failure key 变化时允许追加一次新回帖
- warning 事件文案包含“执行结果已产出，收尾步骤失败”

## 验证

在 `src/` 目录执行：

```bash
go test ./internal/app
```

结果通过。

## 结论

本次改动把“首轮对外可见”从 retry 抑制里解耦出来，优先修复了 `FINALIZING` 长时间无回帖的根因，同时保留后续 failure key 去重，避免同一错误在自动重试中高频刷屏。
