// Package auth 提供 OAuth 认证和凭证管理功能（本文件实现 Token 登录）
package auth

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// LoginPasteToken 通过粘贴 API Key 或 Session Token 登录
// 适用于不支持 OAuth 的提供商或用户偏好手动输入 Token 的场景
//
// 参数：
// - provider: 提供商名称（用于显示提示信息）
// - r: 输入流（通常是 os.Stdin）
//
// 返回：
// - *AuthCredential: 认证凭证
// - error: 登录错误
func LoginPasteToken(provider string, r io.Reader) (*AuthCredential, error) {
	fmt.Printf("Paste your API key or session token from %s:\n", providerDisplayName(provider))
	fmt.Print("> ")

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading token: %w", err)
		}
		return nil, fmt.Errorf("no input received")
	}

	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return nil, fmt.Errorf("token cannot be empty")
	}

	return &AuthCredential{
		AccessToken: token,
		Provider:    provider,
		AuthMethod:  "token",
	}, nil
}

// LoginSetupToken 通过粘贴 Claude setup-token 登录
// 专门用于 Anthropic 的 OAuth setup-token 流程
//
// 参数：
// - r: 输入流（通常是 os.Stdin）
//
// 返回：
// - *AuthCredential: 认证凭证
// - error: 登录错误（如果 token 格式无效）
func LoginSetupToken(r io.Reader) (*AuthCredential, error) {
	fmt.Println("Paste your setup token from `claude setup-token`:")
	fmt.Print("> ")

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading token: %w", err)
		}
		return nil, fmt.Errorf("no input received")
	}

	token := strings.TrimSpace(scanner.Text())

	// 验证 setup-token 格式
	// Anthropic setup-token 以 sk-ant-oat01- 开头，长度至少 80 字符
	if !strings.HasPrefix(token, "sk-ant-oat01-") {
		return nil, fmt.Errorf("invalid setup token: expected prefix sk-ant-oat01-")
	}

	if len(token) < 80 {
		return nil, fmt.Errorf("invalid setup token: too short (expected at least 80 characters)")
	}

	return &AuthCredential{
		AccessToken: token,
		Provider:    "anthropic",
		AuthMethod:  "oauth",
	}, nil
}

// providerDisplayName 返回提供商的显示名称
// 用于用户友好的提示信息
//
// 参数：
// - provider: 提供商内部名称
//
// 返回：
// - string: 显示名称（如控制台域名）
func providerDisplayName(provider string) string {
	switch provider {
	case "anthropic":
		return "console.anthropic.com"
	case "openai":
		return "platform.openai.com"
	default:
		return provider
	}
}
