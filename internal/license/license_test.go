package license

import (
	"strings"
	"testing"
	"time"
)

const testSecret = "test-secret"

func sample() Payload {
	return Payload{
		Subject:  "北京必优航空科技有限公司",
		Edition:  "pro",
		Features: []string{"export", "api"},
		Issuer:   "bestuav",
	}
}

// TestGenerateVerifyRoundTrip 验证签发 → 校验能原样取回载荷。
func TestGenerateVerifyRoundTrip(t *testing.T) {
	p := sample()
	token, err := Generate(p, testSecret)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Count(token, ".") != 1 {
		t.Fatalf("凭证应为 载荷.签名 两段: %q", token)
	}

	got, err := Verify(token, testSecret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Subject != p.Subject || got.Edition != p.Edition || len(got.Features) != 2 {
		t.Fatalf("载荷不一致: %+v", got)
	}
	if got.IssuedAt.IsZero() {
		t.Error("签发时间应被自动填上")
	}
}

// TestTamperDetected 验证篡改载荷会被签名校验拒绝。
func TestTamperDetected(t *testing.T) {
	token, err := Generate(sample(), testSecret)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	parts := strings.SplitN(token, ".", 2)
	// 把载荷首位字符换掉（JSON 仍然合法的情况下也会签名不符）
	tampered := parts[0][:len(parts[0])-1] + "A" + "." + parts[1]
	if _, err := Verify(tampered, testSecret); err == nil {
		t.Fatal("篡改后的凭证不应通过校验")
	}
}

// TestWrongSecret 验证换密钥后校验失败。
func TestWrongSecret(t *testing.T) {
	token, _ := Generate(sample(), testSecret)
	if _, err := Verify(token, "another-secret"); err != ErrSignature {
		t.Fatalf("应返回 ErrSignature，实际 %v", err)
	}
}

// TestExpired 验证过期凭证被拒。
func TestExpired(t *testing.T) {
	p := sample()
	p.ExpiresAt = time.Now().Add(-time.Hour)
	token, err := Generate(p, testSecret)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := Verify(token, testSecret); err != ErrExpired {
		t.Fatalf("应返回 ErrExpired，实际 %v", err)
	}
}

// TestValidation 验证必填项校验。
func TestValidation(t *testing.T) {
	if _, err := Generate(Payload{}, testSecret); err == nil {
		t.Error("授权对象为空时应报错")
	}
	if _, err := Generate(sample(), ""); err == nil {
		t.Error("密钥为空时应报错")
	}
}

// TestShorten 验证长凭证的列表展示截断。
func TestShorten(t *testing.T) {
	token, _ := Generate(sample(), testSecret)
	s := Shorten(token, 6)
	if len(s) >= len(token) {
		t.Fatalf("截断后应更短: %q", s)
	}
	if !strings.Contains(s, "…") {
		t.Errorf("截断标记缺失: %q", s)
	}
}
