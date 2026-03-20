# CCClaw

```text
  o-o   o-o   o-o o
 /     /     /    |
O     O     O     |  oo o   o   o
 \     \     \    | | |  \ / \ /
  o-o   o-o   o-o o o-o-  o   o
```

> 专注节省 token 消耗的基于 Claude Code 的长效自主编排智能体

默认中文说明，英文版见 [README_en.md](README_en.md)。

`CCClaw` 也可简写为 `3Claw`。名字可以理解为一组开放缩写组合：

- `Claude | Common | Chaos | ...`
- `Code | Continuance | Calm | ...`
- `Claw`

## 项目简介

`ccclaw` 是一个以 **GitHub Issue 作为任务事实源**、以 **Claude Code 作为执行体**、以 **systemd --user 作为后台调度**、以 **本体知识库仓库作为长期记忆** 的 Linux 原生自主编排工具。

它当前已经完成 `phase0.4` 所要求的首个可下载安装 release 闭环，适合先在单机、单维护者或小团队开源协作场景中稳定运行。

相比“每次都手工打开 Claude Code 聊一轮”，`ccclaw` 的重点不是把 agent 做得更花，而是把以下高频损耗尽量移出高价模型执行阶段：

- Issue 巡检、门禁、调度、回写
- 固定目录、固定配置、固定安装拓扑
- 长期记忆与运行记录归档
- 多仓库绑定与默认目标路由
- 非管理员提案的自动拦截

## 核心特点

- **省 token**：把轮询、门禁、调度、目录治理、日志归档交给本地程序和 `systemd`，把高价值 token 留给真正需要 Claude Code 动手的阶段。
- **Issue 驱动**：直接复用开源项目最自然的协作入口，不额外引入新的任务系统。
- **Linux 原生**：优先使用 `systemd --user`，异常时进入 `none + 手工 cron 指引` 的收缩路径，不再把受控 `cron` 作为安装主链路，不强绑 Docker、K8s、外部 SaaS 控制面。
- **记忆与程序分离**：程序树固定在 `~/.ccclaw`，知识仓库固定在 `/opt/ccclaw`，升级不覆盖用户记忆。
- **安装边界清晰**：敏感信息只进 `.env`，普通配置只进 `.toml`，运行时不依赖调用者预先 `export` 环境变量。
- **开源协作可控**：`maintain` 及以上成员的 Issue 自动执行；其他 Issue 需要受信任成员评论 `/ccclaw <批准词>`，最新评论也可用否决词撤回。
- **可发布可升级**：release 从 `src/dist/` 出包，产物至少包含安装包与 `SHA256SUMS`，并提供 `upgrade.sh` 升级程序树。

## 适用场景

- 个人维护多个 GitHub 仓库，希望让 Claude Code 在后台持续巡检和执行。
- 小团队开源协作，希望保留 Issue 审计链，而不是把任务入口搬到聊天工具。
- 需要长期积累项目知识、计划、日报、技能模板，而不是每次上下文都重新喂给模型。
- 想把 Claude Code 用在长周期任务里，但不希望无意义轮询和重复解释消耗太多 token。
- 希望先落地一个极简、稳定、Linux-first 的编排内核，再逐步扩到更多 provider 或协作总线。

## 当前状态

- 当前阶段：`phase0.5`
- 默认平台：`linux/amd64`
- 默认程序目录：`~/.ccclaw`
- 默认知识仓库：`/opt/ccclaw`
- 知识仓库模式：`init | remote | local`
- 任务仓库模式：`none | remote | local`
- 默认执行模式：`daemon`（可按仓或全局回切 `tmux`）
- 默认调度方式：`auto`，优先 `systemd --user`；不可用时进入手工备选提示态
- 版本命名：`yy.mm.dd.HHMM`
- 版本时间基准：固定使用 `Asia/Shanghai`，不跟随发布机本地时区

当前已落地的关键能力：

- **调度后端分层**：`systemd --user` 是自动托管主路径，`cron` 保留为专家手工工具，`none` 用于显式停用；统一切换命令 `scheduler use`，统一状态查询 `scheduler status`
- **审批门禁增强**：支持多批准词和多否决词（中英文），旧配置一键迁移 `config migrate-approval`
- **运行态观测**：`status` 快照、`patrol` 健康检查/补偿（tmux 仓巡查 + daemon 仓补偿）、`stats` token 统计（日期范围、日聚合、rtk 对比）
- **日报归档**：`journal` 命令生成指定日期日报
- **渐进式 kb 索引**：`kb/` 目录可增量索引，支持长期知识检索
- **安装回归测试**：完整的安装流程回归测试套件
- **环境诊断增强**：`doctor` 细化调度器降级路径诊断

## 工作拓扑

```text
GitHub Issue
    |
    v
ccclaw ingest/run
    |
    +--> admin gate / approve gate
    +--> target repo route
    +--> Claude Code executor
    +--> docs + reports + kb memory
    |
systemd --user timers
```

安装后的默认目录结构：

```text
~/.ccclaw
├── bin/
│   ├── ccclaw
│   └── ccclaude
├── .env
├── log/
├── var/
└── ops/
    ├── config/config.toml
    ├── systemd/
    └── scripts/

/opt/ccclaw
├── kb/
│   ├── designs/
│   ├── assay/
│   ├── journal/
│   └── skills/
│       ├── L1/
│       └── L2/
└── docs/
    ├── reports/
    ├── plans/
    └── rfcs/
```

## 三类仓库的职责分工

`ccclaw` 运行时会同时涉及三类仓库。它们可以有关联，但职责不同，不应混为一谈。

### 控制仓库是什么

控制仓库对应 `config.toml` 中的 `github.control_repo`。

它的主要作用不是承载业务代码，而是作为 `ccclaw` 的默认控制平面与自维护入口：

- 控制仓库中的 Issue 天然可以作为任务事实源
- Comment 是审批、追问、补充上下文和回执的载体
- 当 Issue 位于控制仓库时，权限检查与 `/ccclaw ...` 门禁命令就在这里完成
- 当 Issue 位于某个已绑定任务仓库时，权限检查、标签判定与回帖都在该任务仓库内完成
- 执行结果、阻塞原因、审计痕迹会回写到对应的 Issue 所在仓库

可以把控制仓库理解为：

- **任务入口**
- **审计入口**
- **协作入口**
- **门禁入口**

控制仓库最适合具备以下特征：

- 管理员权限边界清晰
- 长期稳定存在
- 适合集中追踪自动化任务
- 不一定等于实际被修改的代码仓库

单仓库场景下，控制仓库也可以与任务仓库是同一个仓库；多项目协作时，控制仓库与某个任务仓库也允许重叠，但是否执行仍以 `ccclaw` 标签和审批门禁为准。

### Issue 观测边界怎么判定

`ccclaw` 在仓库语义上还要区分 4 个紧挨着、但不能混用的概念：

- `Issue 来源仓库` / `issue_repo`：`ingest` 每轮只观察 `github.control_repo` 与所有已启用 `[[targets]].repo`；这组仓库就是当前 Issue 观测边界
- `Issue 仓库`：具体某条 Issue 实际所在的仓库；审批、标签、回帖、`/ccclaw [DONE]` 与状态页里的 `ISSUE` 都以它为准
- `target_repo`：代码落点路由字段；解析顺序固定为 `target_repo:` 显式声明 -> Issue 仓库本身就是启用 target -> `default_target`
- `control_repo`：官方控制面默认入口；它定义默认入口与自维护落点，但不会替代实际 `issue_repo` 去回帖

调度链路里的口径也按这个边界拆开：

- `ingest` 只负责观察当前观测边界内的 Issue
- `dispatch / slot / sync_target / journal` 只看 `target_repo`
- `reporter / approval / status / done marker` 只看 `issue_repo`

如果某条 Issue 来自观测边界之外的仓库，即使还能解析出某个 `target_repo`，当前实现也会先把它标成 `BLOCKED`，而不是默认为正常入口。

### 知识仓库是什么

知识仓库对应 `paths.home_repo`，默认是 `/opt/ccclaw`。

它不是程序安装目录，也不是业务代码仓库。它的职责是保存 `ccclaw` 的长期记忆和工程沉淀：

- `kb/` 中的长期知识、设计、日报、skills
- `docs/` 中的计划、报告、RFC
- 随机器持续演化、但不应被程序升级覆盖的内容

之所以把知识仓库单独拆出来，是为了解决两个长期问题：

1. **程序升级不应覆盖记忆**
2. **跨任务、跨仓库的知识不应散落在每个业务仓库里**

也就是说：

- `~/.ccclaw` 是程序树
- `/opt/ccclaw` 是知识树

这两个目录故意分离，是 `ccclaw` 当前设计的关键边界。

### 任务仓库是什么

任务仓库对应 `config.toml` 中的 `[[targets]]`。

它们才是 `ccclaw` 真正要进入、检查、修改、提交成果的工作仓库。一个 `target` 至少包含：

- `repo`
- `local_path`
- 可选 `kb_path`
- 可选 `disabled`

任务仓库的职责包括：

- 承载实际代码、文档或配置修改
- 作为运行时路由的目标
- 让同一个控制仓库下的多个项目可以共用一套编排入口

简化理解：

- 控制仓库负责“默认接任务与自维护”
- 知识仓库负责“存记忆”
- 任务仓库负责“干活”

## 知识仓库模式说明

安装阶段知识仓库支持 `init | remote | local` 三种模式。

### `init`

含义：

- 在目标目录初始化一个新的 git 仓库
- 用 release 内置的 `dist/kb/` 初始树进行种子化

适合场景：

- 第一次安装 `ccclaw`
- 还没有现成的知识仓库
- 希望先在当前机器快速落一个本地封闭仓库

这是默认、也是最稳妥的起步方式。

### `remote`

含义：

- 从远程仓库 clone 到本地 `home_repo`
- 再补齐 `kb/` 与必要初始化内容

适合场景：

- 你已经有一个专门保存 `ccclaw` 记忆的私有仓库
- 需要在多台机器之间共享同一套本体记忆
- 希望知识仓库天然具备异地备份和远程同步能力

通常更推荐远程仓库使用私有仓库，而不是公开暴露长期记忆与内部报告。

### `local`

含义：

- 接管一个本机已存在的 git 仓库
- 不重新 clone，只在当前路径补齐 `ccclaw` 需要的目录与契约文件

适合场景：

- 你已经手工 clone 了知识仓库
- 你有自定义目录布局，不想让安装脚本重新决定路径
- 你正在迁移旧环境，希望平滑接入现有仓库

### 如何选择

- **第一次安装，先求稳**：选 `init`
- **已经有专门的远程记忆仓库**：选 `remote`
- **本地已有仓库且你明确知道路径与内容**：选 `local`

## 任务仓库模式说明

安装阶段任务仓库支持 `none | remote | local`。

### `none`

含义：

- 本轮安装只完成程序树和知识仓库，不立即绑定任何工作仓库

适合场景：

- 你想先把 `ccclaw` 装起来，再慢慢添加 target
- 你还没决定第一批要接入哪些业务仓库
- 你希望安装与项目接管分两步完成

### `remote`

含义：

- 从远程仓库 clone 到本地路径
- 然后自动写入 `config.toml` 的 `[[targets]]`

适合场景：

- 当前机器上还没有这个任务仓库
- 你希望安装时顺手把首个工作仓库接进来
- 远程仓库会默认 clone 到 `/opt/src/3claw/owner/repo`
- 若显式指定 `--task-repo-path`，也必须位于 `/opt/src/3claw/` 入口下

### `local`

含义：

- 直接接管本机已有的业务仓库路径
- 不重新 clone，只写入 `targets`

适合场景：

- 仓库已经在本机存在
- 你有自定义 workspace / monorepo 布局
- 你不希望安装脚本改动已有 clone 位置

### 如何选择

- **先把系统装好再说**：选 `none`
- **首个业务仓库还没 clone 到本机**：选 `remote`
- **业务仓库已经在本机**：选 `local`

## 多任务仓库如何配置

`ccclaw` 当前就是通过 `[[targets]]` 支持多个任务仓库的。

### 路由规则

运行时路由顺序是固定的：

1. Issue body 中若显式写了 `target_repo: owner/repo`，优先使用它
2. 否则，若该 Issue 所在仓库本身就是一个已启用 target，就直接路由到该仓库
3. 否则，若配置了 `default_target`，就走默认 target
4. 若既没有 `target_repo:`，Issue 所在仓库也不是 target，且没有 `default_target`：
   - 没有任何 enabled target：任务阻塞
   - 有多个 enabled target 但没有默认值：任务同样阻塞，不会猜测

这意味着多仓库场景下，最稳的做法是：

- 明确设置一个 `default_target`
- 对需要跨仓库执行的 Issue，在正文里显式写 `target_repo: owner/repo`

### 用命令追加多个任务仓库

安装完成后，可以继续添加多个 target：

```bash
ccclaw target add --repo owner/repo-a --path /work/repo-a --default
ccclaw target add --repo owner/repo-b --path /work/repo-b
ccclaw target add --repo owner/repo-c --path /work/repo-c --kb-path /work/shared-kb
ccclaw target list
```

禁用某个 target：

```bash
ccclaw target disable --repo owner/repo-b
```

当前还没有 `target remove`，因此“停用但保留记录”是当前可用方式。

### `config.toml` 示例

```toml
default_target = "owner/repo-a"

[github]
control_repo = "owner/ccclaw-control"

[paths]
app_dir = "~/.ccclaw"
home_repo = "/opt/ccclaw"
var_dir = "~/.ccclaw/var"
log_dir = "~/.ccclaw/log"
kb_dir = "/opt/ccclaw/kb"
env_file = "~/.ccclaw/.env"

[[targets]]
repo = "owner/repo-a"
local_path = "/opt/src/3claw/owner/repo-a"
kb_path = "/opt/ccclaw/kb"

[[targets]]
repo = "owner/repo-b"
local_path = "/opt/src/3claw/owner/repo-b"

[[targets]]
repo = "owner/repo-c"
local_path = "/srv/work/repo-c"
kb_path = "/srv/work/shared-kb"
disabled = true
```

说明：

- `default_target` 只能指向一个存在且未禁用的 target
- `repo` 不能重复
- `local_path` 必填
- `kb_path` 为空时会继承全局 `paths.kb_dir`
- `disabled = true` 的 target 不参与路由

### 旧审批配置迁移

如果你本机的旧 `config.toml` 仍然保留：

```toml
[approval]
command = "/ccclaw approve"
minimum_permission = "admin"
```

升级后会被显式拒绝加载。可直接执行：

```bash
ccclaw --config ~/.ccclaw/ops/config/config.toml config migrate-approval
```

迁移命令会把旧字段改写为 `approval.words` / `approval.reject_words`，并保留你原有的 `minimum_permission`。

### Issue 中显式指定目标仓库

在多任务仓库场景，建议在 Issue 正文中显式加上：

```text
target_repo: owner/repo-b
```

这样可以把任务准确路由到指定仓库，而不是依赖默认值。

## 为什么是 CCClaw

同类 agent/automation 项目通常会优先强调多模型、前端面板、聊天协作或云端控制面；`ccclaw` 当前阶段反过来，先把开源协作里最难稳定的几个基线收口：

1. **任务事实源固定为 GitHub Issue**
2. **安装拓扑固定，不靠调用方自由拼环境变量**
3. **敏感配置与普通配置分轨**
4. **长期记忆与程序升级解耦**
5. **开源仓库默认先门禁，再执行**

这使它非常适合“低噪声、可审计、长期运行”的工程环境。

## 安装前准备

在下载 release 之前，建议先准备好以下依赖与账号条件。

### 账户与凭据

- 一个可正常使用的 GitHub 帐号
- 一个具备目标仓库访问权限的 GitHub token，供 `gh` CLI 与运行时 `.env` 中的 `GH_TOKEN` 使用
- 二选一的 Claude 接入方式：
  - 有可用的 Claude Code 订阅帐号，并能完成对应登录/授权
  - 或者有可供 Claude Code 代理调用的 API `URL + token`

### 网络与地区条件

- 推荐直接位于 Claude Code 官方允许使用的国家/地区
- 若不在允许范围内，请自行确认你的网络出口、账号使用方式和服务条款是否合规
- 本项目文档只说明官方安装与代理配置入口，不提供绕过区域校验的方法

### 主机条件

- Linux 主机
- 建议存在 `systemd --user`；若不可用，安装器不再自动托管 `cron`，而是进入 `none + 手工 cron 指引` 提示态
- 建议当前用户具备 `sudo` 能力，以便安装基础依赖与写入 `/opt/ccclaw`
- 推荐预装或允许安装：`git`、`gh`、`curl`、`wget`、`rg`、`sqlite3`、`golang`

### 建议预先完成的动作

```bash
gh auth login
gh auth status
```

如果你计划使用代理模式，而不是直接走 Claude Code 官方登录，请预先确认至少准备好：

- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_AUTH_TOKEN`

## 下载与安装

### 1. 下载最新 release

可直接从 Releases 页面下载最新安装包与校验文件：

- <https://github.com/41490/ccclaw/releases>

如果已经装好 `gh`，也可以在目标主机执行：

```bash
mkdir -p /tmp/ccclaw-release
cd /tmp/ccclaw-release
gh release download --repo 41490/ccclaw --pattern 'ccclaw_*_linux_amd64.tar.gz' --pattern 'SHA256SUMS'
```

### 2. 校验安装包

```bash
sha256sum -c SHA256SUMS
```

### 3. 解压并执行安装脚本

```bash
tar -xzf ccclaw_*_linux_amd64.tar.gz
cd ccclaw_*_linux_amd64
bash install.sh
```

默认安装会引导你交互式确认以下信息：

- 程序目录，默认 `~/.ccclaw`
- 控制仓库，默认 `41490/ccclaw`
- 知识仓库模式：`init | remote | local`
- 知识仓库目录，默认 `/opt/ccclaw`
- 任务仓库模式：`none | remote | local`
- 调度器模式：`auto | systemd | cron | none`
- `GH_TOKEN`，若本机已 `gh auth login`，会优先直接复用 `gh auth token`
- 可选的 Claude 代理配置

### 4. 安装脚本会做什么

`install.sh` 当前会按真实实现执行以下动作：

- 探查 `claude`、`gh`、`rg`、`sqlite3`、`rtk`、`git`、`node`、`npm`、`uv`
- 安装前体检 `gh auth`、`systemd --user`、`crontab`、手工 cron 可行性与 remote clone 入口
- 探查 Claude 官方安装通道可达性
- 在需要时安装基础系统依赖
- 安装 `rtk`，并生成 `ccclaude` 包装器
- 初始化或接管知识仓库
- 优先复用已有 `.env`，并在缺失时回填 `gh auth token`
- 生成带中文注释的 `config.toml`
- 安装 `~/.ccclaw/bin/ccclaw`
- 安装 `install.sh` 与 `upgrade.sh`
- 仅在可用时安装 `systemd --user` unit 文件；若不可用则输出手工 `cron`/`none` 指引，不自动写入当前用户 `crontab`
- 按需绑定任务仓库并写入 `[[targets]]`
- 对现有 Claude 登录态 / marketplace / plugins 仅做只读探查与继承，不默认改写
- 输出安装完成摘要、后续命令和日常工作流程

## 推荐安装路径

### 路径 A：交互式首次安装

适合第一次部署、希望看清每一步提示的人：

```bash
bash install.sh
```

### 路径 B：非交互安装，初始化知识仓库

```bash
bash install.sh \
  --yes \
  --home-repo-mode init \
  --task-repo-mode none
```

### 路径 C：非交互安装，并接管远程知识仓库

```bash
bash install.sh \
  --yes \
  --home-repo-mode remote \
  --home-repo /opt/ccclaw \
  --home-repo-remote owner/private-home-repo
```

### 路径 D：非交互安装，并绑定远程任务仓库

```bash
bash install.sh \
  --yes \
  --home-repo-mode init \
  --task-repo-mode remote \
  --task-repo-remote owner/work-repo \
  --task-repo owner/work-repo \
  --task-repo-path /opt/src/3claw/owner/work-repo
```

### 路径 E：非交互安装，并绑定本地任务仓库

```bash
bash install.sh \
  --yes \
  --home-repo-mode init \
  --task-repo-mode local \
  --task-repo-local /abs/path/to/repo \
  --task-repo owner/work-repo
```

## Claude Code 接入准备

`ccclaw` 当前只建议两类 Claude 接入方式：

### 方式 1：Claude Code 官方登录/授权

- 已安装 Claude Code CLI
- 已完成对应登录
- 安装脚本会只读探查当前本机 `claude` 状态

如果本机未安装 `claude`，交互安装时会提示是否执行官方安装脚本；非交互模式下只有显式传入 `--install-claude` 才会自动安装。若本机已完成 Claude 登录，安装器会直接复用当前 CLI 会话，不强制要求填写 `ANTHROPIC_API_KEY`，也不会默认追加 marketplace、plugins 或执行 `rtk init --global`。

### 方式 2：代理 API 模式

在安装或安装后，确保 `.env` 中至少配置：

```bash
ANTHROPIC_BASE_URL=
ANTHROPIC_AUTH_TOKEN=
```

如果你走的是代理模式，README 中所有“Claude 登录”相关步骤都可以替换为“保证代理 URL 与 token 可用”。

## 安装完成后先做什么

```bash
~/.ccclaw/bin/ccclaw doctor \
  --config ~/.ccclaw/ops/config/config.toml \
  --env-file ~/.ccclaw/.env
```

如果需要启用默认后台调度：

```bash
systemctl --user daemon-reload
systemctl --user enable --now ccclaw-ingest.timer ccclaw-patrol.timer ccclaw-journal.timer
```

如果需要专家手工维护受控 `cron`，先阅读：

```bash
~/.ccclaw/croncfg.md
```

然后再按文档中的专家路径执行：

```bash
~/.ccclaw/bin/ccclaw scheduler enable-cron \
  --config ~/.ccclaw/ops/config/config.toml \
  --env-file ~/.ccclaw/.env
```

如果需要单独查看或切换调度后端：

```bash
~/.ccclaw/bin/ccclaw scheduler status \
  --config ~/.ccclaw/ops/config/config.toml \
  --env-file ~/.ccclaw/.env

~/.ccclaw/bin/ccclaw scheduler status --json \
  --config ~/.ccclaw/ops/config/config.toml \
  --env-file ~/.ccclaw/.env

~/.ccclaw/bin/ccclaw scheduler use systemd --config ~/.ccclaw/ops/config/config.toml
~/.ccclaw/bin/ccclaw scheduler use none --config ~/.ccclaw/ops/config/config.toml
```

如需专家手工切到 `cron`，请在确认 `systemd --user` 不可用或确有例外需求后，先阅读 `~/.ccclaw/croncfg.md`，再执行：

```bash
~/.ccclaw/bin/ccclaw scheduler use cron --config ~/.ccclaw/ops/config/config.toml
```

如果需要检查任务仓库绑定结果：

```bash
~/.ccclaw/bin/ccclaw target list --config ~/.ccclaw/ops/config/config.toml
```

## 常用命令

```bash
# 基础
ccclaw                          # 无参数显示帮助
ccclaw -h                       # 显示帮助
ccclaw -V                       # 显示版本
ccclaw version                  # 显示版本（子命令形式）

# 环境诊断
ccclaw doctor                   # 执行环境与部署健康检查

# 配置
ccclaw config                   # 校验并展示当前配置
ccclaw config migrate-approval  # 旧审批配置一键迁移
ccclaw config set-scheduler --mode auto|systemd|cron|none  # 更新调度器配置

# 任务流
ccclaw ingest                   # 拉取并入队 Issue 任务
ccclaw run                      # 兼容入口，内部转发到全局单飞 ingest 调度
ccclaw --log-level debug run    # 临时放大兼容入口本次运行态日志密度

# 运行态观测
ccclaw status                   # 查看当前运行态快照
ccclaw status --json            # 结构化输出运行态快照
ccclaw patrol                   # 健康检查与补偿（tmux 仓巡查 + daemon 仓补偿）
ccclaw stats                    # 查看 token 使用统计
ccclaw stats --daily --from 2026-03-01 --to 2026-03-10
ccclaw stats --rtk-comparison   # rtk 与非 rtk 对比统计

# 日报归档
ccclaw journal                  # 生成当日 journal
ccclaw journal --date 2026-03-10

# 调度器
ccclaw scheduler status         # 查看调度后端状态
ccclaw scheduler status --json  # 结构化输出调度状态
ccclaw scheduler doctor         # 查看调度与日志运维诊断
ccclaw scheduler doctor --json  # 结构化输出诊断摘要与检查项
ccclaw scheduler timers         # 默认窄视图，只保留关键列
ccclaw scheduler timers --wide  # 完整视图，显示 CAL_RAW/CAL_CFG 与双时区时间
ccclaw scheduler timers --raw   # 原始字段视图，便于人工排障
ccclaw scheduler timers --json  # 结构化输出，便于脚本消费
ccclaw scheduler logs run       # 兼容别名，当前映射到 ingest service 日志
ccclaw scheduler logs run --level error  # 仅查看 error/err 级别日志
ccclaw scheduler enable-cron    # 专家手工启用受控 cron
ccclaw scheduler disable-cron   # 禁用受控 cron / 清理历史托管块
ccclaw scheduler use systemd    # 切换到 systemd 调度
ccclaw scheduler use none       # 停用调度
ccclaw scheduler use cron       # 专家手工切换到 cron 调度

# 任务仓库
ccclaw target list              # 列出所有绑定的任务仓库
ccclaw target add --repo owner/repo --path /abs/path/to/repo
ccclaw target disable --repo owner/repo
```

CLI 约定：

- 无参数：输出帮助
- `-h | --help`：输出帮助
- `-V | --version`：输出版本

调度排障建议路径：

1. 先看 `ccclaw scheduler status`，确认请求模式与当前生效模式是否一致
2. 再看 `ccclaw doctor`，确认 `jj/git 同步能力` 是否通过；若失败，先执行 `jj --version`、`git --version`、`git fetch -h | rg porcelain`
3. 再看 `ccclaw scheduler doctor`，确认 linger、user bus、unit 漂移、timer/service 最近状态
4. 最后按需要选择 `ccclaw scheduler timers --wide|--raw|--json`
5. 若 doctor 提示 service 异常，再执行 `ccclaw scheduler logs <scope>` 或 `journalctl --user -u <service>`

脚本化观测建议：

- 根级运行态：`ccclaw status --json`
- 调度后端状态：`ccclaw scheduler status --json`
- 深度诊断：`ccclaw scheduler doctor --json`
- 定时器详情：`ccclaw scheduler timers --json`

## 配置文件

### 敏感配置

固定在：

```text
~/.ccclaw/.env
```

至少应检查：

- `GH_TOKEN`
- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_AUTH_TOKEN`

可按需补充：

- `ANTHROPIC_API_KEY`
- `GREPTILE_API_KEY`

### 普通配置

固定在：

```text
~/.ccclaw/ops/config/config.toml
```

这里会记录：

- 控制仓库
- 知识仓库
- 路径拓扑
- 执行器命令
- 运行态日志级别与调度日志归档策略
- 审批门禁
- `default_target`
- `targets`

运行态日志约定：

- `[scheduler.logs].level` 同时控制 `ingest/patrol/journal` 三类入口写入 `stderr`/journald 的日志阈值，也是 `ccclaw scheduler logs` 的默认查看过滤
- 运行态统一按 `debug|info|warning|error` 四级收口；历史别名 `notice|err|crit|alert|emerg` 继续兼容，内部会归一到四级语义
- `ccclaw scheduler logs --level ...` 是查看过滤；`ccclaw --log-level ... <command>` 是本次命令的临时运行态覆盖，不会改写配置文件
- `[scheduler.logs].retention_days` 与 `max_files` 只作用于带 ccclaw 归档头的受管文件，不会清理归档目录中的人工文件
- `[scheduler.logs].compress = true` 时，最新一次归档保留明文，历史受管 `.log` 会自动压缩为 `.log.gz`

运行产物约定：

- `~/.ccclaw/var/results/<task>.json` 只承载结构化 JSON 结果；成功时写入，失败或协议异常时保持为空
- `~/.ccclaw/var/results/<task>.diag.txt` 单独承载诊断文本；当结构化结果缺失或解析失败时，`patrol` / 回帖链路优先读取这里
- `~/.ccclaw/log/<task>.log` 保留执行期汇总日志，作为诊断文件之后的最后兜底现场

## 日常使用流程

1. 绑定工作任务仓库
2. 在当前观测边界内创建/维护带 `ccclaw` 标签的 Issue
   默认观测边界是控制仓库 + 所有已启用 target 仓库
3. 由 `ccclaw` 巡检并在满足门禁后执行
4. 检验工作成果，并继续在 Issue 中回复交流

开源协作门禁流程：

1. 当前观测边界内，只有带 `ccclaw` 标签的 open Issue 才进入执行判定
2. `maintain` 及以上成员创建的 Issue 自动进入执行判定；外部成员 Issue 默认只巡查与讨论
3. 受信任成员评论 `/ccclaw <批准词>` 后，Issue 才进入执行；最新评论也可用 `/ccclaw <否决词>` 撤回
4. 若 `ccclaw` 标签被移除，任务转为 `BLOCKED`，重新加标签后再恢复判定

## 升级与发布

程序树升级入口：

```bash
~/.ccclaw/upgrade.sh
```

升级约束：

- 升级只刷新程序树与受管契约文件
- 不覆盖 `/opt/ccclaw` 中的用户记忆
- `kb/**/CLAUDE.md` 使用受管区块方式无损合并

开发者发布入口统一在 `src/Makefile`：

```bash
cd src
make fmt
make test
make build
make package
make checksum
make archive
make release
```

release 约束：

- 从 `src/dist/` 打包
- 至少包含安装包与 `SHA256SUMS`
- 本机归档目录固定为 `/ops/logs/ccclaw/`
- `make release` 会按模板生成 release notes，并显式写入控制仓库 / 知识仓库 / 任务仓库术语
- `make release` 仅允许在干净工作树上执行

## 从源码构建

如果你不是使用 release 包，而是直接从源码构建：

```bash
cd src
make build
make package
```

构建后的主二进制位于：

```text
src/dist/bin/ccclaw
```

release 安装树位于：

```text
src/dist/
```

## 项目优势总结

- 对开源维护者友好：直接接 GitHub Issue，而不是要求团队切换协作入口
- 对 Linux 主机友好：优先 `systemd --user`，异常时可降级，不强迫引入额外编排系统
- 对 token 预算友好：把确定性流程收敛到本地程序与固定目录
- 对长期项目友好：记忆树、报告、计划、技能模板都有固定落点
- 对升级友好：程序树和知识仓库分离，降低误覆盖风险

## 已知边界

- 当前默认只覆盖 `linux/amd64`
- 当前执行器仍以 Claude Code 为中心，尚未进入 phase1 的多 provider 扩展
- 当前主协作入口仍是 GitHub Issue，Matrix 等实时协作总线尚未进入默认安装面

## PS:
本质上这是一种 `CC龙`(嘻嘻龙), 适用范围非常小, 专门解决俺的私人场景问题/需求,
只是, 给大家演示一下, 现在嘦正确发愿,
任何, 都可以快速手搓出一个自己的 XXClaw 出来的;
并不建议没有这种严重依赖 gh-issue 来推进各种项目的大家, 立即安装尝试;

等等, 俺逐步根据愿景兼容到其它各种国产模型后, 再说哈;

唯一的优势, 可能就是卫生了:

- 所有代码 < 20000 行
- 安装包 < 10M
- 只配置了 Debain 12 兼容系统的少数服务, 没有安装任何要长期占用内存/CPU 资源的服务
- 所有行为, 都由 Claude Code 执行, 安全边界由 Claude Code 团队负责了

PPS:
> ccclaw 26.03.11.0440
代码分析:
```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 Language              Files        Lines         Code     Comments       Blanks
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 Go                       24         8928         8323            1          604
 Makefile                  1           80           66            0           14
 Shell                     5         2513         2283           38          192
 TOML                      2          100           46           42           12
─────────────────────────────────────────────────────────────────────────────────
```

PPPS:

> 内心话, 整个儿 CCClaw 其实, 只解决了一个真实问题: 通过 SSH(或是其它技术渠道) 
进入远程主机使用 Claude Code 时, 经常因为各种混乱的网络状况, 引发连接中断;
尝试了各种办法后, 突然明白: `网络不可信`, 这是互联网的底层架构共识之一;
必须有办法令 Claude Code 和当前通过 mailling-list(邮件列表) 就可以异步协同的技术社区一样,
可以和我异步进行协作, 就能脱离和没谱的互联网对抗了...


## 许可证

本项目使用 [MIT License](LICENSE)。
