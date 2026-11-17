package production

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// conversationLabelDefaults is the Markdown wording in the source language; the export request may replace any
// entry with the viewer's language.
var conversationLabelDefaults = map[string]string{
	"host":                      "主机",
	"unlinked":                  "未关联主机",
	"status":                    "状态",
	"created":                   "创建时间",
	"updated":                   "最后活动",
	"runs":                      "运行次数",
	"user":                      "用户",
	"assistant":                 "AI",
	"interrupted":               "（回答已中断）",
	"images":                    "附带 {count} 张图片",
	"tool":                      "工具调用",
	"result":                    "结果",
	"failed":                    "失败",
	"exitCode":                  "退出码",
	"fileChange":                "文件变更",
	"plan":                      "提交的方案",
	"summary":                   "上下文摘要",
	"approvals":                 "确认记录",
	"pending":                   "未处理",
	"question":                  "提问",
	"status.running":            "运行中",
	"status.interrupted":        "已中断",
	"status.completed":          "已完成",
	"status.failed":             "失败",
	"status.idle":               "未运行",
	"decision.allow":            "已允许一次",
	"decision.allow_session":    "本会话已允许此类",
	"decision.allow_persistent": "已永久允许",
	"decision.deny":             "已拒绝",
	"decision.answered":         "已回答",
	"decision.expired":          "已过期",
}

type conversationLabels map[string]string

func newConversationLabels(requested map[string]string) conversationLabels {
	labels := make(conversationLabels, len(conversationLabelDefaults))
	for key, value := range conversationLabelDefaults {
		labels[key] = value
	}
	for key, value := range requested {
		if _, known := conversationLabelDefaults[key]; known && strings.TrimSpace(value) != "" && len(value) <= 200 {
			labels[key] = value
		}
	}
	return labels
}

func (l conversationLabels) get(key string) string {
	if value, ok := l[key]; ok {
		return value
	}
	return key
}

type conversationDocument struct {
	row       store.ConvOverviewRow
	host      string
	messages  []store.MessageRow
	approvals []aiConversationApprovalDTO
}

type conversationHistoryRow struct {
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Interim    bool            `json:"interim"`
	Partial    bool            `json:"partial"`
	ImageCount int             `json:"imageCount"`
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args"`
	Tool       string          `json:"tool"`
	OK         bool            `json:"ok"`
	Summary    json.RawMessage `json:"summary"`
	ExitCode   int             `json:"exitCode"`
	Panic      bool            `json:"panic"`
	Path       string          `json:"path"`
	Before     string          `json:"before"`
	After      string          `json:"after"`
	Plan       string          `json:"plan"`
}

const conversationArgsPreview = 300

// renderConversationMarkdown writes the user and assistant text, one line per tool call with its outcome, file
// changes as unified diffs, submitted plans, context summaries and the confirmation record. Reasoning and the skill
// catalog sent to the model are left out.
func renderConversationMarkdown(out io.Writer, document conversationDocument, labels conversationLabels, location *time.Location) error {
	w := bufio.NewWriter(out)
	row := document.row
	title := strings.TrimSpace(row.Title)
	if title == "" {
		title = row.ID
	}
	fmt.Fprintf(w, "# %s\n\n", markdownLine(title))
	when := func(ms int64) string { return time.UnixMilli(ms).In(location).Format("2006-01-02 15:04:05") }
	fmt.Fprintf(w, "- %s: %s\n", labels.get("host"), markdownLine(document.host))
	fmt.Fprintf(w, "- %s: %s\n", labels.get("status"), labels.get("status."+row.Status))
	fmt.Fprintf(w, "- %s: %s\n", labels.get("created"), when(row.CreatedAt))
	fmt.Fprintf(w, "- %s: %s\n", labels.get("updated"), when(row.UpdatedAt))
	fmt.Fprintf(w, "- %s: %d\n", labels.get("runs"), row.RunCount)

	pending := map[string]int{}
	type toolLine struct {
		name, args string
		done       bool
		ok, panic  bool
		exitCode   int
		summary    string
	}
	tools := []*toolLine{}
	flushTools := func() {
		if len(tools) == 0 {
			return
		}
		fmt.Fprintf(w, "\n**%s**\n\n", labels.get("tool"))
		for _, tool := range tools {
			line := "- `" + strings.ReplaceAll(tool.name, "`", "'") + "`"
			if tool.args != "" {
				line += " " + inlineCode(tool.args)
			}
			if tool.done {
				outcome := labels.get("result")
				if !tool.ok || tool.panic {
					outcome = labels.get("failed")
				}
				if tool.exitCode != 0 {
					outcome += fmt.Sprintf(" (%s %d)", labels.get("exitCode"), tool.exitCode)
				}
				if tool.summary != "" {
					outcome += ": " + markdownLine(tool.summary)
				}
				line += " - " + outcome
			}
			fmt.Fprintln(w, line)
		}
		tools = tools[:0]
		pending = map[string]int{}
	}
	for _, message := range document.messages {
		var item conversationHistoryRow
		if json.Unmarshal([]byte(message.ContentJSON), &item) != nil {
			continue
		}
		switch item.Type {
		case "toolCall":
			pending[item.ID] = len(tools)
			tools = append(tools, &toolLine{name: item.Name, args: argsPreview(item.Args)})
			continue
		case "toolResult":
			index, ok := pending[item.ID]
			if !ok {
				index = len(tools)
				tools = append(tools, &toolLine{name: item.Tool})
			}
			delete(pending, item.ID)
			tool := tools[index]
			tool.done, tool.ok, tool.panic, tool.exitCode, tool.summary = true, item.OK, item.Panic, item.ExitCode, jsonText(item.Summary)
			continue
		}
		flushTools()
		switch item.Type {
		case "fileChange":
			fmt.Fprintf(w, "\n**%s** `%s`\n\n", labels.get("fileChange"), strings.ReplaceAll(item.Path, "`", "'"))
			writeFenced(w, "diff", unifiedDiff(item.Path, item.Before, item.After))
		case "planSubmitted":
			fmt.Fprintf(w, "\n**%s**\n\n%s\n", labels.get("plan"), strings.TrimSpace(item.Plan))
		case "summary":
			fmt.Fprintf(w, "\n> **%s**\n>\n", labels.get("summary"))
			for _, line := range strings.Split(strings.TrimSpace(jsonText(item.Summary)), "\n") {
				fmt.Fprintf(w, "> %s\n", line)
			}
		case "":
			text := strings.TrimSpace(item.Content)
			if text == "" {
				continue
			}
			if message.Role == "user" {
				fmt.Fprintf(w, "\n## %s · %s\n\n", labels.get("user"), when(message.CreatedAt))
				if item.ImageCount > 0 {
					fmt.Fprintf(w, "_%s_\n\n", strings.ReplaceAll(labels.get("images"), "{count}", fmt.Sprint(item.ImageCount)))
				}
				fmt.Fprintln(w, text)
			} else if message.Role == "assistant" {
				if !item.Interim {
					fmt.Fprintf(w, "\n## %s · %s\n\n", labels.get("assistant"), when(message.CreatedAt))
				} else {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, text)
				if item.Partial {
					fmt.Fprintf(w, "\n_%s_\n", labels.get("interrupted"))
				}
			}
		}
	}
	flushTools()
	if len(document.approvals) > 0 {
		fmt.Fprintf(w, "\n## %s\n\n", labels.get("approvals"))
		for _, approval := range document.approvals {
			decision := labels.get("pending")
			if approval.Decision != "" {
				decision = labels.get("decision." + approval.Decision)
			}
			what := approval.Rendered
			if approval.Kind == "question" {
				what = labels.get("question") + ": " + approval.Question
			} else if what == "" {
				what = approval.Tool
			}
			fmt.Fprintf(w, "- %s %s - %s\n", when(approval.RequestedAt), inlineCode(what), decision)
		}
	}
	return w.Flush()
}

// jsonText reads a field the agent saves as a string; anything else is shown as its JSON.
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func argsPreview(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "{}" || text == "null" {
		return ""
	}
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > conversationArgsPreview {
		return string(runes[:conversationArgsPreview]) + "..."
	}
	return string(runes)
}

func markdownLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// inlineCode wraps text in a code span whose fence is longer than any backtick run inside it.
func inlineCode(text string) string {
	text = markdownLine(text)
	fence := strings.Repeat("`", longestRun(text, '`')+1)
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
		return fence + " " + text + " " + fence
	}
	return fence + text + fence
}

func writeFenced(w io.Writer, language, body string) {
	fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
	fmt.Fprintf(w, "%s%s\n%s", fence, language, body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, fence)
}

func longestRun(text string, r rune) int {
	longest, current := 0, 0
	for _, c := range text {
		if c == r {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return longest
}

// unifiedDiffCells bounds the line comparison; larger files are shown as a whole replacement.
const unifiedDiffCells = 4_000_000

// unifiedDiff renders the change as a unified diff with three lines of context.
func unifiedDiff(path, before, after string) string {
	a, b := splitDiffLines(before), splitDiffLines(after)
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	type op struct {
		kind byte
		text string
	}
	ops := []op{}
	if len(a)*len(b) > unifiedDiffCells {
		for _, line := range a {
			ops = append(ops, op{'-', line})
		}
		for _, line := range b {
			ops = append(ops, op{'+', line})
		}
	} else {
		lcs := make([][]int32, len(a)+1)
		for i := range lcs {
			lcs[i] = make([]int32, len(b)+1)
		}
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(a) || j < len(b) {
			switch {
			case i < len(a) && j < len(b) && a[i] == b[j]:
				ops = append(ops, op{' ', a[i]})
				i++
				j++
			case j < len(b) && (i == len(a) || lcs[i][j+1] > lcs[i+1][j]):
				ops = append(ops, op{'+', b[j]})
				j++
			default:
				ops = append(ops, op{'-', a[i]})
				i++
			}
		}
	}
	const context = 3
	for start := 0; start < len(ops); {
		if ops[start].kind == ' ' {
			start++
			continue
		}
		first := max(0, start-context)
		last := start
		for k := start; k < len(ops); k++ {
			if ops[k].kind != ' ' {
				last = k
			} else if k-last > 2*context {
				break
			}
		}
		end := min(len(ops), last+context+1)
		oldStart, newStart := 1, 1
		for _, previous := range ops[:first] {
			if previous.kind != '+' {
				oldStart++
			}
			if previous.kind != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, current := range ops[first:end] {
			if current.kind != '+' {
				oldCount++
			}
			if current.kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, current := range ops[first:end] {
			out.WriteByte(current.kind)
			out.WriteString(current.text)
			out.WriteByte('\n')
		}
		start = end
	}
	return out.String()
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
