# 260316_76_release_2603161742_local_upgrade_verification

对应 Issue：#76 `ops: 发布 26.03.16.1742 并完成本机缓存升级验收`

- Issue：<https://github.com/41490/ccclaw/issues/76>
- release：<https://github.com/41490/ccclaw/releases/tag/26.03.16.1742>
- tag：`26.03.16.1742`
- target commit：`f34beb9aab1dd7feffdc60efdaf1b6124b874e75`
- 发布时间（UTC）：`2026-03-16T09:44:25Z`

## 决策核查

本轮执行前先核对了与发布/升级直接相关的已拍板议题：

- Issue #73 已拍板要求：
  - `/ops/logs/ccclaw/<version>/` 必须与 GitHub release 资产严格一致
  - 单次 release 只允许产出一份 artifact
  - 本地缓存继续采用“本地生成后即复制”，但上传与缓存必须复用同一份文件
- Issue #58 已拍板要求：
  - 升级时允许自动处理受管模板
  - 范围限定在 `kb/**/CLAUDE.md` 等受管文件
  - 不得误覆盖用户记忆内容

因此本轮验收重点不是“能不能升级”，而是同时确认：

1. 发布资产、缓存资产与安装后二进制版本严格一致
2. 升级不会误改现有 `config.toml` 与 `home_repo` 用户内容

## 发布前核验

### 1. 仓库与账号状态

- `git status --short --branch`
  - 工作树干净
  - 当前分支：`main`
- `git rev-parse HEAD`
  - 当前提交：`f34beb9aab1dd7feffdc60efdaf1b6124b874e75`
- `git rev-parse @{upstream}`
  - 与 `origin/main` 完全一致
- `gh auth status`
  - 已登录账号 `ZoomQuiet`

### 2. 版本与现网基线

- 本次固定发布版本：`26.03.16.1742`
- 升级前程序版本：`26.03.15.1915`
- 升级前 `scheduler status`：
  - `request=systemd effective=systemd`
- 升级前 `systemctl --user list-unit-files 'ccclaw*'`
  - 共 10 个 unit
- 升级前 `systemctl --user list-timers --all 'ccclaw*'`
  - 共 5 个 timer
- 升级前 `config.toml` SHA256：
  - `2843dede8db8f422bb9e5c9e9e95bb00e36ba130286c6571173ad14810785491`
- 升级前 `home_repo`：
  - 仓库：`/opt/data/9527`
  - `HEAD`：`1d34a837d64efb116fc9be9943eba0dd82f05262`
  - 分支状态：`## main...origin/main [ahead 3]`
  - 运行态改动仍在：
    - `kb/assay/gap-signals.md`
    - `kb/journal/**`
    - `kb/journal/sevolver/**`

### 3. 发布前回归

执行：

```bash
cd /opt/src/ccclaw/src
make test VERSION=26.03.16.1742
make release-preflight VERSION=26.03.16.1742
```

结果：

- `make test` 全通过
- `make release-preflight` 通过

## 正式发布

执行：

```bash
cd /opt/src/ccclaw/src
make release VERSION=26.03.16.1742
```

结果：

- 生成 `.release/ccclaw_26.03.16.1742_linux_amd64.tar.gz`
- 生成 `.release/SHA256SUMS`
- 归档到 `/ops/logs/ccclaw/26.03.16.1742/`
- 成功创建 GitHub release：
  - <https://github.com/41490/ccclaw/releases/tag/26.03.16.1742>

## 发布资产一致性验收

### 1. 本地 `.release/` 与本机缓存

- package SHA256：
  - `5523b4cec75282dcaaf5372a5128d7e3b6bac45816036dfb92e28c867792e34f`
- `SHA256SUMS` SHA256：
  - `d9dc1a27dfffb2b5b1409af104634312ef65040a64fc3c9b5f77ba391a5396ba`
- `/ops/logs/ccclaw/26.03.16.1742/` 中同名文件 SHA256 完全一致
- 在缓存目录执行 `sha256sum -c SHA256SUMS` 通过

### 2. 从 GitHub release 回下载校验

执行：

```bash
gh release download 26.03.16.1742 \
  --repo 41490/ccclaw \
  --pattern 'ccclaw_26.03.16.1742_linux_amd64.tar.gz' \
  --pattern 'SHA256SUMS'
```

结果：

- 下载回来的 package SHA256：
  - `5523b4cec75282dcaaf5372a5128d7e3b6bac45816036dfb92e28c867792e34f`
- 下载回来的 `SHA256SUMS` SHA256：
  - `d9dc1a27dfffb2b5b1409af104634312ef65040a64fc3c9b5f77ba391a5396ba`
- `cmp` 结果：
  - `package_equal=yes`
  - `archive_equal=yes`
  - `checksum_equal=yes`
- 在下载目录执行 `sha256sum -c SHA256SUMS` 通过

### 3. 解包后二进制版本核对

从本机缓存包解压后执行：

```bash
bin/ccclaw -V
```

结果：

- 输出：`26.03.16.1742`

这说明：

- `.release/` 上传资产
- `/ops/logs/ccclaw/<version>/` 本地缓存
- GitHub release 下载资产
- 解包后二进制版本

已经对齐到同一版本语义。

## 使用本机缓存升级当前主机

### 1. 升级方式

没有走远端下载，直接使用本机缓存 release：

```bash
tar -xzf /ops/logs/ccclaw/26.03.16.1742/ccclaw_26.03.16.1742_linux_amd64.tar.gz
CCCLAW_VERSION=26.03.16.1742 install.sh \
  --yes \
  --app-dir /home/zoomq/.ccclaw \
  --home-repo /opt/data/9527 \
  --upgrade-home-repo
```

### 2. 升级结果

- 升级后 `~/.ccclaw/bin/ccclaw -V`：
  - `26.03.16.1742`
- 升级后 `~/.local/bin/ccclaw -V`：
  - `26.03.16.1742`
- 升级前后 `config.toml` SHA256 完全一致：
  - `2843dede8db8f422bb9e5c9e9e95bb00e36ba130286c6571173ad14810785491`
- 升级前后 `home_repo HEAD` 完全一致：
  - `1d34a837d64efb116fc9be9943eba0dd82f05262`
- 升级前后 `home_repo` 脏文件集合完全一致：
  - 未新增 seed commit
  - 未新增受管模板误写
  - 未误覆盖现有 journal/gap 运行态内容

因此本轮升级同时满足：

1. 程序版本已切到新 release
2. 配置未被错误迁移或重写
3. `home_repo` 用户记忆未被覆盖

## 升级后健康检查

### 1. 配置与调度

- `ccclaw config`
  - 配置可正常解析
  - 关键路径仍为：
    - `app_dir = /home/zoomq/.ccclaw`
    - `home_repo = /opt/data/9527`
    - `kb_dir = /opt/data/9527/kb`
  - 执行模式仍为：
    - `executor.mode = "daemon"`
  - 调度模式仍为：
    - `scheduler.mode = "systemd"`

### 2. 运行态与系统单元

- `ccclaw scheduler status`
  - `request=systemd effective=systemd`
- `systemctl --user list-unit-files 'ccclaw*'`
  - 仍为 10 个 unit
- `systemctl --user list-timers --all 'ccclaw*'`
  - 仍为 5 个 timer
- `ccclaw doctor`
  - 18 项全部通过
- `ccclaw status`
  - 仍显示 daemon 为默认模式
  - 运行态未出现新异常

## 建议人工最小验收

建议人工补一次最小 gh-issue 驱动验收，而不是只看部署健康状态：

1. 用 maintain 成员新建一个带 `ccclaw` 标签的测试 Issue
2. Issue body 只保留一个最小动作，例如：
   - 在目标仓库创建一个临时文件
   - 写入一行文本
   - 再删除该文件
   - 最后回帖汇报执行结果
3. 观察以下链路是否闭环：
   - ingest 收到 Issue
   - run 正常执行
   - Issue 回帖
   - `status` 中状态收敛
4. 完成后关闭测试 Issue

若要顺带验审批门禁，可再补一个普通成员 Issue，并由受信任成员评论：

```text
/ccclaw approve
```

## 建议后台追查命令

```bash
~/.ccclaw/bin/ccclaw --config ~/.ccclaw/ops/config/config.toml --env-file ~/.ccclaw/.env status
~/.ccclaw/bin/ccclaw --config ~/.ccclaw/ops/config/config.toml --env-file ~/.ccclaw/.env scheduler status
~/.ccclaw/bin/ccclaw --config ~/.ccclaw/ops/config/config.toml --env-file ~/.ccclaw/.env doctor
systemctl --user list-timers --all 'ccclaw*' --no-pager
systemctl --user status ccclaw-ingest.service ccclaw-patrol.service ccclaw-journal.service ccclaw-sevolver.service --no-pager
journalctl --user -u ccclaw-ingest.service -u ccclaw-patrol.service -u ccclaw-journal.service -u ccclaw-sevolver.service -n 200 --no-pager
```

## 残留观察

本轮发布/升级链路本身正常，但运行态仍可见历史 Issue 被 Claude 新 `system/hook_started` 类事件误判为 `FAILED/DEAD` 的残留迹象。该问题已经有独立议题跟踪：

- Issue #75 `forensics: Claude 2.1.76 system stream 事件导致成功任务被误判 FAILED/DEAD`

它不阻塞本次 release 交付，但建议继续人工复核一次 gh-issue 最小链路，确认部署面正常、异常仅限于既有 stream 兼容问题。
