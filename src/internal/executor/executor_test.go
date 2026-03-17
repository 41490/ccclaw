package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/41490/ccclaw/internal/core"
	"github.com/41490/ccclaw/internal/tmux"
)

type fakeTMuxManager struct {
	spec tmux.SessionSpec
}

func (f *fakeTMuxManager) Launch(spec tmux.SessionSpec) error {
	f.spec = spec
	return nil
}

func (f *fakeTMuxManager) Status(name string) (*tmux.SessionStatus, error) { return nil, nil }
func (f *fakeTMuxManager) CaptureOutput(name string, lines int) (string, error) {
	return "", nil
}
func (f *fakeTMuxManager) Kill(name string) error { return nil }
func (f *fakeTMuxManager) List(prefix string) ([]tmux.SessionStatus, error) {
	return nil, nil
}

func TestExecutorRunParsesJSONResult(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	script := "#!/bin/sh\ncat <<'EOF'\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"sess-1\",\"result\":\"任务完成\",\"total_cost_usd\":0.125,\"usage\":{\"input_tokens\":12,\"output_tokens\":8,\"cache_creation_input_tokens\":3,\"cache_read_input_tokens\":4}}\nEOF\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, err := execEngine.Run(context.Background(), tmpDir, "10#body", RunOptions{Prompt: "test prompt"})
	if err != nil {
		t.Fatalf("执行器运行失败: %v", err)
	}
	if result.SessionID != "sess-1" {
		t.Fatalf("unexpected session id: %q", result.SessionID)
	}
	if result.CostUSD != 0.125 {
		t.Fatalf("unexpected cost: %v", result.CostUSD)
	}
	if result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 8 {
		t.Fatalf("unexpected usage: %#v", result.Usage)
	}
	if result.Output != "任务完成" {
		t.Fatalf("unexpected result output: %q", result.Output)
	}
	if result.PromptFile == "" {
		t.Fatalf("预期写入 prompt 归档，实际为 %#v", result)
	}
}

func TestExecutorRunReturnsStructuredErrorResult(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude-error.sh")
	script := "#!/bin/sh\ncat <<'EOF'\n{\"type\":\"result\",\"subtype\":\"error_max_turns\",\"session_id\":\"sess-error\",\"result\":\"达到上限\",\"is_error\":true,\"total_cost_usd\":0.5,\"usage\":{\"input_tokens\":20,\"output_tokens\":10}}\nEOF\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, runErr := execEngine.Run(context.Background(), tmpDir, "10#body", RunOptions{Prompt: "test prompt", ResumeSessionID: "sess-error"})
	if runErr == nil {
		t.Fatal("预期返回结构化错误")
	}
	if !strings.Contains(runErr.Error(), "error_max_turns") {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if result == nil || result.SessionID != "sess-error" {
		t.Fatalf("预期保留 session_id，实际为 %#v", result)
	}
	if result.CostUSD != 0.5 {
		t.Fatalf("unexpected cost: %v", result.CostUSD)
	}
	if result.PromptFile == "" {
		t.Fatalf("预期返回 prompt 归档路径，实际为 %#v", result)
	}
	payload, err := os.ReadFile(result.ResultFile)
	if err != nil {
		t.Fatalf("读取结构化结果文件失败: %v", err)
	}
	if !strings.Contains(string(payload), `"subtype":"error_max_turns"`) {
		t.Fatalf("预期结构化结果文件保留错误 JSON，实际为 %q", string(payload))
	}
}

func TestExecutorRunForcesPlainClaudeMode(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-wrapper.sh")
	script := "#!/bin/sh\nset -eu\ncat <<'EOF'\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"sess-plain\",\"result\":\"任务完成\",\"total_cost_usd\":0.125,\"usage\":{\"input_tokens\":12,\"output_tokens\":8}}\nEOF\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, err := execEngine.Run(context.Background(), tmpDir, "10#body", RunOptions{Prompt: "test prompt"})
	if err != nil {
		t.Fatalf("执行器运行失败: %v", err)
	}
	if result.RTKEnabled {
		t.Fatalf("expected plain claude mode, got %#v", result)
	}
}

func TestExecutorRunLaunchesTMuxSession(t *testing.T) {
	tmpDir := t.TempDir()
	manager := &fakeTMuxManager{}
	execEngine, err := New([]string{"sh"}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, manager)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, runErr := execEngine.Run(context.Background(), tmpDir, "10#body", RunOptions{Prompt: "test prompt", ResumeSessionID: "sess-9"})
	if runErr != nil {
		t.Fatalf("tmux launch 失败: %v", runErr)
	}
	if result == nil || !result.Pending {
		t.Fatalf("预期返回 pending 结果，实际为 %#v", result)
	}
	if !strings.Contains(manager.spec.Command, "--output-format") || !strings.Contains(manager.spec.Command, "stream-json") {
		t.Fatalf("预期 tmux 命令包含 stream-json 输出，实际为 %q", manager.spec.Command)
	}
	if !strings.Contains(manager.spec.Command, "--resume") || !strings.Contains(manager.spec.Command, "sess-9") {
		t.Fatalf("预期 tmux 命令包含 resume 参数，实际为 %q", manager.spec.Command)
	}
	if manager.spec.Name == "" || !strings.HasPrefix(manager.spec.Name, "ccclaw-") {
		t.Fatalf("unexpected session name: %#v", manager.spec)
	}
	if result.PromptFile == "" || result.MetaFile == "" {
		t.Fatalf("预期返回 prompt/meta 路径，实际为 %#v", result)
	}
	if !strings.Contains(manager.spec.Command, ".diag.txt") || !strings.Contains(manager.spec.Command, ".stdout.txt") || !strings.Contains(manager.spec.Command, "2> >(") {
		t.Fatalf("预期 tmux 命令把 stdout/诊断分别写入暂存文件，实际为 %q", manager.spec.Command)
	}
}

func TestExecutorRunCanFallbackToJSONOutputByConfig(t *testing.T) {
	tmpDir := t.TempDir()
	manager := &fakeTMuxManager{}
	execEngine, err := New(
		[]string{"sh"},
		"",
		time.Minute,
		filepath.Join(tmpDir, "log"),
		filepath.Join(tmpDir, "result"),
		map[string]string{"CCCLAW_OUTPUT_FORMAT": "json"},
		manager,
	)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	_, runErr := execEngine.Run(context.Background(), tmpDir, "11#body", RunOptions{Prompt: "test prompt"})
	if runErr != nil {
		t.Fatalf("tmux launch 失败: %v", runErr)
	}
	if !strings.Contains(manager.spec.Command, "--output-format") || !strings.Contains(manager.spec.Command, "json") {
		t.Fatalf("预期回滚到 json 输出，实际为 %q", manager.spec.Command)
	}
}

func TestExecutorRuntimeEnvDiscoversLocalBins(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	localBin := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("创建 local bin 失败: %v", err)
	}
	for _, name := range []string{"claude", "rtk"} {
		path := filepath.Join(localBin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("写入 fake %s 失败: %v", name, err)
		}
	}
	wrapperPath := filepath.Join(tmpDir, "ccclaude")
	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入 wrapper 失败: %v", err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", "/usr/bin:/bin")

	execEngine, err := New([]string{wrapperPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	env := execEngine.runtimeEnv("", "")
	if got, want := env["CCCLAW_CLAUDE_BIN"], filepath.Join(localBin, "claude"); got != want {
		t.Fatalf("unexpected claude bin: got=%q want=%q", got, want)
	}
	if _, ok := env["CCCLAW_RTK_BIN"]; ok {
		t.Fatalf("did not expect rtk bin in runtime env: %#v", env)
	}
}

func TestExecutorRunMovesInvalidStdoutIntoDiagnosticFile(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude-invalid.sh")
	script := "#!/bin/sh\nprintf 'ccclaude: exec: claude: not found\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, runErr := execEngine.Run(context.Background(), tmpDir, "10#body", RunOptions{Prompt: "test prompt"})
	if runErr == nil {
		t.Fatal("预期返回解析错误")
	}
	if result == nil || !strings.Contains(result.Output, "claude: not found") {
		t.Fatalf("预期回退到诊断输出，实际为 %#v", result)
	}

	artifacts := execEngine.ArtifactPaths("10#body")
	structuredPayload, err := os.ReadFile(artifacts.ResultFile)
	if err != nil {
		t.Fatalf("读取结构化结果文件失败: %v", err)
	}
	if strings.TrimSpace(string(structuredPayload)) != "" {
		t.Fatalf("预期结构化结果文件为空，实际为 %q", string(structuredPayload))
	}
	diagnosticPayload, err := os.ReadFile(artifacts.DiagnosticFile)
	if err != nil {
		t.Fatalf("读取诊断文件失败: %v", err)
	}
	if !strings.Contains(string(diagnosticPayload), "claude: not found") {
		t.Fatalf("预期诊断文件保留原始输出，实际为 %q", string(diagnosticPayload))
	}
}

func TestLoadResultFallsBackToDiagnosticFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "claude", raw: "ccclaude: exec: claude: not found\n", want: "claude: not found"},
		{name: "rtk", raw: "ccclaude: exec: /tmp/rtk: not found\n", want: "/tmp/rtk: not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
			if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatalf("写入脚本失败: %v", err)
			}
			execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
			if err != nil {
				t.Fatalf("创建执行器失败: %v", err)
			}

			artifacts := execEngine.ArtifactPaths("10#body")
			if err := os.WriteFile(artifacts.ResultFile, []byte(tc.raw), 0o644); err != nil {
				t.Fatalf("写入结果文件失败: %v", err)
			}

			result, loadErr := execEngine.LoadResult("10#body")
			if loadErr == nil {
				t.Fatal("预期返回解析错误")
			}
			if result == nil || !strings.Contains(result.Output, tc.want) {
				t.Fatalf("预期保留原始诊断输出，实际为 %#v", result)
			}
			structuredPayload, err := os.ReadFile(artifacts.ResultFile)
			if err != nil {
				t.Fatalf("读取结构化结果文件失败: %v", err)
			}
			if strings.TrimSpace(string(structuredPayload)) != "" {
				t.Fatalf("预期无效结构化结果已被清空，实际为 %q", string(structuredPayload))
			}
			diagnosticPayload, err := os.ReadFile(artifacts.DiagnosticFile)
			if err != nil {
				t.Fatalf("读取诊断文件失败: %v", err)
			}
			if !strings.Contains(string(diagnosticPayload), tc.want) {
				t.Fatalf("预期诊断文件保留输出，实际为 %q", string(diagnosticPayload))
			}
		})
	}
}

func TestLoadResultPromotesTMuxStdoutFileToStructuredResult(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	artifacts := execEngine.ArtifactPaths("10#body")
	rawJSON := `{"type":"result","subtype":"success","session_id":"sess-10","duration_ms":2000,"result":"任务完成","total_cost_usd":0.15,"usage":{"input_tokens":50,"output_tokens":20}}`
	if err := os.WriteFile(artifacts.StdoutFile, []byte(rawJSON), 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存文件失败: %v", err)
	}

	result, loadErr := execEngine.LoadResult("10#body")
	if loadErr != nil {
		t.Fatalf("预期成功提升 stdout 暂存，实际失败: %v", loadErr)
	}
	if result == nil || result.SessionID != "sess-10" || result.Output != "任务完成" {
		t.Fatalf("unexpected result: %#v", result)
	}
	structuredPayload, err := os.ReadFile(artifacts.ResultFile)
	if err != nil {
		t.Fatalf("读取结构化结果文件失败: %v", err)
	}
	if strings.TrimSpace(string(structuredPayload)) != rawJSON {
		t.Fatalf("预期结构化结果文件已提升 stdout JSON，实际为 %q", string(structuredPayload))
	}
	if _, err := os.Stat(artifacts.StdoutFile); !os.IsNotExist(err) {
		t.Fatalf("预期 stdout 暂存文件已清理，实际 err=%v", err)
	}
}

func TestLoadResultMaterializesStreamJSONStdoutArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}
	artifacts := execEngine.ArtifactPaths("46#stream")
	if err := execEngine.writeRunMetadata(artifacts.MetaFile, runMetadata{OutputFormat: outputFormatStreamJSON}); err != nil {
		t.Fatalf("写入元数据失败: %v", err)
	}
	streamRaw, err := os.ReadFile(filepath.Join("testdata", "stream_contract", "success.stream.jsonl"))
	if err != nil {
		t.Fatalf("读取 stream fixture 失败: %v", err)
	}
	if err := os.WriteFile(artifacts.StdoutFile, streamRaw, 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存失败: %v", err)
	}

	result, loadErr := execEngine.LoadResult("46#stream")
	if loadErr != nil {
		t.Fatalf("预期 stream-json 能被兼容解析，实际失败: %v", loadErr)
	}
	if result == nil || !strings.Contains(result.Output, "任务已完成") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(artifacts.StreamFile); err != nil {
		t.Fatalf("预期生成 stream 原始流文件: %v", err)
	}
	if _, err := os.Stat(artifacts.EventFile); err != nil {
		t.Fatalf("预期生成 stream 事件快照文件: %v", err)
	}
}

func TestLoadResultMaterializesClaudeSystemStreamJSONStdoutArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}
	artifacts := execEngine.ArtifactPaths("75#stream-system")
	if err := execEngine.writeRunMetadata(artifacts.MetaFile, runMetadata{OutputFormat: outputFormatStreamJSON}); err != nil {
		t.Fatalf("写入元数据失败: %v", err)
	}
	streamRaw, err := os.ReadFile(filepath.Join("testdata", "stream_contract", "system_success.stream.jsonl"))
	if err != nil {
		t.Fatalf("读取 stream fixture 失败: %v", err)
	}
	if err := os.WriteFile(artifacts.StdoutFile, streamRaw, 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存失败: %v", err)
	}

	result, loadErr := execEngine.LoadResult("75#stream-system")
	if loadErr != nil {
		t.Fatalf("预期 system stream-json 能被兼容解析，实际失败: %v", loadErr)
	}
	if result == nil || !strings.Contains(result.Output, "任务已完成") {
		t.Fatalf("unexpected result: %#v", result)
	}
	snapshot, err := execEngine.LoadStreamEventSnapshot("75#stream-system")
	if err != nil {
		t.Fatalf("读取 stream 快照失败: %v", err)
	}
	if snapshot == nil || snapshot.Mapping.TaskState != core.StateFinalizing {
		t.Fatalf("预期 success result 继续主导快照，实际为 %#v", snapshot)
	}
}

func TestLoadResultMaterializesClaudeConversationStreamJSONStdoutArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}
	artifacts := execEngine.ArtifactPaths("79#stream-conversation")
	if err := execEngine.writeRunMetadata(artifacts.MetaFile, runMetadata{OutputFormat: outputFormatStreamJSON}); err != nil {
		t.Fatalf("写入元数据失败: %v", err)
	}
	streamRaw, err := os.ReadFile(filepath.Join("testdata", "stream_contract", "tool_result_success.stream.jsonl"))
	if err != nil {
		t.Fatalf("读取 stream fixture 失败: %v", err)
	}
	if err := os.WriteFile(artifacts.StdoutFile, streamRaw, 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存失败: %v", err)
	}

	result, loadErr := execEngine.LoadResult("79#stream-conversation")
	if loadErr != nil {
		t.Fatalf("预期 assistant/user 对话帧链路能成功收口，实际失败: %v", loadErr)
	}
	if result == nil || !strings.Contains(result.Output, "任务已完成") {
		t.Fatalf("unexpected result: %#v", result)
	}
	snapshot, err := execEngine.LoadStreamEventSnapshot("79#stream-conversation")
	if err != nil {
		t.Fatalf("读取 stream 快照失败: %v", err)
	}
	if snapshot == nil || snapshot.Mapping.TaskState != core.StateFinalizing {
		t.Fatalf("预期 success result 继续主导快照，实际为 %#v", snapshot)
	}
	if snapshot.Mapping.TaskEventDetail != "任务已完成" {
		t.Fatalf("预期最终详情仍由 success result 主导，实际为 %q", snapshot.Mapping.TaskEventDetail)
	}
}

func TestExecutorRunStreamJSONPrefersSuccessResultOverUnknownFrame(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude-stream-success.sh")
	script := "#!/bin/sh\ncat <<'EOF'\n" +
		"{\"type\":\"system\",\"subtype\":\"init\",\"timestamp\":\"2026-03-17T12:00:00Z\",\"session_id\":\"sess-b\"}\n" +
		"{\"type\":\"debug\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"meta_trace\",\"text\":\"shadow marker\"}]},\"timestamp\":\"2026-03-17T12:00:01Z\",\"session_id\":\"sess-b\"}\n" +
		"{\"type\":\"result\",\"subtype\":\"success\",\"timestamp\":\"2026-03-17T12:00:02Z\",\"session_id\":\"sess-b\",\"result\":\"任务完成\",\"total_cost_usd\":0.11}\n" +
		"EOF\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, runErr := execEngine.Run(context.Background(), tmpDir, "79#plan-b", RunOptions{Prompt: "test prompt"})
	if runErr != nil {
		t.Fatalf("预期 success result 压住未知普通帧，实际失败: %v", runErr)
	}
	if result == nil || result.Output != "任务完成" {
		t.Fatalf("unexpected result: %#v", result)
	}
	snapshot, err := execEngine.LoadStreamEventSnapshot("79#plan-b")
	if err != nil {
		t.Fatalf("读取 stream 快照失败: %v", err)
	}
	if snapshot == nil || snapshot.Mapping.TaskState != core.StateFinalizing {
		t.Fatalf("预期快照已按 success 收口，实际为 %#v", snapshot)
	}
	if snapshot.Result != "任务完成" {
		t.Fatalf("预期保留 success result，实际为 %q", snapshot.Result)
	}
}

func TestExecutorRunStreamJSONFailureCarriesPreciseDecodeContext(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude-stream-fail.sh")
	script := "#!/bin/sh\ncat <<'EOF'\n" +
		"{\"type\":\"system\",\"subtype\":\"init\",\"timestamp\":\"2026-03-17T12:00:00Z\",\"session_id\":\"sess-c\"}\n" +
		"{\"type\":\"debug\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"meta_trace\",\"text\":\"shadow marker\"}]},\"timestamp\":\"2026-03-17T12:00:01Z\",\"session_id\":\"sess-c\"}\n" +
		"EOF\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	result, runErr := execEngine.Run(context.Background(), tmpDir, "79#plan-c", RunOptions{Prompt: "test prompt"})
	if runErr == nil {
		t.Fatal("预期未知普通帧且无 success result 时返回失败")
	}
	for _, want := range []string{"第 2 行", "type=debug", "content[].type=meta_trace"} {
		if !strings.Contains(runErr.Error(), want) {
			t.Fatalf("预期错误信息包含 %q，实际为 %v", want, runErr)
		}
	}
	if result == nil {
		t.Fatal("预期返回带诊断的结果对象")
	}
	for _, want := range []string{"第 2 行", "type=debug", "content[].type=meta_trace"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("预期诊断输出包含 %q，实际为 %q", want, result.Output)
		}
	}
}

func TestLoadResultReturnsStructuredErrorFromStreamJSON(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}
	artifacts := execEngine.ArtifactPaths("47#stream-error")
	if err := execEngine.writeRunMetadata(artifacts.MetaFile, runMetadata{OutputFormat: outputFormatStreamJSON}); err != nil {
		t.Fatalf("写入元数据失败: %v", err)
	}
	streamRaw, err := os.ReadFile(filepath.Join("testdata", "stream_contract", "restart_error.stream.jsonl"))
	if err != nil {
		t.Fatalf("读取 stream fixture 失败: %v", err)
	}
	if err := os.WriteFile(artifacts.StdoutFile, streamRaw, 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存失败: %v", err)
	}

	result, loadErr := execEngine.LoadResult("47#stream-error")
	if loadErr == nil {
		t.Fatal("预期 stream-json 错误事件被识别为失败")
	}
	if !strings.Contains(loadErr.Error(), "error_stream") {
		t.Fatalf("unexpected error: %v", loadErr)
	}
	if result == nil || !strings.Contains(result.Output, "退出码") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadResultMovesInvalidTMuxStdoutIntoDiagnosticFile(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "fake-claude.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("写入脚本失败: %v", err)
	}
	execEngine, err := New([]string{scriptPath}, "", time.Minute, filepath.Join(tmpDir, "log"), filepath.Join(tmpDir, "result"), nil, nil)
	if err != nil {
		t.Fatalf("创建执行器失败: %v", err)
	}

	artifacts := execEngine.ArtifactPaths("10#body")
	if err := os.WriteFile(artifacts.StdoutFile, []byte("ccclaude: exec: claude: not found\n"), 0o644); err != nil {
		t.Fatalf("写入 stdout 暂存文件失败: %v", err)
	}

	result, loadErr := execEngine.LoadResult("10#body")
	if loadErr == nil {
		t.Fatal("预期返回结构化结果缺失错误")
	}
	if result == nil || !strings.Contains(result.Output, "claude: not found") {
		t.Fatalf("预期回退到诊断输出，实际为 %#v", result)
	}
	structuredPayload, err := os.ReadFile(artifacts.ResultFile)
	if err != nil {
		t.Fatalf("读取结构化结果文件失败: %v", err)
	}
	if strings.TrimSpace(string(structuredPayload)) != "" {
		t.Fatalf("预期结构化结果文件保持为空，实际为 %q", string(structuredPayload))
	}
	diagnosticPayload, err := os.ReadFile(artifacts.DiagnosticFile)
	if err != nil {
		t.Fatalf("读取诊断文件失败: %v", err)
	}
	if !strings.Contains(string(diagnosticPayload), "claude: not found") {
		t.Fatalf("预期诊断文件吸收 stdout 暂存，实际为 %q", string(diagnosticPayload))
	}
	if _, err := os.Stat(artifacts.StdoutFile); !os.IsNotExist(err) {
		t.Fatalf("预期 stdout 暂存文件已清理，实际 err=%v", err)
	}
}
