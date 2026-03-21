package reporter

import (
	"fmt"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/adapters/github"
	"github.com/41490/ccclaw/internal/adapters/storage"
	"github.com/41490/ccclaw/internal/core"
)

type Reporter struct {
	clientForRepo func(string) *github.Client
}

func New(clientForRepo func(string) *github.Client) *Reporter {
	return &Reporter{clientForRepo: clientForRepo}
}

func (r *Reporter) client(task *core.Task) *github.Client {
	if r == nil || r.clientForRepo == nil {
		return nil
	}
	repo := strings.TrimSpace(task.IssueRepo)
	if repo == "" {
		repo = strings.TrimSpace(task.ControlRepo)
	}
	return r.clientForRepo(repo)
}

func (r *Reporter) ReportBlocked(task *core.Task, reason string) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	body := fmt.Sprintf("任务已进入阻塞状态。\n\n- Issue: %s#%d\n- 原因: %s\n- 当前状态: `%s`", task.IssueRepo, task.IssueNumber, reason, task.State)
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func (r *Reporter) ReportFailure(task *core.Task) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	stage := classifyFailureStage(task)
	errMsg, diagnostic := splitFailureMessage(task.ErrorMsg)
	body := fmt.Sprintf("任务执行失败。\n\n- Issue: %s#%d\n- 状态: `%s`\n- 阶段: `%s`\n- 重试次数: `%d/%d`\n- 错误: `%s`", task.IssueRepo, task.IssueNumber, task.State, stage, task.RetryCount, core.MaxRetry, errMsg)
	if diagnostic != "" {
		body += fmt.Sprintf("\n- 诊断: `%s`", diagnostic)
	}
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func (r *Reporter) ReportSuccess(task *core.Task, duration time.Duration, logFile string) (*github.Comment, error) {
	client := r.client(task)
	if client == nil {
		return nil, nil
	}
	body := successBody(task, duration, logFile)
	return client.AddComment(task.IssueNumber, body)
}

func (r *Reporter) ReportResultReady(task *core.Task, duration time.Duration, logFile string) (*github.Comment, error) {
	client := r.client(task)
	if client == nil {
		return nil, nil
	}
	body := fmt.Sprintf("任务执行结果已形成，正在执行交付收尾。\n\n- Issue: %s#%d\n- 状态: `%s`\n- 执行结果: `已产出`\n- 耗时: `%s`\n- 工程报告: `%s`\n- 日志: `%s`", task.IssueRepo, task.IssueNumber, core.StateFinalizing, duration.Round(time.Second), task.ReportPath, logFile)
	return client.AddComment(task.IssueNumber, body)
}

func (r *Reporter) PromoteResultToDone(task *core.Task, duration time.Duration, logFile string, commentID int64) (*github.Comment, error) {
	client := r.client(task)
	if client == nil {
		return nil, nil
	}
	body := successBody(task, duration, logFile)
	if commentID == 0 {
		return client.AddComment(task.IssueNumber, body)
	}
	return client.UpdateComment(commentID, body)
}

func (r *Reporter) ReportStarted(task *core.Task, sessionName string) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	body := fmt.Sprintf("任务已开始执行。\n\n- Issue: %s#%d\n- 状态: `%s`\n- tmux 会话: `%s`", task.IssueRepo, task.IssueNumber, task.State, sessionName)
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func (r *Reporter) ReportRestarted(task *core.Task, reason string, restartCount, maxRestart int) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	body := fmt.Sprintf("任务已触发 fresh restart。\n\n- Issue: %s#%d\n- 状态: `%s`\n- 次数: `%d/%d`\n- 原因: `%s`", task.IssueRepo, task.IssueNumber, task.State, restartCount, maxRestart, strings.TrimSpace(reason))
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func (r *Reporter) ReportSkipped(task *core.Task, reason string) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	body := fmt.Sprintf("Issue 为汇报型，无可执行任务，已自动跳过 Claude 执行。\n\n- Issue: %s#%d\n- 原因: %s\n- 当前状态: `%s`\n\n%s", task.IssueRepo, task.IssueNumber, reason, task.State, github.DoneMarker)
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func (r *Reporter) ReportFinalizing(task *core.Task, step string, class storage.FinalizeFailureClass, mode storage.FinalizeFailureMode, errMsg string, hints []string) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	finalizeState := "失败"
	if mode == storage.FinalizeFailureModeRetry {
		finalizeState = "待复查"
	}
	body := fmt.Sprintf("任务执行已完成，但交付收尾未闭环。\n\n- Issue: %s#%d\n- 状态: `%s`\n- 业务执行: `成功`\n- 收尾同步: `%s`\n- 生命周期: `未写回 DONE`\n- 当前失败步骤: %s\n- 失败类型: %s\n- 处理策略: %s\n- 错误: `%s`", task.IssueRepo, task.IssueNumber, core.StateFinalizing, finalizeState, formatFinalizeStep(step), formatFinalizeFailureClass(class), formatFinalizeFailureMode(mode), strings.TrimSpace(errMsg))
	if len(hints) > 0 {
		body += "\n\n下一步建议:\n"
		for _, hint := range hints {
			hint = strings.TrimSpace(hint)
			if hint == "" {
				continue
			}
			body += "- " + hint + "\n"
		}
	}
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func (r *Reporter) ReportFinalizeRecovered(task *core.Task, step string, class storage.FinalizeFailureClass) error {
	client := r.client(task)
	if client == nil {
		return nil
	}
	body := fmt.Sprintf("此前 %s 的 %s 收尾失败已恢复，当前已继续完成剩余交付。", formatFinalizeStep(step), formatFinalizeFailureClass(class))
	_, err := client.AddComment(task.IssueNumber, body)
	return err
}

func formatFinalizeStep(step string) string {
	step = strings.TrimSpace(step)
	switch step {
	case "target", "sync_target":
		return "`sync_target`（目标仓同步）"
	case "home", "sync_home":
		return "`sync_home`（知识仓同步）"
	case "report_issue":
		return "`report_issue`（Issue 回帖）"
	case "mark_done":
		return "`mark_done`（补齐 done marker）"
	default:
		return "`" + step + "`"
	}
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

func splitFailureMessage(raw string) (string, string) {
	message := strings.TrimSpace(raw)
	if message == "" {
		return "未知错误", ""
	}
	parts := strings.SplitN(message, "；诊断输出:", 2)
	errMsg := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return errMsg, ""
	}
	return errMsg, strings.TrimSpace(parts[1])
}

func classifyFailureStage(task *core.Task) string {
	if task == nil {
		return "执行阶段"
	}
	message, _ := splitFailureMessage(task.ErrorMsg)
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"启动 tmux 会话失败",
		"找不到执行器命令",
		"未找到 claude 可执行文件",
		"not found",
		"退出码=127",
		"exit status 127",
	} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return "启动阶段"
		}
	}
	for _, marker := range []string{
		"超过超时阈值",
		"context deadline exceeded",
		"claude 返回错误结果",
		"error_max_turns",
		"已运行",
	} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return "执行阶段"
		}
	}
	for _, marker := range []string{
		"解析 claude json 输出失败",
		"结果校验失败",
		"读取结果文件失败",
		"读取结构化结果文件失败",
		"stdout 为空",
		"未找到结果文件",
		"结构化结果缺失",
		"结构化结果解析失败",
		"诊断文件",
		"解析执行元数据失败",
		"读取执行元数据失败",
		"结果文件",
		"会话已丢失",
	} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return "收口阶段"
		}
	}
	return "执行阶段"
}

func successBody(task *core.Task, duration time.Duration, logFile string) string {
	return fmt.Sprintf("任务执行完成。\n\n- Issue: %s#%d\n- 状态: `%s`\n- 耗时: `%s`\n- 工程报告: `%s`\n- 日志: `%s`\n\n%s", task.IssueRepo, task.IssueNumber, task.State, duration.Round(time.Second), task.ReportPath, logFile, github.DoneMarker)
}
