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
	ErrJJNotAvailable = errors.New("jj 未安装")
	ErrConflict       = errors.New("jj rebase 产生冲突，需人工解决")
	ErrPushFailed     = errors.New("jj git push 重试耗尽")
	ErrGitTooOld      = errors.New("git 版本过低，不满足 jj 同步要求")
)

const (
	defaultMaxRetry = 3
	commandTimeout  = 30 * time.Second
	defaultBookmark = "main"
	defaultRemote   = "origin"
	minGitVersion   = "2.41.0"
)

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
	if err := ensureSyncCapabilities(repoPath); err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetry; attempt++ {
		if err := runJJ(repoPath, "git", "fetch", "--remote", remote); err != nil {
			lastErr = fmt.Errorf("拉取远端失败(第 %d/%d 次): %w", attempt, maxRetry, err)
			continue
		}
		if remoteBookmarkExists(repoPath, remote, bookmark) {
			if err := runJJ(repoPath, "rebase", "-d", fmt.Sprintf("%s@%s", bookmark, remote)); err != nil {
				lastErr = fmt.Errorf("rebase 到 %s@%s 失败(第 %d/%d 次): %w", bookmark, remote, attempt, maxRetry, err)
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
			lastErr = fmt.Errorf("推送远端失败(第 %d/%d 次): %w", attempt, maxRetry, err)
			continue
		}
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("未获得可用的推送结果")
	}
	return fmt.Errorf("%w: %v", ErrPushFailed, lastErr)
}

func ensureSyncCapabilities(repoPath string) error {
	jjVersion, err := runJJOutput("", "--version")
	if err != nil {
		return fmt.Errorf("读取 jj 版本失败: %w", err)
	}
	gitVersion, err := runGitOutput("", "--version")
	if err != nil {
		return fmt.Errorf("读取 git 版本失败: %w", err)
	}
	gitVersion = strings.TrimSpace(gitVersion)
	jjVersion = strings.TrimSpace(jjVersion)
	if compareVersion(gitVersionNumber(gitVersion), minGitVersion) < 0 {
		return fmt.Errorf("%w: 当前 git=%s，jj=%s；请升级 git 至 %s 及以上，或切换匹配的 jj 版本", ErrGitTooOld, gitVersion, jjVersion, minGitVersion)
	}
	return nil
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
