package ingest

import (
	"regexp"
	"strings"
)

// InjectionFinding records a detected prompt-injection or retrieval-poisoning pattern
// without storing the raw untrusted payload text.
type InjectionFinding struct {
	Rule   string `json:"rule"`
	Source string `json:"source"`
	Count  int    `json:"count"`
}

type InjectionScanResult struct {
	Detected bool               `json:"detected"`
	Findings []InjectionFinding `json:"findings,omitempty"`
}

type injectionRule struct {
	name    string
	pattern *regexp.Regexp
}

var injectionRules = []injectionRule{
	{
		name:    "instruction_override",
		pattern: regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override)\s+(?:(?:all|the|any)\s+)?(?:previous|prior|above|system)\s+(?:instructions?|prompts?|rules?|directives?)\b`),
	},
	{
		name:    "instruction_override_zh",
		pattern: regexp.MustCompile(`(?:忽略|无视|忘记|覆盖)(?:(?:所有|全部|之前|以上|先前|原有)的?){1,3}(?:指令|提示词|规则|系统提示)`),
	},
	{
		name:    "role_hijack",
		pattern: regexp.MustCompile(`(?im)(?:<\|im_start\|>\s*system|<<SYS>>|\[INST\]|^\s*SYSTEM\s+OVERRIDE\s*:|\byou\s+are\s+now\s+(?:a|an|in)\b|\bnew\s+system\s+prompt\s*:)`),
	},
	{
		name:    "tool_exfiltration",
		pattern: regexp.MustCompile(`(?i)(?:\b(?:send|exfiltrate|leak|upload|post)\s+(?:all\s+|the\s+|user\s+|secret\s+)?(?:data|context|evidence|tokens?|keys?|secrets?)\s+(?:to|via)\s+https?://|\b(?:execute|run)\s+(?:shell|bash|command|curl|wget)\b)`),
	},
	{
		name:    "hidden_html_directive",
		pattern: regexp.MustCompile(`(?is)<!--[^>]{0,400}(?:\b(?:ignore|override|system\s+prompt|exfiltrate)\b|指令|提示词)[^>]{0,400}-->`),
	},
}

// ScanInjection checks extracted document text (native or VLM) for indirect prompt injection
// and obfuscation markers. It never logs or returns raw document content.
func ScanInjection(text, source string) InjectionScanResult {
	if source == "" {
		source = "native"
	}
	if text == "" {
		return InjectionScanResult{}
	}
	var findings []InjectionFinding
	zeroWidthCount := 0
	var cleaned strings.Builder
	cleaned.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff', '\u2060':
			zeroWidthCount++
		default:
			cleaned.WriteRune(r)
		}
	}
	if zeroWidthCount > 8 {
		findings = append(findings, InjectionFinding{
			Rule:   "hidden_zero_width",
			Source: source,
			Count:  zeroWidthCount,
		})
	}
	normalized := cleaned.String()
	for _, rule := range injectionRules {
		if matches := rule.pattern.FindAllStringIndex(normalized, 10); len(matches) > 0 {
			findings = append(findings, InjectionFinding{
				Rule:   rule.name,
				Source: source,
				Count:  len(matches),
			})
		}
	}
	return InjectionScanResult{
		Detected: len(findings) > 0,
		Findings: findings,
	}
}
