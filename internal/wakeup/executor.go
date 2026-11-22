package wakeup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"more-account/internal/model"
	"more-account/internal/quota"
)

const (
	streamPath                  = "/v1internal:streamGenerateContent?alt=sse"
	defaultWakeupPrompt         = "hi"
	defaultWakeupModel          = "gemini-3.8-flash-high"
	defaultMaxOutputTokens      = 64
	minThinkingModelOutputToken = 256
	placeholderProjectID        = "aicode-consumers"
	antigravitySystemPrompt     = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Absolute paths only****Proactiveness**"
)

// StreamResult 表示一次 SSE 唤醒请求的解析结果。
type StreamResult struct {
	Reply            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	DurationMs       int64
}

// StreamAPIError 表示在 HTTP 200 SSE 流式响应内嵌 JSON 中捕获到的结构化服务端错误。
type StreamAPIError struct {
	Info model.QuotaErrorInfo
}

// Error 实现 error 接口，输出可读的错误码与错误消息。
func (e *StreamAPIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Info.Code > 0 {
		return fmt.Sprintf("唤醒流式响应错误 (code %d): %s", e.Info.Code, e.Info.Message)
	}
	return fmt.Sprintf("唤醒流式响应错误: %s", e.Info.Message)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// validWakeupProjectID 过滤空白与占位项目 ID（aicode-consumers），仅返回可用于 CloudCode 请求的有效项目 ID。
func validWakeupProjectID(projectID string) string {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" || strings.EqualFold(trimmed, placeholderProjectID) {
		return ""
	}
	return trimmed
}

// effectiveWakeupProjectID 优先从令牌提取有效项目 ID，其次回退复用配额快照中记录的有效项目 ID。
func effectiveWakeupProjectID(token model.TokenData, quotaData *model.QuotaData) string {
	if p := quota.CloudCodeContextFromToken(token).PreferredProjectID; p != "" {
		return p
	}
	if quotaData != nil {
		if p := validWakeupProjectID(quotaData.ProjectID); p != "" {
			return p
		}
	}
	return ""
}

// isLikelyThinkingModel 判断模型是否默认启用思维链（thought tokens 计入 maxOutputTokens 预算）。
// 同时参考模型原始 ID 与可选显示名，并将 MODEL_PLACEHOLDER_* 占位模型按保守策略视为思考模型，避免被截断。
func isLikelyThinkingModel(modelName string, displayName ...string) bool {
	lowerID := strings.ToLower(strings.TrimSpace(modelName))
	lowerDisp := ""
	if len(displayName) > 0 {
		lowerDisp = strings.ToLower(strings.TrimSpace(displayName[0]))
	}
	if lowerID == "" && lowerDisp == "" {
		return true
	}
	// 云端占位模型 ID 往往对应高配思考模型（如 Gemini 3.8/4 或 Claude Opus），保守按思考模型提升输出预算
	if strings.HasPrefix(lowerID, "model_placeholder_") {
		return true
	}
	combined := strings.TrimSpace(lowerID + " " + lowerDisp)
	for _, kw := range []string{
		"-high", "-medium", "-low", "thinking",
		"argon", "carbon", "hydrogen", "neon",
		"opus", "sonnet", "gemini-3", "gemini-4",
	} {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return false
}

// isClaudeThinkingModel 判断是否为启用思维链的 Claude 模型（部分 Claude 思考路由不接受 temperature=0）。
func isClaudeThinkingModel(modelName string, displayName ...string) bool {
	lowerID := strings.ToLower(strings.TrimSpace(modelName))
	lowerDisp := ""
	if len(displayName) > 0 {
		lowerDisp = strings.ToLower(strings.TrimSpace(displayName[0]))
	}
	combined := strings.TrimSpace(lowerID + " " + lowerDisp)
	isClaude := strings.Contains(combined, "claude") ||
		strings.Contains(combined, "anthropic") ||
		strings.Contains(combined, "sonnet") ||
		strings.Contains(combined, "opus") ||
		lowerID == "model_placeholder_m401" ||
		lowerID == "model_placeholder_m402"
	if !isClaude {
		return false
	}
	return isLikelyThinkingModel(modelName, displayName...)
}

func normalizeWakeupMaxOutputTokens(modelName string, requested int, displayName ...string) int {
	if requested <= 0 {
		requested = defaultMaxOutputTokens
	}
	// Thinking 模型会将思考片段计入 maxOutputTokens；若上限过小（如 16/64），流会在思考阶段因 MAX_TOKENS 提前截断而无正文分片。
	if isLikelyThinkingModel(modelName, displayName...) && requested < minThinkingModelOutputToken {
		return minThinkingModelOutputToken
	}
	return requested
}

func buildWakeupPayload(projectID, modelName, prompt string, maxOutputTokens int, displayName ...string) map[string]any {
	nowMs := time.Now().UnixMilli()
	effectiveMaxTokens := normalizeWakeupMaxOutputTokens(modelName, maxOutputTokens, displayName...)
	genCfg := map[string]any{
		"maxOutputTokens": effectiveMaxTokens,
	}
	// 仅对非 Claude 思考模型设置 temperature=0，避免部分 Claude thinking 后端因 temperature 限制拒绝请求
	if !isClaudeThinkingModel(modelName, displayName...) {
		genCfg["temperature"] = 0
	}
	body := map[string]any{
		"requestId":   fmt.Sprintf("req_%d_%s", nowMs, randomHex(3)),
		"model":       modelName,
		"userAgent":   "antigravity",
		"requestType": "agent",
		"request": map[string]any{
			"contents": []map[string]any{
				{
					"role":  "user",
					"parts": []map[string]any{{"text": prompt}},
				},
			},
			"session_id": fmt.Sprintf("sess_%d_%s", nowMs, randomHex(3)),
			"systemInstruction": map[string]any{
				"parts": []map[string]any{{"text": antigravitySystemPrompt}},
			},
			"generationConfig": genCfg,
		},
	}
	if p := validWakeupProjectID(projectID); p != "" {
		body["project"] = p
	}
	return body
}

// ParseStreamResult 解析 CloudCode SSE 流式文本中的最终回复和 Token 用量，过滤 thought 思考片段。
// 若流中出现内嵌 JSON error 对象，将返回 *StreamAPIError 以便上层提取错误原因与验证链接；
// 若模型返回合法 HTTP 200 流且完成了思考推理（产生 thought 分片或有效候选输出 Token），即使未输出非思考可见字符也视为唤醒成功。
func ParseStreamResult(rawText string) (*StreamResult, error) {
	var (
		replyParts       []string
		res              StreamResult
		maxCandTokens    int
		maxThoughtTokens int
		gotEvent         bool
		sawCandidate     bool
		sawThoughtPart   bool
		streamErrInfo    *model.QuotaErrorInfo
	)

	captureStreamError := func(obj map[string]any) bool {
		errField, hasErr := obj["error"]
		if !hasErr || errField == nil {
			return false
		}
		rawBytes, mErr := json.Marshal(obj)
		if mErr != nil {
			return false
		}
		statusCode := http.StatusOK
		if errMap, ok := errField.(map[string]any); ok {
			if codeVal, ok := errMap["code"].(float64); ok && int(codeVal) > 0 {
				statusCode = int(codeVal)
			}
		}
		parsed := quota.ParseGoogleAPIError(statusCode, string(rawBytes))
		if streamErrInfo == nil || (streamErrInfo.ValidationURL == "" && parsed.ValidationURL != "") {
			cp := parsed
			streamErrInfo = &cp
		}
		return true
	}

	processObj := func(obj map[string]any) {
		if captureStreamError(obj) {
			return
		}
		var cand map[string]any
		if respObj, ok := obj["response"].(map[string]any); ok {
			if captureStreamError(respObj) {
				return
			}
			if cands, ok := respObj["candidates"].([]any); ok && len(cands) > 0 {
				cand, _ = cands[0].(map[string]any)
			}
			if usage, ok := respObj["usageMetadata"].(map[string]any); ok {
				extractUsageMeta(usage, &res, &maxCandTokens, &maxThoughtTokens)
			}
		}
		if cand == nil {
			if cands, ok := obj["candidates"].([]any); ok && len(cands) > 0 {
				cand, _ = cands[0].(map[string]any)
			}
		}
		if usage, ok := obj["usageMetadata"].(map[string]any); ok {
			extractUsageMeta(usage, &res, &maxCandTokens, &maxThoughtTokens)
		}

		if cand != nil {
			sawCandidate = true
			if content, ok := cand["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, p := range parts {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						if thought, ok := pm["thought"].(bool); ok && thought {
							if txt, _ := pm["text"].(string); strings.TrimSpace(txt) != "" {
								sawThoughtPart = true
							}
							continue
						}
						if txt, ok := pm["text"].(string); ok && txt != "" {
							replyParts = append(replyParts, txt)
						}
					}
				}
			}
		}
	}

	for _, line := range strings.Split(rawText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var payload string
		if strings.HasPrefix(trimmed, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
		} else if strings.HasPrefix(trimmed, "{") {
			payload = trimmed
		} else {
			continue
		}

		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err == nil {
			gotEvent = true
			processObj(obj)
		}
	}

	if !gotEvent {
		var obj map[string]any
		if err := json.Unmarshal([]byte(rawText), &obj); err == nil {
			gotEvent = true
			processObj(obj)
		}
	}

	if !gotEvent {
		return nil, fmt.Errorf("CloudCode 流式响应为空")
	}
	res.Reply = strings.TrimSpace(strings.Join(replyParts, ""))
	if res.Reply == "" {
		if sawThoughtPart || (sawCandidate && res.TotalTokens > 0) {
			res.Reply = "[已完成思考唤醒]"
		} else if streamErrInfo != nil {
			return nil, &StreamAPIError{Info: *streamErrInfo}
		} else {
			return nil, fmt.Errorf("唤醒请求未收到模型文本回复")
		}
	}
	return &res, nil
}

// extractUsageMeta 从流式分片中分别追踪 Prompt、正文候选输出与思考输出的最大累计值，
// 防止 candidatesTokenCount 与 thoughtsTokenCount 拆分到不同分片上报时少算 CompletionTokens。
func extractUsageMeta(usage map[string]any, res *StreamResult, maxCandTokens, maxThoughtTokens *int) {
	if v, ok := usage["promptTokenCount"].(float64); ok && int(v) > res.PromptTokens {
		res.PromptTokens = int(v)
	}
	if v, ok := usage["candidatesTokenCount"].(float64); ok && int(v) > *maxCandTokens {
		*maxCandTokens = int(v)
	}
	if v, ok := usage["thoughtsTokenCount"].(float64); ok && int(v) > *maxThoughtTokens {
		*maxThoughtTokens = int(v)
	}
	if combinedOut := *maxCandTokens + *maxThoughtTokens; combinedOut > res.CompletionTokens {
		res.CompletionTokens = combinedOut
	}
	if v, ok := usage["totalTokenCount"].(float64); ok && int(v) > res.TotalTokens {
		res.TotalTokens = int(v)
	}
}

func wakeupUserAgent() string {
	arch := "amd64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return fmt.Sprintf("antigravity/%s (aidev_client; os_type=%s; arch=%s; cl=%s)",
		quota.DefaultCloudCodeIDEVersion, runtime.GOOS, arch, quota.DefaultCloudCodeCL)
}

// ExecuteWakeupRequest 向 CloudCode 发送单次极轻唤醒请求并返回解析结果或错误诊断。
func ExecuteWakeupRequest(ctx context.Context, httpClient *http.Client, token model.TokenData, modelName, prompt string, maxOutputTokens int) (*StreamResult, *model.QuotaErrorInfo, error) {
	return executeWakeupRequestWithDisplayName(ctx, httpClient, token, modelName, "", prompt, maxOutputTokens)
}

func executeWakeupRequestWithDisplayName(ctx context.Context, httpClient *http.Client, token model.TokenData, modelName, modelDisplayName, prompt string, maxOutputTokens int) (*StreamResult, *model.QuotaErrorInfo, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultWakeupPrompt
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = defaultWakeupModel
	}

	ccCtx := quota.CloudCodeContextFromToken(token)
	baseURL := quota.ResolveCloudCodeBaseURL(ccCtx)
	// 仅传入过滤占位值后的 PreferredProjectID，避免把 aicode-consumers 写入请求体
	payload := buildWakeupPayload(ccCtx.PreferredProjectID, modelName, prompt, maxOutputTokens, modelDisplayName)
	rawBody, _ := json.Marshal(payload)

	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+streamPath, bytes.NewReader(rawBody))
	if err != nil {
		return nil, nil, fmt.Errorf("构造唤醒请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", wakeupUserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("发送唤醒请求失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("读取唤醒响应失败: %w", err)
	}
	bodyText := string(bodyBytes)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errInfo := quota.ParseGoogleAPIError(resp.StatusCode, bodyText)
		return nil, &errInfo, fmt.Errorf("唤醒请求失败 (HTTP %d): %s", resp.StatusCode, errInfo.Message)
	}

	parsed, err := ParseStreamResult(bodyText)
	if err != nil {
		var streamErr *StreamAPIError
		if errors.As(err, &streamErr) {
			info := streamErr.Info
			return nil, &info, fmt.Errorf("唤醒请求失败 (code %d): %s", info.Code, info.Message)
		}
		return nil, nil, err
	}
	parsed.DurationMs = time.Since(started).Milliseconds()
	return parsed, nil, nil
}
