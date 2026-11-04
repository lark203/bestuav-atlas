// Package license 生成与校验授权凭证（license）。
//
// 设计取舍：
//   - 不引入任何第三方依赖，只用标准库（crypto/hmac + sha256 + base64 + json）；
//   - 凭证是「载荷.签名」两段 base64url：载荷可读（便于排查问题），签名保证不可
//     篡改——与 JWT 的思路一致，但没有引入 JWT 库的复杂度；
//   - 本包是纯 Go、无 UI 依赖，可独立单测（见 license_test.go）。
//
// 安全提醒：真实产品里 secret 不应硬编码在客户端，应由服务端签名、客户端只做
// 校验（公钥验签更佳）。这里的实现用于本地生成与演示，接口形态可平滑替换成
// 「服务端签发 + 客户端验签」。
package license

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Payload 是授权内容。
type Payload struct {
	Subject   string    `json:"sub"`            // 授权对象（公司/个人）
	Edition   string    `json:"ed"`             // 版本：standard / pro / enterprise
	Features  []string  `json:"feat,omitempty"` // 功能位
	IssuedAt  time.Time `json:"iat"`            // 签发时间
	ExpiresAt time.Time `json:"exp,omitempty"`  // 到期时间；零值表示永久
	Issuer    string    `json:"iss,omitempty"`  // 签发方
}

// ErrSignature 表示签名不匹配（内容被篡改或密钥不同）。
var ErrSignature = errors.New("license: 签名校验失败")

// ErrExpired 表示凭证已过期。
var ErrExpired = errors.New("license: 授权已过期")

// Generate 用 secret 对 p 签名，返回「载荷.签名」形式的凭证字符串。
func Generate(p Payload, secret string) (string, error) {
	if p.Subject == "" {
		return "", errors.New("license: 授权对象不能为空")
	}
	if secret == "" {
		return "", errors.New("license: 签名密钥不能为空")
	}
	if p.IssuedAt.IsZero() {
		p.IssuedAt = time.Now().UTC().Truncate(time.Second)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("license: 序列化载荷失败: %w", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(sign(body, secret)), nil
}

// Verify 校验凭证：签名正确且未过期时返回载荷。
func Verify(token, secret string) (Payload, error) {
	var p Payload
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return p, errors.New("license: 凭证格式不正确（应为 载荷.签名）")
	}
	enc := base64.RawURLEncoding
	body, err := enc.DecodeString(parts[0])
	if err != nil {
		return p, fmt.Errorf("license: 解码载荷失败: %w", err)
	}
	sig, err := enc.DecodeString(parts[1])
	if err != nil {
		return p, fmt.Errorf("license: 解码签名失败: %w", err)
	}
	if !hmac.Equal(sig, sign(body, secret)) {
		return p, ErrSignature
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("license: 解析载荷失败: %w", err)
	}
	if !p.ExpiresAt.IsZero() && time.Now().After(p.ExpiresAt) {
		return p, ErrExpired
	}
	return p, nil
}

// ExpiryText 返回人类可读的有效期描述（永久 / 日期）。
func ExpiryText(p Payload) string {
	if p.ExpiresAt.IsZero() {
		return "永久"
	}
	return p.ExpiresAt.Format("2006-01-02")
}

// Shorten 把凭证截断成适合列表展示的形式（保留头尾，中间省略）。
func Shorten(token string, n int) string {
	if len(token) <= n*2+3 {
		return token
	}
	return token[:n] + "…" + token[len(token)-n:]
}

// sign 计算 HMAC-SHA256 签名。
func sign(body []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return mac.Sum(nil)
}
