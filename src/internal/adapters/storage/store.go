package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/core"
)

type Store struct {
	varDir string
	state  *StateStore
	jsonl  *JSONLStore
	slots  *RepoSlotStore
}

type TokenUsageRecord struct {
	TaskID     string
	SessionID  string
	PromptFile string
	Usage      core.TokenUsage
	CostUSD    float64
	DurationMS int64
	RTKEnabled bool
	RecordedAt time.Time
}

type TokenStatsSummary struct {
	Runs         int
	Sessions     int
	InputTokens  int
	OutputTokens int
	CacheCreate  int
	CacheRead    int
	CostUSD      float64
	FirstUsedAt  time.Time
	LastUsedAt   time.Time
}

type TaskTokenStat struct {
	IssueRepo      string
	TaskID         string
	IssueNumber    int
	IssueTitle     string
	LastSessionID  string
	Runs           int
	InputTokens    int
	OutputTokens   int
	CacheCreate    int
	CacheRead      int
	CostUSD        float64
	LastRecordedAt time.Time
}

type RTKComparison struct {
	RTKRuns         int
	PlainRuns       int
	RTKAvgCostUSD   float64
	PlainAvgCostUSD float64
	RTKAvgTokens    float64
	PlainAvgTokens  float64
	SavingsPercent  float64
}

type DailyTokenStat struct {
	Day          time.Time
	Runs         int
	Sessions     int
	InputTokens  int
	OutputTokens int
	CacheCreate  int
	CacheRead    int
	CostUSD      float64
}

type DailyRTKComparison struct {
	Day             time.Time
	RTKRuns         int
	PlainRuns       int
	RTKAvgCostUSD   float64
	PlainAvgCostUSD float64
	RTKAvgTokens    float64
	PlainAvgTokens  float64
	SavingsPercent  float64
}

type TaskClassFlowStat struct {
	TaskClass       string  `json:"task_class"`
	UpgradeCount    int     `json:"upgrade_count"`
	ConvergedCount  int     `json:"converged_count"`
	BacklogCount    int     `json:"backlog_count"`
	ClosedCount     int     `json:"closed_count"`
	SuccessCount    int     `json:"success_count"`
	FailureCount    int     `json:"failure_count"`
	SuccessRate     float64 `json:"success_rate"`
	FailureRate     float64 `json:"failure_rate"`
	AvgCloseSeconds float64 `json:"avg_close_seconds"`
	Runs            int     `json:"runs"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	CacheCreate     int     `json:"cache_create"`
	CacheRead       int     `json:"cache_read"`
	CostUSD         float64 `json:"cost_usd"`
}

type TaskClassFlowSnapshot struct {
	Start   time.Time           `json:"start,omitempty"`
	End     time.Time           `json:"end,omitempty"`
	Classes []TaskClassFlowStat `json:"classes"`
}

type JournalDaySummary struct {
	TasksTouched int
	Started      int
	Done         int
	Failed       int
	Dead         int
	InputTokens  int
	OutputTokens int
	CacheCreate  int
	CacheRead    int
	CostUSD      float64
}

type JournalTaskSummary struct {
	TaskID       string
	IssueRepo    string
	IssueNumber  int
	IssueTitle   string
	State        core.State
	Runs         int
	InputTokens  int
	OutputTokens int
	CacheCreate  int
	CacheRead    int
	CostUSD      float64
	UpdatedAt    time.Time
}

type EventRecord struct {
	Seq             int64          `json:"seq"`
	TaskID          string         `json:"task_id"`
	EventType       core.EventType `json:"event_type"`
	Detail          string         `json:"detail"`
	GapKeyword      string         `json:"gap_keyword,omitempty"`
	GapScope        string         `json:"gap_scope,omitempty"`
	GapSource       string         `json:"gap_source,omitempty"`
	RootCauseHint   string         `json:"root_cause_hint,omitempty"`
	GapAggregatable bool           `json:"gap_aggregatable,omitempty"`
	GapEscalatable  bool           `json:"gap_escalatable,omitempty"`
	CreatedAt       time.Time      `json:"ts"`
	PrevHash        string         `json:"prev_hash"`
	Hash            string         `json:"hash"`
}

func (r EventRecord) GetSeq() int64 {
	return r.Seq
}

func (r EventRecord) GetHash() string {
	return r.Hash
}

func (r tokenJSONRecord) GetSeq() int64 {
	return r.Seq
}

func (r tokenJSONRecord) GetHash() string {
	return r.Hash
}

func Open(path string) (*Store, error) {
	varDir := resolveVarDir(path)
	if strings.HasSuffix(strings.ToLower(varDir), ".db") {
		return nil, fmt.Errorf("storage.Open 仅接受 var_dir 目录，不接受 .db 路径: %s", path)
	}
	if err := os.MkdirAll(varDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建状态目录失败: %w", err)
	}
	if err := cleanupLegacyDBFiles(varDir); err != nil {
		return nil, err
	}
	return &Store{
		varDir: varDir,
		state:  newStateStore(varDir),
		jsonl:  newJSONLStore(varDir),
		slots:  newRepoSlotStore(varDir),
	}, nil
}

func (s *Store) Close() error {
	return nil
}

func (s *Store) Ping() error {
	if err := os.MkdirAll(s.varDir, 0o755); err != nil {
		return fmt.Errorf("创建状态目录失败: %w", err)
	}
	_, err := s.state.Snapshot()
	return err
}

func (s *Store) GetByIdempotency(idempotencyKey string) (*core.Task, error) {
	return s.state.GetByIdempotency(idempotencyKey)
}

func (s *Store) GetTask(taskID string) (*core.Task, error) {
	return s.state.GetTask(taskID)
}

func (s *Store) UpsertTask(task *core.Task) error {
	return s.state.UpsertTask(task)
}

func (s *Store) ApplyTask(taskID string, fn func(*core.Task) (*core.Task, error)) (*core.Task, error) {
	return s.state.ApplyTask(taskID, fn)
}

func (s *Store) AppendEvent(taskID string, eventType core.EventType, detail string) error {
	return s.jsonl.AppendEvent(taskID, eventType, detail)
}

func (s *Store) AppendEventStructured(taskID string, eventType core.EventType, detail string, meta EventGapMeta) error {
	return s.jsonl.AppendEventWithMeta(taskID, eventType, detail, meta)
}

func (s *Store) GetRepoSlot(targetRepo string) (*RepoSlot, error) {
	return s.slots.Get(targetRepo)
}

func (s *Store) GetActiveRepoSlot() (*RepoSlot, error) {
	return s.slots.GetActive()
}

func (s *Store) ListRepoSlots() ([]*RepoSlot, error) {
	return s.slots.List()
}

func (s *Store) UpsertRepoSlot(slot *RepoSlot) error {
	return s.slots.Upsert(slot)
}

func (s *Store) DeleteRepoSlot(targetRepo string) error {
	return s.slots.Delete(targetRepo)
}

func (s *Store) DeleteActiveRepoSlot() error {
	return s.slots.Delete("")
}

func (s *Store) AppendEventAt(taskID string, eventType core.EventType, detail string, createdAt time.Time) error {
	return s.jsonl.AppendEventAt(taskID, eventType, detail, createdAt)
}

func (s *Store) AppendEventAtStructured(taskID string, eventType core.EventType, detail string, createdAt time.Time, meta EventGapMeta) error {
	return s.jsonl.AppendEventAtWithMeta(taskID, eventType, detail, createdAt, meta)
}

func (s *Store) RecordTokenUsage(record TokenUsageRecord) error {
	return s.jsonl.AppendToken(record)
}

func (s *Store) TokenStats() (*TokenStatsSummary, error) {
	return s.TokenStatsBetween(time.Time{}, time.Time{})
}

func (s *Store) TokenStatsBetween(start, end time.Time) (*TokenStatsSummary, error) {
	records, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	summary := &TokenStatsSummary{}
	sessions := map[string]struct{}{}
	for _, record := range records {
		summary.Runs++
		summary.InputTokens += record.InputTokens
		summary.OutputTokens += record.OutputTokens
		summary.CacheCreate += record.CacheCreate
		summary.CacheRead += record.CacheRead
		summary.CostUSD += record.CostUSD
		if strings.TrimSpace(record.SessionID) != "" {
			sessions[record.SessionID] = struct{}{}
		}
		if summary.FirstUsedAt.IsZero() || record.RecordedAt.Before(summary.FirstUsedAt) {
			summary.FirstUsedAt = record.RecordedAt
		}
		if summary.LastUsedAt.IsZero() || record.RecordedAt.After(summary.LastUsedAt) {
			summary.LastUsedAt = record.RecordedAt
		}
	}
	summary.Sessions = len(sessions)
	return summary, nil
}

func (s *Store) RTKComparisonBetween(start, end time.Time) (*RTKComparison, error) {
	records, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	return summarizeRTKComparison(records), nil
}

func (s *Store) TaskTokenStats(limit int) ([]TaskTokenStat, error) {
	return s.TaskTokenStatsBetween(time.Time{}, time.Time{}, limit)
}

func (s *Store) TaskTokenStatsBetween(start, end time.Time, limit int) ([]TaskTokenStat, error) {
	if limit <= 0 {
		limit = 20
	}
	tasks, err := s.state.Snapshot()
	if err != nil {
		return nil, err
	}
	records, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	grouped := map[string]*TaskTokenStat{}
	for _, record := range records {
		item, ok := grouped[record.TaskID]
		if !ok {
			item = &TaskTokenStat{TaskID: record.TaskID}
			if task := tasks[record.TaskID]; task != nil {
				item.IssueRepo = task.IssueRepo
				item.IssueNumber = task.IssueNumber
				item.IssueTitle = task.IssueTitle
				item.LastSessionID = task.LastSessionID
			}
			grouped[record.TaskID] = item
		}
		item.Runs++
		item.InputTokens += record.InputTokens
		item.OutputTokens += record.OutputTokens
		item.CacheCreate += record.CacheCreate
		item.CacheRead += record.CacheRead
		item.CostUSD += record.CostUSD
		if record.RecordedAt.After(item.LastRecordedAt) {
			item.LastRecordedAt = record.RecordedAt
		}
	}
	items := make([]TaskTokenStat, 0, len(grouped))
	for _, item := range grouped {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].LastRecordedAt.Equal(items[j].LastRecordedAt) {
			return items[i].LastRecordedAt.After(items[j].LastRecordedAt)
		}
		return items[i].IssueNumber > items[j].IssueNumber
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) DailyTokenStatsBetween(start, end time.Time) ([]DailyTokenStat, error) {
	records, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	loc := time.Local
	if !start.IsZero() {
		loc = start.Location()
	}
	type aggregate struct {
		item     DailyTokenStat
		sessions map[string]struct{}
	}
	grouped := map[string]*aggregate{}
	ordered := make([]string, 0)
	for _, record := range records {
		day := normalizeDay(record.RecordedAt, loc)
		key := day.Format("2006-01-02")
		entry, ok := grouped[key]
		if !ok {
			entry = &aggregate{
				item:     DailyTokenStat{Day: day},
				sessions: map[string]struct{}{},
			}
			grouped[key] = entry
			ordered = append(ordered, key)
		}
		entry.item.Runs++
		entry.item.InputTokens += record.InputTokens
		entry.item.OutputTokens += record.OutputTokens
		entry.item.CacheCreate += record.CacheCreate
		entry.item.CacheRead += record.CacheRead
		entry.item.CostUSD += record.CostUSD
		if strings.TrimSpace(record.SessionID) != "" {
			entry.sessions[record.SessionID] = struct{}{}
		}
	}
	sort.Strings(ordered)
	items := make([]DailyTokenStat, 0, len(ordered))
	for _, key := range ordered {
		entry := grouped[key]
		entry.item.Sessions = len(entry.sessions)
		items = append(items, entry.item)
	}
	return items, nil
}

func (s *Store) DailyRTKComparisonBetween(start, end time.Time) ([]DailyRTKComparison, error) {
	records, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	loc := time.Local
	if !start.IsZero() {
		loc = start.Location()
	}
	type bucket struct {
		runs   int
		cost   float64
		tokens int
	}
	type aggregate struct {
		day   time.Time
		rtk   bucket
		plain bucket
	}
	grouped := map[string]*aggregate{}
	ordered := make([]string, 0)
	for _, record := range records {
		day := normalizeDay(record.RecordedAt, loc)
		key := day.Format("2006-01-02")
		entry, ok := grouped[key]
		if !ok {
			entry = &aggregate{day: day}
			grouped[key] = entry
			ordered = append(ordered, key)
		}
		target := &entry.plain
		if record.RTKEnabled {
			target = &entry.rtk
		}
		target.runs++
		target.cost += record.CostUSD
		target.tokens += record.InputTokens + record.OutputTokens + record.CacheCreate + record.CacheRead
	}
	sort.Strings(ordered)
	items := make([]DailyRTKComparison, 0, len(ordered))
	for _, key := range ordered {
		entry := grouped[key]
		item := DailyRTKComparison{
			Day:       entry.day,
			RTKRuns:   entry.rtk.runs,
			PlainRuns: entry.plain.runs,
		}
		if entry.rtk.runs > 0 {
			item.RTKAvgCostUSD = entry.rtk.cost / float64(entry.rtk.runs)
			item.RTKAvgTokens = float64(entry.rtk.tokens) / float64(entry.rtk.runs)
		}
		if entry.plain.runs > 0 {
			item.PlainAvgCostUSD = entry.plain.cost / float64(entry.plain.runs)
			item.PlainAvgTokens = float64(entry.plain.tokens) / float64(entry.plain.runs)
		}
		if item.RTKRuns > 0 && item.PlainRuns > 0 && item.PlainAvgCostUSD > 0 {
			item.SavingsPercent = (item.PlainAvgCostUSD - item.RTKAvgCostUSD) / item.PlainAvgCostUSD * 100
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) TaskClassFlowStatsBetween(start, end time.Time, classes []core.TaskClass) (*TaskClassFlowSnapshot, error) {
	tasks, err := s.state.Snapshot()
	if err != nil {
		return nil, err
	}

	order, selected := normalizeTaskClassSelection(classes)
	if len(order) == 0 {
		return &TaskClassFlowSnapshot{Start: start, End: end, Classes: nil}, nil
	}

	indexed := make(map[core.TaskClass]*TaskClassFlowStat, len(order))
	for _, taskClass := range order {
		indexed[taskClass] = &TaskClassFlowStat{TaskClass: string(taskClass)}
	}

	for _, task := range tasks {
		if task == nil {
			continue
		}
		taskClass := normalizeTaskClass(task.TaskClass)
		if _, ok := selected[taskClass]; !ok {
			continue
		}
		if task.State != core.StateDone && task.State != core.StateDead {
			indexed[taskClass].BacklogCount++
		}
	}

	tokenRecords, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	for _, record := range tokenRecords {
		task := tasks[record.TaskID]
		if task == nil {
			continue
		}
		taskClass := normalizeTaskClass(task.TaskClass)
		if _, ok := selected[taskClass]; !ok {
			continue
		}
		item := indexed[taskClass]
		item.Runs++
		item.InputTokens += record.InputTokens
		item.OutputTokens += record.OutputTokens
		item.CacheCreate += record.CacheCreate
		item.CacheRead += record.CacheRead
		item.CostUSD += record.CostUSD
	}

	events, err := s.filteredEvents(start, end, "")
	if err != nil {
		return nil, err
	}
	createdByClass := make(map[core.TaskClass]map[string]struct{}, len(order))
	for _, taskClass := range order {
		createdByClass[taskClass] = map[string]struct{}{}
	}
	type terminalOutcome struct {
		EventType core.EventType
		ClosedAt  time.Time
	}
	closedByTask := map[string]terminalOutcome{}
	for _, event := range events {
		task := tasks[event.TaskID]
		if task == nil {
			continue
		}
		taskClass := normalizeTaskClass(task.TaskClass)
		if _, ok := selected[taskClass]; !ok {
			continue
		}
		switch event.EventType {
		case core.EventCreated:
			createdByClass[taskClass][event.TaskID] = struct{}{}
		case core.EventDone, core.EventDead:
			closedByTask[event.TaskID] = terminalOutcome{
				EventType: event.EventType,
				ClosedAt:  event.CreatedAt,
			}
		}
	}
	for _, taskClass := range order {
		indexed[taskClass].UpgradeCount = len(createdByClass[taskClass])
	}

	closeDurationSum := make(map[core.TaskClass]time.Duration, len(order))
	closeDurationSamples := make(map[core.TaskClass]int, len(order))
	for taskID, outcome := range closedByTask {
		task := tasks[taskID]
		if task == nil {
			continue
		}
		taskClass := normalizeTaskClass(task.TaskClass)
		item, ok := indexed[taskClass]
		if !ok {
			continue
		}
		item.ClosedCount++
		if outcome.EventType == core.EventDone {
			item.ConvergedCount++
			item.SuccessCount++
		} else {
			item.FailureCount++
		}
		if !task.CreatedAt.IsZero() && !outcome.ClosedAt.IsZero() && !outcome.ClosedAt.Before(task.CreatedAt) {
			closeDurationSum[taskClass] += outcome.ClosedAt.Sub(task.CreatedAt)
			closeDurationSamples[taskClass]++
		}
	}

	items := make([]TaskClassFlowStat, 0, len(order))
	for _, taskClass := range order {
		item := indexed[taskClass]
		total := item.SuccessCount + item.FailureCount
		if total > 0 {
			item.SuccessRate = float64(item.SuccessCount) * 100 / float64(total)
			item.FailureRate = float64(item.FailureCount) * 100 / float64(total)
		}
		if samples := closeDurationSamples[taskClass]; samples > 0 {
			item.AvgCloseSeconds = closeDurationSum[taskClass].Seconds() / float64(samples)
		}
		items = append(items, *item)
	}
	return &TaskClassFlowSnapshot{
		Start:   start,
		End:     end,
		Classes: items,
	}, nil
}

func (s *Store) ListTasks() ([]*core.Task, error) {
	tasks, err := s.state.ListTasks()
	if err != nil {
		return nil, err
	}
	sortTasksByUpdatedDesc(tasks)
	return tasks, nil
}

func (s *Store) ListRunnable(limit int) ([]*core.Task, error) {
	tasks, err := s.state.ListTasks()
	if err != nil {
		return nil, err
	}
	items := make([]*core.Task, 0)
	for _, task := range tasks {
		if task.State == core.StateNew || task.State == core.StateFailed {
			items = append(items, task)
		}
	}
	sortTasksByUpdatedAsc(items)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Store) ListRunnableByTarget(limitPerTarget int) (map[string][]*core.Task, error) {
	tasks, err := s.state.ListTasks()
	if err != nil {
		return nil, err
	}
	grouped := map[string][]*core.Task{}
	for _, task := range tasks {
		if task.State != core.StateNew && task.State != core.StateFailed {
			continue
		}
		targetRepo := strings.TrimSpace(task.TargetRepo)
		if targetRepo == "" {
			continue
		}
		grouped[targetRepo] = append(grouped[targetRepo], task)
	}
	for targetRepo := range grouped {
		sortTasksByCreatedAsc(grouped[targetRepo])
		if limitPerTarget > 0 && len(grouped[targetRepo]) > limitPerTarget {
			grouped[targetRepo] = grouped[targetRepo][:limitPerTarget]
		}
	}
	return grouped, nil
}

func (s *Store) ListRunning() ([]*core.Task, error) {
	tasks, err := s.state.ListTasks()
	if err != nil {
		return nil, err
	}
	items := make([]*core.Task, 0)
	for _, task := range tasks {
		if task.State == core.StateRunning {
			items = append(items, task)
		}
	}
	sortTasksByUpdatedAsc(items)
	return items, nil
}

func (s *Store) JournalDaySummary(day time.Time) (*JournalDaySummary, error) {
	start, end := dayBounds(day)
	return s.JournalSummaryBetween(start, end)
}

func (s *Store) JournalDaySummaryByTarget(day time.Time, targetRepo string) (*JournalDaySummary, error) {
	start, end := dayBounds(day)
	targetRepo = strings.TrimSpace(targetRepo)
	tokens, err := s.filteredTokens(start, end, targetRepo)
	if err != nil {
		return nil, err
	}
	events, err := s.filteredEvents(start, end, targetRepo)
	if err != nil {
		return nil, err
	}
	return summarizeJournalDay(tokens, events), nil
}

func (s *Store) JournalSummaryBetween(start, end time.Time) (*JournalDaySummary, error) {
	tokens, err := s.filteredTokens(start, end, "")
	if err != nil {
		return nil, err
	}
	events, err := s.filteredEvents(start, end, "")
	if err != nil {
		return nil, err
	}
	return summarizeJournalDay(tokens, events), nil
}

func (s *Store) JournalTaskSummaries(day time.Time) ([]JournalTaskSummary, error) {
	return s.journalTaskSummaries(day, "")
}

func (s *Store) JournalTaskSummariesByTarget(day time.Time, targetRepo string) ([]JournalTaskSummary, error) {
	return s.journalTaskSummaries(day, strings.TrimSpace(targetRepo))
}

func (s *Store) ListTaskEventsBetween(start, end time.Time, limit int) ([]EventRecord, error) {
	records, err := s.filteredEvents(start, end, "")
	if err != nil {
		return nil, err
	}
	return limitEvents(records, limit), nil
}

func (s *Store) ListAllTaskEventsBetween(start, end time.Time) ([]EventRecord, error) {
	return s.filteredEvents(start, end, "")
}

func (s *Store) ListTaskEventsBetweenByTarget(start, end time.Time, limit int, targetRepo string) ([]EventRecord, error) {
	records, err := s.filteredEvents(start, end, strings.TrimSpace(targetRepo))
	if err != nil {
		return nil, err
	}
	return limitEvents(records, limit), nil
}

func (s *Store) RTKComparisonBetweenByTarget(start, end time.Time, targetRepo string) (*RTKComparison, error) {
	records, err := s.filteredTokens(start, end, strings.TrimSpace(targetRepo))
	if err != nil {
		return nil, err
	}
	return summarizeRTKComparison(records), nil
}

func (s *Store) ListTaskEvents(limit int) ([]EventRecord, error) {
	records, err := s.filteredEvents(time.Time{}, time.Time{}, "")
	if err != nil {
		return nil, err
	}
	return limitEvents(records, limit), nil
}

func (s *Store) filteredTokens(start, end time.Time, targetRepo string) ([]tokenJSONRecord, error) {
	tasks, err := s.state.Snapshot()
	if err != nil {
		return nil, err
	}
	records, err := s.jsonl.ReadTokens()
	if err != nil {
		return nil, err
	}
	items := make([]tokenJSONRecord, 0, len(records))
	for _, record := range records {
		if !withinRange(record.RecordedAt, start, end) {
			continue
		}
		if targetRepo != "" {
			task := tasks[record.TaskID]
			if task == nil || strings.TrimSpace(task.TargetRepo) != targetRepo {
				continue
			}
		}
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].RecordedAt.Equal(items[j].RecordedAt) {
			return items[i].RecordedAt.Before(items[j].RecordedAt)
		}
		return items[i].Seq < items[j].Seq
	})
	return items, nil
}

func (s *Store) filteredEvents(start, end time.Time, targetRepo string) ([]EventRecord, error) {
	tasks, err := s.state.Snapshot()
	if err != nil {
		return nil, err
	}
	records, err := s.jsonl.ReadEvents()
	if err != nil {
		return nil, err
	}
	items := make([]EventRecord, 0, len(records))
	for _, record := range records {
		if !withinRange(record.CreatedAt, start, end) {
			continue
		}
		if targetRepo != "" {
			task := tasks[record.TaskID]
			if task == nil || strings.TrimSpace(task.TargetRepo) != targetRepo {
				continue
			}
		}
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Seq < items[j].Seq
	})
	return items, nil
}

func (s *Store) journalTaskSummaries(day time.Time, targetRepo string) ([]JournalTaskSummary, error) {
	start, end := dayBounds(day)
	tasks, err := s.state.Snapshot()
	if err != nil {
		return nil, err
	}
	tokens, err := s.filteredTokens(start, end, targetRepo)
	if err != nil {
		return nil, err
	}
	events, err := s.filteredEvents(start, end, targetRepo)
	if err != nil {
		return nil, err
	}
	touched := map[string]struct{}{}
	for _, event := range events {
		touched[event.TaskID] = struct{}{}
	}
	for _, token := range tokens {
		touched[token.TaskID] = struct{}{}
	}
	for taskID, task := range tasks {
		if task == nil || !withinRange(task.UpdatedAt, start, end) {
			continue
		}
		if targetRepo != "" && strings.TrimSpace(task.TargetRepo) != targetRepo {
			continue
		}
		touched[taskID] = struct{}{}
	}

	summaries := make(map[string]*JournalTaskSummary, len(touched))
	for taskID := range touched {
		task := tasks[taskID]
		item := &JournalTaskSummary{TaskID: taskID}
		if task != nil {
			item.IssueRepo = task.IssueRepo
			item.IssueNumber = task.IssueNumber
			item.IssueTitle = task.IssueTitle
			item.State = task.State
			item.UpdatedAt = task.UpdatedAt
		}
		summaries[taskID] = item
	}
	for _, token := range tokens {
		item, ok := summaries[token.TaskID]
		if !ok {
			continue
		}
		item.Runs++
		item.InputTokens += token.InputTokens
		item.OutputTokens += token.OutputTokens
		item.CacheCreate += token.CacheCreate
		item.CacheRead += token.CacheRead
		item.CostUSD += token.CostUSD
	}
	items := make([]JournalTaskSummary, 0, len(summaries))
	for _, item := range summaries {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].IssueNumber > items[j].IssueNumber
	})
	return items, nil
}

func summarizeJournalDay(tokens []tokenJSONRecord, events []EventRecord) *JournalDaySummary {
	summary := &JournalDaySummary{}
	touched := map[string]struct{}{}
	started := map[string]struct{}{}
	done := map[string]struct{}{}
	failed := map[string]struct{}{}
	dead := map[string]struct{}{}
	for _, token := range tokens {
		touched[token.TaskID] = struct{}{}
		summary.InputTokens += token.InputTokens
		summary.OutputTokens += token.OutputTokens
		summary.CacheCreate += token.CacheCreate
		summary.CacheRead += token.CacheRead
		summary.CostUSD += token.CostUSD
	}
	for _, event := range events {
		touched[event.TaskID] = struct{}{}
		switch event.EventType {
		case core.EventStarted:
			started[event.TaskID] = struct{}{}
		case core.EventDone:
			done[event.TaskID] = struct{}{}
		case core.EventFailed:
			failed[event.TaskID] = struct{}{}
		case core.EventDead:
			dead[event.TaskID] = struct{}{}
		}
	}
	summary.TasksTouched = len(touched)
	summary.Started = len(started)
	summary.Done = len(done)
	summary.Failed = len(failed)
	summary.Dead = len(dead)
	return summary
}

func summarizeRTKComparison(records []tokenJSONRecord) *RTKComparison {
	item := &RTKComparison{}
	var rtkCost, plainCost float64
	var rtkTokens, plainTokens int
	for _, record := range records {
		if record.RTKEnabled {
			item.RTKRuns++
			rtkCost += record.CostUSD
			rtkTokens += record.InputTokens + record.OutputTokens + record.CacheCreate + record.CacheRead
			continue
		}
		item.PlainRuns++
		plainCost += record.CostUSD
		plainTokens += record.InputTokens + record.OutputTokens + record.CacheCreate + record.CacheRead
	}
	if item.RTKRuns > 0 {
		item.RTKAvgCostUSD = rtkCost / float64(item.RTKRuns)
		item.RTKAvgTokens = float64(rtkTokens) / float64(item.RTKRuns)
	}
	if item.PlainRuns > 0 {
		item.PlainAvgCostUSD = plainCost / float64(item.PlainRuns)
		item.PlainAvgTokens = float64(plainTokens) / float64(item.PlainRuns)
	}
	if item.RTKRuns > 0 && item.PlainRuns > 0 && item.PlainAvgCostUSD > 0 {
		item.SavingsPercent = (item.PlainAvgCostUSD - item.RTKAvgCostUSD) / item.PlainAvgCostUSD * 100
	}
	return item
}

func limitEvents(records []EventRecord, limit int) []EventRecord {
	if limit <= 0 {
		limit = 50
	}
	items := append([]EventRecord(nil), records...)
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].Seq > items[j].Seq
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func withinRange(ts, start, end time.Time) bool {
	if !start.IsZero() && ts.Before(start) {
		return false
	}
	if !end.IsZero() && !ts.Before(end) {
		return false
	}
	return true
}

func normalizeDay(ts time.Time, loc *time.Location) time.Time {
	day := ts.In(loc)
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

func dayBounds(day time.Time) (time.Time, time.Time) {
	loc := day.Location()
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return start, start.Add(24 * time.Hour)
}

func normalizeTaskClassSelection(classes []core.TaskClass) ([]core.TaskClass, map[core.TaskClass]struct{}) {
	order := make([]core.TaskClass, 0, len(classes))
	selected := make(map[core.TaskClass]struct{}, len(classes))
	for _, raw := range classes {
		taskClass := normalizeTaskClass(raw)
		if taskClass == "" {
			continue
		}
		if _, ok := selected[taskClass]; ok {
			continue
		}
		selected[taskClass] = struct{}{}
		order = append(order, taskClass)
	}
	return order, selected
}

func normalizeTaskClass(taskClass core.TaskClass) core.TaskClass {
	value := strings.TrimSpace(string(taskClass))
	if value == "" {
		return core.TaskClassGeneral
	}
	return core.TaskClass(value)
}

func resolveVarDir(path string) string {
	return filepath.Clean(path)
}
