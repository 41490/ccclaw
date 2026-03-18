package memory

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Document struct {
	Path     string
	Title    string
	Summary  string
	Keywords []string
	Score    int
}

type Index struct {
	docs []Document
}

func Build(root string) (*Index, error) {
	index := &Index{}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return index, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isArchivedSkillsDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		index.docs = append(index.docs, buildDocument(path, string(data)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return index, nil
}

func isArchivedSkillsDir(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.HasSuffix(clean, "/skills/archived") || clean == "skills/archived"
}

func (idx *Index) Match(keywords []string, limit int) []Document {
	if idx == nil || len(idx.docs) == 0 || len(keywords) == 0 {
		return nil
	}
	matches := make([]Document, 0)
	for _, doc := range idx.docs {
		score := scoreDocument(doc, keywords)
		if score > 0 {
			doc.Score = score
			matches = append(matches, doc)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Score > matches[j].Score
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Trigger     string   `yaml:"trigger"`
	Keywords    []string `yaml:"keywords"`
}

func buildDocument(path, raw string) Document {
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	body := raw
	meta := frontmatter{}
	hasMetaTitle := false
	if frontmatterBody, markdownBody, ok := splitFrontmatter(raw); ok {
		body = markdownBody
		if err := yaml.Unmarshal([]byte(frontmatterBody), &meta); err == nil {
			if strings.TrimSpace(meta.Name) != "" {
				title = strings.TrimSpace(meta.Name)
				hasMetaTitle = true
			}
		}
	}
	if heading := firstHeading(body); heading != "" && !hasMetaTitle {
		title = heading
	}
	summary := strings.TrimSpace(meta.Description)
	if summary == "" {
		summary = strings.TrimSpace(meta.Trigger)
	}
	if summary == "" {
		summary = firstSummaryLine(body)
	}
	if summary == "" {
		summary = title
	}
	return Document{
		Path:     path,
		Title:    title,
		Summary:  summary,
		Keywords: normalizeKeywords(meta.Keywords),
	}
}

func splitFrontmatter(raw string) (string, string, bool) {
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

func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if text != "" {
			return text
		}
	}
	return ""
}

func firstSummaryLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<!--") || strings.HasPrefix(line, "```") {
			continue
		}
		return line
	}
	return ""
}

func normalizeKeywords(keywords []string) []string {
	if len(keywords) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(keywords))
	normalized := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(strings.ToLower(keyword))
		if keyword == "" {
			continue
		}
		if _, exists := seen[keyword]; exists {
			continue
		}
		seen[keyword] = struct{}{}
		normalized = append(normalized, keyword)
	}
	return normalized
}

func scoreDocument(doc Document, rawKeywords []string) int {
	score := 0
	joined := strings.ToLower(strings.Join([]string{
		doc.Path,
		doc.Title,
		doc.Summary,
		strings.Join(doc.Keywords, " "),
	}, "\n"))
	for _, keyword := range expandKeywords(rawKeywords) {
		if strings.Contains(joined, keyword) {
			score++
		}
	}
	return score
}

func expandKeywords(rawKeywords []string) []string {
	seen := map[string]struct{}{}
	keywords := make([]string, 0, len(rawKeywords)*2)
	for _, raw := range rawKeywords {
		raw = strings.TrimSpace(strings.ToLower(raw))
		if raw == "" {
			continue
		}
		if _, exists := seen[raw]; !exists {
			seen[raw] = struct{}{}
			keywords = append(keywords, raw)
		}
		for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
			switch {
			case r >= 'a' && r <= 'z':
				return false
			case r >= '0' && r <= '9':
				return false
			case r >= 0x4e00 && r <= 0x9fff:
				return false
			default:
				return true
			}
		}) {
			if len([]rune(token)) < 2 {
				continue
			}
			if _, exists := seen[token]; exists {
				continue
			}
			seen[token] = struct{}{}
			keywords = append(keywords, token)
		}
	}
	return keywords
}
