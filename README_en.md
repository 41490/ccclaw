# CCClaw

```text
  o-o   o-o   o-o o
 /     /     /    |
O     O     O     |  oo o   o   o
 \     \     \    | | |  \ / \ /
  o-o   o-o   o-o o o-o-  o   o
```

> A long-running Claude Code orchestration agent focused on reducing token waste

Chinese is the default documentation. See [README.md](README.md) for the primary version.

`CCClaw` can also be shortened to `3Claw`. The name is intentionally open-ended:

- `Claude | Common | Chaos | ...`
- `Code | Continuance | Calm | ...`
- `Claw`

## Overview

`ccclaw` is a Linux-native orchestration tool built around:

- **GitHub Issues** as the task source of truth
- **Claude Code** as the execution engine
- **systemd --user** as the preferred background scheduler
- A dedicated **home knowledge repository** as long-term memory

The current repository state is aligned with the `phase0.5` baseline: `daemon` is the default executor, `systemd --user` is the only auto-managed scheduler path, and `cron` has been reduced to an expert manual fallback.

The point of `ccclaw` is not to make agents look flashy. It is to move repetitive operational work out of expensive model time:

- polling and scheduling
- approval gates
- fixed runtime layout
- long-term memory persistence
- multi-repo routing
- open-source collaboration control

## Key Strengths

- **Token-aware by design**: deterministic work stays in local CLI logic and `systemd` guidance; `cron` remains available only as an expert manual fallback, and Claude Code is used when real execution is needed.
- **Issue-driven workflow**: it fits the way open-source teams already work.
- **Linux-first**: no Docker, Kubernetes, or hosted control plane required for the default setup.
- **Program tree and knowledge tree are separated**: upgrades do not overwrite user memory under `/opt/ccclaw`.
- **Strict config boundaries**: secrets live in `.env`, normal config lives in `.toml`, and runtime does not depend on pre-exported shell variables.
- **Open-source safe gate**: issues from `maintain` or above can execute automatically; other issues require a trusted `/ccclaw <approval-word>` comment, and the latest trusted reject comment can revoke execution.
- **Release-ready packaging**: releases are built from `src/dist/` and include at least the install package plus `SHA256SUMS`.

## Best Fit

- Solo maintainers running Claude Code across multiple GitHub repositories
- Small teams that want background automation without abandoning Issues as the audit trail
- Long-running engineering workflows that need persistent memory, reports, and operational history
- Teams trying to reduce token burn from repetitive orchestration overhead

## Current Status

- Stage: `phase0.5`
- Release status: install, upgrade, `stream-json`, scheduler observability, and sevolver maintenance are already wired into the default path
- Default platform: `linux/amd64`
- Default app dir: `~/.ccclaw`
- Default knowledge repo: `/opt/ccclaw`
- Knowledge repo modes: `init | remote | local`
- Task repo modes: `none | remote | local`
- Default executor mode: `daemon` (can be switched back globally or per-target to `tmux`)
- Default scheduler: `auto`, preferring `systemd --user`; if unavailable, the installer falls back to `none + manual cron guidance`
- Version format: `yy.mm.dd.HHMM`

## Topology

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

Default installed layout:

```text
~/.ccclaw
├── bin/
│   ├── ccclaw
│   └── ccclaude
├── croncfg.md
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

## Repository Roles

`ccclaw` works with three different repository roles at runtime. They may be related, but they serve different purposes.

### What is the control repository

The control repository is `github.control_repo` in `config.toml`.

Its main job is not to hold the working code. It acts as the default control plane and self-maintenance entry for `ccclaw`:

- Issues in the control repository can serve directly as task sources of truth
- comments carry approvals, follow-ups, extra context, and execution feedback
- when an Issue lives in the control repository, permission checks and `/ccclaw ...` gate commands are evaluated there
- when an Issue lives in a bound task repository, permission checks, label checks, and report comments happen in that task repository
- execution results, blocked reasons, and audit traces flow back to the repository that hosts the Issue

You can think of the control repository as:

- **task entrypoint**
- **audit entrypoint**
- **collaboration entrypoint**
- **permission gate entrypoint**

Best fit for a control repository:

- clear admin boundary
- long-lived and stable
- suitable for centralized automation tracking
- not necessarily the same repository that gets modified

In a single-repo setup, the control repository can be the same as a task repository. In multi-project setups, the control repository may still overlap with one task repository, but execution still depends on the `ccclaw` label plus the approval gate.

### How Issue observation boundaries work

`ccclaw` still needs four closely related but non-interchangeable repository concepts:

- `Issue source repository` / `issue_repo`: each ingest round only observes `github.control_repo` plus every enabled `[[targets]].repo`; together they form the current Issue observation boundary
- `Issue repository`: the repository that actually hosts a given Issue; approvals, labels, report comments, `/ccclaw [DONE]`, and the `ISSUE` column in status output all follow this repository
- `target_repo`: the code landing route; resolution order stays `target_repo:` in the Issue body -> the Issue repository itself when it is an enabled target -> `default_target`
- `control_repo`: the default official control-plane entry; it defines the default entrypoint and self-maintenance landing zone, but it does not replace the real `issue_repo` for reporting

The runtime chain follows the same split:

- `ingest` only observes Issues inside the current observation boundary
- `dispatch / slot / sync_target / journal` only use `target_repo`
- `reporter / approval / status / done marker` only use `issue_repo`

If an Issue comes from a repository outside the observation boundary, the current implementation blocks it first even if a valid `target_repo` can still be resolved.

### What is the knowledge repository

The knowledge repository is `paths.home_repo`, defaulting to `/opt/ccclaw`.

It is neither the program install tree nor a business code repository. Its job is to keep long-term `ccclaw` memory and project artifacts:

- long-term knowledge, designs, journals, and skills in `kb/`
- plans, reports, and RFCs in `docs/`
- machine-local evolving knowledge that upgrades must not overwrite

This separation exists to solve two long-term problems:

1. **program upgrades must not overwrite memory**
2. **cross-task and cross-repo knowledge should not be scattered into every business repository**

In short:

- `~/.ccclaw` is the program tree
- `/opt/ccclaw` is the knowledge tree

That separation is a core boundary in the current design.

### What is a task repository

Task repositories are the `[[targets]]` entries in `config.toml`.

These are the actual working repositories that `ccclaw` enters, inspects, modifies, and reports against. A target contains at least:

- `repo`
- `local_path`
- optional `kb_path`
- optional `disabled`

Task repositories are responsible for:

- holding the real code, docs, or config to be changed
- serving as the runtime routing destination
- allowing one control repository to orchestrate multiple projects

A simple mental model:

- the control repository **handles default ingress and self-maintenance**
- the knowledge repository **stores memory**
- task repositories **do the work**

## Knowledge Repository Modes

The installer supports `init | remote | local` for the knowledge repository.

### `init`

Meaning:

- initialize a new git repository at the target path
- seed it with the built-in `dist/kb/` initial structure

Use it when:

- this is your first `ccclaw` install
- you do not already have a knowledge repository
- you want the fastest local-only bootstrap

This is the default and safest starting point.

### `remote`

Meaning:

- clone a remote repository into the local `home_repo`
- then backfill required `kb/` and bootstrap content

Use it when:

- you already have a dedicated remote knowledge repository
- you want to share the same long-term memory across machines
- you want backup and sync through git from day one

In practice, a private remote repository is usually the right choice here.

### `local`

Meaning:

- attach to an already existing local git repository
- do not clone again; just add the required `ccclaw` directories and contract files

Use it when:

- you already cloned the knowledge repository manually
- you have a custom directory layout
- you are migrating from an older setup and want a controlled takeover

### How to choose

- **first install, lowest risk**: `init`
- **already have a dedicated remote knowledge repo**: `remote`
- **already have the repo locally and know exactly what you are doing**: `local`

## Task Repository Modes

The installer supports `none | remote | local` for task repositories.

### `none`

Meaning:

- install the program tree and knowledge repository now, but do not bind any work repository yet

Use it when:

- you want to bring up `ccclaw` first and add targets later
- you have not decided which repositories to manage yet
- you prefer installation and repo onboarding as separate steps

### `remote`

Meaning:

- clone the remote repository into a local path
- automatically write it into `[[targets]]`

Use it when:

- the task repository is not yet cloned on this machine
- you want the installer to onboard the first work repository
- the default clone path `/opt/src/3claw/owner/repo` works for you
- if you pass `--task-repo-path`, it still needs to stay under `/opt/src/3claw/`

### `local`

Meaning:

- attach to an already existing local work repository
- do not clone again; only write the target config

Use it when:

- the repository already exists on this machine
- you have a custom workspace or monorepo layout
- you do not want the installer to move or recreate local clones

### How to choose

- **install first, bind later**: `none`
- **first work repo is not on this machine yet**: `remote`
- **the work repo already exists locally**: `local`

## Multiple Task Repositories

`ccclaw` supports multiple task repositories through `[[targets]]`.

### Routing rules

Runtime routing follows a fixed order:

1. if the Issue body contains `target_repo: owner/repo`, that wins
2. otherwise, if the Issue repository itself is an enabled target, route to that repository
3. otherwise, if `default_target` is configured, it is used
4. otherwise, if none of the above is present:
   - no enabled targets: the task is blocked
   - enabled targets exist but no default is set: the task is also blocked; `ccclaw` does not guess

For multi-repo setups, the safe practice is:

- set one explicit `default_target`
- add `target_repo: owner/repo` in Issues that must run against a non-default repository

### Add multiple targets by command

After installation, keep adding targets as needed:

```bash
ccclaw target add --repo owner/repo-a --path /work/repo-a --default
ccclaw target add --repo owner/repo-b --path /work/repo-b
ccclaw target add --repo owner/repo-c --path /work/repo-c --kb-path /work/shared-kb
ccclaw target list
```

Disable a target:

```bash
ccclaw target disable --repo owner/repo-b
```

There is no `target remove` yet, so "disable but keep the record" is the current workflow.

### `config.toml` example

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

Notes:

- `default_target` must point to an existing enabled target
- `repo` values must be unique
- `local_path` is required
- if `kb_path` is empty, it inherits the global `paths.kb_dir`
- `disabled = true` targets do not participate in routing

### Migrating Legacy Approval Config

If your older `config.toml` still contains:

```toml
[approval]
command = "/ccclaw approve"
minimum_permission = "admin"
```

new builds will reject it explicitly. Run:

```bash
ccclaw --config ~/.ccclaw/ops/config/config.toml config migrate-approval
```

The migration rewrites the section to `approval.words` / `approval.reject_words` and keeps your existing `minimum_permission`.

### Explicit target selection in an Issue

In multi-repo setups, put this in the Issue body when needed:

```text
target_repo: owner/repo-b
```

That routes the task to the intended repository instead of relying on the default.

## Why CCClaw

Many agent projects lead with multi-model routing, dashboards, chat UX, or cloud infrastructure. `ccclaw` deliberately starts with the harder operational basics:

1. GitHub Issues remain the task truth source
2. installation layout is fixed and predictable
3. secrets and normal config are separated
4. memory survives upgrades
5. open-source execution is gated by permission

That makes it a better fit for low-noise, auditable, long-running engineering work.

## Prerequisites

Prepare these before downloading a release.

### Accounts and Credentials

- A working GitHub account
- A GitHub token with access to the target repositories; it will be used by `gh` and later stored as `GH_TOKEN` in `.env`
- One of the following Claude access paths:
  - a valid Claude Code subscription/account and a usable login/auth path
  - or an API proxy URL + token that can be used as the Claude backend

### Network and Region

- Best case: you are in a country/region officially supported by Claude Code
- If not, you need to verify your own network path, account setup, and compliance constraints yourself
- This README documents official install and proxy configuration paths only; it does not explain bypassing region checks

### Host Requirements

- Linux
- `systemd --user` recommended; if unavailable, the installer now falls back to `none + manual cron guidance` instead of auto-managing `crontab`
- `sudo` recommended, because the installer may need to install packages and write into `/opt/ccclaw`
- Recommended packages: `git`, `gh`, `curl`, `wget`, `rg`, `sqlite3`, `golang`

### Recommended Pre-flight

```bash
gh auth login
gh auth status
```

If you plan to use proxy mode, make sure you already have:

- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_AUTH_TOKEN`

## Download and Install

### 1. Download the latest release

Release page:

- <https://github.com/41490/ccclaw/releases>

With `gh`:

```bash
mkdir -p /tmp/ccclaw-release
cd /tmp/ccclaw-release
gh release download --repo 41490/ccclaw --pattern 'ccclaw_*_linux_amd64.tar.gz' --pattern 'SHA256SUMS'
```

### 2. Verify checksums

```bash
sha256sum -c SHA256SUMS
```

### 3. Extract and run the installer

```bash
tar -xzf ccclaw_*_linux_amd64.tar.gz
cd ccclaw_*_linux_amd64
bash install.sh
```

The interactive installer will ask for:

- app dir, default `~/.ccclaw`
- control repo, default `41490/ccclaw`
- knowledge repo mode: `init | remote | local`
- knowledge repo dir, default `/opt/ccclaw`
- task repo mode: `none | remote | local`
- scheduler mode: `auto | systemd | none` in the interactive path; `cron` remains an expert-only manual path
- `GH_TOKEN`; if `gh auth login` is already in place, the installer reuses `gh auth token` first
- optional Claude proxy settings

### 4. What the installer does

`install.sh` currently performs these real actions:

- probes `claude`, `gh`, `rg`, `sqlite3`, `rtk`, `git`, `node`, `npm`, `uv`
- runs a preflight for `gh auth`, `systemd --user`, manual cron viability, and the remote clone root
- checks whether the official Claude install channel is reachable
- installs base system dependencies when needed
- installs `rtk` and creates the `ccclaude` wrapper
- initializes or attaches the knowledge repository
- reuses an existing `.env` when possible and backfills `GH_TOKEN` from `gh auth token` when missing
- creates a Chinese-commented `config.toml`
- installs `~/.ccclaw/bin/ccclaw`
- installs `install.sh` and `upgrade.sh`
- installs `systemd --user` unit files only when user-level systemd is usable; otherwise it continues in `none` mode and prints exact manual cron guidance
- syncs `croncfg.md` into the app dir so the expert manual cron path has a fixed local reference
- binds task repositories and writes `[[targets]]`
- prints a deployment summary and next-step commands

## Recommended Install Paths

### Path A: first interactive install

```bash
bash install.sh
```

### Path B: non-interactive install with a fresh knowledge repo

```bash
bash install.sh \
  --yes \
  --home-repo-mode init \
  --task-repo-mode none
```

### Path C: non-interactive install with a remote knowledge repo

```bash
bash install.sh \
  --yes \
  --home-repo-mode remote \
  --home-repo /opt/ccclaw \
  --home-repo-remote owner/private-home-repo
```

### Path D: non-interactive install with a remote task repo

```bash
bash install.sh \
  --yes \
  --home-repo-mode init \
  --task-repo-mode remote \
  --task-repo-remote owner/work-repo \
  --task-repo owner/work-repo \
  --task-repo-path /opt/src/3claw/owner/work-repo
```

### Expert-only cron note

If you explicitly need `cron`, read `~/.ccclaw/croncfg.md` after install. The installer no longer auto-manages `crontab`, and `--scheduler cron` now fails fast on purpose.

### Path E: non-interactive install with a local task repo

```bash
bash install.sh \
  --yes \
  --home-repo-mode init \
  --task-repo-mode local \
  --task-repo-local /abs/path/to/repo \
  --task-repo owner/work-repo
```

## Claude Code Readiness

`ccclaw` currently assumes one of two Claude access paths.

### Path 1: official Claude Code login/auth

- Claude Code CLI is installed
- login/auth has already been completed
- the installer will probe the current local `claude` state

If `claude` is missing, the interactive installer can offer the official installer. In non-interactive mode, automatic Claude installation only happens when `--install-claude` is explicitly passed. If Claude login is already present, the installer reuses the existing CLI session and does not require `ANTHROPIC_API_KEY`.

### Path 2: proxy API mode

Make sure `.env` contains at least:

```bash
ANTHROPIC_BASE_URL=
ANTHROPIC_AUTH_TOKEN=
```

If you use proxy mode, every "Claude login" step in this README can be treated as "make sure the proxy URL and token are valid".

## First Steps After Install

```bash
~/.ccclaw/bin/ccclaw doctor \
  --config ~/.ccclaw/ops/config/config.toml \
  --env-file ~/.ccclaw/.env
```

Enable background timers if desired:

```bash
systemctl --user daemon-reload
systemctl --user enable --now ccclaw-ingest.timer ccclaw-patrol.timer
```

Check task repo bindings:

```bash
~/.ccclaw/bin/ccclaw target list --config ~/.ccclaw/ops/config/config.toml
```

## Common Commands

```bash
ccclaw
ccclaw -h
ccclaw -V
ccclaw doctor
ccclaw config
ccclaw ingest
ccclaw run                      # compatibility entry; forwards into the per-target ingest cycle
ccclaw status
ccclaw scheduler status
ccclaw scheduler doctor
ccclaw scheduler use systemd
ccclaw scheduler use none
ccclaw scheduler enable-cron    # expert manual path
ccclaw scheduler disable-cron
ccclaw target list
ccclaw target add --repo owner/repo --path /abs/path/to/repo
ccclaw target disable --repo owner/repo
```

CLI behavior:

- no args: help
- `-h | --help`: help
- `-V | --version`: version

## Configuration

### Secrets

Stored at:

```text
~/.ccclaw/.env
```

At minimum, verify:

- `GH_TOKEN`
- `ANTHROPIC_BASE_URL`
- `ANTHROPIC_AUTH_TOKEN`

Optional:

- `ANTHROPIC_API_KEY`
- `GREPTILE_API_KEY`

### Normal config

Stored at:

```text
~/.ccclaw/ops/config/config.toml
```

It contains:

- control repo
- knowledge repo
- path layout
- executor command
- approval gate
- `default_target`
- `targets`

## Daily Workflow

1. bind a work repository
2. create or update a labeled `ccclaw` Issue inside the current observation boundary
   by default, that boundary is the control repository plus all enabled target repositories
3. let `ccclaw` inspect and execute when the gate allows it
4. review results and continue discussion in the Issue

Open-source gate flow:

1. Only open Issues with the `ccclaw` label inside the current observation boundary enter execution evaluation
2. Issues created by members with `maintain` or above are eligible for automatic execution; external Issues are inspected and discussed by default
3. A trusted `/ccclaw <approval-word>` comment moves the Issue into execution, and a later trusted reject word can pull it back
4. If the `ccclaw` label is removed, the task becomes `BLOCKED` until the label is added back

## Upgrade and Release

Program-tree upgrade entrypoint:

```bash
~/.ccclaw/upgrade.sh
```

Upgrade rules:

- upgrades refresh the program tree and managed contract files
- user memory under `/opt/ccclaw` is not overwritten
- `kb/**/CLAUDE.md` files are merged with managed blocks

Developer release flow from `src/Makefile`:

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

Release rules:

- packages are built from `src/dist/`
- at least the install tarball and `SHA256SUMS` must be included
- local archives go to `/ops/logs/ccclaw/`
- `make release` only runs on a clean git worktree

## Build From Source

```bash
cd src
make build
make package
```

Built binary:

```text
src/dist/bin/ccclaw
```

Release tree:

```text
src/dist/
```

## Main Advantages

- natural fit for open-source maintenance because it stays on GitHub Issues
- natural fit for Linux hosts because it prefers `systemd --user`; when user-level systemd is unavailable, it contracts to `none + manual cron guidance` instead of silently taking over `crontab`
- lower token waste because deterministic orchestration is kept local
- durable project memory through fixed knowledge and report locations
- safer upgrades because the program tree and knowledge repo are separated

## Current Boundaries

- default coverage is still `linux/amd64`
- the runtime is still Claude Code-centered; multi-provider support is not phase0 scope
- GitHub Issues remain the main collaboration entrypoint; Matrix-style realtime collaboration is not in the default install path yet

## License

This project is licensed under the [MIT License](LICENSE).
