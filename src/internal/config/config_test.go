package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("GH_TOKEN=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnvFile(path); err != nil {
		t.Fatalf("expected valid env file, got %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnvFile(path); err == nil {
		t.Fatal("expected permission validation error")
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got := ExpandPath("~/.ccclaw")
	want := filepath.Join(home, ".ccclaw")
	if got != want {
		t.Fatalf("unexpected expanded path: got %q want %q", got, want)
	}
}

func TestLoadConfigWithCommandAndHomePaths(t *testing.T) {
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
provider = "claude-code"
command = ["~/.ccclaw/bin/ccclaude"]
timeout = "30m"

[approval]
words = ["approve", "go", "推进"]
reject_words = ["reject", "no", "拒绝"]
minimum_permission = "maintain"

[[targets]]
repo = "41490/ccclaw"
local_path = "~/src/ccclaw"
kb_path = "/opt/ccclaw/kb"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Executor.Command) != 1 {
		t.Fatalf("unexpected executor command: %#v", cfg.Executor.Command)
	}
	if cfg.Executor.Command[0] == "~/.ccclaw/bin/ccclaude" {
		t.Fatal("expected executor command path to be expanded")
	}
	if cfg.Targets[0].LocalPath == "~/src/ccclaw" {
		t.Fatal("expected target local path to be expanded")
	}
}

func TestLoadConfigAllowsZeroTargets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `default_target = ""

[github]
control_repo = "41490/ccclaw"

[paths]
app_dir = "~/.ccclaw"
home_repo = "/opt/ccclaw"
var_dir = "~/.ccclaw/var"
log_dir = "~/.ccclaw/log"
kb_dir = "/opt/ccclaw/kb"
env_file = "~/.ccclaw/.env"

[executor]
provider = "claude-code"
command = ["~/.ccclaw/bin/ccclaude"]
timeout = "30m"

[approval]
words = ["approve"]
reject_words = ["reject"]
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 0 {
		t.Fatalf("expected no targets, got %d", len(cfg.Targets))
	}
}

func TestExecutorModeForRepoSupportsGlobalAndOverride(t *testing.T) {
	cfg := &Config{
		Executor: ExecutorConfig{Mode: "tmux"},
		Targets: []TargetConfig{
			{Repo: "41490/ccclaw", LocalPath: "/opt/src/ccclaw", ExecutorMode: "daemon"},
			{Repo: "41490/other", LocalPath: "/opt/src/other"},
		},
	}
	cfg.NormalizePaths()
	if got := cfg.ExecutorMode(); got != ExecutorModeTMux {
		t.Fatalf("unexpected executor mode: %s", got)
	}
	if got := cfg.ExecutorModeForRepo("41490/ccclaw"); got != ExecutorModeDaemon {
		t.Fatalf("expected daemon override, got %s", got)
	}
	if got := cfg.ExecutorModeForRepo("41490/other"); got != ExecutorModeTMux {
		t.Fatalf("expected tmux fallback, got %s", got)
	}
}

func TestLoadRejectsInvalidExecutorMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[github]
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
mode = "invalid-mode"

[approval]
words = ["approve"]
reject_words = ["reject"]
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err == nil {
		t.Fatal("expected invalid executor.mode error")
	}
}

func TestLoadConfigDefaultsSchedulerLogArchiveDirFromLogDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[github]
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

[approval]
words = ["approve"]
reject_words = ["reject"]
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Scheduler.Logs.Level, "info"; got != want {
		t.Fatalf("unexpected scheduler log level: got=%q want=%q", got, want)
	}
	if got, want := cfg.Scheduler.Logs.ArchiveDir, "/tmp/ccclaw-app/log/scheduler"; got != want {
		t.Fatalf("unexpected scheduler archive dir: got=%q want=%q", got, want)
	}
	if got, want := cfg.Scheduler.Logs.RetentionDays, defaultSchedulerLogRetentionDays; got != want {
		t.Fatalf("unexpected scheduler retention days: got=%d want=%d", got, want)
	}
	if got, want := cfg.Scheduler.Logs.MaxFiles, defaultSchedulerLogMaxFiles; got != want {
		t.Fatalf("unexpected scheduler max files: got=%d want=%d", got, want)
	}
	if got, want := cfg.Scheduler.Logs.Compress, defaultSchedulerLogCompress; got != want {
		t.Fatalf("unexpected scheduler compress default: got=%t want=%t", got, want)
	}
}

func TestLoadConfigPreservesExplicitSchedulerLogCompressFalse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[github]
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

[scheduler.logs]
compress = false

[approval]
words = ["approve"]
reject_words = ["reject"]
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scheduler.Logs.Compress {
		t.Fatal("expected explicit compress=false to be preserved")
	}
}

func TestLoadConfigRejectsLegacyApprovalCommandField(t *testing.T) {
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
provider = "claude-code"
command = ["~/.ccclaw/bin/ccclaude"]
timeout = "30m"

[approval]
command = "/ccclaw approve"
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err == nil {
		t.Fatal("expected legacy approval.command to be rejected")
	} else if got := err.Error(); !strings.Contains(got, "config migrate") {
		t.Fatalf("expected migration hint, got %q", got)
	}
}

func TestLoadAutoRewritesLegacyStateDBToVarDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[github]
control_repo = "41490/ccclaw"

[paths]
app_dir = "/tmp/ccclaw-app"
home_repo = "/opt/ccclaw"
state_db = "/tmp/ccclaw-app/var/state.db"
log_dir = "/tmp/ccclaw-app/log"
kb_dir = "/opt/ccclaw/kb"
env_file = "/tmp/ccclaw-app/.env"

[executor]
command = ["claude"]

[approval]
words = ["approve"]
reject_words = ["reject"]
minimum_permission = "maintain"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if got, want := cfg.Paths.VarDir, "/tmp/ccclaw-app/var"; got != want {
		t.Fatalf("unexpected var dir: got=%q want=%q", got, want)
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `var_dir = "/tmp/ccclaw-app/var"`) {
		t.Fatalf("expected var_dir auto writeback, got %q", text)
	}
	if strings.Contains(text, "state_db") {
		t.Fatalf("legacy state_db should be removed, got %q", text)
	}
}

func TestMigrateRewritesLegacyConfigToCurrentShape(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `default_target = "41490/task-local"

[github]
control_repo = "someone/else"
issue_label = "ccclaw"
limit = 20

[paths]
app_dir = "/tmp/ccclaw-app"
home_repo = "/opt/data/9527"
state_db = "/tmp/ccclaw-app/var/state.db"
log_dir = "/tmp/ccclaw-app/log"
kb_dir = "/opt/data/9527/kb"
env_file = "/tmp/ccclaw-app/.env"

[executor]
provider = "claude-code"
binary = ""
command = ["/tmp/ccclaw-app/bin/ccclaude"]
timeout = "30m"

[scheduler]
mode = "systemd"
systemd_user_dir = "/tmp/systemd-user"

[approval]
command = "/ccclaw approve"
minimum_permission = "admin"

[[targets]]
repo = "41490/task-local"
local_path = "/opt/src/task-local"
kb_path = "/opt/data/9527/kb"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := Migrate(configPath)
	if err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if !changed {
		t.Fatal("expected migrate to rewrite legacy config")
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{
		`control_repo = "41490/ccclaw"`,
		`home_repo = "/opt/data/9527"`,
		`var_dir = "/tmp/ccclaw-app/var"`,
		`calendar_timezone = "Asia/Shanghai"`,
		`[scheduler.timers]`,
		`[scheduler.logs]`,
		`archive_dir = "/tmp/ccclaw-app/log/scheduler"`,
		`retention_days = 30`,
		`max_files = 200`,
		`compress = true`,
		`minimum_permission = "admin"`,
		`reject_words = ["reject", "no", "cancel", "nil", "null", "拒绝", "000"]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
	if strings.Contains(text, `control_repo = "someone/else"`) {
		t.Fatalf("control repo should be normalized: %q", text)
	}
	if strings.Contains(text, `command = "/ccclaw approve"`) {
		t.Fatalf("legacy approval command should be removed: %q", text)
	}
	if strings.Contains(text, "state_db") {
		t.Fatalf("legacy state_db should be removed: %q", text)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("load migrated config failed: %v", err)
	}
	if got, want := cfg.GitHub.ControlRepo, OfficialControlRepo; got != want {
		t.Fatalf("unexpected control repo: got=%q want=%q", got, want)
	}
	if got, want := cfg.Paths.HomeRepo, "/opt/data/9527"; got != want {
		t.Fatalf("unexpected home repo: got=%q want=%q", got, want)
	}
	if got, want := cfg.Paths.VarDir, "/tmp/ccclaw-app/var"; got != want {
		t.Fatalf("unexpected var dir: got=%q want=%q", got, want)
	}
}

func TestMigrateNoopForCurrentConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	cfg := &Config{
		DefaultTarget: "41490/task-local",
		GitHub: GitHubConfig{
			ControlRepo: OfficialControlRepo,
			IssueLabel:  "ccclaw",
			Limit:       20,
		},
		Paths: PathsConfig{
			AppDir:   "/tmp/ccclaw-app",
			HomeRepo: "/opt/data/9527",
			VarDir:   "/tmp/ccclaw-app/var",
			LogDir:   "/tmp/ccclaw-app/log",
			KBDir:    "/opt/data/9527/kb",
			EnvFile:  "/tmp/ccclaw-app/.env",
		},
		Executor: ExecutorConfig{
			Provider: "claude-code",
			Command:  []string{"/tmp/ccclaw-app/bin/ccclaude"},
			Timeout:  "30m",
		},
		Scheduler: SchedulerConfig{
			Mode:             "systemd",
			SystemdUserDir:   "/tmp/systemd-user",
			CalendarTimezone: "Asia/Shanghai",
			Timers: SchedulerTimersConfig{
				Ingest:  "*:0/5",
				Run:     "*:0/10",
				Patrol:  "*:0/2",
				Journal: "*-*-* 23:50:00",
			},
			Logs: SchedulerLogsConfig{
				Level:         "info",
				ArchiveDir:    "/tmp/ccclaw-app/log/scheduler",
				RetentionDays: defaultSchedulerLogRetentionDays,
				MaxFiles:      defaultSchedulerLogMaxFiles,
				Compress:      defaultSchedulerLogCompress,
			},
		},
		Approval: ApprovalConfig{
			Words:             []string{"approve"},
			RejectWords:       []string{"reject"},
			MinimumPermission: "maintain",
		},
		Targets: []TargetConfig{{
			Repo:      "41490/task-local",
			LocalPath: "/opt/src/task-local",
			KBPath:    "/opt/data/9527/kb",
		}},
	}
	if err := Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	changed, err := Migrate(configPath)
	if err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if changed {
		t.Fatal("expected migrate to be noop for current config")
	}
}

func TestMigrateLegacyApprovalRewritesSection(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `default_target = ""

[github]
control_repo = "41490/ccclaw"

[approval]
command = "/ccclaw approve"
minimum_permission = "admin"

[[targets]]
repo = "41490/ccclaw"
local_path = "/opt/src/ccclaw"
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateLegacyApproval(configPath)
	if err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if !changed {
		t.Fatal("expected file to change")
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{
		`minimum_permission = "admin"`,
		`words = ["approve", "go", "confirm", "批准", "agree", "同意", "推进", "通过", "ok"]`,
		`reject_words = ["reject", "no", "cancel", "nil", "null", "拒绝", "000"]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
	if strings.Contains(text, `command = "/ccclaw approve"`) {
		t.Fatalf("legacy command should be removed: %q", text)
	}
}

func minimalValidTOML(extraSections ...string) string {
	base := `
[github]
control_repo = "test/repo"

[paths]
app_dir    = "/tmp/ccclaw-test"
home_repo  = "/tmp/ccclaw-repo"
var_dir    = "/tmp/ccclaw-var"
log_dir    = "/tmp/ccclaw-log"
kb_dir     = "/tmp/ccclaw-kb"
env_file   = "/tmp/ccclaw.env"

[executor]
command = ["claude"]
`
	for _, s := range extraSections {
		base += "\n" + s
	}
	return base
}

func TestKBConfigContextMaxLinesDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(minimalValidTOML()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.KB.ContextMaxLines != 256 {
		t.Fatalf("expected ContextMaxLines=256, got %d", cfg.KB.ContextMaxLines)
	}
}

func TestKBConfigContextMaxLinesConfigurable(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(minimalValidTOML(`
[kb]
context_max_lines = 128
`)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.KB.ContextMaxLines != 128 {
		t.Fatalf("expected ContextMaxLines=128, got %d", cfg.KB.ContextMaxLines)
	}
}

func TestMigrateLegacyApprovalNoopForMigratedConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `[approval]
minimum_permission = "maintain"
words = ["approve"]
reject_words = ["reject"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := MigrateLegacyApproval(configPath)
	if err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if changed {
		t.Fatal("expected no change for migrated config")
	}
}

func TestDisableTargetClearsDefaultTarget(t *testing.T) {
	cfg := &Config{
		DefaultTarget: "41490/ccclaw",
		GitHub:        GitHubConfig{ControlRepo: "41490/ccclaw"},
		Paths: PathsConfig{
			AppDir:   "/tmp/app",
			HomeRepo: "/opt/ccclaw",
			VarDir:   "/tmp/app/var",
			LogDir:   "/tmp/app/log",
			KBDir:    "/opt/ccclaw/kb",
			EnvFile:  "/tmp/app/.env",
		},
		Executor: ExecutorConfig{Command: []string{"claude"}, Timeout: "30m"},
		Scheduler: SchedulerConfig{
			Mode:             "none",
			SystemdUserDir:   "/tmp/systemd-user",
			CalendarTimezone: "Asia/Shanghai",
			Timers: SchedulerTimersConfig{
				Ingest:  "*:0/5",
				Run:     "*:0/10",
				Patrol:  "*:0/2",
				Journal: "*-*-* 23:50:00",
			},
			Logs: SchedulerLogsConfig{
				Level:         "info",
				ArchiveDir:    "/tmp/app/log/scheduler",
				RetentionDays: defaultSchedulerLogRetentionDays,
				MaxFiles:      defaultSchedulerLogMaxFiles,
				Compress:      defaultSchedulerLogCompress,
			},
		},
		Approval: ApprovalConfig{
			Words:             []string{"approve", "go"},
			RejectWords:       []string{"reject"},
			MinimumPermission: "maintain",
		},
		Targets: []TargetConfig{{
			Repo:      "41490/ccclaw",
			LocalPath: "/opt/src/ccclaw",
			KBPath:    "/opt/ccclaw/kb",
		}},
	}
	if err := cfg.DisableTarget("41490/ccclaw"); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultTarget != "" {
		t.Fatalf("expected default target to be cleared, got %q", cfg.DefaultTarget)
	}
	if !cfg.Targets[0].Disabled {
		t.Fatal("expected target to be disabled")
	}
}

func TestUpdateSchedulerSectionPreservesOuterContent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `default_target = ""

[github]
control_repo = "41490/ccclaw"
# github 注释应保留

[scheduler]
mode = "none"
systemd_user_dir = "~/.config/systemd/user"

[approval]
minimum_permission = "maintain"
words = ["approve"]
reject_words = ["reject"]
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	err := UpdateSchedulerSection(configPath, SchedulerConfig{
		Mode:             "systemd",
		SystemdUserDir:   "/tmp/systemd-user",
		CalendarTimezone: "Asia/Shanghai",
		Timers: SchedulerTimersConfig{
			Ingest:  "*:0/5",
			Run:     "*:0/10",
			Patrol:  "*:0/2",
			Journal: "*-*-* 01:01:42",
		},
		Logs: SchedulerLogsConfig{
			Level:         "warning",
			ArchiveDir:    "/tmp/ccclaw-app/log/scheduler",
			RetentionDays: defaultSchedulerLogRetentionDays,
			MaxFiles:      defaultSchedulerLogMaxFiles,
			Compress:      defaultSchedulerLogCompress,
		},
	})
	if err != nil {
		t.Fatalf("update scheduler failed: %v", err)
	}
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{
		`# github 注释应保留`,
		`mode = "systemd"`,
		`systemd_user_dir = "/tmp/systemd-user"`,
		`calendar_timezone = "Asia/Shanghai"`,
		`journal = "*-*-* 01:01:42"`,
		`# - 若要配置凌晨 01:01:42，可写为 ` + "`*-*-* 01:01:42`",
		`[scheduler.logs]`,
		`level = "warning"`,
		`archive_dir = "/tmp/ccclaw-app/log/scheduler"`,
		`retention_days = 30`,
		`max_files = 200`,
		`compress = true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}
