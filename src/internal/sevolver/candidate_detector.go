package sevolver

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const candidateScanWindowDays = 14 // 独立于 scanWindowDays=7，不影响现有 sevolver 日报

var actionPattern = regexp.MustCompile(`(?:发布|执行|完成|运行|触发|处理)\s+([a-zA-Z_\-]+(?:\s+[a-zA-Z_\-]+)?)`)

type patternHit struct {
	Pattern string
	Sources []string
	Count   int
}

type journalFileEntry struct {
	Path string
	Day  time.Time
}

// DetectCandidates 扫描 journalDir 最近 candidateScanWindowDays 天，
// 识别重复操作模式（≥3次）并在 candidatesDir 生成草稿文件。
// 返回新建草稿数量。
func DetectCandidates(journalDir, candidatesDir string, now time.Time) (int, error) {
	since := dateFloor(now).AddDate(0, 0, -candidateScanWindowDays)
	files, err := collectCandidateJournalFiles(journalDir, since)
	if err != nil {
		return 0, err
	}

	// 统计操作模式出现次数（以文件为单位，同一文件只计一次）
	patternSources := map[string][]string{}
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			matches := actionPattern.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				pattern := strings.ToLower(strings.TrimSpace(m[1]))
				if pattern == "" {
					continue
				}
				patternSources[pattern] = appendUniq(patternSources[pattern], file.Path)
			}
		}
	}

	// 筛选出现 ≥3 次的模式
	var hits []patternHit
	for pattern, sources := range patternSources {
		if len(sources) >= 3 {
			hits = append(hits, patternHit{Pattern: pattern, Sources: sources, Count: len(sources)})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Count > hits[j].Count })

	created := 0
	for _, hit := range hits {
		slug := strings.ReplaceAll(hit.Pattern, " ", "-")
		dirName := fmt.Sprintf("%s-%s", now.Format("20060102"), slug)
		draftDir := filepath.Join(candidatesDir, dirName)
		draftPath := filepath.Join(draftDir, "CLAUDE.md")

		// 若草稿已存在则跳过
		if _, err := os.Stat(draftPath); err == nil {
			continue
		}
		if err := os.MkdirAll(draftDir, 0o755); err != nil {
			return created, fmt.Errorf("创建候选目录失败: %w", err)
		}
		content := buildCandidateDraft(hit, now)
		if err := os.WriteFile(draftPath, []byte(content), 0o644); err != nil {
			return created, fmt.Errorf("写入候选草稿失败: %w", err)
		}
		created++
	}
	return created, nil
}

func collectCandidateJournalFiles(dir string, since time.Time) ([]journalFileEntry, error) {
	var files []journalFileEntry
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(since) {
			return nil
		}
		files = append(files, journalFileEntry{Path: path, Day: info.ModTime()})
		return nil
	})
	return files, err
}

func buildCandidateDraft(hit patternHit, now time.Time) string {
	keywords := strings.Split(hit.Pattern, " ")
	var sourcesYAML strings.Builder
	for _, s := range hit.Sources {
		sourcesYAML.WriteString(fmt.Sprintf("  - %s\n", s))
	}
	var keywordsYAML strings.Builder
	for _, k := range keywords {
		keywordsYAML.WriteString(fmt.Sprintf("  - %s\n", k))
	}
	return fmt.Sprintf(`---
name: %s
status: candidate
detected_at: %s
source_journal:
%sexternal_match_keywords:
%s# ⚠️ 禁止自动安装。请人工核实后移入 L1/ 或 L2/
---

## 草稿内容（sevolver 自动识别）

操作模式：%s（在 %d 个日志文件中出现 %d 次）

## 匹配的外部技能（需人工核实，禁止自动安装）

搜索关键词：%s

建议搜索来源：
- superpowers skills：Skill tool 搜索 %s
- 确认后操作：手动安装并删除本草稿，或补全后移入 L1/ 或 L2/
- 若废弃：标注 status: rejected，无需删除
`,
		hit.Pattern,
		now.Format("2006-01-02"),
		sourcesYAML.String(),
		keywordsYAML.String(),
		hit.Pattern,
		len(hit.Sources),
		hit.Count,
		strings.Join(keywords, ", "),
		keywords[0],
	)
}

func appendUniq(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
