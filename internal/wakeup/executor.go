package wakeup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	streamPath              = "/v1internal:streamGenerateContent?alt=sse"
	defaultWakeupPrompt     = "hi"
	defaultWakeupModel      = "gemini-3.8-flash-high"
	antigravitySystemPrompt = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Absolute paths only****Proactiveness**"
)

// StreamResult 表示一次 SSE 唤醒请求的解析结果。
type StreamResult struct {
	Reply            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	DurationMs       int64
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func buildWakeupPayload(projectID, modelName, prompt string, maxOutputTokens int) map[string]any {
	nowMs := time.Now().UnixMilli()
	genCfg := map[string]any{"temperature": 0}
	if maxOutputTokens > 0 {
		genCfg["maxOutputTokens"] = maxOutputTokens
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
	if p := strings.TrimSpace(projectID); p != "" {
		body["project"] = p
	}
	return body
}

// ParseStreamResult 解析 CloudCode SSE 流式文本中的最终回复和 Token 用量，过滤 thought 思考片段。
func ParseStreamResult(rawText string) (*StreamResult, error) {
	var (
		replyParts []string
		res        StreamResult
		gotEvent   bool
	)

	processObj := func(obj map[string]any) {
		var cand map[string]any
		if respObj, ok := obj["response"].(map[string]any); ok {
			if cands, ok := respObj["candidates"].([]any); ok && len(cands) > 0 {
				cand, _ = cands[0].(map[string]any)
			}
			if usage, ok := respObj["usageMetadata"].(map[string]any); ok {
				extractUsageMeta(usage, &res)
			}
		}
		if cand == nil {
			if cands, ok := obj["candidates"].([]any); ok && len(cands) > 0 {
				cand, _ = cands[0].(map[string]any)
			}
		}
		if usage, ok := obj["usageMetadata"].(map[string]any); ok {
			extractUsageMeta(usage, &res)
		}

		if cand != nil {
			if content, ok := cand["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, p := range parts {
						pm, ok := p.(map[string]any)
						if !ok {
							continue
						}
						if thought, ok := pm["thought"].(bool); ok && thought {
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
		return nil, fmt.Errorf("唤醒请求未收到模型文本回复")
	}
	return &res, nil
}

// extractUsageMeta 从流式分片中更新累计 Token 计数，始终取最新且不回退的最大累计值。
func extractUsageMeta(usage map[string]any, res *StreamResult) {
	if v, ok := usage["promptTokenCount"].(float64); ok && int(v) > res.PromptTokens {
		res.PromptTokens = int(v)
	}
	if v, ok := usage["candidatesTokenCount"].(float64); ok && int(v) > res.CompletionTokens {
		res.CompletionTokens = int(v)
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
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultWakeupPrompt
	}
	if maxOutputTokens <= 0 {
		maxOutputTokens = 16
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = defaultWakeupModel
	}

	ccCtx := quota.CloudCodeContextFromToken(token)
	baseURL := quota.ResolveCloudCodeBaseURL(ccCtx)
	payload := buildWakeupPayload(token.ProjectID, modelName, prompt, maxOutputTokens)
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
		return nil, nil, err
	}
	parsed.DurationMs = time.Since(started).Milliseconds()
	return parsed, nil, nil
}
