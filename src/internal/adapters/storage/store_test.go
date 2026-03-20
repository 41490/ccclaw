package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/core"
)

func TestStorePersistsLastSessionIDAndTokenStats(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         "10#body",
		IdempotencyKey: "10#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		LastSessionID:  "sess-10",
		IssueNumber:    10,
		IssueTitle:     "phase1 stats",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentResearch,
		RiskLevel:      core.RiskLow,
		State:          core.StateDone,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := store.RecordTokenUsage(TokenUsageRecord{
		TaskID:     task.TaskID,
		SessionID:  "sess-10",
		PromptFile: "/tmp/prompts/10_body.md",
		Usage: core.TokenUsage{
			InputTokens:              100,
			OutputTokens:             40,
			CacheCreationInputTokens: 10,
			CacheReadInputTokens:     5,
		},
		CostUSD:    0.42,
		DurationMS: 9000,
		RecordedAt: time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 token 记录失败: %v", err)
	}

	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.LastSessionID != "sess-10" {
		t.Fatalf("unexpected task session: %#v", loaded)
	}

	summary, err := store.TokenStats()
	if err != nil {
		t.Fatalf("读取统计失败: %v", err)
	}
	if summary.Runs != 1 || summary.Sessions != 1 {
		t.Fatalf("unexpected summary counts: %#v", summary)
	}
	if summary.InputTokens != 100 || summary.OutputTokens != 40 {
		t.Fatalf("unexpected summary tokens: %#v", summary)
	}
	if summary.CostUSD != 0.42 {
		t.Fatalf("unexpected summary cost: %#v", summary)
	}

	stats, err := store.TaskTokenStats(10)
	if err != nil {
		t.Fatalf("读取任务维度统计失败: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("unexpected stats count: %d", len(stats))
	}
	if stats[0].IssueNumber != 10 || stats[0].LastSessionID != "sess-10" {
		t.Fatalf("unexpected task stat: %#v", stats[0])
	}
}

func TestStoreMigratesLegacyRepoSlotToGlobalSidecar(t *testing.T) {
	varDir := filepath.Join(t.TempDir(), "var")
	store, err := Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	legacy := &RepoSlot{
		TargetRepo:   "41490/ccclaw",
		TaskID:       "21#body",
		ExecutorMode: "daemon",
		Phase:        RepoSlotPhaseFinalizing,
		CurrentStep:  "sync_home",
		UpdatedAt:    time.Date(2026, 3, 19, 15, 0, 0, 0, time.UTC),
	}
	payload, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("序列化 legacy sidecar 失败: %v", err)
	}
	legacyPath := filepath.Join(varDir, "runtime", sanitizeRepoKey(legacy.TargetRepo)+".json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("创建 runtime 目录失败: %v", err)
	}
	if err := os.WriteFile(legacyPath, payload, 0o644); err != nil {
		t.Fatalf("写入 legacy sidecar 失败: %v", err)
	}

	loaded, err := store.GetActiveRepoSlot()
	if err != nil {
		t.Fatalf("迁移 legacy sidecar 失败: %v", err)
	}
	if loaded == nil || loaded.TaskID != legacy.TaskID || loaded.TargetRepo != legacy.TargetRepo {
		t.Fatalf("unexpected active sidecar: %#v", loaded)
	}

	activePath := filepath.Join(varDir, "runtime", globalRepoSlotFileName)
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("预期已写入全局 sidecar，实际错误: %v", err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("预期 legacy sidecar 已被回收，实际错误: %v", err)
	}

	compat, err := store.GetRepoSlot(legacy.TargetRepo)
	if err != nil {
		t.Fatalf("兼容读取迁移后 sidecar 失败: %v", err)
	}
	if compat == nil || compat.TaskID != legacy.TaskID {
		t.Fatalf("unexpected compat sidecar: %#v", compat)
	}
}

func TestStoreRejectsAmbiguousLegacyRepoSlotsDuringMigration(t *testing.T) {
	varDir := filepath.Join(t.TempDir(), "var")
	store, err := Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	cases := []*RepoSlot{
		{
			TargetRepo: "41490/repo-a",
			TaskID:     "91#a",
			Phase:      RepoSlotPhaseRunning,
			UpdatedAt:  time.Date(2026, 3, 19, 16, 0, 0, 0, time.UTC),
		},
		{
			TargetRepo: "41490/repo-b",
			TaskID:     "91#b",
			Phase:      RepoSlotPhaseFinalizeFailed,
			UpdatedAt:  time.Date(2026, 3, 19, 16, 5, 0, 0, time.UTC),
		},
	}
	runtimeDir := filepath.Join(varDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("创建 runtime 目录失败: %v", err)
	}
	for _, item := range cases {
		payload, err := json.MarshalIndent(item, "", "  ")
		if err != nil {
			t.Fatalf("序列化 legacy sidecar 失败: %v", err)
		}
		path := filepath.Join(runtimeDir, sanitizeRepoKey(item.TargetRepo)+".json")
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			t.Fatalf("写入 legacy sidecar 失败: %v", err)
		}
	}

	loaded, err := store.GetActiveRepoSlot()
	if err == nil || !strings.Contains(err.Error(), "多个旧版 repo sidecar") {
		t.Fatalf("预期显式识别多 legacy sidecar，loaded=%#v err=%v", loaded, err)
	}
}

func TestStoreSupportsRTKComparisonAndJournalQueries(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	day := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	tasks := []*core.Task{
		{
			TaskID:         "10#body",
			IdempotencyKey: "10#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    10,
			IssueTitle:     "rtk on",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentResearch,
			RiskLevel:      core.RiskLow,
			State:          core.StateDone,
		},
		{
			TaskID:         "11#body",
			IdempotencyKey: "11#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/other",
			IssueNumber:    11,
			IssueTitle:     "rtk off",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentResearch,
			RiskLevel:      core.RiskLow,
			State:          core.StateDead,
		},
	}
	for _, task := range tasks {
		if err := store.UpsertTask(task); err != nil {
			t.Fatalf("写入任务失败: %v", err)
		}
	}
	if err := store.AppendEventAt("10#body", core.EventStarted, "开始执行", day); err != nil {
		t.Fatalf("写入 started 事件失败: %v", err)
	}
	if err := store.AppendEventAt("10#body", core.EventDone, "执行完成", day); err != nil {
		t.Fatalf("写入 done 事件失败: %v", err)
	}
	if err := store.AppendEventAt("11#body", core.EventDead, "tmux 会话超时", day); err != nil {
		t.Fatalf("写入 dead 事件失败: %v", err)
	}
	if err := store.RecordTokenUsage(TokenUsageRecord{
		TaskID:     "10#body",
		SessionID:  "sess-10",
		PromptFile: "/tmp/prompts/10.md",
		Usage: core.TokenUsage{
			InputTokens:  60,
			OutputTokens: 20,
		},
		CostUSD:    0.10,
		DurationMS: 1000,
		RTKEnabled: true,
		RecordedAt: day,
	}); err != nil {
		t.Fatalf("写入 rtk token 失败: %v", err)
	}
	if err := store.RecordTokenUsage(TokenUsageRecord{
		TaskID:     "11#body",
		SessionID:  "sess-11",
		PromptFile: "/tmp/prompts/11.md",
		Usage: core.TokenUsage{
			InputTokens:  80,
			OutputTokens: 30,
		},
		CostUSD:    0.40,
		DurationMS: 1500,
		RTKEnabled: false,
		RecordedAt: day,
	}); err != nil {
		t.Fatalf("写入 plain token 失败: %v", err)
	}

	comparison, err := store.RTKComparisonBetween(day.Add(-time.Hour), day.Add(time.Hour))
	if err != nil {
		t.Fatalf("读取 rtk 对比失败: %v", err)
	}
	if comparison.RTKRuns != 1 || comparison.PlainRuns != 1 {
		t.Fatalf("unexpected comparison counts: %#v", comparison)
	}
	if comparison.SavingsPercent <= 0 {
		t.Fatalf("预期 rtk 节省为正数，实际为 %#v", comparison)
	}

	summary, err := store.JournalDaySummary(day)
	if err != nil {
		t.Fatalf("读取 journal 汇总失败: %v", err)
	}
	if summary.TasksTouched != 2 || summary.Done != 1 || summary.Dead != 1 {
		t.Fatalf("unexpected journal summary: %#v", summary)
	}

	items, err := store.JournalTaskSummaries(day)
	if err != nil {
		t.Fatalf("读取 journal 任务汇总失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("unexpected journal task count: %d", len(items))
	}

	targetSummary, err := store.JournalDaySummaryByTarget(day, "41490/ccclaw")
	if err != nil {
		t.Fatalf("读取目标仓库 journal 汇总失败: %v", err)
	}
	if targetSummary.TasksTouched != 1 || targetSummary.Done != 1 || targetSummary.Dead != 0 {
		t.Fatalf("unexpected target journal summary: %#v", targetSummary)
	}

	targetItems, err := store.JournalTaskSummariesByTarget(day, "41490/ccclaw")
	if err != nil {
		t.Fatalf("读取目标仓库 journal 任务汇总失败: %v", err)
	}
	if len(targetItems) != 1 || targetItems[0].IssueNumber != 10 {
		t.Fatalf("unexpected target journal tasks: %#v", targetItems)
	}

	targetComparison, err := store.RTKComparisonBetweenByTarget(day.Add(-time.Hour), day.Add(time.Hour), "41490/ccclaw")
	if err != nil {
		t.Fatalf("读取目标仓库 rtk 对比失败: %v", err)
	}
	if targetComparison.RTKRuns != 1 || targetComparison.PlainRuns != 0 {
		t.Fatalf("unexpected target comparison: %#v", targetComparison)
	}

	events, err := store.ListTaskEventsBetween(day.Add(-24*time.Hour), day.Add(24*time.Hour), 10)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("unexpected event count: %d", len(events))
	}

	targetEvents, err := store.ListTaskEventsBetweenByTarget(day.Add(-24*time.Hour), day.Add(24*time.Hour), 10, "41490/ccclaw")
	if err != nil {
		t.Fatalf("读取目标仓库事件失败: %v", err)
	}
	if len(targetEvents) != 2 {
		t.Fatalf("unexpected target event count: %d", len(targetEvents))
	}
}

func TestListRunnableExcludesBlockedTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	tasks := []*core.Task{
		{
			TaskID:         "30#new",
			IdempotencyKey: "30#new",
			ControlRepo:    "41490/ccclaw",
			IssueRepo:      "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    30,
			IssueTitle:     "new",
			State:          core.StateNew,
		},
		{
			TaskID:         "31#failed",
			IdempotencyKey: "31#failed",
			ControlRepo:    "41490/ccclaw",
			IssueRepo:      "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    31,
			IssueTitle:     "failed",
			State:          core.StateFailed,
		},
		{
			TaskID:         "32#blocked",
			IdempotencyKey: "32#blocked",
			ControlRepo:    "41490/ccclaw",
			IssueRepo:      "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    32,
			IssueTitle:     "blocked",
			State:          core.StateBlocked,
		},
	}
	for _, task := range tasks {
		if err := store.UpsertTask(task); err != nil {
			t.Fatalf("写入任务失败: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	runnable, err := store.ListRunnable(10)
	if err != nil {
		t.Fatalf("读取待执行任务失败: %v", err)
	}
	if len(runnable) != 2 {
		t.Fatalf("预期只有 NEW/FAILED 可执行，实际=%d", len(runnable))
	}
	for _, task := range runnable {
		if task.State == core.StateBlocked {
			t.Fatalf("BLOCKED 任务不应进入待执行集合: %#v", task)
		}
	}
}

func TestStoreSupportsStatsDateRangeAndDailyAggregation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	for _, task := range []*core.Task{
		{
			TaskID:         "20#body",
			IdempotencyKey: "20#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    20,
			IssueTitle:     "range one",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentResearch,
			RiskLevel:      core.RiskLow,
			State:          core.StateDone,
		},
		{
			TaskID:         "21#body",
			IdempotencyKey: "21#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    21,
			IssueTitle:     "range two",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentResearch,
			RiskLevel:      core.RiskLow,
			State:          core.StateDone,
		},
	} {
		if err := store.UpsertTask(task); err != nil {
			t.Fatalf("写入任务失败: %v", err)
		}
	}

	records := []TokenUsageRecord{
		{
			TaskID:     "20#body",
			SessionID:  "sess-20-a",
			PromptFile: "/tmp/20-a.md",
			Usage:      core.TokenUsage{InputTokens: 10, OutputTokens: 5},
			CostUSD:    0.05,
			RecordedAt: time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC),
		},
		{
			TaskID:     "20#body",
			SessionID:  "sess-20-b",
			PromptFile: "/tmp/20-b.md",
			Usage:      core.TokenUsage{InputTokens: 12, OutputTokens: 6},
			CostUSD:    0.06,
			RecordedAt: time.Date(2026, 3, 9, 11, 0, 0, 0, time.UTC),
		},
		{
			TaskID:     "21#body",
			SessionID:  "sess-21-a",
			PromptFile: "/tmp/21-a.md",
			Usage:      core.TokenUsage{InputTokens: 20, OutputTokens: 8},
			CostUSD:    0.08,
			RecordedAt: time.Date(2026, 3, 9, 13, 0, 0, 0, time.UTC),
		},
		{
			TaskID:     "21#body",
			SessionID:  "sess-21-b",
			PromptFile: "/tmp/21-b.md",
			Usage:      core.TokenUsage{InputTokens: 30, OutputTokens: 10},
			CostUSD:    0.10,
			RecordedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
		},
	}
	for _, record := range records {
		if err := store.RecordTokenUsage(record); err != nil {
			t.Fatalf("写入 token 记录失败: %v", err)
		}
	}

	start := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	summary, err := store.TokenStatsBetween(start, end)
	if err != nil {
		t.Fatalf("读取范围统计失败: %v", err)
	}
	if summary.Runs != 2 || summary.Sessions != 2 {
		t.Fatalf("unexpected range summary: %#v", summary)
	}
	if summary.InputTokens != 32 || summary.OutputTokens != 14 {
		t.Fatalf("unexpected range tokens: %#v", summary)
	}

	taskStats, err := store.TaskTokenStatsBetween(start, end, 10)
	if err != nil {
		t.Fatalf("读取范围任务统计失败: %v", err)
	}
	if len(taskStats) != 2 {
		t.Fatalf("unexpected task stats count: %d", len(taskStats))
	}

	daily, err := store.DailyTokenStatsBetween(time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("读取按天统计失败: %v", err)
	}
	if len(daily) != 3 {
		t.Fatalf("unexpected daily count: %d", len(daily))
	}
	if daily[1].Day.Format("2006-01-02") != "2026-03-09" || daily[1].Runs != 2 || daily[1].Sessions != 2 {
		t.Fatalf("unexpected daily item: %#v", daily[1])
	}

	dailyRTK, err := store.DailyRTKComparisonBetween(time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("读取按天 rtk 对比失败: %v", err)
	}
	if len(dailyRTK) != 3 {
		t.Fatalf("unexpected daily rtk count: %d", len(dailyRTK))
	}
	if dailyRTK[1].Day.Format("2006-01-02") != "2026-03-09" || dailyRTK[1].RTKRuns != 0 || dailyRTK[1].PlainRuns != 2 {
		t.Fatalf("unexpected daily rtk item: %#v", dailyRTK[1])
	}
}

func TestTaskClassFlowStatsBetween(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	tasks := []*core.Task{
		{
			TaskID:         "50#body",
			IdempotencyKey: "50#body",
			IssueRepo:      "41490/ccclaw",
			IssueNumber:    50,
			IssueTitle:     "sevolver daily",
			TaskClass:      core.TaskClassSevolver,
			State:          core.StateDone,
			CreatedAt:      base.Add(-2 * time.Hour),
		},
		{
			TaskID:         "51#body",
			IdempotencyKey: "51#body",
			IssueRepo:      "41490/ccclaw",
			IssueNumber:    51,
			IssueTitle:     "deep done",
			TaskClass:      core.TaskClassSevolverDeepAnalysis,
			State:          core.StateDone,
			CreatedAt:      base.Add(-3 * time.Hour),
		},
		{
			TaskID:         "52#body",
			IdempotencyKey: "52#body",
			IssueRepo:      "41490/ccclaw",
			IssueNumber:    52,
			IssueTitle:     "deep dead",
			TaskClass:      core.TaskClassSevolverDeepAnalysis,
			State:          core.StateDead,
			CreatedAt:      base.Add(-4 * time.Hour),
		},
		{
			TaskID:         "53#body",
			IdempotencyKey: "53#body",
			IssueRepo:      "41490/ccclaw",
			IssueNumber:    53,
			IssueTitle:     "deep backlog",
			TaskClass:      core.TaskClassSevolverDeepAnalysis,
			State:          core.StateRunning,
			CreatedAt:      base.Add(-30 * time.Minute),
		},
	}
	for _, task := range tasks {
		if err := store.UpsertTask(task); err != nil {
			t.Fatalf("写入任务失败: %v", err)
		}
	}

	for _, event := range []struct {
		taskID    string
		eventType core.EventType
		at        time.Time
	}{
		{taskID: "50#body", eventType: core.EventCreated, at: base.Add(-2 * time.Hour)},
		{taskID: "50#body", eventType: core.EventDone, at: base.Add(-90 * time.Minute)},
		{taskID: "51#body", eventType: core.EventCreated, at: base.Add(-3 * time.Hour)},
		{taskID: "51#body", eventType: core.EventDone, at: base.Add(-2 * time.Hour)},
		{taskID: "52#body", eventType: core.EventCreated, at: base.Add(-4 * time.Hour)},
		{taskID: "52#body", eventType: core.EventDead, at: base.Add(-3 * time.Hour)},
		{taskID: "53#body", eventType: core.EventCreated, at: base.Add(-30 * time.Minute)},
	} {
		if err := store.AppendEventAt(event.taskID, event.eventType, "test", event.at); err != nil {
			t.Fatalf("写入事件失败: %v", err)
		}
	}

	for _, token := range []TokenUsageRecord{
		{
			TaskID:     "50#body",
			SessionID:  "sess-50",
			PromptFile: "/tmp/50.md",
			Usage:      core.TokenUsage{InputTokens: 20, OutputTokens: 5},
			CostUSD:    0.10,
			RecordedAt: base.Add(-89 * time.Minute),
		},
		{
			TaskID:     "51#body",
			SessionID:  "sess-51",
			PromptFile: "/tmp/51.md",
			Usage:      core.TokenUsage{InputTokens: 40, OutputTokens: 10},
			CostUSD:    0.20,
			RecordedAt: base.Add(-119 * time.Minute),
		},
		{
			TaskID:     "52#body",
			SessionID:  "sess-52",
			PromptFile: "/tmp/52.md",
			Usage:      core.TokenUsage{InputTokens: 60, OutputTokens: 15},
			CostUSD:    0.30,
			RecordedAt: base.Add(-179 * time.Minute),
		},
	} {
		if err := store.RecordTokenUsage(token); err != nil {
			t.Fatalf("写入 token 失败: %v", err)
		}
	}

	snapshot, err := store.TaskClassFlowStatsBetween(
		base.Add(-24*time.Hour),
		base.Add(time.Hour),
		[]core.TaskClass{core.TaskClassSevolver, core.TaskClassSevolverDeepAnalysis},
	)
	if err != nil {
		t.Fatalf("读取 task_class 聚合失败: %v", err)
	}
	if snapshot == nil || len(snapshot.Classes) != 2 {
		t.Fatalf("unexpected task_class snapshot: %#v", snapshot)
	}

	sevolver := snapshot.Classes[0]
	deep := snapshot.Classes[1]
	if sevolver.TaskClass != string(core.TaskClassSevolver) || sevolver.UpgradeCount != 1 || sevolver.ConvergedCount != 1 || sevolver.BacklogCount != 0 {
		t.Fatalf("unexpected sevolver stat: %#v", sevolver)
	}
	if sevolver.SuccessRate != 100 || sevolver.FailureRate != 0 || sevolver.Runs != 1 || sevolver.CostUSD != 0.10 {
		t.Fatalf("unexpected sevolver rates or costs: %#v", sevolver)
	}
	if deep.TaskClass != string(core.TaskClassSevolverDeepAnalysis) || deep.UpgradeCount != 3 || deep.ConvergedCount != 1 || deep.BacklogCount != 1 {
		t.Fatalf("unexpected deep stat: %#v", deep)
	}
	if deep.SuccessCount != 1 || deep.FailureCount != 1 || deep.SuccessRate != 50 || deep.FailureRate != 50 {
		t.Fatalf("unexpected deep success/failure: %#v", deep)
	}
	if deep.Runs != 2 || deep.InputTokens != 100 || deep.CostUSD != 0.50 {
		t.Fatalf("unexpected deep token costs: %#v", deep)
	}
	if deep.AvgCloseSeconds <= 0 {
		t.Fatalf("unexpected deep avg close: %#v", deep)
	}
}

func TestOpenRejectsLegacyDBPath(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "state.db")); err == nil {
		t.Fatal("expected .db path to be rejected")
	}
}

func TestOpenArchivesLegacyDBFiles(t *testing.T) {
	varDir := filepath.Join(t.TempDir(), "var")
	if err := os.MkdirAll(varDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "ccclaw.db"} {
		if err := os.WriteFile(filepath.Join(varDir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	store, err := Open(varDir)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer store.Close()

	for _, name := range []string{"state.db", "ccclaw.db"} {
		if _, err := os.Stat(filepath.Join(varDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected legacy db to be removed from var dir: %s", name)
		}
	}

	entries, err := os.ReadDir(filepath.Join(varDir, "archive", "legacy-db"))
	if err != nil {
		t.Fatalf("read legacy archive dir failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected legacy archive entries: %d", len(entries))
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".bak") {
			t.Fatalf("unexpected archive name: %s", entry.Name())
		}
	}
}
