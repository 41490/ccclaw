# 260319_91_global_sidecar_convergence

## 背景

Issue #91 要求把当前运行态 slot 存储从“按 `target_repo` 命名的 repo slot store”继续收敛为真正的“全局唯一 sidecar”。

`#87` 已经把调度语义切到“全局单飞串行”，但实现里仍保留了明显的旧语义残留：

- sidecar 文件仍按 `target_repo` 命名
- `hydrate/advance/finalize/delete` 仍默认把 `target_repo` 当作 sidecar 唯一键
- `status` 人类可读文案仍在说“仓位/仓位总数”

这会带来一个事实裂缝：

- 调度层已经是全局唯一执行窗口
- 运行态存储和观测层却还像是“每仓一个 sidecar”

## 目标

本轮收敛的范围是：

- sidecar 落盘改为单文件模型
- 运行态读取不再依赖 `target_repo` 才能定位唯一 sidecar
- `status` 文案显式改成“全局 sidecar”
- 兼容迁移旧版 repo 命名的 runtime 文件
- 保持现有 finalize_failed / next_retry_at / current_step 恢复链路不变

## 根因

此前 `slot_store.go` 的核心假设是：

1. `runtime/<sanitize(target_repo)>.json` 就是 sidecar 主键
2. `Get/Delete` 都必须带 `target_repo`
3. `List()` 会把 `runtime/` 目录下所有 `*.json` 都当成活动 slot 载入

这套模型在“按仓串行”阶段是成立的；但在 `#87` 之后，活动 sidecar 理论上只能有 1 个，继续沿用 repo 命名会让：

- 实现层误以为“唯一性来自 repo”
- 运维层误以为“同一时刻允许多个 sidecar，只是分仓而已”
- 兼容迁移缺少单点入口，旧 runtime 文件也无法统一识别

## 实现

### 1. sidecar 存储改为单文件

在 `src/internal/adapters/storage/slot_store.go` 中：

- 新增固定文件名 `runtime/global_sidecar.json`
- `List()` 改为最多只返回 1 个 sidecar
- `Upsert()` 始终写入固定单文件
- `GetActive()` / `DeleteActive()` 用于按“全局唯一 sidecar”读写
- 兼容保留 `Get(targetRepo)` / `Delete(targetRepo)` 包装，供旧调用和测试平滑过渡

这样后续运行态不再需要依赖 `target_repo -> 文件名` 推导 sidecar 主键。

### 2. 兼容迁移旧版 repo sidecar

仍在 `slot_store.go` 中新增迁移逻辑：

- 若 `global_sidecar.json` 不存在，会扫描 `runtime/*.json`
- 若只发现 1 个旧版 repo sidecar，则自动迁移为 `global_sidecar.json` 并删除旧文件
- 若发现多个旧版 repo sidecar，则显式报错并列出文件、task、repo、更新时间

这里没有“偷偷挑一个最新文件顶上去”，因为那会在多 sidecar 残留场景下引入新的误判；显式报错更符合“安全识别/安全回收”的要求。

### 3. 运行态调用切到全局 sidecar 语义

在 `src/internal/app/runtime_cycle.go` 中：

- `advanceRepoSlots()` 改为统计推进数量，而不是继续按 `target_repo` 聚合
- `hydrateRepoSlots()` 改为优先读取全局 sidecar，不再逐仓探测 sidecar 唯一性
- `finalize/fail/delete` 路径改为删除活动 sidecar，而不是按 `target_repo` 删除
- 调度提示文案从“活动槽位”改成“全局 sidecar”

这样 `runIngestCycle()` 与 sidecar 存储模型终于一致：都是全局唯一窗口。

### 4. status 文案显式改成全局模型

在 `src/internal/app/runtime.go` 中：

- `status --json.slots` 新增：
  - `model = global_sidecar`
  - `scope = singleton`
- 人类可读输出改为：
  - `全局 sidecar 快照`
  - `sidecar 模型: global_sidecar (singleton)`
  - `当前无活动 sidecar`

保留 `slots` 这一 JSON 顶层键，是为了不在本轮扩大兼容面；但它的内部语义已经明确标注为全局唯一 sidecar。

## 验证

本轮新增并通过了以下回归：

```bash
go test ./internal/adapters/storage -run 'TestStoreMigratesLegacyRepoSlotToGlobalSidecar|TestStoreRejectsAmbiguousLegacyRepoSlotsDuringMigration' -count=1
go test ./internal/app -run 'TestStatusWithoutTasksStillShowsSnapshot|TestStatusJSONIncludesFinalizeFailureSlotFields|TestHandleFinalizeFailureReportsFirstVisibleFailureAndDedupsSameFailureKey|TestCompleteTaskFinalizingPostsVisibleCommentBeforeSyncFailure|TestPatrolUsesLogTailWhenMissingSessionResultIsEmpty|TestIngestDispatchFinalizesImmediatelyWhenSessionMissingRightAfterLaunch' -count=1
go test ./...
```

重点验证了三件事：

- 旧 `runtime/<repo>.json` 能自动迁移到 `runtime/global_sidecar.json`
- 多个旧 sidecar 残留时会显式报错，而不是错误地自动挑选
- finalize_failed / patrol / status / reporter 相关链路没有被这次存储收口破坏

## 结果

本轮完成后：

- sidecar 的事实主键已经从“repo 文件名”收口为“全局唯一文件”
- 运行态不再依赖 `target_repo` 才能定位唯一 sidecar
- `status` 对 sidecar 的解释与“全局单飞串行”一致
- 旧 runtime 文件具备单文件自动迁移能力
- finalize 阻塞、重试和恢复路径保持原语义

## 后续建议

1. `doctor` 可补一个“检测到多个 legacy sidecar，需人工回收”的显式诊断项，避免运行期才暴露。
2. `status --json` 后续可考虑把 `slots` 顶层键进一步别名化到 `sidecar`，再通过一个 release 周期完成兼容迁移。
3. `docs/reports/260319_87_global_single_flight_dispatch.md` 中“本轮不改写 slot 存储模型”的备注，至此可以由本报告补齐后续收敛闭环。
