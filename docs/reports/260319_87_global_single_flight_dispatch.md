# 260319_87_global_single_flight_dispatch

## 背景

- 关联 Issue: [#87](https://github.com/41490/ccclaw/issues/87)
- 拍板评论:
  - <https://github.com/41490/ccclaw/issues/87#issuecomment-4090885176>

`#87` 最新拍板已经明确：

1. 使用 `S1: 全局单飞串行`
2. `sync_target` 失败时继续阻塞后续队列
3. `/ccclaw [DONE]` 仍是唯一完成标记
4. Claude 任务之间必须从新会话开始，避免跨 Issue 继承上下文

当前仓内实现与该决策的主要偏差，是 `ingest` 仍按 `target_repo` 分仓选择空槽位发射任务。这样会让不同仓在同一轮里并发发射，与“全局只跑 1 个 Issue”的拍板不一致。

## 本轮目标

本轮只收敛已经拍板的最小核心差异：

- 发射逻辑从“按仓找空槽位”改为“全局最多发射 1 个任务”
- 保留既有 `slot/finalize_failed/sync_target` 阻塞语义
- 不在本轮顺手改写 `slot` 存储模型
- 不在本轮扩展未拍板的 issue 路由或 finalize 解耦

## 实现

### 1. 发射逻辑改为全局单飞

修改 [runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go)：

- `runIngestCycle()` 不再调用按仓分发逻辑
- 新增 `dispatchNextSingleFlight()`
- 在发射前先读取当前全部 slot
  - 只要还有任意活动 slot，本轮就直接停止发射
- 若当前无 slot，则从全部 runnable 任务中按既有顺序只取 1 个发射
- 即使该任务在同一轮内已完成收口，也不会继续补发第二个任务，确保“每轮最多发射 1 个”

这意味着：

- 同仓任务不会再靠 repo slot 局部串行
- 跨仓任务也不再并行发射
- `sync_target` / `sync_home` / `mark_done` 仍继续占住全局发射窗口，直到收尾完成或进入人工介入状态

### 2. 运行日志文案同步到新口径

修改：

- [runtime.go](/opt/src/ccclaw/src/internal/app/runtime.go)
- [runtime_logging_test.go](/opt/src/ccclaw/src/internal/app/runtime_logging_test.go)
- [main_test.go](/opt/src/ccclaw/src/cmd/ccclaw/main_test.go)

把运行日志中的“按仓 ingest 调度”改为“全局单飞 ingest 调度”，避免状态输出继续误导运维判断。

### 3. 新增回归测试

新增 [runtime_test.go](/opt/src/ccclaw/src/internal/app/runtime_test.go) 用例：

- `TestRunIngestCycleSingleFlightAcrossTargets`

覆盖场景：

- 两个不同 `target_repo` 的 runnable 任务同时存在
- 执行器使用 `daemon`
- 第一轮 `runIngestCycle()` 后，只有第一个任务完成，第二个仍保持 `NEW`
- 第二轮再执行一次后，第二个任务才会被发射并完成

这个用例直接卡住了“跨仓也不能并发发射”的拍板要求。

## 验证

### 已通过

执行：

```bash
cd /opt/src/ccclaw/src
go test ./internal/app -run 'TestRunIngestCycleSingleFlightAcrossTargets|TestRunIngestCycleDaemonModeDoesNotDependOnTMuxPatrol|TestRunLogsRespectRuntimeLevel'
```

结果通过。

### 当前全量回归现场

执行：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

本轮修正了 `cmd/ccclaw` 中由日志文案变更引起的断言差异，但全量测试仍暴露两条与本次调度改造无直接关系的既有失败：

- `TestPatrolFallsBackToDiagnosticFileWhenStructuredResultMissing`
- `TestPatrolUsesLogTailWhenMissingSessionResultIsEmpty`

两条失败都集中在 `patrol -> load_result` 的诊断回退链，现象是最终错误文本只保留了“结构化结果缺失”，没有保留测试期望的诊断尾部信息。这不是本轮“全局单飞串行”逻辑引入的新调度分歧，需单独开口追查。

## 结论

本轮已把 `#87` 拍板的核心调度约束落到代码：

- `ccclaw` 发射改为全局单飞串行
- 任意活动 slot 都会阻塞后续任务发射
- 跨 `target_repo` 也不再并发执行
- 既有 `sync_target` 失败阻塞策略保持不变

未在本轮处理的内容：

- slot 存储从“按 repo 命名”进一步收缩为“全局唯一 sidecar”
- issue 路由观测模型的更大调整
- `patrol` 诊断回退链的既有测试失败
