package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPeriod    = 30
	defaultDigits    = 6
	defaultAlgorithm = "SHA1"
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
	algorithm   string
}

// NormalizeSecret 规范化 Base32 密钥或 otpauth://totp URI。
// 若均为默认参数（SHA1 / 30s / 6 位），返回大写无空白 Base32 字符串；
// 若包含非默认 period/digits/algorithm，则保留规范化的 otpauth:// URI 以避免丢失参数。
func NormalizeSecret(raw string) (string, error) {
	cfg, cleanedSecret, err := parseSecretConfig(raw)
	if err != nil {
		return "", err
	}
	if len(cfg.secretBytes) == 0 {
		return "", fmt.Errorf("2FA 密钥为空")
	}
	if cfg.period == defaultPeriod && cfg.digits == defaultDigits && cfg.algorithm == defaultAlgorithm {
		return cleanedSecret, nil
	}
	params := url.Values{}
	params.Set("secret", cleanedSecret)
	if cfg.algorithm != defaultAlgorithm {
		params.Set("algorithm", cfg.algorithm)
	}
	if cfg.digits != defaultDigits {
		params.Set("digits", strconv.Itoa(cfg.digits))
	}
	if cfg.period != defaultPeriod {
		params.Set("period", strconv.Itoa(cfg.period))
	}
	return "otpauth://totp/account?" + params.Encode(), nil
}

func parseSecretConfig(raw string) (parsedConfig, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return parsedConfig{}, "", fmt.Errorf("2FA 密钥不能为空")
	}

	period := defaultPeriod
	digits := defaultDigits
	algorithm := defaultAlgorithm
	secretPart := trimmed

	if strings.HasPrefix(strings.ToLower(trimmed), "otpauth://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			return parsedConfig{}, "", fmt.Errorf("无法解析 otpauth 链接: %w", err)
		}
		if host := strings.ToLower(strings.TrimSpace(u.Host)); host != "" && host != "totp" {
			return parsedConfig{}, "", fmt.Errorf("仅支持 otpauth://totp 类型，不支持 %s", u.Host)
		}
		q := u.Query()
		secretPart = strings.TrimSpace(q.Get("secret"))
		if secretPart == "" {
			return parsedConfig{}, "", fmt.Errorf("otpauth 链接中缺少 secret 参数")
		}
		if p := strings.TrimSpace(q.Get("period")); p != "" {
			parsed, err := strconv.Atoi(p)
			if err != nil || parsed <= 0 || parsed > 3600 {
				return parsedConfig{}, "", fmt.Errorf("otpauth 链接中的 period 参数无效: %s", p)
			}
			period = parsed
		}
		if d := strings.TrimSpace(q.Get("digits")); d != "" {
			parsed, err := strconv.Atoi(d)
			if err != nil || parsed < 6 || parsed > 8 {
				return parsedConfig{}, "", fmt.Errorf("otpauth 链接中的 digits 参数无效（仅支持 6~8 位）: %s", d)
			}
			digits = parsed
		}
		if alg := strings.ToUpper(strings.TrimSpace(q.Get("algorithm"))); alg != "" {
			switch alg {
			case "SHA1", "SHA-1":
				algorithm = "SHA1"
			case "SHA256", "SHA-256":
				algorithm = "SHA256"
			case "SHA512", "SHA-512":
				algorithm = "SHA512"
			default:
				return parsedConfig{}, "", fmt.Errorf("不支持的 TOTP 哈希算法: %s", alg)
			}
		}
	}

	// 清洗空白与连字符，统一转为大写并去除末尾 Base32 填充符号。
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
		algorithm:   algorithm,
	}, cleaned, nil
}

func newHashFunc(algorithm string) func() hash.Hash {
	switch algorithm {
	case "SHA256":
		return sha256.New
	case "SHA512":
		return sha512.New
	default:
		return sha1.New
	}
}

// GenerateNow 使用当前系统时间计算 2FA 动态验证码。
func GenerateNow(rawSecret string) (CodeResult, error) {
	return GenerateAt(rawSecret, time.Now())
}

// GenerateAt 按指定时间点遵循 RFC 6238 计算动态验证码（支持 SHA1/SHA256/SHA512）。
func GenerateAt(rawSecret string, at time.Time) (CodeResult, error) {
	cfg, _, err := parseSecretConfig(rawSecret)
	if err != nil {
		return CodeResult{}, err
	}

	unixSec := at.Unix()
	if unixSec < 0 {
		return CodeResult{}, fmt.Errorf("无效的系统时间戳")
	}
	period := int64(cfg.period)
	counter := uint64(unixSec / period)
	elapsed := unixSec % period
	remaining := int(period - elapsed)
	validUntil := unixSec + int64(remaining)

	var counterBuf [8]byte
	binary.BigEndian.PutUint64(counterBuf[:], counter)

	mac := hmac.New(newHashFunc(cfg.algorithm), cfg.secretBytes)
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
