package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPeriod = 30
	defaultDigits = 6
)

// CodeResult 表示一次 TOTP 动态码计算结果。
type CodeResult struct {
	Code             string `json:"code"`
	Period           int    `json:"period"`
	RemainingSeconds int    `json:"remainingSeconds"`
	ValidUntilUnix   int64  `json:"validUntilUnix"`
}

type parsedConfig struct {
	secretBytes []byte
	period      int
	digits      int
}

// NormalizeSecret 规范化 Base32 密钥或 otpauth://totp URI，返回标准大写无空白 Base32 字符串。
func NormalizeSecret(raw string) (string, error) {
	cfg, normalized, err := parseSecretConfig(raw)
	if err != nil {
		return "", err
	}
	if len(cfg.secretBytes) == 0 {
		return "", fmt.Errorf("2FA 密钥为空")
	}
	return normalized, nil
}

func parseSecretConfig(raw string) (parsedConfig, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return parsedConfig{}, "", fmt.Errorf("2FA 密钥不能为空")
	}

	period := defaultPeriod
	digits := defaultDigits
	secretPart := trimmed

	if strings.HasPrefix(strings.ToLower(trimmed), "otpauth://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			return parsedConfig{}, "", fmt.Errorf("无法解析 otpauth 链接: %w", err)
		}
		q := u.Query()
		secretPart = strings.TrimSpace(q.Get("secret"))
		if secretPart == "" {
			return parsedConfig{}, "", fmt.Errorf("otpauth 链接中缺少 secret 参数")
		}
		if p := strings.TrimSpace(q.Get("period")); p != "" {
			if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
				period = parsed
			}
		}
		if d := strings.TrimSpace(q.Get("digits")); d != "" {
			if parsed, err := strconv.Atoi(d); err == nil && parsed >= 6 && parsed <= 8 {
				digits = parsed
			}
		}
	}

	// 清洗空白与连字符，统一转为大写并补齐 Base32 等号填充。
	cleaned := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "-", "").Replace(strings.ToUpper(secretPart))
	cleaned = strings.TrimRight(cleaned, "=")
	if cleaned == "" {
		return parsedConfig{}, "", fmt.Errorf("2FA 密钥无效")
	}

	keyBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(cleaned)
	if err != nil {
		return parsedConfig{}, "", fmt.Errorf("无效的 Base32 2FA 密钥: %w", err)
	}
	return parsedConfig{
		secretBytes: keyBytes,
		period:      period,
		digits:      digits,
	}, cleaned, nil
}

// GenerateNow 使用当前系统时间计算 2FA 动态验证码。
func GenerateNow(rawSecret string) (CodeResult, error) {
	return GenerateAt(rawSecret, time.Now())
}

// GenerateAt 按指定时间点遵循 RFC 6238 (HMAC-SHA1) 计算动态验证码。
func GenerateAt(rawSecret string, at time.Time) (CodeResult, error) {
	cfg, _, err := parseSecretConfig(rawSecret)
	if err != nil {
		return CodeResult{}, err
	}

	unixSec := at.Unix()
	period := int64(cfg.period)
	counter := uint64(unixSec / period)
	elapsed := unixSec % period
	remaining := int(period - elapsed)
	validUntil := unixSec + int64(remaining)

	var counterBuf [8]byte
	binary.BigEndian.PutUint64(counterBuf[:], counter)

	mac := hmac.New(sha1.New, cfg.secretBytes)
	_, _ = mac.Write(counterBuf[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	binaryCode := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])

	mod := uint32(math.Pow10(cfg.digits))
	otp := binaryCode % mod
	format := fmt.Sprintf("%%0%dd", cfg.digits)

	return CodeResult{
		Code:             fmt.Sprintf(format, otp),
		Period:           cfg.period,
		RemainingSeconds: remaining,
		ValidUntilUnix:   validUntil,
	}, nil
}
