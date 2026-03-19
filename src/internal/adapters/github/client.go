package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const defaultTimeout = 20 * time.Second
const ApprovalPrefix = "/ccclaw"
const DoneMarker = "/ccclaw [DONE]"

type Label struct {
	Name string `json:"name"`
}

type User struct {
	Login string `json:"login"`
}

type Issue struct {
	Repo        string    `json:"-"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason string    `json:"state_reason,omitempty"`
	Labels      []Label   `json:"labels"`
	User        User      `json:"user"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullReq     any       `json:"pull_request,omitempty"`
}

type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Approval struct {
	Approved  bool
	Rejected  bool
	Actor     string
	CommentID int64
	Command   string
}

type Client struct {
	repo    string
	secrets map[string]string
	cache   map[string]string
}

func NewClient(repo string, secrets map[string]string) *Client {
	cloned := make(map[string]string, len(secrets))
	for key, value := range secrets {
		cloned[key] = value
	}
	return &Client{repo: repo, secrets: cloned, cache: map[string]string{}}
}

func (c *Client) ListOpenIssues(label string, limit int) ([]Issue, error) {
	endpoint := fmt.Sprintf("repos/%s/issues?state=open&per_page=%d", c.repo, limit)
	if label != "" {
		endpoint += "&labels=" + label
	}
	out, err := c.runGH(context.Background(), "api", endpoint)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("解析 issue 列表失败: %w", err)
	}
	filtered := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.PullReq != nil {
			continue
		}
		issue.Repo = c.repo
		filtered = append(filtered, issue)
	}
	return filtered, nil
}

func (c *Client) GetIssue(number int) (*Issue, error) {
	out, err := c.runGH(context.Background(), "api", fmt.Sprintf("repos/%s/issues/%d", c.repo, number))
	if err != nil {
		return nil, err
	}
	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("解析 issue 失败: %w", err)
	}
	issue.Repo = c.repo
	return &issue, nil
}

func (c *Client) ListComments(number int) ([]Comment, error) {
	out, err := c.runGH(context.Background(), "api", fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", c.repo, number))
	if err != nil {
		return nil, err
	}
	var comments []Comment
	if err := json.Unmarshal(out, &comments); err != nil {
		return nil, fmt.Errorf("解析 issue 评论失败: %w", err)
	}
	return comments, nil
}

func (c *Client) AddComment(number int, body string) (*Comment, error) {
	out, err := c.runGH(context.Background(), "api", fmt.Sprintf("repos/%s/issues/%d/comments", c.repo, number), "-f", "body="+body)
	if err != nil {
		return nil, fmt.Errorf("回写 issue 评论失败: %w", err)
	}
	var comment Comment
	if err := json.Unmarshal(out, &comment); err != nil {
		return nil, fmt.Errorf("解析 issue 评论回写结果失败: %w", err)
	}
	return &comment, nil
}

func (c *Client) UpdateComment(commentID int64, body string) (*Comment, error) {
	out, err := c.runGH(context.Background(), "api", fmt.Sprintf("repos/%s/issues/comments/%d", c.repo, commentID), "--method", "PATCH", "-f", "body="+body)
	if err != nil {
		return nil, fmt.Errorf("更新 issue 评论失败: %w", err)
	}
	var comment Comment
	if err := json.Unmarshal(out, &comment); err != nil {
		return nil, fmt.Errorf("解析 issue 评论更新结果失败: %w", err)
	}
	return &comment, nil
}

func (c *Client) CreateIssue(title, body string, labels []string) (*Issue, error) {
	args := []string{
		"api",
		fmt.Sprintf("repos/%s/issues", c.repo),
		"--method", "POST",
		"-f", "title=" + title,
		"-f", "body=" + body,
	}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		args = append(args, "-f", "labels[]="+label)
	}
	out, err := c.runGH(context.Background(), args...)
	if err != nil {
		return nil, fmt.Errorf("创建 issue 失败: %w", err)
	}
	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("解析新建 issue 失败: %w", err)
	}
	issue.Repo = c.repo
	return &issue, nil
}

func (c *Client) NetworkCheck(ctx context.Context) error {
	_, err := c.runGH(ctx, "api", "rate_limit")
	return err
}

func (c *Client) IsTrustedActor(username, minimumPermission string) (bool, string, error) {
	permission, err := c.permissionOf(username)
	if err != nil {
		return false, "", err
	}
	return permissionRank(permission) >= permissionRank(minimumPermission), permission, nil
}

func (c *Client) FindApproval(number int, words, rejectWords []string, minimumPermission string) (*Approval, error) {
	comments, err := c.ListComments(number)
	if err != nil {
		return nil, err
	}
	return c.FindApprovalInComments(comments, words, rejectWords, minimumPermission)
}

func (c *Client) FindApprovalInComments(comments []Comment, words, rejectWords []string, minimumPermission string) (*Approval, error) {
	for idx := len(comments) - 1; idx >= 0; idx-- {
		comment := comments[idx]
		match, ok := MatchApprovalCommand(comment.Body, words, rejectWords)
		if !ok {
			continue
		}
		trusted, _, err := c.IsTrustedActor(comment.User.Login, minimumPermission)
		if err != nil {
			return nil, err
		}
		if trusted {
			return &Approval{
				Approved:  match.Approved,
				Rejected:  match.Rejected,
				Actor:     comment.User.Login,
				CommentID: comment.ID,
				Command:   match.Command,
			}, nil
		}
	}
	return &Approval{}, nil
}

type ApprovalCommandMatch struct {
	Approved bool
	Rejected bool
	Command  string
}

func MatchApprovalCommand(body string, words, rejectWords []string) (ApprovalCommandMatch, bool) {
	approveSet := approvalWordSet(words)
	rejectSet := approvalWordSet(rejectWords)
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		if !strings.EqualFold(fields[0], ApprovalPrefix) {
			continue
		}
		word := strings.ToLower(strings.TrimSpace(fields[1]))
		command := ApprovalPrefix + " " + word
		if _, ok := rejectSet[word]; ok {
			return ApprovalCommandMatch{Rejected: true, Command: command}, true
		}
		if _, ok := approveSet[word]; ok {
			return ApprovalCommandMatch{Approved: true, Command: command}, true
		}
	}
	return ApprovalCommandMatch{}, false
}

func HasDoneMarker(body string) bool {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := strings.TrimSpace(lines[idx])
		if line == "" {
			continue
		}
		return strings.EqualFold(line, DoneMarker)
	}
	return false
}

func FindDoneComment(comments []Comment, currentDoneCommentID int64) *Comment {
	if currentDoneCommentID != 0 {
		for _, comment := range comments {
			if comment.ID != currentDoneCommentID {
				continue
			}
			if HasDoneMarker(comment.Body) {
				copy := comment
				return &copy
			}
			return nil
		}
		return nil
	}
	for idx := len(comments) - 1; idx >= 0; idx-- {
		if !HasDoneMarker(comments[idx].Body) {
			continue
		}
		copy := comments[idx]
		return &copy
	}
	return nil
}

func approvalWordSet(words []string) map[string]struct{} {
	set := make(map[string]struct{}, len(words))
	for _, word := range words {
		candidate := strings.ToLower(strings.TrimSpace(word))
		if candidate == "" {
			continue
		}
		set[candidate] = struct{}{}
	}
	return set
}

func (c *Client) permissionOf(username string) (string, error) {
	if permission, ok := c.cache[username]; ok {
		return permission, nil
	}
	out, err := c.runGH(context.Background(), "api", fmt.Sprintf("repos/%s/collaborators/%s/permission", c.repo, username))
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			c.cache[username] = "none"
			return "none", nil
		}
		return "", err
	}
	var payload struct {
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("解析权限失败: %w", err)
	}
	if payload.Permission == "" {
		payload.Permission = "none"
	}
	c.cache[username] = payload.Permission
	return payload.Permission, nil
}

func (c *Client) runGH(parent context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, defaultTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(cmd.Environ(), c.envPairs()...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh %s 失败: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (c *Client) envPairs() []string {
	pairs := make([]string, 0, len(c.secrets))
	for key, value := range c.secrets {
		pairs = append(pairs, key+"="+value)
	}
	return pairs
}

func permissionRank(permission string) int {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "admin":
		return 5
	case "maintain":
		return 4
	case "write":
		return 3
	case "triage":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

func LabelNames(labels []Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}

func HasLabel(labels []Label, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), want) {
			return true
		}
	}
	return false
}

func IssueURL(repo string, number int) string {
	return "https://github.com/" + repo + "/issues/" + strconv.Itoa(number)
}
