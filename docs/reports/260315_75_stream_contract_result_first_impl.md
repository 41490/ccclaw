# 260315_75_stream_contract_result_first_impl

## 背景

Issue [#75](https://github.com/41490/ccclaw/issues/75) 已拍板四项动作：

1. 收口判定采用“结果优先”
2. 显式兼容 Claude Code 2.1.76 新增 `system/hook_started`、`system/hook_response`、`system/init`
3. Issue 生命周期评论统一由 reporter 输出，任务执行体不再直接触发 `gh issue comment`
4. 对 #72 / #74 补充“历史误判说明”

本轮实现聚焦代码与回归测试；第 4 项经核查已在现场完成补充留痕，无需重复回帖。

## 实现

### 1. stream 契约兼容新系统事件

修改 [stream_contract.go](/opt/src/ccclaw/src/internal/executor/stream_contract.go)：

- 新增 `StreamEventSystem`
- 保留 `subtype`，把 `system/init`、`system/hook_started`、`system/hook_response` 显式映射进契约
- 未知 `system/*` 不再直接判解析失败，而是降级为 `WARNING`

效果：

- Claude Code 2.1.76 输出的新 `system/*` 事件可以进入 `*.event.json`
- `stream.jsonl` 不会因为这些事件整批失效

### 2. 收口改为结果优先

同文件中补了快照映射覆盖规则：

- 已出现 success result 且未出现显式 error 时，后续 `system/*` 不再覆盖 `FINALIZING`
- 显式 error 仍保持最高优先级
- `CurrentStep` 也同步遵循该优先级，避免 result 之后又被 system 事件改回运行步骤

另外在 [stream_artifact.go](/opt/src/ccclaw/src/internal/executor/stream_artifact.go) 收紧兼容结果生成条件：

- 只有快照里存在终态 `result/error` 才允许生成兼容 `*.json`
- 防止“只有 system/progress 无终态”被误包装成 success

### 3. 统一 Issue 回帖出口

修改 [runtime.go](/opt/src/ccclaw/src/internal/app/runtime.go) 的执行 prompt：

- 明确禁止任务执行体直接执行 `gh issue comment`、`gh issue close`
- 要求把对外结论写入工程报告与任务摘要
- 生命周期回帖统一交给 reporter，并由 reporter 追加 `/ccclaw [DONE]`

这一步不改变 reporter 现有实现，只收紧 prompt 边界，避免再次出现“普通评论成功但生命周期收口失败”的混乱现场。

## 测试

新增 fixture：

- [system_success.stream.jsonl](/opt/src/ccclaw/src/internal/executor/testdata/stream_contract/system_success.stream.jsonl)
- [system_success.event.json](/opt/src/ccclaw/src/internal/executor/testdata/stream_contract/system_success.event.json)

新增/更新测试：

- [stream_contract_test.go](/opt/src/ccclaw/src/internal/executor/stream_contract_test.go)
  - 覆盖 Claude `system/*` 事件兼容
  - 覆盖 success result 后未知 system 事件不再翻转收口
- [executor_test.go](/opt/src/ccclaw/src/internal/executor/executor_test.go)
  - 覆盖 `LoadResult()` 对带 `system/*` 的 `stream-json` 收口
- [runtime_test.go](/opt/src/ccclaw/src/internal/app/runtime_test.go)
  - 覆盖 prompt 中“禁止直接 Issue 回帖、统一由 reporter 回帖”的约束

执行结果：

```bash
cd /opt/src/ccclaw/src && go test ./internal/executor
cd /opt/src/ccclaw/src && go test ./internal/app
```

均通过。

## 结论

本轮已完成 #75 的 1/2/3 三项代码落地，并确认第 4 项历史说明已在 #72 / #74 现场补齐。

修复后，`ccclaw` 对 Claude Code 2.1.76 的 `system/*` stream 事件具备兼容能力，success result 不会再被未知 system 事件误翻成 `FAILED/DEAD`，Issue 生命周期评论出口也重新收敛到 reporter 单一路径。
