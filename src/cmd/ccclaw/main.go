package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/41490/ccclaw/internal/app"
	"github.com/41490/ccclaw/internal/buildinfo"
	"github.com/41490/ccclaw/internal/claude"
	"github.com/41490/ccclaw/internal/config"
	"github.com/41490/ccclaw/internal/scheduler"
	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var configPath string
	var envFile string
	var runLimit int
	var showVersion bool
	var showRTKComparison bool
	var statsFrom string
	var statsTo string
	var statsDaily bool
	var statsLimit int
	var journalDate string
	var schedulerLogsFollow bool
	var schedulerLogsSince string
	var schedulerLogsLines int
	var schedulerLogsLevel string
	var schedulerLogsArchive bool
	var statusJSON bool
	var schedulerStatusJSON bool
	var schedulerDoctorJSON bool
	var schedulerTimersWide bool
	var schedulerTimersRaw bool
	var schedulerTimersJSON bool
	var runtimeLogLevel string

	rootCmd := &cobra.Command{
		Use:           "ccclaw",
		Short:         "ccclaw 长期异步任务执行器",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Short())
				return nil
			}
			return cmd.Help()
		},
	}

	rootCmd.PersistentFlags().StringVar(&configPath, "config", defaultConfigPath(), "TOML 配置文件路径")
	rootCmd.PersistentFlags().StringVar(&envFile, "env-file", defaultEnvFilePath(), "固定 .env 隐私配置文件路径")
	rootCmd.PersistentFlags().StringVar(&runtimeLogLevel, "log-level", "", "临时覆盖运行态日志级别: debug|info|warning|error")
	rootCmd.Flags().BoolVarP(&showVersion, "version", "V", false, "显示版本")
	addArchiveCommand(rootCmd, &configPath)
	addSevolverCommand(rootCmd, &configPath, &envFile)
	addRecallCommand(rootCmd, &configPath, &envFile)
	addClaudeHookCommand(rootCmd, &configPath)

	newRuntime := func(cmd *cobra.Command) (*app.Runtime, error) {
		return app.NewRuntimeWithOptions(configPath, envFile, app.RuntimeOptions{
			LogWriter:        cmd.ErrOrStderr(),
			LogLevelOverride: runtimeLogLevel,
		})
	}

	rootCmd.AddCommand(&cobra.Command{
		Use:   "ingest",
		Short: "拉取并入队 Issue 任务",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			return rt.Ingest(cmd.Context())
		},
	})

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "执行待处理任务",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			return rt.Run(cmd.Context(), cmd.OutOrStdout(), runLimit)
		},
	}
	runCmd.Flags().IntVar(&runLimit, "limit", 10, "本轮最多执行任务数")
	rootCmd.AddCommand(runCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "查看当前运行态快照",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			return rt.StatusWithOptions(cmd.OutOrStdout(), app.StatusOptions{JSON: statusJSON})
		},
	}
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "输出结构化 JSON，便于脚本消费")
	rootCmd.AddCommand(statusCmd)

	statsCmd := &cobra.Command{
		Use:   "stats",
		Short: "查看 token 使用统计",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			options, err := parseStatsOptions(statsFrom, statsTo, statsDaily, showRTKComparison, statsLimit)
			if err != nil {
				return err
			}
			return rt.StatsWithOptions(cmd.OutOrStdout(), options)
		},
	}
	statsCmd.Flags().StringVar(&statsFrom, "from", "", "按 YYYY-MM-DD 指定统计起始日期(含当日)")
	statsCmd.Flags().StringVar(&statsTo, "to", "", "按 YYYY-MM-DD 指定统计截止日期(含当日)")
	statsCmd.Flags().BoolVar(&statsDaily, "daily", false, "按天输出聚合统计")
	statsCmd.Flags().IntVar(&statsLimit, "limit", 20, "限制任务明细与 daily 视图输出规模")
	statsCmd.Flags().BoolVar(&showRTKComparison, "rtk-comparison", false, "显示 rtk 与非 rtk 的对比统计")
	rootCmd.AddCommand(statsCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "patrol",
		Short: "巡查 tmux 会话与运行中任务",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			return rt.Patrol(cmd.Context(), cmd.OutOrStdout())
		},
	})

	journalCmd := &cobra.Command{
		Use:   "journal",
		Short: "生成指定日期的 journal 日报",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			day := time.Now()
			if journalDate != "" {
				parsed, err := time.ParseInLocation("2006-01-02", journalDate, time.Local)
				if err != nil {
					return fmt.Errorf("解析 --date 失败: %w", err)
				}
				day = parsed
			}
			return rt.Journal(day, cmd.OutOrStdout())
		},
	}
	journalCmd.Flags().StringVar(&journalDate, "date", "", "按 YYYY-MM-DD 生成指定日期 journal")
	rootCmd.AddCommand(journalCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "执行环境与部署健康检查",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			return rt.Doctor(cmd.Context(), cmd.OutOrStdout())
		},
	})

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "校验并展示当前配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			return rt.ShowConfig(cmd.OutOrStdout())
		},
	}
	configCmd.AddCommand(&cobra.Command{
		Use:   "migrate",
		Short: "补齐缺失配置并迁移已废弃字段",
		RunE: func(cmd *cobra.Command, args []string) error {
			changed, err := config.Migrate(configPath)
			if err != nil {
				return err
			}
			if !changed {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "当前配置无需迁移")
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "已迁移配置: %s\n", configPath)
			return nil
		},
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "migrate-approval",
		Short: "将旧 approval.command 配置迁移为 words/reject_words",
		RunE: func(cmd *cobra.Command, args []string) error {
			changed, err := config.MigrateLegacyApproval(configPath)
			if err != nil {
				return err
			}
			if !changed {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "当前配置无需迁移 approval 门禁")
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "已迁移审批配置: %s\n", configPath)
			return nil
		},
	})
	var schedulerMode string
	var schedulerUserDir string
	var schedulerCalendarTimezone string
	var schedulerIngestCalendar string
	var schedulerPatrolCalendar string
	var schedulerJournalCalendar string
	configSetSchedulerCmd := &cobra.Command{
		Use:   "set-scheduler",
		Short: "更新调度器配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			cfg.Scheduler.Mode = schedulerMode
			if schedulerUserDir != "" {
				cfg.Scheduler.SystemdUserDir = schedulerUserDir
			}
			if schedulerCalendarTimezone != "" {
				cfg.Scheduler.CalendarTimezone = schedulerCalendarTimezone
			}
			if schedulerIngestCalendar != "" {
				cfg.Scheduler.Timers.Ingest = schedulerIngestCalendar
			}
			if schedulerPatrolCalendar != "" {
				cfg.Scheduler.Timers.Patrol = schedulerPatrolCalendar
			}
			if schedulerJournalCalendar != "" {
				cfg.Scheduler.Timers.Journal = schedulerJournalCalendar
			}
			cfg.NormalizePaths()
			if err := config.UpdateSchedulerSection(configPath, cfg.Scheduler); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "已更新调度配置: mode=%s systemd_user_dir=%s calendar_timezone=%s\n", cfg.Scheduler.Mode, cfg.Scheduler.SystemdUserDir, cfg.Scheduler.CalendarTimezone)
			return nil
		},
	}
	configSetSchedulerCmd.Flags().StringVar(&schedulerMode, "mode", "", "调度器模式: auto|systemd|cron|none")
	configSetSchedulerCmd.Flags().StringVar(&schedulerUserDir, "systemd-user-dir", "", "user systemd 单元目录")
	configSetSchedulerCmd.Flags().StringVar(&schedulerCalendarTimezone, "calendar-timezone", "", "systemd timer 日程解释时区，默认 Asia/Shanghai")
	configSetSchedulerCmd.Flags().StringVar(&schedulerIngestCalendar, "ingest-calendar", "", "ingest timer 的 OnCalendar 表达式")
	configSetSchedulerCmd.Flags().StringVar(&schedulerPatrolCalendar, "patrol-calendar", "", "patrol timer 的 OnCalendar 表达式")
	configSetSchedulerCmd.Flags().StringVar(&schedulerJournalCalendar, "journal-calendar", "", "journal timer 的 OnCalendar 表达式")
	_ = configSetSchedulerCmd.MarkFlagRequired("mode")
	configCmd.AddCommand(configSetSchedulerCmd)
	rootCmd.AddCommand(configCmd)

	schedulerCmd := &cobra.Command{
		Use:   "scheduler",
		Short: "管理调度器后端",
	}
	schedulerStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "单独查看当前调度器状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return scheduler.RenderStatus(cfg, cmd.OutOrStdout(), schedulerStatusJSON)
		},
	}
	schedulerStatusCmd.Flags().BoolVar(&schedulerStatusJSON, "json", false, "输出结构化 JSON，便于脚本消费")
	schedulerCmd.AddCommand(schedulerStatusCmd)
	schedulerDoctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "单独检查调度后端、timer 与日志运维能力",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return scheduler.Doctor(cmd.Context(), cfg, cmd.OutOrStdout(), schedulerDoctorJSON)
		},
	}
	schedulerDoctorCmd.Flags().BoolVar(&schedulerDoctorJSON, "json", false, "输出结构化 JSON，便于脚本消费")
	schedulerCmd.AddCommand(schedulerDoctorCmd)
	schedulerTimersCmd := &cobra.Command{
		Use:   "timers",
		Short: "查看 ccclaw user systemd timers 状态与下一次触发时间",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			view, err := scheduler.ResolveTimersView(schedulerTimersWide, schedulerTimersRaw, schedulerTimersJSON)
			if err != nil {
				return err
			}
			return scheduler.RenderManagedTimers(cmd.Context(), cfg, cmd.OutOrStdout(), scheduler.TimersRenderOptions{View: view})
		},
	}
	schedulerTimersCmd.Flags().BoolVar(&schedulerTimersWide, "wide", false, "显示完整宽表，包含原始日程与双时区触发时间")
	schedulerTimersCmd.Flags().BoolVar(&schedulerTimersRaw, "raw", false, "显示原始字段名的 key=value 视图")
	schedulerTimersCmd.Flags().BoolVar(&schedulerTimersJSON, "json", false, "输出结构化 JSON，便于脚本消费")
	schedulerCmd.AddCommand(schedulerTimersCmd)
	schedulerLogsCmd := &cobra.Command{
		Use:   "logs [all|ingest|run|patrol|journal|archive|sevolver]",
		Short: "查看或追随 ccclaw user systemd 服务日志",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			scope := "all"
			if len(args) == 1 {
				scope = args[0]
			}
			level := schedulerLogsLevel
			if level == "" {
				level = cfg.Scheduler.Logs.Level
			}
			archivePath := ""
			if schedulerLogsArchive {
				archivePath = scheduler.BuildLogArchivePath(cfg.Scheduler.Logs.ArchiveDir, scope, time.Now())
			}
			if err := scheduler.StreamLogs(cmd.Context(), scheduler.LogsOptions{
				Scope:       scope,
				Follow:      schedulerLogsFollow,
				Since:       schedulerLogsSince,
				Lines:       schedulerLogsLines,
				Level:       level,
				ArchivePath: archivePath,
				ArchivePolicy: scheduler.LogArchivePolicy{
					RetentionDays: cfg.Scheduler.Logs.RetentionDays,
					MaxFiles:      cfg.Scheduler.Logs.MaxFiles,
					Compress:      cfg.Scheduler.Logs.Compress,
				},
			}, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}
			if archivePath != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "日志已归档: %s\n", archivePath)
			}
			return nil
		},
	}
	schedulerLogsCmd.Flags().BoolVarP(&schedulerLogsFollow, "follow", "f", false, "持续追随日志输出")
	schedulerLogsCmd.Flags().StringVar(&schedulerLogsSince, "since", "", "仅显示指定时间之后的日志，如 '1 hour ago'")
	schedulerLogsCmd.Flags().IntVar(&schedulerLogsLines, "lines", 50, "默认显示最近多少行日志")
	schedulerLogsCmd.Flags().StringVar(&schedulerLogsLevel, "level", "", "journal 优先级过滤: emerg|alert|crit|err|warning|notice|info|debug|error")
	schedulerLogsCmd.Flags().BoolVar(&schedulerLogsArchive, "archive", false, "将本次日志输出同步归档到 scheduler.logs.archive_dir")
	schedulerCmd.AddCommand(schedulerLogsCmd)
	schedulerCmd.AddCommand(&cobra.Command{
		Use:   "enable-cron",
		Short: "写入或更新当前用户的受控 crontab 规则",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			message, err := scheduler.EnableCron(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), message)
			return nil
		},
	})
	schedulerCmd.AddCommand(&cobra.Command{
		Use:   "disable-cron",
		Short: "只清理当前用户 crontab 中的 ccclaw 受控规则",
		RunE: func(cmd *cobra.Command, args []string) error {
			message, err := scheduler.DisableCron(cmd.Context())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), message)
			return nil
		},
	})
	useSchedulerCmd := &cobra.Command{
		Use:   "use MODE",
		Short: "统一切换调度后端并同步配置",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			result, err := scheduler.Use(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			if err := config.UpdateSchedulerSection(configPath, cfg.Scheduler); err != nil {
				return err
			}
			for _, step := range result.Steps {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), step)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "已切换调度后端: mode=%s\n", result.Mode)
			return nil
		},
	}
	schedulerCmd.AddCommand(useSchedulerCmd)
	rootCmd.AddCommand(schedulerCmd)

	targetCmd := &cobra.Command{
		Use:   "target",
		Short: "管理目标仓库绑定",
	}

	targetCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "列出当前 target 配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if len(cfg.Targets) == 0 {
				_, _ = fmt.Fprintln(os.Stdout, "当前未绑定任何 target")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "REPO\tSTATUS\tDEFAULT\tLOCAL_PATH\tKB_PATH")
			for _, target := range cfg.Targets {
				status := "enabled"
				if target.Disabled {
					status = "disabled"
				}
				isDefault := ""
				if cfg.DefaultTarget == target.Repo {
					isDefault = "*"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", target.Repo, status, isDefault, target.LocalPath, target.KBPath)
			}
			return w.Flush()
		},
	})

	var addRepo string
	var addPath string
	var addKBPath string
	var makeDefault bool
	targetAddCmd := &cobra.Command{
		Use:   "add",
		Short: "追加或更新一个 target",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if err := cfg.UpsertTarget(config.TargetConfig{
				Repo:      addRepo,
				LocalPath: addPath,
				KBPath:    addKBPath,
			}, makeDefault); err != nil {
				return err
			}
			return config.Save(configPath, cfg)
		},
	}
	targetAddCmd.Flags().StringVar(&addRepo, "repo", "", "target 仓库 owner/repo")
	targetAddCmd.Flags().StringVar(&addPath, "path", "", "target 本地路径")
	targetAddCmd.Flags().StringVar(&addKBPath, "kb-path", "", "target 对应 kb 路径，默认继承全局 kb_dir")
	targetAddCmd.Flags().BoolVar(&makeDefault, "default", false, "设为默认 target")
	_ = targetAddCmd.MarkFlagRequired("repo")
	_ = targetAddCmd.MarkFlagRequired("path")
	targetCmd.AddCommand(targetAddCmd)

	var disableRepo string
	targetDisableCmd := &cobra.Command{
		Use:   "disable",
		Short: "禁用一个 target",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if err := cfg.DisableTarget(disableRepo); err != nil {
				return err
			}
			return config.Save(configPath, cfg)
		},
	}
	targetDisableCmd.Flags().StringVar(&disableRepo, "repo", "", "要禁用的 target 仓库 owner/repo")
	_ = targetDisableCmd.MarkFlagRequired("repo")
	targetCmd.AddCommand(targetDisableCmd)
	rootCmd.AddCommand(targetCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "显示版本",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Short())
		},
	})

	return rootCmd
}

func addClaudeHookCommand(rootCmd *cobra.Command, configPath *string) {
	hookCmd := &cobra.Command{
		Use:    "claude-hook EVENT",
		Short:  "处理 Claude hook 回调",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("读取 Claude hook stdin 失败: %w", err)
			}
			currentConfigPath := ""
			if configPath != nil {
				currentConfigPath = *configPath
			}
			return handleClaudeHook(args[0], payload, currentConfigPath)
		},
	}
	rootCmd.AddCommand(hookCmd)
}

func handleClaudeHook(eventArg string, payload []byte, configPath string) error {
	event, err := parseClaudeHookEvent(eventArg)
	if err != nil {
		return err
	}
	var hookPayload claude.HookPayload
	if len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, &hookPayload); err != nil {
			return fmt.Errorf("解析 Claude hook payload 失败: %w", err)
		}
	}
	hookPayload.HookEventName = event
	taskID := strings.TrimSpace(os.Getenv("CCCLAW_TASK_ID"))
	if taskID == "" {
		// 交互式会话没有受管 task 上下文时，hook 应静默跳过。
		return nil
	}
	stateDir, err := resolveClaudeHookStateDir(configPath)
	if err != nil {
		return err
	}
	if stateDir == "" {
		return fmt.Errorf("CCCLAW_TASK_ID 已设置但缺少可用的 Claude hook 状态目录")
	}
	return claude.HandleHook(stateDir, taskID, hookPayload)
}

func parseClaudeHookEvent(eventArg string) (string, error) {
	switch strings.TrimSpace(eventArg) {
	case "session-start":
		return claude.HookEventSessionStart, nil
	case "pre-compact":
		return claude.HookEventPreCompact, nil
	case "stop":
		return claude.HookEventStop, nil
	default:
		return "", fmt.Errorf("未知 Claude hook 事件: %s", eventArg)
	}
}

func resolveClaudeHookStateDir(configPath string) (string, error) {
	if stateDir := strings.TrimSpace(os.Getenv("CCCLAW_HOOK_STATE_DIR")); stateDir != "" {
		return stateDir, nil
	}
	if appDir := strings.TrimSpace(os.Getenv("CCCLAW_APP_DIR")); appDir != "" {
		return claude.DefaultHookStateDir(appDir), nil
	}
	path := strings.TrimSpace(configPath)
	if path == "" {
		path = defaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return "", fmt.Errorf("加载 Claude hook 配置失败: %w", err)
	}
	return claude.DefaultHookStateDir(cfg.Paths.AppDir), nil
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".ccclaw", "ops", "config", "config.toml"),
		filepath.Join("ops", "config", "config.toml"),
		filepath.Join("ops", "config", "config.example.toml"),
		filepath.Join("dist", "ops", "config", "config.example.toml"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if home != "" {
		return filepath.Join(home, ".ccclaw", "ops", "config", "config.toml")
	}
	return filepath.Join("ops", "config", "config.toml")
}

func parseStatsOptions(from, to string, daily, showRTKComparison bool, limit int) (app.StatsOptions, error) {
	options := app.StatsOptions{
		Daily:             daily,
		ShowRTKComparison: showRTKComparison,
		Limit:             limit,
	}
	if options.Limit <= 0 {
		return options, fmt.Errorf("--limit 必须大于 0")
	}
	if from != "" {
		parsed, err := time.ParseInLocation("2006-01-02", from, time.Local)
		if err != nil {
			return options, fmt.Errorf("解析 --from 失败: %w", err)
		}
		options.Start = parsed
	}
	if to != "" {
		parsed, err := time.ParseInLocation("2006-01-02", to, time.Local)
		if err != nil {
			return options, fmt.Errorf("解析 --to 失败: %w", err)
		}
		options.End = parsed.Add(24 * time.Hour)
	}
	if !options.Start.IsZero() && !options.End.IsZero() && !options.Start.Before(options.End) {
		return options, fmt.Errorf("--from 必须早于或等于 --to")
	}
	return options, nil
}

func defaultEnvFilePath() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".ccclaw", ".env"),
		".env",
		filepath.Join("dist", ".env"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if home != "" {
		return filepath.Join(home, ".ccclaw", ".env")
	}
	return ".env"
}
