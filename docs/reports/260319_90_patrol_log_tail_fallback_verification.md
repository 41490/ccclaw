# 260319_90_patrol_log_tail_fallback_verification

## 背景

Issue #90 要求修复 `patrol` 在 `session missing + result empty + diag empty` 场景下未回退 `log tail` 的问题，避免最终错误退化成抽象的“会话丢失/结构化结果缺失”。

本次先按仓库约束回看 Issue，再对当前 `main` 做现场复核，判断是否仍存在待修代码，而不是直接重复补丁。

## 结论

当前 `main` 已满足 Issue #90 的验收标准，本轮未再发现需要追加的代码修复。

换句话说，Issue #90 描述的问题已经在仓内现状被覆盖修复，但 Issue 状态尚未与代码事实同步收敛。

## 为什么当前已经满足

结合 `src/internal/app/runtime.go`、`src/internal/app/runtime_cycle.go` 与 `src/internal/executor/executor.go` 的现状，当前链路已经具备以下行为：

- `LoadResult()` 在结构化结果缺失时，会优先尝试从 `diag` 摘要取诊断；若 `diag` 为空，会继续回退 `log tail`
- `enrichDiagnosticResult()` 在 `result == nil` 或 `result.Output` 为空时，同样会按 `diag -> log tail` 顺序补齐诊断输出
- `formatExecutionFailure()` 在主错误文本之外，会将 `result.Output` 或日志尾部摘要拼入最终 `ErrorMsg`
- `formatLaunchProbeFailure()` 只在探测阶段补充诊断，不再阻断最终失败链路从 `diag/log tail` 提取真实原因

这意味着在 Issue #90 指定的场景下，即使 `tmux session` 已经缺失，只要日志文件里还有真实启动失败信息，最终 `ErrorMsg` 仍能拿到日志尾部摘要，而不会只剩“会话丢失”。

## 与 Issue #89 的关系

复核结果表明，Issue #90 实际上是 `patrol` 诊断回退链收敛工作里的更细一条现场用例；其修复效果已经包含在 Issue #89 对以下链路的治理中：

- 结构化结果缺失时，错误文本追加真实诊断摘要
- `diag` 为空时继续回退 `log tail`
- patrol 收口与最终失败文案复用统一诊断拼装口径

因此本轮没有继续改代码，而是补做一次针对 Issue #90 的独立验收记录，避免后续重复判断。

## 验证

本次执行了以下验证：

```bash
go test ./internal/app -run 'TestPatrolUsesLogTailWhenMissingSessionResultIsEmpty|TestPatrolMarksTaskFailedWhenStructuredResultMissingAfterSessionLost' -count=1
go test ./...
```

验证结果：

- `TestPatrolUsesLogTailWhenMissingSessionResultIsEmpty` 已通过
- 全量 `go test ./...` 通过
- 当前仓库工作树在本轮开始前为干净状态

## 后续建议

- Issue #90 可直接按“主干已满足验收标准”收敛
- 若需要保留审计痕迹，建议在 Issue 中补一条说明：当前主干已由 Issue #89 相关修复覆盖，引用本报告作为验收记录
