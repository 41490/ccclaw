# 260315_74_issue_lifecycle_false_fail_forensics

## 背景

用户要求追查 Issue [#74](https://github.com/41490/ccclaw/issues/74) 的完整行为历史，明确回答：

1. 为什么观众创建的 Issue，在 admin 人工追加 `ccclaw` 标签并正面批复后会正确执行
2. 为什么执行成功后又被判定失败
3. 为什么这种反直觉过程重复三次
4. 当前代码里固化了哪些标签、回复标志行、状态迁移与触发关系

本轮只做取证、代码比对、方案规划，不直接修改实现。

补充留痕：

- #74 已回帖：<https://github.com/41490/ccclaw/issues/74#issuecomment-4063251723>
- 已新建跟踪 Issue [#75](https://github.com/41490/ccclaw/issues/75)

## GitHub 现场时间线

通过 `gh api` 核查，#74 的关键时间线如下：

- `2026-03-15T14:30:00Z`
  - `DU4DAMA` 以 `read` 权限创建 Issue #74
- `2026-03-15T14:30:23Z`
  - `ZoomQuiet` 给 #74 加 `ccclaw` 标签
- `2026-03-15T14:30:35Z`
  - `ZoomQuiet` 评论 `/ccclaw approve`
- `2026-03-15T14:36:48Z`
  - Claude 任务内自行执行 `gh issue comment`，发布第一条普通结果评论
- `2026-03-15T14:36:53Z`
  - `ccclaw` 收口失败，回帖 `FAILED 1/3`
- `2026-03-15T14:41:20Z`
  - 第二轮再次发布普通结果评论
- `2026-03-15T14:41:25Z`
  - 第二轮再次回帖 `FAILED 2/3`
- `2026-03-15T14:46:15Z`
  - 第三轮再次发布普通结果评论
- `2026-03-15T14:46:20Z`
  - 第三轮回帖 `DEAD 3/3`

结论：

- 门禁放行是符合当前代码设计的
- 执行体三次都实际跑到了结果输出阶段
- 失败发生在收口解析，而不是审批阶段

## 本机运行产物取证

### 1. 任务实际成功结束过

文件：

- `~/.ccclaw/var/results/41490_ccclaw_74_body.stream.jsonl`

关键事实：

- 第 34 行是任务内直接执行的：
  - `gh issue comment 74 --repo 41490/ccclaw --body ...`
- 第 35 行返回了真实评论 URL
- 第 37 行是标准成功结果：
  - `{"type":"result","subtype":"success",...}`

这说明：

- Claude 执行体本轮并没有卡死
- 它已经拿到了可交付结果
- 并且还主动回了普通 Issue 评论

### 2. 同一个 stream 文件前面混入了新系统事件

同一文件前几行是：

- `type=system subtype=hook_started`
- `type=system subtype=hook_response`
- `type=system subtype=init`

这些不是仓库内既有 fixture 覆盖的旧 `started/progress/usage/result` 事件集。

### 3. 没有生成对应 `*.event.json`

`41490_ccclaw_74_body.event.json` 不存在，说明 stream 快照在解析阶段就失败了，没有落成聚合事件快照。

## 为什么会正确执行

根因在门禁逻辑，而不是偶然碰巧成功。

`src/internal/app/runtime.go` 中 `syncIssue()` 的现行规则是：

- Issue 必须是 `open`
- Issue 必须带触发标签 `ccclaw`
- 若作者权限低于 `approval.minimum_permission`
  - 则需要受信任成员在评论里发 `/ccclaw <批准词>`

#74 的现场正好满足：

- 作者 `DU4DAMA` 权限是 `read`
- `ZoomQuiet` 权限是 `admin`
- 标签已补
- `/ccclaw approve` 已发

因此它会从 `BLOCKED` 进入 `NEW`，随后进入执行。

## 为什么没有最终 `/ccclaw [DONE]`

这是本案最容易看错的地方。

### 1. 普通结果评论 != 结构化完成标记

当前执行 prompt 里只写了：

- “如需回复 Issue，请总结成果、测试结果和后续优化建议”

这会让 Claude 在任务内直接调用 `gh issue comment` 发普通评论。

但当前系统真正认的完成标记只有一个：

- `/ccclaw [DONE]`

对应逻辑在：

- `src/internal/adapters/github/client.go`
  - `DoneMarker = "/ccclaw [DONE]"`
  - `HasDoneMarker()`
  - `FindDoneComment()`

也就是说：

- #74 上三条“调查结果”评论只是普通业务评论
- 它们不是结构化生命周期终态
- `syncIssue()` 不会把它们识别成 `DONE`

### 2. 真正追加 `/ccclaw [DONE]` 的只有 `ReportSuccess()`

当前统一成功回帖入口是：

- `src/internal/adapters/reporter/reporter.go`
  - `ReportSuccess()`

它固定会在评论末尾追加：

- `/ccclaw [DONE]`

但这条路径只有在收口成功时才会走到：

- `src/internal/app/runtime_cycle.go`
  - `completeTaskFinalizing()`
  - `rt.reportSuccess(...)`

### 3. #74 在进入 `ReportSuccess()` 之前就被解析失败拦住了

由于 stream 解析报错，任务在 `finalizeRepoSlot()` 里直接走了：

- `failTaskExecution()`

所以：

- 任务内普通评论已经发出
- 但结构化成功回帖没机会执行
- 最终自然不会出现 `/ccclaw [DONE]`

## 为什么成功执行后却判定失败

### 直接根因

`src/internal/executor/stream_contract.go` 里的 `resolveStreamEventKind()` 对未知事件返回：

- `无法识别事件类型`

而当前 live stream 里已经出现了 Claude Code 2.1.76 的新增系统事件：

- `system/hook_started`
- `system/hook_response`
- `system/init`

于是整批 `stream.jsonl` 被视为解析失败。

### 错误放大路径

`src/internal/executor/executor.go`：

1. `PersistStreamEventSnapshot()` 尝试解析整份 `streamRaw`
2. 若失败，则 `parseErr` 被包装成：
   - `stream-json 解码失败: 无法识别事件类型`
3. 最后继续包装成：
   - `解析 Claude JSON 输出失败: ...`

然后 `src/internal/app/runtime_cycle.go` 中：

1. `finalizeRepoSlot()` 调 `LoadResult()`
2. 看到 `runErr != nil`
3. 直接进入 `failTaskExecution()`
4. 状态改为 `FAILED` / `DEAD`
5. 再调用 `ReportFailure()`

所以 #74 的“成功却失败”不是 GitHub 评论误读，而是：

- 真实结果存在
- 但整份 stream 契约校验失败
- 收口层把这类契约失败定义成任务失败

## 为什么会重复三次

当前重试规则是显式写死在代码里的：

- `src/internal/core/task.go`
  - `MaxRetry = 3`
- `src/internal/adapters/storage/store.go`
  - `ListRunnable()`: `NEW/FAILED` 都可继续执行
  - `ListRunnableByTarget()`: 同样把 `FAILED` 当可执行任务

因此 #74 的链路是：

- 第 1 轮：普通评论成功，收口解析失败，状态转 `FAILED`
- 调度器下一轮再捞起 `FAILED`
- 第 2 轮：重复同样过程
- 第 3 轮：再次重复
- `retry_count >= 3` 后转 `DEAD`

这不是偶发重跑，而是当前状态机的必然结果。

## 当前代码里固化的标签 / 标志行 / 生命周期元素

### 1. 触发标签

- `ccclaw`
  - 默认配置位于 `src/internal/config/config.go`
  - `github.issue_label = "ccclaw"`

### 2. 审批前缀与动作词

- 固定前缀：
  - `/ccclaw`
- 默认批准词：
  - `approve`
  - `go`
  - `confirm`
  - `批准`
  - `agree`
  - `同意`
  - `推进`
  - `通过`
  - `ok`
- 默认否决词：
  - `reject`
  - `no`
  - `cancel`
  - `nil`
  - `null`
  - `拒绝`
  - `000`

### 3. 完成标记

- 只有：
  - `/ccclaw [DONE]`

### 4. intent 相关标签

- `bug` / `fix` -> `fix`
- `ops` -> `ops`
- `release` -> `release`
- `research` -> `research`
- `report` / `ops-report` / `announce` -> `report`

### 5. risk 相关标签

- `risk:med`
- `risk:high`
- 无标签则默认 `low`

### 6. task_class 相关标记

- label:
  - `sevolver`
- title 前缀：
  - `[sevolver]`
- body 字段：
  - `task_class: sevolver_deep_analysis`
- body 指纹：
  - `ccclaw:sevolver:deep-analysis:fingerprint=...`

### 7. 状态集合

- `NEW`
- `RUNNING`
- `FINALIZING`
- `BLOCKED`
- `FAILED`
- `DONE`
- `DEAD`

### 8. 事件集合

- `CREATED`
- `BLOCKED`
- `STARTED`
- `FAILED`
- `DONE`
- `DEAD`
- `UPDATED`
- `WARNING`

### 9. 固定回帖模板

- `任务已进入阻塞状态。`
- `任务已开始执行。`
- `任务已触发 fresh restart。`
- `任务执行失败。`
- `任务执行完成。`
- `Issue 为汇报型，无可执行任务，已自动跳过 Claude 执行。`
- `任务进入收尾待处理状态。`

## 广泛对比

### 对比 1：仓库内 stream fixture

仓库已有 fixture：

- `src/internal/executor/testdata/stream_contract/success.stream.jsonl`

其中只有旧协议事件：

- `started`
- `progress`
- `usage`
- `result`

因此现有测试覆盖的是“旧契约 happy path”。

同时 `src/internal/executor/stream_contract_test.go` 还显式规定：

- 未知事件必须报错

这解释了为什么当前实现会把 live stream 新事件判成致命错误。

### 对比 2：Issue #22

`docs/reports/260312_NA_issue22_tmux_claude_path_forensics.md` 显示：

- #22 的失败是真失败
- 根因是 tmux pane 内 `PATH` 不完整，`claude` 根本没启动
- 现场没有成功 `result` 行

对比 #74：

- #22：执行链路前段失败，失败判定合理
- #74：执行链路末段已有成功结果，但收口解析失败，属于假失败

### 对比 3：Issue #72

Issue #72 现场也出现了同构现象：

- 先有成功汇报/自动收口评论
- 随后又出现同样的 `hook_started` 解析失败
- 最后走到 `FAILED/DEAD`

说明该问题不是 #74 特例，而是当前 parser 与 Claude 新 stream 协议的系统性不兼容。

## 根因归纳

本问题至少有两层根因：

### 根因 A：stream 契约解析过严，且未兼容 Claude Code 2.1.76 新系统事件

这是“成功却被判失败”的主技术根因。

### 根因 B：Issue 评论通道设计混杂

当前既允许任务内直接 `gh issue comment` 发普通结果评论，又要求生命周期终态只认 `ReportSuccess()` 追加的 `/ccclaw [DONE]`。

这会导致：

- 用户看见“已经回帖成功”
- 但系统并未进入结构化 `DONE`
- 一旦收口失败，就会出现“看起来成功、状态却失败”的强烈反直觉

## 建议修复方案

### 方案 A：对未知 `system/*` 事件降级忽略

做法：

- `resolveStreamEventKind()` 对 `type=system` 的未知事件不再报错
- 仅保留已知业务事件参与聚合

优点：

- 改动最小
- 先止血

风险：

- 新事件若将来承载真正错误信息，可能被静默吞掉

### 方案 B：显式兼容 Claude 2.1.76 的 `system/hook_* / init`

做法：

- 为这些系统事件建立明确映射
- 可映射为 `progress` 或 `started`
- 在 event snapshot 中保留原始 raw

优点：

- 兼容关系清晰
- 可观测性更好

风险：

- 需要跟随上游持续维护

### 方案 C：结果优先，未知非错误事件不得翻转成功终态

做法：

- 若整份 stream 中已出现明确 `result subtype=success`
- 且不存在显式 error 事件
- 则未知非错误事件最多记 `WARNING`
- 不得把任务翻成 `FAILED`

优点：

- 直接切断“成功结果被前置噪音否决”的错误语义
- 符合用户直觉

风险：

- 需要重新定义 stream 校验优先级

### 方案 D：统一 Issue 回帖出口

做法：

- 禁止任务 prompt 鼓励直接 `gh issue comment`
- 业务总结只通过 Claude 最终 `result` 输出
- GitHub 回帖统一由 `ReportSuccess()` / `ReportFailure()` / `ReportBlocked()` 负责

优点：

- 用户不会再把普通评论误认成生命周期终态
- `DONE` 结构更稳定

风险：

- 需要调整 prompt 与执行约束

## 建议拍板

推荐组合：

1. 先做 **C + B**
   - 结果优先
   - 显式兼容 `system/hook_* / init`
2. 再做 **D**
   - 收口 GitHub 评论出口

原因：

- 仅做 A 止血不够稳
- 仅做 D 不能解决假失败
- `C + B` 能先修正错误状态机
- `D` 再修正用户看到的生命周期语义

## 待用户决策

1. 是否接受“结果优先”语义
   - 即已看到成功 `result` 时，未知非错误事件只能记 warning，不能再把任务打成 `FAILED`
2. 是否接受“任务内不再直接回 Issue”
   - 即统一由 reporter 输出 GitHub 终态评论
3. 修复落地是否直接在 `main`
   - 按仓库当前约束，推荐直接在 `main`
4. 对历史污染现场是否补人工修正
   - 例如是否对 #72 / #74 再追加一条“历史误判”说明

