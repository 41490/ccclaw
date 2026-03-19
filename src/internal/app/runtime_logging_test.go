package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/logging"
)

func TestIngestLogsRespectRuntimeLevel(t *testing.T) {
	fakeBin := writeFakeBin(t, map[string]string{
		"gh": `#!/bin/sh
set -eu
if [ "${1:-}" != "api" ]; then
  exit 1
fi
endpoint="${2:-}"
case "$endpoint" in
  "repos/41490/ccclaw/issues?state=open&per_page=20&labels=ccclaw")
    printf '[]\n'
    ;;
  *)
    printf 'unexpected endpoint: %s\n' "$endpoint" >&2
    exit 1
    ;;
esac
`,
	})
	t.Setenv("PATH", fakeBin)

	infoLogs := runRuntimeLoggingEntry(t, "info", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Ingest(context.Background())
	})
	if !strings.Contains(infoLogs, "开始同步 open issues") {
		t.Fatalf("expected ingest info log, got %q", infoLogs)
	}

	warningLogs := runRuntimeLoggingEntry(t, "warning", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Ingest(context.Background())
	})
	if strings.Contains(warningLogs, "开始同步 open issues") {
		t.Fatalf("warning level should suppress ingest info log: %q", warningLogs)
	}
}

func TestRunLogsRespectRuntimeLevel(t *testing.T) {
	infoLogs := runRuntimeLoggingEntry(t, "info", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Run(context.Background(), out, 10)
	})
	if !strings.Contains(infoLogs, "兼容入口已转发到全局单飞 ingest 调度") {
		t.Fatalf("expected run info log, got %q", infoLogs)
	}

	warningLogs := runRuntimeLoggingEntry(t, "warning", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Run(context.Background(), out, 10)
	})
	if strings.Contains(warningLogs, "兼容入口已转发到全局单飞 ingest 调度") {
		t.Fatalf("warning level should suppress run info log: %q", warningLogs)
	}
}

func TestPatrolLogsRespectRuntimeLevel(t *testing.T) {
	fakeBin := writeFakeBin(t, map[string]string{
		"claude": "#!/bin/sh\nprintf '{}\\n'\n",
	})
	t.Setenv("PATH", fakeBin)

	infoLogs := runRuntimeLoggingEntry(t, "info", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Patrol(context.Background(), out)
	})
	if !strings.Contains(infoLogs, "当前未启用 tmux，会话巡查已跳过") {
		t.Fatalf("expected patrol info log, got %q", infoLogs)
	}

	warningLogs := runRuntimeLoggingEntry(t, "warning", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Patrol(context.Background(), out)
	})
	if strings.Contains(warningLogs, "当前未启用 tmux，会话巡查已跳过") {
		t.Fatalf("warning level should suppress patrol info log: %q", warningLogs)
	}
}

func TestJournalLogsRespectRuntimeLevel(t *testing.T) {
	infoLogs := runRuntimeLoggingEntry(t, "info", func(rt *Runtime, out *bytes.Buffer) error {
		rt.syncRepo = func(repoPath, message string, paths []string, maxRetry int) error {
			return nil
		}
		return rt.Journal(time.Date(2026, 3, 12, 0, 0, 0, 0, time.Local), out)
	})
	if !strings.Contains(infoLogs, "日报已生成") {
		t.Fatalf("expected journal info log, got %q", infoLogs)
	}

	warningLogs := runRuntimeLoggingEntry(t, "warning", func(rt *Runtime, out *bytes.Buffer) error {
		rt.syncRepo = func(repoPath, message string, paths []string, maxRetry int) error {
			return nil
		}
		return rt.Journal(time.Date(2026, 3, 12, 0, 0, 0, 0, time.Local), out)
	})
	if strings.Contains(warningLogs, "日报已生成") {
		t.Fatalf("warning level should suppress journal info log: %q", warningLogs)
	}
}

func TestStatusLogsRespectRuntimeLevel(t *testing.T) {
	infoLogs := runRuntimeLoggingEntry(t, "info", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Status(out)
	})
	if !strings.Contains(infoLogs, "开始生成运行态快照") {
		t.Fatalf("expected status info log, got %q", infoLogs)
	}
	if !strings.Contains(infoLogs, "entry=status") {
		t.Fatalf("expected status entry field, got %q", infoLogs)
	}

	warningLogs := runRuntimeLoggingEntry(t, "warning", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.Status(out)
	})
	if strings.Contains(warningLogs, "开始生成运行态快照") {
		t.Fatalf("warning level should suppress status info log: %q", warningLogs)
	}
}

func TestConfigLogsRespectRuntimeLevel(t *testing.T) {
	infoLogs := runRuntimeLoggingEntry(t, "info", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.ShowConfig(out)
	})
	if !strings.Contains(infoLogs, "开始导出配置快照") {
		t.Fatalf("expected config info log, got %q", infoLogs)
	}
	if !strings.Contains(infoLogs, "配置快照已输出") {
		t.Fatalf("expected config completion log, got %q", infoLogs)
	}

	warningLogs := runRuntimeLoggingEntry(t, "warning", func(rt *Runtime, out *bytes.Buffer) error {
		return rt.ShowConfig(out)
	})
	if strings.Contains(warningLogs, "开始导出配置快照") {
		t.Fatalf("warning level should suppress config info log: %q", warningLogs)
	}
}

func TestRuntimeLogsNormalizeFixedFieldOrder(t *testing.T) {
	logs := new(bytes.Buffer)
	logger, level, err := logging.New(logs, "debug")
	if err != nil {
		t.Fatalf("创建 logger 失败: %v", err)
	}

	rt := &Runtime{log: logger, logLevel: level}
	rt.logInfo("run", "固定字段顺序", "session_id", "sess-24", "retry", 2, "target_repo", "41490/ccclaw", "issue", "41490/ccclaw#24", "task_id", "task-24")

	text := logs.String()
	order := []string{
		"entry=run",
		"task_id=task-24",
		"issue=41490/ccclaw#24",
		"target_repo=41490/ccclaw",
		"session_id=sess-24",
		"retry=2",
	}
	last := -1
	for _, want := range order {
		pos := strings.Index(text, want)
		if pos < 0 {
			t.Fatalf("expected %q in %q", want, text)
		}
		if pos < last {
			t.Fatalf("expected fixed fields keep stable order, got %q", text)
		}
		last = pos
	}
}

func runRuntimeLoggingEntry(t *testing.T, level string, run func(rt *Runtime, out *bytes.Buffer) error) string {
	t.Helper()

	configPath, envPath := writeRuntimeLoggingFixture(t)
	logs := new(bytes.Buffer)
	out := new(bytes.Buffer)

	rt, err := NewRuntimeWithOptions(configPath, envPath, RuntimeOptions{
		LogWriter:        logs,
		LogLevelOverride: level,
	})
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	if err := run(rt, out); err != nil {
		t.Fatalf("执行入口失败: %v", err)
	}
	return logs.String()
}

func writeRuntimeLoggingFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	homeRepo := filepath.Join(root, "home")
	logDir := filepath.Join(appDir, "log")
	kbDir := filepath.Join(homeRepo, "kb")
	archiveDir := filepath.Join(logDir, "scheduler")

	for _, path := range []string{
		appDir,
		homeRepo,
		logDir,
		kbDir,
		filepath.Join(kbDir, "journal"),
		filepath.Join(kbDir, "assay"),
		filepath.Join(kbDir, "designs"),
		archiveDir,
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("创建目录失败 %s: %v", path, err)
		}
	}

	envPath := filepath.Join(appDir, ".env")
	if err := os.WriteFile(envPath, []byte("GH_TOKEN=\n"), 0o600); err != nil {
		t.Fatalf("写入 env 失败: %v", err)
	}

	configPath := filepath.Join(appDir, "config.toml")
	configBody := strings.Join([]string{
		`default_target = ""`,
		``,
		`[github]`,
		`control_repo = "41490/ccclaw"`,
		`issue_label = "ccclaw"`,
		`limit = 20`,
		``,
		`[paths]`,
		`app_dir = "` + appDir + `"`,
		`home_repo = "` + homeRepo + `"`,
		`var_dir = "` + filepath.Join(appDir, "var") + `"`,
		`log_dir = "` + logDir + `"`,
		`kb_dir = "` + kbDir + `"`,
		`env_file = "` + envPath + `"`,
		``,
		`[executor]`,
		`command = ["claude"]`,
		`timeout = "1m"`,
		``,
		`[scheduler]`,
		`mode = "none"`,
		`systemd_user_dir = "` + filepath.Join(root, "systemd-user") + `"`,
		`calendar_timezone = "UTC"`,
		``,
		`[scheduler.timers]`,
		`ingest = "*:0/5"`,
		`run = "*:0/10"`,
		`patrol = "*:0/2"`,
		`journal = "*-*-* 23:50:00"`,
		``,
		`[scheduler.logs]`,
		`level = "info"`,
		`archive_dir = "` + archiveDir + `"`,
		``,
		`[approval]`,
		`words = ["approve"]`,
		`reject_words = ["reject"]`,
		`minimum_permission = "maintain"`,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("写入 config 失败: %v", err)
	}
	return configPath, envPath
}

func writeFakeBin(t *testing.T, scripts map[string]string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	for name, body := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("写入 fake 命令失败 %s: %v", name, err)
		}
	}
	return dir
}
