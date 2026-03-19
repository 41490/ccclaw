package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/adapters/storage"
	"github.com/41490/ccclaw/internal/claude"
	"github.com/41490/ccclaw/internal/config"
	"github.com/41490/ccclaw/internal/core"
	"github.com/41490/ccclaw/internal/scheduler"
)

func TestRootCmdVersionFlag(t *testing.T) {
	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"-V"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 -V 失败: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("预期输出版本号，实际为空")
	}
}

func TestRootCmdHelpByDefault(t *testing.T) {
	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行默认帮助失败: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("预期输出帮助信息，实际为: %q", out.String())
	}
}

func TestConfigMigrateApprovalCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[github]
control_repo = "41490/ccclaw"

[paths]
app_dir = "~/.ccclaw"
home_repo = "/opt/ccclaw"
var_dir = "~/.ccclaw/var"
log_dir = "~/.ccclaw/log"
kb_dir = "/opt/ccclaw/kb"
env_file = "~/.ccclaw/.env"

[executor]
command = ["claude"]

[approval]
command = "/ccclaw approve"
minimum_permission = "admin"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "config", "migrate-approval"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 config migrate-approval 失败: %v", err)
	}
	if !strings.Contains(out.String(), "已迁移审批配置") {
		t.Fatalf("unexpected output: %q", out.String())
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `command = "/ccclaw approve"`) {
		t.Fatalf("legacy command should be removed: %q", string(payload))
	}
}

func TestConfigMigrateCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[github]
control_repo = "someone/else"

[paths]
app_dir = "/tmp/ccclaw-app"
home_repo = "/opt/data/9527"
state_db = "/tmp/ccclaw-app/var/state.db"
log_dir = "/tmp/ccclaw-app/log"
kb_dir = "/opt/data/9527/kb"
env_file = "/tmp/ccclaw-app/.env"

[executor]
command = ["claude"]

[scheduler]
mode = "systemd"
systemd_user_dir = "/tmp/systemd-user"

[approval]
command = "/ccclaw approve"
minimum_permission = "admin"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "config", "migrate"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 config migrate 失败: %v", err)
	}
	if !strings.Contains(out.String(), "已迁移配置") {
		t.Fatalf("unexpected output: %q", out.String())
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `control_repo = "41490/ccclaw"`) {
		t.Fatalf("expected official control repo: %q", text)
	}
	if !strings.Contains(text, `[scheduler.logs]`) {
		t.Fatalf("expected scheduler logs section: %q", text)
	}
	if strings.Contains(text, `command = "/ccclaw approve"`) {
		t.Fatalf("legacy approval command should be removed: %q", text)
	}
}

func TestConfigSetSchedulerCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `default_target = ""

[github]
control_repo = "41490/ccclaw"

[paths]
app_dir = "/tmp/ccclaw-app"
home_repo = "/opt/ccclaw"
var_dir = "/tmp/ccclaw-app/var"
log_dir = "/tmp/ccclaw-app/log"
kb_dir = "/opt/ccclaw/kb"
env_file = "/tmp/ccclaw-app/.env"

[executor]
command = ["claude"]

[scheduler]
mode = "none"
systemd_user_dir = "/tmp/systemd-old"

[approval]
words = ["approve"]
reject_words = ["reject"]
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "config", "set-scheduler", "--mode", "cron", "--systemd-user-dir", "/tmp/systemd-new"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 config set-scheduler 失败: %v", err)
	}
	if !strings.Contains(out.String(), "已更新调度配置") {
		t.Fatalf("unexpected output: %q", out.String())
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `mode = "cron"`) {
		t.Fatalf("expected scheduler mode update: %q", text)
	}
	if !strings.Contains(text, `systemd_user_dir = "/tmp/systemd-new"`) {
		t.Fatalf("expected systemd dir update: %q", text)
	}
	if !strings.Contains(text, `calendar_timezone = "Asia/Shanghai"`) {
		t.Fatalf("expected scheduler timezone update: %q", text)
	}
}

func TestSchedulerStatusCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	crontabStore := filepath.Join(dir, "crontab.txt")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "none")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCrontab(t, filepath.Join(fakeBin, "crontab"), crontabStore)
	writeFakeSystemctlProbe(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_CRONTAB_FILE", crontabStore)
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "--env-file", envPath, "scheduler", "status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler status 失败: %v", err)
	}
	if !strings.Contains(out.String(), "request=none effective=none") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestSchedulerStatusJSONCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "none")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCrontab(t, filepath.Join(fakeBin, "crontab"), filepath.Join(dir, "crontab.txt"))
	writeFakeSystemctlProbe(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_CRONTAB_FILE", filepath.Join(dir, "crontab.txt"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "--env-file", envPath, "scheduler", "status", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler status --json 失败: %v", err)
	}
	var payload struct {
		Requested      string `json:"requested"`
		Effective      string `json:"effective"`
		MatchesRequest bool   `json:"matches_request"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("解析 JSON 失败: %v; output=%q", err, out.String())
	}
	if payload.Requested != "none" || payload.Effective != "none" || !payload.MatchesRequest {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestStatusJSONCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "none")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"--config", configPath, "--env-file", envPath, "status", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 status --json 失败: %v", err)
	}
	var payload struct {
		Scheduler struct {
			Requested string `json:"requested"`
			Effective string `json:"effective"`
		} `json:"scheduler"`
		Tasks struct {
			Total int `json:"total"`
		} `json:"tasks"`
		Tokens struct {
			Runs int `json:"runs"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("解析 JSON 失败: %v; output=%q", err, out.String())
	}
	if payload.Scheduler.Requested != "none" || payload.Scheduler.Effective != "none" {
		t.Fatalf("unexpected scheduler payload: %+v", payload.Scheduler)
	}
	if payload.Tasks.Total != 0 || payload.Tokens.Runs != 0 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestSchedulerTimersCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSystemctlTimers(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "timers"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler timers 失败: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "视图: human") ||
		!strings.Contains(text, "配置时区: Asia/Shanghai") ||
		!strings.Contains(text, "TASK") ||
		!strings.Contains(text, "ingest") {
		t.Fatalf("unexpected output: %q", text)
	}
	if strings.Contains(text, "CAL_RAW") || strings.Contains(text, "ccclaw-ingest.timer") {
		t.Fatalf("default view should stay concise: %q", text)
	}
}

func TestSchedulerTimersWideCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSystemctlTimers(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "timers", "--wide"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler timers --wide 失败: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "视图: wide") ||
		!strings.Contains(text, "CAL_RAW") ||
		!strings.Contains(text, "*:0/4") ||
		!strings.Contains(text, "ccclaw-ingest.timer") {
		t.Fatalf("unexpected output: %q", text)
	}
}

func TestSchedulerTimersRawCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSystemctlTimers(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "timers", "--raw"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler timers --raw 失败: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "视图: raw") ||
		!strings.Contains(text, "[ingest]") ||
		!strings.Contains(text, "calendar_raw=*:0/4") ||
		!strings.Contains(text, "timer_unit=ccclaw-ingest.timer") {
		t.Fatalf("unexpected output: %q", text)
	}
}

func TestSchedulerTimersJSONCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSystemctlTimers(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "timers", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler timers --json 失败: %v", err)
	}
	var payload struct {
		View  string `json:"view"`
		Items []struct {
			Task        string `json:"task"`
			TimerUnit   string `json:"timer_unit"`
			CalendarRaw string `json:"calendar_raw"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("解析 JSON 失败: %v; output=%q", err, out.String())
	}
	if payload.View != "json" || len(payload.Items) == 0 || payload.Items[0].Task == "" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Items[0].TimerUnit == "" || payload.Items[0].CalendarRaw == "" {
		t.Fatalf("unexpected payload: %+v", payload.Items[0])
	}
}

func TestSchedulerLogsCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	journalLog := filepath.Join(dir, "journalctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeJournalctl(t, filepath.Join(fakeBin, "journalctl"), journalLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_JOURNALCTL_LOG", journalLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "logs", "run", "--lines", "12", "--level", "warning", "--archive"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler logs 失败: %v", err)
	}
	payload, err := os.ReadFile(journalLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "--user --no-pager -n 12 -p warning -u ccclaw-ingest.service") {
		t.Fatalf("unexpected journalctl args: %q", string(payload))
	}
	archiveDir := filepath.Join(dir, "app", "log", "scheduler")
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("读取归档目录失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 archive file, got %d", len(entries))
	}
	archivePayload, err := os.ReadFile(filepath.Join(archiveDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(archivePayload), "# ccclaw scheduler logs archive") || !strings.Contains(string(archivePayload), "ok") {
		t.Fatalf("unexpected archive payload: %q", string(archivePayload))
	}
	if !strings.Contains(out.String(), "日志已归档:") {
		t.Fatalf("expected archive notice: %q", out.String())
	}
}

func TestSchedulerLogsCommandAcceptsErrorAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	journalLog := filepath.Join(dir, "journalctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeJournalctl(t, filepath.Join(fakeBin, "journalctl"), journalLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_JOURNALCTL_LOG", journalLog)

	cmd := newRootCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "logs", "run", "--level", "error"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler logs --level error 失败: %v", err)
	}
	payload, err := os.ReadFile(journalLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "-p err") {
		t.Fatalf("expected error alias to map to err: %q", string(payload))
	}
}

func TestArchiveCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	duckdbLog := filepath.Join(dir, "duckdb.log")
	appDir := filepath.Join(dir, "app")
	varDir := filepath.Join(appDir, "var")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "none")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeDuckDB(t, filepath.Join(fakeBin, "duckdb"), duckdbLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_DUCKDB_LOG", duckdbLog)

	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -10).UTC()
	if err := store.AppendEventAt("task-archive", core.EventCreated, "{}", oldTime); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     "task-archive",
		SessionID:  "sess-archive",
		Usage:      core.TokenUsage{InputTokens: 10, OutputTokens: 2},
		CostUSD:    0.1,
		RecordedAt: oldTime,
	}); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "--env-file", envPath, "archive"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 archive 失败: %v", err)
	}
	if !strings.Contains(out.String(), "archive complete") {
		t.Fatalf("unexpected archive output: %q", out.String())
	}
	archiveDir := filepath.Join(appDir, "var", "archive")
	for _, name := range []string{"events", "token"} {
		matches, err := filepath.Glob(filepath.Join(archiveDir, name+"-*.parquet"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("expected one parquet for %s, got %d", name, len(matches))
		}
	}
}

func TestRunCommandLogLevelOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "none")), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--config", configPath, "--env-file", envPath, "--log-level", "info", "run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 run --log-level info 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "暂无可发射任务") {
		t.Fatalf("expected run stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "兼容入口已转发到全局单飞 ingest 调度") {
		t.Fatalf("expected runtime info log on stderr: %q", stderr.String())
	}

	cmd = newRootCmd()
	stdout = new(bytes.Buffer)
	stderr = new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--config", configPath, "--env-file", envPath, "--log-level", "warning", "run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 run --log-level warning 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "暂无可发射任务") {
		t.Fatalf("expected run stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "兼容入口已转发到全局单飞 ingest 调度") {
		t.Fatalf("warning level should suppress runtime info log: %q", stderr.String())
	}
}

func TestSchedulerDoctorCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if err := writeManagedUnitFixture(cfg); err != nil {
		t.Fatalf("写入 managed unit fixture 失败: %v", err)
	}
	writeFakeSystemctlDoctor(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	writeFakeLoginctl(t, filepath.Join(fakeBin, "loginctl"), filepath.Join(dir, "loginctl.log"))
	writeFakeJournalctl(t, filepath.Join(fakeBin, "journalctl"), filepath.Join(dir, "journalctl.log"))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)
	t.Setenv("CCCLAW_FAKE_LOGINCTL_LOG", filepath.Join(dir, "loginctl.log"))
	t.Setenv("CCCLAW_FAKE_JOURNALCTL_LOG", filepath.Join(dir, "journalctl.log"))

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "doctor"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler doctor 失败: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "[ OK ] 调度配置:") ||
		!strings.Contains(text, "[ OK ] linger:") ||
		!strings.Contains(text, "[ OK ] unit 漂移:") ||
		!strings.Contains(text, "[ OK ] 遗留 run 单元:") ||
		!strings.Contains(text, "[ OK ] 日志归档策略:") ||
		!strings.Contains(text, "[ OK ] user bus:") ||
		!strings.Contains(text, "[ OK ] 托管 timers:") ||
		!strings.Contains(text, "[ OK ] 托管 services:") {
		t.Fatalf("unexpected output: %q", text)
	}
}

func TestSchedulerDoctorJSONCommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	fakeBin := filepath.Join(dir, "bin")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigToml(dir, envPath, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if err := writeManagedUnitFixture(cfg); err != nil {
		t.Fatalf("写入 managed unit fixture 失败: %v", err)
	}
	writeFakeSystemctlDoctor(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	writeFakeLoginctl(t, filepath.Join(fakeBin, "loginctl"), filepath.Join(dir, "loginctl.log"))
	writeFakeJournalctl(t, filepath.Join(fakeBin, "journalctl"), filepath.Join(dir, "journalctl.log"))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)
	t.Setenv("CCCLAW_FAKE_LOGINCTL_LOG", filepath.Join(dir, "loginctl.log"))
	t.Setenv("CCCLAW_FAKE_JOURNALCTL_LOG", filepath.Join(dir, "journalctl.log"))

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "doctor", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler doctor --json 失败: %v", err)
	}
	var payload struct {
		Summary struct {
			Total  int `json:"total"`
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		} `json:"summary"`
		Checks []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("解析 JSON 失败: %v; output=%q", err, out.String())
	}
	if payload.Summary.Total == 0 || payload.Summary.Failed != 0 || payload.Summary.Passed != payload.Summary.Total {
		t.Fatalf("unexpected summary: %+v", payload.Summary)
	}
	foundLinger := false
	for _, check := range payload.Checks {
		if check.Name == "linger" {
			foundLinger = true
			if !check.OK || check.Detail == "" {
				t.Fatalf("unexpected linger payload: %+v", check)
			}
		}
	}
	if !foundLinger {
		t.Fatalf("missing linger check: %+v", payload.Checks)
	}
}

func TestSchedulerUseCronCommand(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(dir, "app")
	systemdDir := filepath.Join(dir, "systemd-user")
	if err := os.MkdirAll(filepath.Join(appDir, "ops", "systemd"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(testConfigTomlWithSystemdDir(appDir, envPath, systemdDir, "systemd")), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(dir, "bin")
	crontabStore := filepath.Join(dir, "crontab.txt")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCrontab(t, filepath.Join(fakeBin, "crontab"), crontabStore)
	writeFakeSystemctl(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_CRONTAB_FILE", crontabStore)
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "use", "cron"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler use cron 失败: %v", err)
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `mode = "cron"`) {
		t.Fatalf("expected mode=cron: %q", string(payload))
	}
	cronPayload, err := os.ReadFile(crontabStore)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cronPayload), scheduler.ManagedCronBegin) {
		t.Fatalf("expected managed cron block: %q", string(cronPayload))
	}
	logPayload, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logPayload), "disable --now ccclaw-ingest.timer") {
		t.Fatalf("expected systemctl disable call: %q", string(logPayload))
	}
	if !strings.Contains(out.String(), "已切换调度后端: mode=cron") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestSchedulerUseSystemdCommand(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(dir, "app")
	systemdDir := filepath.Join(dir, "systemd-user")
	sourceDir := filepath.Join(appDir, "ops", "systemd")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"ccclaw-ingest.service",
		"ccclaw-ingest.timer",
		"ccclaw-patrol.service",
		"ccclaw-patrol.timer",
		"ccclaw-journal.service",
		"ccclaw-journal.timer",
		"ccclaw-archive.service",
		"ccclaw-archive.timer",
	} {
		if err := os.WriteFile(filepath.Join(sourceDir, name), []byte("[Unit]\nDescription=test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(testConfigTomlWithSystemdDir(appDir, envPath, systemdDir, "cron")), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(dir, "bin")
	crontabStore := filepath.Join(dir, "crontab.txt")
	systemctlLog := filepath.Join(dir, "systemctl.log")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeCrontab(t, filepath.Join(fakeBin, "crontab"), crontabStore)
	if err := os.WriteFile(crontabStore, []byte(scheduler.ManagedCronBlock(testSchedulerConfig(appDir, envPath, systemdDir, "cron").toConfig())), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeSystemctl(t, filepath.Join(fakeBin, "systemctl"), systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CCCLAW_FAKE_CRONTAB_FILE", crontabStore)
	t.Setenv("CCCLAW_FAKE_SYSTEMCTL_LOG", systemctlLog)

	cmd := newRootCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--config", configPath, "scheduler", "use", "systemd"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("执行 scheduler use systemd 失败: %v", err)
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `mode = "systemd"`) {
		t.Fatalf("expected mode=systemd: %q", string(payload))
	}
	cronPayload, err := os.ReadFile(crontabStore)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cronPayload), scheduler.ManagedCronBegin) {
		t.Fatalf("managed cron should be removed: %q", string(cronPayload))
	}
	if _, err := os.Stat(filepath.Join(systemdDir, "ccclaw-ingest.service")); err != nil {
		t.Fatalf("expected unit file copied: %v", err)
	}
	logPayload, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logPayload), "enable --now ccclaw-ingest.timer") {
		t.Fatalf("expected systemctl enable call: %q", string(logPayload))
	}
	if !strings.Contains(out.String(), "已切换调度后端: mode=systemd") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestParseStatsOptions(t *testing.T) {
	options, err := parseStatsOptions("2026-03-09", "2026-03-10", true, true, 7)
	if err != nil {
		t.Fatalf("解析 stats 选项失败: %v", err)
	}
	if !options.Daily || !options.ShowRTKComparison {
		t.Fatalf("unexpected options flags: %#v", options)
	}
	if options.Limit != 7 {
		t.Fatalf("unexpected limit: %#v", options)
	}
	if options.Start.Format("2006-01-02") != "2026-03-09" {
		t.Fatalf("unexpected start: %#v", options.Start)
	}
	if options.End.Format("2006-01-02") != "2026-03-11" {
		t.Fatalf("unexpected end: %#v", options.End)
	}
}

func TestHandleClaudeHookSkipsWhenTaskContextMissing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.toml")
	if err := handleClaudeHook("stop", nil, configPath); err != nil {
		t.Fatalf("无 task 上下文时应静默跳过，实际报错: %v", err)
	}
}

func TestHandleClaudeHookUsesConfigAppDirWhenEnvMissing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	appDir := filepath.Join(dir, "app")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigTomlWithSystemdDir(appDir, envPath, filepath.Join(dir, "systemd-user"), "none")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCCLAW_TASK_ID", "task-from-config")

	if err := handleClaudeHook("stop", nil, configPath); err != nil {
		t.Fatalf("应从 config.toml 推导 hook 状态目录，实际报错: %v", err)
	}

	state, err := claude.LoadHookState(claude.DefaultHookStateDir(appDir), "task-from-config")
	if err != nil {
		t.Fatalf("读取 hook 状态失败: %v", err)
	}
	if state == nil {
		t.Fatal("预期写入 hook 状态，实际为空")
	}
	if state.StoppedAt.IsZero() {
		t.Fatalf("预期 Stop hook 记录 stopped_at，实际为零值: %+v", state)
	}
}

func TestHandleClaudeHookPrefersExplicitHookStateDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	appDir := filepath.Join(dir, "app")
	overrideDir := filepath.Join(dir, "override-hooks")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(testConfigTomlWithSystemdDir(appDir, envPath, filepath.Join(dir, "systemd-user"), "none")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCCLAW_TASK_ID", "task-env-override")
	t.Setenv("CCCLAW_HOOK_STATE_DIR", overrideDir)

	if err := handleClaudeHook("stop", nil, configPath); err != nil {
		t.Fatalf("显式 hook 状态目录应优先于配置，实际报错: %v", err)
	}

	overrideState, err := claude.LoadHookState(overrideDir, "task-env-override")
	if err != nil {
		t.Fatalf("读取 override hook 状态失败: %v", err)
	}
	if overrideState == nil {
		t.Fatal("预期写入 override hook 状态，实际为空")
	}
	defaultState, err := claude.LoadHookState(claude.DefaultHookStateDir(appDir), "task-env-override")
	if err != nil {
		t.Fatalf("读取默认 hook 状态失败: %v", err)
	}
	if defaultState != nil {
		t.Fatalf("显式目录覆盖时不应写入默认目录: %+v", defaultState)
	}
}

func testConfigToml(root, envPath, mode string) string {
	return testConfigTomlWithSystemdDir(filepath.Join(root, "app"), envPath, filepath.Join(root, "systemd-user"), mode)
}

func testConfigTomlWithSystemdDir(appDir, envPath, systemdDir, mode string) string {
	return "default_target = ''\n\n" +
		"[github]\ncontrol_repo = '41490/ccclaw'\n\n" +
		"[paths]\n" +
		"app_dir = '" + appDir + "'\n" +
		"home_repo = '/opt/ccclaw'\n" +
		"var_dir = '" + filepath.Join(appDir, "var") + "'\n" +
		"log_dir = '" + filepath.Join(appDir, "log") + "'\n" +
		"kb_dir = '/opt/ccclaw/kb'\n" +
		"env_file = '" + envPath + "'\n\n" +
		"[executor]\ncommand = ['claude']\n\n" +
		"[scheduler]\n" +
		"mode = '" + mode + "'\n" +
		"systemd_user_dir = '" + systemdDir + "'\n\n" +
		"[approval]\n" +
		"words = ['approve']\n" +
		"reject_words = ['reject']\n" +
		"minimum_permission = 'maintain'\n"
}

func testSchedulerConfig(appDir, envPath, systemdDir, mode string) *schedulerConfigFixture {
	return &schedulerConfigFixture{
		AppDir:     appDir,
		EnvPath:    envPath,
		SystemdDir: systemdDir,
		Mode:       mode,
	}
}

type schedulerConfigFixture struct {
	AppDir     string
	EnvPath    string
	SystemdDir string
	Mode       string
}

func (f *schedulerConfigFixture) toConfig() *config.Config {
	return &config.Config{
		GitHub: config.GitHubConfig{ControlRepo: "41490/ccclaw"},
		Paths: config.PathsConfig{
			AppDir:   f.AppDir,
			HomeRepo: "/opt/ccclaw",
			VarDir:   filepath.Join(f.AppDir, "var"),
			LogDir:   filepath.Join(f.AppDir, "log"),
			KBDir:    "/opt/ccclaw/kb",
			EnvFile:  f.EnvPath,
		},
		Executor: config.ExecutorConfig{Command: []string{"claude"}},
		Scheduler: config.SchedulerConfig{
			Mode:           f.Mode,
			SystemdUserDir: f.SystemdDir,
			Logs: config.SchedulerLogsConfig{
				Level:         "info",
				ArchiveDir:    filepath.Join(f.AppDir, "log", "scheduler"),
				RetentionDays: 30,
				MaxFiles:      200,
				Compress:      true,
			},
		},
		Approval: config.ApprovalConfig{
			Words:             []string{"approve"},
			RejectWords:       []string{"reject"},
			MinimumPermission: "maintain",
		},
	}
}

func writeFakeCrontab(t *testing.T, scriptPath, store string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
store="${CCCLAW_FAKE_CRONTAB_FILE:?}"
case "${1:-}" in
  -l)
    if [[ -f "$store" && -s "$store" ]]; then
      cat "$store"
      exit 0
    fi
    printf 'no crontab for tester\n' >&2
    exit 1
    ;;
  -)
    cat > "$store"
    exit 0
    ;;
  *)
    printf 'unsupported crontab args: %s\n' "$*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFakeSystemctl(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_SYSTEMCTL_LOG:?}"
printf '%s\n' "$*" >> "$log_file"
exit 0
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeSystemctlProbe(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_SYSTEMCTL_LOG:?}"
printf '%s\n' "$*" >> "$log_file"
printf 'disabled\n' >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeDuckDB(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_DUCKDB_LOG:?}"
printf '%s\n' "$*" >> "$log_file"
sql=""
while (($#)); do
  case "$1" in
    -c)
      shift
      sql="${1:-}"
      ;;
  esac
  shift || true
done
if [[ "$sql" =~ TO\ \'([^\']+)\' ]]; then
  target="${BASH_REMATCH[1]}"
  mkdir -p "$(dirname "$target")"
  printf 'parquet\n' > "$target"
fi
printf 'ok\n'
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeSystemctlTimers(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_SYSTEMCTL_LOG:?}"
printf '%s\n' "$*" >> "$log_file"
if [[ "${1:-}" == "--user" && "${2:-}" == "show" ]]; then
  unit="${3:-}"
  cat <<EOF
Id=${unit}
ActiveState=active
UnitFileState=enabled
NextElapseUSecRealtime=Wed 2026-03-11 06:15:00 EDT
LastTriggerUSec=Wed 2026-03-11 06:10:15 EDT
Result=success
Triggers=${unit%.timer}.service
EOF
  exit 0
fi
printf 'unsupported systemctl args: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeSystemctlDoctor(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_SYSTEMCTL_LOG:?}"
printf '%s\n' "$*" >> "$log_file"
if [[ "${1:-}" == "--user" && "${2:-}" == "show-environment" ]]; then
  printf 'XDG_RUNTIME_DIR=/run/user/1000\n'
  exit 0
fi
if [[ "${1:-}" == "--user" && "${2:-}" == "is-enabled" ]]; then
  printf 'enabled\n'
  exit 0
fi
if [[ "${1:-}" == "--user" && "${2:-}" == "is-active" ]]; then
  printf 'active\n'
  exit 0
fi
if [[ "${1:-}" == "--user" && "${2:-}" == "show" ]]; then
  unit="${3:-}"
  if [[ "$unit" == *.service ]]; then
    cat <<EOF
Id=${unit}
ActiveState=inactive
SubState=dead
UnitFileState=static
Result=success
ExecMainCode=exited
ExecMainStatus=0
EOF
    exit 0
  fi
  cat <<EOF
Id=${unit}
ActiveState=active
UnitFileState=enabled
NextElapseUSecRealtime=Wed 2026-03-11 06:15:00 EDT
LastTriggerUSec=Wed 2026-03-11 06:10:15 EDT
Result=success
Triggers=${unit%.timer}.service
EOF
  exit 0
fi
printf 'unsupported systemctl args: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeLoginctl(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_LOGINCTL_LOG:?}"
printf '%s\n' "$*" >> "$log_file"
if [[ "${1:-}" == "show-user" ]]; then
  cat <<EOF
Linger=yes
State=active
EOF
  exit 0
fi
printf 'unsupported loginctl args: %s\n' "$*" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeJournalctl(t *testing.T, scriptPath, logPath string) {
	t.Helper()
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${CCCLAW_FAKE_JOURNALCTL_LOG:?}"
printf '%s\n' "$*" > "$log_file"
printf 'ok\n'
`
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeManagedUnitFixture(cfg *config.Config) error {
	if err := os.MkdirAll(cfg.Scheduler.SystemdUserDir, 0o755); err != nil {
		return err
	}
	units, err := scheduler.GenerateSystemdUnitContents(cfg)
	if err != nil {
		return err
	}
	for name, content := range units {
		if err := os.WriteFile(filepath.Join(cfg.Scheduler.SystemdUserDir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func TestParseStatsOptionsRejectsInvalidRange(t *testing.T) {
	_, err := parseStatsOptions("2026-03-10", "2026-03-09", false, false, 20)
	if err == nil {
		t.Fatal("预期无效日期范围报错")
	}
}

func TestParseStatsOptionsAcceptsOpenEndedRange(t *testing.T) {
	options, err := parseStatsOptions("", "2026-03-10", false, false, 20)
	if err != nil {
		t.Fatalf("解析开放区间失败: %v", err)
	}
	if !options.Start.IsZero() {
		t.Fatalf("unexpected start: %#v", options.Start)
	}
	want := time.Date(2026, 3, 11, 0, 0, 0, 0, time.Local)
	if !options.End.Equal(want) {
		t.Fatalf("unexpected end: got=%v want=%v", options.End, want)
	}
}

func TestParseStatsOptionsRejectsNonPositiveLimit(t *testing.T) {
	_, err := parseStatsOptions("", "", false, false, 0)
	if err == nil {
		t.Fatal("预期 limit 非法时报错")
	}
}
