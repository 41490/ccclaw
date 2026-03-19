# 260319_81_issue_lineage_cleanup_release_decision

## 背景

- 父 Issue: [#81](https://github.com/41490/ccclaw/issues/81)
- 现场样本: [#82](https://github.com/41490/ccclaw/issues/82)
- 关联子 Issue:
  - [#83](https://github.com/41490/ccclaw/issues/83)
  - [#84](https://github.com/41490/ccclaw/issues/84)
  - [#85](https://github.com/41490/ccclaw/issues/85)
  - [#86](https://github.com/41490/ccclaw/issues/86)

`#81` 在 `2026-03-18` 发布 `26.03.18.2026` 后关闭，但当时真实样本 `#82` 已证明：

- 执行主链成立
- GitHub 外部可见性与收尾闭环仍不成立
- `sync_target`、`report_issue`、`mark_done`、`jj/git` 兼容性与失败分类仍有关键缺口

本轮目标不是重复讨论，而是清查 `#81` 拆出的后续任务是否已经真实落到 `main`，并据此决定：

1. 哪些子 Issue 应关闭
2. `#81` 是否应重开等待复审
3. 是否必须发布新 release，才能让用户真正用到修复后的 Issue 驱动工作流

## 清查范围与证据

### 1. GitHub Issue 状态

核查时点：

- 本机时区：`2026-03-19 00:07 EDT`
- GitHub 最新 release：`26.03.18.2026`

核查结果：

- `#83` 已关闭
- `#84` 已关闭
- `#85` 仍为 open
- `#86` 仍为 open

其中 `#85` 与 `#86` 虽然未手工关单，但对应实现提交已经进入 `main`。

### 2. `main` 上与 `#81` 直接相关的实现提交

相对 `26.03.18.2026` 发布提交 `59aa631`，当前 `main` 已包含以下关键修订：

- `60e7068` `fix: 提前暴露 finalize_failed 首轮回帖`
- `37e0608` `fix: 补齐 finalize 恢复与兼容诊断`
- `3a92f1e` `fix: 前置探测 jj/git 同步能力`
- `3202b0d` `fix: 优先发布 issue 可见回帖`
- `085a34a` `feat: 细化 sync_target 收尾失败分类`

这些提交共同覆盖了 `#81` 拆出的四个后续方向。

### 3. 代码落地核查

本轮抽查的关键代码点如下：

- [runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go)
- [runtime.go](/opt/src/ccclaw/src/internal/app/runtime.go)
- [reporter.go](/opt/src/ccclaw/src/internal/adapters/reporter/reporter.go)
- [jj.go](/opt/src/ccclaw/src/internal/vcs/jj.go)

确认结果：

1. `report_issue` 已前置到收尾阶段最前，成功路径改为：
   - `report_issue`
   - `sync_target`
   - `sync_home`
   - `mark_done`
2. 成功路径不再新增第二条 DONE 评论，而是：
   - 先发首条“结果已形成”评论
   - 再对同一评论执行 PATCH 补齐 `/ccclaw [DONE]`
3. `ResultCommentID` 已落入任务状态，支持同评论升级为 DONE
4. finalize failure 已具备：
   - 首轮可见回帖
   - `failure_class`
   - `failure_mode`
   - 恢复后补回帖
5. `vcs.SyncRepo()` 已具备：
   - `jj/git` capability probe
   - `ErrCapabilityMismatch`
   - `ErrSyncNetwork/Auth/Protection/Unknown`
   - 非网络类错误首轮停止，不再盲目重试

### 4. 测试核查

执行：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

结果通过。

这说明当前 `main` 至少在自动化测试层面，已经覆盖并通过：

- 首轮 finalize failure 可见性
- finalize 恢复回帖
- `jj/git` 同步能力探测
- `report_issue` 与 repo sync 解耦
- `sync_target` 失败分类与处理策略

## 子 Issue 关单判断

### `#83`

已关闭，且提交与报告齐备，无需动作。

### `#84`

已关闭，且 capability probe、doctor、文档与测试均已落地，无需动作。

### `#85`

应关闭。

原因：

- 目标“解耦 `report_issue` 与 repo sync，先保证 GitHub 可见响应”已在 `3202b0d` 落地
- 自动化测试通过
- 工程报告 [260318_85_report_issue_sync_decouple](./260318_85_report_issue_sync_decouple.md) 已提交

### `#86`

应关闭。

原因：

- 目标“细化 `sync_target` 失败分类与处置策略”已在 `085a34a` 落地
- 自动化测试通过
- 工程报告 [260318_86_sync_target_failure_classification](./260318_86_sync_target_failure_classification.md) 已提交

## 发布决策

结论：**应发布新 release。**

原因不是“代码变了很多”，而是：

1. 最新已发布版本 `26.03.18.2026` 对应的仍是问题暴露阶段
   - 当时 `#82` 还停留在“执行成立，但交付闭环不成立”
2. `#81` 相关关键修补全部发生在该 release 之后
   - 用户如果继续安装 `26.03.18.2026`，拿到的仍不是当前已修复链路
3. 本轮修复直指 Issue 驱动工作流可用性
   - 不是内部重构或文档修饰，而是 GitHub 外部可见响应、DONE 收口、同步失败分类、环境兼容探测这些直接决定“能不能用”的能力

因此若不发布，新代码只存在于 `main`，无法视为“工作流可用问题已彻底解决到可交付状态”。

## 当前结论

到本轮清查为止，可以把 `#81` 的结论从先前的：

- 执行链路成立
- 交付闭环未成立

推进为：

- 执行链路成立
- GitHub 外部首轮可见性成立
- `FINALIZING` 首轮失败可见性成立
- `jj/git` 兼容缺口已前置探测
- `sync_target` 失败分类与处置策略已结构化
- 成功路径可在同评论补齐 DONE

也就是说，Issue 驱动工作流的“最小可用闭环”已经从代码与测试层面成立，剩下的重点不再是根因修复，而是发布交付与后续体验优化。

## 后续优化建议

仍建议保留以下后续项，但它们已不构成阻塞本轮发布的根因问题：

1. `jj/git` capability matrix 继续细化
   - 现在已有 probe，但版本规则仍可继续从“固定门槛”演进到“能力矩阵”
2. finalize event 增加更结构化元字段
   - 目前 `slot/status` 结构化较好，event detail 仍可继续机器可读化
3. `protection` 类失败探索自动 PR 交付链路
   - 这能进一步降低 pause 场景中的人工介入比例
4. 用新的真实 Issue 再跑一轮外部验收
   - 重点验证“首条结果回帖 -> DONE PATCH -> 收尾恢复/失败说明”在 GitHub 页面上的最终体验

## 收口建议

建议按以下顺序执行：

1. 关闭 `#85`
2. 关闭 `#86`
3. 从当前 `main` 发布新 release
4. 回到 `#81` 汇总本轮清查结论、发布决定与后续优化，并重开等待人工复审
