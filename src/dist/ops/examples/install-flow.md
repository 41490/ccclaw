# phase0.1 安装流程草案

## 安装拓扑

- 程序目录：`~/.ccclaw`
- 知识仓库：`/opt/ccclaw`，支持 `init|remote|local`
- 首个任务仓库：由安装交互确认，支持 `remote|local`
- remote 任务仓库固定 clone 入口：`/opt/src/3claw/owner/repo`
- 调度模式：`auto|systemd|cron|none`

## 交互项

### 敏感项，写入 `.env`

- `GH_TOKEN`：必须
- `ANTHROPIC_API_KEY`：可选；若本机 Claude 凭据已可用，可留空
- `GREPTILE_API_KEY`：可选

### 普通项，写入 `config.toml`

- `app_dir`
- `home_repo`
- `kb_dir`
- `var_dir`
- `log_dir`
- `targets[].repo`
- `targets[].local_path`
- `targets[].kb_path`

## 自动探查项

- `claude` 可执行文件与版本
- `~/.claude/settings.json`
- `~/.claude/.credentials.json`
- 已安装 plugins / marketplaces
- `gh` / `rg` / `rtk` / `git` / `node` / `npm` / `uv`
- `gh auth status` / `gh auth token`
- `systemctl --user` 与 `~/.config/systemd/user` 可用性

以上探查默认只读，不自动改写现有 `~/.claude` 配置。

## 默认策略

- 若本机已有 Claude 配置：仅只读探查并直接继承
- `control_repo` 固定为 `41490/ccclaw`，不再交互询问
- 不默认补装 plugins / marketplace / `example-skills`
- 若缺少 `rtk`：优先按官方 quick install 安装
- 不默认执行 `rtk init --global`
- 知识仓库默认 `init` 到 `/opt/ccclaw`，也允许 clone 远程仓库或接管本地仓库
- 任务仓库默认不绑定；若指定远程仓库，则 clone 到 `/opt/src/3claw/owner/repo`
- 若 `gh` 已登录，则优先复用 `gh auth token` 写入 `.env`
- 若 `systemd --user` 不可用，默认继续安装为 `none + 手工 cron 指引`，不自动托管 `cron`
- 升级只覆盖程序树，不覆盖知识仓库
