package recall

import (
	"math"
	"time"
)

// ScoreInput 包含评分所需的全部输入。TaskTags 为空时等同于冷启动模式（tag_match=0）。
type ScoreInput struct {
	UseCount int
	LastUsed time.Time
	TaskTags []string
	NodeTags []string
	Now      time.Time
}

// Score 计算单个节点的置信度分数。
//
//	score = (use_count_norm × 0.4) + (recency × 0.4) + (tag_match × 0.2)
//	use_count_norm = min(use_count / 20, 1.0)
//	recency        = max(0, 1 - Δdays / 90)
//	tag_match      = |NodeTags ∩ TaskTags| / max(len(TaskTags), 1)   冷启动时=0
func Score(input ScoreInput) float64 {
	useNorm := math.Min(float64(input.UseCount)/20.0, 1.0)

	recency := 0.0
	if !input.LastUsed.IsZero() {
		days := input.Now.Sub(input.LastUsed).Hours() / 24.0
		recency = math.Max(0, 1.0-days/90.0)
	}

	tagMatch := 0.0
	if len(input.TaskTags) > 0 {
		nodeSet := make(map[string]struct{}, len(input.NodeTags))
		for _, t := range input.NodeTags {
			nodeSet[t] = struct{}{}
		}
		var hits int
		for _, t := range input.TaskTags {
			if _, ok := nodeSet[t]; ok {
				hits++
			}
		}
		tagMatch = float64(hits) / float64(len(input.TaskTags))
	}

	return useNorm*0.4 + recency*0.4 + tagMatch*0.2
}

// Classify 根据分数和最后使用日期确定节点状态。
//
//	score ≥ 0.6                     → "active"
//	0.3 ≤ score < 0.6               → "dormant"
//	score < 0.3 且 Δdays > 180      → "archived"
//	score < 0.3 且 Δdays ≤ 180      → "dormant"
func Classify(score float64, lastUsed time.Time, now time.Time) string {
	switch {
	case score >= 0.6:
		return "active"
	case score >= 0.3:
		return "dormant"
	default:
		days := now.Sub(lastUsed).Hours() / 24.0
		if days > 180 {
			return "archived"
		}
		return "dormant"
	}
}
