package notify

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Message is the human-readable content of an event, shared by the
// Feishu and Slack formats.
type Message struct {
	Title  string
	Fields []Field
	// Note is an optional closing sentence.
	Note     string
	LinkText string
	Link     string
}

// Field is one labelled line of a message.
type Field struct {
	Label string
	Value string
}

// statusLabels name the run statuses that produce notifications.
var statusLabels = map[string]string{
	"failed":      "执行失败",
	"timed_out":   "已超时",
	"interrupted": "运行中断",
}

// errorLabels name the error codes a run can record. They follow the
// problem titles of the API.
var errorLabels = map[string]string{ // #nosec G101 -- labels; "credential" in the keys trips the heuristic
	"dependency_install_failed": "依赖安装失败",
	"script_error":              "脚本执行失败",
	"timeout":                   "执行超时",
	"out_of_memory":             "内存超出上限",
	"run_interrupted":           "运行被中断",
	"runner_unavailable":        "没有可用的 runner",
	"internal_error":            "服务端内部错误",
	"credential_missing":        "缺少凭证",
	"credential_unavailable":    "凭证不可用",
}

// Compose renders e as a message. Times are shown in loc, UTC when nil.
func Compose(e Event, loc *time.Location) Message {
	if loc == nil {
		loc = time.UTC
	}
	taskName := ""
	if e.Task != nil {
		taskName = e.Task.Name
	}
	switch e.Type {
	case EventRunFailed, EventRunTimedOut, EventRunInterrupted:
		return composeRun(e, taskName)
	case EventCredentialExpiring:
		m := Message{Title: "凭证即将到期", LinkText: "查看详情", Link: e.Link}
		if e.Credential != nil {
			m.Title = "凭证 " + e.Credential.Name + " 即将到期"
		}
		m.Fields = append(m.Fields, Field{"项目", e.Project.Name})
		if taskName != "" {
			m.Fields = append(m.Fields, Field{"使用它的任务", taskName})
		}
		if e.Credential != nil {
			m.Fields = append(m.Fields, Field{"到期时间", formatTime(e.Credential.ExpiresAt, loc)})
		}
		m.Note = "到期后，引用它的运行会在准备阶段失败。请在到期前更新。"
		return m
	default:
		return Message{
			Title:    "Lerryn 测试通知",
			Note:     "这个渠道可以正常收到 Lerryn 的通知。",
			LinkText: "打开控制台",
			Link:     e.Link,
		}
	}
}

func composeRun(e Event, taskName string) Message {
	status := ""
	if e.Run != nil {
		status = statusLabels[e.Run.Status]
	}
	if status == "" {
		status = statusLabels[strings.TrimPrefix(e.Type, "run_")]
	}
	m := Message{Title: "任务 " + taskName + " " + status, LinkText: "查看运行", Link: e.Link}
	m.Fields = append(m.Fields, Field{"项目", e.Project.Name})
	if r := e.Run; r != nil {
		m.Fields = append(m.Fields, Field{"状态", status})
		if r.ErrorCode != nil {
			label := errorLabels[*r.ErrorCode]
			if label == "" {
				label = *r.ErrorCode
			}
			m.Fields = append(m.Fields, Field{"错误类别", label + "（" + *r.ErrorCode + "）"})
		}
		if r.Reason != nil && *r.Reason != "" {
			m.Fields = append(m.Fields, Field{"原因", *r.Reason})
		}
		m.Fields = append(m.Fields, Field{"版本", fmt.Sprintf("第 %d 版，第 %d 次尝试", r.Version, r.Attempt)})
		if r.Link != "" {
			m.Link = r.Link
		}
	}
	if e.MergedCount > 1 {
		m.Note = fmt.Sprintf("这个任务连续失败了 %d 次，合并为这一条通知，内容为最近一次运行。", e.MergedCount)
	}
	return m
}

func formatTime(t time.Time, loc *time.Location) string {
	t = t.In(loc)
	name, _ := t.Zone()
	return t.Format("2006-01-02 15:04 ") + name
}

// tokenPattern matches Lerryn tokens and bearer tokens. Runners redact
// credential values before error messages leave the host (tech design
// 12.6); this is a second line of defence for tokens that were never
// declared as credentials.
var tokenPattern = regexp.MustCompile(`\bwl_[a-z]+_[A-Za-z0-9_-]{8,}|(?i:bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)

// Reason turns a run's error message into the reason of a notification:
// tokens masked, control characters and runs of white space folded into
// single spaces, at most MaxReasonRunes characters.
func Reason(message string) string {
	s := tokenPattern.ReplaceAllString(message, "[已隐藏]")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	runes := []rune(b.String())
	if len(runes) > MaxReasonRunes {
		return string(runes[:MaxReasonRunes-1]) + "…"
	}
	return string(runes)
}
