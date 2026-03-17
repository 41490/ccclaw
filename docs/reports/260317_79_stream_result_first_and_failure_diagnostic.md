# 260317_79：补齐结果优先语义与失败评论精确诊断

## 背景

`2026-03-16` 已先完成 #79 的方案 A + D：

- 补齐 `assistant/user` 与 `message.content[]` 已知 live frame 的 stream 契约
- 新增 #78 脱敏 fixture 与基础回归

但 #79 里剩余两项仍未真正收口：

- 方案 B：`stream-json` 中若已出现标准 `result/success`，未知普通帧不应再把整轮任务翻成失败
- 方案 C：失败评论不能再只靠诊断文件前缀猜根因，必须直接给出真实出错行与帧类型

## 本轮目标

1. 在 `executor.runSyncStreamJSON()` 中落实“结果优先”语义
2. 让解码错误天然携带：
   - 实际出错行号
   - top-level `event/type/subtype`
   - `message.content[].type`
3. 让 runtime/reporter 最终拿到的失败信息直接可用于 Issue 排障，而不是继续贴 `hook_started | hook_started | ...`

## 实现说明

### 1. 结果优先：不再被首个 decodeErr 锁死

此前 `runSyncStreamJSON()` 的问题有两层：

1. 一旦某一行 `parseStreamLine()` 失败，`decodeErr` 被置位后，后续行将不再继续解析
2. 即使后面已经有标准 success result，最终仍会被 `decodeErr` 覆写成整轮失败

本轮调整为：

- 扫描器在遇到单行解码失败后继续解析后续行
- 先基于已成功解析的 events 聚合 `partialSnapshot`
- 若 `partialSnapshot` 已表明：
  - 有成功 result
  - 无 error result

则视为 **stream result 已具备最终事实优先级**，此时：

- 原始 `*.stream.jsonl` 仍照常落盘
- `*.event.json` 直接基于已解析 events 落盘
- 不再让单个未知普通帧把整轮任务判成 FAILED/DEAD
- 即使 Claude 进程最终退出非零，只要 stream 中已有明确 success result，也不再强制判失败

### 2. 失败评论诊断：直接带真实帧上下文

此前 failure comment 的误导根因是：

- `task.ErrorMsg` 中只有“无法识别事件类型”
- `diagnostic` 字段则来自诊断文件前缀
- 因此评论常常只显示 `hook_started | hook_started | ...`

本轮在 `parseStreamLine()` 失败处新增结构化上下文包装，错误文本会直接带出：

- `解析 stream-json 第 N 行失败`
- `event=...`
- `type=...`
- `subtype=...`
- `content[].type=...`

例如：

```text
解析 stream-json 第 2 行失败: type=debug content[].type=meta_trace: 无法识别事件类型
```

同时，为避免把纯文本 shell/stderr 误改成 JSON 诊断：

- 只有当解码错误中确实出现 `event/type/subtype/content[].type` 等结构化标记时
- 才会把该上下文作为 failure comment 的主诊断
- 对 `claude: not found` 这类非 JSON 原始错误，仍保留原始诊断输出

## 文件变更

| 文件 | 类型 | 说明 |
|------|------|------|
| `src/internal/executor/stream_contract.go` | 修复 | 未知 stream 帧报错时补充 `event/type/subtype/content[].type` 上下文 |
| `src/internal/executor/stream_artifact.go` | 修复 | 新增基于已解析 events 的 stream snapshot 落盘能力 |
| `src/internal/executor/executor.go` | 修复 | 实现结果优先语义，并仅在结构化 decodeErr 场景下覆写失败诊断 |
| `src/internal/executor/stream_contract_test.go` | 测试 | 断言未知帧错误信息包含行号与帧类型 |
| `src/internal/executor/executor_test.go` | 测试 | 覆盖 success result 压住未知普通帧，以及 failure 时返回精确 decode 上下文 |
| `src/internal/app/runtime_test.go` | 测试 | 断言 runtime 不再把结构化 decode 上下文重复降解成文件前缀诊断 |

## 回归测试

执行：

```bash
go test ./internal/executor ./internal/app ./internal/adapters/reporter
go test ./...
```

本轮新增覆盖点：

- `TestExecutorRunStreamJSONPrefersSuccessResultOverUnknownFrame`
- `TestExecutorRunStreamJSONFailureCarriesPreciseDecodeContext`
- `TestFormatExecutionFailureKeepsStructuredStreamDecodeContext`

## 验收对应关系

| #79 要求 | 本轮状态 |
|---------|---------|
| 已出现标准 success result 时，未知普通帧不再翻失败 | ✅ |
| 失败评论输出真实出错行号 | ✅ |
| 失败评论输出真实 `type/subtype` | ✅ |
| 若为 content frame，输出 `content[].type` | ✅ |
| 不再继续误导成 `hook_started` 前缀问题 | ✅ |

## 后续建议

1. 若后续 Claude 再加入新的 conversation frame，优先先补 `stream_contract` 识别，再评估是否需要扩展“结果优先”白名单。
2. 可进一步把精确解码上下文落到 `*.event.json` 的 warning/event 链路，便于 `status/stats` 与 `sevolver` 做聚合分析。
3. 若未来需要更细粒度排障，可在 failure comment 中增加“原始出错行片段（脱敏后）”，但应避免直接回帖整段原始流。
