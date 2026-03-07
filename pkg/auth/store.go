// Package auth 提供 OAuth 认证和凭证管理功能（本文件实现凭证存储）
package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// AuthCredential OAuth 凭证结构
// 存储访问令牌、刷新令牌和过期时间等信息
//
// 字段说明：
// - AccessToken: 访问令牌（用于 API 请求）
// - RefreshToken: 刷新令牌（可选，用于刷新访问令牌）
// - AccountID: 账户标识符
// - ExpiresAt: 过期时间（零值表示永不过期）
// - Provider: 提供商名称（openai、anthropic 等）
// - AuthMethod: 认证方式（oauth、token 等）
// - Email: 用户邮箱（可选）
// - ProjectID: 项目 ID（可选，用于某些提供商）
type AuthCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Provider     string    `json:"provider"`
	AuthMethod   string    `json:"auth_method"`
	Email        string    `json:"email,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
}

// AuthStore 凭证存储
// 管理多个提供商的凭证
type AuthStore struct {
	Credentials map[string]*AuthCredential `json:"credentials"` // 凭证映射（provider -> credential）
}

// IsExpired 检查凭证是否已过期
//
// 返回：
// - true: 已过期
// - false: 未过期或永不过期（ExpiresAt 为零值）
func (c *AuthCredential) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

// NeedsRefresh 检查凭证是否需要刷新
// 在过期前 5 分钟就认为需要刷新，以避免使用即将过期的令牌
//
// 返回：
// - true: 需要刷新
// - false: 不需要刷新或永不过期
func (c *AuthCredential) NeedsRefresh() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(c.ExpiresAt)
}

// authFilePath 获取凭证文件路径
// 优先使用 PICOCLAW_HOME 环境变量，否则使用 ~/.picoclaw/auth.json
//
// 返回：
// - 凭证文件路径
func authFilePath() string {
	if home := os.Getenv("PICOCLAW_HOME"); home != "" {
		return filepath.Join(home, "auth.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".picoclaw", "auth.json")
}

// LoadStore 加载凭证存储
// 如果文件不存在，返回空的存储
//
// 返回：
// - *AuthStore: 凭证存储
// - error: 加载错误
func LoadStore() (*AuthStore, error) {
	path := authFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{Credentials: make(map[string]*AuthCredential)}, nil
		}
		return nil, err
	}

	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Credentials == nil {
		store.Credentials = make(map[string]*AuthCredential)
	}
	return &store, nil
}

// SaveStore 保存凭证存储
// 使用原子写入确保数据安全（避免写入过程中崩溃导致文件损坏）
//
// 参数：
// - store: 要保存的凭证存储
//
// 返回：
// - error: 保存错误
func SaveStore(store *AuthStore) error {
	path := authFilePath()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}

	// 使用原子写入，权限设置为 0o600（仅所有者可读写）
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

// GetCredential 获取指定提供商的凭证
//
// 参数：
// - provider: 提供商名称
//
// 返回：
// - *AuthCredential: 凭证（如果不存在则返回 nil）
// - error: 加载错误
func GetCredential(provider string) (*AuthCredential, error) {
	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	cred, ok := store.Credentials[provider]
	if !ok {
		return nil, nil
	}
	return cred, nil
}

// SetCredential 保存凭证
//
// 参数：
// - provider: 提供商名称
// - cred: 凭证
//
// 返回：
// - error: 保存错误
func SetCredential(provider string, cred *AuthCredential) error {
	store, err := LoadStore()
	if err != nil {
		return err
	}
	store.Credentials[provider] = cred
	return SaveStore(store)
}

// DeleteCredential 删除指定提供商的凭证
//
// 参数：
// - provider: 提供商名称
//
// 返回：
// - error: 删除错误
func DeleteCredential(provider string) error {
	store, err := LoadStore()
	if err != nil {
		return err
	}
	delete(store.Credentials, provider)
	return SaveStore(store)
}

// DeleteAllCredentials 删除所有凭证
// 直接删除凭证文件
//
// 返回：
// - error: 删除错误
func DeleteAllCredentials() error {
	path := authFilePath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
