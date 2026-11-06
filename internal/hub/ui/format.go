package ui

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// tone groups statuses into the colours the stylesheet knows.
type statusLook struct {
	Label string
	Tone  string
	Icon  string
}

var statusLooks = map[domain.Status]statusLook{
	domain.StatusSucceeded:       {"成功", "good", "check"},
	domain.StatusFailed:          {"失败", "bad", "x"},
	domain.StatusRunning:         {"运行中", "agent", "spin"},
	domain.StatusPending:         {"待开始", "neutral", "dashed"},
	domain.StatusQueued:          {"排队中", "neutral", "dashed"},
	domain.StatusWaiting:         {"等人处理", "human", "person"},
	domain.StatusWaitingInput:    {"等待输入", "human", "person"},
	domain.StatusWaitingResource: {"等资源", "warn", "dashed"},
	domain.StatusCancelled:       {"已取消", "neutral", "ban"},
	domain.StatusSkipped:         {"已跳过", "neutral", "skip"},
}

func lookOf(status domain.Status) statusLook {
	if look, ok := statusLooks[status]; ok {
		return look
	}
	return statusLook{Label: string(status), Tone: "neutral"}
}

// Icons are a handful of Lucide paths (ISC licence), drawn with currentColor.
var icons = map[string]string{
	"home":     `<path d="M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8"/><path d="M3 10a2 2 0 0 1 .709-1.528l7-5.999a2 2 0 0 1 2.582 0l7 5.999A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`,
	"activity": `<path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"/>`,
	"workflow": `<rect width="8" height="8" x="3" y="3" rx="2"/><path d="M7 11v4a2 2 0 0 0 2 2h4"/><rect width="8" height="8" x="13" y="13" rx="2"/>`,
	"server":   `<rect width="20" height="8" x="2" y="2" rx="2"/><rect width="20" height="8" x="2" y="14" rx="2"/><path d="M6 6h.01M6 18h.01"/>`,
	"check":    `<path d="M20 6 9 17l-5-5"/>`,
	"x":        `<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`,
	"spin":     `<path d="M21 12a9 9 0 1 1-6.219-8.56"/>`,
	"dashed":   `<circle cx="12" cy="12" r="9" stroke-dasharray="3.5 3.5"/>`,
	"person":   `<circle cx="12" cy="8" r="5"/><path d="M20 21a8 8 0 0 0-16 0"/>`,
	"ban":      `<circle cx="12" cy="12" r="9"/><path d="m5.7 5.7 12.6 12.6"/>`,
	"skip":     `<path d="m5 4 10 8-10 8z"/><path d="M19 5v14"/>`,
	"sun":      `<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>`,
	"moon":     `<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>`,
}

func icon(name string) template.HTML {
	paths, ok := icons[name]
	if !ok {
		return ""
	}
	class := "icon"
	if name == "spin" {
		class += " spin"
	}
	return template.HTML(`<svg class="` + class + `" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + paths + `</svg>`)
}

func badge(status domain.Status) template.HTML {
	look := lookOf(status)
	return template.HTML(fmt.Sprintf(`<span class="badge tone-%s" data-status="%s">%s%s</span>`,
		look.Tone, template.HTMLEscapeString(string(status)), icon(look.Icon), template.HTMLEscapeString(look.Label)))
}

func dot(status domain.Status) template.HTML {
	look := lookOf(status)
	return template.HTML(fmt.Sprintf(`<span class="dot tone-%s" title="%s"></span>`, look.Tone, template.HTMLEscapeString(look.Label)))
}

// clock is the time source for relative times; tests replace it.
var clock = time.Now

// ago renders a time the way the Loops console does: relative when recent.
func ago(t time.Time) template.HTML {
	if t.IsZero() {
		return `<span class="muted">—</span>`
	}
	local := t.Local()
	full := local.Format("2006-01-02 15:04:05")
	return template.HTML(fmt.Sprintf(`<time class="num nowrap" datetime="%s" title="%s">%s</time>`,
		t.UTC().Format(time.RFC3339), full, relative(local, clock().Local())))
}

func relative(t, now time.Time) string {
	elapsed := now.Sub(t)
	switch {
	case elapsed < 0:
		return t.Format("01-02 15:04")
	case elapsed < time.Minute:
		return "刚刚"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour && t.Day() == now.Day():
		return fmt.Sprintf("%d 小时前", int(elapsed.Hours()))
	case t.Year() == now.Year():
		yesterday := now.AddDate(0, 0, -1)
		if t.YearDay() == yesterday.YearDay() {
			return "昨天 " + t.Format("15:04")
		}
		return t.Format("01-02 15:04")
	default:
		return t.Format("2006-01-02")
	}
}

func clockTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// duration renders milliseconds compactly: 850ms, 12.3s, 4m 05s, 1h 12m, 2d 3h.
func duration(ms int64) string {
	switch d := time.Duration(ms) * time.Millisecond; {
	case ms <= 0:
		return "—"
	case d < time.Second:
		return fmt.Sprintf("%dms", ms)
	case d < time.Minute:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", d.Seconds()), ".0") + "s"
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// percent renders a rate in 0..1, or — for "no data" (negative).
func percent(rate float64) string {
	if rate < 0 {
		return "—"
	}
	if rate == 1 {
		return "100%"
	}
	return fmt.Sprintf("%.1f%%", rate*100)
}

func runURL(run domain.Run) string {
	return "/runs/" + url.PathEscape(run.Key.Machine) + "/" + url.PathEscape(run.Key.RunID)
}

func shortID(id string) string {
	if len(id) > 18 {
		return id[:18] + "…"
	}
	return id
}

func triggerLabel(kind string) string {
	switch kind {
	case "":
		return "手动"
	case "cron":
		return "定时"
	case "webhook":
		return "Webhook"
	default:
		return kind
	}
}

func orchestrationLabel(kind string) string {
	if kind == "code" {
		return "代码编排"
	}
	return "JSON 编排"
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index] + " …"
	}
	return text
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"icon": icon, "badge": badge, "dot": dot, "ago": ago, "clock": clockTime, "duration": duration,
		"percent": percent, "runURL": runURL, "short": shortID, "trigger": triggerLabel, "orchestration": orchestrationLabel,
		"firstLine": firstLine, "label": func(s domain.Status) string { return lookOf(s).Label },
		"tone":       func(s domain.Status) string { return lookOf(s).Tone },
		"pathEscape": url.PathEscape,
	}
}
