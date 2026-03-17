package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/core"
)

func (e *Executor) PersistStreamEventSnapshot(taskID string, streamRaw []byte) (*StreamEventSnapshot, error) {
	artifacts := e.ArtifactPaths(taskID)
	if err := writeStreamRawFile(artifacts.StreamFile, streamRaw); err != nil {
		return nil, fmt.Errorf("写入 stream-json 原始流失败: %w", err)
	}
	events, err := ParseStreamJSONL(streamRaw)
	if err != nil {
		return nil, err
	}
	snapshot := AggregateStreamEvents(taskID, events)
	if err := writeStreamEventSnapshotFile(artifacts.EventFile, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (e *Executor) PersistStreamEventSnapshotFromEvents(taskID string, streamRaw []byte, events []StreamEvent) (*StreamEventSnapshot, error) {
	artifacts := e.ArtifactPaths(taskID)
	if err := writeStreamRawFile(artifacts.StreamFile, streamRaw); err != nil {
		return nil, fmt.Errorf("写入 stream-json 原始流失败: %w", err)
	}
	if len(events) == 0 {
		return nil, nil
	}
	snapshot := AggregateStreamEvents(taskID, events)
	if err := writeStreamEventSnapshotFile(artifacts.EventFile, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (e *Executor) LoadStreamEventSnapshot(taskID string) (*StreamEventSnapshot, error) {
	artifacts := e.ArtifactPaths(taskID)
	snapshot, err := readStreamEventSnapshotFile(artifacts.EventFile)
	if err != nil {
		return nil, err
	}
	if snapshot != nil {
		return snapshot, nil
	}
	streamRaw, err := os.ReadFile(artifacts.StreamFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 stream-json 原始流失败: %w", err)
	}
	events, err := ParseStreamJSONL(streamRaw)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	snapshot = AggregateStreamEvents(taskID, events)
	if err := writeStreamEventSnapshotFile(artifacts.EventFile, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func readStreamEventSnapshotFile(path string) (*StreamEventSnapshot, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 stream 事件快照失败: %w", err)
	}
	snapshot, err := UnmarshalStreamEventSnapshot(payload)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func writeStreamEventSnapshotFile(path string, snapshot *StreamEventSnapshot) error {
	payload, err := MarshalStreamEventSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("写入 stream 事件快照失败: %w", err)
	}
	return nil
}

func writeStreamRawFile(path string, streamRaw []byte) error {
	if err := os.WriteFile(path, streamRaw, 0o644); err != nil {
		return err
	}
	return nil
}

func marshalCompatibleResultFromStreamSnapshot(snapshot *StreamEventSnapshot, duration time.Duration) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("stream 事件快照为空，无法生成兼容结果")
	}
	if !streamSnapshotHasResult(snapshot) && !streamSnapshotHasError(snapshot) {
		return nil, fmt.Errorf("stream 事件快照缺少终态 result/error")
	}
	output := strings.TrimSpace(snapshot.Result)
	if output == "" {
		output = strings.TrimSpace(snapshot.Error)
	}
	if output == "" {
		output = strings.TrimSpace(snapshot.Mapping.TaskEventDetail)
	}
	payload := claudeJSONResult{
		Type:         "result",
		Subtype:      "success",
		SessionID:    strings.TrimSpace(snapshot.SessionID),
		Result:       output,
		TotalCostUSD: snapshot.CostUSD,
		Usage:        snapshot.Usage,
	}
	if snapshotStartedAndEnded(snapshot) {
		payload.DurationMS = int64(snapshot.UpdatedAt.Sub(snapshot.StartedAt) / time.Millisecond)
	}
	if payload.DurationMS <= 0 && duration > 0 {
		payload.DurationMS = int64(duration / time.Millisecond)
	}
	if streamSnapshotHasError(snapshot) {
		payload.IsError = true
		payload.Subtype = "error_stream"
		if subtype := strings.TrimSpace(snapshot.Error); strings.HasPrefix(subtype, "error_") {
			payload.Subtype = subtype
		}
		if strings.TrimSpace(payload.Result) == "" {
			payload.Result = "stream-json 报告执行失败"
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化 stream 兼容结果失败: %w", err)
	}
	return append(encoded, '\n'), nil
}

func streamSnapshotHasError(snapshot *StreamEventSnapshot) bool {
	if snapshot == nil {
		return false
	}
	switch snapshot.Mapping.TaskState {
	case core.StateFailed, core.StateDead:
		return true
	}
	if snapshot.LastEvent == StreamEventError {
		return true
	}
	if strings.TrimSpace(snapshot.Error) != "" && strings.TrimSpace(snapshot.Result) == "" {
		return true
	}
	return false
}

func snapshotStartedAndEnded(snapshot *StreamEventSnapshot) bool {
	if snapshot == nil || snapshot.StartedAt.IsZero() || snapshot.UpdatedAt.IsZero() {
		return false
	}
	return snapshot.UpdatedAt.After(snapshot.StartedAt)
}
