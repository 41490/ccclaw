# 260318_81_release_2603182026_publish_announce_upgrade

对应 Issue：[#81](https://github.com/41490/ccclaw/issues/81)

## 背景

本轮目标：

1. 将 `main` 当前代码正式发布为 `26.03.18.2026`
2. 在 Discussions 的 `Announcements` 分类补一则发布公告
3. 直接复用本地发布缓存 `/ops/logs/ccclaw/26.03.18.2026/` 升级当前主机部署
4. 用新建 Issue 说明本次核心修订，以及最小人工检验 CCClaw Issue 驱动工作流的方法

发布前已先复核既有决策与约束：

- `src/Makefile` 仍采用单 artifact 发布链路
- 相关历史决策已在 `#73`、`#76` 中沉淀
- 当前仓库 `HEAD` 与 `origin/main` 一致
- 发布前测试通过

## 发布结果

- 版本：`26.03.18.2026`
- 发布时间：`2026-03-18T12:28:59Z`
- Release：<https://github.com/41490/ccclaw/releases/tag/26.03.18.2026>
- 公告：<https://github.com/41490/ccclaw/discussions/80>
- 发布提交：`59aa631f327f265b70f389d4cde25dcb24522c80`

本次 release 核心修订：

1. 树型记忆 v2 合入主线，新增 `ccclaw recall` 与 recall 节点/上下文/评分链路
2. `journal` 收口接入冷启动 recall，日报尾段可回带长期记忆
3. `sevolver` 候选技能识别默认聚合近 14 天信号，并统一技能生命周期目录到 `archived`
4. `stream-json` 收口继续加固结果优先与失败诊断，降低成功任务被误判 `FAILED/DEAD` 的概率
5. 本地产物隔离补齐 `.worktrees/` 与 recall 生成文件忽略规则

## 发布前验证

执行：

```bash
cd /opt/src/ccclaw/src && make test
cd /opt/src/ccclaw/src && bash tests/install_regression.sh
gh auth status
git rev-parse HEAD
git rev-parse @{upstream}
```

结果：

- `go test ./...` 通过
- `install_regression.sh` 全量通过
- `gh` 登录正常
- `HEAD` 与 `origin/main` 相同

## 资产一致性校验

本轮发布按单 artifact 流执行：

```bash
cd /opt/src/ccclaw/src
VERSION=26.03.18.2026 make release-preflight release-assets archive
gh release create 26.03.18.2026 ...
```

关键摘要：

- `.release/ccclaw_26.03.18.2026_linux_amd64.tar.gz`
  - SHA256：`773dbaad19b7dcaf0a5d7d8270b53563c9968d68bd59324470502e879e69fcb9`
- `.release/SHA256SUMS`
  - SHA256：`d747fbedae7084d1809cf61ba4b2cdfcc93e3aa9a879e65d4a321d51f8bda324`
- `/ops/logs/ccclaw/26.03.18.2026/` 中对应两份文件的 SHA256 与 `.release/` 完全一致
- 从 GitHub release 再下载回验后，包体与 `SHA256SUMS` 也均与本地缓存逐字节一致

结论：

1. 本地缓存与 GitHub release 资产严格一致
2. 当前发布链路继续满足 `#73` 已拍板的单 artifact 约束

## 本机升级验收

升级前状态：

- 已部署版本：`26.03.16.1742`
- 调度状态：`request=systemd effective=systemd`
- `home_repo`：`/opt/data/9527`
- 升级前 `home_repo` 状态：`## main...origin/main [ahead 3]`

升级方式：

```bash
tar -C <tmpdir> -xzf /ops/logs/ccclaw/26.03.18.2026/ccclaw_26.03.18.2026_linux_amd64.tar.gz
CCCLAW_VERSION=26.03.18.2026 <tmpdir>/ccclaw_26.03.18.2026_linux_amd64/install.sh \
  --yes \
  --app-dir /home/zoomq/.ccclaw \
  --home-repo /opt/data/9527 \
  --upgrade-home-repo
```

升级后结果：

- `~/.ccclaw/bin/ccclaw -V`：`26.03.18.2026`
- `~/.ccclaw/bin/ccclaw scheduler status`：
  `request=systemd effective=systemd`
- `~/.ccclaw/bin/ccclaw doctor`：`failed=0 checks=18`
- `systemctl --user list-unit-files 'ccclaw*'`：仍为 10 个目标 unit，包含 `ccclaw-sevolver.timer`

本轮升级还触发了一次既有设计内的 `home_repo` 受管模板刷新：

- 新提交：`ea2f8dfc6e19231bc5be7cd10eaa66d6b4fb06c1`
- 提交消息：`seed ccclaw home repo (v26.03.18.2026)`
- `diff-tree` 显示仅改动：`kb/CLAUDE.md`
- 升级后 `home_repo` 状态为：`## main...origin/main [ahead 4]`

这说明：

1. 升级程序继续只刷新受管模板文件
2. 未覆盖现有 `kb/journal/**` 与其它用户记忆内容

## 公告与追踪

已完成：

1. `Announcements` 公告：<https://github.com/41490/ccclaw/discussions/80>
2. 发布说明 Issue：<https://github.com/41490/ccclaw/issues/81>

其中 `#81` 特别补充了：

- 本次 release 核心修订摘要
- 不带 `ccclaw` 标签的发布后说明
- 一轮最小人工检验 CCClaw Issue 驱动工作流的操作步骤
- maintain 自动执行路径与 `/ccclaw approve` 审批补测建议

## 结论

本轮 `26.03.18.2026` 已完成：

1. 正式 release 发布
2. 公告发布
3. 本地缓存一致性回验
4. 现网主机升级
5. 最小人工验收指引沉淀

当前未发现本轮发布引入的阻断性回归。后续若要验证“核心 Issue 驱动工作流”运行态闭环，应按 `#81` 中的最小验收案例再执行一次真实 Issue 演练。
