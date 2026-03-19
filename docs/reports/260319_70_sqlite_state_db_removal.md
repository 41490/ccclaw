# 260319_70_sqlite_state_db_removal

## 背景

- 对应 Issue：#70
- 直接决策来源：
  - `https://github.com/41490/ccclaw/issues/70#issuecomment-4088833586`
  - 当前追加要求：追查 `~/.ccclaw/var/state.db` 的实际用途，完成 SQLite 清除，不再使用本地 `.db` 承载记忆或状态

## 决策理解

- 运行态事实源应统一为 `var_dir/state.json + *.jsonl`
- `paths.state_db` 已不再是允许的配置入口
- 本地遗留 `.db` 只允许作为历史残留被清走，不允许继续参与读写
- 安装与文档口径不得再把 `sqlite3` 当成运行前提

## 现场核查

### 1. 仓库现状

- 运行态主链路已经不再导入 SQLite 驱动
- `storage.Open()` 实际走的是 `state.json + JSONL`
- 但仍保留两类残留：
  - 配置/存储层对 `.db` 路径的兼容入口
  - 安装脚本与示例文档中的 `sqlite3` 依赖表述

### 2. 本机现场

- `~/.ccclaw/ops/config/config.toml` 已经使用 `paths.var_dir`
- `~/.ccclaw/var/state.db` 最后修改时间为 `2026-03-12 18:15:18 -0400`
- `~/.ccclaw/var/state.json` 持续更新，最新修改时间为 `2026-03-19`
- 核查时没有进程保持打开 `state.db`

结论：

- 当前主链路并未继续以 SQLite 为事实源
- `state.db` 属于历史残留文件，加上兼容代码与错误文档认知，才造成“似乎仍在使用 SQLite”的混淆

## 本轮改动

### 1. 禁止 `.db` 再作为合法配置与存储入口

- `src/internal/config/config.go`
  - `Load()` 改为直接拒绝 `paths.state_db`，提示执行 `ccclaw config migrate`
  - `Validate()` 新增校验：`paths.var_dir` 不能指向 `.db`
  - `Migrate()` 保留旧配置显式迁移能力
- `src/internal/adapters/storage/store.go`
  - `storage.Open()` 改为拒绝 `.db` 路径输入，不再静默兼容

### 2. 运行时自动收口遗留 SQLite 文件

- 新增 `src/internal/adapters/storage/legacy_cleanup.go`
- 在 `storage.Open()` 中自动扫描：
  - `state.db`
  - `ccclaw.db`
- 若发现遗留文件，自动移动到：
  - `var/archive/legacy-db/*.bak`

这样既把 `.db` 从运行目录移走，也保留了历史取证副本，不再干扰当前事实链路。

### 3. 删除 SQLite 依赖与安装误导

- `src/go.mod`
- `src/go.sum`
- `src/dist/install.sh`
- `src/ops/examples/install-flow.md`
- `src/dist/ops/examples/install-flow.md`

收口后：

- Go 模块不再依赖 `modernc.org/sqlite`
- 安装器不再要求 `sqlite3`
- 示例文档不再把 SQLite 视为基础组件

### 4. 测试同步

- `src/internal/config/config_test.go`
  - 改为验证 legacy `state_db` 会被拒绝，并要求显式迁移
  - 新增 `.db` 形式 `var_dir` 非法校验
- `src/internal/adapters/storage/store_test.go`
  - 新增 `.db` 路径拒绝测试
  - 新增 legacy `.db` 自动归档测试
- `src/cmd/ccclaw/main_test.go`
  - 保留通过 CLI `config migrate` 迁移 legacy `state_db` 的回归用例

## 验证

### 1. 自动化测试

执行：

```bash
cd /opt/src/ccclaw/src && go test ./...
```

结果：通过。

### 2. 本机现场验证

执行：

```bash
cd /opt/src/ccclaw/src && \
go run ./cmd/ccclaw --config /home/zoomq/.ccclaw/ops/config/config.toml --env-file /home/zoomq/.ccclaw/.env status
```

结果：

- `~/.ccclaw/var/` 下的 `state.db` 与 `ccclaw.db` 已被移出
- 新归档路径为：
  - `~/.ccclaw/var/archive/legacy-db/state_sqlite_20260319T093721Z.bak`
  - `~/.ccclaw/var/archive/legacy-db/ccclaw_sqlite_20260319T093721Z.bak`

说明当前源码已能在首次打开运行态存储时自动完成遗留 `.db` 收口。

## 结论

Issue #70 本轮完成了 4 件事：

1. 彻底切断 `.db` 作为配置入口与存储入口的兼容链路
2. 将本地遗留 `state.db/ccclaw.db` 自动迁出运行目录
3. 移除 Go 侧 SQLite 依赖与安装脚本中的 `sqlite3` 前提
4. 用自动化测试与本机现场验证确认当前事实源只剩 `state.json + JSONL`

## 后续建议

1. 在 `doctor` 中追加 legacy `.db` 检查项，明确提示“已归档/仍残留”
2. 升级脚本可进一步显式输出 legacy `.db` 清理结果，减少现场排障成本
3. 后续报告与文档中仍出现 `state.db` 叙述的地方，建议逐步改写为 `state.json + JSONL`
