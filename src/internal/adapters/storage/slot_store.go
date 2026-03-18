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
	if targetRepo == "" {
		return nil, nil
	}
	path := s.slotPath(targetRepo)
	lockPath := path + ".lock"
	var slot *RepoSlot
	err := withFileLock(lockPath, func() error {
		loaded, err := s.loadUnlocked(path)
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
	root := s.slotDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取仓库槽位目录失败: %w", err)
	}
	items := make([]*RepoSlot, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		slot, err := s.loadUnlocked(path)
		if err != nil {
			return nil, err
		}
		if slot != nil {
			items = append(items, slot)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.Before(items[j].UpdatedAt)
		}
		return items[i].TargetRepo < items[j].TargetRepo
	})
	return items, nil
}

func (s *RepoSlotStore) Upsert(slot *RepoSlot) error {
	if slot == nil {
		return errors.New("repo slot 不能为空")
	}
	slot.TargetRepo = strings.TrimSpace(slot.TargetRepo)
	if slot.TargetRepo == "" {
		return errors.New("repo slot target_repo 不能为空")
	}
	path := s.slotPath(slot.TargetRepo)
	lockPath := path + ".lock"
	return withFileLock(lockPath, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("创建仓库槽位目录失败: %w", err)
		}
		slot.UpdatedAt = time.Now().UTC()
		encoded, err := json.MarshalIndent(slot, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化仓库槽位失败: %w", err)
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), "slot-*.json")
		if err != nil {
			return fmt.Errorf("创建仓库槽位临时文件失败: %w", err)
		}
		tmpPath := tmp.Name()
		if _, err := tmp.Write(encoded); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("写入仓库槽位临时文件失败: %w", err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("关闭仓库槽位临时文件失败: %w", err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("替换仓库槽位失败: %w", err)
		}
		return nil
	})
}

func (s *RepoSlotStore) Delete(targetRepo string) error {
	targetRepo = strings.TrimSpace(targetRepo)
	if targetRepo == "" {
		return nil
	}
	path := s.slotPath(targetRepo)
	lockPath := path + ".lock"
	return withFileLock(lockPath, func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除仓库槽位失败: %w", err)
		}
		return nil
	})
}

func (s *RepoSlotStore) loadUnlocked(path string) (*RepoSlot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取仓库槽位失败: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var slot RepoSlot
	if err := json.Unmarshal(data, &slot); err != nil {
		return nil, fmt.Errorf("解析仓库槽位失败: %w", err)
	}
	slot.TargetRepo = strings.TrimSpace(slot.TargetRepo)
	if slot.TargetRepo == "" {
		return nil, nil
	}
	slot.Hints = append([]string(nil), slot.Hints...)
	return &slot, nil
}

func (s *RepoSlotStore) slotDir() string {
	return filepath.Join(s.varDir, "runtime")
}

func (s *RepoSlotStore) slotPath(targetRepo string) string {
	return filepath.Join(s.slotDir(), sanitizeRepoKey(targetRepo)+".json")
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
