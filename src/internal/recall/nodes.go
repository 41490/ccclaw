package recall

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MemoryNode 表示 kb/ 下一条记忆条目的置信度元数据。
type MemoryNode struct {
	ID       string   `json:"id"`        // "<type>:<relative_path_from_kb_root>"
	Tags     []string `json:"tags"`      // 标签，用于 tag_match 评分
	UseCount int      `json:"use_count"` // 被命中次数
	LastUsed string   `json:"last_used"` // YYYY-MM-DD
	Status   string   `json:"status"`    // active|dormant|archived|candidate
	Score    float64  `json:"score"`     // 上次 recall 计算的缓存值
}

// LoadNodes 从 JSONL 文件加载节点列表。文件不存在时返回空列表（不报错）。
func LoadNodes(path string) ([]MemoryNode, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("打开 nodes.jsonl 失败: %w", err)
	}
	defer f.Close()

	var nodes []MemoryNode
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var n MemoryNode
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			return nil, fmt.Errorf("解析 nodes.jsonl 行失败: %w", err)
		}
		nodes = append(nodes, n)
	}
	return nodes, scanner.Err()
}

// SaveNodes 原子覆写 nodes.jsonl（临时文件 + rename）。
func SaveNodes(path string, nodes []MemoryNode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 memory 目录失败: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	for _, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("序列化节点失败: %w", err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("写入节点失败: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	return os.Rename(tmp, path)
}

// RebuildNodes 扫描 kbRoot 下所有记忆文件重建节点列表。
// skill 节点从 frontmatter 恢复 use_count/last_used；其他节点 use_count 归零。
func RebuildNodes(kbRoot string) ([]MemoryNode, error) {
	var nodes []MemoryNode
	dirs := []struct {
		subdir   string
		nodeType string
	}{
		{"skills/L1", "skill"},
		{"skills/L2", "skill"},
		{"journal", "journal"},
		{"designs", "design"},
		{"assay", "assay"},
	}
	for _, d := range dirs {
		dir := filepath.Join(kbRoot, d.subdir)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".md" {
				return nil
			}
			rel, err := filepath.Rel(kbRoot, path)
			if err != nil {
				return err
			}
			id := d.nodeType + ":" + filepath.ToSlash(rel)
			node := buildNode(id, path, d.nodeType)
			nodes = append(nodes, node)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("扫描 %s 失败: %w", d.subdir, err)
		}
	}
	return nodes, nil
}

type skillFrontmatter struct {
	Keywords []string `yaml:"keywords"`
	UseCount int      `yaml:"use_count"`
	LastUsed string   `yaml:"last_used"`
	Status   string   `yaml:"status"`
}

func buildNode(id, path, nodeType string) MemoryNode {
	node := MemoryNode{
		ID:     id,
		Status: "active",
	}
	data, err := os.ReadFile(path)
	if err != nil {
		node.Tags = tagsFromPath(id)
		return node
	}
	content := string(data)
	if nodeType == "skill" {
		if fm, ok := parseFrontmatter(content); ok {
			node.UseCount = fm.UseCount
			node.LastUsed = fm.LastUsed
			if fm.Status != "" {
				node.Status = fm.Status
			}
			node.Tags = fm.Keywords
		}
	}
	if len(node.Tags) == 0 {
		node.Tags = tagsFromPath(id)
	}
	if node.LastUsed == "" {
		info, err := os.Stat(path)
		if err == nil {
			node.LastUsed = info.ModTime().Format("2006-01-02")
		}
	}
	return node
}

func parseFrontmatter(content string) (*skillFrontmatter, bool) {
	if !strings.HasPrefix(content, "---") {
		return nil, false
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return nil, false
	}
	return &fm, true
}

func tagsFromPath(id string) []string {
	parts := strings.Split(id, ":")
	if len(parts) < 2 {
		return nil
	}
	segments := strings.Split(filepath.ToSlash(parts[1]), "/")
	var tags []string
	for _, seg := range segments {
		seg = strings.TrimSuffix(seg, ".md")
		seg = strings.TrimSuffix(seg, ".CLAUDE")
		if seg == "" || seg == "skills" || seg == "L1" || seg == "L2" || seg == "journal" || seg == "designs" || seg == "assay" {
			continue
		}
		tags = append(tags, seg)
	}
	return tags
}
