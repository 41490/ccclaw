package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/adapters/github"
	"github.com/41490/ccclaw/internal/adapters/reporter"
	"github.com/41490/ccclaw/internal/adapters/storage"
	"github.com/41490/ccclaw/internal/config"
	"github.com/41490/ccclaw/internal/core"
	"github.com/41490/ccclaw/internal/executor"
	"github.com/41490/ccclaw/internal/memory"
	"github.com/41490/ccclaw/internal/scheduler"
)

func TestParseTargetRepo(t *testing.T) {
	body := "请处理这个问题\n\ntarget_repo: 41490/ccclaw\n"
	if got := parseTargetRepo(body); got != "41490/ccclaw" {
		t.Fatalf("unexpected target repo: %q", got)
	}
}

func TestResolveTargetRepoFallsBackToDefaultTarget(t *testing.T) {
	rt := &Runtime{
		cfg: &config.Config{
			DefaultTarget: "41490/ccclaw",
			Paths:         config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: "/opt/src/ccclaw",
			}},
		},
	}
	repo, reasons := rt.resolveTargetRepo("41490/other", "无显式 target")
	if repo != "41490/ccclaw" {
		t.Fatalf("unexpected repo: %q", repo)
	}
	if len(reasons) != 0 {
		t.Fatalf("expected no reasons, got %#v", reasons)
	}
}

func TestResolveTargetRepoUsesIssueRepoWhenItIsEnabledTarget(t *testing.T) {
	rt := &Runtime{
		cfg: &config.Config{
			DefaultTarget: "41490/other",
			Paths:         config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Targets: []config.TargetConfig{
				{
					Repo:      "41490/ccclaw",
					LocalPath: "/opt/src/ccclaw",
				},
				{
					Repo:      "41490/other",
					LocalPath: "/opt/src/other",
				},
			},
		},
	}
	repo, reasons := rt.resolveTargetRepo("41490/ccclaw", "无显式 target")
	if repo != "41490/ccclaw" {
		t.Fatalf("unexpected repo: %q", repo)
	}
	if len(reasons) != 0 {
		t.Fatalf("expected no reasons, got %#v", reasons)
	}
}

func TestResolveTargetRepoBlocksWhenNoTargetConfigured(t *testing.T) {
	rt := &Runtime{
		cfg: &config.Config{
			Paths:   config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Targets: nil,
		},
	}
	repo, reasons := rt.resolveTargetRepo("41490/other", "无显式 target")
	if repo != "" {
		t.Fatalf("expected empty repo, got %q", repo)
	}
	if len(reasons) != 1 {
		t.Fatalf("expected 1 reason, got %#v", reasons)
	}
}

func TestSyncIssueBlocksWhenLabelMissing(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}

	script := filepath.Join(fakeBin, "gh")
	body := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" != "api" ]]; then
  exit 1
fi
endpoint="${2:-}"
case "$endpoint" in
  "repos/41490/work/collaborators/outsider/permission")
    printf '{"permission":"read"}\n'
    ;;
  "repos/41490/work/issues/27/comments?per_page=100")
    printf '[]\n'
    ;;
  *)
    printf 'unexpected endpoint: %s\n' "$endpoint" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)

	store, err := storage.Open(filepath.Join(tmpDir, "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{
				ControlRepo: "41490/ccclaw",
				IssueLabel:  "ccclaw",
			},
			Paths: config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Approval: config.ApprovalConfig{
				Words:             []string{"approve"},
				RejectWords:       []string{"reject"},
				MinimumPermission: "maintain",
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/work",
				LocalPath: "/opt/src/work",
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		store:   store,
		ghCache: map[string]*github.Client{},
	}

	issue := github.Issue{
		Repo:      "41490/work",
		Number:    27,
		Title:     "缺少标签",
		Body:      "请处理",
		State:     "open",
		User:      github.User{Login: "outsider"},
		CreatedAt: time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC),
	}
	if err := rt.syncIssue(context.Background(), issue, true); err != nil {
		t.Fatalf("syncIssue failed: %v", err)
	}

	task, err := store.GetByIdempotency(core.IdempotencyKey(issue.Repo, issue.Number))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task == nil {
		t.Fatal("expected task to be created")
	}
	if task.State != core.StateBlocked {
		t.Fatalf("unexpected task state: %#v", task)
	}
	if task.TargetRepo != "41490/work" {
		t.Fatalf("expected issue repo to become target repo: %#v", task)
	}
	if task.TaskClass != core.TaskClassGeneral {
		t.Fatalf("expected general task class, got %#v", task)
	}
}

func TestSyncIssueClassifiesSevolverDeepAnalysis(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}

	script := filepath.Join(fakeBin, "gh")
	body := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" != "api" ]]; then
  exit 1
fi
endpoint="${2:-}"
case "$endpoint" in
  "repos/41490/ccclaw/collaborators/ZoomQuiet/permission")
    printf '{"permission":"admin"}\n'
    ;;
  "repos/41490/ccclaw/issues/36/comments?per_page=100")
    printf '[]\n'
    ;;
  *)
    printf 'unexpected endpoint: %s\n' "$endpoint" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)

	store, err := storage.Open(filepath.Join(tmpDir, "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{
				ControlRepo: "41490/ccclaw",
				IssueLabel:  "ccclaw",
			},
			Paths: config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Approval: config.ApprovalConfig{
				Words:             []string{"approve"},
				RejectWords:       []string{"reject"},
				MinimumPermission: "maintain",
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: "/opt/src/ccclaw",
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		store:   store,
		ghCache: map[string]*github.Client{},
	}

	issue := github.Issue{
		Repo:   "41490/ccclaw",
		Number: 36,
		Title:  "[sevolver] 能力缺口深度分析 2026-03-12",
		Body: `<!-- ccclaw:sevolver:deep-analysis:fingerprint=sg-demo -->
target_repo: 41490/ccclaw
task_class: sevolver_deep_analysis
`,
		State:     "open",
		Labels:    []github.Label{{Name: "ccclaw"}, {Name: "sevolver"}},
		User:      github.User{Login: "ZoomQuiet"},
		CreatedAt: time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC),
	}
	if err := rt.syncIssue(context.Background(), issue, true); err != nil {
		t.Fatalf("syncIssue failed: %v", err)
	}

	task, err := store.GetByIdempotency(core.IdempotencyKey(issue.Repo, issue.Number))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task == nil {
		t.Fatal("expected task to be created")
	}
	if task.TaskClass != core.TaskClassSevolverDeepAnalysis {
		t.Fatalf("unexpected task class: %#v", task)
	}
}

func TestSyncIssueRejectCommandOverridesTrustedAuthor(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}

	script := filepath.Join(fakeBin, "gh")
	body := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" != "api" ]]; then
  exit 1
fi
endpoint="${2:-}"
case "$endpoint" in
  "repos/41490/ccclaw/collaborators/author/permission")
    printf '{"permission":"maintain"}\n'
    ;;
  "repos/41490/ccclaw/collaborators/reviewer/permission")
    printf '{"permission":"maintain"}\n'
    ;;
  "repos/41490/ccclaw/issues/15/comments?per_page=100")
    cat <<'JSON'
[
  {"id":301,"body":"/ccclaw reject","user":{"login":"reviewer"}}
]
JSON
    ;;
  *)
    printf 'unexpected endpoint: %s\n' "$endpoint" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)

	store, err := storage.Open(filepath.Join(tmpDir, "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{ControlRepo: "41490/ccclaw"},
			Paths:  config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Approval: config.ApprovalConfig{
				Words:             []string{"approve", "go"},
				RejectWords:       []string{"reject", "拒绝"},
				MinimumPermission: "maintain",
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: "/opt/src/ccclaw",
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		store:   store,
		ghCache: map[string]*github.Client{},
	}

	issue := github.Issue{
		Repo:      "41490/ccclaw",
		Number:    15,
		Title:     "审批否决覆盖可信发起者",
		Body:      "请先暂停\n\ntarget_repo: 41490/ccclaw\n",
		User:      github.User{Login: "author"},
		CreatedAt: time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC),
	}
	if err := rt.syncIssue(context.Background(), issue, true); err != nil {
		t.Fatalf("syncIssue failed: %v", err)
	}

	task, err := store.GetByIdempotency(core.IdempotencyKey(issue.Repo, issue.Number))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task == nil {
		t.Fatal("expected task to be created")
	}
	if task.Approved {
		t.Fatalf("expected task to remain blocked: %#v", task)
	}
	if task.State != core.StateBlocked {
		t.Fatalf("unexpected task state: %#v", task)
	}
	if task.ApprovalActor != "reviewer" || task.ApprovalCommentID != 301 {
		t.Fatalf("unexpected approval metadata: %#v", task)
	}
	if task.ApprovalCommand != "/ccclaw reject" {
		t.Fatalf("unexpected approval command: %#v", task)
	}
	if task.IssueRepo != "41490/ccclaw" {
		t.Fatalf("unexpected issue repo: %#v", task)
	}
}

func TestSyncIssueKeepsDoneUntilDoneCommentRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	commentsFile := filepath.Join(tmpDir, "comments.json")
	if err := os.WriteFile(commentsFile, []byte(`[
  {"id":501,"body":"任务已完成\n\n/ccclaw [DONE]","user":{"login":"ccclaw-bot"}},
  {"id":502,"body":"还有个想法，但先别重跑","user":{"login":"reviewer"}}
]`), 0o644); err != nil {
		t.Fatalf("写入 comments fixture 失败: %v", err)
	}

	script := filepath.Join(fakeBin, "gh")
	body := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" != "api" ]]; then
  exit 1
fi
endpoint="${2:-}"
case "$endpoint" in
  "repos/41490/ccclaw/collaborators/author/permission")
    printf '{"permission":"maintain"}\n'
    ;;
  "repos/41490/ccclaw/collaborators/reviewer/permission")
    printf '{"permission":"maintain"}\n'
    ;;
  "repos/41490/ccclaw/collaborators/ccclaw-bot/permission")
    printf '{"permission":"maintain"}\n'
    ;;
  "repos/41490/ccclaw/issues/41/comments?per_page=100")
    cat "$TEST_COMMENTS_FILE"
    ;;
  *)
    printf 'unexpected endpoint: %s\n' "$endpoint" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TEST_COMMENTS_FILE", commentsFile)

	store, err := storage.Open(filepath.Join(tmpDir, "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         core.TaskID("41490/ccclaw", 41),
		IdempotencyKey: core.IdempotencyKey("41490/ccclaw", 41),
		ControlRepo:    "41490/ccclaw",
		IssueRepo:      "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    41,
		IssueTitle:     "done marker",
		State:          core.StateDone,
		DoneCommentID:  501,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{ControlRepo: "41490/ccclaw", IssueLabel: "ccclaw"},
			Paths:  config.PathsConfig{KBDir: "/opt/ccclaw/kb"},
			Approval: config.ApprovalConfig{
				Words:             []string{"approve"},
				RejectWords:       []string{"reject"},
				MinimumPermission: "maintain",
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: "/opt/src/ccclaw",
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		store:   store,
		ghCache: map[string]*github.Client{},
	}

	issue := github.Issue{
		Repo:      "41490/ccclaw",
		Number:    41,
		Title:     "done marker",
		Body:      "请处理",
		State:     "open",
		Labels:    []github.Label{{Name: "ccclaw"}},
		User:      github.User{Login: "author"},
		CreatedAt: time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC),
	}
	if err := rt.syncIssue(context.Background(), issue, true); err != nil {
		t.Fatalf("第一次 syncIssue 失败: %v", err)
	}
	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateDone || loaded.DoneCommentID != 501 {
		t.Fatalf("预期保留 DONE，实际为 %#v", loaded)
	}

	if err := os.WriteFile(commentsFile, []byte(`[
  {"id":502,"body":"已删完成标记，请重新执行","user":{"login":"reviewer"}}
]`), 0o644); err != nil {
		t.Fatalf("重写 comments fixture 失败: %v", err)
	}
	if err := rt.syncIssue(context.Background(), issue, true); err != nil {
		t.Fatalf("第二次 syncIssue 失败: %v", err)
	}
	loaded, err = store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateNew || loaded.DoneCommentID != 0 {
		t.Fatalf("预期删除 done 标记后回到 NEW，实际为 %#v", loaded)
	}
}

func TestSummarizeSchedulerNoneModePasses(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:  "none",
		CronReason: "未检测到受控 crontab 规则",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if detail == "" {
		t.Fatal("expected scheduler detail")
	}
}

func TestSummarizeSchedulerAutoDegradeFails(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:     "auto",
		SystemdReason: "Failed to connect to bus: No medium found",
		CronReason:    "未检测到受控 crontab 规则",
	})
	if err == nil {
		t.Fatal("expected degrade error")
	}
	if detail == "" {
		t.Fatal("expected scheduler detail")
	}
	if want := "reason=当前会话无法连接 user systemd 总线"; !strings.Contains(detail, want) {
		t.Fatalf("expected %q in %q", want, detail)
	}
	if want := "repair=请在用户登录会话中执行 systemctl --user daemon-reload"; !strings.Contains(detail, want) {
		t.Fatalf("expected repair hint %q in %q", want, detail)
	}
}

func TestSummarizeSchedulerSystemdNeedsRepair(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:        "systemd",
		SystemdUserDir:   "/tmp/systemd-user",
		SystemdInstalled: true,
		SystemdReason:    "systemctl --user is-enabled ccclaw-ingest.timer 失败: disabled",
	})
	if err == nil {
		t.Fatal("expected systemd mismatch error")
	}
	if want := "repair=请执行 systemctl --user daemon-reload"; !strings.Contains(detail, want) {
		t.Fatalf("expected repair hint %q in %q", want, detail)
	}
	if want := "reason=已写入 user systemd 单元目录 /tmp/systemd-user，但 timer 尚未启用"; !strings.Contains(detail, want) {
		t.Fatalf("expected systemd reason %q in %q", want, detail)
	}
	if want := "systemd=systemctl --user is-enabled"; !strings.Contains(detail, want) {
		t.Fatalf("expected systemd context %q in %q", want, detail)
	}
}

func TestSummarizeSchedulerCronPasses(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:  "cron",
		CronActive: true,
		CronReason: "已检测到受控 crontab ingest/patrol/journal 规则",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if want := "effective=cron"; !strings.Contains(detail, want) {
		t.Fatalf("expected %q in %q", want, detail)
	}
}

func TestSummarizeSchedulerDoubleSchedulingFails(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:     "auto",
		SystemdActive: true,
		SystemdReason: "systemd active",
		CronActive:    true,
		CronReason:    "cron active",
	})
	if err == nil {
		t.Fatal("expected duplicate scheduling error")
	}
	if want := "systemd=systemd active"; !strings.Contains(detail, want) {
		t.Fatalf("expected systemd context %q in %q", want, detail)
	}
	if want := "cron=cron active"; !strings.Contains(detail, want) {
		t.Fatalf("expected cron context %q in %q", want, detail)
	}
}

func TestSummarizeSchedulerCronMissingCommand(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:  "cron",
		CronReason: "未找到 crontab: exec: \"crontab\": executable file not found in $PATH",
	})
	if err == nil {
		t.Fatal("expected cron mismatch error")
	}
	if want := "reason=当前环境缺少 crontab，无法使用 cron 调度"; !strings.Contains(detail, want) {
		t.Fatalf("expected cron reason %q in %q", want, detail)
	}
	if want := "repair=当前环境缺少 crontab；请安装 cron/cronie，或执行 `ccclaw scheduler disable-cron` 后改用 systemd/none"; !strings.Contains(detail, want) {
		t.Fatalf("expected cron repair %q in %q", want, detail)
	}
}

func TestSummarizeSchedulerCronMissingManagedEntries(t *testing.T) {
	detail, err := summarizeSchedulerProbe(t, scheduler.Probe{
		Requested:  "cron",
		CronReason: "未检测到受控 crontab 规则",
	})
	if err == nil {
		t.Fatal("expected cron mismatch error")
	}
	if want := "repair=请执行 `ccclaw scheduler enable-cron` 补齐 ingest/patrol/journal 三条受控规则"; !strings.Contains(detail, want) {
		t.Fatalf("expected cron repair %q in %q", want, detail)
	}
}

func summarizeSchedulerProbe(t *testing.T, probe scheduler.Probe) (string, error) {
	t.Helper()

	snapshot, err := scheduler.CollectStatusFromProbe(probe)
	return snapshot.Detail(), err
}

func TestStatsRendersSummaryAndTaskTable(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "var"))
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
		IssueTitle:     "phase1 统计命令",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentResearch,
		RiskLevel:      core.RiskLow,
		State:          core.StateDone,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:    task.TaskID,
		SessionID: "sess-10",
		Usage: core.TokenUsage{
			InputTokens:  30,
			OutputTokens: 12,
		},
		CostUSD:    0.09,
		DurationMS: 5000,
		RecordedAt: time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 token 使用失败: %v", err)
	}

	rt := &Runtime{store: store}
	var out bytes.Buffer
	if err := rt.StatsWithOptions(&out, StatsOptions{}); err != nil {
		t.Fatalf("执行 stats 失败: %v", err)
	}
	text := out.String()
	for _, want := range []string{"执行次数: 1", "会话数: 1", "#10", "sess-10", "累计成本(USD): 0.0900"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestBuildExecutionOptionsUsesResumeSession(t *testing.T) {
	rt := &Runtime{}
	task := &core.Task{
		TaskID:        "10#body",
		IssueNumber:   10,
		IssueTitle:    "resume task",
		IssueBody:     "继续修复",
		IssueAuthor:   "zoomq",
		Labels:        []string{"ccclaw"},
		Intent:        core.IntentFix,
		RiskLevel:     core.RiskLow,
		State:         core.StateFailed,
		RetryCount:    1,
		LastSessionID: "sess-10",
		ErrorMsg:      "tmux 会话超时",
	}
	target := &config.TargetConfig{
		Repo:      "41490/ccclaw",
		LocalPath: "/opt/src/ccclaw",
	}
	opts := rt.buildExecutionOptions(task, target)
	if opts.ResumeSessionID != "sess-10" {
		t.Fatalf("unexpected resume session: %#v", opts)
	}
	if !strings.Contains(opts.Prompt, "上次任务执行中断") || !strings.Contains(opts.Prompt, "sess-10") {
		t.Fatalf("unexpected resume prompt: %q", opts.Prompt)
	}
}

func TestStatsWithRTKComparisonRendersSection(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	for _, task := range []*core.Task{
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
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    11,
			IssueTitle:     "rtk off",
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
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     "10#body",
		SessionID:  "sess-10",
		PromptFile: "/tmp/10.md",
		Usage:      core.TokenUsage{InputTokens: 30, OutputTokens: 10},
		CostUSD:    0.10,
		RTKEnabled: true,
		RecordedAt: time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 rtk 记录失败: %v", err)
	}
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     "11#body",
		SessionID:  "sess-11",
		PromptFile: "/tmp/11.md",
		Usage:      core.TokenUsage{InputTokens: 60, OutputTokens: 20},
		CostUSD:    0.30,
		RTKEnabled: false,
		RecordedAt: time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 plain 记录失败: %v", err)
	}

	rt := &Runtime{store: store}
	var out bytes.Buffer
	if err := rt.StatsWithOptions(&out, StatsOptions{ShowRTKComparison: true}); err != nil {
		t.Fatalf("执行 stats --rtk-comparison 失败: %v", err)
	}
	text := out.String()
	for _, want := range []string{"RTK 对比:", "RTK runs: 1", "Plain runs: 1", "Estimated savings:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestStatsWithDateRangeAndDailyRendersSections(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "var"))
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
	for _, record := range []storage.TokenUsageRecord{
		{
			TaskID:     "20#body",
			SessionID:  "sess-20",
			PromptFile: "/tmp/20.md",
			Usage:      core.TokenUsage{InputTokens: 10, OutputTokens: 5},
			CostUSD:    0.05,
			RecordedAt: time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC),
		},
		{
			TaskID:     "20#body",
			SessionID:  "sess-20b",
			PromptFile: "/tmp/20b.md",
			Usage:      core.TokenUsage{InputTokens: 12, OutputTokens: 6},
			CostUSD:    0.06,
			RTKEnabled: true,
			RecordedAt: time.Date(2026, 3, 9, 11, 0, 0, 0, time.UTC),
		},
		{
			TaskID:     "21#body",
			SessionID:  "sess-21",
			PromptFile: "/tmp/21.md",
			Usage:      core.TokenUsage{InputTokens: 20, OutputTokens: 8},
			CostUSD:    0.08,
			RTKEnabled: false,
			RecordedAt: time.Date(2026, 3, 9, 13, 0, 0, 0, time.UTC),
		},
		{
			TaskID:     "21#body",
			SessionID:  "sess-21b",
			PromptFile: "/tmp/21b.md",
			Usage:      core.TokenUsage{InputTokens: 30, OutputTokens: 10},
			CostUSD:    0.10,
			RecordedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
		},
	} {
		if err := store.RecordTokenUsage(record); err != nil {
			t.Fatalf("写入 token 记录失败: %v", err)
		}
	}

	rt := &Runtime{store: store}
	var out bytes.Buffer
	if err := rt.StatsWithOptions(&out, StatsOptions{
		Start:             time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		End:               time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC),
		Daily:             true,
		ShowRTKComparison: true,
		Limit:             1,
	}); err != nil {
		t.Fatalf("执行 stats 范围/按天失败: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"统计范围:",
		"起始日期: 2026-03-09",
		"截止日期: 2026-03-10 (含当日)",
		"执行次数: 3",
		"按天聚合(最近 1 天):",
		"2026-03-10",
		"任务明细(最近 1 项):",
		"RTK 对比:",
		"按天 RTK 对比(最近 1 天):",
		"Sevolver 自进化链路聚合:",
		"[day] 日窗口(最近 24 小时)",
		"[week] 周窗口(最近 7 天)",
		"[range] 指定范围",
		"sevolver_deep_analysis",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
	for _, unexpected := range []string{"2026-03-08", "#20"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("unexpected limited output %q in %q", unexpected, text)
		}
	}
}

func TestStatusRendersRuntimeSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	fixtureDir := filepath.Join(tmpDir, "fixture")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}

	tmuxScript := filepath.Join(fakeBin, "tmux")
	tmuxBody := `#!/bin/bash
set -euo pipefail
fixture_dir="${TMUX_FIXTURE_DIR:?}"
if [[ "${1:-}" == "list-panes" ]]; then
  cat "$fixture_dir/list-panes.txt"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(tmuxScript, []byte(tmuxBody), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}
	systemctlScript := filepath.Join(fakeBin, "systemctl")
	systemctlBody := "#!/bin/bash\nprintf 'Failed to connect to bus: No medium found\\n' >&2\nexit 1\n"
	if err := os.WriteFile(systemctlScript, []byte(systemctlBody), 0o755); err != nil {
		t.Fatalf("写入 fake systemctl 失败: %v", err)
	}
	crontabScript := filepath.Join(fakeBin, "crontab")
	crontabBody := "#!/bin/bash\nprintf 'no crontab for tester\\n' >&2\nexit 1\n"
	if err := os.WriteFile(crontabScript, []byte(crontabBody), 0o755); err != nil {
		t.Fatalf("写入 fake crontab 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMUX_FIXTURE_DIR", fixtureDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	tasks := []*core.Task{
		{
			TaskID:         "10#body",
			IdempotencyKey: "10#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    10,
			IssueTitle:     "status running",
			IssueAuthor:    "zoomq",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentResearch,
			RiskLevel:      core.RiskLow,
			State:          core.StateRunning,
		},
		{
			TaskID:         "11#body",
			IdempotencyKey: "11#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    11,
			IssueTitle:     "status failed",
			IssueAuthor:    "zoomq",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentFix,
			RiskLevel:      core.RiskLow,
			State:          core.StateFailed,
			ErrorMsg:       "tmux 会话超时",
		},
		{
			TaskID:         "12#body",
			IdempotencyKey: "12#body",
			ControlRepo:    "41490/ccclaw",
			TargetRepo:     "41490/ccclaw",
			IssueNumber:    12,
			IssueTitle:     "status done",
			IssueAuthor:    "zoomq",
			Labels:         []string{"ccclaw"},
			Intent:         core.IntentResearch,
			RiskLevel:      core.RiskLow,
			State:          core.StateDone,
		},
	}
	for _, task := range tasks {
		if err := store.UpsertTask(task); err != nil {
			t.Fatalf("写入任务失败: %v", err)
		}
	}
	if err := store.AppendEvent("11#body", core.EventDead, "tmux 会话超时"); err != nil {
		t.Fatalf("写入异常事件失败: %v", err)
	}
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     "10#body",
		SessionID:  "sess-10",
		PromptFile: "/tmp/10.md",
		Usage:      core.TokenUsage{InputTokens: 30, OutputTokens: 10},
		CostUSD:    0.10,
		RTKEnabled: true,
		RecordedAt: time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 rtk 记录失败: %v", err)
	}
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     "12#body",
		SessionID:  "sess-12",
		PromptFile: "/tmp/12.md",
		Usage:      core.TokenUsage{InputTokens: 60, OutputTokens: 20},
		CostUSD:    0.30,
		RTKEnabled: false,
		RecordedAt: time.Date(2026, 3, 10, 16, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 plain 记录失败: %v", err)
	}

	sessionName := executor.SessionName("10#body")
	listPanes := sessionName + "\t" + strconv.FormatInt(time.Now().Add(-25*time.Minute).Unix(), 10) + "\t0\t0\t123\n" +
		"ccclaw-orphan\t" + strconv.FormatInt(time.Now().Add(-5*time.Minute).Unix(), 10) + "\t0\t0\t456\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "list-panes.txt"), []byte(listPanes), 0o644); err != nil {
		t.Fatalf("写入 list-panes fixture 失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir:  filepath.Join(tmpDir, "app"),
				LogDir:  filepath.Join(tmpDir, "log"),
				VarDir:  varDir,
				KBDir:   "/opt/ccclaw/kb",
				EnvFile: filepath.Join(tmpDir, ".env"),
			},
			Executor:  config.ExecutorConfig{Timeout: "30m", Mode: string(config.ExecutorModeTMux)},
			Scheduler: config.SchedulerConfig{Mode: "none"},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.Status(&out); err != nil {
		t.Fatalf("执行 status 失败: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"当前快照",
		"调度快照:",
		"执行器快照:",
		"入口配置: claude",
		"Claude 解析:",
		"RTK 解析:",
		"会话快照:",
		"会话健康: 正常=0 接近超时=1 超时=0 已退出=0 丢失=0 孤儿=1",
		"#10",
		"general",
		"warning",
		"任务总数: 3",
		"Token 快照:",
		"RTK 对比样本: rtk=1 plain=1",
		"RTK 估算节省:",
		"详情请执行: ccclaw stats --rtk-comparison",
		"Sevolver 自进化链路聚合:",
		"最近异常:",
		"tmux 会话超时",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestStatusJSONRendersRuntimeSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         "20#body",
		IdempotencyKey: "20#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueRepo:      "41490/ccclaw",
		IssueNumber:    20,
		IssueTitle:     "status json",
		IssueAuthor:    "zoomq",
		TaskClass:      core.TaskClassGeneral,
		State:          core.StateRunning,
		LastSessionID:  "sess-20",
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     task.TaskID,
		SessionID:  task.LastSessionID,
		PromptFile: "/tmp/20.md",
		Usage:      core.TokenUsage{InputTokens: 11, OutputTokens: 7},
		CostUSD:    0.12,
		RTKEnabled: false,
		RecordedAt: time.Date(2026, 3, 12, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("写入 token 记录失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir:  filepath.Join(tmpDir, "app"),
				LogDir:  filepath.Join(tmpDir, "log"),
				VarDir:  varDir,
				KBDir:   "/opt/ccclaw/kb",
				EnvFile: filepath.Join(tmpDir, ".env"),
			},
			Executor:  config.ExecutorConfig{Timeout: "30m", Mode: string(config.ExecutorModeTMux)},
			Scheduler: config.SchedulerConfig{Mode: "none"},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.StatusWithOptions(&out, StatusOptions{JSON: true}); err != nil {
		t.Fatalf("执行 status --json 失败: %v", err)
	}
	var payload struct {
		GeneratedAt string `json:"generated_at"`
		Scheduler   struct {
			Requested      string `json:"requested"`
			Effective      string `json:"effective"`
			MatchesRequest bool   `json:"matches_request"`
		} `json:"scheduler"`
		Tasks struct {
			Total  int            `json:"total"`
			Counts map[string]int `json:"counts"`
			Items  []struct {
				TaskID    string `json:"task_id"`
				Title     string `json:"title"`
				TaskClass string `json:"task_class"`
			} `json:"items"`
		} `json:"tasks"`
		Tokens struct {
			Runs        int    `json:"runs"`
			LastUsedAt  string `json:"last_used_at"`
			InputTokens int    `json:"input_tokens"`
		} `json:"tokens"`
		TaskClass []struct {
			Name    string `json:"name"`
			Label   string `json:"label"`
			Classes []struct {
				TaskClass string `json:"task_class"`
			} `json:"classes"`
		} `json:"task_class"`
		Sessions struct {
			Error string `json:"error"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("解析 JSON 失败: %v; output=%q", err, out.String())
	}
	if payload.GeneratedAt == "" {
		t.Fatalf("expected generated_at in %q", out.String())
	}
	if payload.Scheduler.Requested != "none" || payload.Scheduler.Effective != "none" || !payload.Scheduler.MatchesRequest {
		t.Fatalf("unexpected scheduler payload: %+v", payload.Scheduler)
	}
	if payload.Tasks.Total != 1 || payload.Tasks.Counts[string(core.StateRunning)] != 1 || len(payload.Tasks.Items) != 1 || payload.Tasks.Items[0].TaskID != task.TaskID {
		t.Fatalf("unexpected tasks payload: %+v", payload.Tasks)
	}
	if payload.Tasks.Items[0].TaskClass != string(core.TaskClassGeneral) {
		t.Fatalf("unexpected task class payload: %+v", payload.Tasks.Items[0])
	}
	if payload.Tokens.Runs != 1 || payload.Tokens.InputTokens != 11 || payload.Tokens.LastUsedAt == "" {
		t.Fatalf("unexpected tokens payload: %+v", payload.Tokens)
	}
	if len(payload.TaskClass) != 3 {
		t.Fatalf("unexpected task_class windows payload: %+v", payload.TaskClass)
	}
	if payload.TaskClass[0].Name != "day" || payload.TaskClass[1].Name != "week" || payload.TaskClass[2].Name != "range" {
		t.Fatalf("unexpected task_class window names: %+v", payload.TaskClass)
	}
	if len(payload.TaskClass[0].Classes) != 2 || payload.TaskClass[0].Classes[1].TaskClass != string(core.TaskClassSevolverDeepAnalysis) {
		t.Fatalf("unexpected task_class rows: %+v", payload.TaskClass[0].Classes)
	}
	if !strings.Contains(payload.Sessions.Error, "tmux") {
		t.Fatalf("unexpected sessions payload: %+v", payload.Sessions)
	}
}

func TestStatusJSONIncludesRepoSlotsAndFinalizingTask(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         "21#body",
		IdempotencyKey: "21#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueRepo:      "41490/ccclaw",
		IssueNumber:    21,
		IssueTitle:     "status slot json",
		IssueAuthor:    "zoomq",
		TaskClass:      core.TaskClassGeneral,
		State:          core.StateFinalizing,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := store.UpsertRepoSlot(&storage.RepoSlot{
		TargetRepo:         task.TargetRepo,
		TaskID:             task.TaskID,
		Phase:              storage.RepoSlotPhaseFinalizeFailed,
		CurrentStep:        "sync_home",
		FinalizeRetryStep:  "sync_home",
		FinalizeRetryCount: 2,
		SyncTarget:         storage.FinalizeStepOK,
		SyncHome:           storage.FinalizeStepFailed,
		ReportIssue:        storage.FinalizeStepPending,
		LastAdvanceAt:      time.Date(2026, 3, 13, 10, 0, 0, 0, time.UTC),
		NextRetryAt:        time.Date(2026, 3, 13, 10, 8, 0, 0, time.UTC),
		LastError:          "推送远端失败",
	}); err != nil {
		t.Fatalf("写入仓位失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir:  filepath.Join(tmpDir, "app"),
				LogDir:  filepath.Join(tmpDir, "log"),
				VarDir:  varDir,
				KBDir:   "/opt/ccclaw/kb",
				EnvFile: filepath.Join(tmpDir, ".env"),
			},
			Executor:  config.ExecutorConfig{Timeout: "30m"},
			Scheduler: config.SchedulerConfig{Mode: "none"},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.StatusWithOptions(&out, StatusOptions{JSON: true}); err != nil {
		t.Fatalf("执行 status --json 失败: %v", err)
	}
	var payload struct {
		Tasks struct {
			Counts map[string]int `json:"counts"`
		} `json:"tasks"`
		Slots struct {
			Total int `json:"total"`
			Items []struct {
				Phase              string `json:"phase"`
				CurrentStep        string `json:"current_step"`
				FinalizeRetryStep  string `json:"finalize_retry_step"`
				FinalizeRetryCount int    `json:"finalize_retry_count"`
				NextRetryAt        string `json:"next_retry_at"`
			} `json:"items"`
		} `json:"slots"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("解析 JSON 失败: %v; output=%q", err, out.String())
	}
	if payload.Tasks.Counts[string(core.StateFinalizing)] != 1 {
		t.Fatalf("expected FINALIZING count in %+v", payload.Tasks.Counts)
	}
	if payload.Slots.Total != 1 || len(payload.Slots.Items) != 1 {
		t.Fatalf("unexpected slots payload: %+v", payload.Slots)
	}
	item := payload.Slots.Items[0]
	if item.Phase != string(storage.RepoSlotPhaseFinalizeFailed) || item.CurrentStep != "sync_home" || item.FinalizeRetryStep != "sync_home" || item.FinalizeRetryCount != 2 || item.NextRetryAt == "" {
		t.Fatalf("unexpected slot item: %+v", item)
	}
}

func TestHandleFinalizeFailureBackoffSuppressesEarlyIssueNoise(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	logPath := filepath.Join(tmpDir, "gh.log")
	scriptPath := filepath.Join(fakeBin, "gh")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s
' "$@" >> "$CCCLAW_REPORTER_LOG"
printf '{}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("CCCLAW_REPORTER_LOG", logPath)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         core.TaskID("41490/ccclaw", 42),
		IdempotencyKey: core.IdempotencyKey("41490/ccclaw", 42),
		ControlRepo:    "41490/ccclaw",
		IssueRepo:      "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    42,
		IssueTitle:     "finalize retry",
		State:          core.StateFinalizing,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	slot := &storage.RepoSlot{
		TargetRepo: task.TargetRepo,
		TaskID:     task.TaskID,
		Phase:      storage.RepoSlotPhaseFinalizing,
	}

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{ControlRepo: "41490/ccclaw"},
			Paths:  config.PathsConfig{VarDir: varDir, KBDir: "/opt/ccclaw/kb"},
		},
		store: store,
		rep: reporter.New(func(repo string) *github.Client {
			return github.NewClient(repo, map[string]string{})
		}),
	}

	transientErr := errors.New("context deadline exceeded")
	for attempt := 1; attempt <= maxFinalizeRetry; attempt++ {
		if err := rt.handleFinalizeFailure(task, slot, "target", transientErr, []string{"网络抖动"}); err != nil {
			t.Fatalf("第 %d 次 handleFinalizeFailure 失败: %v", attempt, err)
		}
		payload, readErr := os.ReadFile(logPath)
		if readErr == nil && strings.TrimSpace(string(payload)) != "" {
			t.Fatalf("第 %d 次不应回帖，实际日志=%q", attempt, string(payload))
		}
		loaded, err := store.GetRepoSlot(task.TargetRepo)
		if err != nil {
			t.Fatalf("读取仓位失败: %v", err)
		}
		if loaded == nil || loaded.FinalizeRetryCount != attempt || loaded.NextRetryAt.IsZero() {
			t.Fatalf("unexpected slot after attempt %d: %#v", attempt, loaded)
		}
		slot = loaded
	}

	if err := rt.handleFinalizeFailure(task, slot, "target", transientErr, []string{"网络抖动"}); err != nil {
		t.Fatalf("耗尽后 handleFinalizeFailure 失败: %v", err)
	}
	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 gh 日志失败: %v", err)
	}
	if !strings.Contains(string(payload), "repos/41490/ccclaw/issues/42/comments") {
		t.Fatalf("expected reporter log after retry exhausted, got %q", string(payload))
	}
	loaded, err := store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取仓位失败: %v", err)
	}
	if loaded == nil || loaded.LastReportedFailure == "" || loaded.NextRetryAt.IsZero() {
		t.Fatalf("unexpected final slot: %#v", loaded)
	}
}

func TestStatusWithoutTasksStillShowsSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir:  filepath.Join(tmpDir, "app"),
				LogDir:  filepath.Join(tmpDir, "log"),
				VarDir:  varDir,
				KBDir:   "/opt/ccclaw/kb",
				EnvFile: filepath.Join(tmpDir, ".env"),
			},
			Executor:  config.ExecutorConfig{Timeout: "30m"},
			Scheduler: config.SchedulerConfig{Mode: "none"},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.Status(&out); err != nil {
		t.Fatalf("执行 status 失败: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"当前快照",
		"任务总数: 0",
		"执行器快照:",
		"入口配置: claude",
		"当前无任务",
		"tmux 状态: 当前无 tmux 托管任务（默认 daemon 模式）",
		"未检测到 token 使用记录",
		"最近 7 天无失败/超时告警",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestFormatExecutionFailureKeepsStructuredStreamDecodeContext(t *testing.T) {
	runErr := errors.New("解析 Claude JSON 输出失败: stream-json 解码失败: 解析 stream-json 第 2 行失败: type=debug content[].type=meta_trace: 无法识别事件类型")
	result := &executor.Result{
		Output: "解析 stream-json 第 2 行失败: type=debug content[].type=meta_trace: 无法识别事件类型",
	}

	got := formatExecutionFailure(runErr, result)
	if strings.Contains(got, "hook_started") {
		t.Fatalf("不应再退化成文件前缀诊断，实际为 %q", got)
	}
	if strings.Contains(got, "；诊断输出:") {
		t.Fatalf("结构化解码上下文已在主错误中，不应重复拼接诊断，实际为 %q", got)
	}
	for _, want := range []string{"第 2 行", "type=debug", "content[].type=meta_trace"} {
		if !strings.Contains(got, want) {
			t.Fatalf("预期失败信息包含 %q，实际为 %q", want, got)
		}
	}
}

func TestDescribeExecutorIncludesResolvedBinaryPaths(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	localBin := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("创建 local bin 失败: %v", err)
	}
	for _, name := range []string{"claude", "rtk"} {
		path := filepath.Join(localBin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("写入 fake %s 失败: %v", name, err)
		}
	}
	wrapperPath := filepath.Join(tmpDir, "ccclaude")
	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入 wrapper 失败: %v", err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", "/usr/bin:/bin")

	rt := &Runtime{
		cfg: &config.Config{
			Executor: config.ExecutorConfig{
				Command: []string{wrapperPath},
			},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
	}
	text := rt.describeExecutor()
	for _, want := range []string{
		"configured=" + wrapperPath,
		"resolved=" + wrapperPath,
		"claude=" + filepath.Join(localBin, "claude"),
		"rtk=" + filepath.Join(localBin, "rtk"),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestPatrolFinalizesDeadTMuxSessionFromStdoutStagingFile(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	fixtureDir := filepath.Join(tmpDir, "fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}
	tmuxScript := filepath.Join(fakeBin, "tmux")
	tmuxBody := `#!/usr/bin/env bash
set -euo pipefail
fixture_dir="${TMUX_FIXTURE_DIR:?}"
if [[ "${1:-}" == "list-panes" ]]; then
  cat "$fixture_dir/list-panes.txt"
  exit 0
fi
if [[ "${1:-}" == "kill-session" ]]; then
  printf '%s\n' "${3:-}" >> "$fixture_dir/killed.txt"
  exit 0
fi
if [[ "${1:-}" == "capture-pane" ]]; then
  cat "$fixture_dir/capture.txt"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(tmuxScript, []byte(tmuxBody), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMUX_FIXTURE_DIR", fixtureDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}

	task := &core.Task{
		TaskID:         "10#body",
		IdempotencyKey: "10#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    10,
		IssueTitle:     "tmux patrol",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentResearch,
		RiskLevel:      core.RiskLow,
		State:          core.StateRunning,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	sessionName := executor.SessionName(task.TaskID)
	if err := os.WriteFile(filepath.Join(fixtureDir, "list-panes.txt"), []byte(sessionName+"\t"+strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)+"\t1\t0\t123\n"), 0o644); err != nil {
		t.Fatalf("写入 list-panes fixture 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "capture.txt"), []byte(""), 0o644); err != nil {
		t.Fatalf("写入 capture fixture 失败: %v", err)
	}

	resultDir := filepath.Join(tmpDir, "app", "var", "results")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("创建结果目录失败: %v", err)
	}
	resultPath := filepath.Join(resultDir, "10_body.stdout.txt")
	resultJSON := `{"type":"result","subtype":"success","session_id":"sess-10","duration_ms":2000,"result":"任务完成","total_cost_usd":0.15,"usage":{"input_tokens":50,"output_tokens":20}}`
	if err := os.WriteFile(resultPath, []byte(resultJSON), 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存文件失败: %v", err)
	}

	logDir := filepath.Join(tmpDir, "app", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: filepath.Join(tmpDir, "app"),
				LogDir: logDir,
				VarDir: varDir,
				KBDir:  "/opt/ccclaw/kb",
			},
			Executor: config.ExecutorConfig{
				Command: []string{"/bin/sh"},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: tmpDir,
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}
	rt.syncRepo = func(repoPath, message string, paths []string, maxRetry int) error { return nil }

	var out bytes.Buffer
	if err := rt.Patrol(context.Background(), &out); err != nil {
		t.Fatalf("执行 patrol 失败: %v", err)
	}
	if !strings.Contains(out.String(), "pane_dead=1") {
		t.Fatalf("unexpected patrol output: %q", out.String())
	}
	if err := rt.runIngestCycle(context.Background(), nil, true); err != nil {
		t.Fatalf("执行 ingest cycle 失败: %v", err)
	}

	store, err = storage.Open(varDir)
	if err != nil {
		t.Fatalf("重新打开 store 失败: %v", err)
	}
	defer store.Close()
	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateDone || loaded.LastSessionID != "sess-10" {
		t.Fatalf("unexpected task after patrol: %#v", loaded)
	}
	promotedPayload, err := os.ReadFile(filepath.Join(resultDir, "10_body.json"))
	if err != nil {
		t.Fatalf("读取提升后的结构化结果文件失败: %v", err)
	}
	if strings.TrimSpace(string(promotedPayload)) != resultJSON {
		t.Fatalf("预期 patrol 已提升 stdout 暂存为结构化结果，实际为 %q", string(promotedPayload))
	}
	if _, err := os.Stat(filepath.Join(resultDir, "10_body.stdout.txt")); !os.IsNotExist(err) {
		t.Fatalf("预期 stdout 暂存文件已清理，实际 err=%v", err)
	}
	summary, err := store.TokenStats()
	if err != nil {
		t.Fatalf("读取 token 汇总失败: %v", err)
	}
	if summary.Runs != 1 || summary.InputTokens != 50 {
		t.Fatalf("unexpected summary after patrol: %#v", summary)
	}
}

func TestPatrolFallsBackToDiagnosticFileWhenStructuredResultMissing(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	fixtureDir := filepath.Join(tmpDir, "fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}
	tmuxScript := filepath.Join(fakeBin, "tmux")
	tmuxBody := `#!/usr/bin/env bash
set -euo pipefail
fixture_dir="${TMUX_FIXTURE_DIR:?}"
if [[ "${1:-}" == "list-panes" ]]; then
  cat "$fixture_dir/list-panes.txt"
  exit 0
fi
if [[ "${1:-}" == "kill-session" ]]; then
  exit 0
fi
exit 0
`
	if err := os.WriteFile(tmuxScript, []byte(tmuxBody), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMUX_FIXTURE_DIR", fixtureDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}

	task := &core.Task{
		TaskID:         "34#body",
		IdempotencyKey: "34#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    34,
		IssueTitle:     "stderr raw output",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentFix,
		RiskLevel:      core.RiskLow,
		State:          core.StateRunning,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	sessionName := executor.SessionName(task.TaskID)
	if err := os.WriteFile(filepath.Join(fixtureDir, "list-panes.txt"), []byte(sessionName+"\t"+strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)+"\t1\t127\t123\n"), 0o644); err != nil {
		t.Fatalf("写入 list-panes fixture 失败: %v", err)
	}

	appDir := filepath.Join(tmpDir, "app")
	resultDir := filepath.Join(appDir, "var", "results")
	logDir := filepath.Join(appDir, "log")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("创建结果目录失败: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "34_body.json"), []byte(""), 0o644); err != nil {
		t.Fatalf("写入结构化结果文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "34_body.diag.txt"), []byte("ccclaude: exec: claude: not found\n"), 0o644); err != nil {
		t.Fatalf("写入诊断文件失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: appDir,
				LogDir: logDir,
				VarDir: varDir,
				KBDir:  "/opt/ccclaw/kb",
			},
			Executor: config.ExecutorConfig{
				Command: []string{"/bin/sh"},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: tmpDir,
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.Patrol(context.Background(), &out); err != nil {
		t.Fatalf("执行 patrol 失败: %v", err)
	}
	slot, err := store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取 repo slot 失败: %v", err)
	}
	if slot == nil || slot.Phase != storage.RepoSlotPhasePaneDead {
		t.Fatalf("预期 patrol 仅标记 pane_dead，实际为 %#v", slot)
	}
	if err := rt.runIngestCycle(context.Background(), nil, true); err != nil {
		t.Fatalf("执行 ingest cycle 失败: %v", err)
	}

	store, err = storage.Open(varDir)
	if err != nil {
		t.Fatalf("重新打开 store 失败: %v", err)
	}
	defer store.Close()
	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateFailed {
		t.Fatalf("预期任务进入 FAILED，实际为 %#v", loaded)
	}
	if !strings.Contains(loaded.ErrorMsg, "claude: not found") {
		t.Fatalf("预期保留真实诊断输出，实际为 %q", loaded.ErrorMsg)
	}
	if !strings.Contains(loaded.ErrorMsg, "结构化结果缺失") {
		t.Fatalf("预期明确区分结构化结果缺失，实际为 %q", loaded.ErrorMsg)
	}
}

func TestPatrolUsesLogTailWhenMissingSessionResultIsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	fixtureDir := filepath.Join(tmpDir, "fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}
	tmuxScript := filepath.Join(fakeBin, "tmux")
	tmuxBody := `#!/usr/bin/env bash
set -euo pipefail
fixture_dir="${TMUX_FIXTURE_DIR:?}"
if [[ "${1:-}" == "list-panes" ]]; then
  cat "$fixture_dir/list-panes.txt"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(tmuxScript, []byte(tmuxBody), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMUX_FIXTURE_DIR", fixtureDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}

	task := &core.Task{
		TaskID:         "35#body",
		IdempotencyKey: "35#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    35,
		IssueTitle:     "empty result",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentFix,
		RiskLevel:      core.RiskLow,
		State:          core.StateRunning,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "list-panes.txt"), []byte(""), 0o644); err != nil {
		t.Fatalf("写入 list-panes fixture 失败: %v", err)
	}

	appDir := filepath.Join(tmpDir, "app")
	resultDir := filepath.Join(appDir, "var", "results")
	logDir := filepath.Join(appDir, "log")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("创建结果目录失败: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "35_body.json"), []byte(""), 0o644); err != nil {
		t.Fatalf("写入空结果文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "35_body.diag.txt"), []byte(""), 0o644); err != nil {
		t.Fatalf("写入空诊断文件失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "35_body.log"), []byte("ccclaude: 未找到 Claude 可执行文件\n"), 0o644); err != nil {
		t.Fatalf("写入日志失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: appDir,
				LogDir: logDir,
				VarDir: varDir,
				KBDir:  "/opt/ccclaw/kb",
			},
			Executor: config.ExecutorConfig{
				Command: []string{"/bin/sh"},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: tmpDir,
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.Patrol(context.Background(), &out); err != nil {
		t.Fatalf("执行 patrol 失败: %v", err)
	}
	slot, err := store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取 repo slot 失败: %v", err)
	}
	if slot == nil || slot.Phase != storage.RepoSlotPhasePaneDead {
		t.Fatalf("预期 patrol 仅标记 pane_dead，实际为 %#v", slot)
	}
	if err := rt.runIngestCycle(context.Background(), nil, true); err != nil {
		t.Fatalf("执行 ingest cycle 失败: %v", err)
	}

	store, err = storage.Open(varDir)
	if err != nil {
		t.Fatalf("重新打开 store 失败: %v", err)
	}
	defer store.Close()
	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateFailed {
		t.Fatalf("预期任务进入 FAILED，实际为 %#v", loaded)
	}
	if strings.Contains(loaded.ErrorMsg, "会话") && strings.Contains(loaded.ErrorMsg, "丢失") {
		t.Fatalf("不应再退化成会话丢失，实际为 %q", loaded.ErrorMsg)
	}
	if !strings.Contains(loaded.ErrorMsg, "未找到 Claude 可执行文件") {
		t.Fatalf("预期回退到日志尾部诊断，实际为 %q", loaded.ErrorMsg)
	}
}

func TestIngestDispatchFinalizesImmediatelyWhenSessionMissingRightAfterLaunch(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	appDir := filepath.Join(tmpDir, "app")
	resultDir := filepath.Join(appDir, "var", "results")
	logDir := filepath.Join(appDir, "log")
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("创建结果目录失败: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	diagPath := filepath.Join(resultDir, "44_body.diag.txt")
	tmuxScript := filepath.Join(fakeBin, "tmux")
	tmuxBody := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "new-session" ]]; then
  printf '%s\n' 'ccclaude: launch hook crashed' > "${TEST_DIAG_FILE:?}"
  exit 0
fi
if [[ "${1:-}" == "set-option" ]]; then
  exit 0
fi
if [[ "${1:-}" == "list-panes" ]]; then
  printf '%s\n' 'no server running on /tmp/tmux-1000/default' >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(tmuxScript, []byte(tmuxBody), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TEST_DIAG_FILE", diagPath)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         "44#body",
		IdempotencyKey: "44#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueRepo:      "41490/ccclaw",
		IssueNumber:    44,
		IssueTitle:     "launch then missing session",
		IssueBody:      "请修复 tmux no server",
		IssueAuthor:    "ZoomQuiet",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentFix,
		RiskLevel:      core.RiskLow,
		State:          core.StateNew,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	targetPath := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("创建 target 目录失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: appDir,
				LogDir: logDir,
				VarDir: varDir,
				KBDir:  "/opt/ccclaw/kb",
			},
			Executor: config.ExecutorConfig{
				Command: []string{"/bin/sh"},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: targetPath,
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	if err := rt.runIngestCycle(context.Background(), nil, false); err != nil {
		t.Fatalf("执行 ingest cycle 失败: %v", err)
	}

	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateFailed {
		t.Fatalf("预期任务同轮进入 FAILED，实际为 %#v", loaded)
	}
	if !strings.Contains(loaded.ErrorMsg, "发射后立即丢失") {
		t.Fatalf("预期失败信息包含即时自检结论，实际为 %q", loaded.ErrorMsg)
	}
	if !strings.Contains(loaded.ErrorMsg, "launch hook crashed") {
		t.Fatalf("预期失败信息包含最早期诊断输出，实际为 %q", loaded.ErrorMsg)
	}

	slot, err := store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取 repo slot 失败: %v", err)
	}
	if slot != nil {
		t.Fatalf("预期槽位已同轮收口删除，实际为 %#v", slot)
	}

	events, err := store.ListTaskEvents(20)
	if err != nil {
		t.Fatalf("读取任务事件失败: %v", err)
	}
	hasWarning := false
	for _, item := range events {
		if item.TaskID == task.TaskID && item.EventType == core.EventWarning && strings.Contains(item.Detail, "发射后立即丢失") {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Fatalf("预期产生即时告警事件，实际事件=%#v", events)
	}
}

func TestPatrolFreshRestartsWhenUsableContextExceedsThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	fixtureDir := filepath.Join(tmpDir, "fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("创建 fixture 目录失败: %v", err)
	}
	tmuxScript := filepath.Join(fakeBin, "tmux")
	tmuxBody := `#!/usr/bin/env bash
set -euo pipefail
fixture_dir="${TMUX_FIXTURE_DIR:?}"
if [[ "${1:-}" == "list-panes" ]]; then
  cat "$fixture_dir/list-panes.txt"
  exit 0
fi
if [[ "${1:-}" == "kill-session" ]]; then
  printf '%s\n' "${3:-}" >> "$fixture_dir/killed.txt"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(tmuxScript, []byte(tmuxBody), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMUX_FIXTURE_DIR", fixtureDir)

	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}

	task := &core.Task{
		TaskID:         "64#body",
		IdempotencyKey: "64#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    64,
		IssueTitle:     "context restart",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentFix,
		RiskLevel:      core.RiskLow,
		State:          core.StateRunning,
		LastSessionID:  "sess-old",
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	sessionName := executor.SessionName(task.TaskID)
	if err := os.WriteFile(filepath.Join(fixtureDir, "list-panes.txt"), []byte(sessionName+"\t"+strconv.FormatInt(time.Now().Add(-2*time.Minute).Unix(), 10)+"\t0\t\t123\n"), 0o644); err != nil {
		t.Fatalf("写入 list-panes fixture 失败: %v", err)
	}

	appDir := filepath.Join(tmpDir, "app")
	logDir := filepath.Join(appDir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	hookStateDir := filepath.Join(appDir, "var", "claude-hooks")
	if err := os.MkdirAll(hookStateDir, 0o755); err != nil {
		t.Fatalf("创建 hook 状态目录失败: %v", err)
	}
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	transcript := `{"timestamp":"2026-03-12T10:02:00Z","message":{"model":"Claude Sonnet 4","usage":{"input_tokens":80000,"cache_creation_input_tokens":20000,"cache_read_input_tokens":12000}}}`
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("写入 transcript 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hookStateDir, "64_body.json"), []byte(`{
  "task_id": "64#body",
  "session_id": "sess-new",
  "transcript_path": "`+transcriptPath+`",
  "model": "Claude Sonnet 4",
  "context_window_size": 200000
}`), 0o644); err != nil {
		t.Fatalf("写入 hook 状态失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: appDir,
				LogDir: logDir,
				VarDir: varDir,
				KBDir:  "/opt/ccclaw/kb",
			},
			Executor: config.ExecutorConfig{
				Command: []string{"/bin/sh"},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: tmpDir,
				KBPath:    "/opt/ccclaw/kb",
			}},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}

	var out bytes.Buffer
	if err := rt.Patrol(context.Background(), &out); err != nil {
		t.Fatalf("执行 patrol 失败: %v", err)
	}
	slot, err := store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取 repo slot 失败: %v", err)
	}
	if slot == nil || slot.Phase != storage.RepoSlotPhaseRunning {
		t.Fatalf("预期任务仍保持运行槽位，实际为 %#v", slot)
	}

	store, err = storage.Open(varDir)
	if err != nil {
		t.Fatalf("重新打开 store 失败: %v", err)
	}
	defer store.Close()
	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateRunning {
		t.Fatalf("预期任务保持 RUNNING，实际为 %#v", loaded)
	}
	if loaded.RestartCount != 0 {
		t.Fatalf("预期 RestartCount 仍由 sidecar 管理，实际为 %#v", loaded)
	}
	if loaded.LastSessionID != "sess-new" {
		t.Fatalf("预期仅同步最新 session_id，实际为 %#v", loaded)
	}
	if _, err := os.Stat(filepath.Join(hookStateDir, "64_body.json")); !os.IsNotExist(err) {
		t.Fatalf("预期 hook 状态已清理，实际 err=%v", err)
	}
	killed, err := os.ReadFile(filepath.Join(fixtureDir, "killed.txt"))
	if err != nil {
		t.Fatalf("读取 killed 记录失败: %v", err)
	}
	if strings.TrimSpace(string(killed)) != sessionName {
		t.Fatalf("预期 kill 当前 session，实际为 %q", string(killed))
	}
	slot, err = store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取 repo slot 失败: %v", err)
	}
	if slot == nil || slot.Phase != storage.RepoSlotPhaseRunning || slot.RestartCount != 1 {
		t.Fatalf("预期 sidecar 记录重启后的运行态，实际为 %#v", slot)
	}
}

func TestJournalWritesDailyFile(t *testing.T) {
	tmpDir := t.TempDir()
	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}

	task := &core.Task{
		TaskID:         "10#body",
		IdempotencyKey: "10#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueNumber:    10,
		IssueTitle:     "journal",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentResearch,
		RiskLevel:      core.RiskLow,
		State:          core.StateDone,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}
	if err := store.AppendEvent(task.TaskID, core.EventDone, "tmux 会话完成"); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}
	day := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	if err := store.RecordTokenUsage(storage.TokenUsageRecord{
		TaskID:     task.TaskID,
		SessionID:  "sess-10",
		PromptFile: filepath.Join(tmpDir, "prompt.md"),
		Usage:      core.TokenUsage{InputTokens: 40, OutputTokens: 15},
		CostUSD:    0.12,
		RTKEnabled: true,
		RecordedAt: day,
	}); err != nil {
		t.Fatalf("写入 token 失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			GitHub: config.GitHubConfig{
				ControlRepo: "41490/ccclaw",
			},
			Paths: config.PathsConfig{
				HomeRepo: tmpDir,
				VarDir:   varDir,
				KBDir:    filepath.Join(tmpDir, "kb"),
			},
			Targets: []config.TargetConfig{{
				Repo:      "41490/ccclaw",
				LocalPath: filepath.Join(tmpDir, "target"),
			}},
		},
		store: store,
		syncRepo: func(repoPath, message string, paths []string, maxRetry int) error {
			if repoPath != tmpDir {
				t.Fatalf("unexpected sync repo path: %s", repoPath)
			}
			if !strings.Contains(message, "journal: 2026-03-10") {
				t.Fatalf("unexpected sync message: %s", message)
			}
			if len(paths) != 4 {
				t.Fatalf("unexpected sync paths: %#v", paths)
			}
			return nil
		},
	}

	var out bytes.Buffer
	if err := rt.Journal(day, &out); err != nil {
		t.Fatalf("执行 journal 失败: %v", err)
	}
	if !strings.Contains(out.String(), "journal 已生成:") {
		t.Fatalf("unexpected journal output: %q", out.String())
	}
	path := filepath.Join(tmpDir, "kb", "journal", "2026", "03", "2026.03.10."+journalUserName()+".41490-ccclaw.ccclaw_log.md")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 journal 文件失败: %v", err)
	}
	text := string(payload)
	for _, want := range []string{"# ccclaw journal 2026-03-10 [41490/ccclaw]", "任务触达数", "巡查事件"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}

	for path, wants := range map[string][]string{
		filepath.Join(tmpDir, "kb", "journal", "2026", "03", "summary.md"): {
			"# ccclaw journal 月汇总 2026-03",
			"2026.03.10",
		},
		filepath.Join(tmpDir, "kb", "journal", "2026", "summary.md"): {
			"# ccclaw journal 年汇总 2026",
			"./03/summary.md",
		},
		filepath.Join(tmpDir, "kb", "journal", "summary.md"): {
			"# ccclaw journal 总览",
			"./2026/summary.md",
		},
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 summary 文件失败(%s): %v", path, err)
		}
		text := string(payload)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Fatalf("expected %q in %q", want, text)
			}
		}
	}
}

func TestFinishTaskExecutionClearsResumeSessionAfterFailure(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "var"))
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         "30#body",
		IdempotencyKey: "30#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		LastSessionID:  "sess-30",
		IssueNumber:    30,
		IssueTitle:     "resume fallback",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentFix,
		RiskLevel:      core.RiskLow,
		State:          core.StateFailed,
		RetryCount:     1,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	rt := &Runtime{store: store}
	if err := rt.finishTaskExecution(task, &executor.Result{
		ResumeSessionID: "sess-30",
		PromptFile:      filepath.Join(t.TempDir(), "prompt.md"),
	}, context.DeadlineExceeded); err != nil {
		t.Fatalf("finishTaskExecution 失败: %v", err)
	}

	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded.LastSessionID != "" {
		t.Fatalf("expected cleared session id, got %#v", loaded)
	}
	events, err := store.ListTaskEvents(10)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	found := false
	for _, event := range events {
		if event.EventType == core.EventWarning && strings.Contains(event.Detail, "降级为新执行") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected resume fallback warning, got %#v", events)
	}
}

func TestBuildPromptUsesTargetKBPathAndSummaryOnly(t *testing.T) {
	base := t.TempDir()
	globalKB := filepath.Join(base, "kb-global")
	targetKB := filepath.Join(base, "kb-target")
	globalDoc := filepath.Join(globalKB, "skills", "summary.md")
	targetDoc := filepath.Join(targetKB, "skills", "git-conflict-resolve", "CLAUDE.md")
	for _, dir := range []string{filepath.Dir(globalDoc), filepath.Dir(targetDoc)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
	}
	if err := os.WriteFile(globalDoc, []byte("# 全局\n\n这份全局知识不应进入 prompt。"), 0o644); err != nil {
		t.Fatalf("写入全局 kb 失败: %v", err)
	}
	targetContent := `---
name: git-conflict-resolve
description: 处理 git 冲突的最小路径
keywords: [git, conflict, resolve]
---
# 细节正文

SECRET-LONG-BODY
`
	if err := os.WriteFile(targetDoc, []byte(targetContent), 0o644); err != nil {
		t.Fatalf("写入 target kb 失败: %v", err)
	}
	globalIndex, err := memory.Build(globalKB)
	if err != nil {
		t.Fatalf("构建全局索引失败: %v", err)
	}

	rt := &Runtime{
		mem:      globalIndex,
		memRoot:  globalKB,
		memCache: map[string]*memory.Index{globalKB: globalIndex},
	}
	task := &core.Task{
		ControlRepo: "41490/ccclaw",
		IssueNumber: 14,
		IssueTitle:  "处理 git conflict",
		IssueBody:   "请整理最小步骤",
		IssueAuthor: "ZoomQuiet",
		Labels:      []string{"ccclaw"},
		Intent:      core.IntentFix,
		TaskClass:   core.TaskClassGeneral,
		RiskLevel:   core.RiskLow,
	}
	target := &config.TargetConfig{
		Repo:      "41490/ccclaw",
		LocalPath: "/opt/src/ccclaw",
		KBPath:    targetKB,
	}

	prompt := rt.buildPrompt(task, target)
	for _, want := range []string{
		"kb_path: " + targetKB,
		"git-conflict-resolve",
		"处理 git 冲突的最小路径",
		"不要直接执行 `gh issue comment`",
		"由 reporter 统一回帖并追加 `/ccclaw [DONE]`",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt: %q", want, prompt)
		}
	}
	for _, unwanted := range []string{"SECRET-LONG-BODY", "这份全局知识不应进入 prompt"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("did not expect %q in prompt: %q", unwanted, prompt)
		}
	}
}

func TestBuildPromptUsesSevolverDeepAnalysisTemplate(t *testing.T) {
	rt := &Runtime{}
	task := &core.Task{
		ControlRepo: "41490/ccclaw",
		IssueRepo:   "41490/ccclaw",
		IssueNumber: 36,
		IssueTitle:  "[sevolver] 能力缺口深度分析 2026-03-12",
		IssueBody:   "task_class: sevolver_deep_analysis",
		IssueAuthor: "ZoomQuiet",
		Labels:      []string{"ccclaw", "sevolver"},
		Intent:      core.IntentResearch,
		TaskClass:   core.TaskClassSevolverDeepAnalysis,
		RiskLevel:   core.RiskLow,
	}
	target := &config.TargetConfig{
		Repo:      "41490/ccclaw",
		LocalPath: "/opt/src/ccclaw",
		KBPath:    t.TempDir(),
	}

	prompt := rt.buildPrompt(task, target)
	for _, want := range []string{
		"任务分类: sevolver_deep_analysis",
		"本任务属于 `sevolver_deep_analysis`",
		"gap 样本与 fingerprint",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt: %q", want, prompt)
		}
	}
}

func TestNewExecutorForRepoRespectsModeOverride(t *testing.T) {
	tmpDir := t.TempDir()
	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	tmuxScript := filepath.Join(fakeBin, "tmux")
	if err := os.WriteFile(tmuxScript, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入 fake tmux 失败: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: tmpDir,
				LogDir: filepath.Join(tmpDir, "log"),
			},
			Executor: config.ExecutorConfig{
				Command: []string{"/bin/sh"},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{
				{Repo: "41490/ccclaw", LocalPath: tmpDir, ExecutorMode: string(config.ExecutorModeDaemon)},
				{Repo: "41490/other", LocalPath: tmpDir},
			},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
	}

	daemonExec, daemonMode, err := rt.newExecutorForRepo("41490/ccclaw")
	if err != nil {
		t.Fatalf("创建 daemon 执行器失败: %v", err)
	}
	if daemonMode != config.ExecutorModeDaemon || daemonExec.TMux() != nil {
		t.Fatalf("expected daemon executor without tmux, mode=%s engine=%#v", daemonMode, daemonExec)
	}

	tmuxExec, tmuxMode, err := rt.newExecutorForRepo("41490/other")
	if err != nil {
		t.Fatalf("创建 tmux 执行器失败: %v", err)
	}
	if tmuxMode != config.ExecutorModeTMux || tmuxExec.TMux() == nil {
		t.Fatalf("expected tmux executor, mode=%s engine=%#v", tmuxMode, tmuxExec)
	}
}

func TestRunIngestCycleDaemonModeDoesNotDependOnTMuxPatrol(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("PATH", tmpDir)
	varDir := filepath.Join(tmpDir, "var")
	store, err := storage.Open(varDir)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	defer store.Close()

	task := &core.Task{
		TaskID:         "48#body",
		IdempotencyKey: "48#body",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IssueRepo:      "41490/ccclaw",
		IssueNumber:    48,
		IssueTitle:     "daemon mode",
		IssueBody:      "phase2 daemon execution",
		IssueAuthor:    "ZoomQuiet",
		Labels:         []string{"ccclaw"},
		Intent:         core.IntentFix,
		RiskLevel:      core.RiskLow,
		State:          core.StateNew,
	}
	if err := store.UpsertTask(task); err != nil {
		t.Fatalf("写入任务失败: %v", err)
	}

	targetPath := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("创建 target 目录失败: %v", err)
	}
	execScript := filepath.Join(tmpDir, "fake-stream.sh")
	stream := `#!/bin/sh
printf '%s\n' '{"event":"started","timestamp":"2026-03-13T20:00:00Z","session_id":"sess-48-a","message":"start","step":"execute"}'
printf '%s\n' '{"event":"result","timestamp":"2026-03-13T20:00:02Z","session_id":"sess-48-a","result":"daemon done","total_cost_usd":0.21,"usage":{"input_tokens":30,"output_tokens":12}}'
`
	if err := os.WriteFile(execScript, []byte(stream), 0o755); err != nil {
		t.Fatalf("写入执行脚本失败: %v", err)
	}

	rt := &Runtime{
		cfg: &config.Config{
			Paths: config.PathsConfig{
				AppDir: filepath.Join(tmpDir, "app"),
				LogDir: filepath.Join(tmpDir, "log"),
				VarDir: varDir,
				KBDir:  "/opt/ccclaw/kb",
			},
			Executor: config.ExecutorConfig{
				Command: []string{execScript},
				Timeout: "30m",
				Mode:    string(config.ExecutorModeTMux),
			},
			Targets: []config.TargetConfig{{
				Repo:         "41490/ccclaw",
				LocalPath:    targetPath,
				KBPath:       "/opt/ccclaw/kb",
				ExecutorMode: string(config.ExecutorModeDaemon),
			}},
		},
		secrets: &config.Secrets{Values: map[string]string{}},
		store:   store,
	}
	rt.syncRepo = func(repoPath, message string, paths []string, maxRetry int) error { return nil }

	if err := rt.runIngestCycle(context.Background(), nil, false); err != nil {
		t.Fatalf("执行 ingest cycle 失败: %v", err)
	}
	loaded, err := store.GetByIdempotency(task.IdempotencyKey)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if loaded == nil || loaded.State != core.StateDone {
		t.Fatalf("预期 daemon 模式任务同轮 DONE，实际为 %#v", loaded)
	}
	slot, err := store.GetRepoSlot(task.TargetRepo)
	if err != nil {
		t.Fatalf("读取 repo slot 失败: %v", err)
	}
	if slot != nil {
		t.Fatalf("预期 daemon 模式无需 tmux 巡查槽位，实际为 %#v", slot)
	}
}
