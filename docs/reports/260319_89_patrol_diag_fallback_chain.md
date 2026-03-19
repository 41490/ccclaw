# 260319_89_patrol_diag_fallback_chain

## 背景

Issue #89 要求修复 `patrol -> load_result -> finalize/failTaskExecution` 诊断回退链，确保结构化结果缺失时，最终 `FAILED/DEAD`、事件详情与回帖文案仍能带出真实可排障信息，而不是只剩抽象的“结构化结果缺失”。

现场失败主要表现为两类：

- `diag` 文件已有真实错误，但最终 `ErrorMsg` 只剩 `结构化结果缺失`
- `diag` 为空、只能从日志尾部取诊断时，最终错误仍退化成抽象提示

## 根因

本次确认到两个叠加根因：

1. `src/internal/executor/executor.go` 中 `LoadResult()` 在回退到 `diag/log tail` 后，返回的 `error` 仍只保留“结构化结果缺失/解析失败”语义，没有把真实诊断摘要拼进错误文本。
2. `src/internal/app/runtime_cycle.go` 中 `runIngestCycle()` 虽然先 `advanceRepoSlots()`，但 `dispatchNextSingleFlight()` 把 `advanced` 参数直接丢弃，导致同一轮刚收口失败的任务在槽位删除后又被立刻重发；新的 tmux 发射会先清空旧工件，进而覆盖掉上一次 patrol 已捕获的真实诊断。

这也是为什么测试里最终看到的是 `tmux 会话发射后立即退出/丢失` 之类的二次错误，而不是第一次 `patrol -> load_result` 收口时读到的真实 `diag` 内容。

## 处理

### 1. 给结构化结果错误补上诊断摘要

在 `src/internal/executor/executor.go` 中：

- 为 `LoadResult()` 增加 `loadStructuredResultDiagnosticSummary()`，优先读取 `diag`，为空时再回退 `log tail`
- 扩展 `wrapStructuredResultError()`，在“结构化结果缺失/解析失败”语义后追加 `诊断输出: ...`
- 保持原有 `stream-json / json / stdout` 兼容回退链不变，只增强错误文本可观测性

这样即使上层只消费 `runErr`，也不会再丢失关键诊断。

### 2. 统一 patrol 巡检分支的 pane dead 诊断拼装

在 `src/internal/app/runtime_cycle.go` 中：

- 将 `patrolRepoSlots()` 里“session missing / pane dead”分支统一改为调用 `formatLaunchProbeFailure()`
- 保证 patrol 巡检发现异常时，`slot.LastError` 与 warning 事件和“发射后立即退出”分支使用同一套诊断拼装口径

### 3. 恢复同轮单飞门禁

同样在 `src/internal/app/runtime_cycle.go` 中：

- 恢复 `dispatchNextSingleFlight()` 对 `advanced` 的使用
- 只要本轮已经推进过任意槽位，就不再在同一轮继续发射新任务

这一步是为了避免“刚 finalize 失败就同轮重发”，把上一轮已采集到的 `diag/log tail` 用新空工件覆盖掉；也与 `Issue #87` 的全局单飞串行约束保持一致。

## 验证

执行了以下定向回归：

```bash
go test ./internal/executor -run 'TestLoadResultFallsBackToDiagnosticFile|TestLoadResultUsesLogTailWhenStructuredResultMissing|TestLoadResultMovesInvalidTMuxStdoutIntoDiagnosticFile'
go test ./internal/app -run 'TestIngestDispatchFinalizesImmediatelyWhenSessionMissingRightAfterLaunch|TestPatrolFallsBackToDiagnosticFileWhenStructuredResultMissing|TestPatrolUsesLogTailWhenMissingSessionResultIsEmpty|TestRunIngestCycleSingleFlightAcrossTargets'
```

结果均通过。

## 结果

修复后：

- 结构化结果缺失时，最终 `ErrorMsg` 能同时保留语义提示与真实诊断
- `diag` 为空时，仍能回退到 `log tail`
- patrol 巡检发现 pane dead 后，不会因为同轮重发而覆盖第一次收口拿到的诊断
- 全局单飞语义保持一致：本轮一旦推进过槽位，不再继续发射新任务
