package main

import (
	"strings"

	"github.com/41490/ccclaw/internal/app"
	"github.com/spf13/cobra"
)

func addRecallCommand(rootCmd *cobra.Command, configPath, envFile *string) {
	var (
		issueNum int
		tags     string
		cold     bool
		rebuild  bool
	)
	cmd := &cobra.Command{
		Use:   "recall",
		Short: "生成 kb/context.md（置信度评分，只加载相关记忆）",
		Long: `recall 扫描 kb/memory/nodes.jsonl，按置信度评分后生成 kb/context.md。
在任务完成后调用以更新下次会话的上下文。

示例：
  ccclaw recall --issue 77
  ccclaw recall --tags memory,architecture
  ccclaw recall --cold
  ccclaw recall --rebuild`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := app.NewRuntime(*configPath, *envFile)
			if err != nil {
				return err
			}
			var tagList []string
			for _, t := range strings.Split(tags, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tagList = append(tagList, t)
				}
			}
			return rt.Recall(app.RecallOptions{
				IssueNum: issueNum,
				Tags:     tagList,
				Cold:     cold,
				Rebuild:  rebuild,
			}, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&issueNum, "issue", 0, "从 GitHub Issue 提取 task tags（指定 Issue 编号）")
	cmd.Flags().StringVar(&tags, "tags", "", "显式指定 task tags（逗号分隔）")
	cmd.Flags().BoolVar(&cold, "cold", false, "冷启动模式，tag_match=0")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "强制重建 nodes.jsonl（丢失后恢复用）")
	rootCmd.AddCommand(cmd)
}
