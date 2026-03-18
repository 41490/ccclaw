# 260318_84_jj_git_capability_probe_degrade

## 背景

- 关联 Issue: [#84](https://github.com/41490/ccclaw/issues/84)
- 父 Issue: [#81](https://github.com/41490/ccclaw/issues/81)
- 现场样本: [#82](https://github.com/41490/ccclaw/issues/82)
- 前置报告:
  - [260318_81_issue82_min_response_tracking](./260318_81_issue82_min_response_tracking.md)
  - [260318_83_finalize_followup_recovery_probe](./260318_83_finalize_followup_recovery_probe.md)

Issue #83 已经把 `git < 2.41.0` 前置拦下，并让 `FINALIZING` 可见。但 #84 要解决的不是“再加一条版本判断”，而是把这类问题收口成完整链路：

1. 在真正 `fetch/push` 前做能力探测，而不是只靠版本号猜
2. 一旦 `jj git fetch/push` 明确返回 capability mismatch，就立即停止普通 retry
3. 让 `doctor`、status/reporter hints、运维文档都能指向同一套排障动作

## 实现

### 1. `vcs.SyncRepo()` 增加真实 capability probe

本次在 [jj.go](/opt/src/ccclaw/src/internal/vcs/jj.go) 新增：

- `ErrCapabilityMismatch`
- `ErrUnsupportedGit`
- `SyncCapabilityError`
- `SyncCapabilityStatus()`

同步把前置探测从“只看 `git --version`”扩成两层：

1. 读取 `jj --version` 与 `git --version`
2. 执行不触网的 `git fetch -h`，检查帮助里是否存在 `--porcelain`

结果是：

- `git < 2.41.0` 时仍然直接返回 `ErrGitTooOld`
- 即使版本号看起来够新，只要缺少 `git fetch --porcelain`，也会返回 `ErrUnsupportedGit`

这比单纯版本阈值更接近真实能力。

### 2. 在 `fetch/push` 重试环中对 capability mismatch fail-fast

此前如果 `jj git fetch` 返回：

- `required option: porcelain`
- `supported version is`

这类文本，会被当成普通同步失败继续进入 retry 逻辑。

本次改为：

- 在 `fetch` / `push` 失败后先走 `classifyCapabilityMismatch()`
- 命中 capability mismatch 直接返回
- 不再继续包进 `ErrPushFailed`
- 不再误判成网络抖动

这样 `FINALIZING` 的 failure policy 会直接落到 `pause`，等待人工介入。

### 3. 把新分类接入 finalize 与 doctor

本次同步调整了 [runtime_cycle.go](/opt/src/ccclaw/src/internal/app/runtime_cycle.go)：

- `ErrUnsupportedGit`
- `ErrCapabilityMismatch`

都会归到 `version_mismatch`，并且：

- 不进入 transient retry
- hints 新增 `git fetch -h | rg porcelain`
- hints 明确说明“不会按网络抖动自动重试”
- hints 引导先看 `ccclaw doctor`

同时在 [runtime.go](/opt/src/ccclaw/src/internal/app/runtime.go) 的总 `doctor` 中新增：

- `jj/git 同步能力`

这样安装后、升级后、现场排障时都能在真正执行任务前看到环境缺口。

### 4. 文档与安装树说明同步

本次更新：

- [README.md](/opt/src/ccclaw/README.md)
- [src/dist/README.md](/opt/src/ccclaw/src/dist/README.md)
- [src/ops/examples/app-readme.md](/opt/src/ccclaw/src/ops/examples/app-readme.md)
- [src/dist/ops/examples/app-readme.md](/opt/src/ccclaw/src/dist/ops/examples/app-readme.md)

统一补充：

- 先看 `ccclaw doctor`
- 若 `jj/git 同步能力` 失败，执行：
  - `jj --version`
  - `git --version`
  - `git fetch -h | rg porcelain`

## 测试

### 自动化测试

执行：

```bash
cd /opt/src/ccclaw/src
go test ./...
```

结果通过。

本次新增覆盖点：

- `git < 2.41.0` 时前置失败，不进入 fetch
- `git fetch -h` 缺少 `--porcelain` 时前置失败，不进入 fetch
- `jj git fetch` 直接报 capability mismatch 时，不再继续 retry
- finalize assessment 对 `ErrUnsupportedGit` 仍会归为 `version_mismatch + pause`

### 当前主机现场验证

执行：

```bash
jj --version
git --version
git fetch -h | rg -- '--porcelain'
```

结果：

- `jj 0.39.0-d9689cd9b51b4139d2842fcf6c30f65f4eed8cd1`
- `git version 2.39.5`
- `git fetch -h` 中未检出 `--porcelain`

这与 #82 现场现象一致，说明新 probe 能在真正进入 repo sync 前提前暴露这类兼容性缺口。

## 结论

Issue #84 本轮完成后，`jj/git` 兼容问题已经从“执行到 fetch/push 才暴露”推进为：

- `doctor` 可提前发现
- `SyncRepo()` 可前置失败
- 重试环会对 capability mismatch 直接短路
- `status/reporter` 会继续统一落到 `version_mismatch`
- 运维文档给出同一套排障动作

这样 #82 那类环境问题不再混进网络重试，也不会继续在 `FINALIZING` 里长时间伪装成普通抖动。
