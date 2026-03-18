package sevolver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const scanWindowDays = 7

type Config struct {
	KBDir       string
	JournalDir  string
	ReportDir   string
	VarDir      string
	ControlRepo string
	TargetRepo  string
	IssueLabel  string
	Secrets     map[string]string
	IssueClient DeepAnalysisClient
	Now         time.Time
}

type Result struct {
	Now                    time.Time
	WindowStart            time.Time
	Hits                   []SkillHit
	Gaps                   []GapSignal
	TaskEventGaps          []GapSignal
	Dormant                []string
	Archived               []string
	ReportPath             string
	GapFilePath            string
	DeepAnalysis           *DeepAnalysisDecision
	EscalatedGapIDs        []string
	EscalatedSkills        []string
	ResolvedGapIDs         []string
	ResolvedSkills         []string
	ResolvedIssues         []int
	ResolvedIssueReasons   map[int]string
	ResolvedReasonCounters map[string]int
	Errors                 []string
}

func Run(cfg Config, out io.Writer) (*Result, error) {
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}
	if strings.TrimSpace(cfg.KBDir) == "" {
		return nil, fmt.Errorf("KBDir 不能为空")
	}
	journalDir := strings.TrimSpace(cfg.JournalDir)
	if journalDir == "" {
		journalDir = filepath.Join(cfg.KBDir, "journal")
	}
	reportRoot := strings.TrimSpace(cfg.ReportDir)
	if reportRoot == "" {
		reportRoot = cfg.KBDir
	}

	result := &Result{
		Now:         dateFloor(now),
		WindowStart: dateFloor(now).AddDate(0, 0, -scanWindowDays),
	}
	if out != nil {
		_, _ = fmt.Fprintf(out, "sevolver: start %s\n", result.Now.Format("2006-01-02"))
	}

	hits, err := ScanJournal(journalDir, result.WindowStart)
	if err != nil {
		return nil, fmt.Errorf("扫描 journal skill 命中失败: %w", err)
	}
	result.Hits = hits

	gaps, err := ScanJournalForGaps(journalDir, cfg.KBDir, result.WindowStart)
	if err != nil {
		return nil, fmt.Errorf("扫描 journal 缺口信号失败: %w", err)
	}
	taskEventGaps, err := ScanTaskEventsForGaps(cfg.VarDir, cfg.KBDir, result.WindowStart)
	if err != nil {
		return nil, fmt.Errorf("扫描 task_events 缺口信号失败: %w", err)
	}
	result.TaskEventGaps = taskEventGaps
	result.Gaps = mergeGapSignals(gaps, taskEventGaps)

	for _, hit := range hits {
		skillFile := filepath.Join(cfg.KBDir, filepath.FromSlash(hit.SkillPath))
		if _, err := os.Stat(skillFile); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("skill 不存在: %s", filepath.ToSlash(skillFile)))
			continue
		}
		if err := UpdateSkillMeta(skillFile, hit); err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
	}

	lifecycleActions, err := processSkillLifecycle(cfg.KBDir, result.Now)
	if err != nil {
		return nil, err
	}
	for _, action := range lifecycleActions {
		switch action.Status {
		case skillStatusDormant:
			result.Dormant = append(result.Dormant, filepath.ToSlash(action.Path))
		case skillStatusArchived:
			result.Archived = append(result.Archived, filepath.ToSlash(action.Path))
		}
	}

	gapFilePath, err := AppendGapSignals(cfg.KBDir, gaps, result.Now)
	if err != nil {
		return nil, err
	}
	result.GapFilePath = gapFilePath

	backlog, err := LoadGapSignals(cfg.KBDir, result.Now)
	if err != nil {
		return nil, err
	}
	resolvedBacklog, resolution, err := ResolveClosedDeepAnalysisEscalations(cfg, backlog, result.Now)
	if resolvedBacklog != nil {
		backlog = resolvedBacklog
	}
	if resolution != nil {
		result.ResolvedGapIDs = append([]string(nil), resolution.GapIDs...)
		result.ResolvedSkills = append([]string(nil), resolution.SkillPaths...)
		result.ResolvedIssues = append([]int(nil), resolution.IssueNumbers...)
		result.ResolvedIssueReasons = cloneIssueReasonMap(resolution.IssueCloseReasons)
		result.ResolvedReasonCounters = cloneReasonCounterMap(resolution.CloseReasonCounters)
	}
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("深度分析收敛回写失败: %v", err))
	}
	activeBacklog := filterActiveGapSignals(backlog)
	deepAnalysis, err := MaybeTriggerDeepAnalysis(cfg, activeBacklog)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("深度分析触发失败: %v", err))
	} else {
		result.DeepAnalysis = deepAnalysis
		backfill, err := BackfillDeepAnalysisEscalation(cfg.KBDir, backlog, deepAnalysis, result.Hits, result.Now)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("深度分析回写失败: %v", err))
		} else if backfill != nil {
			result.EscalatedGapIDs = append([]string(nil), backfill.GapIDs...)
			result.EscalatedSkills = append([]string(nil), backfill.SkillPaths...)
		}
	}

	reportPath, err := WriteDailyReport(reportRoot, result.Now, *result)
	if err != nil {
		return nil, err
	}
	result.ReportPath = reportPath

	if out != nil {
		_, _ = fmt.Fprintf(out, "sevolver: hits=%d gaps=%d task_event_gaps=%d dormant=%d archived=%d\n", len(result.Hits), len(result.Gaps), len(result.TaskEventGaps), len(result.Dormant), len(result.Archived))
		if result.DeepAnalysis != nil && result.DeepAnalysis.Triggered {
			state := "reused"
			if result.DeepAnalysis.Created {
				state = "created"
			}
			_, _ = fmt.Fprintf(out, "sevolver: deep_analysis=%s fingerprint=%s issue=%s\n", state, result.DeepAnalysis.Fingerprint, result.DeepAnalysis.IssueURL)
			if len(result.EscalatedGapIDs) > 0 || len(result.EscalatedSkills) > 0 {
				_, _ = fmt.Fprintf(out, "sevolver: deep_analysis_backfill gaps=%d skills=%d\n", len(result.EscalatedGapIDs), len(result.EscalatedSkills))
			}
		}
		if len(result.ResolvedIssues) > 0 || len(result.ResolvedGapIDs) > 0 || len(result.ResolvedSkills) > 0 {
			_, _ = fmt.Fprintf(
				out,
				"sevolver: deep_analysis_resolved issues=%d gaps=%d skills=%d reasons=%s\n",
				len(result.ResolvedIssues),
				len(result.ResolvedGapIDs),
				len(result.ResolvedSkills),
				formatCloseReasonCounters(result.ResolvedReasonCounters),
			)
		}
		if result.ReportPath != "" {
			_, _ = fmt.Fprintf(out, "sevolver: report=%s\n", result.ReportPath)
		}
		_, _ = fmt.Fprintln(out, "sevolver: done")
	}
	return result, nil
}

func (cfg Config) deepAnalysisNow() time.Time {
	if cfg.Now.IsZero() {
		return time.Now()
	}
	return cfg.Now
}

func mergeGapSignals(groups ...[]GapSignal) []GapSignal {
	indexed := map[string]GapSignal{}
	for _, group := range groups {
		for _, gap := range group {
			if strings.TrimSpace(gap.ID) == "" {
				continue
			}
			if existing, ok := indexed[gap.ID]; ok {
				indexed[gap.ID] = mergeGapSignal(existing, gap)
				continue
			}
			indexed[gap.ID] = gap
		}
	}
	items := make([]GapSignal, 0, len(indexed))
	for _, item := range indexed {
		items = append(items, item)
	}
	sortGapSignals(items)
	return items
}

func cloneIssueReasonMap(input map[int]string) map[int]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[int]string, len(input))
	for issue, reason := range input {
		cloned[issue] = reason
	}
	return cloned
}

func cloneReasonCounterMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]int, len(input))
	for reason, count := range input {
		cloned[reason] = count
	}
	return cloned
}

func formatCloseReasonCounters(counters map[string]int) string {
	if len(counters) == 0 {
		return "-"
	}
	reasons := make([]string, 0, len(counters))
	for reason := range counters {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, counters[reason]))
	}
	return strings.Join(parts, ",")
}
