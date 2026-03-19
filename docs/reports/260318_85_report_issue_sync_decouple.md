# 260318_85_report_issue_sync_decouple

## 背景

- 关联 Issue: [#85](https://github.com/41490/ccclaw/issues/85)
- 父 Issue: [#81](https://github.com/41490/ccclaw/issues/81)
- 现场样本: [#82](https://github.com/41490/ccclaw/issues/82)
- 前置报告:
  - [260318_81_issue82_min_response_tracking](./260318_81_issue82_min_response_tracking.md)
  - [260318_84_jj_git_capability_probe_degrade](./260318_84_jj_git_capability_probe_degrade.md)

#82 暴露出的核心问题不是“任务没跑起来”，而是：

- Claude 结果已经产出
- 但当前 finalize 顺序仍是 `sync_target -> sync_home -> report_issue`
- 所以只要 repo sync 失败，GitHub 外部就完全看不到执行结果

这会让用户把“收尾失败”误判成“系统没响应”。

## 本次实现

### 1. `report_issue` 改为前置的首条可见回帖

本次修改 [runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go) 的 finalize 编排：

- 旧顺序：
  - `sync_target`
  - `sync_home`
  - `report_issue`
- 新顺序：
  - `report_issue`
  - `sync_target`
  - `sync_home`
  - `mark_done`

其中：

- `report_issue`
  - 负责“结果已形成，正在交付收尾”的首条可见回帖
  - 不追加 `/ccclaw [DONE]`
- `mark_done`
  - 只在 sync 全部成功后执行
  - 把首条结果回帖补齐为最终 done comment

这样即使 target/home 仓同步失败，Issue 页面也至少已经出现过一次可见回应。

### 2. 成功路径不再新增第二条 done 回帖

为避免“先回一条结果，再回一条 DONE”形成成功路径双回帖，本次新增：

- [client.go](/opt/src/ccclaw/src/internal/adapters/github/client.go) `UpdateComment()`
- [reporter.go](/opt/src/ccclaw/src/internal/adapters/reporter/reporter.go)
  - `ReportResultReady()`
  - `PromoteResultToDone()`

具体行为：

1. 首轮 finalize 先 `AddComment`
2. 收尾成功后，对同一 comment 执行 `PATCH`
3. 把正文更新成最终完成文案，并补上 `/ccclaw [DONE]`

因此成功路径现在是：

- 一次 POST
- 一次 PATCH

而不是两次 POST。

### 3. 新增 `ResultCommentID` 追踪同一条评论

本次在 [task.go](/opt/src/ccclaw/src/internal/core/task.go) 增加：

- `ResultCommentID`

用途：

- `report_issue` 成功后记录首条可见评论 ID
- `mark_done` 时用它定位并更新同一条评论
- 最终成功后让 `ResultCommentID == DoneCommentID`

这样既能维持 GitHub done marker 绑定，又能避免成功路径新增第二条“完成”评论。

### 4. finalize failure 语义同步扩展到 `mark_done`

本次把 `mark_done` 纳入 issue reporting failure 范围：

- `report_issue` 失败：首条可见回帖失败
- `mark_done` 失败：done marker 回写失败

两者都会归到：

- `FinalizeFailureClassIssueReporting`

从而保持 `FINALIZING / finalize_failed / DONE` 与评论语义一致。

## 测试

### 自动化验证

执行：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

结果通过。

本次新增覆盖：

- `ReportResultReady()` 不追加 done marker
- `PromoteResultToDone()` 会更新已有评论而不是再发新评论
- `sync_target` 失败时，先出现首条可见回帖，再出现 `FINALIZING` 失败说明
- 成功路径只执行“一次 POST + 一次 PATCH”，不再出现第二条成功回帖

### 行为验证结论

#### 失败路径

当 repo sync 失败时：

1. Issue 先出现“结果已形成，正在执行交付收尾”
2. 随后若 `sync_target` / `sync_home` 失败
3. 再补一条 `FINALIZING` 失败说明

用户不再看到“完全没回应”。

#### 成功路径

当 repo sync 成功时：

1. 先发首条可见回帖
2. 最终通过 PATCH 把同一条评论补成 DONE
3. 不再新增第二条成功回帖

## 取舍

### 1. 失败路径仍允许补充第二条说明

原因：

- 首条可见回帖只负责证明“结果已形成”
- 真正的同步失败原因仍需要单独说明

因此失败路径允许：

- 首条可见回帖
- 再补一条 finalize failure 说明

这不是噪音，而是把“已执行”与“收尾失败”拆开表达。

### 2. 成功路径优先避免双回帖

原因：

- 成功时用户只需要最终一条 DONE 结果
- 若保留两条 POST，会让 Issue 页面多出一条很快失效的中间态评论

因此成功路径采用：

- 先 POST
- 后 PATCH

而不是“结果回帖 + DONE 回帖”双发。

## 结论

#85 本轮本地实现后，GitHub 外部可见性已经从：

- “只有 repo sync 成功才看得到回复”

推进到：

- “结果一形成就有首条可见回帖”
- “sync 失败会继续补 `FINALIZING` 失败说明”
- “sync 成功则把同一条评论补成 DONE，而不是再发第二条成功回帖”

这直接修复了 #82 暴露出的最关键外部体验缺口：系统明明做完了执行，但用户在 Issue 页面上却像是完全没收到响应。
