package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const OfficialControlRepo = "41490/ccclaw"

const legacyApprovalMigrationHint = "检测到废弃配置 approval.command；请执行 `ccclaw config migrate`，或兼容使用 `ccclaw config migrate-approval`"

const (
	defaultSchedulerLogLevel         = "info"
	defaultSchedulerLogRetentionDays = 30
	defaultSchedulerLogMaxFiles      = 200
	defaultSchedulerLogCompress      = true
)

type Config struct {
	DefaultTarget string          `mapstructure:"default_target" toml:"default_target"`
	GitHub        GitHubConfig    `mapstructure:"github" toml:"github"`
	Paths         PathsConfig     `mapstructure:"paths" toml:"paths"`
	Executor      ExecutorConfig  `mapstructure:"executor" toml:"executor"`
	Scheduler     SchedulerConfig `mapstructure:"scheduler" toml:"scheduler"`
	Approval      ApprovalConfig  `mapstructure:"approval" toml:"approval"`
	KB            KBConfig        `mapstructure:"kb" toml:"kb"`
	Targets       []TargetConfig  `mapstructure:"targets" toml:"targets"`
}

type KBConfig struct {
	ContextMaxLines int `mapstructure:"context_max_lines" toml:"context_max_lines"`
}

type ExecutorMode string

const (
	ExecutorModeTMux   ExecutorMode = "tmux"
	ExecutorModeDaemon ExecutorMode = "daemon"
)

type GitHubConfig struct {
	ControlRepo string `mapstructure:"control_repo" toml:"control_repo"`
	IssueLabel  string `mapstructure:"issue_label" toml:"issue_label"`
	Limit       int    `mapstructure:"limit" toml:"limit"`
}

type PathsConfig struct {
	AppDir   string `mapstructure:"app_dir" toml:"app_dir"`
	HomeRepo string `mapstructure:"home_repo" toml:"home_repo"`
	VarDir   string `mapstructure:"var_dir" toml:"var_dir"`
	LogDir   string `mapstructure:"log_dir" toml:"log_dir"`
	KBDir    string `mapstructure:"kb_dir" toml:"kb_dir"`
	EnvFile  string `mapstructure:"env_file" toml:"env_file"`
}

type ExecutorConfig struct {
	Provider string   `mapstructure:"provider" toml:"provider"`
	Binary   string   `mapstructure:"binary" toml:"binary"`
	Command  []string `mapstructure:"command" toml:"command"`
	Timeout  string   `mapstructure:"timeout" toml:"timeout"`
	Mode     string   `mapstructure:"mode" toml:"mode"`
}

type SchedulerConfig struct {
	Mode             string                `mapstructure:"mode" toml:"mode"`
	SystemdUserDir   string                `mapstructure:"systemd_user_dir" toml:"systemd_user_dir"`
	CalendarTimezone string                `mapstructure:"calendar_timezone" toml:"calendar_timezone"`
	Timers           SchedulerTimersConfig `mapstructure:"timers" toml:"timers"`
	Logs             SchedulerLogsConfig   `mapstructure:"logs" toml:"logs"`
}

type SchedulerTimersConfig struct {
	Ingest  string `mapstructure:"ingest" toml:"ingest"`
	Run     string `mapstructure:"run" toml:"run"`
	Patrol  string `mapstructure:"patrol" toml:"patrol"`
	Journal string `mapstructure:"journal" toml:"journal"`
}

type SchedulerLogsConfig struct {
	Level         string `mapstructure:"level" toml:"level"`
	ArchiveDir    string `mapstructure:"archive_dir" toml:"archive_dir"`
	RetentionDays int    `mapstructure:"retention_days" toml:"retention_days"`
	MaxFiles      int    `mapstructure:"max_files" toml:"max_files"`
	Compress      bool   `mapstructure:"compress" toml:"compress"`
}

type ApprovalConfig struct {
	Words             []string `mapstructure:"words" toml:"words"`
	RejectWords       []string `mapstructure:"reject_words" toml:"reject_words"`
	MinimumPermission string   `mapstructure:"minimum_permission" toml:"minimum_permission"`
}

type TargetConfig struct {
	Repo         string `mapstructure:"repo" toml:"repo"`
	LocalPath    string `mapstructure:"local_path" toml:"local_path"`
	KBPath       string `mapstructure:"kb_path" toml:"kb_path"`
	ExecutorMode string `mapstructure:"executor_mode" toml:"executor_mode,omitempty"`
	Disabled     bool   `mapstructure:"disabled" toml:"disabled,omitempty"`
}

type Secrets struct {
	Path   string
	Values map[string]string
}

func Load(path string) (*Config, error) {
	path = ExpandPath(path)
	legacyApproval, err := DetectLegacyApprovalCommand(path)
	if err != nil {
		return nil, err
	}
	if legacyApproval {
		return nil, fmt.Errorf("%s: %s", legacyApprovalMigrationHint, path)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	updatedPayload, changed, err := rewriteLegacyStateDBPaths(string(payload))
	if err != nil {
		return nil, err
	}
	if changed {
		if err := os.WriteFile(path, []byte(updatedPayload), 0o644); err != nil {
			return nil, fmt.Errorf("自动回写废弃 paths.state_db 失败: %w", err)
		}
	}

	v := viper.New()
	v.SetConfigType("toml")
	v.SetDefault("github.control_repo", OfficialControlRepo)
	v.SetDefault("github.issue_label", "ccclaw")
	v.SetDefault("github.limit", 20)
	v.SetDefault("executor.provider", "claude-code")
	v.SetDefault("executor.command", []string{"claude"})
	v.SetDefault("executor.timeout", "30m")
	v.SetDefault("executor.mode", string(ExecutorModeDaemon))
	v.SetDefault("scheduler.mode", "none")
	v.SetDefault("scheduler.systemd_user_dir", "~/.config/systemd/user")
	v.SetDefault("scheduler.calendar_timezone", "Asia/Shanghai")
	v.SetDefault("scheduler.timers.ingest", defaultSchedulerTimers().Ingest)
	v.SetDefault("scheduler.timers.run", defaultSchedulerTimers().Run)
	v.SetDefault("scheduler.timers.patrol", defaultSchedulerTimers().Patrol)
	v.SetDefault("scheduler.timers.journal", defaultSchedulerTimers().Journal)
	v.SetDefault("scheduler.logs.level", defaultSchedulerLogLevel)
	v.SetDefault("scheduler.logs.retention_days", defaultSchedulerLogRetentionDays)
	v.SetDefault("scheduler.logs.max_files", defaultSchedulerLogMaxFiles)
	v.SetDefault("scheduler.logs.compress", defaultSchedulerLogCompress)
	v.SetDefault("approval.words", defaultApprovalWords())
	v.SetDefault("approval.reject_words", defaultRejectWords())
	v.SetDefault("approval.minimum_permission", "maintain")
	v.SetDefault("default_target", "")
	v.SetDefault("kb.context_max_lines", 256)

	if err := v.ReadConfig(bytes.NewBufferString(updatedPayload)); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := v.UnmarshalExact(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if !v.IsSet("scheduler.logs.compress") {
		cfg.Scheduler.Logs.Compress = defaultSchedulerLogCompress
	}
	cfg.NormalizePaths()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cfg *Config) NormalizePaths() {
	cfg.GitHub.ControlRepo = OfficialControlRepo
	cfg.Paths.AppDir = ExpandPath(cfg.Paths.AppDir)
	cfg.Paths.HomeRepo = ExpandPath(cfg.Paths.HomeRepo)
	cfg.Paths.VarDir = normalizeVarDir(ExpandPath(cfg.Paths.VarDir))
	cfg.Paths.LogDir = ExpandPath(cfg.Paths.LogDir)
	cfg.Paths.KBDir = ExpandPath(cfg.Paths.KBDir)
	cfg.Paths.EnvFile = ExpandPath(cfg.Paths.EnvFile)
	if cfg.Scheduler.Mode == "" {
		cfg.Scheduler.Mode = "none"
	}
	if cfg.Scheduler.SystemdUserDir == "" {
		cfg.Scheduler.SystemdUserDir = ExpandPath("~/.config/systemd/user")
	} else {
		cfg.Scheduler.SystemdUserDir = ExpandPath(cfg.Scheduler.SystemdUserDir)
	}
	cfg.Scheduler.CalendarTimezone = strings.TrimSpace(cfg.Scheduler.CalendarTimezone)
	if cfg.Scheduler.CalendarTimezone == "" {
		cfg.Scheduler.CalendarTimezone = "Asia/Shanghai"
	}
	normalizeSchedulerTimers(&cfg.Scheduler.Timers)
	normalizeSchedulerLogs(&cfg.Scheduler.Logs, cfg.Paths.LogDir)
	for idx := range cfg.Targets {
		cfg.Targets[idx].LocalPath = ExpandPath(cfg.Targets[idx].LocalPath)
		cfg.Targets[idx].KBPath = ExpandPath(cfg.Targets[idx].KBPath)
		cfg.Targets[idx].ExecutorMode = strings.ToLower(strings.TrimSpace(cfg.Targets[idx].ExecutorMode))
	}
	for idx, arg := range cfg.Executor.Command {
		cfg.Executor.Command[idx] = ExpandPath(arg)
	}
	cfg.Executor.Binary = ExpandPath(cfg.Executor.Binary)
	cfg.Executor.Mode = strings.ToLower(strings.TrimSpace(cfg.Executor.Mode))
	if cfg.Executor.Mode == "" {
		cfg.Executor.Mode = string(ExecutorModeDaemon)
	}
}

func (cfg *Config) Validate() error {
	if cfg.GitHub.ControlRepo == "" {
		return errors.New("github.control_repo 不能为空")
	}
	if cfg.Paths.AppDir == "" {
		return errors.New("paths.app_dir 不能为空")
	}
	if cfg.Paths.HomeRepo == "" {
		return errors.New("paths.home_repo 不能为空")
	}
	if cfg.Paths.VarDir == "" {
		return errors.New("paths.var_dir 不能为空")
	}
	if cfg.Paths.LogDir == "" {
		return errors.New("paths.log_dir 不能为空")
	}
	if cfg.Paths.KBDir == "" {
		return errors.New("paths.kb_dir 不能为空")
	}
	if cfg.Paths.EnvFile == "" {
		return errors.New("paths.env_file 不能为空")
	}
	if len(cfg.Executor.Command) == 0 && cfg.Executor.Binary == "" {
		return errors.New("executor.command 或 executor.binary 至少需要一个")
	}
	if normalizeExecutorMode(cfg.Executor.Mode, "") == "" && strings.TrimSpace(cfg.Executor.Mode) != "" {
		return fmt.Errorf("executor.mode 取值无效: %s", cfg.Executor.Mode)
	}
	switch cfg.Scheduler.Mode {
	case "", "auto", "systemd", "cron", "none":
	default:
		return fmt.Errorf("scheduler.mode 取值无效: %s", cfg.Scheduler.Mode)
	}
	if strings.TrimSpace(cfg.Scheduler.CalendarTimezone) != "" {
		if _, err := time.LoadLocation(cfg.Scheduler.CalendarTimezone); err != nil {
			return fmt.Errorf("scheduler.calendar_timezone 无效: %w", err)
		}
	}
	for key, value := range map[string]string{
		"ingest":  cfg.Scheduler.Timers.Ingest,
		"patrol":  cfg.Scheduler.Timers.Patrol,
		"journal": cfg.Scheduler.Timers.Journal,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("scheduler.timers.%s 不能为空", key)
		}
	}
	if !isSupportedSchedulerLogLevel(cfg.Scheduler.Logs.Level) {
		return fmt.Errorf("scheduler.logs.level 取值无效: %s", cfg.Scheduler.Logs.Level)
	}
	if strings.TrimSpace(cfg.Scheduler.Logs.ArchiveDir) == "" {
		return errors.New("scheduler.logs.archive_dir 不能为空")
	}
	if cfg.Scheduler.Logs.RetentionDays <= 0 {
		return errors.New("scheduler.logs.retention_days 必须大于 0")
	}
	if cfg.Scheduler.Logs.MaxFiles <= 0 {
		return errors.New("scheduler.logs.max_files 必须大于 0")
	}
	if len(normalizeApprovalWords(cfg.Approval.Words)) == 0 {
		return errors.New("approval.words 至少需要一个批准词")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Approval.MinimumPermission)) {
	case "admin", "maintain", "write", "triage", "read", "none":
	default:
		return fmt.Errorf("approval.minimum_permission 取值无效: %s", cfg.Approval.MinimumPermission)
	}
	seenTargets := map[string]struct{}{}
	for _, target := range cfg.Targets {
		if target.Repo == "" || target.LocalPath == "" {
			return errors.New("targets.repo 与 targets.local_path 均不能为空")
		}
		if normalizeExecutorMode(target.ExecutorMode, "") == "" && strings.TrimSpace(target.ExecutorMode) != "" {
			return fmt.Errorf("targets.executor_mode 取值无效: repo=%s mode=%s", target.Repo, target.ExecutorMode)
		}
		if _, exists := seenTargets[target.Repo]; exists {
			return fmt.Errorf("targets.repo 不允许重复: %s", target.Repo)
		}
		seenTargets[target.Repo] = struct{}{}
	}
	if cfg.DefaultTarget != "" {
		target, err := cfg.TargetByRepo(cfg.DefaultTarget)
		if err != nil {
			return fmt.Errorf("default_target 无效: %w", err)
		}
		if target.Disabled {
			return fmt.Errorf("default_target 指向的 target 已禁用: %s", cfg.DefaultTarget)
		}
	}
	return nil
}

func (cfg *Config) TargetByRepo(repo string) (*TargetConfig, error) {
	for _, target := range cfg.Targets {
		if target.Repo == repo {
			copy := target
			if copy.KBPath == "" {
				copy.KBPath = cfg.Paths.KBDir
			}
			if strings.TrimSpace(copy.ExecutorMode) == "" {
				copy.ExecutorMode = string(cfg.ExecutorMode())
			}
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("未找到 repo=%s 的 target 配置", repo)
}

func (cfg *Config) EnabledTargets() []TargetConfig {
	targets := make([]TargetConfig, 0, len(cfg.Targets))
	for _, target := range cfg.Targets {
		if target.Disabled {
			continue
		}
		if target.KBPath == "" {
			target.KBPath = cfg.Paths.KBDir
		}
		if strings.TrimSpace(target.ExecutorMode) == "" {
			target.ExecutorMode = string(cfg.ExecutorMode())
		}
		targets = append(targets, target)
	}
	return targets
}

func (cfg *Config) EnabledTargetByRepo(repo string) (*TargetConfig, error) {
	target, err := cfg.TargetByRepo(repo)
	if err != nil {
		return nil, err
	}
	if target.Disabled {
		return nil, fmt.Errorf("repo=%s 的 target 已禁用", repo)
	}
	return target, nil
}

func (cfg *Config) ExecutorMode() ExecutorMode {
	if cfg == nil {
		return ExecutorModeDaemon
	}
	return normalizeExecutorMode(cfg.Executor.Mode, ExecutorModeDaemon)
}

func (cfg *Config) ExecutorModeForRepo(repo string) ExecutorMode {
	repo = strings.TrimSpace(repo)
	if cfg == nil || repo == "" {
		return ExecutorModeDaemon
	}
	if target, err := cfg.TargetByRepo(repo); err == nil {
		if mode := normalizeExecutorMode(target.ExecutorMode, ""); mode != "" {
			return mode
		}
	}
	return cfg.ExecutorMode()
}

func (cfg *Config) UpsertTarget(target TargetConfig, makeDefault bool) error {
	target.Repo = strings.TrimSpace(target.Repo)
	target.LocalPath = ExpandPath(target.LocalPath)
	target.KBPath = ExpandPath(target.KBPath)
	target.ExecutorMode = string(normalizeExecutorMode(target.ExecutorMode, ""))
	if target.Repo == "" || target.LocalPath == "" {
		return errors.New("repo 与 local_path 均不能为空")
	}
	if target.KBPath == "" {
		target.KBPath = cfg.Paths.KBDir
	}
	for idx := range cfg.Targets {
		if cfg.Targets[idx].Repo != target.Repo {
			continue
		}
		cfg.Targets[idx].LocalPath = target.LocalPath
		cfg.Targets[idx].KBPath = target.KBPath
		cfg.Targets[idx].ExecutorMode = target.ExecutorMode
		cfg.Targets[idx].Disabled = target.Disabled
		if makeDefault {
			cfg.DefaultTarget = target.Repo
		}
		return cfg.Validate()
	}
	cfg.Targets = append(cfg.Targets, target)
	if makeDefault {
		cfg.DefaultTarget = target.Repo
	}
	return cfg.Validate()
}

func (cfg *Config) DisableTarget(repo string) error {
	for idx := range cfg.Targets {
		if cfg.Targets[idx].Repo != repo {
			continue
		}
		cfg.Targets[idx].Disabled = true
		if cfg.DefaultTarget == repo {
			cfg.DefaultTarget = ""
		}
		return cfg.Validate()
	}
	return fmt.Errorf("未找到 repo=%s 的 target 配置", repo)
}

func Save(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("配置不能为空")
	}
	cfg.NormalizePaths()
	if err := cfg.Validate(); err != nil {
		return err
	}
	payload := renderAnnotatedConfig(cfg)
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	return nil
}

func UpdateSchedulerSection(path string, scheduler SchedulerConfig) error {
	path = ExpandPath(path)
	normalizeSchedulerConfig(&scheduler)
	if err := validateSchedulerConfig(scheduler); err != nil {
		return err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}
	updated, changed, err := rewriteSchedulerSection(string(payload), scheduler)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("写入 scheduler 配置失败: %w", err)
	}
	return nil
}

func LoadSecrets(path string) (*Secrets, error) {
	if path == "" {
		return &Secrets{Values: map[string]string{}}, nil
	}
	path = ExpandPath(path)
	if err := ValidateEnvFile(path); err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 .env 文件失败: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf(".env 存在非法行: %q", line)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 .env 文件失败: %w", err)
	}
	return &Secrets{Path: path, Values: values}, nil
}

func ValidateEnvFile(path string) error {
	abs, err := filepath.Abs(ExpandPath(path))
	if err != nil {
		return fmt.Errorf("解析 .env 路径失败: %w", err)
	}

	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("读取 .env 文件信息失败: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(".env 不允许使用符号链接: %s", abs)
	}
	if info.IsDir() {
		return fmt.Errorf(".env 路径不能是目录: %s", abs)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(".env 权限过宽，必须为 0600 或更严格: %s (%#o)", abs, info.Mode().Perm())
	}

	allowedKey := regexp.MustCompile(`^[A-Z0-9_]+$`)
	file, err := os.Open(abs)
	if err != nil {
		return fmt.Errorf("打开 .env 文件失败: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || !allowedKey.MatchString(strings.TrimSpace(parts[0])) {
			return fmt.Errorf(".env 存在非法键格式: %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 .env 文件失败: %w", err)
	}
	return nil
}

func ExpandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if path == "~" {
			return home
		}
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func DetectLegacyApprovalCommand(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	payload, err := os.ReadFile(ExpandPath(path))
	if err != nil {
		return false, fmt.Errorf("读取配置文件失败: %w", err)
	}
	return approvalSectionContainsLegacyCommand(string(payload)), nil
}

func MigrateLegacyApproval(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("配置文件路径不能为空")
	}
	path = ExpandPath(path)
	payload, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读取配置文件失败: %w", err)
	}

	updated, changed, err := rewriteApprovalSection(string(payload))
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("写入迁移后的配置文件失败: %w", err)
	}
	return true, nil
}

func Migrate(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("配置文件路径不能为空")
	}
	path = ExpandPath(path)

	payload, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读取配置文件失败: %w", err)
	}

	loadPath := path
	updatedPayload := string(payload)
	approvalChanged := false

	if rewritten, changed, err := rewriteApprovalSection(updatedPayload); err == nil {
		if changed {
			updatedPayload = rewritten
			approvalChanged = true
		}
	} else if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "未找到 [approval] 配置段") {
		return false, err
	}

	var cleanup func()
	if approvalChanged {
		tmpFile, err := os.CreateTemp(filepath.Dir(path), ".ccclaw-config-migrate-*.toml")
		if err != nil {
			return false, fmt.Errorf("创建迁移临时文件失败: %w", err)
		}
		loadPath = tmpFile.Name()
		cleanup = func() { _ = os.Remove(loadPath) }
		if _, err := tmpFile.WriteString(updatedPayload); err != nil {
			_ = tmpFile.Close()
			cleanup()
			return false, fmt.Errorf("写入迁移临时文件失败: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			cleanup()
			return false, fmt.Errorf("关闭迁移临时文件失败: %w", err)
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	cfg, err := Load(loadPath)
	if err != nil {
		return false, err
	}
	rendered := renderAnnotatedConfig(cfg)
	if rendered == string(payload) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return false, fmt.Errorf("写入迁移后的配置文件失败: %w", err)
	}
	return true, nil
}

func defaultApprovalWords() []string {
	return []string{"approve", "go", "confirm", "批准", "agree", "同意", "推进", "通过", "ok"}
}

func defaultRejectWords() []string {
	return []string{"reject", "no", "cancel", "nil", "null", "拒绝", "000"}
}

func defaultSchedulerTimers() SchedulerTimersConfig {
	return SchedulerTimersConfig{
		Ingest:  "*:0/4",
		Run:     "",
		Patrol:  "*:0/2",
		Journal: "*-*-* 23:50:00",
	}
}

func normalizeSchedulerTimers(timers *SchedulerTimersConfig) {
	if timers == nil {
		return
	}
	defaults := defaultSchedulerTimers()
	timers.Ingest = strings.TrimSpace(timers.Ingest)
	if timers.Ingest == "" {
		timers.Ingest = defaults.Ingest
	}
	timers.Run = strings.TrimSpace(timers.Run)
	timers.Patrol = strings.TrimSpace(timers.Patrol)
	if timers.Patrol == "" {
		timers.Patrol = defaults.Patrol
	}
	timers.Journal = strings.TrimSpace(timers.Journal)
	if timers.Journal == "" {
		timers.Journal = defaults.Journal
	}
}

func normalizeSchedulerLogs(logs *SchedulerLogsConfig, defaultArchiveRoot string) {
	if logs == nil {
		return
	}
	logs.Level = strings.ToLower(strings.TrimSpace(logs.Level))
	if logs.Level == "" {
		logs.Level = defaultSchedulerLogLevel
	}
	logs.ArchiveDir = ExpandPath(strings.TrimSpace(logs.ArchiveDir))
	if logs.ArchiveDir == "" {
		base := ExpandPath(strings.TrimSpace(defaultArchiveRoot))
		if base == "" {
			base = ExpandPath("~/.ccclaw/log")
		}
		logs.ArchiveDir = filepath.Join(base, "scheduler")
	}
	if logs.RetentionDays <= 0 {
		logs.RetentionDays = defaultSchedulerLogRetentionDays
	}
	if logs.MaxFiles <= 0 {
		logs.MaxFiles = defaultSchedulerLogMaxFiles
	}
}

func isSupportedSchedulerLogLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "emerg", "alert", "crit", "err", "error", "warning", "notice", "info", "debug":
		return true
	default:
		return false
	}
}

func normalizeSchedulerConfig(scheduler *SchedulerConfig) {
	if scheduler == nil {
		return
	}
	scheduler.Mode = strings.TrimSpace(scheduler.Mode)
	if scheduler.Mode == "" {
		scheduler.Mode = "none"
	}
	scheduler.SystemdUserDir = ExpandPath(scheduler.SystemdUserDir)
	if scheduler.SystemdUserDir == "" {
		scheduler.SystemdUserDir = ExpandPath("~/.config/systemd/user")
	}
	scheduler.CalendarTimezone = strings.TrimSpace(scheduler.CalendarTimezone)
	if scheduler.CalendarTimezone == "" {
		scheduler.CalendarTimezone = "Asia/Shanghai"
	}
	normalizeSchedulerTimers(&scheduler.Timers)
	normalizeSchedulerLogs(&scheduler.Logs, "")
}

func validateSchedulerConfig(scheduler SchedulerConfig) error {
	switch scheduler.Mode {
	case "auto", "systemd", "cron", "none":
	default:
		return fmt.Errorf("scheduler.mode 取值无效: %s", scheduler.Mode)
	}
	if scheduler.CalendarTimezone != "" {
		if _, err := time.LoadLocation(scheduler.CalendarTimezone); err != nil {
			return fmt.Errorf("scheduler.calendar_timezone 无效: %w", err)
		}
	}
	for key, value := range map[string]string{
		"ingest":  scheduler.Timers.Ingest,
		"patrol":  scheduler.Timers.Patrol,
		"journal": scheduler.Timers.Journal,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("scheduler.timers.%s 不能为空", key)
		}
	}
	if !isSupportedSchedulerLogLevel(scheduler.Logs.Level) {
		return fmt.Errorf("scheduler.logs.level 取值无效: %s", scheduler.Logs.Level)
	}
	if strings.TrimSpace(scheduler.Logs.ArchiveDir) == "" {
		return errors.New("scheduler.logs.archive_dir 不能为空")
	}
	if scheduler.Logs.RetentionDays <= 0 {
		return errors.New("scheduler.logs.retention_days 必须大于 0")
	}
	if scheduler.Logs.MaxFiles <= 0 {
		return errors.New("scheduler.logs.max_files 必须大于 0")
	}
	return nil
}

func normalizeApprovalWords(words []string) []string {
	normalized := make([]string, 0, len(words))
	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		candidate := strings.ToLower(strings.TrimSpace(word))
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		normalized = append(normalized, candidate)
	}
	return normalized
}

func normalizeExecutorMode(raw string, fallback ExecutorMode) ExecutorMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ExecutorModeTMux):
		return ExecutorModeTMux
	case string(ExecutorModeDaemon):
		return ExecutorModeDaemon
	default:
		return fallback
	}
}

func normalizeVarDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if strings.HasSuffix(strings.ToLower(path), ".db") {
		return filepath.Dir(path)
	}
	return path
}

func approvalSectionContainsLegacyCommand(content string) bool {
	_, changed, err := rewriteApprovalSection(content)
	return err == nil && changed
}

func rewriteApprovalSection(content string) (string, bool, error) {
	lines := strings.Split(content, "\n")
	start := -1
	end := len(lines)
	for idx, line := range lines {
		if isApprovalHeader(line) {
			start = idx
			break
		}
	}
	if start < 0 {
		return "", false, errors.New("未找到 [approval] 配置段")
	}
	for idx := start + 1; idx < len(lines); idx++ {
		if isSectionHeader(lines[idx]) {
			end = idx
			break
		}
	}

	section := lines[start:end]
	if !sectionHasLegacyCommand(section) {
		return content, false, nil
	}
	minimumPermission := sectionMinimumPermission(section)
	if minimumPermission == "" {
		minimumPermission = "maintain"
	}

	replacement := []string{
		"[approval]",
		fmt.Sprintf("minimum_permission = %q", minimumPermission),
		fmt.Sprintf("words = %s", tomlArrayLiteral(defaultApprovalWords())),
		fmt.Sprintf("reject_words = %s", tomlArrayLiteral(defaultRejectWords())),
	}

	updatedLines := make([]string, 0, len(lines)-len(section)+len(replacement))
	updatedLines = append(updatedLines, lines[:start]...)
	updatedLines = append(updatedLines, replacement...)
	updatedLines = append(updatedLines, lines[end:]...)
	return strings.Join(updatedLines, "\n"), true, nil
}

func rewriteLegacyStateDBPaths(content string) (string, bool, error) {
	lines := strings.Split(content, "\n")
	start := -1
	end := len(lines)
	for idx, line := range lines {
		if isPathsHeader(line) {
			start = idx
			break
		}
	}
	if start < 0 {
		return content, false, nil
	}
	for idx := start + 1; idx < len(lines); idx++ {
		if isSectionHeader(lines[idx]) {
			end = idx
			break
		}
	}

	section := lines[start:end]
	hasVarDir := false
	hasLegacyStateDB := false
	for _, line := range section {
		switch tomlKeyName(line) {
		case "var_dir":
			hasVarDir = true
		case "state_db":
			hasLegacyStateDB = true
		}
	}
	if !hasLegacyStateDB {
		return content, false, nil
	}

	replacement := make([]string, 0, len(section))
	changed := false
	for _, line := range section {
		switch tomlKeyName(line) {
		case "state_db":
			changed = true
			if hasVarDir {
				continue
			}
			rewritten, err := rewriteLegacyStateDBLine(line)
			if err != nil {
				return "", false, err
			}
			replacement = append(replacement, rewritten)
		default:
			replacement = append(replacement, line)
		}
	}
	if !changed {
		return content, false, nil
	}

	updatedLines := make([]string, 0, len(lines)-len(section)+len(replacement))
	updatedLines = append(updatedLines, lines[:start]...)
	updatedLines = append(updatedLines, replacement...)
	updatedLines = append(updatedLines, lines[end:]...)
	return strings.Join(updatedLines, "\n"), true, nil
}

func rewriteLegacyStateDBLine(line string) (string, error) {
	indentWidth := len(line) - len(strings.TrimLeft(line, " \t"))
	indent := line[:indentWidth]
	trimmed := strings.TrimSpace(line)
	sep := strings.Index(trimmed, "=")
	if sep < 0 {
		return "", fmt.Errorf("解析 paths.state_db 失败: %s", line)
	}
	value, quote, suffix, err := parseTomlStringValue(trimmed[sep+1:])
	if err != nil {
		return "", fmt.Errorf("解析 paths.state_db 失败: %w", err)
	}
	renderedValue := renderTomlStringValue(normalizeVarDir(value), quote)
	return indent + "var_dir = " + renderedValue + suffix, nil
}

func parseTomlStringValue(raw string) (value, quote, suffix string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", errors.New("值为空")
	}
	switch raw[0] {
	case '"', '\'':
		quote = string(raw[0])
		end := strings.IndexByte(raw[1:], raw[0])
		if end < 0 {
			return "", "", "", errors.New("字符串缺少闭合引号")
		}
		end++
		return raw[1:end], quote, raw[end+1:], nil
	default:
		value = strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if value == "" {
			return "", "", "", errors.New("值为空")
		}
		commentIdx := len(value)
		return value, "", raw[commentIdx:], nil
	}
}

func renderTomlStringValue(value, quote string) string {
	if quote == "'" {
		return "'" + value + "'"
	}
	return fmt.Sprintf("%q", value)
}

func tomlKeyName(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	sep := strings.Index(trimmed, "=")
	if sep < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(trimmed[:sep]))
}

func rewriteSchedulerSection(content string, scheduler SchedulerConfig) (string, bool, error) {
	lines := strings.Split(content, "\n")
	start := -1
	end := len(lines)
	for idx, line := range lines {
		if isSchedulerHeader(line) {
			start = idx
			break
		}
	}
	replacement := renderSchedulerSection(scheduler)
	if start < 0 {
		updated := strings.TrimRight(content, "\n")
		if updated != "" {
			updated += "\n\n"
		}
		updated += strings.Join(replacement, "\n")
		return updated + "\n", true, nil
	}
	for idx := start + 1; idx < len(lines); idx++ {
		if isSchedulerScopedHeader(lines[idx]) {
			continue
		}
		if isAnyTableHeader(lines[idx]) {
			end = idx
			break
		}
	}
	updatedLines := make([]string, 0, len(lines)-len(lines[start:end])+len(replacement))
	updatedLines = append(updatedLines, lines[:start]...)
	updatedLines = append(updatedLines, replacement...)
	updatedLines = append(updatedLines, lines[end:]...)
	updated := strings.Join(updatedLines, "\n")
	return updated, updated != content, nil
}

func isApprovalHeader(line string) bool {
	return strings.EqualFold(strings.TrimSpace(line), "[approval]")
}

func isPathsHeader(line string) bool {
	return strings.EqualFold(strings.TrimSpace(line), "[paths]")
}

func isSectionHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
}

func isAnyTableHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
}

func isSchedulerHeader(line string) bool {
	return strings.EqualFold(strings.TrimSpace(line), "[scheduler]")
}

func isSchedulerScopedHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.EqualFold(trimmed, "[scheduler]") ||
		strings.EqualFold(trimmed, "[scheduler.timers]") ||
		strings.EqualFold(trimmed, "[scheduler.logs]")
}

func sectionHasLegacyCommand(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "command") {
			return true
		}
	}
	return false
}

func sectionMinimumPermission(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), "minimum_permission") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	}
	return ""
}

func tomlArrayLiteral(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func renderSchedulerSection(scheduler SchedulerConfig) []string {
	normalizeSchedulerConfig(&scheduler)
	return []string{
		"# 调度策略配置：",
		"# - mode: 安装时请求的调度模式 auto|systemd|cron|none",
		"# - systemd_user_dir: user systemd 单元写入目录",
		"# - calendar_timezone: systemd timer 日程解释时区；默认 Asia/Shanghai",
		"[scheduler]",
		fmt.Sprintf("mode = %q", scheduler.Mode),
		fmt.Sprintf("systemd_user_dir = %q", scheduler.SystemdUserDir),
		fmt.Sprintf("calendar_timezone = %q", scheduler.CalendarTimezone),
		"",
		"# systemd timer 周期，采用 systemd OnCalendar 语法。",
		"# - `*-*-* 23:50:00` 表示“每年-每月-每日 23:50:00”",
		"# - 若要配置凌晨 01:01:42，可写为 `*-*-* 01:01:42`",
		"# - 若表达式未显式附带时区，运行时会追加 scheduler.calendar_timezone",
		"[scheduler.timers]",
		fmt.Sprintf("ingest = %q", scheduler.Timers.Ingest),
		fmt.Sprintf("patrol = %q", scheduler.Timers.Patrol),
		fmt.Sprintf("journal = %q", scheduler.Timers.Journal),
		"",
		"# 调度日志级别：",
		"# - level: 运行态 `ingest/patrol/journal` 共享的日志阈值，同时也是 `ccclaw scheduler logs` 默认过滤",
		"# - 运行态统一归一为 debug|info|warning|error；兼容历史别名 emerg|alert|crit|err|notice",
		"# - `error` 在 journalctl 查询时会自动映射为 `err`",
		"# - archive_dir: `ccclaw scheduler logs --archive` 默认归档目录",
		"# - retention_days: 只清理带 ccclaw 归档头的受管文件；超过 N 天的旧归档会删除",
		"# - max_files: 只统计受管归档文件；超过上限时删除最旧文件，不影响人工文件",
		"# - compress: 新归档保留明文，历史受管 `.log` 自动压缩为 `.log.gz`",
		"[scheduler.logs]",
		fmt.Sprintf("level = %q", scheduler.Logs.Level),
		fmt.Sprintf("archive_dir = %q", scheduler.Logs.ArchiveDir),
		fmt.Sprintf("retention_days = %d", scheduler.Logs.RetentionDays),
		fmt.Sprintf("max_files = %d", scheduler.Logs.MaxFiles),
		fmt.Sprintf("compress = %t", scheduler.Logs.Compress),
		"",
	}
}

func renderAnnotatedConfig(cfg *Config) string {
	var buf bytes.Buffer
	buf.WriteString("# default_target:\n")
	buf.WriteString("# - 留空时，运行时必须依赖 Issue body 中的 target_repo: owner/repo\n")
	buf.WriteString("# - 仅在存在多个 [[targets]] 时建议显式指定默认值\n")
	buf.WriteString(fmt.Sprintf("default_target = %q\n\n", cfg.DefaultTarget))

	buf.WriteString("# GitHub 控制面配置。\n")
	buf.WriteString("# control_repo 固定指向官方控制仓库，不接受运行时自定义。\n")
	buf.WriteString("[github]\n")
	buf.WriteString(fmt.Sprintf("control_repo = %q\n", cfg.GitHub.ControlRepo))
	buf.WriteString(fmt.Sprintf("issue_label = %q\n", cfg.GitHub.IssueLabel))
	buf.WriteString("# ingest 每轮会在 control_repo 与所有启用 target repo 中分别拉取 open issues。\n")
	buf.WriteString("# - 只统计匹配 issue_label 的 Issue\n")
	buf.WriteString("# - 不是并发数；实际执行改由 ingest 按仓槽位推进\n")
	buf.WriteString(fmt.Sprintf("limit = %d\n\n", cfg.GitHub.Limit))

	buf.WriteString("# 固定路径边界：\n")
	buf.WriteString("# - app_dir: 程序树\n")
	buf.WriteString("# - home_repo: 知识仓库\n")
	buf.WriteString("# - var_dir: 运行态状态目录，统一承载 state.json 与 JSONL 事实产物\n")
	buf.WriteString("# - kb_dir: 默认知识库目录\n")
	buf.WriteString("# - env_file: 所有敏感信息唯一入口\n")
	buf.WriteString("[paths]\n")
	buf.WriteString(fmt.Sprintf("app_dir = %q\n", cfg.Paths.AppDir))
	buf.WriteString(fmt.Sprintf("home_repo = %q\n", cfg.Paths.HomeRepo))
	buf.WriteString(fmt.Sprintf("var_dir = %q\n", cfg.Paths.VarDir))
	buf.WriteString(fmt.Sprintf("log_dir = %q\n", cfg.Paths.LogDir))
	buf.WriteString(fmt.Sprintf("kb_dir = %q\n", cfg.Paths.KBDir))
	buf.WriteString(fmt.Sprintf("env_file = %q\n\n", cfg.Paths.EnvFile))

	buf.WriteString("# 执行器默认走 ccclaude 包装器；当前包装器默认直连 claude，不再拼接 rtk proxy。\n")
	buf.WriteString("# mode: 默认 daemon；可按仓配置 executor_mode=tmux 用于 debug attach。\n")
	buf.WriteString("[executor]\n")
	buf.WriteString(fmt.Sprintf("provider = %q\n", cfg.Executor.Provider))
	buf.WriteString(fmt.Sprintf("binary = %q\n", cfg.Executor.Binary))
	buf.WriteString(fmt.Sprintf("command = %s\n", tomlArrayLiteral(cfg.Executor.Command)))
	buf.WriteString(fmt.Sprintf("timeout = %q\n", cfg.Executor.Timeout))
	buf.WriteString(fmt.Sprintf("mode = %q\n\n", cfg.ExecutorMode()))

	buf.WriteString(strings.Join(renderSchedulerSection(cfg.Scheduler), "\n"))
	buf.WriteString("\n")

	buf.WriteString("# maintain 及以上权限的 Issue 自动执行；其他情况需要受信任成员评论 /ccclaw + 同义词。\n")
	buf.WriteString("# 若 issue_label 被移除，任务会转为 BLOCKED，待重新打标签后再恢复。\n")
	buf.WriteString("[approval]\n")
	buf.WriteString(fmt.Sprintf("minimum_permission = %q\n", cfg.Approval.MinimumPermission))
	buf.WriteString(fmt.Sprintf("words = %s\n", tomlArrayLiteral(cfg.Approval.Words)))
	buf.WriteString(fmt.Sprintf("reject_words = %s\n", tomlArrayLiteral(cfg.Approval.RejectWords)))

	if len(cfg.Targets) == 0 {
		buf.WriteString("\n# 任务仓库样例：\n")
		buf.WriteString("# [[targets]]\n")
		buf.WriteString("# repo = \"owner/repo\"\n")
		buf.WriteString("# local_path = \"/opt/src/3claw/owner/repo\"\n")
		buf.WriteString(fmt.Sprintf("# kb_path = %q\n", cfg.Paths.KBDir))
		buf.WriteString("# executor_mode = \"tmux\" # 可选: tmux|daemon，留空继承 [executor].mode\n")
		buf.WriteString("# disabled = false\n")
		return buf.String()
	}

	for _, target := range cfg.Targets {
		buf.WriteString("\n\n[[targets]]\n")
		buf.WriteString(fmt.Sprintf("repo = %q\n", target.Repo))
		buf.WriteString(fmt.Sprintf("local_path = %q\n", target.LocalPath))
		buf.WriteString(fmt.Sprintf("kb_path = %q\n", target.KBPath))
		if strings.TrimSpace(target.ExecutorMode) != "" {
			buf.WriteString(fmt.Sprintf("executor_mode = %q\n", target.ExecutorMode))
		}
		if target.Disabled {
			buf.WriteString("disabled = true\n")
		}
	}
	return buf.String() + "\n"
}
