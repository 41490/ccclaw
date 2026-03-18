# ccclaw release tree

本目录是 `ccclaw` 的 release 安装树源，也是发布包解压后的默认安装入口。

```bash
bash install.sh
```

## 目录内容

- `install.sh`：交互式安装、预检、调度选择与首次配置采集
- `upgrade.sh`：程序发布树升级入口，固定下载 `41490/ccclaw` 最新官方 release 并校验 `SHA256SUMS`
- `bin/ccclaw`：主二进制
- `ops/`：配置样板、systemd 单元、release notes 模板与运维脚本
- `ops/scripts/ccclaude`：Claude 执行包装器源，安装时复制到 `~/.ccclaw/bin/ccclaude`
- `croncfg.md`：`cron` 专家手工配置说明，安装时复制到程序目录
- `kb/`：本体仓库初始化目录树
- `kb/**/CLAUDE.md`：记忆记录与整理规约模板，升级时无损合并
- `SHA256SUMS`：release 资产校验文件

## 三类仓库术语

- 控制仓库：接收 GitHub Issue、评论审批与执行门禁的官方控制面仓库，固定为 `41490/ccclaw`
- 本体仓库：保存长期记忆、设计、报告与 `kb/**` 的仓库，默认路径 `/opt/ccclaw`
- 任务仓库：通过 `[[targets]]` 绑定的实际工作仓库，可绑定多个

三者可以是同一个仓库，也可以拆分；但职责必须分离，不应混写。

## 默认安装边界

- 程序目录：`~/.ccclaw`
- 本体仓库模式：`init|remote|local`
- 任务仓库模式：`none|remote|local`
- remote 任务仓库固定 clone 入口：`/opt/src/3claw/owner/repo`
- 调度模式：`auto|systemd|cron|none`
- Claude 探查：默认只读，不自动改写现有 marketplace / plugins / `rtk` 全局配置

## 调度行为

- `auto`：优先部署 `systemd --user`
- 若 `systemd --user` 不可部署，安装器进入 `none + 手工 cron 指引`，不再自动改写当前用户 `crontab`
- 受控 `cron` 保留为专家手工工具，会写入 `ingest/patrol/journal` 三类周期任务
- 专家手工 `cron` 仅管理带 `ccclaw` 标记的块，不覆盖用户其它 `crontab` 规则
- `systemd --user` 额外托管 `ccclaw-archive.timer` 与 `ccclaw-sevolver.timer`
- `ccclaw-archive.timer` 每周导出历史周 JSONL 为 Parquet，`ccclaw-sevolver.timer` 每晚维护 Skill 生命周期与缺口信号
- 日常切换优先使用 `ccclaw scheduler use systemd|none`；`cron` 仅在专家手工场景下启用
- 如需清理受控块，可执行：

```bash
~/.ccclaw/bin/ccclaw scheduler disable-cron
# 或
bash install.sh --remove-cron
```

## 安装后建议

1. 运行 `~/.ccclaw/bin/ccclaw doctor`
   - 若 `jj/git 同步能力` 失败，先执行 `jj --version`、`git --version`、`git fetch -h | rg porcelain`
2. 检查本体仓库 / 任务仓库 配置是否符合预期
3. 若体检结果为 `systemd`，且当前会话可直连 user bus，安装/升级会自动启用或重启 timer
4. 若体检结果为 `systemd`，但当前会话无法直连 user bus，请在登录会话中手工执行 `systemctl --user daemon-reload && systemctl --user enable --now ...`
5. 若需要专家手工启用 `cron`，先阅读 `~/.ccclaw/croncfg.md`，再确认 `systemd --user` 确实不可用，并用 `crontab -l` 或 `ccclaw scheduler enable-cron` 复核受控规则
6. 如需单独查看或切换调度后端：

```bash
~/.ccclaw/bin/ccclaw status --json
~/.ccclaw/bin/ccclaw scheduler status
~/.ccclaw/bin/ccclaw scheduler status --json
~/.ccclaw/bin/ccclaw scheduler doctor
~/.ccclaw/bin/ccclaw scheduler doctor --json
~/.ccclaw/bin/ccclaw scheduler timers
~/.ccclaw/bin/ccclaw scheduler timers --wide
~/.ccclaw/bin/ccclaw scheduler timers --raw
~/.ccclaw/bin/ccclaw scheduler timers --json
~/.ccclaw/bin/ccclaw scheduler logs -f
~/.ccclaw/bin/ccclaw --log-level debug run
~/.ccclaw/bin/ccclaw scheduler use systemd
~/.ccclaw/bin/ccclaw scheduler use none
```

如需专家手工切换到 `cron`，先阅读 `~/.ccclaw/croncfg.md`：

```bash
~/.ccclaw/bin/ccclaw scheduler use cron
```

说明：

- `status --json` 提供整机运行态快照的结构化出口，适合巡检脚本直接消费
- `scheduler status --json` 提供请求模式、生效模式、修复建议与 systemd/cron 原始探测字段
- `doctor` 会额外检查 `jj/git` 同步能力，提前暴露 `git fetch --porcelain` 缺失这类环境兼容问题
- `scheduler doctor` 会额外检查 linger、user bus、unit 漂移，以及最近失败的托管 service；`--json` 提供摘要与逐项检查结果
- `scheduler timers` 默认只保留关键列；`--wide` 显示 `CAL_RAW/CAL_CFG` 与双时区时间；`--raw` 输出 key=value 原始字段；`--json` 提供结构化脚本视图
- `[scheduler.logs].level` 会同时影响 `ingest/patrol/journal` 的运行态输出阈值，以及 `scheduler logs` 的默认查看过滤
- 手工排障时可用 `--log-level debug` 临时放大本次命令日志，不改写配置
- `[scheduler.logs].retention_days`、`max_files`、`compress` 只治理带 ccclaw 归档头的受管文件，不影响归档目录里的人工文件

## Shell 集成

如需让当前 shell 直接识别 `ccclaw`：

```bash
bash install.sh --inject-shell bashrc
```

如需回滚受控 shell 块：

```bash
bash install.sh --remove-shell bashrc
```
