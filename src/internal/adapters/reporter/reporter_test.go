package reporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/adapters/github"
	"github.com/41490/ccclaw/internal/adapters/storage"
	"github.com/41490/ccclaw/internal/core"
)

func TestClassifyFailureStage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		task    *core.Task
		want    string
		diag    string
		errLine string
	}{
		{
			name: "startup",
			task: &core.Task{ErrorMsg: "tmux 会话退出码=127；诊断输出: ccclaude: exec: claude: not found"},
			want: "启动阶段",
			diag: "ccclaude: exec: claude: not found",
		},
		{
			name: "execution",
			task: &core.Task{ErrorMsg: "tmux 会话 ccclaw-34 已运行 31m，超过超时阈值 30m"},
			want: "执行阶段",
		},
		{
			name: "finalize",
			task: &core.Task{ErrorMsg: "解析 Claude JSON 输出失败: stdout 为空"},
			want: "收口阶段",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFailureStage(tc.task); got != tc.want {
				t.Fatalf("unexpected stage: got=%q want=%q", got, tc.want)
			}
			errLine, diag := splitFailureMessage(tc.task.ErrorMsg)
			if tc.diag != "" && diag != tc.diag {
				t.Fatalf("unexpected diagnostic: got=%q want=%q", diag, tc.diag)
			}
			if errLine == "" {
				t.Fatal("expected primary error line")
			}
		})
	}
}

func TestReportFailureIncludesStageAndDiagnostic(t *testing.T) {
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
' "$@" > "$CCCLAW_REPORTER_LOG"
printf '{}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("CCCLAW_REPORTER_LOG", logPath)

	rep := New(func(repo string) *github.Client {
		return github.NewClient(repo, map[string]string{})
	})
	task := &core.Task{
		IssueRepo:      "41490/ccclaw",
		IssueNumber:    34,
		State:          core.StateFailed,
		RetryCount:     1,
		ErrorMsg:       "tmux 会话退出码=127；诊断输出: ccclaude: exec: claude: not found",
		ControlRepo:    "41490/ccclaw",
		TargetRepo:     "41490/ccclaw",
		IdempotencyKey: "41490/ccclaw#34#body",
	}
	if err := rep.ReportFailure(task); err != nil {
		t.Fatalf("ReportFailure failed: %v", err)
	}

	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 gh 日志失败: %v", err)
	}
	text := string(payload)
	for _, want := range []string{
		"repos/41490/ccclaw/issues/34/comments",
		"阶段: `启动阶段`",
		"错误: `tmux 会话退出码=127`",
		"诊断: `ccclaude: exec: claude: not found`",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestReportSuccessAppendsDoneMarker(t *testing.T) {
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
' "$@" > "$CCCLAW_REPORTER_LOG"
printf '{}\n'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("CCCLAW_REPORTER_LOG", logPath)

	rep := New(func(repo string) *github.Client {
		return github.NewClient(repo, map[string]string{})
	})
	task := &core.Task{
		IssueRepo:   "41490/ccclaw",
		IssueNumber: 37,
		State:       core.StateDone,
		ReportPath:  "docs/reports/260312_37_done.md",
	}
	if _, err := rep.ReportSuccess(task, 2*time.Second, "/tmp/task.log"); err != nil {
		t.Fatalf("ReportSuccess failed: %v", err)
	}

	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 gh 日志失败: %v", err)
	}
	if !strings.Contains(string(payload), github.DoneMarker) {
		t.Fatalf("expected done marker in %q", string(payload))
	}
}

func TestReportResultReadyDoesNotAppendDoneMarker(t *testing.T) {
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
' "$*" > "$CCCLAW_REPORTER_LOG"
printf '{"id":701}'`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("CCCLAW_REPORTER_LOG", logPath)

	rep := New(func(repo string) *github.Client {
		return github.NewClient(repo, map[string]string{})
	})
	task := &core.Task{
		IssueRepo:   "41490/ccclaw",
		IssueNumber: 38,
		State:       core.StateFinalizing,
		ReportPath:  "docs/reports/260318_85_ready.md",
	}
	if _, err := rep.ReportResultReady(task, 3*time.Second, "/tmp/task.log"); err != nil {
		t.Fatalf("ReportResultReady failed: %v", err)
	}

	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 gh 日志失败: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "任务执行结果已形成，正在执行交付收尾。") {
		t.Fatalf("expected ready text in %q", text)
	}
	if strings.Contains(text, github.DoneMarker) {
		t.Fatalf("ready comment should not append done marker: %q", text)
	}
}

func TestPromoteResultToDoneUpdatesExistingComment(t *testing.T) {
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
' "$*" > "$CCCLAW_REPORTER_LOG"
printf '{"id":701}'`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("CCCLAW_REPORTER_LOG", logPath)

	rep := New(func(repo string) *github.Client {
		return github.NewClient(repo, map[string]string{})
	})
	task := &core.Task{
		IssueRepo:   "41490/ccclaw",
		IssueNumber: 39,
		State:       core.StateDone,
		ReportPath:  "docs/reports/260318_85_done.md",
	}
	if _, err := rep.PromoteResultToDone(task, 2*time.Second, "/tmp/task.log", 701); err != nil {
		t.Fatalf("PromoteResultToDone failed: %v", err)
	}

	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 gh 日志失败: %v", err)
	}
	text := string(payload)
	for _, want := range []string{
		"repos/41490/ccclaw/issues/comments/701",
		"--method PATCH",
		github.DoneMarker,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestReportFinalizingIncludesFailureClassAndMode(t *testing.T) {
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
' "$*" > "$CCCLAW_REPORTER_LOG"
printf '{}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入 fake gh 失败: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+oldPath)
	t.Setenv("CCCLAW_REPORTER_LOG", logPath)

	rep := New(func(repo string) *github.Client {
		return github.NewClient(repo, map[string]string{})
	})
	task := &core.Task{
		IssueRepo:   "41490/ccclaw",
		IssueNumber: 40,
		State:       core.StateFinalizing,
	}
	if err := rep.ReportFinalizing(task, "sync_target", storage.FinalizeFailureClassProtection, storage.FinalizeFailureModePause, "protected branch", []string{"改走 PR"}); err != nil {
		t.Fatalf("ReportFinalizing failed: %v", err)
	}

	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读取 gh 日志失败: %v", err)
	}
	text := string(payload)
	for _, want := range []string{
		"业务执行: `成功`",
		"收尾同步: `失败`",
		"生命周期: `未写回 DONE`",
		"当前失败步骤: `sync_target`（目标仓同步）",
		"失败类型: `protection`（分支保护）",
		"处理策略: `pause`（需人工介入）",
		"改走 PR",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}
