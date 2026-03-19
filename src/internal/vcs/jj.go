package vcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrJJNotAvailable     = errors.New("jj 未安装")
	ErrCapabilityMismatch = errors.New("jj/git 能力不兼容")
	ErrUnsupportedGit     = errors.New("git 缺少 jj 同步所需能力")
	ErrConflict           = errors.New("jj rebase 产生冲突，需人工解决")
	ErrPushFailed         = errors.New("jj git push 重试耗尽")
	ErrGitTooOld          = errors.New("git 版本过低，不满足 jj 同步要求")
	ErrSyncNetwork        = errors.New("仓库同步网络异常")
	ErrSyncAuth           = errors.New("仓库同步认证或权限失败")
	ErrSyncProtection     = errors.New("远端分支保护阻止直接推送")
	ErrSyncUnknown        = errors.New("仓库同步出现未分类错误")
)

const (
	defaultMaxRetry = 3
	commandTimeout  = 30 * time.Second
	defaultBookmark = "main"
	defaultRemote   = "origin"
	minGitVersion   = "2.41.0"
)

type syncCapabilityProbe struct {
	JJVersion           string
	GitVersion          string
	FetchPorcelain      bool
	FetchHelpDiagnostic string
}

type SyncCapabilityError struct {
	Reason     error
	JJVersion  string
	GitVersion string
	Probe      string
	Detail     string
}

type SyncCommandError struct {
	Operation string
	Reason    error
	Detail    string
}

func (e *SyncCommandError) Error() string {
	if e == nil {
		return ""
	}
	reason := ErrSyncUnknown.Error()
	switch {
	case errors.Is(e.Reason, ErrSyncNetwork):
		reason = ErrSyncNetwork.Error()
	case errors.Is(e.Reason, ErrSyncAuth):
		reason = ErrSyncAuth.Error()
	case errors.Is(e.Reason, ErrSyncProtection):
		reason = ErrSyncProtection.Error()
	case errors.Is(e.Reason, ErrSyncUnknown):
		reason = ErrSyncUnknown.Error()
	}
	if strings.TrimSpace(e.Detail) == "" {
		return reason
	}
	if strings.TrimSpace(e.Operation) == "" {
		return reason + ": " + strings.TrimSpace(e.Detail)
	}
	return fmt.Sprintf("%s(%s): %s", reason, strings.TrimSpace(e.Operation), strings.TrimSpace(e.Detail))
}

func (e *SyncCommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Reason
}

func (e *SyncCommandError) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	return errors.Is(e.Reason, target)
}

type SyncRetryError struct {
	Operation string
	Attempts  int
	Cause     error
}

func (e *SyncRetryError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return ErrPushFailed.Error()
	}
	if strings.TrimSpace(e.Operation) == "" {
		return fmt.Sprintf("%s: %v", ErrPushFailed.Error(), e.Cause)
	}
	return fmt.Sprintf("%s(最后失败步骤=%s, 已重试=%d): %v", ErrPushFailed.Error(), strings.TrimSpace(e.Operation), e.Attempts, e.Cause)
}

func (e *SyncRetryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *SyncRetryError) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	return target == ErrPushFailed || errors.Is(e.Cause, target)
}

func (e *SyncCapabilityError) Error() string {
	if e == nil {
		return ""
	}
	reason := ErrCapabilityMismatch.Error()
	switch {
	case errors.Is(e.Reason, ErrGitTooOld):
		reason = ErrGitTooOld.Error()
	case errors.Is(e.Reason, ErrUnsupportedGit):
		reason = ErrUnsupportedGit.Error()
	case errors.Is(e.Reason, ErrCapabilityMismatch):
		reason = ErrCapabilityMismatch.Error()
	}
	parts := make([]string, 0, 5)
	if strings.TrimSpace(e.GitVersion) != "" {
		parts = append(parts, "当前 git="+strings.TrimSpace(e.GitVersion))
	}
	if strings.TrimSpace(e.JJVersion) != "" {
		parts = append(parts, "jj="+strings.TrimSpace(e.JJVersion))
	}
	if strings.TrimSpace(e.Probe) != "" {
		parts = append(parts, "探测项="+strings.TrimSpace(e.Probe))
	}
	if strings.TrimSpace(e.Detail) != "" {
		parts = append(parts, strings.TrimSpace(e.Detail))
	}
	if len(parts) == 0 {
		return reason
	}
	return reason + ": " + strings.Join(parts, "；")
}

func (e *SyncCapabilityError) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	switch target {
	case ErrCapabilityMismatch:
		return true
	case ErrUnsupportedGit:
		return errors.Is(e.Reason, ErrUnsupportedGit) || errors.Is(e.Reason, ErrGitTooOld)
	default:
		return errors.Is(e.Reason, target)
	}
}

// SyncRepo 使用 jj 在本地提交并尽力同步远端。
func SyncRepo(repoPath, message string, paths []string, maxRetry int) error {
	repoPath = filepath.Clean(strings.TrimSpace(repoPath))
	if repoPath == "" {
		return errors.New("仓库路径不能为空")
	}
	if strings.TrimSpace(message) == "" {
		message = "ccclaw sync"
	}
	if maxRetry <= 0 {
		maxRetry = defaultMaxRetry
	}
	if _, err := exec.LookPath("jj"); err != nil {
		return ErrJJNotAvailable
	}
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return fmt.Errorf("创建仓库目录失败: %w", err)
	}
	if err := ensureJJRepo(repoPath); err != nil {
		return err
	}

	normalizedPaths, err := normalizePaths(repoPath, paths)
	if err != nil {
		return err
	}
	bookmark := detectPrimaryBookmark(repoPath)
	remote := detectRemote(repoPath)
	if remote == "" {
		if err := trackPaths(repoPath, normalizedPaths); err != nil {
			return err
		}
		_, err := commitChanges(repoPath, message, normalizedPaths)
		return err
	}
	probe, err := ensureSyncCapabilities()
	if err != nil {
		return err
	}

	var lastErr error
	lastOp := ""
	for attempt := 1; attempt <= maxRetry; attempt++ {
		if err := runJJ(repoPath, "git", "fetch", "--remote", remote); err != nil {
			if capabilityErr := classifyCapabilityMismatch(err, probe, "jj git fetch --remote "+remote); capabilityErr != nil {
				return capabilityErr
			}
			classified := classifySyncCommandError("fetch", err)
			if classified != nil {
				lastErr = classified
				lastOp = "fetch"
				if errors.Is(classified, ErrSyncNetwork) {
					continue
				}
				return classified
			}
			lastErr = fmt.Errorf("拉取远端失败(第 %d/%d 次): %w", attempt, maxRetry, err)
			lastOp = "fetch"
			continue
		}
		if remoteBookmarkExists(repoPath, remote, bookmark) {
			if err := runJJ(repoPath, "rebase", "-d", fmt.Sprintf("%s@%s", bookmark, remote)); err != nil {
				lastErr = fmt.Errorf("rebase 到 %s@%s 失败(第 %d/%d 次): %w", bookmark, remote, attempt, maxRetry, err)
				lastOp = "rebase"
				continue
			}
			conflicted, err := hasConflicts(repoPath)
			if err != nil {
				return err
			}
			if conflicted {
				return fmt.Errorf("%w: %s", ErrConflict, repoPath)
			}
		}
		if err := trackPaths(repoPath, normalizedPaths); err != nil {
			return err
		}
		committed, err := commitChanges(repoPath, message, normalizedPaths)
		if err != nil {
			return err
		}
		if committed {
			if err := runJJ(repoPath, "bookmark", "set", bookmark, "--revision", "@-"); err != nil {
				return fmt.Errorf("更新 bookmark %s 失败: %w", bookmark, err)
			}
		}
		if err := runJJ(repoPath, "git", "push", "--remote", remote, "--bookmark", bookmark); err != nil {
			if capabilityErr := classifyCapabilityMismatch(err, probe, "jj git push --remote "+remote+" --bookmark "+bookmark); capabilityErr != nil {
				return capabilityErr
			}
			classified := classifySyncCommandError("push", err)
			if classified != nil {
				lastErr = classified
				lastOp = "push"
				if errors.Is(classified, ErrSyncNetwork) {
					continue
				}
				return classified
			}
			lastErr = fmt.Errorf("推送远端失败(第 %d/%d 次): %w", attempt, maxRetry, err)
			lastOp = "push"
			continue
		}
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("未获得可用的推送结果")
	}
	return &SyncRetryError{
		Operation: lastOp,
		Attempts:  maxRetry,
		Cause:     lastErr,
	}
}

func ensureSyncCapabilities() (syncCapabilityProbe, error) {
	probe, err := probeSyncCapabilities()
	if err != nil {
		return syncCapabilityProbe{}, err
	}
	if err := validateSyncCapabilities(probe); err != nil {
		return syncCapabilityProbe{}, err
	}
	return probe, nil
}

func SyncCapabilityStatus() (string, error) {
	probe, err := probeSyncCapabilities()
	if err != nil {
		return "", err
	}
	detail := fmt.Sprintf("jj=%s git=%s fetch_porcelain=%t", strings.TrimSpace(probe.JJVersion), strings.TrimSpace(probe.GitVersion), probe.FetchPorcelain)
	if err := validateSyncCapabilities(probe); err != nil {
		return detail, err
	}
	return detail, nil
}

func probeSyncCapabilities() (syncCapabilityProbe, error) {
	jjVersion, err := runJJOutput("", "--version")
	if err != nil {
		return syncCapabilityProbe{}, fmt.Errorf("读取 jj 版本失败: %w", err)
	}
	gitVersion, err := runGitOutput("", "--version")
	if err != nil {
		return syncCapabilityProbe{}, fmt.Errorf("读取 git 版本失败: %w", err)
	}
	fetchPorcelain, diagnostic, err := detectGitFetchPorcelainSupport()
	if err != nil {
		return syncCapabilityProbe{}, fmt.Errorf("探测 git fetch 帮助失败: %w", err)
	}
	return syncCapabilityProbe{
		JJVersion:           strings.TrimSpace(jjVersion),
		GitVersion:          strings.TrimSpace(gitVersion),
		FetchPorcelain:      fetchPorcelain,
		FetchHelpDiagnostic: diagnostic,
	}, nil
}

func validateSyncCapabilities(probe syncCapabilityProbe) error {
	if compareVersion(gitVersionNumber(probe.GitVersion), minGitVersion) < 0 {
		return &SyncCapabilityError{
			Reason:     ErrGitTooOld,
			JJVersion:  probe.JJVersion,
			GitVersion: probe.GitVersion,
			Probe:      "git fetch --porcelain",
			Detail:     fmt.Sprintf("缺少 `git fetch --porcelain` 能力；请升级 git 至 %s 及以上，或切换匹配的 jj 版本", minGitVersion),
		}
	}
	if !probe.FetchPorcelain {
		return &SyncCapabilityError{
			Reason:     ErrUnsupportedGit,
			JJVersion:  probe.JJVersion,
			GitVersion: probe.GitVersion,
			Probe:      "git fetch --porcelain",
			Detail:     fmt.Sprintf("`git fetch -h` 未暴露 `--porcelain`；请升级 git 至 %s 及以上，或切换匹配的 jj 版本", minGitVersion),
		}
	}
	return nil
}

func detectGitFetchPorcelainSupport() (bool, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "fetch", "-h")
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		return false, text, errors.New("git fetch -h 执行超时")
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 129 {
			if text == "" {
				text = err.Error()
			}
			return false, text, fmt.Errorf("git fetch -h 执行失败: %s", text)
		}
	}
	return strings.Contains(text, "--porcelain"), text, nil
}

func classifyCapabilityMismatch(err error, probe syncCapabilityProbe, command string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCapabilityMismatch) || errors.Is(err, ErrUnsupportedGit) || errors.Is(err, ErrGitTooOld) {
		return err
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"required option: porcelain",
		"supported version is",
		"git does not recognize required option",
		"git fetch --porcelain",
	} {
		if strings.Contains(text, marker) {
			return &SyncCapabilityError{
				Reason:     ErrCapabilityMismatch,
				JJVersion:  probe.JJVersion,
				GitVersion: probe.GitVersion,
				Probe:      command,
				Detail:     strings.TrimSpace(err.Error()),
			}
		}
	}
	return nil
}

func classifySyncCommandError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrSyncNetwork) || errors.Is(err, ErrSyncAuth) || errors.Is(err, ErrSyncProtection) || errors.Is(err, ErrSyncUnknown) {
		return err
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	reason := ErrSyncUnknown
	switch {
	case containsAnyMarker(text,
		"protected branch",
		"branch protection",
		"protected ref",
		"gh006",
		"changes must be made through a pull request",
	):
		reason = ErrSyncProtection
	case containsAnyMarker(text,
		"permission denied",
		"authentication",
		"not authorized",
		"repository not found",
		"could not read from remote repository",
		"access denied",
		"403",
		"401",
	):
		reason = ErrSyncAuth
	case containsAnyMarker(text,
		"timeout",
		"timed out",
		"temporary",
		"temporarily",
		"connection reset",
		"connection refused",
		"broken pipe",
		"no route to host",
		"network is unreachable",
		"i/o timeout",
		"tls handshake timeout",
		"context deadline exceeded",
		"unexpected eof",
		"eof",
		"internal server error",
		"bad gateway",
		"service unavailable",
		"gateway timeout",
		"http 502",
		"http 503",
		"http 504",
		"rate limit",
		"dial tcp",
	):
		reason = ErrSyncNetwork
	}
	return &SyncCommandError{
		Operation: strings.TrimSpace(operation),
		Reason:    reason,
		Detail:    strings.TrimSpace(err.Error()),
	}
}

func containsAnyMarker(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, strings.TrimSpace(marker)) {
			return true
		}
	}
	return false
}

func gitVersionNumber(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func compareVersion(left, right string) int {
	leftParts := strings.Split(strings.TrimSpace(left), ".")
	rightParts := strings.Split(strings.TrimSpace(right), ".")
	maxLen := len(leftParts)
	if len(rightParts) > maxLen {
		maxLen = len(rightParts)
	}
	for idx := 0; idx < maxLen; idx++ {
		leftNum := versionPart(leftParts, idx)
		rightNum := versionPart(rightParts, idx)
		if leftNum < rightNum {
			return -1
		}
		if leftNum > rightNum {
			return 1
		}
	}
	return 0
}

func versionPart(parts []string, idx int) int {
	if idx >= len(parts) {
		return 0
	}
	value := strings.TrimSpace(parts[idx])
	total := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			break
		}
		total = total*10 + int(r-'0')
	}
	return total
}

func ensureJJRepo(repoPath string) error {
	if _, err := os.Stat(filepath.Join(repoPath, ".jj")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 .jj 状态失败: %w", err)
	}
	if err := runCommand("", "jj", "git", "init", "--colocate", repoPath); err != nil {
		return fmt.Errorf("初始化 jj 仓库失败: %w", err)
	}
	return nil
}

func normalizePaths(repoPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	items := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoPath, path)
		}
		rel, err := filepath.Rel(repoPath, path)
		if err != nil {
			return nil, fmt.Errorf("转换仓库相对路径失败: %w", err)
		}
		rel = filepath.Clean(rel)
		if rel == "." || rel == "" {
			rel = "."
		} else if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return nil, fmt.Errorf("路径超出仓库范围: %s", path)
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		items = append(items, rel)
	}
	return items, nil
}

func trackPaths(repoPath string, paths []string) error {
	args := []string{"file", "track"}
	if len(paths) == 0 {
		args = append(args, ".")
	} else {
		args = append(args, paths...)
	}
	if err := runJJ(repoPath, args...); err != nil {
		return fmt.Errorf("跟踪仓库路径失败: %w", err)
	}
	return nil
}

func commitChanges(repoPath, message string, paths []string) (bool, error) {
	changed, err := hasWorkingCopyChanges(repoPath, paths)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	args := []string{"commit", "-m", message}
	if len(paths) > 0 {
		args = append(args, paths...)
	}
	if err := runJJ(repoPath, args...); err != nil {
		return false, fmt.Errorf("提交仓库变更失败: %w", err)
	}
	return true, nil
}

func hasWorkingCopyChanges(repoPath string, paths []string) (bool, error) {
	args := []string{"diff", "--summary"}
	if len(paths) > 0 {
		args = append(args, paths...)
	}
	output, err := runJJOutput(repoPath, args...)
	if err != nil {
		return false, fmt.Errorf("检查仓库变更失败: %w", err)
	}
	return strings.TrimSpace(output) != "", nil
}

func hasConflicts(repoPath string) (bool, error) {
	output, err := runJJOutput(repoPath, "log", "-r", "conflicts()", "--count", "--no-graph")
	if err != nil {
		return false, fmt.Errorf("检查 jj 冲突失败: %w", err)
	}
	return strings.TrimSpace(output) != "0", nil
}

func detectPrimaryBookmark(repoPath string) string {
	output, err := runGitOutput(repoPath, "branch", "--show-current")
	if err == nil && strings.TrimSpace(output) != "" {
		return strings.TrimSpace(output)
	}
	return defaultBookmark
}

func detectRemote(repoPath string) string {
	if _, err := runGitOutput(repoPath, "remote", "get-url", defaultRemote); err == nil {
		return defaultRemote
	}
	return ""
}

func remoteBookmarkExists(repoPath, remote, bookmark string) bool {
	ref := fmt.Sprintf("refs/remotes/%s/%s", remote, bookmark)
	_, err := runGitOutput(repoPath, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

func runJJ(repoPath string, args ...string) error {
	return runCommand(repoPath, "jj", args...)
}

func runJJOutput(repoPath string, args ...string) (string, error) {
	return runCommandOutput(repoPath, "jj", args...)
}

func runGitOutput(repoPath string, args ...string) (string, error) {
	return runCommandOutput(repoPath, "git", args...)
}

func runCommand(repoPath, name string, args ...string) error {
	_, err := runCommandOutput(repoPath, name, args...)
	return err
}

func runCommandOutput(repoPath, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmdArgs := append([]string(nil), args...)
	if repoPath != "" && name == "jj" {
		cmdArgs = append([]string{"-R", repoPath}, cmdArgs...)
	}
	cmd := exec.CommandContext(ctx, name, cmdArgs...)
	if repoPath != "" && name != "jj" {
		cmd.Dir = repoPath
	}
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("%s %s 执行超时", name, strings.Join(args, " "))
	}
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("%s %s 执行失败: %s", name, strings.Join(args, " "), text)
	}
	return text, nil
}
