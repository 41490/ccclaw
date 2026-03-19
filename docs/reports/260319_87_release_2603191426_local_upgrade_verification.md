# 260319_87_release_2603191426_local_upgrade_verification

对应 Issue：[#87](https://github.com/41490/ccclaw/issues/87)

## 背景

本轮目标：

1. 将当前 `main` 正式发布为 `26.03.19.1426`
2. 直接使用本地发布缓存 `/ops/logs/ccclaw/26.03.19.1426/` 对当前主机进行升级
3. 逐项核对发布资产、缓存资产、解包后二进制与本机安装结果是否一致
4. 用 GitHub Issue 留存升级过程、结果与最小人工检验建议

额外约束：

- Issue 不追加 `ccclaw` 标签
- 升级必须直接使用本地缓存安装包，不走 GitHub 下载升级链路

## 发布前核查

### 1. 主线与已发布状态

- 当前 `main`：`99871db1a50f6f9933f20cc7556c9ba8d056d4f5`
- 发布前最新 release：`26.03.19.1212`
- 当前主机已安装版本：`26.03.18.2026`

其中 `26.03.19.1212` 尚未覆盖当前 `main`，因此本轮需要重新发布。

### 2. 发布前回归

执行：

```bash
cd /opt/src/ccclaw/src
make test
bash tests/install_regression.sh
```

结果：

- `go test ./...` 通过
- `install_regression.sh` 全量通过

### 3. 发布前工作树状态

本地工作树在执行过程中会被运行态自动刷新 `src/internal/app/context.md`，导致 `release-preflight` 在当前仓库直跑失败。

因此本轮正式发布改为：

- 使用当前 `origin/main`
- 在临时干净克隆中执行 `src/Makefile`
- 避免把运行态时间戳文件误带入 release 流程

这不改变发布来源，仍然满足“统一由 `src/Makefile` 产出 release”的约束。

## 正式发布

执行方式：

```bash
version=$(TZ=Asia/Shanghai date '+%y.%m.%d.%H%M')
tmpdir=$(mktemp -d /tmp/ccclaw-release.XXXXXX)
git clone git@github.com:41490/ccclaw.git "$tmpdir"
cd "$tmpdir/src"
git checkout main
VERSION="$version" make release-preflight release
```

实际发布结果：

- 版本：`26.03.19.1426`
- target commit：`99871db1a50f6f9933f20cc7556c9ba8d056d4f5`
- release：<https://github.com/41490/ccclaw/releases/tag/26.03.19.1426>
- 发布时间（UTC）：`2026-03-19T06:26:30Z`

## 资产一致性验收

### 1. 本地缓存

本轮 release 已自动归档到：

- `/ops/logs/ccclaw/26.03.19.1426/ccclaw_26.03.19.1426_linux_amd64.tar.gz`
- `/ops/logs/ccclaw/26.03.19.1426/SHA256SUMS`

校验结果：

- package SHA256：
  - `c5786ac760b7639f2aac3a14a1aff2903cd4345c59991c61395e70fc9637e409`
- `SHA256SUMS` SHA256：
  - `edbeedaef604f6326cff3a7b470d47f6345c46cb8c628c1fb140547c40b57764`

执行：

```bash
cd /ops/logs/ccclaw/26.03.19.1426
sha256sum -c SHA256SUMS
```

结果通过。

### 2. 从 GitHub release 下载回验

执行：

```bash
tmpdir=$(mktemp -d /tmp/ccclaw-release-verify-1426.XXXXXX)
cd "$tmpdir"
gh release download 26.03.19.1426 \
  --repo 41490/ccclaw \
  --pattern 'ccclaw_26.03.19.1426_linux_amd64.tar.gz' \
  --pattern 'SHA256SUMS'
cmp -s ccclaw_26.03.19.1426_linux_amd64.tar.gz /ops/logs/ccclaw/26.03.19.1426/ccclaw_26.03.19.1426_linux_amd64.tar.gz
cmp -s SHA256SUMS /ops/logs/ccclaw/26.03.19.1426/SHA256SUMS
sha256sum -c SHA256SUMS
```

结果：

- `cmp` 包文件返回 `0`
- `cmp` 校验文件返回 `0`
- `sha256sum -c SHA256SUMS` 通过

说明：

- GitHub release 下载资产
- `/ops/logs/ccclaw/26.03.19.1426/` 本地缓存

两者逐字节一致。

### 3. 解包后二进制版本

执行：

```bash
tmpdir=$(mktemp -d /tmp/ccclaw-unpack-1426.XXXXXX)
tar -C "$tmpdir" -xzf /ops/logs/ccclaw/26.03.19.1426/ccclaw_26.03.19.1426_linux_amd64.tar.gz
"$tmpdir/ccclaw_26.03.19.1426_linux_amd64/bin/ccclaw" -V
```

结果：

- 输出：`26.03.19.1426`

这说明本地缓存包中的二进制版本语义正确。

## 使用本地缓存升级当前主机

### 1. 升级前基线

- `~/.ccclaw/bin/ccclaw -V`
  - `26.03.18.2026`
- `scheduler status`
  - `request=systemd effective=systemd`
- `config.toml` SHA256
  - `2843dede8db8f422bb9e5c9e9e95bb00e36ba130286c6571173ad14810785491`
- `home_repo`
  - `/opt/data/9527`
- `home_repo HEAD`
  - `ea2f8dfc6e19231bc5be7cd10eaa66d6b4fb06c1`
- `home_repo` 分支状态
  - `## main...origin/main [ahead 4]`

升级前 `doctor` 结果：

- 原版本下为 `18` 项通过

### 2. 升级执行

本轮未使用 `~/.ccclaw/upgrade.sh` 远端下载，而是直接使用本地缓存包：

```bash
tmpdir=$(mktemp -d /tmp/ccclaw-local-upgrade-1426.XXXXXX)
tar -C "$tmpdir" -xzf /ops/logs/ccclaw/26.03.19.1426/ccclaw_26.03.19.1426_linux_amd64.tar.gz
CCCLAW_VERSION=26.03.19.1426 \
  "$tmpdir/ccclaw_26.03.19.1426_linux_amd64/install.sh" \
  --yes \
  --app-dir /home/zoomq/.ccclaw \
  --home-repo /opt/data/9527 \
  --upgrade-home-repo
```

安装器摘要：

- 版本切到 `26.03.19.1426`
- 程序目录保持 `/home/zoomq/.ccclaw`
- 本体仓库保持 `/opt/data/9527`
- 调度器保持 `request=systemd, effective=systemd`
- 自动完成 `systemctl --user daemon-reload` 与托管 timer 重启

## 升级后逐项核对

### 1. 版本与命令链接

- `~/.ccclaw/bin/ccclaw -V`
  - `26.03.19.1426`
- `~/.local/bin/ccclaw -V`
  - `26.03.19.1426`

结论：

- 主程序与用户命令链接均已切到新版本

### 2. 调度与 systemd

- `scheduler status`
  - `request=systemd effective=systemd`
- `systemctl --user list-unit-files 'ccclaw*'`
  - 共 `10` 个 unit
- `systemctl --user list-timers --all 'ccclaw*'`
  - 共 `5` 个 timer

结论：

- 升级未破坏当前 `systemd --user` 托管状态

### 3. 配置边界

升级后 `config.toml` SHA256 仍为：

- `2843dede8db8f422bb9e5c9e9e95bb00e36ba130286c6571173ad14810785491`

结论：

- 普通配置未被误写或迁移

### 4. `home_repo` 边界

升级后核查：

- `git -C /opt/data/9527 rev-parse HEAD`
  - `ea2f8dfc6e19231bc5be7cd10eaa66d6b4fb06c1`
- `git -C /opt/data/9527 status --short --branch`
  - 仍为 `## main...origin/main [ahead 4]`
  - 既有 `kb/journal/**`、`kb/assay/gap-signals.md`、`kb/context.md`、`kb/memory/` 运行态内容仍在

结论：

- 未新增 seed commit
- 未误覆盖现有用户记忆或运行态产物

## 升级后健康检查

### 1. 成立项

升级后以下项目确认成立：

1. 版本切换成功
2. 调度器仍正常工作
3. systemd unit/timer 数量与启用状态保持稳定
4. 配置文件未变
5. `home_repo` 未被误覆盖

### 2. 新暴露出的环境缺口

升级后执行 `doctor`，结果从旧版的 `18` 项通过，变成：

- `19` 项检查
- `1` 项失败

失败项：

- `jj/git 同步能力`

现场信息：

- `jj 0.39.0`
- `git 2.39.5`
- `fetch_porcelain=false`

失败文案明确指出：

- 当前 git 缺少 `git fetch --porcelain` 能力
- 不满足当前 `jj` 版本同步要求
- 需升级 git 至 `2.41.0+` 或切换匹配的 `jj` 版本

这说明：

1. 升级本身成功
2. 新版本把原先隐藏的环境兼容缺口前置暴露出来了
3. 若不处理该环境问题，后续任务进入 `sync_target` 时仍可能走：
   - `failure_class=version_mismatch`
   - `failure_mode=pause`

因此，本轮不能简单写成“全部通过”，准确表述应为：

- 发布成功
- 本机升级成功
- 调度和配置边界正常
- 但 `repo sync` 前置体检现在会明确报告 `jj/git` 兼容性不足

## GitHub 留档

本轮已创建说明 Issue：

- <https://github.com/41490/ccclaw/issues/87>

核查结果：

- `labels=[]`
- 未追加 `ccclaw` 标签

Issue 中已包含：

- 发布信息
- 本地缓存与 GitHub 资产一致性说明
- 本机升级过程
- 升级成果与残留问题
- 最小人工检验建议

## 建议的最小人工检验

建议在控制仓库 `41490/ccclaw` 直接执行一轮最小闭环验收，优先使用 maintain 及以上权限账号。

### 验收步骤

1. 创建临时 Issue，正文只要求最小动作：

```markdown
target_repo: 41490/ccclaw

请执行以下最小验收：
1. 运行 `ccclaw -V`
2. 运行 `ccclaw doctor`
3. 在结果评论中回填两条命令的关键输出
4. 若出现 `FINALIZING` 或同步失败，请明确回填失败类型与处理策略
5. 任务完成后使用 `/ccclaw [DONE]` 收口
```

2. 等待 ingest / patrol 将该 Issue 从 `NEW` 推进到 `RUNNING`
3. 确认 Issue 至少先出现首条结果可见回帖
4. 若 `sync_target` 因兼容问题失败，应明确看到：
   - `失败类型: version_mismatch`
   - `处理策略: pause`
5. 主机侧再补只读核查：

```bash
~/.ccclaw/bin/ccclaw status --limit 5
~/.ccclaw/bin/ccclaw stats today
```

### 判定标准

- 版本应回填为 `26.03.19.1426`
- Issue 不应再出现“完全无回帖”的旧问题
- 即使收尾失败，也应先看到结果回帖和 finalize failure 说明
- `status/stats` 应能反映这轮任务记录

## 结论

本轮已经完成：

1. 正式发布 `26.03.19.1426`
2. 证明本地缓存与 GitHub release 资产一致
3. 直接使用本地缓存包升级当前主机
4. 证明程序版本、调度状态、配置边界与 `home_repo` 边界均保持正确
5. 用不带 `ccclaw` 标签的 GitHub Issue 留存了升级记录

当前唯一未全绿项不是升级失败，而是新版本成功暴露出了既有主机环境的 `jj/git` 兼容缺口。后续应优先处理该环境问题，再做一轮真实 Issue 回归。
