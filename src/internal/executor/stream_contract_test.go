package executor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/core"
)

func TestAggregateStreamEventsWithFixtures(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "stream_contract", "*.stream.jsonl"))
	if err != nil {
		t.Fatalf("读取 fixtures 失败: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("预期存在 stream-json fixtures")
	}
	for _, streamPath := range fixtures {
		name := strings.TrimSuffix(filepath.Base(streamPath), ".stream.jsonl")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(streamPath)
			if err != nil {
				t.Fatalf("读取 fixture 失败: %v", err)
			}
			events, err := ParseStreamJSONL(raw)
			if err != nil {
				t.Fatalf("解析 stream fixture 失败: %v", err)
			}
			snapshot := AggregateStreamEvents(name, events)
			if snapshot == nil || snapshot.EventCount != len(events) {
				t.Fatalf("unexpected snapshot: %#v", snapshot)
			}
			replayed := AggregateStreamEvents(name, events)
			if !reflect.DeepEqual(snapshot, replayed) {
				t.Fatalf("同一批 fixtures 重放结果不一致:\nfirst=%#v\nsecond=%#v", snapshot, replayed)
			}

			wantPayload, err := os.ReadFile(filepath.Join("testdata", "stream_contract", name+".event.json"))
			if err != nil {
				t.Fatalf("读取期望快照失败: %v", err)
			}
			wantSnapshot, err := UnmarshalStreamEventSnapshot(wantPayload)
			if err != nil {
				t.Fatalf("解析期望快照失败: %v", err)
			}
			if !reflect.DeepEqual(snapshot, wantSnapshot) {
				t.Fatalf("聚合快照不符合契约:\nwant=%#v\ngot=%#v", wantSnapshot, snapshot)
			}

			gotPayload, err := MarshalStreamEventSnapshot(snapshot)
			if err != nil {
				t.Fatalf("序列化聚合快照失败: %v", err)
			}
			wantCanonical, err := MarshalStreamEventSnapshot(wantSnapshot)
			if err != nil {
				t.Fatalf("序列化期望快照失败: %v", err)
			}
			if string(gotPayload) != string(wantCanonical) {
				t.Fatalf("聚合快照输出不稳定:\nwant=%s\ngot=%s", string(wantCanonical), string(gotPayload))
			}
		})
	}
}

func TestExecutorLoadStreamEventSnapshotRebuildsFromRaw(t *testing.T) {
	tmpDir := t.TempDir()
	execEngine, err := New([]string{"/bin/sh"}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "stream_contract", "success.stream.jsonl"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	artifacts := execEngine.ArtifactPaths("stream#46")
	if err := os.WriteFile(artifacts.StreamFile, raw, 0o644); err != nil {
		t.Fatalf("写入 stream 原始流失败: %v", err)
	}

	snapshot, err := execEngine.LoadStreamEventSnapshot("stream#46")
	if err != nil {
		t.Fatalf("重建 stream 快照失败: %v", err)
	}
	if snapshot == nil || snapshot.EventCount == 0 || snapshot.Mapping.RepoSlotPhase != StreamSlotPhaseFinalizing {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if _, err := os.Stat(artifacts.EventFile); err != nil {
		t.Fatalf("预期写入事件快照文件: %v", err)
	}

	reloaded, err := execEngine.LoadStreamEventSnapshot("stream#46")
	if err != nil {
		t.Fatalf("读取已缓存 stream 快照失败: %v", err)
	}
	if !reflect.DeepEqual(snapshot, reloaded) {
		t.Fatalf("读取事件快照不一致:\nfirst=%#v\nsecond=%#v", snapshot, reloaded)
	}
}

func TestParseStreamJSONLAcceptsClaudeConversationFrames(t *testing.T) {
	// 复现 #79：Claude 2.1.76 --verbose 输出的 assistant/user 对话帧不能导致任务失败
	raw := []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","timestamp":"2026-03-16T10:00:00Z","session_id":"sess-79"}`,
		`{"type":"system","subtype":"hook_started","timestamp":"2026-03-16T10:00:01Z","session_id":"sess-79","message":"SessionStart hook"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"分析任务"}]},"timestamp":"2026-03-16T10:00:02Z","session_id":"sess-79"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"echo hi"}}]},"timestamp":"2026-03-16T10:00:03Z","session_id":"sess-79"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"准备汇总结果"}]},"timestamp":"2026-03-16T10:00:04Z","session_id":"sess-79"}`,
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"hi","is_error":false}]},"timestamp":"2026-03-16T10:00:05Z","session_id":"sess-79"}`,
		`{"type":"result","subtype":"success","timestamp":"2026-03-16T10:00:06Z","session_id":"sess-79","result":"任务完成"}`,
	}, "\n"))
	events, err := ParseStreamJSONL(raw)
	if err != nil {
		t.Fatalf("预期 assistant/user 对话帧被兼容，实际失败: %v", err)
	}
	if got := events[2].Message; got != "分析任务" {
		t.Fatalf("预期提取 thinking 详情，实际为 %q", got)
	}
	if got := events[3].Message; got != "Claude 调用工具 Bash" {
		t.Fatalf("预期提取 tool_use 详情，实际为 %q", got)
	}
	if got := events[4].Message; got != "准备汇总结果" {
		t.Fatalf("预期提取 text 详情，实际为 %q", got)
	}
	if got := events[5].Message; got != "工具返回结果: hi" {
		t.Fatalf("预期提取 tool_result 详情，实际为 %q", got)
	}
	snapshot := AggregateStreamEvents("79#body", events)
	if snapshot == nil {
		t.Fatal("预期生成 stream 快照")
	}
	if snapshot.Mapping.TaskState != core.StateFinalizing {
		t.Fatalf("预期 success result 主导收口，实际为 %#v", snapshot.Mapping)
	}
	if snapshot.Result != "任务完成" {
		t.Fatalf("预期保留 success result，实际为 %q", snapshot.Result)
	}
}

func TestParseStreamJSONLRejectsUnknownEvent(t *testing.T) {
	_, err := ParseStreamJSONL([]byte(`{"event":"noop","timestamp":"2026-03-13T20:00:00Z"}`))
	if err == nil {
		t.Fatal("预期拒绝未知事件")
	}
	if !strings.Contains(err.Error(), "无法识别事件类型") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseStreamJSONLAcceptsClaudeSystemEvents(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","timestamp":"2026-03-15T15:00:00Z","session_id":"sess-75","message":"初始化"}`,
		`{"type":"system","subtype":"hook_started","timestamp":"2026-03-15T15:00:01Z","session_id":"sess-75","message":"SessionStart hook"}`,
		`{"type":"result","subtype":"success","timestamp":"2026-03-15T15:00:02Z","session_id":"sess-75","result":"任务完成"}`,
		`{"type":"system","subtype":"postflight","timestamp":"2026-03-15T15:00:03Z","session_id":"sess-75","message":"未知 system 事件"}`,
	}, "\n"))
	events, err := ParseStreamJSONL(raw)
	if err != nil {
		t.Fatalf("预期兼容 Claude system 事件，实际失败: %v", err)
	}
	snapshot := AggregateStreamEvents("75#body", events)
	if snapshot == nil {
		t.Fatal("预期生成 stream 快照")
	}
	if snapshot.LastEvent != StreamEventSystem {
		t.Fatalf("预期保留最后一个 system 事件，实际为 %q", snapshot.LastEvent)
	}
	if snapshot.Mapping.TaskState != core.StateFinalizing {
		t.Fatalf("预期 success result 继续主导收口，实际为 %#v", snapshot.Mapping)
	}
	if snapshot.Mapping.TaskEventDetail != "任务完成" {
		t.Fatalf("预期沿用 success result 详情，实际为 %q", snapshot.Mapping.TaskEventDetail)
	}
}
