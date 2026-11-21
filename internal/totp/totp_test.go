package totp

import (
	"strings"
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
	if _, err := GenerateAt("otpauth://hotp/Test?secret=GEZDGNBVGY3TQOJQ", time.Now()); err == nil {
		t.Fatal("非 totp 类型的 otpauth URI 应当被拒绝")
	}
	if _, err := GenerateAt("otpauth://totp/Test?secret=GEZDGNBVGY3TQOJQ&algorithm=MD5", time.Now()); err == nil {
		t.Fatal("不支持的算法 MD5 应当被拒绝")
	}
	if _, err := GenerateAt("otpauth://totp/Test?secret=GEZDGNBVGY3TQOJQ&digits=9", time.Now()); err == nil {
		t.Fatal("超出范围的 digits 应当被拒绝")
	}
	if _, err := GenerateAt("otpauth://totp/Test?secret=GEZDGNBVGY3TQOJQ&period=0", time.Now()); err == nil {
		t.Fatal("非法的 period 应当被拒绝")
	}
}

func TestNormalizeSecretPreservesNonDefaultParamsAndSHA256(t *testing.T) {
	// 默认参数 URI 应归一化为纯大写 Base32
	plain, err := NormalizeSecret("otpauth://totp/Google:a@b.com?secret=gezd-gnbv gy3t qojq&digits=6&period=30&algorithm=SHA1")
	if err != nil {
		t.Fatalf("归一化默认参数 URI 失败: %v", err)
	}
	if plain != "GEZDGNBVGY3TQOJQ" {
		t.Fatalf("期望纯 Base32 GEZDGNBVGY3TQOJQ，实际 %q", plain)
	}

	// RFC 6238 SHA256 测试向量："12345678901234567890123456789012" (32 字节) -> t=59s, digits=8 => 46119246
	const rfcSha256Base32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQGEZA"
	rawURI := "otpauth://totp/Corp:alice?secret=" + strings.ToLower(rfcSha256Base32) + "&algorithm=sha256&digits=8&period=30"
	normURI, err := NormalizeSecret(rawURI)
	if err != nil {
		t.Fatalf("归一化非默认参数 URI 失败: %v", err)
	}
	if !strings.HasPrefix(normURI, "otpauth://totp/") {
		t.Fatalf("非默认 algorithm/digits 应保留 otpauth URI 格式，实际 %q", normURI)
	}
	got, err := GenerateAt(normURI, time.Unix(59, 0))
	if err != nil {
		t.Fatalf("按归一化后的 SHA256 URI 生成动态码失败: %v", err)
	}
	if got.Code != "46119246" || got.Period != 30 {
		t.Fatalf("RFC 6238 SHA256 8 位向量不匹配: got=%+v", got)
	}
}
