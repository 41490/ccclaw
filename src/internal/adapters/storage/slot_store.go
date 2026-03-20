package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const globalRepoSlotFileName = "global_sidecar.json"

type RepoSlotPhase string

const (
	RepoSlotPhaseRunning        RepoSlotPhase = "running"
	RepoSlotPhasePaneDead       RepoSlotPhase = "pane_dead"
	RepoSlotPhaseRestarting     RepoSlotPhase = "restarting"
	RepoSlotPhaseFinalizing     RepoSlotPhase = "finalizing"
	RepoSlotPhaseFinalizeFailed RepoSlotPhase = "finalize_failed"
)

type FinalizeStepState string

const (
	FinalizeStepPending  FinalizeStepState = "pending"
	FinalizeStepOK       FinalizeStepState = "ok"
	FinalizeStepFailed   FinalizeStepState = "failed"
	FinalizeStepConflict FinalizeStepState = "conflict"
)

type FinalizeFailureClass string

const (
	FinalizeFailureClassUnknown         FinalizeFailureClass = "unknown"
	FinalizeFailureClassNetwork         FinalizeFailureClass = "network"
	FinalizeFailureClassConflict        FinalizeFailureClass = "conflict"
	FinalizeFailureClassAuth            FinalizeFailureClass = "auth"
	FinalizeFailureClassProtection      FinalizeFailureClass = "protection"
	FinalizeFailureClassVersionMismatch FinalizeFailureClass = "version_mismatch"
	FinalizeFailureClassConfig          FinalizeFailureClass = "config"
	FinalizeFailureClassIssueReporting  FinalizeFailureClass = "issue_reporting"
)

func (c FinalizeFailureClass) Display() string {
	switch c {
	case FinalizeFailureClassNetwork:
		return "网络抖动"
	case FinalizeFailureClassConflict:
		return "仓库冲突"
	case FinalizeFailureClassAuth:
		return "认证或权限"
	case FinalizeFailureClassProtection:
		return "分支保护"
	case FinalizeFailureClassVersionMismatch:
		return "jj/git 兼容"
	case FinalizeFailureClassConfig:
		return "环境或配置"
	case FinalizeFailureClassIssueReporting:
		return "Issue 回帖"
	default:
		return "待分类"
	}
}

type FinalizeFailureMode string

const (
	FinalizeFailureModeRetry FinalizeFailureMode = "retry"
	FinalizeFailureModePause FinalizeFailureMode = "pause"
)

func (m FinalizeFailureMode) Display() string {
	switch m {
	case FinalizeFailureModeRetry:
		return "自动重试"
	case FinalizeFailureModePause:
		return "需人工介入"
	default:
		return "待定"
	}
}

type RepoSlot struct {
	TargetRepo          string               `json:"target_repo"`
	TaskID              string               `json:"task_id"`
	ExecutorMode        string               `json:"executor_mode,omitempty"`
	SessionName         string               `json:"session_name,omitempty"`
	SessionID           string               `json:"session_id,omitempty"`
	Phase               RepoSlotPhase        `json:"phase"`
	CurrentStep         string               `json:"current_step,omitempty"`
	RestartCount        int                  `json:"restart_count,omitempty"`
	FinalizeRetryStep   string               `json:"finalize_retry_step,omitempty"`
	FinalizeRetryCount  int                  `json:"finalize_retry_count,omitempty"`
	SyncTarget          FinalizeStepState    `json:"sync_target,omitempty"`
	SyncHome            FinalizeStepState    `json:"sync_home,omitempty"`
	ReportIssue         FinalizeStepState    `json:"report_issue,omitempty"`
	FailureClass        FinalizeFailureClass `json:"failure_class,omitempty"`
	FailureMode         FinalizeFailureMode  `json:"failure_mode,omitempty"`
	LastError           string               `json:"last_error,omitempty"`
	Hints               []string             `json:"hints,omitempty"`
	LastProbeAt         time.Time            `json:"last_probe_at,omitempty"`
	LastAttemptAt       time.Time            `json:"last_attempt_at,omitempty"`
	LastAdvanceAt       time.Time            `json:"last_advance_at,omitempty"`
	NextRetryAt         time.Time            `json:"next_retry_at,omitempty"`
	LastReportedAt      time.Time            `json:"last_reported_at,omitempty"`
	LastReportedFailure string               `json:"last_reported_failure,omitempty"`
	LastFailureStep     string               `json:"last_failure_step,omitempty"`
	LastFailureClass    FinalizeFailureClass `json:"last_failure_class,omitempty"`
	RecoveryReportedAt  time.Time            `json:"recovery_reported_at,omitempty"`
	UpdatedAt           time.Time            `json:"updated_at"`
	CompletedAt         time.Time            `json:"completed_at,omitempty"`
}

type RepoSlotStore struct {
	varDir string
}

func newRepoSlotStore(varDir string) *RepoSlotStore {
	return &RepoSlotStore{varDir: varDir}
}

func (s *RepoSlotStore) Get(targetRepo string) (*RepoSlot, error) {
	targetRepo = strings.TrimSpace(targetRepo)
	slot, err := s.GetActive()
	if err != nil || slot == nil {
		return slot, err
	}
	if targetRepo != "" && !strings.EqualFold(targetRepo, slot.TargetRepo) {
		return nil, nil
	}
	return slot, nil
}

func (s *RepoSlotStore) GetActive() (*RepoSlot, error) {
	path := s.slotPath()
	lockPath := path + ".lock"
	var slot *RepoSlot
	err := withFileLock(lockPath, func() error {
		loaded, err := s.loadActiveUnlocked()
		if err != nil {
			return err
		}
		slot = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return slot, nil
}

func (s *RepoSlotStore) List() ([]*RepoSlot, error) {
	slot, err := s.GetActive()
	if err != nil {
		return nil, err
	}
	if slot == nil {
		return nil, nil
	}
	return []*RepoSlot{slot}, nil
}

func (s *RepoSlotStore) Upsert(slot *RepoSlot) error {
	if slot == nil {
		return errors.New("全局 sidecar 不能为空")
	}
	slot.TargetRepo = strings.TrimSpace(slot.TargetRepo)
	if slot.TargetRepo == "" {
		return errors.New("全局 sidecar target_repo 不能为空")
	}
	path := s.slotPath()
	lockPath := path + ".lock"
	return withFileLock(lockPath, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("创建全局 sidecar 目录失败: %w", err)
		}
		slot.UpdatedAt = time.Now().UTC()
		encoded, err := json.MarshalIndent(slot, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化全局 sidecar 失败: %w", err)
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), "slot-*.json")
		if err != nil {
			return fmt.Errorf("创建全局 sidecar 临时文件失败: %w", err)
		}
		tmpPath := tmp.Name()
		if _, err := tmp.Write(encoded); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("写入全局 sidecar 临时文件失败: %w", err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("关闭全局 sidecar 临时文件失败: %w", err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("替换全局 sidecar 失败: %w", err)
		}
		return s.cleanupLegacySlotsUnlocked()
	})
}

func (s *RepoSlotStore) Delete(targetRepo string) error {
	path := s.slotPath()
	lockPath := path + ".lock"
	return withFileLock(lockPath, func() error {
		slot, err := s.loadActiveUnlocked()
		if err != nil {
			return err
		}
		targetRepo = strings.TrimSpace(targetRepo)
		if targetRepo != "" && slot != nil && !strings.EqualFold(targetRepo, slot.TargetRepo) {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除全局 sidecar 失败: %w", err)
		}
		return s.cleanupLegacySlotsUnlocked()
	})
}

func (s *RepoSlotStore) loadUnlocked(path string) (*RepoSlot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 sidecar 失败: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var slot RepoSlot
	if err := json.Unmarshal(data, &slot); err != nil {
		return nil, fmt.Errorf("解析 sidecar 失败: %w", err)
	}
	slot.TargetRepo = strings.TrimSpace(slot.TargetRepo)
	if slot.TargetRepo == "" {
		return nil, nil
	}
	slot.Hints = append([]string(nil), slot.Hints...)
	return &slot, nil
}

func (s *RepoSlotStore) loadActiveUnlocked() (*RepoSlot, error) {
	path := s.slotPath()
	slot, err := s.loadUnlocked(path)
	if err != nil || slot != nil {
		return slot, err
	}
	legacy, legacyPath, err := s.loadLegacySlotUnlocked()
	if err != nil || legacy == nil {
		return legacy, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建全局 sidecar 目录失败: %w", err)
	}
	payload, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化迁移 sidecar 失败: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return nil, fmt.Errorf("写入迁移 sidecar 失败: %w", err)
	}
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("清理旧版 repo sidecar 失败: %w", err)
	}
	return legacy, nil
}

func (s *RepoSlotStore) loadLegacySlotUnlocked() (*RepoSlot, string, error) {
	paths, err := s.legacySlotPathsUnlocked()
	if err != nil {
		return nil, "", err
	}
	type legacyItem struct {
		path string
		slot *RepoSlot
	}
	items := make([]legacyItem, 0, len(paths))
	for _, path := range paths {
		slot, err := s.loadUnlocked(path)
		if err != nil {
			return nil, "", err
		}
		if slot != nil {
			items = append(items, legacyItem{path: path, slot: slot})
		}
	}
	if len(items) == 0 {
		return nil, "", nil
	}
	if len(items) > 1 {
		sort.Slice(items, func(i, j int) bool {
			if !items[i].slot.UpdatedAt.Equal(items[j].slot.UpdatedAt) {
				return items[i].slot.UpdatedAt.After(items[j].slot.UpdatedAt)
			}
			return items[i].slot.TargetRepo < items[j].slot.TargetRepo
		})
		summaries := make([]string, 0, len(items))
		for _, item := range items {
			summaries = append(summaries, fmt.Sprintf("%s(task=%s,repo=%s,updated_at=%s)", filepath.Base(item.path), item.slot.TaskID, item.slot.TargetRepo, item.slot.UpdatedAt.Format(time.RFC3339)))
		}
		return nil, "", fmt.Errorf("检测到多个旧版 repo sidecar，无法自动迁移到全局唯一 sidecar: %s", strings.Join(summaries, ", "))
	}
	return items[0].slot, items[0].path, nil
}

func (s *RepoSlotStore) cleanupLegacySlotsUnlocked() error {
	paths, err := s.legacySlotPathsUnlocked()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("清理旧版 repo sidecar 失败: %w", err)
		}
	}
	return nil
}

func (s *RepoSlotStore) legacySlotPathsUnlocked() ([]string, error) {
	root := s.slotDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 sidecar 目录失败: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Name() == globalRepoSlotFileName {
			continue
		}
		paths = append(paths, filepath.Join(root, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *RepoSlotStore) slotDir() string {
	return filepath.Join(s.varDir, "runtime")
}

func (s *RepoSlotStore) slotPath() string {
	return filepath.Join(s.slotDir(), globalRepoSlotFileName)
}

func sanitizeRepoKey(targetRepo string) string {
	targetRepo = strings.ToLower(strings.TrimSpace(targetRepo))
	if targetRepo == "" {
		return "unknown"
	}
	var sb strings.Builder
	for _, r := range targetRepo {
		switch {
		case r >= 'a' && r <= 'z':
			sb.WriteRune(r)
		case r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	return strings.Trim(sb.String(), "_")
}
