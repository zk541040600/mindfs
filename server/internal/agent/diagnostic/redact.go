package diagnostic

import (
	"regexp"
	"strings"
)

var secretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[A-Za-z0-9._~+/=-]+`), `${1}[REDACTED:token]`},
	{regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd)\b\s*[:=]\s*["']?[^"'\s]{8,}`), `${1}=[REDACTED:secret]`},
	{regexp.MustCompile(`\b[A-Za-z0-9+/_-]{48,}={0,2}\b`), `[REDACTED:token]`},
}

// Redact removes credential-shaped values before diagnostic text is logged.
func Redact(line string) string {
	line = strings.TrimSpace(line)
	for _, secret := range secretPatterns {
		line = secret.pattern.ReplaceAllString(line, secret.replacement)
	}
	return line
}
