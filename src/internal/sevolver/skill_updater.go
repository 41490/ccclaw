package sevolver

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	skillStatusActive   = "active"
	skillStatusDormant  = "dormant"
	skillStatusArchived = "archived"
)

type skillMeta struct {
	Name           string               `yaml:"name"`
	LastUsed       string               `yaml:"last_used,omitempty"`
	UseCount       int                  `yaml:"use_count,omitempty"`
	Status         string               `yaml:"status,omitempty"`
	GapSignals     []string             `yaml:"gap_signals"`
	GapEscalations []skillGapEscalation `yaml:"gap_escalations,omitempty"`
}

type skillGapEscalation struct {
	Fingerprint string   `yaml:"fingerprint"`
	Status      string   `yaml:"status,omitempty"`
	CloseReason string   `yaml:"close_reason,omitempty"`
	IssueNumber int      `yaml:"issue_number,omitempty"`
	IssueURL    string   `yaml:"issue_url,omitempty"`
	UpdatedAt   string   `yaml:"updated_at,omitempty"`
	GapIDs      []string `yaml:"gap_ids,omitempty"`
}

type skillLifecycleAction struct {
	Path   string
	Status string
}

var numericFieldPattern = regexp.MustCompile(`(?m)^use_count:\s*(\d+)\s*$`)

func isArchivedSkillDir(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.HasSuffix(clean, "/skills/archived") || strings.Contains(clean, "/skills/archived/")
}

func UpdateSkillMeta(skillFile string, hit SkillHit) error {
	content, meta, body, err := readSkillFile(skillFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("skill 缺少 name frontmatter: %s", skillFile)
	}

	hitDay := dateFloor(hit.Date)
	lastUsed := parseSkillDate(meta.LastUsed)
	if lastUsed.Equal(hitDay) || lastUsed.After(hitDay) {
		if strings.TrimSpace(meta.Status) != skillStatusActive || len(meta.GapSignals) == 0 {
			meta.Status = skillStatusActive
			if meta.GapSignals == nil {
				meta.GapSignals = []string{}
			}
			return writeSkillFile(skillFile, content, meta, body)
		}
		return nil
	}

	meta.LastUsed = hitDay.Format("2006-01-02")
	meta.UseCount = nextUseCount(content, meta.UseCount)
	meta.Status = skillStatusActive
	if meta.GapSignals == nil {
		meta.GapSignals = []string{}
	}
	return writeSkillFile(skillFile, content, meta, body)
}

func MarkDormant(skillFile string) error {
	content, meta, body, err := readSkillFile(skillFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return nil
	}
	if meta.GapSignals == nil {
		meta.GapSignals = []string{}
	}
	if strings.TrimSpace(meta.Status) == skillStatusDormant {
		return nil
	}
	meta.Status = skillStatusDormant
	return writeSkillFile(skillFile, content, meta, body)
}

func MarkArchived(skillFile string) error {
	content, meta, body, err := readSkillFile(skillFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return nil
	}
	if meta.GapSignals == nil {
		meta.GapSignals = []string{}
	}
	meta.Status = skillStatusArchived
	return writeSkillFile(skillFile, content, meta, body)
}

func ApplySkillGapEscalation(skillFile string, gapIDs []string, decision DeepAnalysisDecision, now time.Time) error {
	content, meta, body, err := readSkillFile(skillFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return fmt.Errorf("skill 缺少 name frontmatter: %s", skillFile)
	}
	meta.GapSignals = uniqueSortedStrings(append(meta.GapSignals, gapIDs...))
	meta.GapEscalations = upsertSkillGapEscalation(meta.GapEscalations, skillGapEscalation{
		Fingerprint: decision.Fingerprint,
		Status:      gapEscalationStatusEscalated,
		IssueNumber: decision.IssueNumber,
		IssueURL:    decision.IssueURL,
		UpdatedAt:   dateFloor(now).Format("2006-01-02"),
		GapIDs:      append([]string(nil), gapIDs...),
	})
	return writeSkillFile(skillFile, content, meta, body)
}

func ApplySkillGapEscalationResolution(skillFile string, targets []skillGapEscalation, now time.Time) (bool, error) {
	if len(targets) == 0 {
		return false, nil
	}
	content, meta, body, err := readSkillFile(skillFile)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return false, nil
	}

	changed := false
	for idx := range meta.GapEscalations {
		for _, target := range targets {
			if !skillGapEscalationMatches(meta.GapEscalations[idx], target) {
				continue
			}
			if meta.GapEscalations[idx].Status != gapEscalationStatusConverged {
				meta.GapEscalations[idx].Status = gapEscalationStatusConverged
				changed = true
			}
			if normalized := strings.TrimSpace(target.CloseReason); normalized != "" && meta.GapEscalations[idx].CloseReason != normalized {
				meta.GapEscalations[idx].CloseReason = normalized
				changed = true
			}
			updatedAt := dateFloor(now).Format("2006-01-02")
			if meta.GapEscalations[idx].UpdatedAt != updatedAt {
				meta.GapEscalations[idx].UpdatedAt = updatedAt
				changed = true
			}
			mergedGapIDs := uniqueSortedStrings(append(meta.GapEscalations[idx].GapIDs, target.GapIDs...))
			if !equalStringSlices(meta.GapEscalations[idx].GapIDs, mergedGapIDs) {
				meta.GapEscalations[idx].GapIDs = mergedGapIDs
				changed = true
			}
			mergedSignals := uniqueSortedStrings(append(meta.GapSignals, target.GapIDs...))
			if !equalStringSlices(meta.GapSignals, mergedSignals) {
				meta.GapSignals = mergedSignals
				changed = true
			}
		}
	}
	if !changed {
		return false, nil
	}
	return true, writeSkillFile(skillFile, content, meta, body)
}

func ArchiveSkill(kbDir, skillFile string) (string, error) {
	skillsRoot := filepath.Join(strings.TrimSpace(kbDir), "skills")
	rel, err := filepath.Rel(skillsRoot, skillFile)
	if err != nil {
		return "", fmt.Errorf("计算 archived 相对路径失败: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("skill 不在 skills 根目录下: %s", skillFile)
	}
	target := filepath.Join(skillsRoot, "archived", rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("创建 archived 目录失败: %w", err)
	}
	if err := os.Rename(skillFile, target); err != nil {
		return "", fmt.Errorf("迁移 archived skill 失败: %w", err)
	}
	return target, nil
}

func processSkillLifecycle(kbDir string, now time.Time) ([]skillLifecycleAction, error) {
	skillsRoot := filepath.Join(strings.TrimSpace(kbDir), "skills")
	actions := make([]skillLifecycleAction, 0)
	err := filepath.WalkDir(skillsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isArchivedSkillDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		_, meta, _, err := readSkillFile(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(meta.Name) == "" {
			return nil
		}
		lastUsed := parseSkillDate(meta.LastUsed)
		if lastUsed.IsZero() {
			return nil
		}
		inactiveDays := calendarDaysBetween(now, lastUsed)
		switch {
		case inactiveDays >= 28:
			if err := MarkArchived(path); err != nil {
				return err
			}
			target, err := ArchiveSkill(kbDir, path)
			if err != nil {
				return err
			}
			actions = append(actions, skillLifecycleAction{Path: target, Status: skillStatusArchived})
		case inactiveDays >= 14:
			if err := MarkDormant(path); err != nil {
				return err
			}
			actions = append(actions, skillLifecycleAction{Path: path, Status: skillStatusDormant})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("处理 skill 生命周期失败: %w", err)
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Status != actions[j].Status {
			return actions[i].Status < actions[j].Status
		}
		return actions[i].Path < actions[j].Path
	})
	return actions, nil
}

func readSkillFile(path string) (string, skillMeta, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", skillMeta{}, "", fmt.Errorf("读取 skill 失败: %w", err)
	}
	content := string(data)
	frontmatterBody, markdownBody, ok := splitSkillFrontmatter(content)
	if !ok {
		return content, skillMeta{}, content, nil
	}
	meta := skillMeta{}
	if err := yaml.Unmarshal([]byte(frontmatterBody), &meta); err != nil {
		return "", skillMeta{}, "", fmt.Errorf("解析 skill frontmatter 失败: %w", err)
	}
	return content, meta, markdownBody, nil
}

func writeSkillFile(path, original string, meta skillMeta, body string) error {
	if meta.GapSignals == nil {
		meta.GapSignals = []string{}
	}
	meta.GapSignals = uniqueSortedStrings(meta.GapSignals)
	meta.GapEscalations = normalizeSkillGapEscalations(meta.GapEscalations)
	if strings.TrimSpace(meta.Status) == "" {
		meta.Status = skillStatusActive
	}
	payload, err := yaml.Marshal(&meta)
	if err != nil {
		return fmt.Errorf("序列化 skill frontmatter 失败: %w", err)
	}
	content := "---\n" + strings.TrimRight(string(payload), "\n") + "\n---\n" + strings.TrimLeft(body, "\n")
	if content == original {
		return nil
	}
	return writeAtomically(path, []byte(content), 0o644)
}

func upsertSkillGapEscalation(existing []skillGapEscalation, incoming skillGapEscalation) []skillGapEscalation {
	incoming.GapIDs = uniqueSortedStrings(incoming.GapIDs)
	for idx := range existing {
		if strings.TrimSpace(existing[idx].Fingerprint) != strings.TrimSpace(incoming.Fingerprint) {
			continue
		}
		existing[idx].Status = incoming.Status
		if incoming.IssueNumber > 0 {
			existing[idx].IssueNumber = incoming.IssueNumber
		}
		if strings.TrimSpace(incoming.IssueURL) != "" {
			existing[idx].IssueURL = incoming.IssueURL
		}
		if strings.TrimSpace(incoming.UpdatedAt) != "" {
			existing[idx].UpdatedAt = incoming.UpdatedAt
		}
		existing[idx].GapIDs = uniqueSortedStrings(append(existing[idx].GapIDs, incoming.GapIDs...))
		return existing
	}
	return append(existing, incoming)
}

func normalizeSkillGapEscalations(items []skillGapEscalation) []skillGapEscalation {
	if len(items) == 0 {
		return nil
	}
	normalized := append([]skillGapEscalation(nil), items...)
	for idx := range normalized {
		normalized[idx].Fingerprint = strings.TrimSpace(normalized[idx].Fingerprint)
		normalized[idx].Status = strings.TrimSpace(normalized[idx].Status)
		normalized[idx].IssueURL = strings.TrimSpace(normalized[idx].IssueURL)
		normalized[idx].UpdatedAt = strings.TrimSpace(normalized[idx].UpdatedAt)
		normalized[idx].GapIDs = uniqueSortedStrings(normalized[idx].GapIDs)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Fingerprint < normalized[j].Fingerprint
	})
	return normalized
}

func skillGapEscalationMatches(current, target skillGapEscalation) bool {
	currentFingerprint := strings.TrimSpace(current.Fingerprint)
	targetFingerprint := strings.TrimSpace(target.Fingerprint)
	if currentFingerprint != "" && targetFingerprint != "" && currentFingerprint == targetFingerprint {
		return true
	}
	return current.IssueNumber > 0 && target.IssueNumber > 0 && current.IssueNumber == target.IssueNumber
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	items := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func splitSkillFrontmatter(raw string) (string, string, bool) {
	if !strings.HasPrefix(raw, "---\n") {
		return "", raw, false
	}
	rest := strings.TrimPrefix(raw, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return "", raw, false
	}
	return rest[:idx], rest[idx+5:], true
}

func nextUseCount(content string, current int) int {
	if current > 0 {
		return current + 1
	}
	matches := numericFieldPattern.FindStringSubmatch(content)
	if len(matches) == 2 {
		value, err := strconv.Atoi(matches[1])
		if err == nil {
			return value + 1
		}
	}
	return 1
}

func parseSkillDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}
	}
	return dateFloor(parsed)
}

func calendarDaysBetween(left, right time.Time) int {
	if left.IsZero() || right.IsZero() {
		return 0
	}
	leftUTC := time.Date(left.Year(), left.Month(), left.Day(), 0, 0, 0, 0, time.UTC)
	rightUTC := time.Date(right.Year(), right.Month(), right.Day(), 0, 0, 0, 0, time.UTC)
	return int(leftUTC.Sub(rightUTC).Hours() / 24)
}

func buildGapID(day time.Time, source string, lineNo int, keyword, context string) string {
	sum := sha1.Sum([]byte(strings.Join([]string{
		day.Format("2006-01-02"),
		source,
		strconv.Itoa(lineNo),
		keyword,
		context,
	}, "|")))
	return fmt.Sprintf("gap-%s-%x", day.Format("20060102"), sum[:4])
}

func writeAtomically(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ccclaw-sevolver-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换目标文件失败: %w", err)
	}
	return nil
}
