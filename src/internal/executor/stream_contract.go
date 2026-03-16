package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/core"
)

const StreamContractSchemaVersion = "stream_event_contract/v1"

type StreamEventKind string

const (
	StreamEventStarted   StreamEventKind = "started"
	StreamEventProgress  StreamEventKind = "progress"
	StreamEventUsage     StreamEventKind = "usage"
	StreamEventResult    StreamEventKind = "result"
	StreamEventError     StreamEventKind = "error"
	StreamEventRestarted StreamEventKind = "restarted"
	StreamEventSystem    StreamEventKind = "system"
)

type StreamSlotPhase string

const (
	StreamSlotPhaseRunning    StreamSlotPhase = "running"
	StreamSlotPhasePaneDead   StreamSlotPhase = "pane_dead"
	StreamSlotPhaseRestarting StreamSlotPhase = "restarting"
	StreamSlotPhaseFinalizing StreamSlotPhase = "finalizing"
)

type StreamEvent struct {
	Seq       int             `json:"seq"`
	Type      StreamEventKind `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	Timestamp time.Time       `json:"timestamp,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Step      string          `json:"step,omitempty"`
	Message   string          `json:"message,omitempty"`
	Result    string          `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Usage     core.TokenUsage `json:"usage,omitempty"`
	CostUSD   float64         `json:"cost_usd,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

type StreamStateMapping struct {
	RepoSlotPhase   StreamSlotPhase `json:"repo_slot_phase,omitempty"`
	RepoSlotStep    string          `json:"repo_slot_step,omitempty"`
	TaskState       core.State      `json:"task_state,omitempty"`
	TaskEvent       core.EventType  `json:"task_event,omitempty"`
	TaskEventDetail string          `json:"task_event_detail,omitempty"`
}

type StreamEventSnapshot struct {
	SchemaVersion string             `json:"schema_version"`
	TaskID        string             `json:"task_id"`
	SessionID     string             `json:"session_id,omitempty"`
	EventCount    int                `json:"event_count"`
	StartedAt     time.Time          `json:"started_at,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at,omitempty"`
	LastEvent     StreamEventKind    `json:"last_event,omitempty"`
	RestartCount  int                `json:"restart_count,omitempty"`
	CurrentStep   string             `json:"current_step,omitempty"`
	Result        string             `json:"result,omitempty"`
	Error         string             `json:"error,omitempty"`
	Usage         core.TokenUsage    `json:"usage"`
	CostUSD       float64            `json:"cost_usd,omitempty"`
	Mapping       StreamStateMapping `json:"mapping"`
}

func ParseStreamJSONL(raw []byte) ([]StreamEvent, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	events := make([]StreamEvent, 0, len(lines))
	for idx, line := range lines {
		lineNo := idx + 1
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		event, err := parseStreamLine(lineNo, line)
		if err != nil {
			return nil, fmt.Errorf("解析 stream-json 第 %d 行失败: %w", lineNo, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func AggregateStreamEvents(taskID string, events []StreamEvent) *StreamEventSnapshot {
	snapshot := &StreamEventSnapshot{
		SchemaVersion: StreamContractSchemaVersion,
		TaskID:        strings.TrimSpace(taskID),
		EventCount:    len(events),
	}
	for _, event := range events {
		applyStreamEvent(snapshot, event)
	}
	if strings.TrimSpace(snapshot.CurrentStep) == "" {
		snapshot.CurrentStep = strings.TrimSpace(snapshot.Mapping.RepoSlotStep)
	}
	return snapshot
}

func MapStreamEvent(event StreamEvent) StreamStateMapping {
	step := normalizeStep(event.Step, "execute")
	detail := strings.TrimSpace(event.Message)
	switch event.Type {
	case StreamEventStarted:
		if detail == "" {
			detail = "stream-json: 执行开始"
		}
		return StreamStateMapping{
			RepoSlotPhase:   StreamSlotPhaseRunning,
			RepoSlotStep:    step,
			TaskState:       core.StateRunning,
			TaskEvent:       core.EventStarted,
			TaskEventDetail: detail,
		}
	case StreamEventProgress:
		if detail == "" {
			detail = "stream-json: 执行进展更新"
		}
		return StreamStateMapping{
			RepoSlotPhase:   StreamSlotPhaseRunning,
			RepoSlotStep:    step,
			TaskState:       core.StateRunning,
			TaskEvent:       core.EventUpdated,
			TaskEventDetail: detail,
		}
	case StreamEventUsage:
		if detail == "" {
			detail = fmt.Sprintf("stream-json: token 用量更新 input=%d output=%d cache_create=%d cache_read=%d", event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CacheCreationInputTokens, event.Usage.CacheReadInputTokens)
		}
		return StreamStateMapping{
			RepoSlotPhase:   StreamSlotPhaseRunning,
			RepoSlotStep:    step,
			TaskState:       core.StateRunning,
			TaskEvent:       core.EventUpdated,
			TaskEventDetail: detail,
		}
	case StreamEventResult:
		if detail == "" {
			detail = strings.TrimSpace(event.Result)
		}
		if detail == "" {
			detail = "stream-json 结果已就绪，进入收口"
		}
		return StreamStateMapping{
			RepoSlotPhase:   StreamSlotPhaseFinalizing,
			RepoSlotStep:    "sync_target",
			TaskState:       core.StateFinalizing,
			TaskEvent:       core.EventUpdated,
			TaskEventDetail: detail,
		}
	case StreamEventError:
		errDetail := strings.TrimSpace(event.Error)
		if errDetail == "" {
			errDetail = detail
		}
		if errDetail == "" {
			errDetail = "stream-json 报告执行失败"
		}
		return StreamStateMapping{
			RepoSlotPhase:   StreamSlotPhasePaneDead,
			RepoSlotStep:    "load_result",
			TaskState:       core.StateFailed,
			TaskEvent:       core.EventFailed,
			TaskEventDetail: errDetail,
		}
	case StreamEventRestarted:
		if detail == "" {
			detail = "stream-json: 执行器已重启"
		}
		return StreamStateMapping{
			RepoSlotPhase:   StreamSlotPhaseRestarting,
			RepoSlotStep:    "restart_claude",
			TaskState:       core.StateRunning,
			TaskEvent:       core.EventUpdated,
			TaskEventDetail: detail,
		}
	case StreamEventSystem:
		subtype := strings.ToLower(strings.TrimSpace(event.Subtype))
		switch subtype {
		case "init":
			if detail == "" {
				detail = "stream-json: Claude 会话初始化完成"
			}
			return StreamStateMapping{
				RepoSlotPhase:   StreamSlotPhaseRunning,
				RepoSlotStep:    "execute",
				TaskState:       core.StateRunning,
				TaskEvent:       core.EventStarted,
				TaskEventDetail: detail,
			}
		case "hook_started":
			if detail == "" {
				detail = "stream-json: Claude hook 开始执行"
			}
			return StreamStateMapping{
				RepoSlotPhase:   StreamSlotPhaseRunning,
				RepoSlotStep:    step,
				TaskState:       core.StateRunning,
				TaskEvent:       core.EventUpdated,
				TaskEventDetail: detail,
			}
		case "hook_response":
			if detail == "" {
				detail = "stream-json: Claude hook 已响应"
			}
			return StreamStateMapping{
				RepoSlotPhase:   StreamSlotPhaseRunning,
				RepoSlotStep:    step,
				TaskState:       core.StateRunning,
				TaskEvent:       core.EventUpdated,
				TaskEventDetail: detail,
			}
		default:
			if detail == "" {
				if subtype == "" {
					detail = "stream-json: 未知系统事件"
				} else {
					detail = fmt.Sprintf("stream-json: 未知系统事件 %s", subtype)
				}
			}
			return StreamStateMapping{
				RepoSlotPhase:   StreamSlotPhaseRunning,
				RepoSlotStep:    step,
				TaskState:       core.StateRunning,
				TaskEvent:       core.EventWarning,
				TaskEventDetail: detail,
			}
		}
	default:
		return StreamStateMapping{}
	}
}

func MarshalStreamEventSnapshot(snapshot *StreamEventSnapshot) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("stream event snapshot 不能为空")
	}
	normalized := *snapshot
	normalized.normalize()
	payload, err := json.MarshalIndent(&normalized, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 stream 事件快照失败: %w", err)
	}
	return append(payload, '\n'), nil
}

func UnmarshalStreamEventSnapshot(raw []byte) (*StreamEventSnapshot, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var snapshot StreamEventSnapshot
	if err := json.Unmarshal(trimmed, &snapshot); err != nil {
		return nil, fmt.Errorf("解析 stream 事件快照失败: %w", err)
	}
	snapshot.normalize()
	return &snapshot, nil
}

func parseStreamLine(lineNo int, raw []byte) (StreamEvent, error) {
	payload := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return StreamEvent{}, err
	}
	kind, err := resolveStreamEventKind(payload)
	if err != nil {
		return StreamEvent{}, err
	}
	timestamp, err := readTimeField(payload, "timestamp", "ts", "created_at")
	if err != nil {
		return StreamEvent{}, err
	}
	usage, err := readUsageField(payload)
	if err != nil {
		return StreamEvent{}, err
	}
	errorText, err := readErrorField(payload)
	if err != nil {
		return StreamEvent{}, err
	}
	cost, err := readFloatField(payload, "total_cost_usd", "cost_usd")
	if err != nil {
		return StreamEvent{}, err
	}
	message := readStringField(payload, "message", "detail", "text")
	resultText := readStringField(payload, "result")
	subtype := strings.ToLower(strings.TrimSpace(readStringField(payload, "subtype")))
	if kind == StreamEventResult && strings.TrimSpace(resultText) == "" {
		resultText = message
	}
	if kind == StreamEventError && strings.TrimSpace(errorText) == "" {
		if strings.HasPrefix(subtype, "error_") {
			errorText = subtype
		}
		if errorText == "" {
			errorText = strings.TrimSpace(resultText)
		}
		if errorText == "" {
			errorText = message
		}
	}
	line := append([]byte(nil), raw...)
	return StreamEvent{
		Seq:       lineNo,
		Type:      kind,
		Subtype:   subtype,
		Timestamp: timestamp,
		SessionID: readStringField(payload, "session_id"),
		Step:      readStringField(payload, "step", "current_step"),
		Message:   message,
		Result:    resultText,
		Error:     errorText,
		Usage:     usage,
		CostUSD:   cost,
		Raw:       line,
	}, nil
}

func resolveStreamEventKind(payload map[string]json.RawMessage) (StreamEventKind, error) {
	rawEvent := strings.ToLower(strings.TrimSpace(readStringField(payload, "event")))
	if rawEvent == "system" {
		return StreamEventSystem, nil
	}
	if kind := normalizeEventKind(rawEvent); kind != "" {
		return kind, nil
	}
	subtype := strings.ToLower(strings.TrimSpace(readStringField(payload, "subtype")))
	rawType := strings.ToLower(strings.TrimSpace(readStringField(payload, "type")))
	if rawType == "system" {
		return StreamEventSystem, nil
	}
	if readBoolField(payload, "is_error") || strings.HasPrefix(subtype, "error") {
		return StreamEventError, nil
	}
	if strings.Contains(subtype, "restart") {
		return StreamEventRestarted, nil
	}
	if kind := normalizeEventKind(rawType); kind != "" {
		return kind, nil
	}
	if hasUsageField(payload) {
		return StreamEventUsage, nil
	}
	if strings.TrimSpace(readStringField(payload, "result")) != "" {
		return StreamEventResult, nil
	}
	if strings.TrimSpace(readStringField(payload, "message", "step", "current_step")) != "" {
		return StreamEventProgress, nil
	}
	return "", fmt.Errorf("无法识别事件类型")
}

func normalizeEventKind(raw string) StreamEventKind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "started", "start", "begin":
		return StreamEventStarted
	case "progress", "update", "message", "assistant":
		return StreamEventProgress
	case "usage", "token", "metrics":
		return StreamEventUsage
	case "result", "done", "completed", "completion":
		return StreamEventResult
	case "error", "failed", "failure":
		return StreamEventError
	case "restarted", "restart":
		return StreamEventRestarted
	default:
		return ""
	}
}

func applyStreamEvent(snapshot *StreamEventSnapshot, event StreamEvent) {
	if snapshot == nil {
		return
	}
	if !event.Timestamp.IsZero() {
		if snapshot.StartedAt.IsZero() {
			snapshot.StartedAt = event.Timestamp
		}
		if snapshot.UpdatedAt.IsZero() || event.Timestamp.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = event.Timestamp
		}
	}
	if event.Type == StreamEventStarted && !event.Timestamp.IsZero() && snapshot.StartedAt.IsZero() {
		snapshot.StartedAt = event.Timestamp
	}
	if sid := strings.TrimSpace(event.SessionID); sid != "" {
		snapshot.SessionID = sid
	}
	snapshot.LastEvent = event.Type
	mergeUsage(&snapshot.Usage, event.Usage)
	if event.CostUSD > 0 {
		snapshot.CostUSD = event.CostUSD
	}
	if result := strings.TrimSpace(event.Result); result != "" {
		snapshot.Result = result
	}
	if errText := strings.TrimSpace(event.Error); errText != "" {
		snapshot.Error = errText
	}
	if event.Type == StreamEventRestarted {
		snapshot.RestartCount++
	}
	mapping := MapStreamEvent(event)
	overrideMapping := mapping.RepoSlotPhase != "" && shouldOverrideStreamMapping(snapshot, event)
	if overrideMapping {
		snapshot.Mapping = mapping
	}
	if overrideMapping {
		if step := strings.TrimSpace(mapping.RepoSlotStep); step != "" {
			snapshot.CurrentStep = step
		} else if step := strings.TrimSpace(event.Step); step != "" {
			snapshot.CurrentStep = step
		}
	} else if !streamSnapshotHasError(snapshot) && !streamSnapshotHasResult(snapshot) {
		if step := strings.TrimSpace(mapping.RepoSlotStep); step != "" {
			snapshot.CurrentStep = step
		} else if step := strings.TrimSpace(event.Step); step != "" {
			snapshot.CurrentStep = step
		}
	}
}

func mergeUsage(dst *core.TokenUsage, src core.TokenUsage) {
	if dst == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
}

func shouldOverrideStreamMapping(snapshot *StreamEventSnapshot, event StreamEvent) bool {
	if snapshot == nil {
		return true
	}
	switch event.Type {
	case StreamEventError:
		return true
	case StreamEventResult:
		return !streamSnapshotHasError(snapshot)
	default:
		if streamSnapshotHasError(snapshot) || streamSnapshotHasResult(snapshot) {
			return false
		}
		return true
	}
}

func streamSnapshotHasResult(snapshot *StreamEventSnapshot) bool {
	if snapshot == nil {
		return false
	}
	if strings.TrimSpace(snapshot.Result) != "" {
		return true
	}
	if snapshot.Mapping.TaskState == core.StateFinalizing {
		return true
	}
	return snapshot.LastEvent == StreamEventResult
}

func normalizeStep(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" {
		return "execute"
	}
	return value
}

func (snapshot *StreamEventSnapshot) normalize() {
	if snapshot == nil {
		return
	}
	if strings.TrimSpace(snapshot.SchemaVersion) == "" {
		snapshot.SchemaVersion = StreamContractSchemaVersion
	}
	snapshot.TaskID = strings.TrimSpace(snapshot.TaskID)
	snapshot.SessionID = strings.TrimSpace(snapshot.SessionID)
	snapshot.CurrentStep = strings.TrimSpace(snapshot.CurrentStep)
	snapshot.Result = strings.TrimSpace(snapshot.Result)
	snapshot.Error = strings.TrimSpace(snapshot.Error)
	snapshot.Mapping.RepoSlotStep = strings.TrimSpace(snapshot.Mapping.RepoSlotStep)
	snapshot.Mapping.TaskEventDetail = strings.TrimSpace(snapshot.Mapping.TaskEventDetail)
}

func readStringField(payload map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || len(raw) == 0 {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func readBoolField(payload map[string]json.RawMessage, key string) bool {
	raw, ok := payload[key]
	if !ok || len(raw) == 0 {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.ToLower(strings.TrimSpace(text))
		return text == "true" || text == "1" || text == "yes"
	}
	return false
}

func hasUsageField(payload map[string]json.RawMessage) bool {
	raw, ok := payload["usage"]
	if !ok {
		return false
	}
	return len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null"
}

func readUsageField(payload map[string]json.RawMessage) (core.TokenUsage, error) {
	raw, ok := payload["usage"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return core.TokenUsage{}, nil
	}
	var usage core.TokenUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return core.TokenUsage{}, fmt.Errorf("解析 usage 失败: %w", err)
	}
	return usage, nil
}

func readTimeField(payload map[string]json.RawMessage, keys ...string) (time.Time, error) {
	text := readStringField(payload, keys...)
	if text == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无效时间戳 %q", text)
}

func readFloatField(payload map[string]json.RawMessage, keys ...string) (float64, error) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			continue
		}
		var value float64
		if err := json.Unmarshal(raw, &value); err == nil {
			return value, nil
		}
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			var parsed float64
			if _, err := fmt.Sscanf(text, "%f", &parsed); err == nil {
				return parsed, nil
			}
		}
		return 0, fmt.Errorf("字段 %s 不是有效数字", key)
	}
	return 0, nil
}

func readErrorField(payload map[string]json.RawMessage) (string, error) {
	raw, ok := payload["error"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil {
		if msg := readStringField(object, "message", "error", "detail", "type"); msg != "" {
			return msg, nil
		}
		compact, marshalErr := json.Marshal(object)
		if marshalErr != nil {
			return "", fmt.Errorf("解析 error 对象失败: %w", marshalErr)
		}
		return string(compact), nil
	}
	return "", fmt.Errorf("解析 error 字段失败")
}
