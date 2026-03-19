package vcs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRepoTracksPathsAndRetriesPush(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	prepareGitVersionShim(t, "git version 2.42.0")
	pushState := filepath.Join(t.TempDir(), "push-count")
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_PUSH_STATE_FILE", pushState)
	t.Setenv("JJ_PUSH_FAIL_UNTIL", "1")
	t.Setenv("JJ_PUSH_FAIL_MESSAGE", "connection reset by peer")

	if err := SyncRepo(repoPath, "journal: 2026-03-11", []string{filepath.Join(repoPath, "kb", "journal", "2026", "03", "day.md")}, 3); err != nil {
		t.Fatalf("SyncRepo 失败: %v", err)
	}

	commands := readCommands(t, logFile)
	for _, want := range []string{
		"git init --colocate",
		"git fetch --remote origin",
		"file track kb/journal/2026/03/day.md",
		"commit -m journal: 2026-03-11 kb/journal/2026/03/day.md",
		"bookmark set main --revision @-",
		"git push --remote origin --bookmark main",
	} {
		if !containsCommand(commands, want) {
			t.Fatalf("expected command %q, got %#v", want, commands)
		}
	}
	if countCommands(commands, "git push --remote origin --bookmark main") != 2 {
		t.Fatalf("expected 2 push attempts, got %#v", commands)
	}
	if countCommands(commands, "commit -m journal: 2026-03-11 kb/journal/2026/03/day.md") != 1 {
		t.Fatalf("expected a single commit, got %#v", commands)
	}
}

func TestSyncRepoStopsOnConflict(t *testing.T) {
	repoPath := initGitRepo(t, true)
	if err := os.MkdirAll(filepath.Join(repoPath, ".jj"), 0o755); err != nil {
		t.Fatalf("创建 .jj 失败: %v", err)
	}
	if output, err := runGitOutput(repoPath, "update-ref", "refs/remotes/origin/main", "HEAD"); err != nil {
		t.Fatalf("准备 origin/main 失败: %v (%s)", err, output)
	}
	logFile, committedFile := prepareFakeJJ(t)
	prepareGitVersionShim(t, "git version 2.42.0")
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_CONFLICT", "1")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !strings.Contains(err.Error(), ErrConflict.Error()) {
		t.Fatalf("expected conflict error, got %v", err)
	}

	commands := readCommands(t, logFile)
	if containsCommand(commands, "commit -m task done") {
		t.Fatalf("unexpected commit after conflict: %#v", commands)
	}
}

func TestSyncRepoWithoutRemoteOnlyCommitsLocalChanges(t *testing.T) {
	repoPath := initGitRepo(t, false)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)

	if err := SyncRepo(repoPath, "patrol: sync", nil, 3); err != nil {
		t.Fatalf("SyncRepo 失败: %v", err)
	}

	commands := readCommands(t, logFile)
	if containsCommand(commands, "git fetch --remote origin") || containsCommand(commands, "git push --remote origin --bookmark main") {
		t.Fatalf("unexpected remote sync for local repo: %#v", commands)
	}
	if !containsCommand(commands, "file track .") || !containsCommand(commands, "commit -m patrol: sync") {
		t.Fatalf("expected local track+commit, got %#v", commands)
	}
}

func TestSyncRepoFailsFastWhenGitVersionTooOld(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	prepareGitVersionShim(t, "git version 2.39.5")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !strings.Contains(err.Error(), ErrGitTooOld.Error()) {
		t.Fatalf("expected git-too-old error, got %v", err)
	}
	if !strings.Contains(err.Error(), "jj 0.39.0") || !strings.Contains(err.Error(), "2.41.0") {
		t.Fatalf("expected version diagnostic, got %v", err)
	}

	commands := readCommands(t, logFile)
	if containsCommand(commands, "git fetch --remote origin") {
		t.Fatalf("前置探测失败后不应继续 fetch，实际命令=%#v", commands)
	}
}

func TestSyncRepoFailsFastWhenGitMissingFetchPorcelainCapability(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	prepareGitVersionShim(t, "git version 2.41.0")
	t.Setenv("FAKE_GIT_FETCH_HELP", "usage: git fetch [<options>]\n    --force           force overwrite\n")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !errors.Is(err, ErrUnsupportedGit) {
		t.Fatalf("expected unsupported-git error, got %v", err)
	}
	if !strings.Contains(err.Error(), "--porcelain") {
		t.Fatalf("expected porcelain diagnostic, got %v", err)
	}

	commands := readCommands(t, logFile)
	if containsCommand(commands, "git fetch --remote origin") {
		t.Fatalf("缺少 porcelain 能力时不应继续 fetch，实际命令=%#v", commands)
	}
}

func TestSyncRepoStopsRetryOnCapabilityMismatchFromJJ(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_FETCH_CAPABILITY_FAIL", "1")
	prepareGitVersionShim(t, "git version 2.41.0")
	t.Setenv("FAKE_GIT_FETCH_HELP", "usage: git fetch [<options>]\n    --porcelain      machine-readable output\n")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !errors.Is(err, ErrCapabilityMismatch) {
		t.Fatalf("expected capability-mismatch error, got %v", err)
	}
	if strings.Contains(err.Error(), ErrPushFailed.Error()) {
		t.Fatalf("capability mismatch 不应被包装成 push failed: %v", err)
	}

	commands := readCommands(t, logFile)
	if countCommands(commands, "git fetch --remote origin") != 1 {
		t.Fatalf("capability mismatch 应在首轮 fetch 直接停止，实际命令=%#v", commands)
	}
}

func TestSyncRepoClassifiesBranchProtectionWithoutRetry(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_PUSH_ERROR_TEXT", "remote: error: GH006: Protected branch update failed for refs/heads/main.")
	prepareGitVersionShim(t, "git version 2.42.0")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !errors.Is(err, ErrSyncProtection) {
		t.Fatalf("expected branch protection error, got %v", err)
	}
	if errors.Is(err, ErrPushFailed) {
		t.Fatalf("branch protection 不应继续重试到耗尽: %v", err)
	}

	commands := readCommands(t, logFile)
	if countCommands(commands, "git push --remote origin --bookmark main") != 1 {
		t.Fatalf("branch protection 应在首轮 push 后停止，实际命令=%#v", commands)
	}
}

func TestSyncRepoClassifiesAuthFailureWithoutRetry(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_FETCH_ERROR_TEXT", "fatal: Authentication failed for 'https://example.com/demo.git/'")
	prepareGitVersionShim(t, "git version 2.42.0")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !errors.Is(err, ErrSyncAuth) {
		t.Fatalf("expected auth error, got %v", err)
	}
	if errors.Is(err, ErrPushFailed) {
		t.Fatalf("auth failure 不应继续重试到耗尽: %v", err)
	}

	commands := readCommands(t, logFile)
	if countCommands(commands, "git fetch --remote origin") != 1 {
		t.Fatalf("auth failure 应在首轮 fetch 后停止，实际命令=%#v", commands)
	}
}

func TestSyncRepoKeepsNetworkClassWhenRetryExhausted(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_PUSH_ERROR_TEXT", "dial tcp 140.82.112.4:443: i/o timeout")
	prepareGitVersionShim(t, "git version 2.42.0")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !errors.Is(err, ErrPushFailed) || !errors.Is(err, ErrSyncNetwork) {
		t.Fatalf("expected retry-exhausted network error, got %v", err)
	}

	commands := readCommands(t, logFile)
	if countCommands(commands, "git push --remote origin --bookmark main") != 3 {
		t.Fatalf("network failure 应按 maxRetry 重试，实际命令=%#v", commands)
	}
}

func TestSyncRepoClassifiesUnknownFailure(t *testing.T) {
	repoPath := initGitRepo(t, true)
	logFile, committedFile := prepareFakeJJ(t)
	t.Setenv("JJ_LOG_FILE", logFile)
	t.Setenv("JJ_COMMITTED_FILE", committedFile)
	t.Setenv("JJ_PUSH_ERROR_TEXT", "remote rejected for unexplained reason")
	prepareGitVersionShim(t, "git version 2.42.0")

	err := SyncRepo(repoPath, "task done", nil, 3)
	if err == nil || !errors.Is(err, ErrSyncUnknown) {
		t.Fatalf("expected unknown sync error, got %v", err)
	}
	if errors.Is(err, ErrPushFailed) {
		t.Fatalf("unknown failure 当前应直接抛出，实际=%v", err)
	}
}

func initGitRepo(t *testing.T, withRemote bool) string {
	t.Helper()
	repoPath := t.TempDir()
	if _, err := runGitOutput(repoPath, "init", "-b", "main"); err != nil {
		t.Fatalf("初始化 git 仓库失败: %v", err)
	}
	if _, err := runGitOutput(repoPath, "config", "user.name", "ccclaw"); err != nil {
		t.Fatalf("配置 git 用户失败: %v", err)
	}
	if _, err := runGitOutput(repoPath, "config", "user.email", "ccclaw@example.com"); err != nil {
		t.Fatalf("配置 git 邮箱失败: %v", err)
	}
	readme := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readme, []byte("init\n"), 0o644); err != nil {
		t.Fatalf("写入 README 失败: %v", err)
	}
	if _, err := runGitOutput(repoPath, "add", "README.md"); err != nil {
		t.Fatalf("git add 失败: %v", err)
	}
	if _, err := runGitOutput(repoPath, "commit", "-m", "init"); err != nil {
		t.Fatalf("git commit 失败: %v", err)
	}
	if withRemote {
		if _, err := runGitOutput(repoPath, "remote", "add", "origin", "https://example.com/demo.git"); err != nil {
			t.Fatalf("添加 origin 失败: %v", err)
		}
	}
	return repoPath
}

func prepareFakeJJ(t *testing.T) (string, string) {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("创建 fake bin 失败: %v", err)
	}
	logFile := filepath.Join(t.TempDir(), "jj.log")
	committedFile := filepath.Join(t.TempDir(), "jj.committed")
	script := filepath.Join(binDir, "jj")
	body := `#!/usr/bin/env bash
set -euo pipefail
log_file="${JJ_LOG_FILE:?}"
repo=""
if [[ "${1:-}" == "-R" ]]; then
  repo="$2"
  shift 2
fi
printf '%s\n' "$*" >> "$log_file"
case "${1:-}" in
  --version)
    printf 'jj 0.39.0\n'
    ;;
  git)
    case "${2:-}" in
      init)
        target="${@: -1}"
        mkdir -p "$target/.jj"
        ;;
  fetch)
        if [[ "${JJ_FETCH_CAPABILITY_FAIL:-0}" == "1" ]]; then
          printf 'Error: Git does not recognize required option: porcelain (note: supported version is 2.41.0)\n' >&2
          exit 1
        fi
        if [[ -n "${JJ_FETCH_ERROR_TEXT:-}" ]]; then
          printf '%s\n' "${JJ_FETCH_ERROR_TEXT}" >&2
          exit 1
        fi
        ;;
      push)
        state_file="${JJ_PUSH_STATE_FILE:-}"
        fail_until="${JJ_PUSH_FAIL_UNTIL:-0}"
        push_error="${JJ_PUSH_ERROR_TEXT:-}"
        if [[ -z "$push_error" ]]; then
          push_error="${JJ_PUSH_FAIL_MESSAGE:-push failed}"
        fi
        if [[ -n "${JJ_PUSH_ERROR_TEXT:-}" ]]; then
          printf '%s\n' "$push_error" >&2
          exit 1
        fi
        if [[ -n "$state_file" ]]; then
          count=0
          if [[ -f "$state_file" ]]; then
            count="$(cat "$state_file")"
          fi
          count=$((count + 1))
          printf '%s' "$count" > "$state_file"
          if (( count <= fail_until )); then
            printf '%s\n' "$push_error" >&2
            exit 1
          fi
        fi
        ;;
    esac
    ;;
  diff)
    if [[ ! -f "${JJ_COMMITTED_FILE:-}" ]]; then
      printf 'M tracked-file\n'
    fi
    ;;
  log)
    if [[ "${JJ_CONFLICT:-0}" == "1" ]]; then
      printf '1\n'
    else
      printf '0\n'
    fi
    ;;
  commit)
    if [[ -n "${JJ_COMMITTED_FILE:-}" ]]; then
      : > "$JJ_COMMITTED_FILE"
    fi
    ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 fake jj 失败: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
	return logFile, committedFile
}

func prepareGitVersionShim(t *testing.T, version string) {
	t.Helper()
	binDir := filepath.Join(t.TempDir(), "git-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("创建 fake git bin 失败: %v", err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("定位 git 失败: %v", err)
	}
	script := filepath.Join(binDir, "git")
	body := "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"${1:-}\" == \"--version\" ]]; then\n  printf '%s\\n' \"${FAKE_GIT_VERSION:?}\"\n  exit 0\nfi\nif [[ \"${1:-}\" == \"fetch\" && \"${2:-}\" == \"-h\" ]]; then\n  if [[ -n \"${FAKE_GIT_FETCH_HELP:-}\" ]]; then\n    printf '%s' \"${FAKE_GIT_FETCH_HELP}\"\n  else\n    printf 'usage: git fetch [<options>]\\n    --porcelain      machine-readable output\\n'\n  fi\n  exit 129\nfi\nexec \"" + gitPath + "\" \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写入 fake git 失败: %v", err)
	}
	t.Setenv("FAKE_GIT_VERSION", version)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)
}

func readCommands(t *testing.T, path string) []string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取命令日志失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	items := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			items = append(items, line)
		}
	}
	return items
}

func containsCommand(commands []string, want string) bool {
	for _, command := range commands {
		if strings.Contains(command, want) {
			return true
		}
	}
	return false
}

func countCommands(commands []string, want string) int {
	total := 0
	for _, command := range commands {
		if strings.Contains(command, want) {
			total++
		}
	}
	return total
}
