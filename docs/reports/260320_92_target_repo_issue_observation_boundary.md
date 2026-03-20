# 260320_92_target_repo_issue_observation_boundary

## 背景

- 关联 Issue: <https://github.com/41490/ccclaw/issues/92>
- 拍板来源:
  - <https://github.com/41490/ccclaw/issues/87#issuecomment-4090885176>

`#92` 要求把 `target_repo` 的职责边界与调度观察范围讲清楚，并同步落到实现、文档与测试里，避免继续把控制仓语义、Issue 所在仓语义与代码落点语义混在一起。

本轮不引入新的配置模型，只在现有拍板范围内把边界显式化：

- `control_repo` 仍是官方默认控制面入口
- `target_repo` 仍只负责代码落点路由
- 调度器的 Issue 观察范围继续是“控制仓库 + 已启用 target 仓库”，但要在实现与文档里说清楚这是 **Issue 观测边界**，不是 `target_repo` 本身的回帖仓库语义

## 本轮改动

### 1. 运行时显式收口 Issue 观测边界

修改文件：

- `src/internal/app/runtime.go`

本轮把原先隐含在 `issueSourceRepos()` 里的规则显式化为统一的观测边界定义，并让 `ingest`、`doctor`、阻塞判定共用同一口径：

- `observedIssueRepos()` 统一返回当前可观测的 Issue 仓库集合
  - 固定包含 `github.control_repo`
  - 追加所有已启用 `[[targets]].repo`
  - 自动去重
- `describeTargetRouting()` 现在会输出：
  - `enabled_targets`
  - `observed_issue_repos`
  - `control_repo`
  - `default_target`
- `syncIssue()` 新增观测边界阻塞判定
  - 若某条 Issue 来自观测边界之外的仓库
  - 即使还能解析出 `target_repo`
  - 也会先转成 `BLOCKED`
  - 并把“超出观测边界”的原因写入事件链

这样后续排障时，可以明确区分：

- “Issue 能不能被看见”看的是 `issue_repo` 是否处在观测边界
- “代码往哪落”看的是 `target_repo`

### 2. 配置注释与安装样板同步

修改文件：

- `src/internal/config/config.go`
- `src/ops/config/config.example.toml`
- `src/dist/ops/config/config.example.toml`
- `src/dist/install.sh`
- `src/ops/examples/app-readme.md`
- `src/dist/ops/examples/app-readme.md`

本轮把原先容易误读成“target 仓就是调度仓”的说明，改成更清晰的观测边界定义：

- `ingest` 每轮只观察两类 Issue 来源仓库
  - 官方 `control_repo`
  - 已启用的 `[[targets]].repo`
- `github.limit` 明确是“对每个被观察仓库的 open issues 拉取上限”，不是并发数
- 安装后程序目录示例 README 也同步补上这层说明

### 3. README 补齐四个概念的关系

修改文件：

- `README.md`
- `README_en.md`

README 新增并明确区分：

- `Issue 来源仓库 / issue_repo`
- `Issue 仓库`
- `target_repo`
- `control_repo`

并把链路职责直接拆开：

- `ingest` 只负责观察当前观测边界内的 Issue
- `dispatch / slot / sync_target / journal` 只看 `target_repo`
- `reporter / approval / status / done marker` 只看 `issue_repo`

同时把中英文 README 里的“日常使用流程 / 开源协作门禁流程”入口表述，从“控制仓库或任一已绑定任务仓库”升级为“当前观测边界内”，减少运维时的脑补空间。

## 测试

新增/补强：

- `TestObservedIssueReposDedupesControlAndTargets`
  - 卡住控制仓与启用 target 的观测集合去重规则
- `TestSyncIssueBlocksWhenIssueRepoOutsideObservationBoundary`
  - 卡住“观测边界外的 Issue 只能阻塞，不能被当成正常入口”

## 结论

本轮没有改动已拍板的入口模型，也没有新加配置项；只是把之前分散在代码、注释和 README 里的隐式规则收口成一套更可验证的口径：

- `target_repo` 是代码落点，不是 Issue 回帖仓库
- `issue_repo` 是审批、回帖、DONE 与状态展示的事实来源
- 调度器观察的是“控制仓库 + 已启用 target 仓库”组成的 **Issue 观测边界**
- 观测边界外的仓库不会再被默认为正常任务入口
