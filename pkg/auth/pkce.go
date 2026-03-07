// Package auth 提供 OAuth 认证和凭证管理功能
// 支持多种认证方式：
// - OAuth 浏览器登录（OpenAI、Google Antigravity 等）
// - OAuth 设备码登录（适用于无头环境）
// - API Key/Token 直接粘贴
// - 凭证存储和刷新
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCECodes PKCE（Proof Key for Code Exchange）代码
// 用于 OAuth 2.0 授权码流程的安全增强
// 防止授权码拦截攻击
type PKCECodes struct {
	CodeVerifier  string // 代码验证器（随机生成的秘密）
	CodeChallenge string // 代码挑战（验证器的 SHA256 哈希）
}

// GeneratePKCE 生成 PKCE 代码对
// 使用加密安全的随机数生成器生成 64 字节的验证器
// 然后计算其 SHA256 哈希作为挑战
//
// 返回：
// - PKCECodes: 验证器和挑战
// - error: 生成错误（如果随机数生成失败）
func GeneratePKCE() (PKCECodes, error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return PKCECodes{}, err
	}

	// 使用 base64 URL 安全编码（不含填充）
	verifier := base64.RawURLEncoding.EncodeToString(buf)

	// 计算 SHA256 哈希作为挑战
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return PKCECodes{
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
	}, nil
}
