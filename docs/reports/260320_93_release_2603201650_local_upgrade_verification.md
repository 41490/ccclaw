# 260320_93_release_2603201650_local_upgrade_verification

对应 Issue：[#93](https://github.com/41490/ccclaw/issues/93)

## 背景

本轮目标：

1. 将 `main` 当前代码正式发布为 `26.03.20.1650`
2. 复核发布链路继续满足单 artifact 约束
3. 直接使用本地发布缓存 `/ops/logs/ccclaw/26.03.20.1650/` 升级当前主机部署
4. 用 GitHub Issue 留存升级过程、结果与最小人工检验建议

## 发布前核查

### 1. 主线与已发布状态

- 当前 `HEAD` / `origin/main`：`ba18e2d329afe0ee1e4c0b8f4cb543263053ab39`
- 发布前最新 release：`26.03.19.1426`
- 该 release target commit：`99871db1a50f6f9933f20cc7556c9ba8d056d4f5`
- 当前主机已安装版本：`26.03.19.1426`

结论：

- 当前 `main` 已领先于最新 release，需要重新正式发版

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

当前仓库在执行回归与运行态探查后，`src/internal/app/context.md` 被自动刷新时间戳，导致当前工作树不再满足 `release-preflight` 的干净树要求。

因此本轮正式发布改为：

- 仍以当前 `origin/main` 为发布来源
- 在临时干净克隆中执行 `src/Makefile`
- 首次构建出的 `.release` 直接复用到 GitHub release 创建
- 仅修正临时克隆的 `origin` 指向 GitHub，避免 `gh` 将本地路径远端识别为未知主机

该调整不改变发布来源，也不引入第二份 artifact。

## 正式发布

实际发布结果：

- 版本：`26.03.20.1650`
- target commit：`ba18e2d329afe0ee1e4c0b8f4cb543263053ab39`
- release：<https://github.com/41490/ccclaw/releases/tag/26.03.20.1650>
- 发布时间（UTC）：`2026-03-20T08:51:06Z`

本轮 GitHub release 资产：

- `ccclaw_26.03.20.1650_linux_amd64.tar.gz`
  - 大小：`7135237` bytes
- `SHA256SUMS`
  - 大小：`106` bytes

## 资产一致性验收

### 1. `.release` 与本地缓存

本轮构建完成后，归档缓存位于：

- `/ops/logs/ccclaw/26.03.20.1650/ccclaw_26.03.20.1650_linux_amd64.tar.gz`
- `/ops/logs/ccclaw/26.03.20.1650/SHA256SUMS`

校验结果：

- 包文件 SHA256：
  - `f32ee0ca6b0f28cb15541d5b70a138d627b636e2a790aff2245d3de69a455cfb`
- 校验文件 SHA256：
  - `73da7eca8b27f5010b7adad2686899263d110b1c96db7f8abff3ffda0e8aa35f`
- `.release` 与 `/ops/logs/ccclaw/26.03.20.1650/` 中对应文件 `cmp` 返回均为 `0`
- `sha256sum -c SHA256SUMS` 通过

### 2. 从 GitHub release 下载回验

执行下载回验后：

- 下载包文件 SHA256：
  - `f32ee0ca6b0f28cb15541d5b70a138d627b636e2a790aff2245d3de69a455cfb`
- 下载校验文件 SHA256：
  - `73da7eca8b27f5010b7adad2686899263d110b1c96db7f8abff3ffda0e8aa35f`
- 与 `/ops/logs/ccclaw/26.03.20.1650/` 中对应文件 `cmp` 返回均为 `0`
- `sha256sum -c SHA256SUMS` 通过

结论：

- `.release`、本地缓存、GitHub release 下载资产三者逐字节一致
- 当前发布链路继续满足 `#73` 已拍板的单 artifact 约束

### 3. 解包后二进制版本

执行：

```bash
tar -C <tmpdir> -xzf /ops/logs/ccclaw/26.03.20.1650/ccclaw_26.03.20.1650_linux_amd64.tar.gz
<tmpdir>/ccclaw_26.03.20.1650_linux_amd64/bin/ccclaw -V
```

结果：

- 输出：`26.03.20.1650`

说明本地缓存包中的二进制版本语义正确。

## 使用本地缓存升级当前主机

### 1. 升级前基线

- `~/.ccclaw/bin/ccclaw -V`
  - `26.03.19.1426`
- `~/.local/bin/ccclaw -V`
  - `26.03.19.1426`
- `scheduler status`
  - `request=systemd effective=systemd`
- `config.toml` SHA256
  - `2843dede8db8f422bb9e5c9e9e95bb00e36ba130286c6571173ad14810785491`
- `home_repo`
  - `/opt/data/9527`
- `home_repo HEAD`
  - `2e5db6eb6e3d15848e7fbe16977b6c34eb3495cd`
- `home_repo` 状态
  - `## HEAD (no branch)`
  - 包含既有 `kb/context.md`、`kb/journal/**`、`kb/memory/nodes.jsonl` 运行态改动

### 2. 升级执行

本轮未使用 `~/.ccclaw/upgrade.sh` 的远端下载链路，而是直接使用本地缓存包：

```bash
tar -C <tmpdir> -xzf /ops/logs/ccclaw/26.03.20.1650/ccclaw_26.03.20.1650_linux_amd64.tar.gz
CCCLAW_VERSION=26.03.20.1650 \
  <tmpdir>/ccclaw_26.03.20.1650_linux_amd64/install.sh \
  --yes \
  --app-dir /home/zoomq/.ccclaw \
  --home-repo /opt/data/9527 \
  --upgrade-home-repo
```

安装器摘要：

- 版本切到 `26.03.20.1650`
- 程序目录保持 `/home/zoomq/.ccclaw`
- 本体仓库保持 `/opt/data/9527`
- 调度器保持 `request=systemd, effective=systemd`
- 自动完成 `systemctl --user daemon-reload` 与托管 timer 重启

## 升级后逐项核对

### 1. 版本与命令链接

- `~/.ccclaw/bin/ccclaw -V`
  - `26.03.20.1650`
- `~/.local/bin/ccclaw -V`
  - `26.03.20.1650`

结论：

- 主程序与用户命令链接均已切到新版本

### 2. 调度与运行态体检

- `scheduler status`
  - `request=systemd effective=systemd`
- `doctor`
  - `failed=0 checks=19`
- `systemctl --user list-unit-files 'ccclaw*'`
  - 共 `10` 个 unit
- `systemctl --user list-timers --all 'ccclaw*'`
  - 共 `5` 个 timer

结论：

- 升级未破坏当前 `systemd --user` 托管状态
- 运行态体检通过

### 3. 配置迁移边界

升级后 `config.toml` SHA256 变为：

- `99e273731803fce5618a52c7de3d76db460340881f372edb1be36824daa2cdf1`

同时安装器明确输出：

- `已迁移配置: /home/zoomq/.ccclaw/ops/config/config.toml`

补充验收：

- `ccclaw target list` 仍显示 `41490/ccclaw` 为启用且默认 target
- `scheduler status` 正常
- `doctor` 正常

结论：

- 本轮存在预期内配置迁移
- 当前未观察到因配置迁移导致的目标仓绑定或调度异常

### 4. `home_repo` 边界

升级后核查：

- `git -C /opt/data/9527 rev-parse HEAD`
  - 仍为 `2e5db6eb6e3d15848e7fbe16977b6c34eb3495cd`
- `git -C /opt/data/9527 status --short --branch`
  - 仍为 `## HEAD (no branch)`
  - 仍只包含既有 `kb/context.md`、`kb/journal/**`、`kb/memory/nodes.jsonl` 运行态内容

结论：

- 未新增 seed commit
- 未误覆盖现有用户记忆或运行态产物

## 建议最小人工检验案例

建议人工用一个 maintain 成员可自动执行的最小案例复核当前 Issue 驱动核心链路：

1. 新建带 `ccclaw` 标签的测试 Issue，例如标题：`test: 26.03.20.1650 核心链路最小验收`
2. Issue body 只放最小目标：
   - 在目标仓库创建 `tmp/ccclaw-smoke.txt`
   - 写入一行带时间戳的文本
   - 在评论中回报文件路径与写入内容
   - 再删除该文件并回报删除完成
3. 观察 `ingest -> 执行 -> 回帖 -> status/stats` 是否闭环
4. 验证完成后关闭该测试 Issue，避免污染 backlog

若要补测非 maintain 成员审批门禁，可再额外使用普通成员新建一个带 `ccclaw` 标签的测试 Issue，并由受信任成员评论：

- `/ccclaw approve`

## 结论

本轮 `26.03.20.1650` 已完成：

1. 正式 release 发布
2. 本地缓存一致性回验
3. 当前主机本地缓存升级
4. 升级后版本、调度、配置迁移与 target 绑定验收
5. 最小人工验收指引沉淀

当前未发现本轮发布引入的阻断性回归。
