package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/41490/ccclaw/internal/recall"
)

// RecallOptions 控制 recall 行为。
type RecallOptions struct {
	IssueNum int      // > 0 时从 GitHub Issue 提取 tags
	Tags     []string // 显式追加的 task tags
	Cold     bool     // 冷启动模式，tag_match=0
	Rebuild  bool     // 强制重建 nodes.jsonl
	Now      time.Time
}

// Recall 执行置信度评分，生成 kb/context.md。
func (rt *Runtime) Recall(opts RecallOptions, out io.Writer) error {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	kbRoot := rt.memRoot
	nodesPath := filepath.Join(kbRoot, "memory", "nodes.jsonl")
	contextPath := filepath.Join(kbRoot, "context.md")
	maxLines := rt.cfg.KB.ContextMaxLines
	if maxLines <= 0 {
		maxLines = 256
	}

	// 1. 加载或重建 nodes.jsonl
	nodes, err := rt.loadOrRebuildNodes(nodesPath, kbRoot, opts.Rebuild, out)
	if err != nil {
		return err
	}

	// 2. 若有 --issue N，提取 task tags（带超时，失败降级到 cold）
	taskTags := append([]string{}, opts.Tags...)
	if opts.IssueNum > 0 && !opts.Cold {
		issueTags, err := rt.fetchIssueTags(opts.IssueNum)
		if err != nil {
			rt.logWarning("recall", "获取 Issue tags 失败，降级到冷启动", "issue", opts.IssueNum, "error", err)
			_, _ = fmt.Fprintf(out, "警告: 获取 Issue #%d tags 失败，降级冷启动: %v\n", opts.IssueNum, err)
			opts.Cold = true
		} else {
			taskTags = append(taskTags, issueTags...)
		}
	}

	// 3. 计算每个节点的 score 并更新 status
	scored := make([]recall.MemoryNode, len(nodes))
	for i, n := range nodes {
		if n.Status == "candidate" || n.Status == "archived" {
			scored[i] = n
			continue
		}
		lastUsed, _ := time.ParseInLocation("2006-01-02", n.LastUsed, time.Local)
		scoreInput := recall.ScoreInput{
			UseCount: n.UseCount,
			LastUsed: lastUsed,
			NodeTags: n.Tags,
			Now:      opts.Now,
		}
		if !opts.Cold {
			scoreInput.TaskTags = taskTags
		}
		s := recall.Score(scoreInput)
		n.Score = s
		n.Status = recall.Classify(s, lastUsed, opts.Now)
		scored[i] = n
	}

	// 4. 生成 context.md
	triggerDesc := "cold:journal-timer"
	if !opts.Cold {
		if opts.IssueNum > 0 {
			triggerDesc = fmt.Sprintf("recall:issue=%d", opts.IssueNum)
		} else {
			triggerDesc = "recall:manual"
		}
	}
	ctxContent := recall.BuildContext(recall.ContextInput{
		Nodes:       scored,
		TaskTags:    taskTags,
		Cold:        opts.Cold,
		TriggerDesc: triggerDesc,
		KBRoot:      kbRoot,
		MaxLines:    maxLines,
		Now:         opts.Now,
	})
	if err := os.MkdirAll(filepath.Dir(contextPath), 0o755); err != nil {
		return fmt.Errorf("创建 kb 目录失败: %w", err)
	}
	if err := atomicWriteFile(contextPath, []byte(ctxContent)); err != nil {
		return fmt.Errorf("写入 context.md 失败: %w", err)
	}

	// 5. 被命中的节点 use_count+1，last_used 更新
	for i, n := range scored {
		if n.Score >= 0.3 && n.Status != "archived" && n.Status != "candidate" {
			scored[i].UseCount++
			scored[i].LastUsed = opts.Now.Format("2006-01-02")
		}
	}

	// 6. 归档 status=archived 的节点对应文件
	for _, n := range scored {
		if n.Status == "archived" {
			rt.archiveMemoryNode(n, kbRoot)
		}
	}

	// 7. 原子写回 nodes.jsonl
	if err := recall.SaveNodes(nodesPath, scored); err != nil {
		return fmt.Errorf("写入 nodes.jsonl 失败: %w", err)
	}

	_, _ = fmt.Fprintf(out, "recall 完成: context.md 已更新 (%s)\n", contextPath)
	return nil
}

func (rt *Runtime) loadOrRebuildNodes(nodesPath, kbRoot string, forceRebuild bool, out io.Writer) ([]recall.MemoryNode, error) {
	if forceRebuild {
		_, _ = fmt.Fprintf(out, "WARN: 重建 nodes.jsonl（use_count 对非 skill 条目将归零）\n")
		return recall.RebuildNodes(kbRoot)
	}
	nodes, err := recall.LoadNodes(nodesPath)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		_, _ = fmt.Fprintf(out, "WARN: nodes.jsonl 不存在，自动重建（use_count 对非 skill 条目将归零）\n")
		return recall.RebuildNodes(kbRoot)
	}
	return nodes, nil
}

func (rt *Runtime) fetchIssueTags(issueNum int) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ctx // client.GetIssue 内部使用 defaultTimeout，此处超时作为上层保护
	client := rt.clientForRepo(rt.cfg.GitHub.ControlRepo)
	issue, err := client.GetIssue(issueNum)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, label := range issue.Labels {
		if strings.TrimSpace(label.Name) != "" {
			tags = append(tags, strings.ToLower(strings.ReplaceAll(label.Name, " ", "-")))
		}
	}
	// 从 body 的 keywords 行提取（格式：keywords: foo, bar）
	for _, line := range strings.Split(issue.Body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "keywords:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				for _, kw := range strings.Split(parts[1], ",") {
					kw = strings.TrimSpace(kw)
					if kw != "" {
						tags = append(tags, strings.ToLower(kw))
					}
				}
			}
		}
	}
	return tags, nil
}

func (rt *Runtime) archiveMemoryNode(n recall.MemoryNode, kbRoot string) {
	rel := recall.RelativeID(n.ID)
	src := filepath.Join(kbRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return
	}
	// 仅归档 skills 路径（保守策略）
	if !strings.HasPrefix(rel, "skills/") {
		return
	}
	archivedRel := strings.Replace(rel, "skills/L1/", "skills/archived/", 1)
	archivedRel = strings.Replace(archivedRel, "skills/L2/", "skills/archived/", 1)
	dst := filepath.Join(kbRoot, filepath.FromSlash(archivedRel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		rt.logWarning("recall", "创建 archived 目录失败", "error", err)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		rt.logWarning("recall", "归档 skill 失败", "src", src, "error", err)
	}
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
