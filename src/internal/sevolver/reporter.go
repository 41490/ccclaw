package sevolver

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func WriteDailyReport(kbDir string, now time.Time, result Result) (string, error) {
	reportDir := filepath.Join(strings.TrimSpace(kbDir), "journal", "sevolver", now.Format("2006"), now.Format("01"))
	path := filepath.Join(reportDir, fmt.Sprintf("%s.log.md", now.Format("2006.01.02")))

	lines := []string{
		fmt.Sprintf("- 扫描窗口: %s -> %s", result.WindowStart.Format("2006-01-02"), result.Now.Format("2006-01-02")),
		fmt.Sprintf("- Skill 命中: %d", len(result.Hits)),
		fmt.Sprintf("- 缺口信号: %d", len(result.Gaps)),
		fmt.Sprintf("- task_events 缺口: %d", len(result.TaskEventGaps)),
		fmt.Sprintf("- dormant 处理: %d", len(result.Dormant)),
		fmt.Sprintf("- archived 归档: %d", len(result.Archived)),
	}
	if result.DeepAnalysis != nil {
		switch {
		case result.DeepAnalysis.Created:
			lines = append(lines, fmt.Sprintf("- 深度分析: 已创建 %s", result.DeepAnalysis.IssueURL))
		case result.DeepAnalysis.Existing:
			lines = append(lines, fmt.Sprintf("- 深度分析: 复用已有 %s", result.DeepAnalysis.IssueURL))
		case result.DeepAnalysis.Triggered:
			lines = append(lines, fmt.Sprintf("- 深度分析: 已触发但未创建 issue (%s)", result.DeepAnalysis.Reason))
		default:
			lines = append(lines, fmt.Sprintf("- 深度分析: 未触发 (%s)", result.DeepAnalysis.Reason))
		}
	}
	if len(result.EscalatedGapIDs) > 0 {
		lines = append(lines, fmt.Sprintf("- 升级回写 gap: %d", len(result.EscalatedGapIDs)))
	}
	if len(result.EscalatedSkills) > 0 {
		lines = append(lines, fmt.Sprintf("- 升级回写 skill: %d", len(result.EscalatedSkills)))
	}
	if len(result.ResolvedIssues) > 0 {
		lines = append(lines, fmt.Sprintf("- 收敛关闭 issue: %d", len(result.ResolvedIssues)))
	}
	if len(result.ResolvedReasonCounters) > 0 {
		lines = append(lines, fmt.Sprintf("- 收敛关闭分类: %s", formatCloseReasonCounters(result.ResolvedReasonCounters)))
	}
	if len(result.ResolvedGapIDs) > 0 {
		lines = append(lines, fmt.Sprintf("- 收敛回写 gap: %d", len(result.ResolvedGapIDs)))
	}
	if len(result.ResolvedSkills) > 0 {
		lines = append(lines, fmt.Sprintf("- 收敛回写 skill: %d", len(result.ResolvedSkills)))
	}
	if len(result.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("- 非阻断告警: %d", len(result.Errors)))
	}

	if len(result.Hits) > 0 {
		lines = append(lines, "", "## Skill 命中")
		for _, hit := range result.Hits {
			lines = append(lines, fmt.Sprintf("- %s | %s | %s", hit.Date.Format("2006-01-02"), hit.SkillPath, hit.Source))
		}
	}
	if len(result.Gaps) > 0 {
		lines = append(lines, "", "## 缺口信号")
		for _, gap := range result.Gaps {
			lines = append(lines, fmt.Sprintf("- [%s] %s | %s | %s | %s", gap.ID, gap.Date.Format("2006-01-02"), gap.Keyword, gap.Source, gap.Context))
		}
	}
	if len(result.TaskEventGaps) > 0 {
		lines = append(lines, "", "## task_events 缺口")
		for _, gap := range result.TaskEventGaps {
			lines = append(lines, fmt.Sprintf("- [%s] %s | %s | %s | %s", gap.ID, gap.Date.Format("2006-01-02"), gap.Keyword, gap.Source, gap.Context))
		}
	}
	if len(result.Dormant) > 0 {
		lines = append(lines, "", "## dormant")
		for _, item := range result.Dormant {
			lines = append(lines, "- "+filepath.ToSlash(item))
		}
	}
	if len(result.Archived) > 0 {
		lines = append(lines, "", "## archived")
		for _, item := range result.Archived {
			lines = append(lines, "- "+filepath.ToSlash(item))
		}
	}
	if result.DeepAnalysis != nil && result.DeepAnalysis.Triggered {
		lines = append(lines, "", "## 深度分析")
		lines = append(lines, fmt.Sprintf("- 原因: %s", result.DeepAnalysis.Reason))
		lines = append(lines, fmt.Sprintf("- 指纹: `%s`", result.DeepAnalysis.Fingerprint))
		lines = append(lines, fmt.Sprintf("- 未解决缺口总数: %d", result.DeepAnalysis.BacklogCount))
		if len(result.DeepAnalysis.Keywords) > 0 {
			lines = append(lines, fmt.Sprintf("- 关键词: %s", strings.Join(result.DeepAnalysis.Keywords, ", ")))
		}
		if result.DeepAnalysis.IssueURL != "" {
			lines = append(lines, fmt.Sprintf("- Issue: %s", result.DeepAnalysis.IssueURL))
		}
		if len(result.EscalatedGapIDs) > 0 {
			lines = append(lines, fmt.Sprintf("- 已回写 gap: %s", strings.Join(result.EscalatedGapIDs, ", ")))
		}
		if len(result.EscalatedSkills) > 0 {
			lines = append(lines, fmt.Sprintf("- 已回写 skill: %s", strings.Join(result.EscalatedSkills, ", ")))
		}
	}
	if len(result.ResolvedIssues) > 0 || len(result.ResolvedGapIDs) > 0 || len(result.ResolvedSkills) > 0 {
		lines = append(lines, "", "## 收敛回写")
		if len(result.ResolvedIssues) > 0 {
			values := make([]string, 0, len(result.ResolvedIssues))
			for _, issueNumber := range result.ResolvedIssues {
				reason := strings.TrimSpace(result.ResolvedIssueReasons[issueNumber])
				if reason == "" {
					values = append(values, fmt.Sprintf("#%d", issueNumber))
					continue
				}
				values = append(values, fmt.Sprintf("#%d(%s)", issueNumber, closeReasonLabel(reason)))
			}
			lines = append(lines, fmt.Sprintf("- 已关闭 Issue: %s", strings.Join(values, ", ")))
		}
		if len(result.ResolvedReasonCounters) > 0 {
			reasons := make([]string, 0, len(result.ResolvedReasonCounters))
			for reason := range result.ResolvedReasonCounters {
				reasons = append(reasons, reason)
			}
			sort.Strings(reasons)
			for _, reason := range reasons {
				lines = append(lines, fmt.Sprintf("- 关闭分类 %s: %d", closeReasonLabel(reason), result.ResolvedReasonCounters[reason]))
			}
		}
		if len(result.ResolvedGapIDs) > 0 {
			lines = append(lines, fmt.Sprintf("- 已标记 resolved 的 gap: %s", strings.Join(result.ResolvedGapIDs, ", ")))
		}
		if len(result.ResolvedSkills) > 0 {
			lines = append(lines, fmt.Sprintf("- 已标记 converged 的 skill: %s", strings.Join(result.ResolvedSkills, ", ")))
		}
	}
	if len(result.Errors) > 0 {
		lines = append(lines, "", "## 告警")
		for _, item := range result.Errors {
			lines = append(lines, "- "+item)
		}
	}

	body := strings.Join(lines, "\n")
	title := fmt.Sprintf("%s %s", reportDefaultTitle, now.Format("2006-01-02"))
	if err := writeManagedMarkdownFile(path, title, body, defaultUserStub); err != nil {
		return "", err
	}
	return path, nil
}

func closeReasonLabel(reason string) string {
	switch strings.TrimSpace(reason) {
	case closeReasonFixed:
		return "已修复"
	case closeReasonDuplicate:
		return "重复问题"
	case closeReasonExternalResolved:
		return "外部依赖已解除"
	case closeReasonArchived:
		return "归档不做"
	case closeReasonCannotReproduce:
		return "无法复现"
	default:
		return "其他关闭"
	}
}
