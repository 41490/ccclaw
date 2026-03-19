package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/adapters/storage"
	"github.com/41490/ccclaw/internal/claude"
	"github.com/41490/ccclaw/internal/config"
	"github.com/41490/ccclaw/internal/core"
	"github.com/41490/ccclaw/internal/executor"
	"github.com/41490/ccclaw/internal/tmux"
	"github.com/41490/ccclaw/internal/vcs"
)

const (
	finalizeRetryBaseDelay   = 4 * time.Minute
	finalizeRetryManualDelay = 1 * time.Hour
	finalizeRetryMaxDelay    = 1 * time.Hour
	maxFinalizeRetry         = 4
)

func (rt *Runtime) runIngestCycle(ctx context.Context, out io.Writer, dispatchOnly bool) error {
	advanced, err := rt.advanceRepoSlots(ctx)
	if err != nil {
		return err
	}
	if dispatchOnly {
		return rt.dispatchNextByRepo(ctx, out, advanced)
	}
	return rt.dispatchNextByRepo(ctx, out, advanced)
}

func (rt *Runtime) advanceRepoSlots(ctx context.Context) (map[string]struct{}, error) {
	if err := rt.hydrateRepoSlots(); err != nil {
		return nil, err
	}
	slots, err := rt.store.ListRepoSlots()
	if err != nil {
		return nil, err
	}
	advanced := map[string]struct{}{}
	for _, slot := range slots {
		if slot == nil {
			continue
		}
		mode := config.ExecutorMode(strings.TrimSpace(slot.ExecutorMode))
		if mode == "" {
			mode = rt.cfg.ExecutorModeForRepo(slot.TargetRepo)
			slot.ExecutorMode = string(mode)
		}
		execEngine, err := rt.newExecutorForMode(mode)
		if err != nil {
			return nil, err
		}
		advanced[slot.TargetRepo] = struct{}{}
		if err := rt.advanceRepoSlot(ctx, execEngine, slot, mode); err != nil {
			return nil, err
		}
	}
	return advanced, nil
}

func (rt *Runtime) hydrateRepoSlots() error {
	tasks, err := rt.store.ListTasks()
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task == nil || strings.TrimSpace(task.TargetRepo) == "" {
			continue
		}
		if task.State != core.StateRunning && task.State != core.StateFinalizing {
			continue
		}
		slot, err := rt.store.GetRepoSlot(task.TargetRepo)
		if err != nil {
			return err
		}
		if slot != nil && strings.TrimSpace(slot.TaskID) != "" {
			if strings.TrimSpace(slot.ExecutorMode) == "" {
				slot.ExecutorMode = string(rt.cfg.ExecutorModeForRepo(task.TargetRepo))
				if err := rt.store.UpsertRepoSlot(slot); err != nil {
					return err
				}
			}
			continue
		}
		phase := storage.RepoSlotPhaseRunning
		if task.State == core.StateFinalizing {
			phase = storage.RepoSlotPhaseFinalizing
		}
		if err := rt.store.UpsertRepoSlot(&storage.RepoSlot{
			TargetRepo:    task.TargetRepo,
			TaskID:        task.TaskID,
			ExecutorMode:  string(rt.cfg.ExecutorModeForRepo(task.TargetRepo)),
			SessionName:   executor.SessionName(task.TaskID),
			Phase:         phase,
			CurrentStep:   defaultSlotStepForTask(task.State),
			LastAdvanceAt: time.Now().UTC(),
			LastProbeAt:   time.Now().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (rt *Runtime) advanceRepoSlot(ctx context.Context, execEngine *executor.Executor, slot *storage.RepoSlot, mode config.ExecutorMode) error {
	if slot == nil || strings.TrimSpace(slot.TargetRepo) == "" {
		return nil
	}
	if mode == "" {
		mode = rt.cfg.ExecutorModeForRepo(slot.TargetRepo)
	}
	rt.observeStreamEventContract(execEngine, slot)
	if slot.Phase == storage.RepoSlotPhaseFinalizeFailed && !slot.NextRetryAt.IsZero() && time.Now().UTC().Before(slot.NextRetryAt) {
		return nil
	}
	if mode == config.ExecutorModeDaemon {
		switch slot.Phase {
		case storage.RepoSlotPhaseRunning, storage.RepoSlotPhaseRestarting:
			advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
			slot.CompletedAt = time.Now().UTC()
			slot.LastProbeAt = time.Now().UTC()
			if err := rt.store.UpsertRepoSlot(slot); err != nil {
				return err
			}
			return rt.finalizeRepoSlot(ctx, execEngine, slot)
		case storage.RepoSlotPhasePaneDead, storage.RepoSlotPhaseFinalizing, storage.RepoSlotPhaseFinalizeFailed:
			return rt.finalizeRepoSlot(ctx, execEngine, slot)
		default:
			return nil
		}
	}
	switch slot.Phase {
	case storage.RepoSlotPhaseRunning, storage.RepoSlotPhaseRestarting:
		return rt.refreshRunningRepoSlot(execEngine, slot)
	case storage.RepoSlotPhasePaneDead, storage.RepoSlotPhaseFinalizing, storage.RepoSlotPhaseFinalizeFailed:
		return rt.finalizeRepoSlot(ctx, execEngine, slot)
	default:
		return nil
	}
}

func (rt *Runtime) refreshRunningRepoSlot(execEngine *executor.Executor, slot *storage.RepoSlot) error {
	if slot == nil || execEngine == nil {
		return nil
	}
	manager := execEngine.TMux()
	if manager == nil || strings.TrimSpace(slot.SessionName) == "" {
		return nil
	}
	status, err := manager.Status(slot.SessionName)
	if err != nil {
		if errors.Is(err, tmux.ErrSessionNotFound) {
			advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
			slot.CompletedAt = time.Now().UTC()
			slot.LastError = "tmux 会话已退出，等待 ingest 收口"
			return rt.store.UpsertRepoSlot(slot)
		}
		return err
	}
	if status.PaneDead {
		advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
		slot.CompletedAt = time.Now().UTC()
		slot.LastProbeAt = time.Now().UTC()
		return rt.store.UpsertRepoSlot(slot)
	}
	return nil
}

func (rt *Runtime) dispatchNextByRepo(ctx context.Context, out io.Writer, skipRepos map[string]struct{}) error {
	grouped, err := rt.store.ListRunnableByTarget(1)
	if err != nil {
		return err
	}
	targets := rt.cfg.EnabledTargets()
	dispatched := 0
	for idx := range targets {
		target := &targets[idx]
		if _, skipped := skipRepos[target.Repo]; skipped {
			continue
		}
		slot, err := rt.store.GetRepoSlot(target.Repo)
		if err != nil {
			return err
		}
		if slot != nil && slot.Phase != "" {
			continue
		}
		queue := grouped[target.Repo]
		if len(queue) == 0 {
			continue
		}
		if err := rt.dispatchTask(ctx, queue[0], target); err != nil {
			return err
		}
		dispatched++
	}
	if out != nil {
		if dispatched == 0 {
			_, _ = fmt.Fprintln(out, "暂无可发射任务")
		} else {
			_, _ = fmt.Fprintf(out, "本轮已发射 %d 个仓库槽位任务\n", dispatched)
		}
	}
	return nil
}

func (rt *Runtime) dispatchTask(ctx context.Context, task *core.Task, target *config.TargetConfig) error {
	if task == nil || target == nil {
		return nil
	}
	execEngine, mode, err := rt.newExecutorForRepo(target.Repo)
	if err != nil {
		return err
	}
	if err := rt.ensureClaudeManagedHooks(target.LocalPath); err != nil {
		return err
	}
	runOpts := rt.buildExecutionOptions(task, target)
	fresh, err := rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
		if existing == nil {
			return nil, fmt.Errorf("任务不存在: %s", task.TaskID)
		}
		existing.State = core.StateRunning
		existing.ErrorMsg = ""
		return existing, nil
	})
	if err != nil {
		return err
	}
	_ = rt.store.AppendEvent(fresh.TaskID, core.EventStarted, "开始执行任务")
	if strings.TrimSpace(runOpts.ResumeSessionID) != "" {
		_ = rt.store.AppendEvent(fresh.TaskID, core.EventUpdated, fmt.Sprintf("检测到失败重试，尝试恢复 session %s", runOpts.ResumeSessionID))
	} else if err := claude.ClearHookState(rt.claudeHookStateDir(), string(fresh.TaskID)); err != nil {
		rt.logWarning("ingest", "清理旧 Claude hook 状态失败", "issue", rt.issueRef(fresh.IssueRepo, fresh.IssueNumber), "error", err)
	}

	result, runErr := execEngine.Run(ctx, target.LocalPath, fresh.TaskID, runOpts)
	if result != nil && result.Pending {
		slot := &storage.RepoSlot{
			TargetRepo:    fresh.TargetRepo,
			TaskID:        fresh.TaskID,
			ExecutorMode:  string(mode),
			SessionName:   result.SessionName,
			Phase:         storage.RepoSlotPhaseRunning,
			CurrentStep:   "execute",
			LastAdvanceAt: time.Now().UTC(),
			LastProbeAt:   time.Now().UTC(),
		}
		if err := rt.store.UpsertRepoSlot(slot); err != nil {
			return err
		}
		_ = rt.store.AppendEvent(fresh.TaskID, core.EventUpdated, fmt.Sprintf("任务已挂入 tmux 会话 %s", result.SessionName))
		rt.reportStarted(fresh, result.SessionName)
		return rt.probeDispatchedTMuxSession(ctx, execEngine, fresh, slot)
	}
	if result == nil && runErr != nil && strings.TrimSpace(runOpts.ResumeSessionID) != "" {
		result = &executor.Result{ResumeSessionID: runOpts.ResumeSessionID}
	}
	slot := &storage.RepoSlot{
		TargetRepo:    fresh.TargetRepo,
		TaskID:        fresh.TaskID,
		ExecutorMode:  string(mode),
		SessionName:   executor.SessionName(fresh.TaskID),
		Phase:         storage.RepoSlotPhasePaneDead,
		CurrentStep:   "load_result",
		LastAdvanceAt: time.Now().UTC(),
		LastProbeAt:   time.Now().UTC(),
		CompletedAt:   time.Now().UTC(),
	}
	if runErr != nil {
		slot.LastError = strings.TrimSpace(runErr.Error())
	}
	if err := rt.store.UpsertRepoSlot(slot); err != nil {
		return err
	}
	return rt.finalizeRepoSlot(ctx, execEngine, slot)
}

func (rt *Runtime) probeDispatchedTMuxSession(ctx context.Context, execEngine *executor.Executor, task *core.Task, slot *storage.RepoSlot) error {
	if task == nil || slot == nil || execEngine == nil {
		return nil
	}
	manager := execEngine.TMux()
	if manager == nil || strings.TrimSpace(slot.SessionName) == "" {
		return nil
	}
	status, err := manager.Status(slot.SessionName)
	if err != nil {
		if !errors.Is(err, tmux.ErrSessionNotFound) {
			return err
		}
		return rt.markDispatchedSlotPaneDead(ctx, execEngine, task, slot, "tmux 会话发射后立即丢失，已转入收口")
	}
	if !status.PaneDead {
		return nil
	}
	return rt.markDispatchedSlotPaneDead(ctx, execEngine, task, slot, fmt.Sprintf("tmux 会话发射后立即退出(exit=%d)，已转入收口", status.ExitCode))
}

func (rt *Runtime) markDispatchedSlotPaneDead(ctx context.Context, execEngine *executor.Executor, task *core.Task, slot *storage.RepoSlot, reason string) error {
	if slot == nil {
		return nil
	}
	now := time.Now().UTC()
	advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
	slot.CompletedAt = now
	slot.LastProbeAt = now
	slot.LastError = formatLaunchProbeFailure(execEngine, slot.TaskID, reason)
	if task != nil {
		_ = rt.store.AppendEvent(task.TaskID, core.EventWarning, slot.LastError)
	}
	if err := rt.store.UpsertRepoSlot(slot); err != nil {
		return err
	}
	return rt.finalizeRepoSlot(ctx, execEngine, slot)
}

func formatLaunchProbeFailure(execEngine *executor.Executor, taskID, reason string) string {
	reason = strings.TrimSpace(reason)
	if execEngine == nil || strings.TrimSpace(taskID) == "" {
		return reason
	}
	artifacts := execEngine.ArtifactPaths(taskID)
	diagnostic := readDiagnosticTail(artifacts.DiagnosticFile, 12)
	if diagnostic == "" {
		diagnostic = readDiagnosticTail(artifacts.LogFile, 12)
	}
	if diagnostic == "" || strings.Contains(reason, diagnostic) {
		return reason
	}
	return fmt.Sprintf("%s；诊断输出: %s", reason, diagnostic)
}

func (rt *Runtime) finalizeRepoSlot(ctx context.Context, execEngine *executor.Executor, slot *storage.RepoSlot) error {
	if slot == nil {
		return nil
	}
	task, err := rt.store.GetTask(slot.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return rt.store.DeleteRepoSlot(slot.TargetRepo)
	}
	result, runErr := execEngine.LoadResult(task.TaskID)
	result = enrichDiagnosticResult(execEngine, task.TaskID, result)
	streamSnapshot, streamErr := execEngine.LoadStreamEventSnapshot(task.TaskID)
	if streamErr != nil {
		rt.logWarning("stream", "读取 stream-json 对账快照失败", "task_id", task.TaskID, "target_repo", task.TargetRepo, "error", streamErr)
	}
	rt.recordStreamReconcile(task, streamSnapshot, runErr)
	if slot.Phase == storage.RepoSlotPhasePaneDead && runErr != nil && strings.TrimSpace(slot.LastError) != "" {
		runErr = fmt.Errorf("%s: %w", slot.LastError, runErr)
	}
	if result != nil && result.SessionID != "" {
		if _, err := rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
			if existing == nil {
				return nil, nil
			}
			existing.LastSessionID = result.SessionID
			return existing, nil
		}); err != nil {
			return err
		}
	}
	if result != nil {
		if err := rt.persistExecutionMetrics(task, result); err != nil {
			return err
		}
	}
	if runErr != nil {
		return rt.failTaskExecution(task, result, runErr)
	}
	return rt.completeTaskFinalizing(ctx, task, result, slot)
}

func (rt *Runtime) failTaskExecution(task *core.Task, result *executor.Result, runErr error) error {
	if task == nil {
		return nil
	}
	updated, err := rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
		if existing == nil {
			return nil, nil
		}
		if result != nil && result.SessionID != "" {
			existing.LastSessionID = result.SessionID
		}
		if result != nil && strings.TrimSpace(result.ResumeSessionID) != "" {
			existing.LastSessionID = ""
		}
		existing.ResultCommentID = 0
		existing.DoneCommentID = 0
		existing.RetryCount++
		existing.ErrorMsg = formatExecutionFailure(runErr, result)
		existing.State = core.StateFailed
		if existing.RetryCount >= core.MaxRetry {
			existing.State = core.StateDead
		}
		return existing, nil
	})
	if err != nil {
		return err
	}
	if updated == nil {
		return nil
	}
	eventType := core.EventFailed
	if updated.State == core.StateDead {
		eventType = core.EventDead
	}
	_ = rt.store.AppendEvent(updated.TaskID, eventType, updated.ErrorMsg)
	rt.reportFailure(updated)
	return rt.store.DeleteRepoSlot(updated.TargetRepo)
}

func (rt *Runtime) completeTaskFinalizing(ctx context.Context, task *core.Task, result *executor.Result, slot *storage.RepoSlot) error {
	if task == nil {
		return nil
	}
	updated, err := rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
		if existing == nil {
			return nil, nil
		}
		if result != nil && result.SessionID != "" {
			existing.LastSessionID = result.SessionID
		}
		existing.State = core.StateFinalizing
		existing.ErrorMsg = ""
		existing.RestartCount = 0
		if strings.TrimSpace(existing.ReportPath) == "" {
			existing.ReportPath = filepath.ToSlash(filepath.Join("docs", "reports", core.SafeReportFileName(time.Now(), existing.IssueNumber, existing.IssueTitle)))
		}
		return existing, nil
	})
	if err != nil {
		return err
	}
	if updated == nil {
		return rt.store.DeleteRepoSlot(task.TargetRepo)
	}
	if slot == nil {
		slot = &storage.RepoSlot{
			TargetRepo:    updated.TargetRepo,
			TaskID:        updated.TaskID,
			ExecutorMode:  string(rt.cfg.ExecutorModeForRepo(updated.TargetRepo)),
			Phase:         storage.RepoSlotPhaseFinalizing,
			CurrentStep:   "sync_target",
			LastAdvanceAt: time.Now().UTC(),
		}
	}
	if slot.SyncTarget == "" {
		slot.SyncTarget = storage.FinalizeStepPending
	}
	if slot.SyncHome == "" {
		slot.SyncHome = storage.FinalizeStepPending
	}
	if slot.ReportIssue == "" {
		slot.ReportIssue = storage.FinalizeStepPending
	}
	advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, finalizeCurrentStep(slot))
	slot.NextRetryAt = time.Time{}
	if err := rt.store.UpsertRepoSlot(slot); err != nil {
		return err
	}
	if err := rt.runFinalizeSteps(ctx, updated, result, slot); err != nil {
		return err
	}
	if slot.ReportIssue != storage.FinalizeStepOK || slot.SyncTarget != storage.FinalizeStepOK || slot.SyncHome != storage.FinalizeStepOK {
		return nil
	}
	doneCommentID := updated.DoneCommentID
	if result != nil && rt.rep != nil && doneCommentID == 0 {
		advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, "mark_done")
		slot.LastAttemptAt = time.Now().UTC()
		if err := rt.store.UpsertRepoSlot(slot); err != nil {
			return err
		}
		doneTask := *updated
		doneTask.State = core.StateDone
		if err := rt.reportFinalizeRecovery(updated, slot); err != nil {
			rt.logError("finalize", "回帖收尾恢复说明失败", "task_id", updated.TaskID, "error", err)
		}
		comment := rt.promoteResultCommentToDone(&doneTask, result.Duration, result.LogFile, updated.ResultCommentID)
		if comment == nil || comment.ID == 0 {
			return rt.handleFinalizeFailure(updated, slot, "mark_done", errors.New("Issue 完成回帖失败"), []string{
				"请检查 GitHub Issue 评论权限与网络可达性",
				"确认 `gh auth status` 正常",
				"处理完成后重新执行一轮 ingest 补齐 done marker",
			})
		}
		doneCommentID = comment.ID
	}
	if _, err := rt.store.ApplyTask(updated.TaskID, func(existing *core.Task) (*core.Task, error) {
		if existing == nil {
			return nil, nil
		}
		existing.State = core.StateDone
		existing.ErrorMsg = ""
		existing.ResultCommentID = doneCommentID
		existing.DoneCommentID = doneCommentID
		return existing, nil
	}); err != nil {
		return err
	}
	_ = rt.store.AppendEvent(updated.TaskID, core.EventDone, "任务执行完成")
	return rt.store.DeleteRepoSlot(updated.TargetRepo)
}

func (rt *Runtime) reportFinalizeRecovery(task *core.Task, slot *storage.RepoSlot) error {
	if rt == nil || rt.rep == nil || rt.store == nil || task == nil || slot == nil {
		return nil
	}
	if !slot.RecoveryReportedAt.IsZero() {
		return nil
	}
	if strings.TrimSpace(slot.LastFailureStep) == "" || slot.LastFailureClass == "" {
		return nil
	}
	if err := rt.rep.ReportFinalizeRecovered(task, slot.LastFailureStep, slot.LastFailureClass); err != nil {
		return err
	}
	slot.RecoveryReportedAt = time.Now().UTC()
	if err := rt.store.UpsertRepoSlot(slot); err != nil {
		return err
	}
	_ = rt.store.AppendEvent(task.TaskID, core.EventUpdated, buildFinalizeRecoveryEventDetail(slot.LastFailureStep, slot.LastFailureClass))
	return nil
}

func (rt *Runtime) runFinalizeSteps(ctx context.Context, task *core.Task, result *executor.Result, slot *storage.RepoSlot) error {
	_ = ctx
	if task == nil || slot == nil {
		return nil
	}
	if slot.ReportIssue != storage.FinalizeStepOK {
		prepareFinalizeAttempt(slot, "report_issue")
		if err := rt.store.UpsertRepoSlot(slot); err != nil {
			return err
		}
		if result != nil && rt.rep != nil {
			comment := rt.reportResultReady(task, result.Duration, result.LogFile)
			if comment == nil || comment.ID == 0 {
				slot.ReportIssue = storage.FinalizeStepFailed
				return rt.handleFinalizeFailure(task, slot, "report_issue", errors.New("Issue 首轮结果回帖失败"), []string{
					"请检查 GitHub Issue 评论权限与网络可达性",
					"确认 `gh auth status` 正常",
					"处理完成后重新执行一轮 ingest 补齐首条可见回帖",
				})
			}
			task.ResultCommentID = comment.ID
			if _, err := rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
				if existing == nil {
					return nil, nil
				}
				existing.ResultCommentID = comment.ID
				return existing, nil
			}); err != nil {
				return err
			}
		}
		slot.ReportIssue = storage.FinalizeStepOK
		markFinalizeStepDone(slot, "report_issue")
		if err := rt.store.UpsertRepoSlot(slot); err != nil {
			return err
		}
		_ = rt.store.AppendEvent(task.TaskID, core.EventUpdated, "Issue 首条结果回帖已发布，进入交付收尾")
	}
	if slot.SyncTarget != storage.FinalizeStepOK {
		prepareFinalizeAttempt(slot, "sync_target")
		state, hints, err := rt.syncFinalizeTarget(task)
		slot.SyncTarget = state
		if err != nil {
			return rt.handleFinalizeFailure(task, slot, "target", err, hints)
		}
		markFinalizeStepDone(slot, "sync_target")
	}
	if slot.SyncHome != storage.FinalizeStepOK {
		prepareFinalizeAttempt(slot, "sync_home")
		state, hints, err := rt.syncFinalizeHome(task)
		slot.SyncHome = state
		if err != nil {
			return rt.handleFinalizeFailure(task, slot, "home", err, hints)
		}
		markFinalizeStepDone(slot, "sync_home")
	}
	return nil
}

func (rt *Runtime) handleFinalizeFailure(task *core.Task, slot *storage.RepoSlot, step string, err error, hints []string) error {
	now := time.Now().UTC()
	assessment := assessFinalizeFailure(step, err)
	policy := assessment.policy
	failureKey := buildFinalizeFailureKey(step, err)
	shouldEnsureVisibleReport := slot.LastReportedAt.IsZero() && strings.TrimSpace(slot.LastReportedFailure) == ""
	advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizeFailed, finalizeStepName(step))
	slot.LastError = strings.TrimSpace(err.Error())
	slot.FailureClass = assessment.class
	slot.FailureMode = assessment.policy.mode
	slot.Hints = append([]string(nil), hints...)
	slot.LastAttemptAt = now
	stepName := finalizeStepName(step)
	slot.LastFailureStep = stepName
	slot.LastFailureClass = assessment.class
	if slot.FinalizeRetryStep != stepName {
		slot.FinalizeRetryStep = stepName
		slot.FinalizeRetryCount = 0
	}
	slot.FinalizeRetryCount++
	if policy.mode == storage.FinalizeFailureModeRetry && slot.FinalizeRetryCount > maxFinalizeRetry {
		slot.Hints = append(slot.Hints, "临时重试次数已耗尽，后续请按建议手工处理")
		policy.mode = storage.FinalizeFailureModePause
		policy.delay = finalizeRetryManualDelay
		slot.FailureMode = policy.mode
	}
	slot.NextRetryAt = now.Add(policy.nextDelay(slot.FinalizeRetryCount))
	switch policy.mode {
	case storage.FinalizeFailureModeRetry:
		slot.Hints = append(slot.Hints,
			fmt.Sprintf("检测为临时抖动，系统将在 `%s` 后自动重试；当前已重试 `%d/%d`", policy.nextDelay(slot.FinalizeRetryCount).Round(time.Minute), slot.FinalizeRetryCount, maxFinalizeRetry),
			fmt.Sprintf("下次自动重试时间: `%s`", slot.NextRetryAt.Format(time.RFC3339)),
		)
	default:
		slot.Hints = append(slot.Hints,
			"检测为需人工介入场景；为避免高频噪音，本轮只回帖一次并进入慢速复查",
			fmt.Sprintf("下次慢速复查时间: `%s`", slot.NextRetryAt.Format(time.RFC3339)),
		)
	}
	if saveErr := rt.store.UpsertRepoSlot(slot); saveErr != nil {
		return saveErr
	}
	_, _ = rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
		if existing == nil {
			return nil, nil
		}
		existing.State = core.StateFinalizing
		existing.ErrorMsg = slot.LastError
		return existing, nil
	})
	_ = rt.store.AppendEvent(task.TaskID, core.EventWarning, buildFinalizeFailureEventDetail(step, slot.FailureClass, slot.FailureMode, slot.LastError))
	if rt.rep != nil && slot.LastError != "" && shouldReportFinalizeFailure(slot, failureKey, shouldEnsureVisibleReport) {
		_ = rt.rep.ReportFinalizing(task, step, slot.FailureClass, slot.FailureMode, slot.LastError, slot.Hints)
		slot.LastReportedAt = now
		slot.LastReportedFailure = failureKey
		_ = rt.store.UpsertRepoSlot(slot)
	}
	return nil
}

func (rt *Runtime) syncFinalizeTarget(task *core.Task) (storage.FinalizeStepState, []string, error) {
	if task == nil || strings.TrimSpace(task.TargetRepo) == "" {
		return storage.FinalizeStepOK, nil, nil
	}
	target, err := rt.cfg.EnabledTargetByRepo(task.TargetRepo)
	if err != nil {
		return storage.FinalizeStepFailed, []string{fmt.Sprintf("请检查目标仓配置: %v", err)}, err
	}
	err = rt.repoSync(target.LocalPath, fmt.Sprintf("task done: %s#%d %s", task.IssueRepo, task.IssueNumber, task.IssueTitle), nil, 3)
	if err == nil {
		return storage.FinalizeStepOK, nil, nil
	}
	class := classifyFinalizeFailureClass("target", err)
	return finalizeFailureState(err), buildFinalizeHints(task.TargetRepo, target.LocalPath, err, class), err
}

func (rt *Runtime) syncFinalizeHome(task *core.Task) (storage.FinalizeStepState, []string, error) {
	homeRepo := strings.TrimSpace(rt.cfg.Paths.HomeRepo)
	if homeRepo == "" {
		return storage.FinalizeStepOK, nil, nil
	}
	target, err := rt.cfg.EnabledTargetByRepo(task.TargetRepo)
	if err == nil && target != nil && filepath.Clean(target.LocalPath) == filepath.Clean(homeRepo) {
		return storage.FinalizeStepOK, nil, nil
	}
	err = rt.repoSync(homeRepo, fmt.Sprintf("task done(home): %s#%d %s", task.IssueRepo, task.IssueNumber, task.IssueTitle), nil, 3)
	if err == nil {
		return storage.FinalizeStepOK, nil, nil
	}
	class := classifyFinalizeFailureClass("home", err)
	return finalizeFailureState(err), buildFinalizeHints("知识仓库", homeRepo, err, class), err
}

func finalizeFailureState(err error) storage.FinalizeStepState {
	if err == nil {
		return storage.FinalizeStepOK
	}
	if errors.Is(err, vcs.ErrConflict) {
		return storage.FinalizeStepConflict
	}
	return storage.FinalizeStepFailed
}

func buildFinalizeHints(repo, localPath string, err error, class storage.FinalizeFailureClass) []string {
	hints := []string{fmt.Sprintf("本机仓库路径: `%s`", localPath)}
	switch class {
	case storage.FinalizeFailureClassNetwork:
		hints = append(hints,
			"检测为网络抖动；可先等待系统自动重试，也可手工执行 `jj git fetch --remote origin` 验证链路是否恢复",
			"若多轮仍失败，请检查本机到 GitHub 的网络、代理与 DNS 配置，并关注 GitHub Status",
		)
	case storage.FinalizeFailureClassVersionMismatch:
		hints = append(hints,
			"请先执行 `jj --version` 与 `git --version` 核对本机版本",
			"再执行 `git fetch -h | rg porcelain` 确认本机是否具备 `git fetch --porcelain` 能力",
			"若当前 git 低于 `2.41.0` 或缺少 `--porcelain`，请优先升级 git，或切换匹配的 jj 版本后再重试",
			"该类错误不会按网络抖动自动重试，请先处理环境兼容性后再恢复任务",
			"可先执行 `ccclaw doctor`，确认 `jj/git 同步能力` 检查项是否已恢复为 `[ OK ]`",
		)
	case storage.FinalizeFailureClassConflict:
		hints = append(hints,
			"建议先执行 `jj st` 确认当前工作区状态",
			"建议再执行 `jj log -r 'conflicts()|@|@-'` 查看冲突与最近变更",
			"请在 GitHub 仓库的 `Code -> Commits` 查看远端最近提交",
			"若涉及同一区域并发修改，请在 GitHub 仓库的 `Pull requests` 查看是否已有相关改动待合并",
			"本地收敛后可执行 `jj resolve`，再执行 `jj git push --remote origin --bookmark main`",
		)
	case storage.FinalizeFailureClassProtection:
		hints = append(hints,
			"请在 GitHub 仓库的 `Settings -> Branches` 检查默认分支保护规则",
			"若默认分支禁止直推，请改为人工经 PR 合并，或为自动账号配置允许的交付路径",
			"处理完成前不建议继续自动重试，以免持续触发远端拒绝",
		)
	case storage.FinalizeFailureClassAuth:
		hints = append(hints,
			"请执行 `gh auth status` 或检查当前 git 凭据，确认 token/SSH key 未过期且具备目标仓推送权限",
			"若仓库已迁移或权限模型变更，请同步更新 remote 地址与执行账号授权范围",
			"处理完成后再执行一轮 ingest，让系统补齐同步与 DONE 回写",
		)
	case storage.FinalizeFailureClassConfig:
		hints = append(hints,
			"请检查本机 `jj`、`git` 与目标仓路径配置是否完整可用",
			"必要时先执行 `ccclaw doctor` 确认基础依赖通过，再恢复任务",
		)
	default:
		hints = append(hints,
			"建议先手工执行 `jj git fetch --remote origin` 与 `jj git push --remote origin --bookmark main` 复现原始报错",
			"若仍无法归类，请把完整 stderr 写回工程报告与 Issue，避免只保留摘要错误",
		)
	}
	if repo != "" && repo != "知识仓库" {
		hints = append(hints, fmt.Sprintf("请优先检查 GitHub 仓库 `%s` 的 `Code` / `Pull requests` / `Actions` 页面", repo))
	}
	return hints
}

func (rt *Runtime) patrolRepoSlots(ctx context.Context, execEngine *executor.Executor, manager tmux.Manager, out io.Writer) error {
	if err := rt.hydrateRepoSlots(); err != nil {
		return err
	}
	slots, err := rt.store.ListRepoSlots()
	if err != nil {
		return err
	}
	timeout := execEngine.Timeout()
	var (
		running    int
		restarting int
		paneDead   int
	)
	for _, slot := range slots {
		if slot == nil || strings.TrimSpace(slot.TargetRepo) == "" {
			continue
		}
		if mode := config.ExecutorMode(strings.TrimSpace(slot.ExecutorMode)); mode == config.ExecutorModeDaemon || (mode == "" && rt.cfg.ExecutorModeForRepo(slot.TargetRepo) == config.ExecutorModeDaemon) {
			continue
		}
		if slot.Phase != storage.RepoSlotPhaseRunning && slot.Phase != storage.RepoSlotPhaseRestarting {
			if slot.Phase == storage.RepoSlotPhasePaneDead {
				paneDead++
			}
			continue
		}
		running++
		task, err := rt.store.GetTask(slot.TaskID)
		if err != nil {
			return err
		}
		if task == nil {
			if err := rt.store.DeleteRepoSlot(slot.TargetRepo); err != nil {
				return err
			}
			continue
		}
		if err := rt.syncTaskSessionFromHook(task); err != nil {
			rt.logWarning("patrol", "同步 Claude hook session 失败", "issue", rt.issueRef(task.IssueRepo, task.IssueNumber), "error", err)
		}
		status, err := manager.Status(slot.SessionName)
		if err != nil {
			if errors.Is(err, tmux.ErrSessionNotFound) {
				advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
				slot.CompletedAt = time.Now().UTC()
				slot.LastProbeAt = time.Now().UTC()
				slot.LastError = "tmux 会话已退出，等待 ingest 收口"
				if err := rt.store.UpsertRepoSlot(slot); err != nil {
					return err
				}
				paneDead++
				continue
			}
			return err
		}
		if status.PaneDead {
			advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
			slot.CompletedAt = time.Now().UTC()
			slot.LastProbeAt = time.Now().UTC()
			if err := rt.store.UpsertRepoSlot(slot); err != nil {
				return err
			}
			paneDead++
			continue
		}
		runningFor := time.Since(status.CreatedAt)
		if shouldRestart, reason, err := rt.shouldRestartRunningSlot(task, status.Name, runningFor); err != nil {
			return err
		} else if shouldRestart {
			if err := rt.restartRepoSlotTask(ctx, execEngine, manager, task, slot, reason); err != nil {
				return err
			}
			restarting++
			continue
		}
		if timeout > 0 && runningFor >= timeout {
			reason := fmt.Sprintf("tmux 会话 %s 已运行 %s，超过超时阈值 %s", status.Name, runningFor.Round(time.Second), timeout)
			if err := rt.restartRepoSlotTask(ctx, execEngine, manager, task, slot, reason); err != nil {
				return err
			}
			restarting++
			continue
		}
		slot.LastProbeAt = time.Now().UTC()
		if err := rt.store.UpsertRepoSlot(slot); err != nil {
			return err
		}
	}
	if out != nil {
		_, _ = fmt.Fprintf(out, "巡查完成: running=%d restarting=%d pane_dead=%d\n", running, restarting, paneDead)
	}
	return nil
}

func (rt *Runtime) patrolDaemonRepoSlots(ctx context.Context, execEngine *executor.Executor, out io.Writer) error {
	if err := rt.hydrateRepoSlots(); err != nil {
		return err
	}
	slots, err := rt.store.ListRepoSlots()
	if err != nil {
		return err
	}
	checked := 0
	compensated := 0
	for _, slot := range slots {
		if slot == nil || strings.TrimSpace(slot.TargetRepo) == "" {
			continue
		}
		mode := config.ExecutorMode(strings.TrimSpace(slot.ExecutorMode))
		if mode == "" {
			mode = rt.cfg.ExecutorModeForRepo(slot.TargetRepo)
		}
		if mode != config.ExecutorModeDaemon {
			continue
		}
		checked++
		if slot.Phase == storage.RepoSlotPhaseFinalizeFailed && !slot.NextRetryAt.IsZero() && time.Now().UTC().After(slot.NextRetryAt) {
			if err := rt.advanceRepoSlot(ctx, execEngine, slot, mode); err != nil {
				return err
			}
			compensated++
		}
	}
	if out != nil {
		_, _ = fmt.Fprintf(out, "daemon 健康检查: checked=%d compensated=%d\n", checked, compensated)
	}
	return nil
}

func (rt *Runtime) shouldRestartRunningSlot(task *core.Task, sessionName string, runningFor time.Duration) (bool, string, error) {
	if task == nil {
		return false, "", nil
	}
	state, err := claude.LoadHookState(rt.claudeHookStateDir(), string(task.TaskID))
	if err != nil || state == nil {
		return false, "", err
	}
	if strings.TrimSpace(state.SessionID) != "" && state.SessionID != task.LastSessionID {
		if _, err := rt.store.ApplyTask(task.TaskID, func(existing *core.Task) (*core.Task, error) {
			if existing == nil {
				return nil, nil
			}
			existing.LastSessionID = strings.TrimSpace(state.SessionID)
			return existing, nil
		}); err != nil {
			return false, "", err
		}
	}
	if !state.PreCompactAt.IsZero() {
		return true, fmt.Sprintf("Claude 会话 %s 命中 PreCompact:%s", sessionName, displayPreCompactMatcher(state.PreCompactMatcher)), nil
	}
	if strings.TrimSpace(state.TranscriptPath) == "" {
		return false, "", nil
	}
	metrics, err := claude.ContextMetricsFromTranscript(state.TranscriptPath, state.Model, state.ContextWindowSize)
	if err != nil || metrics == nil {
		return false, "", err
	}
	if metrics.UsablePercent <= contextRestartThreshold {
		return false, "", nil
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
	return true, reason, nil
}

func (rt *Runtime) restartRepoSlotTask(ctx context.Context, execEngine *executor.Executor, manager tmux.Manager, task *core.Task, slot *storage.RepoSlot, reason string) error {
	if task == nil || slot == nil {
		return nil
	}
	target, err := rt.cfg.EnabledTargetByRepo(task.TargetRepo)
	if err != nil {
		return err
	}
	if err := manager.Kill(slot.SessionName); err != nil {
		return err
	}
	if err := claude.ClearHookState(rt.claudeHookStateDir(), string(task.TaskID)); err != nil {
		return err
	}
	runOpts := rt.buildExecutionOptions(task, target)
	advanceRepoSlotPhase(slot, storage.RepoSlotPhaseRestarting, "restart_claude")
	slot.RestartCount++
	slot.LastError = strings.TrimSpace(reason)
	slot.LastProbeAt = time.Now().UTC()
	if err := rt.store.UpsertRepoSlot(slot); err != nil {
		return err
	}
	_ = rt.store.AppendEvent(task.TaskID, core.EventWarning, "patrol 原地重启 Claude: "+strings.TrimSpace(reason))
	result, runErr := execEngine.Run(ctx, target.LocalPath, task.TaskID, runOpts)
	if runErr != nil {
		advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
		slot.LastError = strings.TrimSpace(runErr.Error())
		return rt.store.UpsertRepoSlot(slot)
	}
	if result != nil && result.Pending {
		advanceRepoSlotPhase(slot, storage.RepoSlotPhaseRunning, "execute")
		slot.SessionName = result.SessionName
		slot.LastProbeAt = time.Now().UTC()
		return rt.store.UpsertRepoSlot(slot)
	}
	advanceRepoSlotPhase(slot, storage.RepoSlotPhasePaneDead, "load_result")
	slot.CompletedAt = time.Now().UTC()
	if result != nil {
		slot.SessionName = result.SessionName
	}
	return rt.store.UpsertRepoSlot(slot)
}

func (rt *Runtime) observeStreamEventContract(execEngine *executor.Executor, slot *storage.RepoSlot) {
	if rt == nil || execEngine == nil || slot == nil || strings.TrimSpace(slot.TaskID) == "" {
		return
	}
	snapshot, err := execEngine.LoadStreamEventSnapshot(slot.TaskID)
	if err != nil {
		rt.logWarning("stream", "读取 stream-json 事件快照失败", "task_id", slot.TaskID, "target_repo", slot.TargetRepo, "error", err)
		return
	}
	if snapshot == nil || snapshot.EventCount == 0 {
		return
	}
	slotPhase, slotStep, taskState, taskEvent, detail := mapStreamSnapshot(snapshot)
	rt.logDebug(
		"stream",
		"stream-json 映射快照",
		"task_id", slot.TaskID,
		"target_repo", slot.TargetRepo,
		"events", snapshot.EventCount,
		"last_event", snapshot.LastEvent,
		"slot_phase", slotPhase,
		"slot_step", slotStep,
		"task_state", taskState,
		"task_event", taskEvent,
		"detail", detail,
	)
}

func mapStreamSnapshot(snapshot *executor.StreamEventSnapshot) (storage.RepoSlotPhase, string, core.State, core.EventType, string) {
	if snapshot == nil {
		return "", "", "", "", ""
	}
	slotPhase := storage.RepoSlotPhaseRunning
	switch storage.RepoSlotPhase(snapshot.Mapping.RepoSlotPhase) {
	case storage.RepoSlotPhaseRunning, storage.RepoSlotPhasePaneDead, storage.RepoSlotPhaseRestarting, storage.RepoSlotPhaseFinalizing:
		slotPhase = storage.RepoSlotPhase(snapshot.Mapping.RepoSlotPhase)
	}
	slotStep := strings.TrimSpace(snapshot.Mapping.RepoSlotStep)
	if slotStep == "" {
		slotStep = strings.TrimSpace(snapshot.CurrentStep)
	}
	taskState := snapshot.Mapping.TaskState
	switch taskState {
	case core.StateRunning, core.StateFinalizing, core.StateFailed, core.StateDone, core.StateDead:
	default:
		taskState = core.StateRunning
	}
	taskEvent := snapshot.Mapping.TaskEvent
	switch taskEvent {
	case core.EventStarted, core.EventUpdated, core.EventFailed, core.EventDone, core.EventDead, core.EventWarning:
	default:
		taskEvent = core.EventUpdated
	}
	detail := strings.TrimSpace(snapshot.Mapping.TaskEventDetail)
	if detail == "" {
		detail = strings.TrimSpace(snapshot.Error)
	}
	if detail == "" {
		detail = strings.TrimSpace(snapshot.Result)
	}
	return slotPhase, slotStep, taskState, taskEvent, detail
}

func (rt *Runtime) recordStreamReconcile(task *core.Task, snapshot *executor.StreamEventSnapshot, runErr error) {
	if rt == nil || task == nil || snapshot == nil || snapshot.EventCount == 0 {
		return
	}
	expected := streamExpectedOutcome(snapshot)
	actual := streamActualOutcome(runErr)
	match := expected == actual
	rt.logInfo(
		"stream",
		"stream-json 影子对账",
		"issue", rt.issueRef(task.IssueRepo, task.IssueNumber),
		"task_id", task.TaskID,
		"target_repo", task.TargetRepo,
		"expected", expected,
		"actual", actual,
		"match", match,
		"events", snapshot.EventCount,
		"last_event", snapshot.LastEvent,
	)
	if !match {
		_ = rt.store.AppendEvent(task.TaskID, core.EventWarning, fmt.Sprintf("stream-json 影子对账偏差: expected=%s actual=%s", expected, actual))
	}
}

func streamExpectedOutcome(snapshot *executor.StreamEventSnapshot) string {
	if snapshot == nil {
		return "unknown"
	}
	switch snapshot.Mapping.TaskState {
	case core.StateFailed, core.StateDead:
		return "failed"
	}
	if snapshot.LastEvent == executor.StreamEventError {
		return "failed"
	}
	return "done"
}

func streamActualOutcome(runErr error) string {
	if runErr != nil {
		return "failed"
	}
	return "done"
}

func defaultSlotStepForTask(state core.State) string {
	if state == core.StateFinalizing {
		return "sync_target"
	}
	return "execute"
}

func advanceRepoSlotPhase(slot *storage.RepoSlot, phase storage.RepoSlotPhase, step string) {
	if slot == nil {
		return
	}
	slot.Phase = phase
	slot.CurrentStep = strings.TrimSpace(step)
	slot.LastAdvanceAt = time.Now().UTC()
}

func prepareFinalizeAttempt(slot *storage.RepoSlot, step string) {
	if slot == nil {
		return
	}
	advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, step)
	slot.LastAttemptAt = time.Now().UTC()
	slot.NextRetryAt = time.Time{}
}

func markFinalizeStepDone(slot *storage.RepoSlot, completedStep string) {
	if slot == nil {
		return
	}
	slot.LastError = ""
	slot.FailureClass = ""
	slot.Hints = nil
	slot.NextRetryAt = time.Time{}
	slot.LastReportedAt = time.Time{}
	slot.LastReportedFailure = ""
	if strings.TrimSpace(slot.FinalizeRetryStep) == strings.TrimSpace(completedStep) {
		slot.FinalizeRetryStep = ""
		slot.FinalizeRetryCount = 0
	}
	switch strings.TrimSpace(completedStep) {
	case "report_issue":
		advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, "sync_target")
	case "sync_target":
		advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, "sync_home")
	case "sync_home":
		advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, "mark_done")
	case "mark_done":
		advanceRepoSlotPhase(slot, storage.RepoSlotPhaseFinalizing, "done")
	}
}

func finalizeCurrentStep(slot *storage.RepoSlot) string {
	if slot == nil {
		return "sync_target"
	}
	if step := strings.TrimSpace(slot.CurrentStep); step != "" {
		return step
	}
	if slot.ReportIssue != storage.FinalizeStepOK {
		return "report_issue"
	}
	if slot.SyncTarget != storage.FinalizeStepOK {
		return "sync_target"
	}
	if slot.SyncHome != storage.FinalizeStepOK {
		return "sync_home"
	}
	return "mark_done"
}

func finalizeStepName(step string) string {
	switch strings.TrimSpace(step) {
	case "target":
		return "sync_target"
	case "home":
		return "sync_home"
	case "report_issue":
		return "report_issue"
	case "mark_done":
		return "mark_done"
	default:
		return strings.TrimSpace(step)
	}
}

type finalizeFailurePolicy struct {
	mode  storage.FinalizeFailureMode
	delay time.Duration
}

type finalizeFailureAssessment struct {
	class  storage.FinalizeFailureClass
	policy finalizeFailurePolicy
}

func (p finalizeFailurePolicy) nextDelay(retryCount int) time.Duration {
	if p.mode == storage.FinalizeFailureModePause {
		return p.delay
	}
	delay := p.delay
	for attempt := 1; attempt < retryCount; attempt++ {
		delay *= 2
		if delay >= finalizeRetryMaxDelay {
			return finalizeRetryMaxDelay
		}
	}
	if delay <= 0 {
		return finalizeRetryBaseDelay
	}
	if delay > finalizeRetryMaxDelay {
		return finalizeRetryMaxDelay
	}
	return delay
}

func assessFinalizeFailure(step string, err error) finalizeFailureAssessment {
	policy := finalizeFailurePolicy{mode: storage.FinalizeFailureModePause, delay: finalizeRetryManualDelay}
	if isTransientFinalizeError(err) {
		policy = finalizeFailurePolicy{mode: storage.FinalizeFailureModeRetry, delay: finalizeRetryBaseDelay}
	}
	return finalizeFailureAssessment{
		class:  classifyFinalizeFailureClass(step, err),
		policy: policy,
	}
}

func classifyFinalizeFailureClass(step string, err error) storage.FinalizeFailureClass {
	if strings.TrimSpace(step) == "report_issue" || strings.TrimSpace(step) == "mark_done" {
		return storage.FinalizeFailureClassIssueReporting
	}
	if err == nil {
		return storage.FinalizeFailureClassUnknown
	}
	if errors.Is(err, vcs.ErrConflict) {
		return storage.FinalizeFailureClassConflict
	}
	if errors.Is(err, vcs.ErrGitTooOld) || errors.Is(err, vcs.ErrUnsupportedGit) || errors.Is(err, vcs.ErrCapabilityMismatch) {
		return storage.FinalizeFailureClassVersionMismatch
	}
	if errors.Is(err, vcs.ErrSyncNetwork) {
		return storage.FinalizeFailureClassNetwork
	}
	if errors.Is(err, vcs.ErrSyncAuth) {
		return storage.FinalizeFailureClassAuth
	}
	if errors.Is(err, vcs.ErrSyncProtection) {
		return storage.FinalizeFailureClassProtection
	}
	if errors.Is(err, vcs.ErrSyncUnknown) {
		return storage.FinalizeFailureClassUnknown
	}
	if errors.Is(err, vcs.ErrJJNotAvailable) {
		return storage.FinalizeFailureClassConfig
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"supported version is",
		"git 版本过低",
		"git version",
		"jj/git",
		"porcelain",
	} {
		if strings.Contains(text, marker) {
			return storage.FinalizeFailureClassVersionMismatch
		}
	}
	for _, marker := range []string{
		"protected branch",
		"branch protection",
	} {
		if strings.Contains(text, marker) {
			return storage.FinalizeFailureClassProtection
		}
	}
	for _, marker := range []string{
		"permission denied",
		"authentication",
		"not authorized",
		"repository not found",
		"403",
		"401",
	} {
		if strings.Contains(text, marker) {
			return storage.FinalizeFailureClassAuth
		}
	}
	for _, marker := range []string{
		"仓库路径不能为空",
		"请检查目标仓配置",
		"读取 git 版本失败",
		"读取 jj 版本失败",
		"读取 .jj 状态失败",
		"创建仓库目录失败",
	} {
		if strings.Contains(text, marker) {
			return storage.FinalizeFailureClassConfig
		}
	}
	for _, marker := range []string{
		"timeout",
		"timed out",
		"temporary",
		"temporarily",
		"connection reset",
		"connection refused",
		"broken pipe",
		"no route to host",
		"network is unreachable",
		"i/o timeout",
		"tls handshake timeout",
		"context deadline exceeded",
		"unexpected eof",
		"eof",
		"internal server error",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"http 502",
		"http 503",
		"http 504",
		"rate limit",
		"dial tcp",
	} {
		if strings.Contains(text, marker) {
			return storage.FinalizeFailureClassNetwork
		}
	}
	return storage.FinalizeFailureClassUnknown
}

func isTransientFinalizeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, vcs.ErrSyncNetwork) {
		return true
	}
	if errors.Is(err, vcs.ErrConflict) || errors.Is(err, vcs.ErrJJNotAvailable) || errors.Is(err, vcs.ErrGitTooOld) || errors.Is(err, vcs.ErrUnsupportedGit) || errors.Is(err, vcs.ErrCapabilityMismatch) {
		return false
	}
	if errors.Is(err, vcs.ErrSyncAuth) || errors.Is(err, vcs.ErrSyncProtection) || errors.Is(err, vcs.ErrSyncUnknown) {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"timeout",
		"timed out",
		"temporary",
		"temporarily",
		"connection reset",
		"connection refused",
		"broken pipe",
		"no route to host",
		"network is unreachable",
		"i/o timeout",
		"tls handshake timeout",
		"context deadline exceeded",
		"unexpected eof",
		"eof",
		"internal server error",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"http 502",
		"http 503",
		"http 504",
		"rate limit",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, marker := range []string{
		"protected branch",
		"branch protection",
		"permission denied",
		"authentication",
		"not authorized",
		"repository not found",
		"仓库路径不能为空",
		"请检查目标仓配置",
	} {
		if strings.Contains(text, marker) {
			return false
		}
	}
	return errors.Is(err, vcs.ErrPushFailed)
}

func buildFinalizeFailureKey(step string, err error) string {
	if err == nil {
		return strings.TrimSpace(step)
	}
	return strings.TrimSpace(step) + "|" + strings.TrimSpace(err.Error())
}

func buildFinalizeFailureEventDetail(step string, class storage.FinalizeFailureClass, mode storage.FinalizeFailureMode, errMsg string) string {
	stepName := finalizeStepName(step)
	classText := formatFinalizeFailureClass(class)
	modeText := formatFinalizeFailureMode(mode)
	if strings.TrimSpace(errMsg) == "" {
		return fmt.Sprintf("执行结果已产出，收尾步骤 `%s` 失败；失败类型 %s；处理策略 %s", stepName, classText, modeText)
	}
	return fmt.Sprintf("执行结果已产出，收尾步骤 `%s` 失败；失败类型 %s；处理策略 %s: %s", stepName, classText, modeText, strings.TrimSpace(errMsg))
}

func buildFinalizeRecoveryEventDetail(step string, class storage.FinalizeFailureClass) string {
	return fmt.Sprintf("收尾恢复完成：此前 `%s` 的失败类型 %s 已恢复，任务继续完成交付", finalizeStepName(step), formatFinalizeFailureClass(class))
}

func shouldReportFinalizeFailure(slot *storage.RepoSlot, failureKey string, ensureVisibleReport bool) bool {
	if slot == nil {
		return false
	}
	if ensureVisibleReport {
		return true
	}
	if slot.LastReportedFailure == failureKey {
		return false
	}
	return true
}

func formatFinalizeFailureClass(class storage.FinalizeFailureClass) string {
	if class == "" {
		class = storage.FinalizeFailureClassUnknown
	}
	return fmt.Sprintf("`%s`（%s）", class, class.Display())
}

func formatFinalizeFailureMode(mode storage.FinalizeFailureMode) string {
	if mode == "" {
		mode = storage.FinalizeFailureModePause
	}
	return fmt.Sprintf("`%s`（%s）", mode, mode.Display())
}
