package tokenusage

import (
	"fmt"
	"strconv"
	"strings"
)

// ResolvedModelInfo 保存规范化后的模型 ID、展示名称及所属提供方。
type ResolvedModelInfo struct {
	ModelID     string
	DisplayName string
	APIProvider string
}

var builtinEnumModels = map[string]string{
	"1318":                   "gemini-3.8-flash-high",
	"1319":                   "gemini-3.8-flash-high",
	"1320":                   "gemini-3.8-flash-high",
	"MODEL_PLACEHOLDER_M318": "gemini-3.8-flash-high",
	"MODEL_PLACEHOLDER_M319": "gemini-3.8-flash-high",
	"MODEL_PLACEHOLDER_M320": "gemini-3.8-flash-high",
	"1322":                   "gemini-3.8-flash-tiered",
	"MODEL_PLACEHOLDER_M322": "gemini-3.8-flash-tiered",
	"1401":                   "claude-opus-5-5-medium",
	"MODEL_PLACEHOLDER_M401": "claude-opus-5-5-medium",
	"1402":                   "claude-opus-5-5-high",
	"MODEL_PLACEHOLDER_M402": "claude-opus-5-5-high",
	"1417":                   "gemini-4-argon-high",
	"MODEL_PLACEHOLDER_M417": "gemini-4-argon-high",
	"1427":                   "gemini-4-carbon-high",
	"MODEL_PLACEHOLDER_M427": "gemini-4-carbon-high",
	"1528":                   "gemini-4-hydrogen",
	"MODEL_PLACEHOLDER_M528": "gemini-4-hydrogen",
	"1667":                   "gemini-pro-orchestrator",
	"MODEL_PLACEHOLDER_M667": "gemini-pro-orchestrator",
	"1669":                   "gemini-flash-worker-agy",
	"MODEL_PLACEHOLDER_M669": "gemini-flash-worker-agy",
}

var responseModelAliases = map[string]string{
	"gemini-3.8-flash":         "gemini-3.8-flash-high",
	"gemini-3.8-flash-n":       "gemini-3.8-flash-high",
	"gemini-3.8-flash-control": "gemini-3.8-flash-high",
	"gemini-3.8-flash-tiered":  "gemini-3.8-flash-tiered",
	"gemini-4-argon":           "gemini-4-argon-high",
	"gemini-4-carbon":          "gemini-4-carbon-high",
	"claude-opus-5-5-medium":   "claude-opus-5-5-medium",
	"claude-opus-5-5-high":     "claude-opus-5-5-high",
}

var exactDisplayNames = map[string]string{
	"gemini-3.8-flash-high":   "Gemini 3.8 Flash (High)",
	"gemini-3.8-flash-tiered": "Gemini 3.8 Flash (Tiered)",
	"gemini-4-argon-high":     "Gemini 4 Argon (High)",
	"gemini-4-carbon-high":    "Gemini 4 Carbon (High)",
	"claude-opus-5-5-high":    "Claude Opus 5.5 (High)",
	"claude-opus-5-5-medium":  "Claude Opus 5.5 (Medium)",
	"gpt-oss-120b-medium":     "GPT-OSS 120B (Medium)",
}

func isPlaceholderOrNumeric(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	upper := strings.ToUpper(t)
	if upper == "UNKNOWN" || strings.HasPrefix(upper, "MODEL_PLACEHOLDER_") {
		return true
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return false
		}
	}
	return true
}

func canonicalizeModelID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if m, ok := responseModelAliases[strings.ToLower(trimmed)]; ok {
		return m
	}
	if m, ok := builtinEnumModels[trimmed]; ok {
		return m
	}
	return strings.ToLower(trimmed)
}

func fallbackEnumSlug(rawEnum string) string {
	t := strings.TrimSpace(rawEnum)
	if t == "" {
		return ""
	}
	if num, err := strconv.ParseUint(t, 10, 64); err == nil {
		if num >= 1000 {
			return fmt.Sprintf("model-m%d", num-1000)
		}
		if num > 0 {
			return fmt.Sprintf("model-%d", num)
		}
		return ""
	}
	upper := strings.ToUpper(t)
	if suf, ok := strings.CutPrefix(upper, "MODEL_PLACEHOLDER_M"); ok && suf != "" {
		return "model-m" + strings.ToLower(suf)
	}
	if suf, ok := strings.CutPrefix(upper, "MODEL_PLACEHOLDER_"); ok && suf != "" {
		return "model-" + strings.ToLower(suf)
	}
	return ""
}

// ResolveModelInfo 将 Protobuf 原始枚举号、占位符、响应模型名解析为规范的模型展示信息。
func ResolveModelInfo(rawEnum, responseModel, rawProvider string) ResolvedModelInfo {
	var modelID string
	rawEnum = strings.TrimSpace(rawEnum)
	responseModel = strings.TrimSpace(responseModel)

	if m, ok := builtinEnumModels[rawEnum]; ok {
		modelID = m
	} else if responseModel != "" {
		if m, ok := responseModelAliases[strings.ToLower(responseModel)]; ok {
			modelID = m
		} else if !isPlaceholderOrNumeric(responseModel) {
			modelID = canonicalizeModelID(responseModel)
		}
	}

	if modelID == "" && rawEnum != "" {
		if slug := fallbackEnumSlug(rawEnum); slug != "" {
			modelID = slug
		}
	}
	if modelID == "" {
		modelID = "unknown"
	}

	return ResolvedModelInfo{
		ModelID:     modelID,
		DisplayName: FormatModelDisplayName(modelID),
		APIProvider: resolveAPIProvider(rawProvider, modelID),
	}
}

// FormatModelDisplayName 将规范化模型 ID 美化为界面可读标题。
func FormatModelDisplayName(modelID string) string {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" || strings.EqualFold(trimmed, "unknown") {
		return "Unknown"
	}
	lower := strings.ToLower(trimmed)
	if exact, ok := exactDisplayNames[lower]; ok {
		return exact
	}

	rawParts := strings.Split(trimmed, "-")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "Unknown"
	}

	var tierSuffix string
	body := parts
	if len(parts) >= 2 {
		switch strings.ToLower(parts[len(parts)-1]) {
		case "high":
			tierSuffix = "(High)"
			body = parts[:len(parts)-1]
		case "medium":
			tierSuffix = "(Medium)"
			body = parts[:len(parts)-1]
		case "low":
			tierSuffix = "(Low)"
			body = parts[:len(parts)-1]
		case "tiered":
			tierSuffix = "(Tiered)"
			body = parts[:len(parts)-1]
		case "xhigh":
			tierSuffix = "(XHigh)"
			body = parts[:len(parts)-1]
		case "minimal":
			tierSuffix = "(Minimal)"
			body = parts[:len(parts)-1]
		}
	}

	tokens := make([]string, 0, len(body))
	for i := 0; i < len(body); {
		tok := body[i]
		if isDigits(tok) && i+1 < len(body) && isDigits(body[i+1]) {
			tokens = append(tokens, tok+"."+body[i+1])
			i += 2
			continue
		}
		lw := strings.ToLower(tok)
		switch lw {
		case "gpt":
			tokens = append(tokens, "GPT")
		case "oss":
			tokens = append(tokens, "OSS")
		case "ai":
			tokens = append(tokens, "AI")
		case "llm":
			tokens = append(tokens, "LLM")
		default:
			if len(lw) > 1 && lw[0] == 'm' && isDigits(lw[1:]) {
				tokens = append(tokens, strings.ToUpper(lw))
			} else if len(lw) > 1 && lw[len(lw)-1] == 'b' && isDigits(lw[:len(lw)-1]) {
				tokens = append(tokens, strings.ToUpper(lw))
			} else {
				tokens = append(tokens, strings.ToUpper(lw[:1])+lw[1:])
			}
		}
		i++
	}

	out := strings.Join(tokens, " ")
	if tierSuffix != "" {
		out = out + " " + tierSuffix
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func resolveAPIProvider(rawProvider, modelID string) string {
	switch strings.ToUpper(strings.TrimSpace(rawProvider)) {
	case "24", "API_PROVIDER_GOOGLE_GEMINI":
		return "Google Gemini"
	case "26", "API_PROVIDER_ANTHROPIC_VERTEX":
		return "Anthropic Vertex"
	case "API_PROVIDER_ANTHROPIC":
		return "Anthropic"
	case "API_PROVIDER_OPENAI":
		return "OpenAI"
	}
	lower := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(lower, "gemini-"):
		return "Google Gemini"
	case strings.HasPrefix(lower, "claude-"):
		return "Anthropic Vertex"
	case strings.HasPrefix(lower, "gpt-"):
		return "OpenAI"
	default:
		return "Antigravity"
	}
}
