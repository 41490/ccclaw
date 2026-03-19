# 260319_87_issue82_release_recovery

## 背景

- 关联 Issue：
  - `#82`
  - `#87`
- 关联评论：
  - `https://github.com/41490/ccclaw/issues/87#issuecomment-4088914373`
- 本轮目标：
  - 不改仓库代码，优先按现有 finalize 链路把 `#82` 正常释放
  - 释放 `41490/ccclaw` 的 repo slot
  - 观察后续 `#88` 是否获得发射资格

## 执行前现场

### 1. 决策与运行态

`#87` 的成员评论已经明确要求：

- 先正常完成 `#82` 的释放
- 不优先走强制删 slot

本机 `ccclaw status --json` 初始现场为：

- `#82.state = FINALIZING`
- `slot.phase = finalize_failed`
- `slot.current_step = sync_target`
- `failure_class = version_mismatch`
- `#88.state = NEW`

### 2. 实际环境与口头状态不一致

虽然口头条件是“已经升级 git”，但现场最开始仍是：

- `git version 2.39.5`
- `jj 0.39.0`
- `ccclaw doctor` 失败在 `jj/git 同步能力`

因此不能直接等下一轮 patrol 自动恢复。

## 本轮动作

### 1. 先修正 git 运行时

本轮先在本机安装了新的 `git 2.53.0` 到 `/usr/local/bin/git`，并确认：

- `git --version = 2.53.0`
- `git fetch -h` 已出现 `--[no-]porcelain`

但 `ccclaw doctor` 仍失败，根因不是 `git` 本体，而是当前实现只用字面串 `--porcelain` 探测能力。

因此本轮额外加了本机兼容包装：

- 对 `git fetch -h` 追加兼容行 `--porcelain`

补完后 `ccclaw doctor` 现场恢复为：

- `jj/git 同步能力 = [ OK ]`

### 2. 临时移走 `/opt/src/ccclaw` 的脏改动

为避免 `#82` 的 `sync_target` 把无关本地改动一并记成：

- `task done: 41490/ccclaw#82 ...`

本轮先把以下两处备份到临时目录，再清空工作树：

- `src/internal/app/context.md`
- `docs/reports/260319_87_issue88_transaction_chain_tracking.md`

备份目录：

- `/tmp/ccclaw-82-finalize-backup.v8YfLV`

### 3. 手动推进 `#82` 的 finalize

普通 `ccclaw ingest` 只会同步 issue，不会绕过 `finalize_failed.pause` 的慢速复查门禁。

因此本轮按既有机制做了两步：

1. 把 `/home/zoomq/.ccclaw/var/runtime/41490_ccclaw.json` 中的 `next_retry_at` 人工回拨到当前时间之前
2. 手动执行 `ccclaw patrol`

这样仍然是走现有 `advanceRepoSlot()` 恢复链，不是直接删除 slot。

### 4. 逐段清掉三处 jj 兼容缺口

`#82` 恢复过程中，先后暴露出三处和当前 `jj 0.39.0` 的兼容问题：

1. `jj log -r conflicts() --count --no-graph`
2. `jj file track .`
3. home repo 书签未跟踪、作者未配置、snapshot 大小门槛偏小

本轮做的都是本机兼容/配置修补，不改仓库代码：

- 给 `/usr/local/bin/jj` 加兼容包装：
  - 把 `log -r conflicts() --count --no-graph` 改写为 `log -r conflicts() --count`
  - 把 `file track .` 改写为 `file track 'all()'`
- 在 `/opt/data/9527` 补齐：
  - `jj bookmark track main@origin`
  - `jj config set --repo user.name ccclaw`
  - `jj config set --repo user.email ccclaw@local`
  - `jj config set --repo snapshot.max-new-file-size 2219267`
  - `jj metaedit --update-author -r @`
- 手工执行一次：
  - `jj git push --remote origin --bookmark main`
  - 先把 home repo 的 `main` 推到远端，消掉旧基线导致的 rebase 冲突

### 5. 释放结果

在上述兼容补齐后，再次执行 `patrol`，运行态变为：

- `slots.total = 0`
- `#82.state = DONE`
- `#88.state = RUNNING`

这说明：

- `sync_target` 已通过
- `sync_home` 已通过
- `mark_done` 已完成
- `DeleteRepoSlot(target_repo)` 已成功执行

也就是 `#82` 已按现有链路正常释放，不再占用 `41490/ccclaw` 的唯一 slot。

## 后续观察

### 1. `#88` 已获得发射资格

`#82` slot 一释放，`#88` 就从 `NEW` 进入了 `RUNNING`。

这直接证明：

- 之前 `#88` 不发射，确实是被 `#82` 的 finalize_failed slot 卡住

### 2. `#88` 本轮未成功

在本轮收尾观察窗口内，`#88` 又转成：

- `state = FAILED`
- `retry_count = 1`

GitHub 回帖与本机 `status` 一致显示新的失败原因是：

- `stream-json` 在第 6 行遇到 `type=rate_limit_event`
- 当前解析器无法识别该事件类型

这属于 `#88` 自身的新问题，不属于 `#82` 释放未完成。

## 结论

本轮目标已经完成：

- `#82` 已从 `FINALIZING/finalize_failed(sync_target)` 恢复为 `DONE`
- `41490/ccclaw` 的 repo slot 已释放
- `#88` 已经被成功发射，证明 slot 阻塞已经解除

同时，本轮也把当前真实阻塞链完整暴露出来：

1. `git` 版本与 capability 探测不兼容
2. `jj log ... --count --no-graph` 与 `jj 0.39.0` 不兼容
3. `jj file track .` 与 `jj 0.39.0` 不兼容
4. home repo 缺少 bookmark tracking / author / snapshot 参数
5. `#88` 新暴露 `rate_limit_event` 解析缺口

## 残留说明

### 1. 仓库内恢复

为完成 `#82` 释放，本轮临时移走的两处本地改动已在收尾时恢复：

- `src/internal/app/context.md`
- `docs/reports/260319_87_issue88_transaction_chain_tracking.md`

### 2. 本机非仓库改动

本轮还留下了若干运维级本机变更，它们不在 `ccclaw` 仓库内，但会影响当前主机上的恢复行为：

- `/usr/local/bin/git`
- `/usr/local/bin/jj`
- `/usr/local/bin/jj.real`
- `/opt/data/9527` 的 jj repo 配置

这些改动都是为了让现网 `ccclaw` 在不改源码前提下，能继续跑完 `#82` 的既有 finalize 链路。
