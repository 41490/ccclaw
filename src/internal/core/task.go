package core

import (
	"fmt"
	"strings"
	"time"
)

type State string

const (
	StateNew        State = "NEW"
	StateRunning    State = "RUNNING"
	StateFinalizing State = "FINALIZING"
	StateBlocked    State = "BLOCKED"
	StateFailed     State = "FAILED"
	StateDone       State = "DONE"
	StateDead       State = "DEAD"
)

type EventType string

const (
	EventCreated EventType = "CREATED"
	EventBlocked EventType = "BLOCKED"
	EventStarted EventType = "STARTED"
	EventFailed  EventType = "FAILED"
	EventDone    EventType = "DONE"
	EventDead    EventType = "DEAD"
	EventUpdated EventType = "UPDATED"
	EventWarning EventType = "WARNING"
)

type RiskLevel string

const (
	RiskLow  RiskLevel = "low"
	RiskMed  RiskLevel = "med"
	RiskHigh RiskLevel = "high"
)

type Intent string

const (
	IntentResearch Intent = "research"
	IntentFix      Intent = "fix"
	IntentOps      Intent = "ops"
	IntentRelease  Intent = "release"
	IntentReport   Intent = "report" // 汇报型 Issue，无可执行任务，直接豁免 Claude 执行
)

type TaskClass string

const (
	TaskClassGeneral              TaskClass = "general"
	TaskClassSevolver             TaskClass = "sevolver"
	TaskClassSevolverDeepAnalysis TaskClass = "sevolver_deep_analysis"
)

const MaxRetry = 3

type Task struct {
	TaskID                string
	IdempotencyKey        string
	ControlRepo           string
	IssueRepo             string
	TargetRepo            string
	LastSessionID         string
	ResultCommentID       int64
	DoneCommentID         int64
	IssueNumber           int
	IssueTitle            string
	IssueBody             string
	IssueAuthor           string
	IssueAuthorPermission string
	Labels                []string
	Intent                Intent
	TaskClass             TaskClass
	RiskLevel             RiskLevel
	Approved              bool
	ApprovalCommand       string
	ApprovalActor         string
	ApprovalCommentID     int64
	State                 State
	RetryCount            int
	RestartCount          int
	ErrorMsg              string
	ReportPath            string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func IdempotencyKey(issueRepo string, issueNumber int) string {
	return fmt.Sprintf("%s#%d#body", strings.TrimSpace(issueRepo), issueNumber)
}

func TaskID(issueRepo string, issueNumber int) string {
	return IdempotencyKey(issueRepo, issueNumber)
}

func InferIntent(labels []string) Intent {
	for _, label := range labels {
		switch strings.ToLower(label) {
		case "ops":
			return IntentOps
		case "bug", "fix":
			return IntentFix
		case "release":
			return IntentRelease
		case "research":
			return IntentResearch
		case "report", "ops-report", "announce":
			return IntentReport
		}
	}
	return IntentResearch
}

func InferRisk(labels []string) RiskLevel {
	for _, label := range labels {
		switch strings.ToLower(label) {
		case "risk:high":
			return RiskHigh
		case "risk:med":
			return RiskMed
		}
	}
	return RiskLow
}

func InferTaskClass(title, body string, labels []string) TaskClass {
	if raw := parseBodyField(body, "task_class"); raw != "" {
		if explicit := normalizeTaskClass(raw); explicit != "" {
			return explicit
		}
	}

	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	if strings.HasPrefix(lowerTitle, "[sevolver]") {
		if strings.Contains(title, "能力缺口深度分析") || strings.Contains(strings.ToLower(body), "ccclaw:sevolver:deep-analysis:fingerprint=") {
			return TaskClassSevolverDeepAnalysis
		}
		return TaskClassSevolver
	}

	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label), "sevolver") {
			return TaskClassSevolver
		}
	}
	return TaskClassGeneral
}

func normalizeTaskClass(raw string) TaskClass {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(TaskClassSevolverDeepAnalysis):
		return TaskClassSevolverDeepAnalysis
	case string(TaskClassSevolver):
		return TaskClassSevolver
	case string(TaskClassGeneral):
		return TaskClassGeneral
	default:
		return ""
	}
}

func parseBodyField(body, field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	if field == "" {
		return ""
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(parts[0])) != field {
			continue
		}
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func SafeReportFileName(now time.Time, issueNumber int, title string) string {
	var cleaned strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cleaned.WriteRune(r)
		case r == '-', r == '_':
			cleaned.WriteRune(r)
		default:
			if cleaned.Len() == 0 || strings.HasSuffix(cleaned.String(), "_") {
				continue
			}
			cleaned.WriteRune('_')
		}
	}
	name := strings.Trim(cleaned.String(), "_")
	if name == "" {
		name = "task_report"
	}
	return fmt.Sprintf("%s_%d_%s.md", now.Format("060102"), issueNumber, name)
}
