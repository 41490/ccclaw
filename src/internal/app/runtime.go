package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/41490/ccclaw/internal/adapters/github"
	"github.com/41490/ccclaw/internal/adapters/reporter"
	"github.com/41490/ccclaw/internal/adapters/storage"
	"github.com/41490/ccclaw/internal/claude"
	"github.com/41490/ccclaw/internal/config"
	"github.com/41490/ccclaw/internal/core"
	"github.com/41490/ccclaw/internal/executor"
	"github.com/41490/ccclaw/internal/logging"
	"github.com/41490/ccclaw/internal/memory"
	"github.com/41490/ccclaw/internal/scheduler"
	"github.com/41490/ccclaw/internal/tmux"
	"github.com/41490/ccclaw/internal/vcs"
)

type Runtime struct {
	cfg      *config.Config
	secrets  *config.Secrets
	store    *storage.Store
	rep      *reporter.Reporter
	mem      *memory.Index
	memRoot  string
	memCache map[string]*memory.Index
	ghCache  map[string]*github.Client
	syncRepo func(repoPath, message string, paths []string, maxRetry int) error
	log      *logging.Logger
	logLevel string
}

const (
	claudeStateMissingCLI     = "missing_cli"
	claudeStateInstallBlocked = "install_channel_blocked"
	claudeStateInstalledBare  = "installed_unconfigured"
	claudeStateInstalledSetup = "installed_setup_token_ready"
	claudeStateInstalledProxy = "installed_proxy_ready"
	claudeInstallScriptURL    = "https://claude.ai/install.sh"
	issueTargetRepoPattern    = `(?mi)^\s*target_repo\s*:\s*([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)\s*$`
	manualSudoGuide           = "sudo 无口令未开启，请按文档手工执行 `sudo visudo` 并为当前用户配置 `NOPASSWD` 规则"
	contextRestartThreshold   = 64.0
	maxContextRestarts        = 2
)

type StatsOptions struct {
	Start             time.Time
	End               time.Time
	Daily             bool
	ShowRTKComparison bool
	Limit             int
}

type StatusOptions struct {
	JSON bool
}

const (
	defaultStatsLimit      = 20
	journalManagedStartTag = "<!-- ccclaw:managed:start -->"
	journalManagedEndTag   = "<!-- ccclaw:managed:end -->"
	journalUserStartTag    = "<!-- ccclaw:user:start -->"
	journalUserEndTag      = "<!-- ccclaw:user:end -->"
	journalUserPlaceholder = "<!-- 本区块留给本机人工补充；自动更新时会保留这里的内容。 -->"
)

var sevolverObservedTaskClasses = []core.TaskClass{
	core.TaskClassSevolver,
	core.TaskClassSevolverDeepAnalysis,
}

func NewRuntime(configPath, envFile string) (*Runtime, error) {
	return NewRuntimeWithOptions(configPath, envFile, RuntimeOptions{})
}

func (rt *Runtime) clientForRepo(repo string) *github.Client {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		repo = rt.cfg.GitHub.ControlRepo
	}
	if client, ok := rt.ghCache[repo]; ok {
		return client
	}
	values := map[string]string{}
	if rt.secrets != nil {
		values = rt.secrets.Values
	}
	client := github.NewClient(repo, values)
	rt.ghCache[repo] = client
	return client
}

func (rt *Runtime) repoSync(repoPath, message string, paths []string, maxRetry int) error {
	if rt != nil && rt.syncRepo != nil {
		return rt.syncRepo(repoPath, message, paths, maxRetry)
	}
	return vcs.SyncRepo(repoPath, message, paths, maxRetry)
}

func (rt *Runtime) issueSourceRepos() []string {
	repos := make([]string, 0, len(rt.cfg.Targets)+1)
	seen := map[string]struct{}{}
	appendRepo := func(repo string) {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return
		}
		if _, ok := seen[repo]; ok {
			return
		}
		seen[repo] = struct{}{}
		repos = append(repos, repo)
	}
	appendRepo(rt.cfg.GitHub.ControlRepo)
	for _, target := range rt.cfg.EnabledTargets() {
		appendRepo(target.Repo)
	}
	return repos
}

func (rt *Runtime) Ingest(ctx context.Context) error {
	defer rt.store.Close()
	repos := rt.issueSourceRepos()
	rt.logInfo("ingest", "开始同步 open issues", "repo_count", len(repos), "log_level", rt.runtimeLogLevel())
	seen := make(map[string]struct{})
	for _, repo := range repos {
		issues, err := rt.clientForRepo(repo).ListOpenIssues(rt.cfg.GitHub.IssueLabel, rt.cfg.GitHub.Limit)
		if err != nil {
			rt.logError("ingest", "拉取 open issues 失败", "repo", repo, "error", err)
			return err
		}
		rt.logDebug("ingest", "读取 open issues", "repo", repo, "count", len(issues))
		for _, issue := range issues {
			if err := rt.syncIssue(ctx, issue, false); err != nil {
				rt.logError("ingest", "同步 issue 失败", "issue", rt.issueRef(issue.Repo, issue.Number), "error", err)
				return err
			}
			seen[core.TaskID(issue.Repo, issue.Number)] = struct{}{}
		}
	}

	tasks, err := rt.store.ListTasks()
	if err != nil {
		rt.logError("ingest", "读取任务列表失败", "error", err)
		return err
	}
	observedRepos := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		observedRepos[repo] = struct{}{}
	}
	revisited := 0
	for _, task := range tasks {
		if task == nil || task.State == core.StateDone || task.State == core.StateDead {
			continue
		}
		if _, ok := observedRepos[strings.TrimSpace(task.IssueRepo)]; !ok {
			continue
		}
		if _, ok := seen[task.TaskID]; ok {
			continue
		}
		issue, err := rt.clientForRepo(task.IssueRepo).GetIssue(task.IssueNumber)
		if err != nil {
			rt.logError("ingest", "回查 issue 失败", "issue", rt.issueRef(task.IssueRepo, task.IssueNumber), "error", err)
			return err
		}
		if err := rt.syncIssue(ctx, *issue, false); err != nil {
			rt.logError("ingest", "回查同步 issue 失败", "issue", rt.issueRef(issue.Repo, issue.Number), "error", err)
			return err
		}
		revisited++
	}
	rt.logInfo("ingest", "同步完成，准备执行按仓调度循环", "visible_tasks", len(seen), "revisited", revisited)
	return rt.runIngestCycle(ctx, nil, false)
}

func (rt *Runtime) Run(ctx context.Context, out io.Writer, limit int) error {
	defer rt.store.Close()
	rt.logInfo("run", "兼容入口已转发到按仓 ingest 调度", "limit", limit)
	return rt.runIngestCycle(ctx, out, true)
}

func (rt *Runtime) Patrol(ctx context.Context, out io.Writer) error {
	defer rt.store.Close()
	tmuxEngine, err := rt.newExecutorForMode(config.ExecutorModeTMux)
	if err != nil {
		rt.logError("patrol", "初始化 tmux 执行器失败", "error", err)
		return err
	}
	manager := tmuxEngine.TMux()
	if manager != nil {
		if err := rt.patrolRepoSlots(ctx, tmuxEngine, manager, out); err != nil {
			return err
		}
	} else {
		rt.logInfo("patrol", "当前未启用 tmux，会话巡查已跳过")
		_, _ = fmt.Fprintln(out, "当前未启用 tmux，会话巡查已跳过")
	}
	daemonEngine, err := rt.newExecutorForMode(config.ExecutorModeDaemon)
	if err != nil {
		rt.logError("patrol", "初始化 daemon 执行器失败", "error", err)
		return err
	}
	return rt.patrolDaemonRepoSlots(ctx, daemonEngine, out)
}

func (rt *Runtime) Stats(out io.Writer) error {
	return rt.StatsWithOptions(out, StatsOptions{})
}

func (rt *Runtime) StatsWithOptions(out io.Writer, options StatsOptions) error {
	defer rt.store.Close()
	limit := options.Limit
	if limit <= 0 {
		limit = defaultStatsLimit
	}
	summary, err := rt.store.TokenStatsBetween(options.Start, options.End)
	if err != nil {
		return err
	}
	hasTokens := summary.Runs > 0
	if !hasTokens {
		if hasStatsRange(options.Start, options.End) {
			_, _ = fmt.Fprintln(out, "所选范围内暂无 token 使用记录")
		} else {
			_, _ = fmt.Fprintln(out, "暂无 token 使用记录")
		}
	} else {
		if hasStatsRange(options.Start, options.End) {
			_, _ = fmt.Fprintln(out, "统计范围:")
			if !options.Start.IsZero() {
				_, _ = fmt.Fprintf(out, "  起始日期: %s\n", options.Start.Format("2006-01-02"))
			}
			if !options.End.IsZero() {
				_, _ = fmt.Fprintf(out, "  截止日期: %s (含当日)\n", options.End.Add(-24*time.Hour).Format("2006-01-02"))
			}
			_, _ = fmt.Fprintln(out)
		}
		_, _ = fmt.Fprintf(out, "执行次数: %d\n", summary.Runs)
		_, _ = fmt.Fprintf(out, "会话数: %d\n", summary.Sessions)
		_, _ = fmt.Fprintf(out, "输入 tokens: %d\n", summary.InputTokens)
		_, _ = fmt.Fprintf(out, "输出 tokens: %d\n", summary.OutputTokens)
		_, _ = fmt.Fprintf(out, "缓存创建 tokens: %d\n", summary.CacheCreate)
		_, _ = fmt.Fprintf(out, "缓存命中 tokens: %d\n", summary.CacheRead)
		_, _ = fmt.Fprintf(out, "累计成本(USD): %.4f\n", summary.CostUSD)
		if !summary.FirstUsedAt.IsZero() {
			_, _ = fmt.Fprintf(out, "首次记录: %s\n", summary.FirstUsedAt.Format(time.RFC3339))
		}
		if !summary.LastUsedAt.IsZero() {
			_, _ = fmt.Fprintf(out, "最近记录: %s\n", summary.LastUsedAt.Format(time.RFC3339))
		}
		if options.Daily {
			daily, err := rt.store.DailyTokenStatsBetween(options.Start, options.End)
			if err != nil {
				return err
			}
			daily, dailyTruncated := limitDailyTokenStats(daily, limit)
			if len(daily) > 0 {
				_, _ = fmt.Fprintln(out)
				if dailyTruncated {
					_, _ = fmt.Fprintf(out, "按天聚合(最近 %d 天):\n", limit)
				} else {
					_, _ = fmt.Fprintln(out, "按天聚合:")
				}
				w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(w, "DAY\tRUNS\tSESSIONS\tINPUT\tOUTPUT\tCACHE_CREATE\tCACHE_READ\tCOST_USD")
				for _, item := range daily {
					_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%.4f\n",
						item.Day.Format("2006-01-02"),
						item.Runs,
						item.Sessions,
						item.InputTokens,
						item.OutputTokens,
						item.CacheCreate,
						item.CacheRead,
						item.CostUSD,
					)
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
		}
		stats, err := rt.store.TaskTokenStatsBetween(options.Start, options.End, limit)
		if err != nil {
			return err
		}
		if len(stats) > 0 {
			_, _ = fmt.Fprintln(out)
			_, _ = fmt.Fprintf(out, "任务明细(最近 %d 项):\n", limit)
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ISSUE\tRUNS\tINPUT\tOUTPUT\tCACHE_CREATE\tCACHE_READ\tCOST_USD\tSESSION\tTITLE")
			for _, item := range stats {
				title := item.IssueTitle
				if len(title) > 40 {
					title = title[:37] + "..."
				}
				sessionID := item.LastSessionID
				if sessionID == "" {
					sessionID = "-"
				}
				_, _ = fmt.Fprintf(
					w,
					"%s#%d\t%d\t%d\t%d\t%d\t%d\t%.4f\t%s\t%s\n",
					item.IssueRepo,
					item.IssueNumber,
					item.Runs,
					item.InputTokens,
					item.OutputTokens,
					item.CacheCreate,
					item.CacheRead,
					item.CostUSD,
					sessionID,
					title,
				)
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
		if options.ShowRTKComparison {
			comparison, err := rt.store.RTKComparisonBetween(options.Start, options.End)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(out)
			_, _ = fmt.Fprintln(out, "RTK 对比:")
			_, _ = fmt.Fprintf(out, "  RTK runs: %d\n", comparison.RTKRuns)
			_, _ = fmt.Fprintf(out, "  Plain runs: %d\n", comparison.PlainRuns)
			_, _ = fmt.Fprintf(out, "  RTK avg cost(USD): %.4f\n", comparison.RTKAvgCostUSD)
			_, _ = fmt.Fprintf(out, "  Plain avg cost(USD): %.4f\n", comparison.PlainAvgCostUSD)
			_, _ = fmt.Fprintf(out, "  RTK avg tokens: %.1f\n", comparison.RTKAvgTokens)
			_, _ = fmt.Fprintf(out, "  Plain avg tokens: %.1f\n", comparison.PlainAvgTokens)
			_, _ = fmt.Fprintf(out, "  Estimated savings: %.2f%%\n", comparison.SavingsPercent)
			if options.Daily {
				dailyComparison, err := rt.store.DailyRTKComparisonBetween(options.Start, options.End)
				if err != nil {
					return err
				}
				dailyComparison, dailyComparisonTruncated := limitDailyRTKComparisons(dailyComparison, limit)
				if len(dailyComparison) > 0 {
					_, _ = fmt.Fprintln(out)
					if dailyComparisonTruncated {
						_, _ = fmt.Fprintf(out, "按天 RTK 对比(最近 %d 天):\n", limit)
					} else {
						_, _ = fmt.Fprintln(out, "按天 RTK 对比:")
					}
					w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
					_, _ = fmt.Fprintln(w, "DAY\tRTK_RUNS\tPLAIN_RUNS\tRTK_AVG_COST\tPLAIN_AVG_COST\tRTK_AVG_TOKENS\tPLAIN_AVG_TOKENS\tSAVINGS")
					for _, item := range dailyComparison {
						_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%.4f\t%.4f\t%.1f\t%.1f\t%.2f%%\n",
							item.Day.Format("2006-01-02"),
							item.RTKRuns,
							item.PlainRuns,
							item.RTKAvgCostUSD,
							item.PlainAvgCostUSD,
							item.RTKAvgTokens,
							item.PlainAvgTokens,
							item.SavingsPercent,
						)
					}
					if err := w.Flush(); err != nil {
						return err
					}
				}
			}
		}
	}

	flowWindows, err := rt.collectTaskClassFlowWindows(buildStatsTaskClassFlowWindowSpecs(options))
	if err != nil {
		return err
	}
	if len(flowWindows) > 0 {
		_, _ = fmt.Fprintln(out)
		if err := renderTaskClassFlowWindows(out, "Sevolver 自进化链路聚合:", flowWindows); err != nil {
			return err
		}
	}
	return nil
}

func hasStatsRange(start, end time.Time) bool {
	return !start.IsZero() || !end.IsZero()
}

func limitDailyTokenStats(items []storage.DailyTokenStat, limit int) ([]storage.DailyTokenStat, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[len(items)-limit:], true
}

func limitDailyRTKComparisons(items []storage.DailyRTKComparison, limit int) ([]storage.DailyRTKComparison, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[len(items)-limit:], true
}

type taskClassFlowWindowSpec struct {
	Name  string
	Label string
	Start time.Time
	End   time.Time
}

func buildStatsTaskClassFlowWindowSpecs(options StatsOptions) []taskClassFlowWindowSpec {
	anchor := time.Now()
	if !options.End.IsZero() {
		anchor = options.End
	} else if !options.Start.IsZero() {
		anchor = options.Start.Add(24 * time.Hour)
	}
	rangeLabel := "指定范围"
	if !hasStatsRange(options.Start, options.End) {
		rangeLabel = "全量(未指定范围)"
	}
	return []taskClassFlowWindowSpec{
		{
			Name:  "day",
			Label: "日窗口(最近 24 小时)",
			Start: anchor.Add(-24 * time.Hour),
			End:   anchor,
		},
		{
			Name:  "week",
			Label: "周窗口(最近 7 天)",
			Start: anchor.Add(-7 * 24 * time.Hour),
			End:   anchor,
		},
		{
			Name:  "range",
			Label: rangeLabel,
			Start: options.Start,
			End:   options.End,
		},
	}
}

func buildStatusTaskClassFlowWindowSpecs() []taskClassFlowWindowSpec {
	now := time.Now()
	return []taskClassFlowWindowSpec{
		{
			Name:  "day",
			Label: "日窗口(最近 24 小时)",
			Start: now.Add(-24 * time.Hour),
			End:   now,
		},
		{
			Name:  "week",
			Label: "周窗口(最近 7 天)",
			Start: now.Add(-7 * 24 * time.Hour),
			End:   now,
		},
		{
			Name:  "range",
			Label: "全量(未指定范围)",
		},
	}
}

func (rt *Runtime) collectTaskClassFlowWindows(specs []taskClassFlowWindowSpec) ([]taskClassFlowWindowSnapshot, error) {
	windows := make([]taskClassFlowWindowSnapshot, 0, len(specs))
	for _, spec := range specs {
		snapshot, err := rt.store.TaskClassFlowStatsBetween(spec.Start, spec.End, sevolverObservedTaskClasses)
		if err != nil {
			return nil, err
		}
		window := taskClassFlowWindowSnapshot{
			Name:    spec.Name,
			Label:   spec.Label,
			Start:   formatRFC3339(spec.Start),
			End:     formatRFC3339(spec.End),
			Classes: nil,
		}
		if snapshot != nil {
			window.Classes = snapshot.Classes
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func renderTaskClassFlowWindows(out io.Writer, title string, windows []taskClassFlowWindowSnapshot) error {
	_, _ = fmt.Fprintln(out, title)
	if len(windows) == 0 {
		_, _ = fmt.Fprintln(out, "  当前无 task_class 聚合数据")
		return nil
	}
	for _, window := range windows {
		_, _ = fmt.Fprintf(out, "  [%s] %s\n", emptyStatusValue(window.Name), emptyStatusValue(window.Label))
		if window.Start != "" || window.End != "" {
			_, _ = fmt.Fprintf(out, "  范围: %s -> %s\n", emptyStatusValue(window.Start), emptyStatusValue(window.End))
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "CLASS\tUPGRADE\tCONVERGED\tBACKLOG\tSUCCESS\tFAILURE\tAVG_CLOSE\tRUNS\tINPUT\tOUTPUT\tCACHE_CREATE\tCACHE_READ\tCOST_USD")
		for _, item := range window.Classes {
			_, _ = fmt.Fprintf(
				w,
				"%s\t%d\t%d\t%d\t%.1f%%(%d)\t%.1f%%(%d)\t%s\t%d\t%d\t%d\t%d\t%d\t%.4f\n",
				emptyStatusValue(item.TaskClass),
				item.UpgradeCount,
				item.ConvergedCount,
				item.BacklogCount,
				item.SuccessRate,
				item.SuccessCount,
				item.FailureRate,
				item.FailureCount,
				formatAvgCloseDuration(item.AvgCloseSeconds),
				item.Runs,
				item.InputTokens,
				item.OutputTokens,
				item.CacheCreate,
				item.CacheRead,
				item.CostUSD,
			)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func formatAvgCloseDuration(seconds float64) string {
	if seconds <= 0 {
		return "-"
	}
	duration := time.Duration(seconds * float64(time.Second))
	return duration.Round(time.Second).String()
}

func (rt *Runtime) Status(out io.Writer) error {
	return rt.StatusWithOptions(out, StatusOptions{})
}

func (rt *Runtime) StatusWithOptions(out io.Writer, options StatusOptions) error {
	defer rt.store.Close()
	rt.logInfo("status", "开始生成运行态快照", "log_level", rt.runtimeLogLevel())
	tasks, err := rt.store.ListTasks()
	if err != nil {
		rt.logError("status", "读取任务列表失败", "error", err)
		return err
	}
	counts := map[core.State]int{}
	for _, task := range tasks {
		counts[task.State]++
	}
	schedulerSnapshot, err := scheduler.CollectStatus(rt.cfg)
	if err != nil {
		rt.logDebug("status", "检测到调度配置与生效状态不一致", "requested", schedulerSnapshot.Requested, "effective", schedulerSnapshot.Effective, "reason", schedulerSnapshot.Reason)
	}
	sessionSnapshot := rt.collectStatusSessions(tasks)
	slotSnapshot, err := rt.collectStatusRepoSlots()
	if err != nil {
		rt.logError("status", "读取仓库槽位失败", "error", err)
		return err
	}
	execSnapshot := rt.executorSnapshot()
	tokenSummary, err := rt.store.TokenStats()
	if err != nil {
		rt.logError("status", "读取 token 统计失败", "error", err)
		return err
	}
	rtkComparison, err := rt.store.RTKComparisonBetween(time.Time{}, time.Time{})
	if err != nil {
		rt.logError("status", "读取 RTK 对比失败", "error", err)
		return err
	}
	recentEvents, err := rt.store.ListTaskEvents(20)
	if err != nil {
		rt.logError("status", "读取最近事件失败", "error", err)
		return err
	}
	if sessionSnapshot.Error != "" {
		rt.logDebug("status", "tmux 会话快照不可用", "reason", sessionSnapshot.Error, "running_tasks", sessionSnapshot.RunningTasks)
	} else {
		rt.logDebug(
			"status",
			"tmux 会话快照已采集",
			"running_tasks",
			sessionSnapshot.RunningTasks,
			"total_sessions",
			sessionSnapshot.TotalSessions,
			"healthy_sessions",
			sessionSnapshot.HealthySessions,
			"warning_sessions",
			sessionSnapshot.WarningSessions,
			"timeout_sessions",
			sessionSnapshot.TimeoutSessions,
			"dead_sessions",
			sessionSnapshot.DeadSessions,
			"missing_sessions",
			sessionSnapshot.MissingSessions,
			"orphaned_sessions",
			sessionSnapshot.OrphanedSessions,
		)
	}
	alerts := rt.filterStatusAlerts(recentEvents, tasks)
	flowWindows, err := rt.collectTaskClassFlowWindows(buildStatusTaskClassFlowWindowSpecs())
	if err != nil {
		rt.logError("status", "读取 task_class 聚合失败", "error", err)
		return err
	}
	snapshot := buildRuntimeStatusSnapshot(tasks, counts, schedulerSnapshot, execSnapshot, sessionSnapshot, slotSnapshot, tokenSummary, rtkComparison, alerts, flowWindows)
	if options.JSON {
		if err := renderRuntimeStatusJSON(out, snapshot); err != nil {
			rt.logError("status", "输出 JSON 运行态快照失败", "error", err)
			return err
		}
		rt.logInfo("status", "运行态快照已输出", "tasks", len(tasks), "alerts", len(alerts), "scheduler_effective", schedulerSnapshot.Effective, "format", "json")
		return nil
	}
	if err := renderRuntimeStatusHuman(out, snapshot); err != nil {
		rt.logError("status", "输出运行态快照失败", "error", err)
		return err
	}
	rt.logInfo("status", "运行态快照已输出", "tasks", len(tasks), "alerts", len(alerts), "scheduler_effective", schedulerSnapshot.Effective, "format", "human")
	return nil
}

type statusSessionItem struct {
	IssueRepo   string `json:"issue_repo"`
	IssueNumber int    `json:"issue_number"`
	SessionName string `json:"session_name"`
	Age         string `json:"age"`
	Health      string `json:"health"`
	Title       string `json:"title"`
}

type statusSessionSnapshot struct {
	RunningTasks     int                 `json:"running_tasks"`
	DaemonTasks      int                 `json:"daemon_tasks"`
	TotalSessions    int                 `json:"total_sessions"`
	HealthySessions  int                 `json:"healthy_sessions"`
	WarningSessions  int                 `json:"warning_sessions"`
	TimeoutSessions  int                 `json:"timeout_sessions"`
	DeadSessions     int                 `json:"dead_sessions"`
	MissingSessions  int                 `json:"missing_sessions"`
	OrphanedSessions int                 `json:"orphaned_sessions"`
	Error            string              `json:"error,omitempty"`
	Items            []statusSessionItem `json:"items"`
}

type statusRepoSlotItem struct {
	TargetRepo         string `json:"target_repo"`
	TaskID             string `json:"task_id"`
	ExecutorMode       string `json:"executor_mode,omitempty"`
	Phase              string `json:"phase"`
	CurrentStep        string `json:"current_step,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
	RestartCount       int    `json:"restart_count"`
	FinalizeRetryStep  string `json:"finalize_retry_step,omitempty"`
	FinalizeRetryCount int    `json:"finalize_retry_count"`
	SyncTarget         string `json:"sync_target,omitempty"`
	SyncHome           string `json:"sync_home,omitempty"`
	ReportIssue        string `json:"report_issue,omitempty"`
	LastProbeAt        string `json:"last_probe_at,omitempty"`
	LastAttemptAt      string `json:"last_attempt_at,omitempty"`
	LastAdvanceAt      string `json:"last_advance_at,omitempty"`
	NextRetryAt        string `json:"next_retry_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
	CompletedAt        string `json:"completed_at,omitempty"`
	LastError          string `json:"last_error,omitempty"`
}

type statusRepoSlotSnapshot struct {
	Total  int                  `json:"total"`
	Counts map[string]int       `json:"counts"`
	Items  []statusRepoSlotItem `json:"items"`
}

type statusAlert struct {
	IssueRef  string         `json:"issue_ref"`
	EventType core.EventType `json:"event_type"`
	Detail    string         `json:"detail"`
	CreatedAt string         `json:"created_at"`
}

type executorSnapshot struct {
	Mode              string `json:"mode"`
	TMuxTargets       int    `json:"tmux_targets"`
	DaemonTargets     int    `json:"daemon_targets"`
	LauncherRequested string `json:"launcher_requested"`
	LauncherResolved  string `json:"launcher_resolved,omitempty"`
	LauncherError     string `json:"launcher_error,omitempty"`
	ClaudeRequested   string `json:"claude_requested"`
	ClaudeResolved    string `json:"claude_resolved,omitempty"`
	ClaudeError       string `json:"claude_error,omitempty"`
	RTKRequested      string `json:"rtk_requested"`
	RTKResolved       string `json:"rtk_resolved,omitempty"`
	RTKError          string `json:"rtk_error,omitempty"`
}

type statusTaskItem struct {
	TaskID        string `json:"task_id"`
	IssueRepo     string `json:"issue_repo"`
	IssueNumber   int    `json:"issue_number"`
	State         string `json:"state"`
	TaskClass     string `json:"task_class,omitempty"`
	TargetRepo    string `json:"target_repo,omitempty"`
	Author        string `json:"author"`
	Approved      bool   `json:"approved"`
	RetryCount    int    `json:"retry_count"`
	Title         string `json:"title"`
	LastSessionID string `json:"last_session_id,omitempty"`
	ErrorMsg      string `json:"error_msg,omitempty"`
}

type statusTasksSnapshot struct {
	Total  int              `json:"total"`
	Counts map[string]int   `json:"counts"`
	Items  []statusTaskItem `json:"items"`
}

type statusRTKComparisonSnapshot struct {
	RTKRuns         int     `json:"rtk_runs"`
	PlainRuns       int     `json:"plain_runs"`
	RTKAvgCostUSD   float64 `json:"rtk_avg_cost_usd"`
	PlainAvgCostUSD float64 `json:"plain_avg_cost_usd"`
	RTKAvgTokens    float64 `json:"rtk_avg_tokens"`
	PlainAvgTokens  float64 `json:"plain_avg_tokens"`
	SavingsPercent  float64 `json:"savings_percent"`
}

type statusTokenSnapshot struct {
	Runs          int                         `json:"runs"`
	Sessions      int                         `json:"sessions"`
	InputTokens   int                         `json:"input_tokens"`
	OutputTokens  int                         `json:"output_tokens"`
	CacheCreate   int                         `json:"cache_create"`
	CacheRead     int                         `json:"cache_read"`
	CostUSD       float64                     `json:"cost_usd"`
	FirstUsedAt   string                      `json:"first_used_at,omitempty"`
	LastUsedAt    string                      `json:"last_used_at,omitempty"`
	RTKComparison statusRTKComparisonSnapshot `json:"rtk_comparison"`
}

type taskClassFlowWindowSnapshot struct {
	Name    string                      `json:"name"`
	Label   string                      `json:"label"`
	Start   string                      `json:"start,omitempty"`
	End     string                      `json:"end,omitempty"`
	Classes []storage.TaskClassFlowStat `json:"classes"`
}

type runtimeStatusSnapshot struct {
	GeneratedAt string                        `json:"generated_at"`
	Scheduler   scheduler.StatusSnapshot      `json:"scheduler"`
	Executor    executorSnapshot              `json:"executor"`
	Sessions    statusSessionSnapshot         `json:"sessions"`
	Slots       statusRepoSlotSnapshot        `json:"slots"`
	Tasks       statusTasksSnapshot           `json:"tasks"`
	Tokens      statusTokenSnapshot           `json:"tokens"`
	TaskClass   []taskClassFlowWindowSnapshot `json:"task_class"`
	Alerts      []statusAlert                 `json:"alerts"`
}

func (rt *Runtime) collectStatusSessions(tasks []*core.Task) statusSessionSnapshot {
	snapshot := statusSessionSnapshot{}
	runningTasks := make([]*core.Task, 0)
	daemonTasks := 0
	for _, task := range tasks {
		if task.State != core.StateRunning {
			continue
		}
		if rt.cfg.ExecutorModeForRepo(task.TargetRepo) == config.ExecutorModeTMux {
			runningTasks = append(runningTasks, task)
			continue
		}
		daemonTasks++
	}
	snapshot.RunningTasks = len(runningTasks)
	snapshot.DaemonTasks = daemonTasks
	if snapshot.RunningTasks == 0 {
		return snapshot
	}
	if !tmux.Available("tmux") {
		snapshot.Error = "未检测到 tmux CLI，当前无法汇报会话健康"
		return snapshot
	}
	manager, err := tmux.New("tmux")
	if err != nil {
		snapshot.Error = fmt.Sprintf("初始化 tmux 失败: %v", err)
		return snapshot
	}
	sessions, err := manager.List("ccclaw-")
	if err != nil {
		snapshot.Error = fmt.Sprintf("读取 tmux 会话失败: %v", err)
		return snapshot
	}
	snapshot.TotalSessions = len(sessions)
	timeout, _ := time.ParseDuration(rt.cfg.Executor.Timeout)
	now := time.Now()
	sessionMap := make(map[string]tmux.SessionStatus, len(sessions))
	for _, session := range sessions {
		sessionMap[session.Name] = session
	}
	taskSessions := make(map[string]struct{}, len(runningTasks))
	for _, task := range runningTasks {
		sessionName := executor.SessionName(task.TaskID)
		taskSessions[sessionName] = struct{}{}
		item := statusSessionItem{
			IssueRepo:   task.IssueRepo,
			IssueNumber: task.IssueNumber,
			SessionName: sessionName,
			Age:         "-",
			Health:      "missing",
			Title:       trimStatusText(task.IssueTitle, 40),
		}
		status, exists := sessionMap[sessionName]
		switch {
		case !exists:
			snapshot.MissingSessions++
		case status.PaneDead:
			snapshot.DeadSessions++
			item.Age = now.Sub(status.CreatedAt).Round(time.Second).String()
			item.Health = fmt.Sprintf("dead(exit=%d)", status.ExitCode)
		default:
			age := now.Sub(status.CreatedAt)
			item.Age = age.Round(time.Second).String()
			item.Health = "ok"
			if timeout > 0 && age >= timeout {
				snapshot.TimeoutSessions++
				item.Health = "timeout"
			} else if timeout > 0 && age >= timeout*8/10 {
				snapshot.WarningSessions++
				item.Health = "warning"
			} else {
				snapshot.HealthySessions++
			}
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	for _, session := range sessions {
		if _, exists := taskSessions[session.Name]; !exists {
			snapshot.OrphanedSessions++
		}
	}
	return snapshot
}

func (rt *Runtime) collectStatusRepoSlots() (statusRepoSlotSnapshot, error) {
	snapshot := statusRepoSlotSnapshot{Counts: map[string]int{}}
	slots, err := rt.store.ListRepoSlots()
	if err != nil {
		return snapshot, err
	}
	snapshot.Total = len(slots)
	for _, slot := range slots {
		if slot == nil {
			continue
		}
		phase := strings.TrimSpace(string(slot.Phase))
		if phase == "" {
			phase = "unknown"
		}
		snapshot.Counts[phase]++
		snapshot.Items = append(snapshot.Items, statusRepoSlotItem{
			TargetRepo:         slot.TargetRepo,
			TaskID:             slot.TaskID,
			ExecutorMode:       slot.ExecutorMode,
			Phase:              phase,
			CurrentStep:        slot.CurrentStep,
			FailureClass:       string(slot.FailureClass),
			RestartCount:       slot.RestartCount,
			FinalizeRetryStep:  slot.FinalizeRetryStep,
			FinalizeRetryCount: slot.FinalizeRetryCount,
			SyncTarget:         string(slot.SyncTarget),
			SyncHome:           string(slot.SyncHome),
			ReportIssue:        string(slot.ReportIssue),
			LastProbeAt:        formatRFC3339(slot.LastProbeAt),
			LastAttemptAt:      formatRFC3339(slot.LastAttemptAt),
			LastAdvanceAt:      formatRFC3339(slot.LastAdvanceAt),
			NextRetryAt:        formatRFC3339(slot.NextRetryAt),
			UpdatedAt:          formatRFC3339(slot.UpdatedAt),
			CompletedAt:        formatRFC3339(slot.CompletedAt),
			LastError:          slot.LastError,
		})
	}
	return snapshot, nil
}

func (rt *Runtime) filterStatusAlerts(events []storage.EventRecord, tasks []*core.Task) []statusAlert {
	taskRefs := make(map[string]string, len(tasks))
	for _, task := range tasks {
		taskRefs[task.TaskID] = fmt.Sprintf("%s#%d", task.IssueRepo, task.IssueNumber)
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	alerts := make([]statusAlert, 0, 5)
	for _, item := range events {
		if item.CreatedAt.Before(cutoff) {
			continue
		}
		if item.EventType != core.EventFailed && item.EventType != core.EventDead && item.EventType != core.EventWarning {
			continue
		}
		issueRef := taskRefs[item.TaskID]
		if issueRef == "" {
			issueRef = item.TaskID
		}
		alerts = append(alerts, statusAlert{
			IssueRef:  issueRef,
			EventType: item.EventType,
			Detail:    trimStatusText(strings.ReplaceAll(item.Detail, "\n", " "), 88),
			CreatedAt: formatRFC3339(item.CreatedAt),
		})
		if len(alerts) == 5 {
			break
		}
	}
	return alerts
}

func buildRuntimeStatusSnapshot(tasks []*core.Task, counts map[core.State]int, schedulerSnapshot scheduler.StatusSnapshot, execSnapshot executorSnapshot, sessionSnapshot statusSessionSnapshot, slotSnapshot statusRepoSlotSnapshot, tokenSummary *storage.TokenStatsSummary, rtkComparison *storage.RTKComparison, alerts []statusAlert, taskClass []taskClassFlowWindowSnapshot) runtimeStatusSnapshot {
	taskItems := make([]statusTaskItem, 0, len(tasks))
	for _, task := range tasks {
		taskItems = append(taskItems, statusTaskItem{
			TaskID:        task.TaskID,
			IssueRepo:     task.IssueRepo,
			IssueNumber:   task.IssueNumber,
			State:         string(task.State),
			TaskClass:     string(task.TaskClass),
			TargetRepo:    task.TargetRepo,
			Author:        task.IssueAuthor,
			Approved:      task.Approved,
			RetryCount:    task.RetryCount,
			Title:         task.IssueTitle,
			LastSessionID: task.LastSessionID,
			ErrorMsg:      task.ErrorMsg,
		})
	}
	stateCounts := map[string]int{}
	for _, state := range []core.State{core.StateNew, core.StateRunning, core.StateFinalizing, core.StateBlocked, core.StateFailed, core.StateDone, core.StateDead} {
		stateCounts[string(state)] = counts[state]
	}
	return runtimeStatusSnapshot{
		GeneratedAt: formatRFC3339(time.Now()),
		Scheduler:   schedulerSnapshot,
		Executor:    execSnapshot,
		Sessions:    sessionSnapshot,
		Slots:       slotSnapshot,
		Tasks: statusTasksSnapshot{
			Total:  len(tasks),
			Counts: stateCounts,
			Items:  taskItems,
		},
		Tokens: statusTokenSnapshot{
			Runs:         tokenSummary.Runs,
			Sessions:     tokenSummary.Sessions,
			InputTokens:  tokenSummary.InputTokens,
			OutputTokens: tokenSummary.OutputTokens,
			CacheCreate:  tokenSummary.CacheCreate,
			CacheRead:    tokenSummary.CacheRead,
			CostUSD:      tokenSummary.CostUSD,
			FirstUsedAt:  formatRFC3339(tokenSummary.FirstUsedAt),
			LastUsedAt:   formatRFC3339(tokenSummary.LastUsedAt),
			RTKComparison: statusRTKComparisonSnapshot{
				RTKRuns:         rtkComparison.RTKRuns,
				PlainRuns:       rtkComparison.PlainRuns,
				RTKAvgCostUSD:   rtkComparison.RTKAvgCostUSD,
				PlainAvgCostUSD: rtkComparison.PlainAvgCostUSD,
				RTKAvgTokens:    rtkComparison.RTKAvgTokens,
				PlainAvgTokens:  rtkComparison.PlainAvgTokens,
				SavingsPercent:  rtkComparison.SavingsPercent,
			},
		},
		TaskClass: taskClass,
		Alerts:    alerts,
	}
}

func renderRuntimeStatusHuman(out io.Writer, snapshot runtimeStatusSnapshot) error {
	_, _ = fmt.Fprintln(out, "当前快照")
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "调度快照:")
	_, _ = fmt.Fprintf(out, "  配置请求: %s\n", snapshot.Scheduler.Requested)
	_, _ = fmt.Fprintf(out, "  当前生效: %s\n", snapshot.Scheduler.Effective)
	_, _ = fmt.Fprintf(out, "  当前说明: %s\n", snapshot.Scheduler.Reason)
	if snapshot.Scheduler.Repair != "" {
		_, _ = fmt.Fprintf(out, "  修复建议: %s\n", snapshot.Scheduler.Repair)
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "执行器快照:")
	_, _ = fmt.Fprintf(out, "  默认模式: %s (daemon_targets=%d tmux_targets=%d)\n", emptyStatusValue(snapshot.Executor.Mode), snapshot.Executor.DaemonTargets, snapshot.Executor.TMuxTargets)
	_, _ = fmt.Fprintf(out, "  入口配置: %s\n", snapshot.Executor.LauncherRequested)
	_, _ = fmt.Fprintf(out, "  入口解析: %s\n", formatResolvedBinary(snapshot.Executor.LauncherRequested, snapshot.Executor.LauncherResolved, snapshot.Executor.LauncherError))
	_, _ = fmt.Fprintf(out, "  Claude 解析: %s\n", formatResolvedBinary(snapshot.Executor.ClaudeRequested, snapshot.Executor.ClaudeResolved, snapshot.Executor.ClaudeError))
	_, _ = fmt.Fprintf(out, "  RTK 解析: %s\n", formatResolvedBinary(snapshot.Executor.RTKRequested, snapshot.Executor.RTKResolved, snapshot.Executor.RTKError))
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "会话快照:")
	_, _ = fmt.Fprintf(out, "  tmux 托管任务: %d\n", snapshot.Sessions.RunningTasks)
	_, _ = fmt.Fprintf(out, "  daemon 任务: %d\n", snapshot.Sessions.DaemonTasks)
	if snapshot.Sessions.Error != "" {
		_, _ = fmt.Fprintf(out, "  tmux 状态: %s\n", snapshot.Sessions.Error)
	} else if snapshot.Sessions.RunningTasks == 0 {
		_, _ = fmt.Fprintln(out, "  tmux 状态: 当前无 tmux 托管任务（默认 daemon 模式）")
	} else {
		_, _ = fmt.Fprintf(out, "  tmux 会话: %d\n", snapshot.Sessions.TotalSessions)
		_, _ = fmt.Fprintf(out, "  会话健康: 正常=%d 接近超时=%d 超时=%d 已退出=%d 丢失=%d 孤儿=%d\n",
			snapshot.Sessions.HealthySessions,
			snapshot.Sessions.WarningSessions,
			snapshot.Sessions.TimeoutSessions,
			snapshot.Sessions.DeadSessions,
			snapshot.Sessions.MissingSessions,
			snapshot.Sessions.OrphanedSessions,
		)
		if len(snapshot.Sessions.Items) > 0 {
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "ISSUE\tSESSION\tAGE\tHEALTH\tTITLE")
			for _, item := range snapshot.Sessions.Items {
				_, _ = fmt.Fprintf(w, "%s#%d\t%s\t%s\t%s\t%s\n", item.IssueRepo, item.IssueNumber, item.SessionName, item.Age, item.Health, item.Title)
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "仓位快照:")
	_, _ = fmt.Fprintf(out, "  仓位总数: %d\n", snapshot.Slots.Total)
	if snapshot.Slots.Total == 0 {
		_, _ = fmt.Fprintln(out, "  当前无仓位")
	} else {
		order := []string{
			string(storage.RepoSlotPhaseRunning),
			string(storage.RepoSlotPhaseRestarting),
			string(storage.RepoSlotPhasePaneDead),
			string(storage.RepoSlotPhaseFinalizing),
			string(storage.RepoSlotPhaseFinalizeFailed),
		}
		for _, phase := range order {
			if count := snapshot.Slots.Counts[phase]; count > 0 {
				_, _ = fmt.Fprintf(out, "  %-15s %d\n", phase, count)
			}
		}
		_, _ = fmt.Fprintln(out)
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "TARGET\tMODE\tPHASE\tSTEP\tCLASS\tRESTART\tFINALIZE_RETRY\tNEXT_RETRY\tTASK\tERROR")
		for _, item := range snapshot.Slots.Items {
			nextRetry := item.NextRetryAt
			if nextRetry == "" {
				nextRetry = "-"
			}
			lastError := trimStatusText(item.LastError, 36)
			if lastError == "" {
				lastError = "-"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s:%d\t%s\t%s\t%s\n",
				item.TargetRepo,
				emptyStatusValue(item.ExecutorMode),
				item.Phase,
				emptyStatusValue(item.CurrentStep),
				emptyStatusValue(item.FailureClass),
				item.RestartCount,
				emptyStatusValue(item.FinalizeRetryStep),
				item.FinalizeRetryCount,
				nextRetry,
				item.TaskID,
				lastError,
			)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "任务概览:")
	_, _ = fmt.Fprintf(out, "  任务总数: %d\n", snapshot.Tasks.Total)
	for _, state := range []core.State{core.StateNew, core.StateRunning, core.StateFinalizing, core.StateBlocked, core.StateFailed, core.StateDone, core.StateDead} {
		_, _ = fmt.Fprintf(out, "  %-8s %d\n", state, snapshot.Tasks.Counts[string(state)])
	}
	if snapshot.Tasks.Total == 0 {
		_, _ = fmt.Fprintln(out, "  当前无任务")
	} else {
		_, _ = fmt.Fprintln(out)
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ISSUE\tSTATE\tCLASS\tTARGET\tAUTHOR\tAPPROVED\tRETRY\tTITLE")
		for _, item := range snapshot.Tasks.Items {
			title := item.Title
			if len(title) > 46 {
				title = title[:43] + "..."
			}
			targetRepo := item.TargetRepo
			if targetRepo == "" {
				targetRepo = "-"
			}
			taskClass := item.TaskClass
			if taskClass == "" {
				taskClass = string(core.TaskClassGeneral)
			}
			_, _ = fmt.Fprintf(w, "%s#%d\t%s\t%s\t%s\t%s\t%t\t%d\t%s\n", item.IssueRepo, item.IssueNumber, item.State, taskClass, targetRepo, item.Author, item.Approved, item.RetryCount, title)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "Token 快照:")
	if snapshot.Tokens.Runs == 0 {
		_, _ = fmt.Fprintln(out, "  未检测到 token 使用记录")
	} else {
		_, _ = fmt.Fprintf(out, "  累计执行: %d runs / %d sessions\n", snapshot.Tokens.Runs, snapshot.Tokens.Sessions)
		_, _ = fmt.Fprintf(out, "  累计 tokens: input=%d output=%d cache_create=%d cache_read=%d\n", snapshot.Tokens.InputTokens, snapshot.Tokens.OutputTokens, snapshot.Tokens.CacheCreate, snapshot.Tokens.CacheRead)
		_, _ = fmt.Fprintf(out, "  累计成本(USD): %.4f\n", snapshot.Tokens.CostUSD)
		if snapshot.Tokens.LastUsedAt != "" {
			_, _ = fmt.Fprintf(out, "  最近记录: %s\n", snapshot.Tokens.LastUsedAt)
		}
		if snapshot.Tokens.RTKComparison.RTKRuns > 0 || snapshot.Tokens.RTKComparison.PlainRuns > 0 {
			_, _ = fmt.Fprintf(out, "  RTK 对比样本: rtk=%d plain=%d\n", snapshot.Tokens.RTKComparison.RTKRuns, snapshot.Tokens.RTKComparison.PlainRuns)
			if snapshot.Tokens.RTKComparison.RTKRuns > 0 && snapshot.Tokens.RTKComparison.PlainRuns > 0 {
				_, _ = fmt.Fprintf(out, "  RTK 估算节省: %.2f%%\n", snapshot.Tokens.RTKComparison.SavingsPercent)
			}
		}
		_, _ = fmt.Fprintln(out, "  详情请执行: ccclaw stats --rtk-comparison")
	}
	_, _ = fmt.Fprintln(out)

	if err := renderTaskClassFlowWindows(out, "Sevolver 自进化链路聚合:", snapshot.TaskClass); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintln(out, "最近异常:")
	if len(snapshot.Alerts) == 0 {
		_, _ = fmt.Fprintln(out, "  最近 7 天无失败/超时告警")
		return nil
	}
	for _, alert := range snapshot.Alerts {
		_, _ = fmt.Fprintf(out, "  %s %s %s %s\n", alert.CreatedAt, alert.IssueRef, alert.EventType, alert.Detail)
	}
	return nil
}

func renderRuntimeStatusJSON(out io.Writer, snapshot runtimeStatusSnapshot) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}

func formatRFC3339(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func trimStatusText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func emptyStatusValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func (rt *Runtime) executorSnapshot() executorSnapshot {
	values := map[string]string{}
	if rt.secrets != nil {
		values = rt.secrets.Values
	}
	launcherRequested := strings.TrimSpace(rt.cfg.Executor.Binary)
	if len(rt.cfg.Executor.Command) > 0 && strings.TrimSpace(rt.cfg.Executor.Command[0]) != "" {
		launcherRequested = strings.TrimSpace(rt.cfg.Executor.Command[0])
	}
	if launcherRequested == "" {
		launcherRequested = "claude"
	}
	claudeRequested := strings.TrimSpace(values["CCCLAW_CLAUDE_BIN"])
	if claudeRequested == "" {
		claudeRequested = "claude"
	}
	rtkRequested := strings.TrimSpace(values["CCCLAW_RTK_BIN"])
	if rtkRequested == "" {
		rtkRequested = "rtk"
	}
	tmuxTargets := 0
	daemonTargets := 0
	for _, target := range rt.cfg.EnabledTargets() {
		if rt.cfg.ExecutorModeForRepo(target.Repo) == config.ExecutorModeTMux {
			tmuxTargets++
		} else {
			daemonTargets++
		}
	}
	return executorSnapshot{
		Mode:              string(rt.cfg.ExecutorMode()),
		TMuxTargets:       tmuxTargets,
		DaemonTargets:     daemonTargets,
		LauncherRequested: launcherRequested,
		LauncherResolved:  resolvedBinaryOrEmpty(launcherRequested),
		LauncherError:     unresolvedBinaryError(launcherRequested),
		ClaudeRequested:   claudeRequested,
		ClaudeResolved:    resolvedBinaryOrEmpty(claudeRequested),
		ClaudeError:       unresolvedBinaryError(claudeRequested),
		RTKRequested:      rtkRequested,
		RTKResolved:       resolvedBinaryOrEmpty(rtkRequested),
		RTKError:          unresolvedBinaryError(rtkRequested),
	}
}

func resolvedBinaryOrEmpty(name string) string {
	resolved, err := executor.ResolveBinaryPath(name)
	if err != nil {
		return ""
	}
	return resolved
}

func unresolvedBinaryError(name string) string {
	if _, err := executor.ResolveBinaryPath(name); err != nil {
		return err.Error()
	}
	return ""
}

func formatResolvedBinary(requested, resolved, errText string) string {
	if strings.TrimSpace(resolved) != "" {
		if requested == resolved {
			return resolved
		}
		return fmt.Sprintf("%s (requested=%s)", resolved, requested)
	}
	if strings.TrimSpace(errText) == "" {
		return fmt.Sprintf("未找到 (requested=%s)", requested)
	}
	return fmt.Sprintf("未找到 (requested=%s, error=%s)", requested, errText)
}

func (rt *Runtime) ShowConfig(out io.Writer) error {
	defer rt.store.Close()
	rt.logInfo("config", "开始导出配置快照", "targets", len(rt.cfg.Targets), "log_level", rt.runtimeLogLevel())
	secretCount := 0
	if rt.secrets != nil {
		secretCount = len(rt.secrets.Values)
	}
	targets := make([]map[string]string, 0, len(rt.cfg.Targets))
	for _, target := range rt.cfg.Targets {
		item := map[string]string{
			"repo":       target.Repo,
			"local_path": target.LocalPath,
			"kb_path":    target.KBPath,
		}
		if target.Disabled {
			item["status"] = "disabled"
		} else {
			item["status"] = "enabled"
		}
		targets = append(targets, item)
	}
	payload := map[string]any{
		"default_target": rt.cfg.DefaultTarget,
		"github":         rt.cfg.GitHub,
		"paths":          rt.cfg.Paths,
		"executor":       rt.cfg.Executor,
		"scheduler":      rt.cfg.Scheduler,
		"approval":       rt.cfg.Approval,
		"targets":        targets,
		"env_file":       rt.secrets.Path,
		"secret_keys":    sortedKeys(rt.secrets.Values),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		rt.logError("config", "序列化配置快照失败", "error", err)
		return err
	}
	if _, err = fmt.Fprintln(out, string(encoded)); err != nil {
		rt.logError("config", "输出配置快照失败", "error", err)
		return err
	}
	rt.logInfo("config", "配置快照已输出", "targets", len(rt.cfg.Targets), "enabled_targets", len(rt.cfg.EnabledTargets()), "secret_keys", secretCount)
	return nil
}

func (rt *Runtime) Doctor(ctx context.Context, out io.Writer) error {
	defer rt.store.Close()
	rt.logInfo("doctor", "开始执行环境诊断", "log_level", rt.runtimeLogLevel())
	checks := []struct {
		name string
		run  func() (string, error)
	}{
		{name: "配置文件", run: func() (string, error) { return rt.describeTargetRouting(), rt.cfg.Validate() }},
		{name: ".env 权限", run: func() (string, error) { return rt.secrets.Path, config.ValidateEnvFile(rt.secrets.Path) }},
		{name: "执行器入口", run: func() (string, error) {
			return rt.describeExecutor(), commandExists(rt.cfg.Executor.Command, rt.cfg.Executor.Binary)
		}},
		{name: "Claude 安装通道", run: func() (string, error) { return claudeInstallScriptURL, rt.checkClaudeInstallChannel(ctx) }},
		{name: "Claude 运行态", run: func() (string, error) { return rt.detectClaudeState(ctx) }},
		{name: "claude CLI", run: func() (string, error) { return rt.describeBinaryVersion("CCCLAW_CLAUDE_BIN", "claude", "version") }},
		{name: "Go 工具链", run: func() (string, error) { return commandVersion("go", "version") }},
		{name: "sudo 无口令", run: rt.checkPasswordlessSudo},
		{name: "rtk CLI", run: func() (string, error) { return rt.describeBinaryVersion("CCCLAW_RTK_BIN", "rtk", "--version") }},
		{name: "tmux CLI", run: rt.checkTMuxAvailability},
		{name: "rg CLI", run: func() (string, error) { return commandVersion("rg", "--version") }},
		{name: "duckdb CLI", run: func() (string, error) { return commandVersion("duckdb", "--version") }},
		{name: "git CLI", run: func() (string, error) { return commandVersion("git", "--version") }},
		{name: "jj/git 同步能力", run: vcs.SyncCapabilityStatus},
		{name: "gh CLI", run: func() (string, error) { return commandVersion("gh", "--version") }},
		{name: "GitHub 网络", run: func() (string, error) {
			return "rate_limit", rt.clientForRepo(rt.cfg.GitHub.ControlRepo).NetworkCheck(ctx)
		}},
		{name: "状态存储读写", run: func() (string, error) { return rt.cfg.Paths.VarDir, rt.store.Ping() }},
		{name: "调度器", run: rt.describeSchedulerStatus},
		{name: "磁盘空间", run: func() (string, error) { return rt.cfg.Paths.LogDir, rt.checkDisk() }},
	}
	var failed []string
	for _, check := range checks {
		rt.logDebug("doctor", "开始检查诊断项", "check", check.name)
		detail, err := check.run()
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", check.name, err))
			rt.logWarning("doctor", "诊断项失败", "check", check.name, "detail", detail, "error", err)
			if detail != "" {
				_, _ = fmt.Fprintf(out, "[FAIL] %s: %v (%s)\n", check.name, err, detail)
			} else {
				_, _ = fmt.Fprintf(out, "[FAIL] %s: %v\n", check.name, err)
			}
			continue
		}
		rt.logDebug("doctor", "诊断项通过", "check", check.name, "detail", detail)
		if detail != "" {
			_, _ = fmt.Fprintf(out, "[ OK ] %s: %s\n", check.name, detail)
		} else {
			_, _ = fmt.Fprintf(out, "[ OK ] %s\n", check.name)
		}
	}
	if len(failed) > 0 {
		rt.logWarning("doctor", "环境诊断完成，存在失败项", "failed", len(failed), "checks", len(checks))
		return fmt.Errorf("doctor 失败，共 %d 项异常", len(failed))
	}
	rt.logInfo("doctor", "环境诊断完成", "failed", 0, "checks", len(checks))
	return nil
}

func (rt *Runtime) checkTMuxAvailability() (string, error) {
	if rt == nil || rt.cfg == nil {
		return "", nil
	}
	tmuxTargets := 0
	for _, target := range rt.cfg.EnabledTargets() {
		if rt.cfg.ExecutorModeForRepo(target.Repo) == config.ExecutorModeTMux {
			tmuxTargets++
		}
	}
	if tmuxTargets == 0 {
		return "当前无 tmux 托管仓库（tmux 仅作为 debug attach）", nil
	}
	return commandVersion("tmux", "-V")
}

func (rt *Runtime) syncIssue(ctx context.Context, issue github.Issue, fromRun bool) error {
	_ = ctx
	issueRepo := strings.TrimSpace(issue.Repo)
	if issueRepo == "" {
		issueRepo = rt.cfg.GitHub.ControlRepo
		issue.Repo = issueRepo
	}
	existing, err := rt.store.GetByIdempotency(core.IdempotencyKey(issueRepo, issue.Number))
	if err != nil {
		return err
	}
	client := rt.clientForRepo(issueRepo)
	trusted, permission, err := client.IsTrustedActor(issue.User.Login, rt.cfg.Approval.MinimumPermission)
	if err != nil {
		return err
	}
	comments, err := client.ListComments(issue.Number)
	if err != nil {
		return err
	}
	currentDoneCommentID := int64(0)
	if existing != nil {
		currentDoneCommentID = existing.DoneCommentID
	}
	approval, err := client.FindApprovalInComments(comments, rt.cfg.Approval.Words, rt.cfg.Approval.RejectWords, rt.cfg.Approval.MinimumPermission)
	if err != nil {
		return err
	}
	doneComment := github.FindDoneComment(comments, currentDoneCommentID)
	approved := trusted
	switch {
	case approval.Rejected:
		approved = false
	case approval.Approved:
		approved = true
	}
	targetRepo, routeReasons := rt.resolveTargetRepo(issueRepo, issue.Body)
	taskClass := core.InferTaskClass(issue.Title, issue.Body, github.LabelNames(issue.Labels))
	rt.logDebug("issue", "完成 Issue 评估", "issue", rt.issueRef(issueRepo, issue.Number), "from_run", fromRun, "trusted", trusted, "permission", permission, "approved", approved, "task_class", taskClass, "target_repo", targetRepo)
	blockReasons := make([]string, 0, 2)
	if !strings.EqualFold(strings.TrimSpace(issue.State), "open") {
		blockReasons = append(blockReasons, fmt.Sprintf("Issue 当前状态为 `%s`，等待人工重新打开", issue.State))
	}
	if !github.HasLabel(issue.Labels, rt.cfg.GitHub.IssueLabel) {
		blockReasons = append(blockReasons, fmt.Sprintf("Issue 缺少触发标签 `%s`，等待重新打标签后再恢复", rt.cfg.GitHub.IssueLabel))
	}
	if approval.Rejected {
		blockReasons = append(blockReasons, fmt.Sprintf("最近审批被 `%s` 使用 `%s` 否决", approval.Actor, approval.Command))
	} else if !approved {
		blockReasons = append(blockReasons, fmt.Sprintf(
			"发起者 `%s` 权限为 `%s`，等待 `%s` 及以上成员评论 %s",
			issue.User.Login,
			permission,
			rt.cfg.Approval.MinimumPermission,
			approvalCommandHelp(rt.cfg.Approval.Words),
		))
	}
	blockReasons = append(blockReasons, routeReasons...)
	labels := github.LabelNames(issue.Labels)
	taskID := core.TaskID(issueRepo, issue.Number)
	var previousState core.State
	updated, err := rt.store.ApplyTask(taskID, func(current *core.Task) (*core.Task, error) {
		if current == nil {
			current = &core.Task{
				TaskID:         taskID,
				IdempotencyKey: core.IdempotencyKey(issueRepo, issue.Number),
				ControlRepo:    rt.cfg.GitHub.ControlRepo,
				IssueRepo:      issueRepo,
				TargetRepo:     targetRepo,
				CreatedAt:      issue.CreatedAt,
			}
		}
		previousState = current.State
		current.IssueNumber = issue.Number
		current.IssueRepo = issueRepo
		current.IssueTitle = issue.Title
		current.IssueBody = issue.Body
		current.IssueAuthor = issue.User.Login
		current.IssueAuthorPermission = permission
		current.Labels = labels
		current.Intent = core.InferIntent(labels)
		current.TaskClass = taskClass
		current.RiskLevel = core.InferRisk(labels)
		current.Approved = approved
		current.TargetRepo = targetRepo
		current.ApprovalCommand = approval.Command
		current.ApprovalActor = approval.Actor
		current.ApprovalCommentID = approval.CommentID
		if doneComment != nil {
			current.DoneCommentID = doneComment.ID
		} else {
			current.DoneCommentID = 0
		}

		switch {
		case doneComment != nil:
			current.State = core.StateDone
			current.ErrorMsg = ""
		case current.State == core.StateDead:
			// 保持终态，只更新元数据。
		case current.Intent == core.IntentReport && current.State != core.StateDone:
			// 汇报型 Issue 直接豁免 Claude 执行，标记完成。
			current.State = core.StateDone
			current.ErrorMsg = ""
		case current.State == core.StateRunning || current.State == core.StateFinalizing:
			// 执行中或收尾中任务只刷新元数据，不回退主状态。
		case len(blockReasons) == 0:
			if current.State == core.StateBlocked || current.State == "" || previousState == core.StateDone {
				current.State = core.StateNew
			}
		default:
			current.State = core.StateBlocked
		}
		return current, nil
	})
	if err != nil {
		return err
	}
	if updated == nil {
		return nil
	}
	isReportSkip := updated.Intent == core.IntentReport && updated.State == core.StateDone
	if previousState == "" {
		eventType := core.EventCreated
		detail := fmt.Sprintf("任务入队，author=%s permission=%s approved=%t class=%s target=%s", updated.IssueAuthor, permission, approved, updated.TaskClass, displayTargetRepo(targetRepo))
		if isReportSkip {
			eventType = core.EventDone
			detail = "汇报型 Issue，无可执行任务，已自动豁免 Claude 执行"
		} else if updated.State == core.StateDone {
			eventType = core.EventDone
			detail = "检测到 Issue 完成标记 /ccclaw [DONE]"
		} else if updated.State == core.StateBlocked {
			eventType = core.EventBlocked
			detail = blockReasonText(blockReasons)
		}
		_ = rt.store.AppendEvent(updated.TaskID, eventType, detail)
		if updated.State == core.StateBlocked {
			rt.logWarning("issue", "Issue 已阻塞", "issue", rt.issueRef(issueRepo, issue.Number), "reason", detail)
		} else {
			rt.logInfo("issue", "Issue 已入队", "issue", rt.issueRef(issueRepo, issue.Number), "task_class", updated.TaskClass, "state", updated.State, "target_repo", updated.TargetRepo)
		}
		if updated.State == core.StateBlocked && !fromRun {
			rt.reportBlocked(updated, blockReasonText(blockReasons))
		}
		if isReportSkip && !fromRun {
			rt.reportSkipped(updated, "Intent=report，Issue 内容为纯汇报，不含可执行任务")
		}
		return nil
	}
	if previousState != updated.State {
		eventType := core.EventUpdated
		detail := fmt.Sprintf("状态变更: %s -> %s", previousState, updated.State)
		if isReportSkip {
			eventType = core.EventDone
			detail = "汇报型 Issue，无可执行任务，已自动豁免 Claude 执行"
		} else if updated.State == core.StateDone {
			eventType = core.EventDone
			detail = "检测到 Issue 完成标记 /ccclaw [DONE]"
		} else if updated.State == core.StateBlocked {
			eventType = core.EventBlocked
			detail = blockReasonText(blockReasons)
		}
		_ = rt.store.AppendEvent(updated.TaskID, eventType, detail)
		if updated.State == core.StateBlocked {
			rt.logWarning("issue", "Issue 状态变更为阻塞", "issue", rt.issueRef(issueRepo, issue.Number), "reason", detail)
		} else {
			rt.logInfo("issue", "Issue 状态已更新", "issue", rt.issueRef(issueRepo, issue.Number), "task_class", updated.TaskClass, "from", previousState, "to", updated.State)
		}
		if updated.State == core.StateBlocked && !fromRun {
			rt.reportBlocked(updated, blockReasonText(blockReasons))
		}
		if isReportSkip && !fromRun {
			rt.reportSkipped(updated, "Intent=report，Issue 内容为纯汇报，不含可执行任务")
		}
	} else {
		rt.logDebug("issue", "Issue 状态无变化", "issue", rt.issueRef(issueRepo, issue.Number), "state", updated.State)
	}
	return nil
}

func (rt *Runtime) buildPrompt(task *core.Task, target *config.TargetConfig) string {
	keywords := make([]string, 0, len(task.Labels)+4)
	keywords = append(keywords, string(task.Intent), string(task.TaskClass), string(task.RiskLevel), task.IssueTitle)
	keywords = append(keywords, task.Labels...)
	matches := rt.memoryIndex(target).Match(keywords, 6)
	reportFile := filepath.ToSlash(filepath.Join("docs", "reports", core.SafeReportFileName(time.Now(), task.IssueNumber, task.IssueTitle)))

	var sb strings.Builder
	sb.WriteString("你是 ccclaw 执行器，请在目标仓库中完成以下 Issue 任务。\n\n")
	sb.WriteString(fmt.Sprintf("## 控制仓库\n- %s\n\n", task.ControlRepo))
	sb.WriteString(fmt.Sprintf("## Issue 来源仓库\n- %s\n\n", task.IssueRepo))
	sb.WriteString(fmt.Sprintf("## 目标仓库\n- repo: %s\n- local_path: %s\n- kb_path: %s\n\n", target.Repo, target.LocalPath, target.KBPath))
	sb.WriteString(fmt.Sprintf("## Issue %s#%d: %s\n\n%s\n\n", task.IssueRepo, task.IssueNumber, task.IssueTitle, task.IssueBody))
	sb.WriteString("## 元数据\n")
	sb.WriteString(fmt.Sprintf("- 发起者: %s\n- 发起者权限: %s\n- 风险等级: %s\n- 意图: %s\n- 任务分类: %s\n- 标签: %s\n", task.IssueAuthor, task.IssueAuthorPermission, task.RiskLevel, task.Intent, task.TaskClass, strings.Join(task.Labels, ", ")))
	if task.ApprovalActor != "" {
		sb.WriteString(fmt.Sprintf("- 审批人: %s\n", task.ApprovalActor))
	}
	if len(matches) > 0 {
		sb.WriteString("\n## 相关 kb 摘要\n")
		for _, doc := range matches {
			sb.WriteString(fmt.Sprintf("- 路径: %s\n  标题: %s\n  摘要: %s\n", doc.Path, doc.Title, doc.Summary))
			if len(doc.Keywords) > 0 {
				sb.WriteString(fmt.Sprintf("  关键词: %s\n", strings.Join(doc.Keywords, ", ")))
			}
		}
		sb.WriteString("- 使用方式: 以上仅为索引摘要；仅在确有必要时再按路径打开原文细节\n")
	}
	appendTaskTemplatePrompt(&sb, task)
	sb.WriteString("\n## 交付要求\n")
	sb.WriteString("- 优先修改根因，不要做表面补丁\n")
	sb.WriteString("- 保持改动最小且可验证\n")
	sb.WriteString("- 所有文档和注释使用中文\n")
	sb.WriteString(fmt.Sprintf("- 完成后必须写工程报告到 `%s`\n", reportFile))
	sb.WriteString("- 工程报告文件命名遵循 `yymmdd_[Issue No.]_[Case Summary].md`\n")
	sb.WriteString("- 不要直接执行 `gh issue comment`、`gh issue close` 等 GitHub 生命周期操作\n")
	sb.WriteString("- 需要反馈给 Issue 的结论，请写入工程报告和任务摘要，由 reporter 统一回帖并追加 `/ccclaw [DONE]`\n")
	return sb.String()
}

func (rt *Runtime) buildExecutionOptions(task *core.Task, target *config.TargetConfig) executor.RunOptions {
	basePrompt := rt.buildPrompt(task, target)
	opts := executor.RunOptions{Prompt: basePrompt}
	if !rt.shouldResumeTask(task) {
		return opts
	}
	opts.ResumeSessionID = task.LastSessionID
	opts.Prompt = rt.buildResumePrompt(task, basePrompt)
	return opts
}

func (rt *Runtime) shouldResumeTask(task *core.Task) bool {
	if task == nil {
		return false
	}
	if task.State != core.StateFailed {
		return false
	}
	if task.RetryCount <= 0 || task.RetryCount >= core.MaxRetry {
		return false
	}
	return strings.TrimSpace(task.LastSessionID) != ""
}

func (rt *Runtime) buildResumePrompt(task *core.Task, basePrompt string) string {
	var sb strings.Builder
	sb.WriteString("上次任务执行中断，请基于已有 session 上下文继续完成，不要从头重复分析。\n")
	sb.WriteString(fmt.Sprintf("恢复 session_id: %s\n", task.LastSessionID))
	if strings.TrimSpace(task.ErrorMsg) != "" {
		sb.WriteString(fmt.Sprintf("上次失败原因: %s\n", task.ErrorMsg))
	}
	sb.WriteString("\n以下是原始任务提示词，继续执行时请保持同一交付边界：\n\n")
	sb.WriteString(basePrompt)
	return sb.String()
}

func (rt *Runtime) memoryIndex(target *config.TargetConfig) *memory.Index {
	if rt == nil {
		return &memory.Index{}
	}
	kbPath := rt.memRoot
	if target != nil && strings.TrimSpace(target.KBPath) != "" {
		kbPath = target.KBPath
	}
	if strings.TrimSpace(kbPath) == "" {
		if rt.mem != nil {
			return rt.mem
		}
		return &memory.Index{}
	}
	if rt.memCache == nil {
		rt.memCache = map[string]*memory.Index{}
	}
	if idx, ok := rt.memCache[kbPath]; ok && idx != nil {
		return idx
	}
	idx, err := memory.Build(kbPath)
	if err != nil {
		if rt.mem != nil {
			return rt.mem
		}
		return &memory.Index{}
	}
	rt.memCache[kbPath] = idx
	if kbPath == rt.memRoot && rt.mem == nil {
		rt.mem = idx
	}
	return idx
}

func (rt *Runtime) newExecutor() (*executor.Executor, error) {
	return rt.newExecutorForMode(rt.cfg.ExecutorMode())
}

func (rt *Runtime) newExecutorForRepo(repo string) (*executor.Executor, config.ExecutorMode, error) {
	mode := rt.cfg.ExecutorModeForRepo(repo)
	execEngine, err := rt.newExecutorForMode(mode)
	if err != nil {
		return nil, mode, err
	}
	return execEngine, mode, nil
}

func (rt *Runtime) newExecutorForMode(mode config.ExecutorMode) (*executor.Executor, error) {
	timeout, err := time.ParseDuration(rt.cfg.Executor.Timeout)
	if err != nil {
		return nil, fmt.Errorf("解析执行超时失败: %w", err)
	}
	var manager tmux.Manager
	if mode == config.ExecutorModeTMux && tmux.Available("tmux") {
		tmuxManager, err := tmux.New("tmux")
		if err != nil {
			return nil, err
		}
		manager = tmuxManager
	}
	return executor.New(
		rt.cfg.Executor.Command,
		rt.cfg.Executor.Binary,
		timeout,
		rt.cfg.Paths.LogDir,
		filepath.Join(rt.cfg.Paths.AppDir, "var", "results"),
		rt.secrets.Values,
		manager,
	)
}

func (rt *Runtime) claudeHookStateDir() string {
	if rt == nil || rt.cfg == nil {
		return ""
	}
	return claude.DefaultHookStateDir(rt.cfg.Paths.AppDir)
}

func (rt *Runtime) ensureClaudeManagedHooks(repoPath string) error {
	if rt == nil || rt.cfg == nil {
		return nil
	}
	ccclawBin := filepath.Join(rt.cfg.Paths.AppDir, "bin", "ccclaw")
	configPath := filepath.Join(rt.cfg.Paths.AppDir, "ops", "config", "config.toml")
	return claude.EnsureManagedHookSettings(repoPath, ccclawBin, configPath)
}

func (rt *Runtime) syncTaskSessionFromHook(task *core.Task) error {
	if task == nil {
		return nil
	}
	state, err := claude.LoadHookState(rt.claudeHookStateDir(), string(task.TaskID))
	if err != nil || state == nil {
		return err
	}
	if strings.TrimSpace(state.SessionID) == "" || state.SessionID == task.LastSessionID {
		return nil
	}
	_, err = rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
		if existing == nil {
			return nil, nil
		}
		existing.LastSessionID = strings.TrimSpace(state.SessionID)
		return existing, nil
	})
	return err
}

func (rt *Runtime) maybeRestartForContextPressure(task *core.Task, sessionName string, runningFor time.Duration) (bool, error) {
	if task == nil {
		return false, nil
	}
	state, err := claude.LoadHookState(rt.claudeHookStateDir(), string(task.TaskID))
	if err != nil || state == nil {
		return false, err
	}
	if strings.TrimSpace(state.SessionID) != "" && state.SessionID != task.LastSessionID {
		task.LastSessionID = strings.TrimSpace(state.SessionID)
		if err := rt.store.UpsertTask(task); err != nil {
			return false, err
		}
	}
	if !state.PreCompactAt.IsZero() {
		reason := fmt.Sprintf("Claude 会话 %s 命中 PreCompact:%s", sessionName, displayPreCompactMatcher(state.PreCompactMatcher))
		return rt.scheduleFreshRestart(task, reason)
	}
	if strings.TrimSpace(state.TranscriptPath) == "" {
		return false, nil
	}
	metrics, err := claude.ContextMetricsFromTranscript(state.TranscriptPath, state.Model, state.ContextWindowSize)
	if err != nil || metrics == nil {
		return false, err
	}
	if metrics.UsablePercent <= contextRestartThreshold {
		return false, nil
	}
	reason := fmt.Sprintf(
		"Claude usable context 已达 %.1f%%，超过阈值 %.1f%% (context=%d usable=%d model=%s running=%s)",
		metrics.UsablePercent,
		contextRestartThreshold,
		metrics.ContextLength,
		metrics.UsableTokens,
		displayContextModel(metrics.Model),
		runningFor.Round(time.Second),
	)
	return rt.scheduleFreshRestart(task, reason)
}

func (rt *Runtime) scheduleFreshRestart(task *core.Task, reason string) (bool, error) {
	if task == nil {
		return false, nil
	}
	task.LastSessionID = ""
	task.RestartCount++
	task.ErrorMsg = strings.TrimSpace(reason)
	if task.RestartCount > maxContextRestarts {
		task.RetryCount = core.MaxRetry
		task.State = core.StateDead
		if err := rt.store.UpsertTask(task); err != nil {
			return false, err
		}
		_ = rt.store.AppendEvent(task.TaskID, core.EventDead, "上下文 fresh restart 次数已耗尽: "+task.ErrorMsg)
		rt.reportFailure(task)
		return true, nil
	}
	task.State = core.StateNew
	if err := claude.ClearHookState(rt.claudeHookStateDir(), string(task.TaskID)); err != nil {
		return false, err
	}
	if err := rt.store.UpsertTask(task); err != nil {
		return false, err
	}
	_ = rt.store.AppendEvent(task.TaskID, core.EventUpdated, "触发 fresh restart: "+task.ErrorMsg)
	rt.reportRestarted(task, task.ErrorMsg, task.RestartCount)
	return true, nil
}

func displayPreCompactMatcher(matcher string) string {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" {
		return "auto"
	}
	return matcher
}

func displayContextModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	return model
}

func (rt *Runtime) persistExecutionMetrics(task *core.Task, result *executor.Result) error {
	if task == nil || result == nil {
		return nil
	}
	if result.SessionID == "" && result.CostUSD == 0 && result.Usage == (core.TokenUsage{}) {
		return nil
	}
	if err := rt.store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     task.TaskID,
		SessionID:  result.SessionID,
		PromptFile: result.PromptFile,
		Usage:      result.Usage,
		CostUSD:    result.CostUSD,
		DurationMS: result.Duration.Milliseconds(),
		RTKEnabled: result.RTKEnabled,
	}); err != nil {
		return err
	}
	return nil
}

func (rt *Runtime) finishTaskExecution(task *core.Task, result *executor.Result, runErr error) error {
	if task == nil {
		return nil
	}
	resumeSessionID := ""
	if result != nil {
		if result.SessionID != "" {
			task.LastSessionID = result.SessionID
		}
		resumeSessionID = strings.TrimSpace(result.ResumeSessionID)
		if err := rt.persistExecutionMetrics(task, result); err != nil {
			return err
		}
	}
	if runErr != nil {
		if resumeSessionID != "" {
			task.LastSessionID = ""
			_ = rt.store.AppendEvent(task.TaskID, core.EventWarning, fmt.Sprintf("恢复 session %s 失败，后续重试将降级为新执行", resumeSessionID))
		}
		task.ResultCommentID = 0
		task.DoneCommentID = 0
		task.RetryCount++
		task.ErrorMsg = formatExecutionFailure(runErr, result)
		task.State = core.StateFailed
		eventType := core.EventFailed
		if task.RetryCount >= core.MaxRetry {
			task.State = core.StateDead
			eventType = core.EventDead
		}
		if err := rt.store.UpsertTask(task); err != nil {
			return err
		}
		_ = rt.store.AppendEvent(task.TaskID, eventType, task.ErrorMsg)
		rt.logError("execution", "任务执行失败", "issue", rt.issueRef(task.IssueRepo, task.IssueNumber), "state", task.State, "retry", task.RetryCount, "error", runErr)
		rt.reportFailure(task)
		return nil
	}
	task.State = core.StateDone
	task.ErrorMsg = ""
	task.RestartCount = 0
	task.ReportPath = filepath.ToSlash(filepath.Join("docs", "reports", core.SafeReportFileName(time.Now(), task.IssueNumber, task.IssueTitle)))
	if err := rt.store.UpsertTask(task); err != nil {
		return err
	}
	_ = rt.store.AppendEvent(task.TaskID, core.EventDone, "任务执行完成")
	rt.logInfo("execution", "任务执行完成", "issue", rt.issueRef(task.IssueRepo, task.IssueNumber), "duration", resultDuration(result), "report", task.ReportPath)
	if err := rt.syncTaskTargetRepo(task); err != nil {
		return err
	}
	if result != nil {
		if comment := rt.reportSuccess(task, result.Duration, result.LogFile); comment != nil && comment.ID != 0 {
			task.ResultCommentID = comment.ID
			task.DoneCommentID = comment.ID
			if err := rt.store.UpsertTask(task); err != nil {
				return err
			}
		}
	}
	return nil
}

func (rt *Runtime) finalizePatrolSession(task *core.Task, execEngine *executor.Executor, status tmux.SessionStatus) error {
	result, resultErr := execEngine.LoadResult(task.TaskID)
	result = enrichDiagnosticResult(execEngine, task.TaskID, result)
	if result != nil {
		result.ExitCode = status.ExitCode
	}
	if resultErr == nil && status.ExitCode != 0 {
		resultErr = fmt.Errorf("tmux 会话退出码=%d", status.ExitCode)
	}
	return rt.finishTaskExecution(task, result, resultErr)
}

func (rt *Runtime) finalizeMissingSession(task *core.Task, execEngine *executor.Executor) (bool, error) {
	result, resultErr := execEngine.LoadResult(task.TaskID)
	result = enrichDiagnosticResult(execEngine, task.TaskID, result)
	if result == nil && resultErr != nil {
		return false, nil
	}
	return true, rt.finishTaskExecution(task, result, resultErr)
}

func (rt *Runtime) markTaskDead(task *core.Task, reason, logFile string) error {
	task.RetryCount++
	task.ErrorMsg = reason
	task.DoneCommentID = 0
	task.State = core.StateDead
	if err := rt.store.UpsertTask(task); err != nil {
		return err
	}
	_ = rt.store.AppendEvent(task.TaskID, core.EventDead, reason)
	rt.logError("execution", "任务已标记为终止", "issue", rt.issueRef(task.IssueRepo, task.IssueNumber), "retry", task.RetryCount, "reason", reason)
	rt.reportFailure(task)
	_ = logFile
	return nil
}

func formatExecutionFailure(runErr error, result *executor.Result) string {
	if runErr == nil {
		return ""
	}
	message := strings.TrimSpace(runErr.Error())
	diagnostic := ""
	if result != nil {
		diagnostic = summarizeDiagnosticOutput(result.Output)
		if diagnostic == "" {
			diagnostic = readDiagnosticTail(result.LogFile, 12)
		}
	}
	if diagnostic == "" || strings.Contains(message, diagnostic) {
		return message
	}
	return fmt.Sprintf("%s；诊断输出: %s", message, diagnostic)
}

func enrichDiagnosticResult(execEngine *executor.Executor, taskID string, result *executor.Result) *executor.Result {
	if execEngine == nil {
		return result
	}
	artifacts := execEngine.ArtifactPaths(taskID)
	if result == nil {
		diagnostic := readDiagnosticTail(artifacts.DiagnosticFile, 12)
		if diagnostic == "" {
			diagnostic = readDiagnosticTail(artifacts.LogFile, 12)
		}
		if diagnostic == "" {
			return nil
		}
		return &executor.Result{
			Output:         diagnostic,
			LogFile:        artifacts.LogFile,
			ResultFile:     artifacts.ResultFile,
			DiagnosticFile: artifacts.DiagnosticFile,
			MetaFile:       artifacts.MetaFile,
		}
	}
	if strings.TrimSpace(result.Output) == "" {
		result.Output = readDiagnosticTail(result.DiagnosticFile, 12)
		if strings.TrimSpace(result.Output) == "" {
			result.Output = readDiagnosticTail(result.LogFile, 12)
		}
	}
	return result
}

func readDiagnosticTail(path string, maxLines int) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(payload), "\r\n", "\n"), "\n")
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		trimmed = append(trimmed, line)
	}
	if len(trimmed) == 0 {
		return ""
	}
	if maxLines > 0 && len(trimmed) > maxLines {
		trimmed = trimmed[len(trimmed)-maxLines:]
	}
	return summarizeDiagnosticOutput(strings.Join(trimmed, "\n"))
}

func summarizeDiagnosticOutput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	summary := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		summary = append(summary, part)
	}
	if len(summary) == 0 {
		return ""
	}
	text := strings.Join(summary, " | ")
	if len(text) > 400 {
		return strings.TrimSpace(text[:397]) + "..."
	}
	return text
}

func (rt *Runtime) reportBlocked(task *core.Task, reason string) {
	if rt.rep == nil {
		return
	}
	_ = rt.rep.ReportBlocked(task, reason)
}

func (rt *Runtime) reportSkipped(task *core.Task, reason string) {
	if rt.rep == nil {
		return
	}
	_ = rt.rep.ReportSkipped(task, reason)
}

func (rt *Runtime) reportFailure(task *core.Task) {
	if rt.rep == nil {
		return
	}
	_ = rt.rep.ReportFailure(task)
}

func (rt *Runtime) reportSuccess(task *core.Task, duration time.Duration, logFile string) *github.Comment {
	if rt.rep == nil {
		return nil
	}
	comment, _ := rt.rep.ReportSuccess(task, duration, logFile)
	return comment
}

func (rt *Runtime) reportResultReady(task *core.Task, duration time.Duration, logFile string) *github.Comment {
	if rt.rep == nil {
		return nil
	}
	comment, _ := rt.rep.ReportResultReady(task, duration, logFile)
	return comment
}

func (rt *Runtime) promoteResultCommentToDone(task *core.Task, duration time.Duration, logFile string, commentID int64) *github.Comment {
	if rt.rep == nil {
		return nil
	}
	comment, _ := rt.rep.PromoteResultToDone(task, duration, logFile, commentID)
	return comment
}

func (rt *Runtime) reportStarted(task *core.Task, sessionName string) {
	if rt.rep == nil {
		return
	}
	_ = rt.rep.ReportStarted(task, sessionName)
}

func (rt *Runtime) reportRestarted(task *core.Task, reason string, restartCount int) {
	if rt.rep == nil {
		return
	}
	_ = rt.rep.ReportRestarted(task, reason, restartCount, maxContextRestarts)
}

func (rt *Runtime) describeSchedulerStatus() (string, error) {
	return scheduler.DescribeStatus(rt.cfg)
}

func (rt *Runtime) SchedulerStatus() (string, error) {
	return rt.describeSchedulerStatus()
}

func (rt *Runtime) checkDisk() error {
	paths := []string{rt.cfg.Paths.VarDir, rt.cfg.Paths.LogDir}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			return err
		}
		if stat.Blocks == 0 {
			continue
		}
		freePercent := float64(stat.Bavail) / float64(stat.Blocks) * 100
		if freePercent < 10 {
			return fmt.Errorf("磁盘剩余空间不足 10%%: %s (%.2f%%)", path, freePercent)
		}
	}
	return nil
}

func systemctlUser(action, unit string) (string, error) {
	cmd := exec.Command("systemctl", "--user", action, unit)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err == nil {
		if text == "" {
			text = unit
		}
		return text, nil
	}
	if text == "" {
		text = err.Error()
	}
	return "", fmt.Errorf("systemctl --user %s %s 失败: %s", action, unit, text)
}

func requireCommand(name string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("未找到 %s: %w", name, err)
	}
	return nil
}

func commandExists(command []string, binary string) error {
	if len(command) > 0 {
		return requireCommand(command[0])
	}
	if binary == "" {
		binary = "claude"
	}
	return requireCommand(binary)
}

func (rt *Runtime) resolveTargetRepo(issueRepo, body string) (string, []string) {
	explicitTarget := parseTargetRepo(body)
	if explicitTarget != "" {
		if _, err := rt.cfg.EnabledTargetByRepo(explicitTarget); err != nil {
			return "", []string{fmt.Sprintf("Issue 指定的 `target_repo: %s` 不可用: %v", explicitTarget, err)}
		}
		return explicitTarget, nil
	}
	if issueRepo != "" {
		if _, err := rt.cfg.EnabledTargetByRepo(issueRepo); err == nil {
			return issueRepo, nil
		}
	}
	if rt.cfg.DefaultTarget != "" {
		if _, err := rt.cfg.EnabledTargetByRepo(rt.cfg.DefaultTarget); err != nil {
			return "", []string{fmt.Sprintf("default_target `%s` 不可用: %v", rt.cfg.DefaultTarget, err)}
		}
		return rt.cfg.DefaultTarget, nil
	}
	if len(rt.cfg.EnabledTargets()) == 0 {
		return "", []string{"当前未绑定任何可用 target，请先执行 `ccclaw target add --repo owner/repo --path /path`"}
	}
	return "", []string{"Issue 未声明 `target_repo:`，且当前未配置 `default_target`"}
}

func parseTargetRepo(body string) string {
	matches := regexp.MustCompile(issueTargetRepoPattern).FindStringSubmatch(body)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func blockReasonText(reasons []string) string {
	if len(reasons) == 0 {
		return "无"
	}
	return strings.Join(reasons, "；")
}

func approvalCommandHelp(words []string) string {
	normalized := normalizeApprovalWords(words)
	if len(normalized) == 0 {
		return fmt.Sprintf("`%s approve`", github.ApprovalPrefix)
	}
	if len(normalized) == 1 {
		return fmt.Sprintf("`%s %s`", github.ApprovalPrefix, normalized[0])
	}
	return fmt.Sprintf(
		"`%s %s`（同义词: %s）",
		github.ApprovalPrefix,
		normalized[0],
		strings.Join(normalized[1:], " / "),
	)
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

func displayTargetRepo(repo string) string {
	if repo == "" {
		return "-"
	}
	return repo
}

func appendTaskTemplatePrompt(sb *strings.Builder, task *core.Task) {
	if sb == nil || task == nil {
		return
	}
	switch task.TaskClass {
	case core.TaskClassSevolverDeepAnalysis:
		sb.WriteString("\n## 专项模板\n")
		sb.WriteString("- 本任务属于 `sevolver_deep_analysis`，是由 sevolver 自动升级的自进化分析单。\n")
		sb.WriteString("- 先按 issue 中的 gap 样本与 fingerprint 收敛根因，禁止把样本逐条当成独立 bug 机械修补。\n")
		sb.WriteString("- 优先判断问题落点属于 `internal/sevolver`、`kb/skills/`、`kb/assay/` 还是 `ingest/run` 执行链路。\n")
		sb.WriteString("- 若需补知识资产，必须同步考虑 `gap-signals`、skill frontmatter 和后续闭环状态。\n")
	case core.TaskClassSevolver:
		sb.WriteString("\n## 专项模板\n")
		sb.WriteString("- 本任务属于 `sevolver` 自进化链路相关事项，修改时优先保持长期闭环与可观测性。\n")
	}
}

func (rt *Runtime) describeTargetRouting() string {
	enabled := rt.cfg.EnabledTargets()
	return fmt.Sprintf("enabled=%d default_target=%s", len(enabled), displayTargetRepo(rt.cfg.DefaultTarget))
}

func (rt *Runtime) describeExecutor() string {
	snapshot := rt.executorSnapshot()
	return fmt.Sprintf(
		"configured=%s resolved=%s claude=%s rtk=%s",
		snapshot.LauncherRequested,
		formatResolvedBinary(snapshot.LauncherRequested, snapshot.LauncherResolved, snapshot.LauncherError),
		formatResolvedBinary(snapshot.ClaudeRequested, snapshot.ClaudeResolved, snapshot.ClaudeError),
		formatResolvedBinary(snapshot.RTKRequested, snapshot.RTKResolved, snapshot.RTKError),
	)
}

func (rt *Runtime) describeBinaryVersion(envKey, fallback string, args ...string) (string, error) {
	requested := ""
	if rt.secrets != nil {
		requested = strings.TrimSpace(rt.secrets.Values[envKey])
	}
	if requested == "" {
		requested = fallback
	}
	resolved, err := executor.ResolveBinaryPath(requested)
	if err != nil {
		return fmt.Sprintf("requested=%s", requested), err
	}
	cmd := exec.Command(resolved, args...)
	output, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		return fmt.Sprintf("requested=%s resolved=%s", requested, resolved), nil
	}
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if line == "" {
		line = resolved
	}
	return fmt.Sprintf("requested=%s resolved=%s version=%s", requested, resolved, line), nil
}

func (rt *Runtime) checkClaudeInstallChannel(ctx context.Context) error {
	return probeHTTP(ctx, claudeInstallScriptURL, "")
}

func (rt *Runtime) detectClaudeState(ctx context.Context) (string, error) {
	if err := requireCommand("claude"); err != nil {
		if probeErr := rt.checkClaudeInstallChannel(ctx); probeErr != nil {
			return claudeStateInstallBlocked, fmt.Errorf("未找到 claude，且官方安装通道不可达: %w", probeErr)
		}
		return claudeStateMissingCLI, errors.New("未找到 claude CLI")
	}
	if err := rt.checkClaudeProxyMode(ctx); err == nil {
		return claudeStateInstalledProxy, nil
	}
	if err := rt.checkClaudeSetupToken(ctx); err == nil {
		return claudeStateInstalledSetup, nil
	}
	return claudeStateInstalledBare, errors.New("claude 已安装，但 `setup-token` 或代理模式均未就绪")
}

func (rt *Runtime) checkClaudeSetupToken(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "claude", "auth", "status", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("`claude auth status --json` 失败: %s", strings.TrimSpace(string(output)))
	}
	var payload map[string]any
	if err := json.Unmarshal(output, &payload); err != nil {
		return fmt.Errorf("解析 `claude auth status --json` 失败: %w", err)
	}
	return nil
}

func (rt *Runtime) checkClaudeProxyMode(ctx context.Context) error {
	baseURL := strings.TrimSpace(rt.secrets.Values["ANTHROPIC_BASE_URL"])
	token := strings.TrimSpace(rt.secrets.Values["ANTHROPIC_AUTH_TOKEN"])
	if baseURL == "" || token == "" {
		return errors.New("缺少 ANTHROPIC_BASE_URL 或 ANTHROPIC_AUTH_TOKEN")
	}
	if len(rt.cfg.Executor.Command) == 0 {
		return errors.New("executor.command 未配置代理包装器")
	}
	if err := requireCommand(rt.cfg.Executor.Command[0]); err != nil {
		return fmt.Errorf("代理包装器不可执行: %w", err)
	}
	return probeHTTP(ctx, baseURL, token)
}

func (rt *Runtime) checkPasswordlessSudo() (string, error) {
	if err := requireCommand("sudo"); err != nil {
		return "", err
	}
	cmd := exec.Command("sudo", "-n", "true")
	if output, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = manualSudoGuide
		}
		return manualSudoGuide, errors.New(msg)
	}
	return "已开启", nil
}

func commandVersion(name string, args ...string) (string, error) {
	if err := requireCommand(name); err != nil {
		return "", err
	}
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return name, nil
	}
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	return line, nil
}

func (rt *Runtime) Journal(day time.Time, out io.Writer) error {
	defer rt.store.Close()
	if day.IsZero() {
		day = time.Now()
	}
	targetRepos := rt.journalTargetRepos()
	rt.logInfo("journal", "开始生成日报", "day", day.Format("2006-01-02"), "targets", len(targetRepos), "log_level", rt.runtimeLogLevel())
	writtenPaths := make([]string, 0, len(targetRepos)+3)
	for _, targetRepo := range targetRepos {
		summary, err := rt.store.JournalDaySummaryByTarget(day, targetRepo)
		if err != nil {
			return err
		}
		comparison, err := rt.store.RTKComparisonBetweenByTarget(dayStart(day), dayEnd(day), targetRepo)
		if err != nil {
			return err
		}
		tasks, err := rt.store.JournalTaskSummariesByTarget(day, targetRepo)
		if err != nil {
			return err
		}
		events, err := rt.store.ListTaskEventsBetweenByTarget(dayStart(day), dayEnd(day), 100, targetRepo)
		if err != nil {
			return err
		}
		path := rt.journalFilePath(day, targetRepo)
		if len(targetRepos) == 1 {
			if err := rt.migrateLegacyJournalFile(day, targetRepo, path); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("创建 journal 目录失败: %w", err)
		}
		content := rt.renderJournal(day, targetRepo, summary, comparison, tasks, events)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("写入 journal 失败: %w", err)
		}
		writtenPaths = append(writtenPaths, path)
		rt.logInfo("journal", "日报已生成", "day", day.Format("2006-01-02"), "target_repo", targetRepo, "path", path)
		_, _ = fmt.Fprintf(out, "journal 已生成: %s\n", path)
	}
	summaryPaths, err := rt.refreshJournalSummaries(day)
	if err != nil {
		return err
	}
	writtenPaths = append(writtenPaths, summaryPaths...)
	if homeRepo := strings.TrimSpace(rt.cfg.Paths.HomeRepo); homeRepo != "" && len(writtenPaths) > 0 {
		if err := rt.repoSync(homeRepo, fmt.Sprintf("journal: %s", day.Format("2006-01-02")), writtenPaths, 3); err != nil {
			rt.logWarning("journal", "知识仓库同步失败", "repo", homeRepo, "error", err)
			_, _ = fmt.Fprintf(out, "警告: 知识仓库同步失败: %v\n", err)
		}
	}
	rt.logInfo("journal", "日报生成完成", "day", day.Format("2006-01-02"), "files", len(writtenPaths))
	// 冷启动 recall：刷新 kb/context.md
	if recallErr := rt.Recall(RecallOptions{Cold: true}, out); recallErr != nil {
		rt.logWarning("journal", "recall 冷启动失败（非阻塞）", "error", recallErr)
		_, _ = fmt.Fprintf(out, "警告: recall 冷启动失败: %v\n", recallErr)
	}
	return nil
}

func (rt *Runtime) journalFilePath(day time.Time, targetRepo string) string {
	return filepath.Join(
		rt.journalMonthDir(day),
		fmt.Sprintf("%s.%s.%s.ccclaw_log.md", day.Format("2006.01.02"), journalUserName(), journalRepoFileSlug(targetRepo)),
	)
}

func (rt *Runtime) journalRootDir() string {
	return filepath.Join(rt.cfg.Paths.HomeRepo, "kb", "journal")
}

func (rt *Runtime) journalYearDir(day time.Time) string {
	return filepath.Join(rt.journalRootDir(), day.Format("2006"))
}

func (rt *Runtime) journalMonthDir(day time.Time) string {
	year := day.Format("2006")
	month := day.Format("01")
	return filepath.Join(rt.journalRootDir(), year, month)
}

func (rt *Runtime) refreshJournalSummaries(day time.Time) ([]string, error) {
	monthPath, err := rt.writeJournalMonthSummary(day)
	if err != nil {
		return nil, err
	}
	yearPath, err := rt.writeJournalYearSummary(day)
	if err != nil {
		return nil, err
	}
	rootPath, err := rt.writeJournalRootSummary()
	if err != nil {
		return nil, err
	}
	return []string{monthPath, yearPath, rootPath}, nil
}

func (rt *Runtime) writeJournalMonthSummary(day time.Time) (string, error) {
	monthStart := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
	monthEnd := monthStart.AddDate(0, 1, 0)
	summary, err := rt.store.JournalSummaryBetween(monthStart, monthEnd)
	if err != nil {
		return "", err
	}
	comparison, err := rt.store.RTKComparisonBetween(monthStart, monthEnd)
	if err != nil {
		return "", err
	}
	files, err := collectJournalFiles(rt.journalMonthDir(day), ".ccclaw_log.md")
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(fmt.Sprintf("## 范围\n\n- 月份: %s\n- 日志文件数: %d\n", monthStart.Format("2006-01"), len(files)))
	body.WriteString(renderJournalPeriodMetrics(summary, comparison))
	body.WriteString("\n## 日志入口\n\n")
	if len(files) == 0 {
		body.WriteString("- 当月暂无日报\n")
	} else {
		for _, name := range files {
			dayLabel := strings.TrimSuffix(name, ".ccclaw_log.md")
			body.WriteString(fmt.Sprintf("- [%s](./%s)\n", dayLabel, name))
		}
	}
	path := filepath.Join(rt.journalMonthDir(day), "summary.md")
	return path, writeManagedJournalFile(
		path,
		fmt.Sprintf("# ccclaw journal 月汇总 %s", monthStart.Format("2006-01")),
		body.String(),
	)
}

func (rt *Runtime) writeJournalYearSummary(day time.Time) (string, error) {
	yearStart := time.Date(day.Year(), 1, 1, 0, 0, 0, 0, day.Location())
	yearEnd := yearStart.AddDate(1, 0, 0)
	summary, err := rt.store.JournalSummaryBetween(yearStart, yearEnd)
	if err != nil {
		return "", err
	}
	comparison, err := rt.store.RTKComparisonBetween(yearStart, yearEnd)
	if err != nil {
		return "", err
	}
	monthDirs, err := collectJournalSubdirs(rt.journalYearDir(day))
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(fmt.Sprintf("## 范围\n\n- 年度: %s\n- 月目录数: %d\n", yearStart.Format("2006"), len(monthDirs)))
	body.WriteString(renderJournalPeriodMetrics(summary, comparison))
	body.WriteString("\n## 月度入口\n\n")
	if len(monthDirs) == 0 {
		body.WriteString("- 当年暂无月度归档\n")
	} else {
		for _, month := range monthDirs {
			body.WriteString(fmt.Sprintf("- [%s](./%s/summary.md)\n", month, month))
		}
	}
	path := filepath.Join(rt.journalYearDir(day), "summary.md")
	return path, writeManagedJournalFile(
		path,
		fmt.Sprintf("# ccclaw journal 年汇总 %s", yearStart.Format("2006")),
		body.String(),
	)
}

func (rt *Runtime) writeJournalRootSummary() (string, error) {
	summary, err := rt.store.JournalSummaryBetween(time.Time{}, time.Time{})
	if err != nil {
		return "", err
	}
	comparison, err := rt.store.RTKComparisonBetween(time.Time{}, time.Time{})
	if err != nil {
		return "", err
	}
	yearDirs, err := collectJournalSubdirs(rt.journalRootDir())
	if err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString(fmt.Sprintf("## 范围\n\n- 年度目录数: %d\n", len(yearDirs)))
	body.WriteString(renderJournalPeriodMetrics(summary, comparison))
	body.WriteString("\n## 年度入口\n\n")
	if len(yearDirs) == 0 {
		body.WriteString("- 当前暂无年度归档\n")
	} else {
		for _, year := range yearDirs {
			body.WriteString(fmt.Sprintf("- [%s](./%s/summary.md)\n", year, year))
		}
	}
	path := filepath.Join(rt.journalRootDir(), "summary.md")
	return path, writeManagedJournalFile(
		path,
		"# ccclaw journal 总览",
		body.String(),
	)
}

func renderJournalPeriodMetrics(summary *storage.JournalDaySummary, comparison *storage.RTKComparison) string {
	if summary == nil {
		summary = &storage.JournalDaySummary{}
	}
	if comparison == nil {
		comparison = &storage.RTKComparison{}
	}
	var sb strings.Builder
	sb.WriteString("\n## 聚合指标\n\n")
	sb.WriteString(fmt.Sprintf("- 任务触达数: %d\n", summary.TasksTouched))
	sb.WriteString(fmt.Sprintf("- 启动任务数: %d\n", summary.Started))
	sb.WriteString(fmt.Sprintf("- 完成任务数: %d\n", summary.Done))
	sb.WriteString(fmt.Sprintf("- 失败任务数: %d\n", summary.Failed))
	sb.WriteString(fmt.Sprintf("- 僵死任务数: %d\n", summary.Dead))
	sb.WriteString(fmt.Sprintf("- 输入 tokens: %d\n", summary.InputTokens))
	sb.WriteString(fmt.Sprintf("- 输出 tokens: %d\n", summary.OutputTokens))
	sb.WriteString(fmt.Sprintf("- 缓存创建 tokens: %d\n", summary.CacheCreate))
	sb.WriteString(fmt.Sprintf("- 缓存命中 tokens: %d\n", summary.CacheRead))
	sb.WriteString(fmt.Sprintf("- 累计成本(USD): %.4f\n", summary.CostUSD))
	sb.WriteString(fmt.Sprintf("- RTK runs: %d\n", comparison.RTKRuns))
	sb.WriteString(fmt.Sprintf("- Plain runs: %d\n", comparison.PlainRuns))
	sb.WriteString(fmt.Sprintf("- RTK 平均成本(USD): %.4f\n", comparison.RTKAvgCostUSD))
	sb.WriteString(fmt.Sprintf("- 非 RTK 平均成本(USD): %.4f\n", comparison.PlainAvgCostUSD))
	sb.WriteString(fmt.Sprintf("- 估算节省比例: %.2f%%\n", comparison.SavingsPercent))
	return sb.String()
}

func collectJournalFiles(dir, suffix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 journal 目录失败: %w", err)
	}
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "summary.md" || !strings.HasSuffix(name, suffix) {
			continue
		}
		items = append(items, name)
	}
	return items, nil
}

func collectJournalSubdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 journal 子目录失败: %w", err)
	}
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			items = append(items, entry.Name())
		}
	}
	return items, nil
}

func writeManagedJournalFile(path, title, managedBody string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 journal summary 目录失败: %w", err)
	}
	userBody, err := readJournalUserBody(path)
	if err != nil {
		return err
	}
	content := buildManagedJournalFile(title, managedBody, userBody)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 journal summary 失败: %w", err)
	}
	return nil
}

func readJournalUserBody(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return journalUserPlaceholder, nil
		}
		return "", fmt.Errorf("读取 journal summary 失败: %w", err)
	}
	text := string(payload)
	start := strings.Index(text, journalUserStartTag)
	end := strings.Index(text, journalUserEndTag)
	if start < 0 || end < 0 || end < start {
		return journalUserPlaceholder, nil
	}
	body := strings.TrimSpace(text[start+len(journalUserStartTag) : end])
	if body == "" {
		return journalUserPlaceholder, nil
	}
	return body, nil
}

func buildManagedJournalFile(title, managedBody, userBody string) string {
	var sb strings.Builder
	sb.WriteString(title)
	sb.WriteString("\n\n")
	sb.WriteString(journalManagedStartTag)
	sb.WriteString("\n")
	sb.WriteString(strings.TrimSpace(managedBody))
	sb.WriteString("\n")
	sb.WriteString(journalManagedEndTag)
	sb.WriteString("\n\n")
	sb.WriteString(journalUserStartTag)
	sb.WriteString("\n")
	sb.WriteString(strings.TrimSpace(userBody))
	sb.WriteString("\n")
	sb.WriteString(journalUserEndTag)
	sb.WriteString("\n")
	return sb.String()
}

func (rt *Runtime) renderJournal(day time.Time, targetRepo string, summary *storage.JournalDaySummary, comparison *storage.RTKComparison, tasks []storage.JournalTaskSummary, events []storage.EventRecord) string {
	var sb strings.Builder
	title := fmt.Sprintf("# ccclaw journal %s", day.Format("2006-01-02"))
	if strings.TrimSpace(targetRepo) != "" {
		title += fmt.Sprintf(" [%s]", targetRepo)
	}
	sb.WriteString(title + "\n\n")
	sb.WriteString("## 日汇总\n\n")
	sb.WriteString(fmt.Sprintf("- 任务触达数: %d\n", summary.TasksTouched))
	sb.WriteString(fmt.Sprintf("- 启动任务数: %d\n", summary.Started))
	sb.WriteString(fmt.Sprintf("- 完成任务数: %d\n", summary.Done))
	sb.WriteString(fmt.Sprintf("- 失败任务数: %d\n", summary.Failed))
	sb.WriteString(fmt.Sprintf("- 僵死任务数: %d\n", summary.Dead))
	sb.WriteString(fmt.Sprintf("- 输入 tokens: %d\n", summary.InputTokens))
	sb.WriteString(fmt.Sprintf("- 输出 tokens: %d\n", summary.OutputTokens))
	sb.WriteString(fmt.Sprintf("- 缓存创建 tokens: %d\n", summary.CacheCreate))
	sb.WriteString(fmt.Sprintf("- 缓存命中 tokens: %d\n", summary.CacheRead))
	sb.WriteString(fmt.Sprintf("- 累计成本(USD): %.4f\n", summary.CostUSD))
	sb.WriteString(fmt.Sprintf("- RTK 平均成本(USD): %.4f\n", comparison.RTKAvgCostUSD))
	sb.WriteString(fmt.Sprintf("- 非 RTK 平均成本(USD): %.4f\n", comparison.PlainAvgCostUSD))
	sb.WriteString(fmt.Sprintf("- 估算节省比例: %.2f%%\n", comparison.SavingsPercent))

	sb.WriteString("\n## 任务摘要\n\n")
	if len(tasks) == 0 {
		sb.WriteString("- 当日无任务变更\n")
	} else {
		for _, item := range tasks {
			sb.WriteString(fmt.Sprintf("- %s#%d `%s` 状态=`%s` runs=%d cost=%.4f tokens=%d/%d cache=%d/%d updated_at=%s\n",
				item.IssueRepo,
				item.IssueNumber,
				item.IssueTitle,
				item.State,
				item.Runs,
				item.CostUSD,
				item.InputTokens,
				item.OutputTokens,
				item.CacheCreate,
				item.CacheRead,
				formatJournalTime(item.UpdatedAt),
			))
		}
	}

	sb.WriteString("\n## 巡查事件\n\n")
	patrolEvents := filterPatrolEvents(events)
	if len(patrolEvents) == 0 {
		sb.WriteString("- 当日无巡查事件\n")
	} else {
		for _, event := range patrolEvents {
			sb.WriteString(fmt.Sprintf("- %s [%s] %s %s\n",
				formatJournalTime(event.CreatedAt),
				event.EventType,
				event.TaskID,
				strings.TrimSpace(event.Detail),
			))
		}
	}
	return sb.String()
}

func filterPatrolEvents(events []storage.EventRecord) []storage.EventRecord {
	filtered := make([]storage.EventRecord, 0, len(events))
	for _, event := range events {
		detail := strings.ToLower(strings.TrimSpace(event.Detail))
		if strings.Contains(detail, "tmux") || strings.Contains(detail, "巡查") {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func (rt *Runtime) syncTaskTargetRepo(task *core.Task) error {
	if task == nil || rt.cfg == nil || strings.TrimSpace(task.TargetRepo) == "" {
		return nil
	}
	target, err := rt.cfg.EnabledTargetByRepo(task.TargetRepo)
	if err != nil || target == nil {
		return nil
	}
	if err := rt.repoSync(target.LocalPath, fmt.Sprintf("task done: %s#%d %s", task.IssueRepo, task.IssueNumber, task.IssueTitle), nil, 3); err != nil {
		_ = rt.store.AppendEvent(task.TaskID, core.EventWarning, fmt.Sprintf("任务仓库同步失败: %v", err))
	}
	return nil
}

func (rt *Runtime) syncReposAfterPatrol(out io.Writer) {
	if rt == nil || rt.cfg == nil {
		return
	}
	for _, target := range rt.cfg.EnabledTargets() {
		if err := rt.repoSync(target.LocalPath, "patrol: sync", nil, 3); err != nil {
			_, _ = fmt.Fprintf(out, "警告: %s 同步失败: %v\n", target.Repo, err)
		}
	}
	if homeRepo := strings.TrimSpace(rt.cfg.Paths.HomeRepo); homeRepo != "" {
		if err := rt.repoSync(homeRepo, "patrol: sync home", nil, 3); err != nil {
			_, _ = fmt.Fprintf(out, "警告: 知识仓库同步失败: %v\n", err)
		}
	}
}

func (rt *Runtime) journalTargetRepos() []string {
	if rt == nil || rt.cfg == nil {
		return nil
	}
	targets := rt.cfg.EnabledTargets()
	if len(targets) == 0 {
		repo := strings.TrimSpace(rt.cfg.GitHub.ControlRepo)
		if repo == "" {
			return nil
		}
		return []string{repo}
	}
	repos := make([]string, 0, len(targets))
	for _, target := range targets {
		if repo := strings.TrimSpace(target.Repo); repo != "" {
			repos = append(repos, repo)
		}
	}
	return repos
}

func (rt *Runtime) migrateLegacyJournalFile(day time.Time, targetRepo, targetPath string) error {
	if strings.TrimSpace(targetRepo) == "" || strings.TrimSpace(targetPath) == "" {
		return nil
	}
	legacyPath := filepath.Join(rt.journalMonthDir(day), fmt.Sprintf("%s.%s.ccclaw_log.md", day.Format("2006.01.02"), journalUserName()))
	if legacyPath == targetPath {
		return nil
	}
	if _, err := os.Stat(legacyPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取旧版 journal 文件失败: %w", err)
	}
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取目标 journal 文件失败: %w", err)
	}
	if err := os.Rename(legacyPath, targetPath); err != nil {
		return fmt.Errorf("迁移旧版 journal 文件失败: %w", err)
	}
	return nil
}

func journalUserName() string {
	for _, key := range []string{"USER", "LOGNAME", "USERNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "unknown"
}

func journalRepoFileSlug(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "unknown-repo"
	}
	return strings.NewReplacer("/", "-", "\\", "-").Replace(repo)
}

func formatJournalTime(ts time.Time) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.Format(time.RFC3339)
}

func dayStart(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
}

func dayEnd(day time.Time) time.Time {
	return dayStart(day).Add(24 * time.Hour)
}

func probeHTTP(ctx context.Context, rawURL, bearerToken string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL 无效: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("URL 不完整: %s", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("上游返回 %d", resp.StatusCode)
	}
	return nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if len(keys) < 2 {
		return keys
	}
	for i := 0; i < len(keys)-1; i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
