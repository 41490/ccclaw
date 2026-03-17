package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/core"
	"github.com/41490/ccclaw/internal/tmux"
)

type Result struct {
	Output          string
	ExitCode        int
	Duration        time.Duration
	LogFile         string
	ResultFile      string
	StreamFile      string
	EventFile       string
	DiagnosticFile  string
	StdoutFile      string
	MetaFile        string
	PromptFile      string
	SessionID       string
	SessionName     string
	CostUSD         float64
	Usage           core.TokenUsage
	RTKEnabled      bool
	ResumeSessionID string
	Pending         bool
}

const (
	outputFormatJSON       = "json"
	outputFormatStreamJSON = "stream-json"
)

type RunOptions struct {
	Prompt          string
	ResumeSessionID string
}

type Executor struct {
	command     []string
	timeout     time.Duration
	logDir      string
	resultDir   string
	promptDir   string
	secrets     map[string]string
	claudeBin   string
	rtkBin      string
	tmuxManager tmux.Manager
}

func New(command []string, binary string, timeout time.Duration, logDir, resultDir string, secrets map[string]string, tmuxManager tmux.Manager) (*Executor, error) {
	resolved := make([]string, 0, len(command))
	for _, item := range command {
		if strings.TrimSpace(item) != "" {
			resolved = append(resolved, item)
		}
	}
	if len(resolved) == 0 {
		if binary == "" {
			binary = "claude"
		}
		resolved = []string{binary}
	}
	launcher, err := ResolveBinaryPath(resolved[0])
	if err != nil {
		return nil, fmt.Errorf("找不到执行器命令 %q: %w", resolved[0], err)
	}
	resolved[0] = launcher
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建结果目录失败: %w", err)
	}
	promptDir := filepath.Join(filepath.Dir(resultDir), "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 prompt 归档目录失败: %w", err)
	}
	return &Executor{
		command:     resolved,
		timeout:     timeout,
		logDir:      logDir,
		resultDir:   resultDir,
		promptDir:   promptDir,
		secrets:     copyMap(secrets),
		claudeBin:   configuredOrDiscoveredBinary(secrets, "CCCLAW_CLAUDE_BIN", "claude"),
		rtkBin:      configuredOrDiscoveredBinary(secrets, "CCCLAW_RTK_BIN", "rtk"),
		tmuxManager: tmuxManager,
	}, nil
}

func (e *Executor) Run(parent context.Context, repoPath, taskID string, opts RunOptions) (*Result, error) {
	if e.tmuxManager != nil {
		return e.launchTMux(repoPath, taskID, opts)
	}
	return e.runSync(parent, repoPath, taskID, opts)
}

func (e *Executor) ArtifactPaths(taskID string) Result {
	safe := safeTaskID(taskID)
	return Result{
		SessionName:    sessionName(taskID),
		LogFile:        filepath.Join(e.logDir, safe+".log"),
		ResultFile:     filepath.Join(e.resultDir, safe+".json"),
		StreamFile:     filepath.Join(e.resultDir, safe+".stream.jsonl"),
		EventFile:      filepath.Join(e.resultDir, safe+".event.json"),
		DiagnosticFile: filepath.Join(e.resultDir, safe+".diag.txt"),
		StdoutFile:     filepath.Join(e.resultDir, safe+".stdout.txt"),
		MetaFile:       filepath.Join(e.resultDir, safe+".meta.json"),
	}
}

func (e *Executor) LoadResult(taskID string) (*Result, error) {
	artifacts := e.ArtifactPaths(taskID)
	meta, metaErr := e.loadRunMetadata(artifacts.MetaFile)
	if metaErr != nil {
		return nil, metaErr
	}
	resultPayload, err := os.ReadFile(artifacts.ResultFile)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取结构化结果文件失败: %w", err)
	}
	diagnosticPayload, diagnosticErr := os.ReadFile(artifacts.DiagnosticFile)
	if diagnosticErr != nil && !os.IsNotExist(diagnosticErr) {
		return nil, fmt.Errorf("读取诊断文件失败: %w", diagnosticErr)
	}
	stdoutPayload, stdoutErr := os.ReadFile(artifacts.StdoutFile)
	if stdoutErr != nil && !os.IsNotExist(stdoutErr) {
		return nil, fmt.Errorf("读取 stdout 暂存文件失败: %w", stdoutErr)
	}
	if normalizeOutputFormat(meta.OutputFormat) == outputFormatStreamJSON {
		streamResultPayload, streamErr := e.materializeTMuxStreamArtifacts(taskID, artifacts, stdoutPayload)
		if streamErr != nil {
			return nil, streamErr
		}
		if len(bytes.TrimSpace(streamResultPayload)) > 0 {
			resultPayload = streamResultPayload
			stdoutPayload = nil
		}
	}
	resultPayload, diagnosticPayload, materializeErr := e.materializeTMuxStdoutArtifact(artifacts, resultPayload, diagnosticPayload, stdoutPayload)
	if materializeErr != nil {
		return nil, materializeErr
	}
	result, parseErr := parseClaudeResult(resultPayload, 0, artifacts.LogFile, artifacts.ResultFile)
	if result != nil {
		e.applyArtifactMetadata(result, artifacts, meta)
	}
	if parseErr == nil {
		return result, nil
	}
	if hasStructuredClaudePayload(resultPayload) {
		if result != nil {
			return result, parseErr
		}
		return nil, parseErr
	}
	diagnosticPayload, sanitizeErr := e.normalizeDiagnosticArtifacts(artifacts, resultPayload, diagnosticPayload, parseErr)
	if sanitizeErr != nil {
		return nil, sanitizeErr
	}
	fallback := fallbackResultFromRaw(diagnosticPayload, artifacts.LogFile, artifacts.ResultFile, artifacts.DiagnosticFile)
	if fallback != nil {
		e.applyArtifactMetadata(fallback, artifacts, meta)
		return fallback, wrapStructuredResultError(resultPayload, parseErr, true)
	}
	if result != nil {
		return result, wrapStructuredResultError(resultPayload, parseErr, false)
	}
	return nil, wrapStructuredResultError(resultPayload, parseErr, false)
}

func (e *Executor) Timeout() time.Duration {
	return e.timeout
}

func (e *Executor) TMux() tmux.Manager {
	return e.tmuxManager
}

func (e *Executor) launchTMux(repoPath, taskID string, opts RunOptions) (*Result, error) {
	artifacts, meta, err := e.prepareArtifacts(taskID, opts)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(artifacts.LogFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧日志失败: %w", err)
	}
	if err := os.Remove(artifacts.ResultFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧结果失败: %w", err)
	}
	if err := os.Remove(artifacts.StreamFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧流式事件失败: %w", err)
	}
	if err := os.Remove(artifacts.EventFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧事件快照失败: %w", err)
	}
	if err := os.Remove(artifacts.DiagnosticFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧诊断失败: %w", err)
	}
	if err := os.Remove(artifacts.StdoutFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧 stdout 暂存失败: %w", err)
	}
	if err := os.Remove(artifacts.MetaFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧元数据失败: %w", err)
	}
	if err := e.initializeArtifactFiles(artifacts); err != nil {
		return nil, err
	}
	if err := e.writeRunMetadata(artifacts.MetaFile, meta); err != nil {
		return nil, err
	}
	launchCommand := e.buildTMuxCommand(taskID, opts, artifacts.StdoutFile, artifacts.DiagnosticFile, artifacts.LogFile)
	if err := e.tmuxManager.Launch(tmux.SessionSpec{
		Name:    artifacts.SessionName,
		WorkDir: repoPath,
		Command: launchCommand,
		LogFile: artifacts.LogFile,
	}); err != nil {
		return nil, fmt.Errorf("启动 tmux 会话失败: %w", err)
	}
	artifacts.Pending = true
	return &artifacts, nil
}

func (e *Executor) buildTMuxCommand(taskID string, opts RunOptions, stdoutFile, diagnosticFile, logFile string) string {
	commandLine := shellJoin(e.commandWithEnv(taskID, opts, ""))
	script := fmt.Sprintf(
		"set -o pipefail; %s 1> >(tee %s >> %s) 2> >(tee %s >> %s >&2)",
		commandLine,
		shellQuote(stdoutFile),
		shellQuote(logFile),
		shellQuote(diagnosticFile),
		shellQuote(logFile),
	)
	return fmt.Sprintf("bash -lc %s", shellQuote(script))
}

func (e *Executor) runSync(parent context.Context, repoPath, taskID string, opts RunOptions) (*Result, error) {
	ctx, cancel := context.WithTimeout(parent, e.timeout)
	defer cancel()

	artifacts, meta, err := e.prepareArtifacts(taskID, opts)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(artifacts.LogFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧日志失败: %w", err)
	}
	if err := os.Remove(artifacts.ResultFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧结果失败: %w", err)
	}
	if err := os.Remove(artifacts.StreamFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧流式事件失败: %w", err)
	}
	if err := os.Remove(artifacts.EventFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧事件快照失败: %w", err)
	}
	if err := os.Remove(artifacts.DiagnosticFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧诊断失败: %w", err)
	}
	if err := os.Remove(artifacts.StdoutFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧 stdout 暂存失败: %w", err)
	}
	if err := os.Remove(artifacts.MetaFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧元数据失败: %w", err)
	}
	if err := e.initializeArtifactFiles(artifacts); err != nil {
		return nil, err
	}
	if err := e.writeRunMetadata(artifacts.MetaFile, meta); err != nil {
		return nil, err
	}
	if normalizeOutputFormat(meta.OutputFormat) == outputFormatStreamJSON {
		return e.runSyncStreamJSON(ctx, repoPath, taskID, opts, artifacts, meta)
	}

	logHandle, err := os.Create(artifacts.LogFile)
	if err != nil {
		return nil, fmt.Errorf("创建日志文件失败: %w", err)
	}
	defer logHandle.Close()

	diagnosticHandle, err := os.OpenFile(artifacts.DiagnosticFile, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开诊断文件失败: %w", err)
	}
	defer diagnosticHandle.Close()

	args := e.commandArgs(opts)
	cmd := exec.CommandContext(ctx, e.command[0], args...)
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Environ(), mapToEnv(e.runtimeEnv(taskID, meta.RTKMarkerFile))...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(logHandle, &stdout)
	cmd.Stderr = io.MultiWriter(logHandle, diagnosticHandle, &stderr)

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)
	exitCode := 0
	resultPayload := stdout.Bytes()
	var (
		snapshot  *StreamEventSnapshot
		streamErr error
	)
	if normalizeOutputFormat(meta.OutputFormat) == outputFormatStreamJSON {
		snapshot, streamErr = e.PersistStreamEventSnapshot(taskID, stdout.Bytes())
	}
	result, parseErr := parseClaudeResult(resultPayload, duration, artifacts.LogFile, artifacts.ResultFile)
	if parseErr != nil && normalizeOutputFormat(meta.OutputFormat) == outputFormatStreamJSON && !hasStructuredClaudePayload(resultPayload) {
		switch {
		case snapshot == nil && streamErr != nil:
			parseErr = fmt.Errorf("解析 stream-json 输出失败: %w", streamErr)
		case snapshot != nil:
			compatPayload, compatErr := marshalCompatibleResultFromStreamSnapshot(snapshot, duration)
			if compatErr != nil {
				parseErr = fmt.Errorf("解析 stream-json 输出失败: %w", compatErr)
			} else {
				resultPayload = compatPayload
				result, parseErr = parseClaudeResult(resultPayload, duration, artifacts.LogFile, artifacts.ResultFile)
			}
		}
	}
	if persistErr := e.persistStructuredArtifacts(artifacts, resultPayload, stdout.Bytes(), stderr.Bytes(), parseErr); persistErr != nil {
		return nil, persistErr
	}
	if result != nil {
		e.applyArtifactMetadata(result, artifacts, meta)
	}
	if runErr != nil {
		exitCode = 1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if result == nil {
			result = &Result{Output: strings.TrimSpace(string(joinDiagnosticPayloads(stdout.Bytes(), stderr.Bytes()))), Duration: duration}
			e.applyArtifactMetadata(result, artifacts, meta)
		}
		result.ExitCode = exitCode
		if parseErr != nil {
			return result, fmt.Errorf("执行器运行失败: %w；结果校验失败: %v", runErr, parseErr)
		}
		return result, fmt.Errorf("执行器运行失败: %w", runErr)
	}
	if parseErr != nil {
		if result != nil {
			result.ExitCode = exitCode
			return result, parseErr
		}
		fallback := &Result{
			Output:   strings.TrimSpace(string(joinDiagnosticPayloads(stdout.Bytes(), stderr.Bytes()))),
			ExitCode: exitCode,
			Duration: duration,
		}
		e.applyArtifactMetadata(fallback, artifacts, meta)
		return fallback, fmt.Errorf("解析 Claude JSON 输出失败: %w", parseErr)
	}
	result.ExitCode = exitCode
	return result, nil
}

func (e *Executor) runSyncStreamJSON(ctx context.Context, repoPath, taskID string, opts RunOptions, artifacts Result, meta runMetadata) (*Result, error) {
	logHandle, err := os.Create(artifacts.LogFile)
	if err != nil {
		return nil, fmt.Errorf("创建日志文件失败: %w", err)
	}
	defer logHandle.Close()

	diagnosticHandle, err := os.OpenFile(artifacts.DiagnosticFile, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开诊断文件失败: %w", err)
	}
	defer diagnosticHandle.Close()

	args := e.commandArgs(opts)
	cmd := exec.CommandContext(ctx, e.command[0], args...)
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Environ(), mapToEnv(e.runtimeEnv(taskID, meta.RTKMarkerFile))...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stderr 管道失败: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动执行器失败: %w", err)
	}

	var stderr bytes.Buffer
	stderrDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(io.MultiWriter(logHandle, diagnosticHandle, &stderr), stderrPipe)
		stderrDone <- copyErr
	}()

	var rawStream bytes.Buffer
	events := make([]StreamEvent, 0, 32)
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var decodeErr error
	lineNo := 0
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lineNo++
		rawStream.Write(line)
		rawStream.WriteByte('\n')
		_, _ = logHandle.Write(line)
		_, _ = logHandle.Write([]byte{'\n'})
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		event, err := parseStreamLine(lineNo, line)
		if err != nil {
			if decodeErr == nil {
				decodeErr = fmt.Errorf("解析 stream-json 第 %d 行失败: %w", lineNo, err)
			}
			continue
		}
		events = append(events, event)
	}
	if scanErr := scanner.Err(); scanErr != nil && decodeErr == nil {
		decodeErr = scanErr
	}
	if stderrErr := <-stderrDone; stderrErr != nil && decodeErr == nil {
		decodeErr = stderrErr
	}
	runErr := cmd.Wait()
	duration := time.Since(start)
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	streamRaw := rawStream.Bytes()
	partialSnapshot := AggregateStreamEvents(taskID, events)
	preferStreamResult := streamResultShouldWin(partialSnapshot)
	var (
		snapshot  *StreamEventSnapshot
		streamErr error
	)
	if preferStreamResult {
		snapshot, streamErr = e.PersistStreamEventSnapshotFromEvents(taskID, streamRaw, events)
	} else {
		snapshot, streamErr = e.PersistStreamEventSnapshot(taskID, streamRaw)
	}
	resultPayload, compatErr := marshalCompatibleResultFromStreamSnapshot(snapshot, duration)
	if compatErr != nil {
		resultPayload = streamRaw
	}

	result, parseErr := parseClaudeResult(resultPayload, duration, artifacts.LogFile, artifacts.ResultFile)
	if parseErr != nil {
		if decodeErr != nil && !streamResultShouldWin(snapshot) {
			parseErr = fmt.Errorf("stream-json 解码失败: %w", decodeErr)
		} else if streamErr != nil {
			parseErr = fmt.Errorf("stream-json 聚合失败: %w", streamErr)
		} else if compatErr != nil {
			parseErr = fmt.Errorf("stream-json 兼容结果转换失败: %w", compatErr)
		}
	}
	if persistErr := e.persistStructuredArtifacts(artifacts, resultPayload, streamRaw, stderr.Bytes(), parseErr); persistErr != nil {
		return nil, persistErr
	}
	if result != nil {
		e.applyArtifactMetadata(result, artifacts, meta)
	}
	if runErr != nil {
		if result == nil {
			output := strings.TrimSpace(string(joinDiagnosticPayloads(streamRaw, stderr.Bytes())))
			if shouldUseStructuredDecodeDiagnostic(decodeErr) && !streamResultShouldWin(snapshot) {
				output = decodeErr.Error()
			}
			result = &Result{Output: output, Duration: duration}
			e.applyArtifactMetadata(result, artifacts, meta)
		}
		result.ExitCode = exitCode
		if streamResultShouldWin(snapshot) {
			return result, nil
		}
		if parseErr != nil {
			return result, fmt.Errorf("执行器运行失败: %w；结果校验失败: %v", runErr, parseErr)
		}
		return result, fmt.Errorf("执行器运行失败: %w", runErr)
	}
	if parseErr != nil {
		if result != nil {
			if shouldUseStructuredDecodeDiagnostic(decodeErr) && !streamResultShouldWin(snapshot) && strings.TrimSpace(result.Output) == "" {
				result.Output = decodeErr.Error()
			}
			result.ExitCode = exitCode
			return result, parseErr
		}
		output := strings.TrimSpace(string(joinDiagnosticPayloads(streamRaw, stderr.Bytes())))
		if shouldUseStructuredDecodeDiagnostic(decodeErr) && !streamResultShouldWin(snapshot) {
			output = decodeErr.Error()
		}
		fallback := &Result{
			Output:   output,
			ExitCode: exitCode,
			Duration: duration,
		}
		e.applyArtifactMetadata(fallback, artifacts, meta)
		return fallback, fmt.Errorf("解析 Claude JSON 输出失败: %w", parseErr)
	}
	result.ExitCode = exitCode
	return result, nil
}

func streamResultShouldWin(snapshot *StreamEventSnapshot) bool {
	if snapshot == nil {
		return false
	}
	return streamSnapshotHasResult(snapshot) && !streamSnapshotHasError(snapshot)
}

func shouldUseStructuredDecodeDiagnostic(err error) bool {
	if err == nil {
		return false
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return false
	}
	for _, marker := range []string{"type=", "subtype=", "content[].type=", "event="} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type runMetadata struct {
	PromptFile      string `json:"prompt_file"`
	RTKEnabled      bool   `json:"rtk_enabled"`
	RTKMarkerFile   string `json:"rtk_marker_file,omitempty"`
	ResumeSessionID string `json:"resume_session_id,omitempty"`
	OutputFormat    string `json:"output_format,omitempty"`
}

type claudeJSONResult struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	SessionID    string          `json:"session_id"`
	IsError      bool            `json:"is_error"`
	DurationMS   int64           `json:"duration_ms"`
	Result       string          `json:"result"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	Usage        core.TokenUsage `json:"usage"`
}

func parseClaudeResult(raw []byte, duration time.Duration, logFile, resultFile string) (*Result, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("stdout 为空")
	}
	var payload claudeJSONResult
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, err
	}
	result := &Result{
		Output:     payload.Result,
		Duration:   duration,
		LogFile:    logFile,
		ResultFile: resultFile,
		SessionID:  payload.SessionID,
		CostUSD:    payload.TotalCostUSD,
		Usage:      payload.Usage,
	}
	if payload.DurationMS > 0 {
		result.Duration = time.Duration(payload.DurationMS) * time.Millisecond
	}
	if payload.IsError || strings.HasPrefix(payload.Subtype, "error_") {
		if payload.Subtype == "" {
			return result, fmt.Errorf("Claude 返回错误结果")
		}
		return result, fmt.Errorf("Claude 返回错误结果: %s", payload.Subtype)
	}
	if payload.Type != "" && payload.Type != "result" {
		return result, fmt.Errorf("Claude 返回了非 result 类型: %s", payload.Type)
	}
	if result.SessionID == "" && result.Output == "" && result.CostUSD == 0 {
		return result, fmt.Errorf("Claude JSON 输出缺少有效结果")
	}
	return result, nil
}

func SessionName(taskID string) string {
	return sessionName(taskID)
}

func sessionName(taskID string) string {
	checksum := 0
	for idx, r := range taskID {
		checksum += (idx + 1) * int(r)
	}
	return fmt.Sprintf("ccclaw-%s-%04x", safeTaskID(taskID), checksum&0xffff)
}

func (e *Executor) prepareArtifacts(taskID string, opts RunOptions) (Result, runMetadata, error) {
	artifacts := e.ArtifactPaths(taskID)
	promptFile, err := e.archivePrompt(taskID, opts)
	if err != nil {
		return Result{}, runMetadata{}, err
	}
	meta := runMetadata{
		PromptFile:      promptFile,
		RTKEnabled:      false,
		ResumeSessionID: opts.ResumeSessionID,
		OutputFormat:    e.outputFormat(),
	}
	e.applyArtifactMetadata(&artifacts, artifacts, meta)
	return artifacts, meta, nil
}

func (e *Executor) archivePrompt(taskID string, opts RunOptions) (string, error) {
	if err := os.MkdirAll(e.promptDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 prompt 归档目录失败: %w", err)
	}
	ts := time.Now().Format("20060102_150405")
	path := filepath.Join(e.promptDir, fmt.Sprintf("%s_%s.md", safeTaskID(taskID), ts))
	var sb strings.Builder
	sb.WriteString("# ccclaw prompt archive\n\n")
	sb.WriteString(fmt.Sprintf("- task_id: %s\n", taskID))
	sb.WriteString(fmt.Sprintf("- archived_at: %s\n", time.Now().Format(time.RFC3339)))
	if strings.TrimSpace(opts.ResumeSessionID) != "" {
		sb.WriteString(fmt.Sprintf("- resume_session_id: %s\n", opts.ResumeSessionID))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(opts.Prompt)
	sb.WriteString("\n")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("写入 prompt 归档失败: %w", err)
	}
	return path, nil
}

func (e *Executor) writeRunMetadata(path string, meta runMetadata) error {
	payload, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化执行元数据失败: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("写入执行元数据失败: %w", err)
	}
	return nil
}

func (e *Executor) loadRunMetadata(path string) (runMetadata, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return runMetadata{}, nil
		}
		return runMetadata{}, fmt.Errorf("读取执行元数据失败: %w", err)
	}
	var meta runMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		return runMetadata{}, fmt.Errorf("解析执行元数据失败: %w", err)
	}
	return meta, nil
}

func (e *Executor) applyArtifactMetadata(result *Result, artifacts Result, meta runMetadata) {
	if result == nil {
		return
	}
	result.SessionName = artifacts.SessionName
	result.LogFile = artifacts.LogFile
	result.ResultFile = artifacts.ResultFile
	result.StreamFile = artifacts.StreamFile
	result.EventFile = artifacts.EventFile
	result.DiagnosticFile = artifacts.DiagnosticFile
	result.StdoutFile = artifacts.StdoutFile
	result.MetaFile = artifacts.MetaFile
	result.PromptFile = meta.PromptFile
	result.RTKEnabled = e.resolveRTKEnabled(meta)
	result.ResumeSessionID = meta.ResumeSessionID
}

func (e *Executor) commandArgs(opts RunOptions) []string {
	args := append([]string{}, e.command[1:]...)
	if strings.TrimSpace(opts.ResumeSessionID) != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args,
		"-p", opts.Prompt,
		"--dangerously-skip-permissions",
		"--output-format", e.outputFormat(),
	)
	// Claude Code CLI 2.1.76+ 要求 --print 模式下 stream-json 必须附带 --verbose
	if e.outputFormat() == outputFormatStreamJSON {
		args = append(args, "--verbose")
	}
	return args
}

func (e *Executor) outputFormat() string {
	if e == nil {
		return outputFormatStreamJSON
	}
	if format := normalizeOutputFormat(e.secrets["CCCLAW_OUTPUT_FORMAT"]); format != "" {
		return format
	}
	return outputFormatStreamJSON
}

func normalizeOutputFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case outputFormatJSON:
		return outputFormatJSON
	case outputFormatStreamJSON:
		return outputFormatStreamJSON
	default:
		return ""
	}
}

func (e *Executor) commandWithEnv(taskID string, opts RunOptions, rtkMarkerFile string) []string {
	commandLine := append([]string{}, e.command...)
	envMap := e.runtimeEnv(taskID, rtkMarkerFile)
	if len(envMap) == 0 {
		return append(commandLine, e.commandArgs(opts)...)
	}
	envArgs := []string{"env"}
	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		envArgs = append(envArgs, key+"="+envMap[key])
	}
	envArgs = append(envArgs, commandLine...)
	envArgs = append(envArgs, e.commandArgs(opts)...)
	return envArgs
}

func (e *Executor) runtimeEnv(taskID, _ string) map[string]string {
	envMap := copyMap(e.secrets)
	if strings.TrimSpace(envMap["CCCLAW_CLAUDE_BIN"]) == "" && strings.TrimSpace(e.claudeBin) != "" {
		envMap["CCCLAW_CLAUDE_BIN"] = e.claudeBin
	}
	appDir := filepath.Dir(filepath.Dir(e.resultDir))
	if strings.TrimSpace(appDir) != "" {
		envMap["CCCLAW_APP_DIR"] = appDir
		envMap["CCCLAW_HOOK_STATE_DIR"] = filepath.Join(filepath.Dir(e.resultDir), "claude-hooks")
	}
	if strings.TrimSpace(taskID) != "" {
		envMap["CCCLAW_TASK_ID"] = taskID
	}
	return envMap
}

func (e *Executor) resolveRTKEnabled(meta runMetadata) bool {
	return false
}

func (e *Executor) inferRTKEnabled() bool {
	return false
}

func fallbackResultFromRaw(raw []byte, logFile, resultFile, diagnosticFile string) *Result {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil
	}
	return &Result{
		Output:         text,
		LogFile:        logFile,
		ResultFile:     resultFile,
		DiagnosticFile: diagnosticFile,
	}
}

func (e *Executor) initializeArtifactFiles(artifacts Result) error {
	for _, item := range []struct {
		path string
		name string
	}{
		{path: artifacts.ResultFile, name: "结构化结果文件"},
		{path: artifacts.DiagnosticFile, name: "诊断文件"},
		{path: artifacts.StdoutFile, name: "stdout 暂存文件"},
	} {
		if err := os.WriteFile(item.path, nil, 0o644); err != nil {
			return fmt.Errorf("初始化%s失败: %w", item.name, err)
		}
	}
	return nil
}

func (e *Executor) persistStructuredArtifacts(artifacts Result, resultPayload, stdoutPayload, stderrPayload []byte, parseErr error) error {
	if parseErr == nil || hasStructuredClaudePayload(resultPayload) {
		if err := os.WriteFile(artifacts.ResultFile, resultPayload, 0o644); err != nil {
			return fmt.Errorf("写入结构化结果文件失败: %w", err)
		}
		if err := os.WriteFile(artifacts.DiagnosticFile, stderrPayload, 0o644); err != nil {
			return fmt.Errorf("写入诊断文件失败: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(artifacts.ResultFile, nil, 0o644); err != nil {
		return fmt.Errorf("清空结构化结果文件失败: %w", err)
	}
	if err := os.WriteFile(artifacts.DiagnosticFile, joinDiagnosticPayloads(stdoutPayload, stderrPayload), 0o644); err != nil {
		return fmt.Errorf("写入诊断文件失败: %w", err)
	}
	return nil
}

func (e *Executor) normalizeDiagnosticArtifacts(artifacts Result, resultPayload, diagnosticPayload []byte, parseErr error) ([]byte, error) {
	trimmedResult := bytes.TrimSpace(resultPayload)
	trimmedDiagnostic := bytes.TrimSpace(diagnosticPayload)
	if len(trimmedResult) == 0 {
		return diagnosticPayload, nil
	}
	if parseErr == nil {
		return diagnosticPayload, nil
	}
	if hasStructuredClaudePayload(resultPayload) {
		return diagnosticPayload, nil
	}
	merged := joinDiagnosticPayloads(resultPayload, diagnosticPayload)
	if !bytes.Equal(bytes.TrimSpace(merged), trimmedDiagnostic) {
		if err := os.WriteFile(artifacts.DiagnosticFile, merged, 0o644); err != nil {
			return nil, fmt.Errorf("回写诊断文件失败: %w", err)
		}
	}
	if err := os.WriteFile(artifacts.ResultFile, nil, 0o644); err != nil {
		return nil, fmt.Errorf("清空无效结构化结果文件失败: %w", err)
	}
	return merged, nil
}

func (e *Executor) materializeTMuxStdoutArtifact(artifacts Result, resultPayload, diagnosticPayload, stdoutPayload []byte) ([]byte, []byte, error) {
	trimmedStdout := bytes.TrimSpace(stdoutPayload)
	if len(trimmedStdout) == 0 {
		return resultPayload, diagnosticPayload, nil
	}
	trimmedResult := bytes.TrimSpace(resultPayload)
	if len(trimmedResult) > 0 {
		if err := removeIfExists(artifacts.StdoutFile); err != nil {
			return nil, nil, fmt.Errorf("清理已消费 stdout 暂存文件失败: %w", err)
		}
		return resultPayload, diagnosticPayload, nil
	}
	if hasStructuredClaudePayload(stdoutPayload) {
		if err := os.WriteFile(artifacts.ResultFile, stdoutPayload, 0o644); err != nil {
			return nil, nil, fmt.Errorf("提升 stdout 暂存为结构化结果失败: %w", err)
		}
		if err := removeIfExists(artifacts.StdoutFile); err != nil {
			return nil, nil, fmt.Errorf("清理已提升 stdout 暂存文件失败: %w", err)
		}
		return stdoutPayload, diagnosticPayload, nil
	}
	merged := joinDiagnosticPayloads(stdoutPayload, diagnosticPayload)
	if err := os.WriteFile(artifacts.DiagnosticFile, merged, 0o644); err != nil {
		return nil, nil, fmt.Errorf("迁移 stdout 暂存到诊断文件失败: %w", err)
	}
	if err := os.WriteFile(artifacts.ResultFile, nil, 0o644); err != nil {
		return nil, nil, fmt.Errorf("清空结构化结果文件失败: %w", err)
	}
	if err := removeIfExists(artifacts.StdoutFile); err != nil {
		return nil, nil, fmt.Errorf("清理无效 stdout 暂存文件失败: %w", err)
	}
	return nil, merged, nil
}

func (e *Executor) materializeTMuxStreamArtifacts(taskID string, artifacts Result, stdoutPayload []byte) ([]byte, error) {
	if len(bytes.TrimSpace(stdoutPayload)) == 0 {
		return nil, nil
	}
	snapshot, err := e.PersistStreamEventSnapshot(taskID, stdoutPayload)
	if err != nil {
		return nil, fmt.Errorf("解析 tmux stream-json 暂存失败: %w", err)
	}
	compatPayload, err := marshalCompatibleResultFromStreamSnapshot(snapshot, 0)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(artifacts.ResultFile, compatPayload, 0o644); err != nil {
		return nil, fmt.Errorf("写入 stream-json 兼容结果失败: %w", err)
	}
	if err := removeIfExists(artifacts.StdoutFile); err != nil {
		return nil, fmt.Errorf("清理已消费 stdout 暂存文件失败: %w", err)
	}
	return compatPayload, nil
}

func wrapStructuredResultError(resultPayload []byte, parseErr error, hasDiagnostic bool) error {
	trimmed := bytes.TrimSpace(resultPayload)
	if len(trimmed) == 0 {
		if hasDiagnostic {
			return fmt.Errorf("结构化结果缺失，已回退诊断文件")
		}
		return fmt.Errorf("结构化结果缺失")
	}
	if parseErr == nil {
		if hasDiagnostic {
			return fmt.Errorf("结构化结果缺失，已回退诊断文件")
		}
		return fmt.Errorf("结构化结果缺失")
	}
	if hasDiagnostic {
		return fmt.Errorf("结构化结果解析失败: %w；已回退诊断文件", parseErr)
	}
	return fmt.Errorf("结构化结果解析失败: %w", parseErr)
}

func joinDiagnosticPayloads(chunks ...[]byte) []byte {
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		text := strings.TrimSpace(string(chunk))
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return nil
	}
	return []byte(strings.Join(parts, "\n"))
}

func hasStructuredClaudePayload(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	var payload claudeJSONResult
	return json.Unmarshal(trimmed, &payload) == nil
}

func removeIfExists(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func configuredOrDiscoveredBinary(secrets map[string]string, envKey, name string) string {
	if override := strings.TrimSpace(expandUserPath(secrets[envKey])); override != "" {
		return override
	}
	path, err := ResolveBinaryPath(name)
	if err != nil {
		return ""
	}
	return path
}

func ResolveBinaryPath(name string) (string, error) {
	trimmed := strings.TrimSpace(expandUserPath(name))
	if trimmed == "" {
		return "", fmt.Errorf("命令名为空")
	}
	if strings.Contains(trimmed, string(os.PathSeparator)) {
		if isExecutableFile(trimmed) {
			return trimmed, nil
		}
		return "", exec.ErrNotFound
	}
	if path, err := exec.LookPath(trimmed); err == nil {
		return path, nil
	}
	for _, dir := range candidateBinaryDirs() {
		candidate := filepath.Join(dir, trimmed)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func candidateBinaryDirs() []string {
	seen := map[string]struct{}{}
	dirs := make([]string, 0, 8)
	appendDir := func(path string) {
		path = strings.TrimSpace(expandUserPath(path))
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		dirs = append(dirs, path)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		appendDir(dir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		appendDir(filepath.Join(home, ".local", "bin"))
		appendDir(filepath.Join(home, "bin"))
	}
	return dirs
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

func expandUserPath(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if len(path) > 1 && path[1] == os.PathSeparator {
		return filepath.Join(home, path[2:])
	}
	return path
}

func readRTKMarker(path string) (bool, bool) {
	if strings.TrimSpace(path) == "" {
		return false, false
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(payload)) {
	case "1", "true", "rtk":
		return true, true
	case "0", "false", "plain":
		return false, true
	default:
		return false, false
	}
}

func safeTaskID(taskID string) string {
	replacer := strings.NewReplacer("#", "_", "/", "_", ":", "_", ".", "_", " ", "_")
	return replacer.Replace(taskID)
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func ParseExitCode(text string) int {
	code, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return code
}

func mapToEnv(values map[string]string) []string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		pairs = append(pairs, key+"="+value)
	}
	return pairs
}

func copyMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
