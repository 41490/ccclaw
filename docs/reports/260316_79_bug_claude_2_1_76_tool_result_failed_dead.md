# 260316_79：修复 Claude 2.1.76 tool_result 对话帧导致成功任务误判 FAILED/DEAD

## 背景

Issue #79 报告：在 release `26.03.16.1742` 上，最小案例 Issue（如 #78）在任务实际成功后，仍被 reporter 回帖为 `FAILED/DEAD`，且 GitHub 评论显示"阶段：收口阶段，错误：解析 Claude JSON 输出失败: stream-json 解码失败: 无法识别事件类型"。

## 根因分析

### 炸点：`normalizeEventKind()` 不认识 `type=user`

Claude 2.1.76 CLI 在 `--verbose` 模式（stream-json 格式）下，会额外输出完整对话帧，包括：

```json
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"hello","is_error":false}]}}
```

`resolveStreamEventKind()` 的识别链路为：

1. `event` 字段缺失 → `normalizeEventKind("")` 返回空
2. `subtype` 字段缺失 → 无法通过 subtype 分支识别
3. `rawType = "user"` → 不是 `"system"` → 通过
4. `is_error` 顶层字段为 false → 不触发 `StreamEventError`
5. `normalizeEventKind("user")` → **不在已知 case 列表中，返回空字符串**
6. `message` 字段为对象而非字符串 → `readStringField(payload, "message")` 静默失败，返回 ""
7. 所有后备检查均失败 → **返回 `"无法识别事件类型"` 错误**

注意：`"assistant"` 已在 case 列表中（映射为 `StreamEventProgress`），但 `"user"` 未加入，造成单点遗漏。

### 级联效应

1. `runSyncStreamJSON()` 第 9 行解析失败，`decodeErr` 被设置；
2. 后续行（包括末尾的成功 result 帧）仍写入 `rawStream`，但不再解析为 events；
3. `PersistStreamEventSnapshot()` 调用 `ParseStreamJSONL()` 同样因第 9 行失败而返回 nil snapshot；
4. `marshalCompatibleResultFromStreamSnapshot(nil, ...)` 返回错误；
5. `parseClaudeResult()` 失败，`parseErr` 被 `decodeErr` 覆写为 `"stream-json 解码失败: 无法识别事件类型"`；
6. `failTaskExecution()` 将任务状态置为 `FAILED/DEAD`，reporter 回帖。

### 误导现象

failure comment 中的"诊断"来自 `readDiagnosticTail(result.DiagnosticFile, 12)`，读取的是 `.diag.txt` 文件头部的 `hook_started` 等系统帧，并非真实炸点行（第 9 行），导致排障信息误导。

## 修复方案

### 最小修复（方案 A）

本轮按最小侵入原则补齐两层兼容：

1. `normalizeEventKind()` 将 `"user"` 纳入 `StreamEventProgress`，使 `type=user` 的 `tool_result`/`tool_use_result` 不再触发 fatal decode。
2. `readMessageDetail()` 与 `hasRecognizedMessageContent()` 新增对 `message.content[]` 的识别，至少兼容以下真实帧：
   - `thinking`
   - `text`
   - `tool_use`
   - `tool_result`
   - `tool_use_result`

这样即使未来某些帧的顶层 `type/event` 不够完整，只要 `message.content[]` 仍保留 Claude 当前真实形态，也会被降级识别为 `progress`，而不是整轮 stream 判坏。

- `assistant/user` 对话帧统一映射为 `StreamEventProgress`
- `message.content[]` 会提炼出可读详情，例如 `Claude 调用工具 Bash`、`工具返回结果: hi`
- 顶层 `is_error=true` 仍能正确触发 `StreamEventError`（该检查在 conversation fallback 之前执行）

### 回归验证

新增 fixture `testdata/stream_contract/tool_result_success.stream.jsonl`，模拟 #78 现场：

```
system/init → system/hook_started → assistant/thinking → assistant/tool_use → user/tool_result → result/success
```

预期聚合结果：`StateFinalizing`，result="任务已完成"，cos_usd=0.15。

新增单元测试 `TestParseStreamJSONLAcceptsClaudeConversationFrames`，直接验证该帧序列不再报错，且 `thinking/tool_use/tool_result` 详情被正确提取。

新增执行器回归 `TestLoadResultMaterializesClaudeConversationStreamJSONStdoutArtifacts`，直接覆盖 `stdout -> *.stream.jsonl -> *.event.json -> 兼容 result` 全链路，验证 success result 仍主导最终收口。

## 文件变更

| 文件 | 类型 | 说明 |
|------|------|------|
| `src/internal/executor/stream_contract.go` | 修复 | `normalizeEventKind()` 增加 `"user"`，并新增 `message.content[]` conversation fallback 与详情提炼 |
| `src/internal/executor/stream_contract_test.go` | 测试 | 新增/增强 `TestParseStreamJSONLAcceptsClaudeConversationFrames` |
| `src/internal/executor/executor_test.go` | 测试 | 新增 `TestLoadResultMaterializesClaudeConversationStreamJSONStdoutArtifacts` |
| `src/internal/executor/testdata/stream_contract/tool_result_success.stream.jsonl` | fixture | #78 场景脱敏复现 |
| `src/internal/executor/testdata/stream_contract/tool_result_success.event.json` | fixture | 对应期望聚合快照 |

## 测试结果

```
=== RUN   TestAggregateStreamEventsWithFixtures
=== RUN   TestAggregateStreamEventsWithFixtures/restart_error
--- PASS: TestAggregateStreamEventsWithFixtures/restart_error (0.00s)
=== RUN   TestAggregateStreamEventsWithFixtures/success
--- PASS: TestAggregateStreamEventsWithFixtures/success (0.00s)
=== RUN   TestAggregateStreamEventsWithFixtures/system_success
--- PASS: TestAggregateStreamEventsWithFixtures/system_success (0.00s)
=== RUN   TestAggregateStreamEventsWithFixtures/tool_result_success
--- PASS: TestAggregateStreamEventsWithFixtures/tool_result_success (0.00s)
--- PASS: TestAggregateStreamEventsWithFixtures (0.00s)
=== RUN   TestParseStreamJSONLAcceptsClaudeConversationFrames
--- PASS: TestParseStreamJSONLAcceptsClaudeConversationFrames (0.00s)
=== RUN   TestLoadResultMaterializesClaudeConversationStreamJSONStdoutArtifacts
--- PASS: TestLoadResultMaterializesClaudeConversationStreamJSONStdoutArtifacts (0.00s)
=== RUN   TestParseStreamJSONLRejectsUnknownEvent
--- PASS: TestParseStreamJSONLRejectsUnknownEvent (0.00s)
=== RUN   TestParseStreamJSONLAcceptsClaudeSystemEvents
--- PASS: TestParseStreamJSONLAcceptsClaudeSystemEvents (0.00s)
ok  github.com/41490/ccclaw/internal/executor  0.041s
```

## 验收标准覆盖

| 验收项 | 状态 |
|--------|------|
| `type=user`+`tool_result` 帧不再触发解码错误 | ✅ |
| 成功 result 帧在 tool_result 帧之后仍被正确识别 | ✅ |
| 同类 stream 不再被误报为 `hook_started` 导致失败 | ✅ |
| 旧有 fixture（success/system_success/restart_error）全部保持通过 | ✅ |
| `TestParseStreamJSONLRejectsUnknownEvent` 仍正确拒绝真未知事件 | ✅ |

## 遗留建议

### 方案 B（结果优先语义）

当 stream 中已出现 `type=result/subtype=success` 帧时，后续或前置的未知非错误帧不应让整轮任务判 FAILED。当前修复通过识别 `user` 帧间接解决了这个问题，但更健壮的做法是在 `runSyncStreamJSON` 中：若 `decodeErr != nil` 但 stream 末尾存在成功 result，仍从 rawStream 重建 snapshot。建议作为独立 Issue 跟进。

### 方案 C（失败评论诊断改进）

failure comment 的"诊断"字段目前截取文件头部，会把 `hook_started` 等无关帧混入。应改为：输出实际出错行号 + 出错行 `type/subtype`。建议作为独立 Issue 跟进。

## 关联

- 本修复是 #75（system/* 兼容）的延续，覆盖 live conversation frames
- 复现现场：#76 comment 4066431491、#78 stream 文件第 9 行
