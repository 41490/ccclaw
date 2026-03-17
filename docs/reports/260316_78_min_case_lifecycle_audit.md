# 260316_78_min_case_lifecycle_audit

对应 Issue：[#78](https://github.com/41490/ccclaw/issues/78) `try: 普通又叕发愿`

- 报告日期：2026-03-16
- 对照异常：[#76 comment 4066431491](https://github.com/41490/ccclaw/issues/76#issuecomment-4066431491)
- 根因跟踪：[#79](https://github.com/41490/ccclaw/issues/79)
- 目标：判断当前版本 `26.03.16.1742` 的 CCClaw，是否已经能正确完成一个最小 Issue 的“巡查、感知、触发、监察、结束、汇报、关闭”全链路

## 结论先行

结论：**不能完整闭环**。

当前版本在最小案例上已经能完成：

1. 巡查
2. 感知
3. 触发
4. 实际执行并产出成功结果

但还不能稳定完成：

1. 正确收口
2. 正确汇报
3. 自动关闭 Issue

直接根因不是此前表面归因的 `system/hook_started`，而是 **Claude Code 2.1.76 的真实 `stream-json` 中已混入 `type=user` / `type=assistant` 对话帧与 `tool_use` / `tool_result` 内容帧，当前 `stream_contract` 只兼容了 `system/*`，仍会在这些新帧上报 “无法识别事件类型”**。

因此，成功任务会在“收口阶段”被误翻成 `FAILED` 或 `DEAD`。

## 取证范围

本轮取证基于以下现场事实：

- GitHub Issue：
  - [#78](https://github.com/41490/ccclaw/issues/78)
  - [#76 comment 4066431491](https://github.com/41490/ccclaw/issues/76#issuecomment-4066431491)
- 本机运行态：
  - `~/.ccclaw/bin/ccclaw -V` = `26.03.16.1742`
  - `ccclaw scheduler status` = `request=systemd effective=systemd`
  - `ccclaw status` 可见 `#78 FAILED`、`#76 DEAD`
- 现场产物：
  - `~/.ccclaw/var/results/41490_ccclaw_78_body.stream.jsonl`
  - `~/.ccclaw/var/results/41490_ccclaw_76_body.stream.jsonl`
  - `~/.ccclaw/var/events-2026-W12.jsonl`
  - `~/.ccclaw/var/state.json`
- 源码与测试：
  - `src/internal/executor/stream_contract.go`
  - `src/internal/executor/stream_contract_test.go`
  - `src/internal/executor/executor.go`
  - `src/internal/executor/executor_test.go`
  - `src/internal/app`

## GitHub 与运行态时间线

### 1. #78 的最小案例时间线

- `2026-03-16T12:16:07Z`
  - `DU4DAMA` 创建 [#78](https://github.com/41490/ccclaw/issues/78)
- `2026-03-16T12:16:44Z`
  - `ZoomQuiet` 评论 `/ccclaw approve`
- `2026-03-16T12:20:17Z`
  - `ingest` 日志记录：`Issue 已入队`
- `2026-03-16T12:20:23Z`
  - `events-2026-W12.jsonl` 记录 `STARTED`
- `2026-03-16T12:21:31Z`
  - GitHub 被回帖 `FAILED 1/3`
  - 失败原因为：`解析 Claude JSON 输出失败: stream-json 解码失败: 无法识别事件类型`

### 2. #76 的对照异常

[#76 comment 4066431491](https://github.com/41490/ccclaw/issues/76#issuecomment-4066431491) 与 #78 的失败文案几乎一致：

- 阶段：`收口阶段`
- 错误：`解析 Claude JSON 输出失败: stream-json 解码失败: 无法识别事件类型: 结构化结果缺失，已回退诊断文件`
- 诊断前缀同样从 `system/hook_started` 开始

说明这不是 #78 单点偶发，而是当前 release 上仍在稳定复现的同类问题。

## 全链路判定

| 环节 | 结论 | 证据 |
|------|------|------|
| 巡查 | 正常 | `systemd` timer 正常运行；`journalctl` 可见 `开始同步 open issues` |
| 感知 | 正常 | `ingest` 识别到 `#78` 并记录 `Issue 已入队` |
| 触发 | 正常 | `events-2026-W12.jsonl` 记录 `CREATED -> STARTED`；prompt 文件已生成 |
| 监察 | 部分正常 | 任务确实被执行；但巡查层未能把成功执行与收口失败区分展示 |
| 结束 | 实际成功 | `41490_ccclaw_78_body.stream.jsonl` 末尾存在 `type=result subtype=success` |
| 汇报 | 失败 | GitHub 最终只收到 `FAILED` 回帖，没有成功回帖 |
| 关闭 | 失败 | `DoneCommentID = 0`，Issue 仍为 `OPEN` |

## 为什么说“实际执行成功，但系统判失败”

### 1. #78 的真实 stream 文件已经包含成功结果

现场文件：

- `~/.ccclaw/var/results/41490_ccclaw_78_body.stream.jsonl`

关键事实：

- 前 1-5 行是 `system/hook_started`、`system/hook_response`、`system/init`
- 第 6-8 行开始进入 `assistant` 消息与工具调用
- **第 9 行是 `type=user` 的 `tool_result` 返回**
- 末尾存在标准成功结果：
  - `{"type":"result","subtype":"success", ...}`

这说明：

1. Claude 执行体并没有卡死
2. 它已经完成任务
3. 它已经给出结构化 success result

## 真正的首个炸点不是 `hook_started`

`journalctl` 中的警告是：

- `解析 stream-json 第 9 行失败: 无法识别事件类型`

而 #78 现场第 9 行实际内容是：

```json
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01Tt7fewyBygqvQzUQ7HcKTc","type":"tool_result","content":"...","is_error":false}]}}
```

也就是说：

- GitHub 失败评论里展示的“诊断前缀”只是文件开头两条 `hook_started`
- 真正让解析器报错的，是后面出现的 `type=user + tool_result`
- #76 的失败评论也只是复用了同样的“文件前缀”，因此把根因误导成了 `hook_started`

## 当前源码为什么还会失败

### 1. #75 的修复只覆盖了 `system/*`

当前源码 `src/internal/executor/stream_contract.go` 已显式兼容：

- `system/init`
- `system/hook_started`
- `system/hook_response`

对应测试 `src/internal/executor/stream_contract_test.go` 与 fixture `system_success.stream.jsonl` 也只覆盖了这类事件。

### 2. 解析器仍不认识 `type=user` / `type=assistant` 对话帧

`resolveStreamEventKind()` 当前只按这些条件判定：

- `event`
- `type`
- `subtype`
- `usage`
- 顶层 `result`
- 顶层 `message/step/current_step`

当一行数据是：

- `type=user`
- 内容嵌在 `message.content[].type=tool_result`

时：

- 顶层没有可识别的 `result`
- 顶层没有可识别的 `message/detail/text`
- 也不是 `system/*`
- 最终返回：`无法识别事件类型`

### 3. 执行器虽然继续扫流，但仍会把整轮任务判失败

`src/internal/executor/executor.go` 的 `runSync()` 有两个关键行为：

1. 逐行解析时，某一行失败后并不会立刻退出，后续行仍继续扫描
2. 但只要出现过任意 `decodeErr`，收口时就会把整轮结果包装成：
   - `stream-json 解码失败: 无法识别事件类型`

于是现场就出现了这个反直觉现象：

1. stream 末尾已经有 success result
2. 但前面某个 `tool_result` 行先报了解码错误
3. 最后整轮仍被当成失败

### 4. reporter 只能回失败，不能回 DONE

一旦收口层返回 parse error：

- reporter 会把阶段归类到 `收口阶段`
- GitHub 收到 `FAILED` 或 `DEAD` 评论
- 不会产生 `/ccclaw [DONE]`
- `DoneCommentID` 保持 `0`
- Issue 无法自动关闭

## 为什么 `ccclaw status` 能看到失败，但 `state.json` 仍可能残留旧状态

现场 `ccclaw status` 已显示：

- `#78 FAILED`
- `#76 DEAD`

而 `~/.ccclaw/var/state.json` 中，`#78` 仍可见 `State = RUNNING`。

这说明当前运行态至少存在一个附带问题：

- CLI 聚合口径与 `state.json` 文件落盘并非完全同步
- 或者 `state.json` 已退化为兼容快照，不再是唯一事实源

它不是本案主根因，但会放大排障误导，建议作为次级问题补充核查。

## 对“当前版本是否能正确巡查到关闭一个 Issue”的判断

严格按用户要求的完整链路判断：

### 能做到的

1. 巡查 open issue
2. 感知标签与审批状态
3. 触发执行
4. 实际完成最小任务
5. 写出工程报告文件

### 还做不到的

1. 正确识别 Claude 2.1.76 的完整 `stream-json` 事实流
2. 正确把 success result 收口为 `DONE`
3. 正确生成成功回帖
4. 自动关闭 Issue

因此答案是：

**当前版本 CCClaw 还不能正确闭环处理一个最小 Issue。它能执行，但不能稳定收口。**

## 根因拆解

### 根因 1：契约覆盖面落后于 Claude 2.1.76 实际输出

此前修复把问题收敛到 `system/*`，但真实输出还包含：

- `type=assistant` + `thinking`
- `type=assistant` + `text`
- `type=assistant` + `tool_use`
- `type=user` + `tool_result`

这些都还没有进入 `stream_event_contract/v1`。

### 根因 2：收口策略仍是“任意未知行致整批失败”

当前策略虽然允许继续扫描后续行，但只要出现一次未知行：

- 最终仍视为整批 `stream-json` 不可信
- 即使后面已出现标准 success result，也不会进入 `DONE`

### 根因 3：失败评论的诊断信息只截了文件前缀

当前 GitHub failure comment 展示的是 stream 前缀：

- 看到的总是 `hook_started`

但真实炸点在第 9 行的 `tool_result`。

这会把现场误导到错误方向，导致排障反复绕回已修过的 `system/*`。

## 解决方案建议

遵循“优先解决根因，不做表面补丁”，建议分三层处理。

### 第一层：补齐 `stream-json` 契约

在 `src/internal/executor/stream_contract.go` 中扩展兼容范围：

1. 接受 `type=assistant`
2. 接受 `type=user`
3. 解析 `message.content[]` 中的：
   - `thinking`
   - `text`
   - `tool_use`
   - `tool_result`
4. 将这些帧映射为：
   - `progress`
   - 或 `warning`
   - 绝不能直接当 fatal decode error

### 第二层：真正落实“结果优先”

当前 #75 的“结果优先”只覆盖了 `system/*`。

应继续收紧为：

1. 未知非错误事件只记 warning
2. 若整条流中已出现标准 success result：
   - 不允许再因为前序未知普通帧把任务翻成 `FAILED`
3. 只有以下情况才可判任务失败：
   - 显式 error result
   - Claude 进程退出失败且无 success result
   - 结果文件完全缺失

### 第三层：修正失败诊断与回归样本

必须补：

1. 真实现场 fixture
   - 直接用 #78 或脱敏后的最小流做测试样本
2. 新测试覆盖：
   - `assistant/tool_use`
   - `user/tool_result`
   - success result 在后、未知帧在前
3. 失败评论改为输出：
   - 实际出错行号
   - 实际出错行的 `type/subtype/content-type`
   - 不能只贴文件前缀

## 建议后续动作

1. 新建独立 root-cause Issue，明确本案不再继续归因于 `hook_started`
2. 在 Issue 讨论拍板后，再进入代码修复
3. 修复完成后，用 maintain 成员再跑一次最小验收 Issue
4. 对 #76 / #78 做历史说明或受控重跑，避免 backlog 留下错误失败结论

## 本轮结论

本轮最小案例已经证明：

- **执行链路是通的**
- **收口链路仍然不通**

CCClaw 当前版本的真实问题不是“不会执行任务”，而是：

**不能消费 Claude Code 2.1.76 的完整事实流，所以会把已经成功的任务误判成失败。**
