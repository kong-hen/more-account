package totp

import (
	"testing"
	"time"
)

func TestGenerateAtRFC6238AndFormats(t *testing.T) {
	// RFC 6238 SHA1 测试密钥 "12345678901234567890" 的 Base32 编码：
	const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	// t = 59s 对应 SHA1 8位结果 94287082 -> 6位后六位为 287082
	res, err := GenerateAt(rfcSecret, time.Unix(59, 0))
	if err != nil {
		t.Fatalf("生成 TOTP 失败: %v", err)
	}
	if res.Code != "287082" {
		t.Fatalf("期望 6 位动态码为 287082，实际为 %s", res.Code)
	}
	if res.Period != 30 || res.RemainingSeconds != 1 {
		t.Fatalf("期望 Period=30, RemainingSeconds=1，实际 %+v", res)
	}

	// 验证空格、小写、连字符与 otpauth:// URI 兼容
	uriRes, err := GenerateAt("otpauth://totp/Google:alice@example.com?secret=gezd-gnbv gy3t qojq gezd gnbv gy3t qojq&issuer=Google", time.Unix(59, 0))
	if err != nil {
		t.Fatalf("解析 otpauth URI 失败: %v", err)
	}
	if uriRes.Code != "287082" {
		t.Fatalf("otpauth URI 动态码不匹配: got %s", uriRes.Code)
	}
}

func TestGenerateAtInvalidSecret(t *testing.T) {
	if _, err := GenerateAt("!!!invalid-base32!!!", time.Now()); err == nil {
		t.Fatal("非法 Base32 密钥应当返回错误")
	}
	if _, err := GenerateAt("   ", time.Now()); err == nil {
		t.Fatal("空白密钥应当返回错误")
	}
}
