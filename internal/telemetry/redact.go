package telemetry

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Reports leave the machine, so anything that looks like a credential is
// masked before it is written to the outbox.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`xox[abprse]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`),
	regexp.MustCompile(`lin_(?:api|oauth)_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`vck_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
}

var (
	bearerPattern   = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{12,}`)
	keyValuePattern = regexp.MustCompile(`(?i)((?:token|secret|password|passwd|api[_-]?key|access[_-]?key|private[_-]?key|authorization)["']?\s*[:=]\s*["']?)[^\s"',;&]{6,}`)
	secretKeyName   = regexp.MustCompile(`(?i)(token|secret|password|passwd|credential|api[_-]?key|access[_-]?key|private[_-]?key|auth)`)
)

const masked = "***"

// Redact masks credential-shaped substrings.
func Redact(text string) string {
	if text == "" {
		return text
	}
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, masked)
	}
	text = bearerPattern.ReplaceAllString(text, "${1}"+masked)
	return keyValuePattern.ReplaceAllString(text, "${1}"+masked)
}

// redactValue masks a whole value when its name says it is a secret.
func redactValue(name, value string) string {
	if secretKeyName.MatchString(name) && value != "" {
		return masked
	}
	return Redact(value)
}

// clip shortens text to at most limit runes.
func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
