// Package auth 提供 OAuth 认证和凭证管理功能
// 本文件实现 OAuth 浏览器登录和设备码登录流程
// 支持 OpenAI、Google Antigravity 等提供商
package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// OAuthProviderConfig OAuth 提供商配置
// 包含 OAuth 2.0 授权码流程所需的所有参数
//
// 字段说明：
// - Issuer: 授权服务器地址（如 https://auth.openai.com）
// - ClientID: 客户端 ID（OAuth 应用标识）
// - ClientSecret: 客户端密钥（Google 等需要，机密客户端）
// - TokenURL: 令牌端点 URL（可选，覆盖默认端点）
// - Scopes: 请求的权限范围（空格分隔）
// - Originator: 发起者标识（OpenAI 特定参数）
// - Port: 本地回调服务器监听端口
type OAuthProviderConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string // Google OAuth 需要（机密客户端）
	TokenURL     string // 覆盖令牌端点（Google 使用不同的 URL）
	Scopes       string
	Originator   string
	Port         int
}

// OpenAIOAuthConfig 返回 OpenAI OAuth 配置
// 使用 Codex CLI 的客户端凭证
//
// 返回：
// - OAuthProviderConfig: OpenAI OAuth 配置
func OpenAIOAuthConfig() OAuthProviderConfig {
	return OAuthProviderConfig{
		Issuer:     "https://auth.openai.com",
		ClientID:   "app_EMoamEEZ73f0CkXaXp7hrann",
		Scopes:     "openid profile email offline_access",
		Originator: "codex_cli_rs",
		Port:       1455,
	}
}

// GoogleAntigravityOAuthConfig 返回 Google Cloud Code Assist (Antigravity) 的 OAuth 配置
// 客户端凭证与 OpenCode/pi-ai 使用的相同
//
// 返回：
// - OAuthProviderConfig: Google Antigravity OAuth 配置
func GoogleAntigravityOAuthConfig() OAuthProviderConfig {
	// 这些凭证与 OpenCode antigravity 插件使用的相同
	clientID := decodeBase64(
		"MTA3MTAwNjA2MDU5MS10bWhzc2luMmgyMWxjcmUyMzV2dG9sb2poNGc0MDNlcC5hcHBzLmdvb2dsZXVzZXJjb250ZW50LmNvbQ==",
	)
	clientSecret := decodeBase64("R09DU1BYLUs1OEZXUjQ4NkxkTEoxbUxCOHNYQzR6NnFEQWY=")
	return OAuthProviderConfig{
		Issuer:       "https://accounts.google.com/o/oauth2/v2",
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs",
		Port:         51121,
	}
}

// decodeBase64 解码 Base64 字符串
// 如果解码失败，返回原始字符串
//
// 参数：
// - s: Base64 编码的字符串
//
// 返回：
// - string: 解码后的字符串
func decodeBase64(s string) string {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s
	}
	return string(data)
}

// GenerateState 生成随机 state 字符串用于 OAuth CSRF 保护
//
// 返回：
// - string: 随机 state 字符串
// - error: 生成错误
func GenerateState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// LoginBrowser 通过浏览器进行 OAuth 登录
// 启动本地 HTTP 服务器接收回调，支持无头环境手动粘贴授权码
//
// 流程：
// 1. 生成 PKCE 代码和 state
// 2. 构建授权 URL 并打开浏览器
// 3. 启动本地回调服务器监听 /auth/callback
// 4. 等待回调或用户手动粘贴授权码
// 5. 用授权码交换令牌
//
// 参数：
// - cfg: OAuth 提供商配置
//
// 返回：
// - *AuthCredential: OAuth 凭证
// - error: 登录错误
func LoginBrowser(cfg OAuthProviderConfig) (*AuthCredential, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	state, err := GenerateState()
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", cfg.Port)

	authURL := buildAuthorizeURL(cfg, pkce, state, redirectURI)

	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			resultCh <- callbackResult{err: fmt.Errorf("state mismatch")}
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error")
			resultCh <- callbackResult{err: fmt.Errorf("no code received: %s", errMsg)}
			http.Error(w, "No authorization code received", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Authentication successful!</h2><p>You can close this window.</p></body></html>")
		resultCh <- callbackResult{code: code}
	})

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("starting callback server on port %d: %w", cfg.Port, err)
	}

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	fmt.Printf("Open this URL to authenticate:\n\n%s\n\n", authURL)

	if err := OpenBrowser(authURL); err != nil {
		fmt.Printf("Could not open browser automatically.\nPlease open this URL manually:\n\n%s\n\n", authURL)
	}

	fmt.Printf(
		"Wait! If you are in a headless environment (like Coolify/VPS) and cannot reach localhost:%d,\n",
		cfg.Port,
	)
	fmt.Println(
		"please complete the login in your local browser and then PASTE the final redirect URL (or just the code) here.",
	)
	fmt.Println("Waiting for authentication (browser or manual paste)...")

	// Start manual input in a goroutine
	manualCh := make(chan string)
	go func() {
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		manualCh <- strings.TrimSpace(input)
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		return ExchangeCodeForTokens(cfg, result.code, pkce.CodeVerifier, redirectURI)
	case manualInput := <-manualCh:
		if manualInput == "" {
			return nil, fmt.Errorf("manual input canceled")
		}
		// Extract code from URL if it's a full URL
		code := manualInput
		if strings.Contains(manualInput, "?") {
			u, err := url.Parse(manualInput)
			if err == nil {
				code = u.Query().Get("code")
			}
		}
		if code == "" {
			return nil, fmt.Errorf("could not find authorization code in input")
		}
		return ExchangeCodeForTokens(cfg, code, pkce.CodeVerifier, redirectURI)
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authentication timed out after 5 minutes")
	}
}

// callbackResult 回调结果结构
// 用于接收 HTTP 回调或手动输入的结果
type callbackResult struct {
	code string // 授权码
	err  error  // 错误信息
}

// deviceCodeResponse 设备码响应（内部结构）
type deviceCodeResponse struct {
	DeviceAuthID string // 设备认证 ID
	UserCode     string // 用户代码
	Interval     int    // 轮询间隔（秒）
}

// DeviceCodeInfo 设备码信息
// 用于无头环境的 OAuth 认证流程
// 用户在浏览器打开 URL 并输入代码完成认证
//
// 字段说明：
// - DeviceAuthID: 设备认证 ID（用于轮询令牌状态）
// - UserCode: 用户代码（用户在浏览器输入）
// - VerifyURL: 验证 URL（用户打开此 URL 进行认证）
// - Interval: 轮询间隔（秒）
type DeviceCodeInfo struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	VerifyURL    string `json:"verify_url"`
	Interval     int    `json:"interval"`
}

// RequestDeviceCode 从 OAuth 提供商请求设备码
// 适用于无头环境（如 VPS、Docker 容器）
//
// 参数：
// - cfg: OAuth 提供商配置
//
// 返回：
// - *DeviceCodeInfo: 设备码信息
// - error: 请求错误
func RequestDeviceCode(cfg OAuthProviderConfig) (*DeviceCodeInfo, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"client_id": cfg.ClientID,
	})

	resp, err := http.Post(
		cfg.Issuer+"/api/accounts/deviceauth/usercode",
		"application/json",
		strings.NewReader(string(reqBody)),
	)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: %s", string(body))
	}

	deviceResp, err := parseDeviceCodeResponse(body)
	if err != nil {
		return nil, fmt.Errorf("parsing device code response: %w", err)
	}

	if deviceResp.Interval < 1 {
		deviceResp.Interval = 5
	}

	return &DeviceCodeInfo{
		DeviceAuthID: deviceResp.DeviceAuthID,
		UserCode:     deviceResp.UserCode,
		VerifyURL:    cfg.Issuer + "/codex/device",
		Interval:     deviceResp.Interval,
	}, nil
}

// PollDeviceCodeOnce 轮询设备码认证状态（单次尝试）
//
// 参数：
// - cfg: OAuth 提供商配置
// - deviceAuthID: 设备认证 ID
// - userCode: 用户代码
//
// 返回：
// - *AuthCredential: 认证成功返回凭证
// - error: 认证错误
func PollDeviceCodeOnce(cfg OAuthProviderConfig, deviceAuthID, userCode string) (*AuthCredential, error) {
	return pollDeviceCode(cfg, deviceAuthID, userCode)
}

// parseDeviceCodeResponse 解析设备码响应
func parseDeviceCodeResponse(body []byte) (deviceCodeResponse, error) {
	var raw struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}

	if err := json.Unmarshal(body, &raw); err != nil {
		return deviceCodeResponse{}, err
	}

	interval, err := parseFlexibleInt(raw.Interval)
	if err != nil {
		return deviceCodeResponse{}, err
	}

	return deviceCodeResponse{
		DeviceAuthID: raw.DeviceAuthID,
		UserCode:     raw.UserCode,
		Interval:     interval,
	}, nil
}

// parseFlexibleInt 灵活解析整数（支持 JSON 数字和字符串）
//
// 参数：
// - raw: JSON 原始数据
//
// 返回：
// - int: 解析的整数值
// - error: 解析错误
func parseFlexibleInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}

	var interval int
	if err := json.Unmarshal(raw, &interval); err == nil {
		return interval, nil
	}

	var intervalStr string
	if err := json.Unmarshal(raw, &intervalStr); err == nil {
		intervalStr = strings.TrimSpace(intervalStr)
		if intervalStr == "" {
			return 0, nil
		}
		return strconv.Atoi(intervalStr)
	}

	return 0, fmt.Errorf("invalid integer value: %s", string(raw))
}

// LoginDeviceCode 通过设备码登录（无头环境）
// 适用于无法打开浏览器的环境（VPS、Docker 等）
// 流程：
// 1. 请求设备码和用户代码
// 2. 用户在其他设备打开 URL 并输入代码
// 3. 轮询检查认证状态
// 4. 认证成功后返回凭证
//
// 参数：
// - cfg: OAuth 提供商配置
//
// 返回：
// - *AuthCredential: OAuth 凭证
// - error: 登录错误
func LoginDeviceCode(cfg OAuthProviderConfig) (*AuthCredential, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"client_id": cfg.ClientID,
	})

	resp, err := http.Post(
		cfg.Issuer+"/api/accounts/deviceauth/usercode",
		"application/json",
		strings.NewReader(string(reqBody)),
	)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: %s", string(body))
	}

	deviceResp, err := parseDeviceCodeResponse(body)
	if err != nil {
		return nil, fmt.Errorf("parsing device code response: %w", err)
	}

	if deviceResp.Interval < 1 {
		deviceResp.Interval = 5
	}

	fmt.Printf(
		"\nTo authenticate, open this URL in your browser:\n\n  %s/codex/device\n\nThen enter this code: %s\n\nWaiting for authentication...\n",
		cfg.Issuer,
		deviceResp.UserCode,
	)

	deadline := time.After(15 * time.Minute)
	ticker := time.NewTicker(time.Duration(deviceResp.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return nil, fmt.Errorf("device code authentication timed out after 15 minutes")
		case <-ticker.C:
			cred, err := pollDeviceCode(cfg, deviceResp.DeviceAuthID, deviceResp.UserCode)
			if err != nil {
				continue
			}
			if cred != nil {
				return cred, nil
			}
		}
	}
}

// pollDeviceCode 轮询设备码认证状态（内部方法）
//
// 参数：
// - cfg: OAuth 提供商配置
// - deviceAuthID: 设备认证 ID
// - userCode: 用户代码
//
// 返回：
// - *AuthCredential: 认证成功返回凭证
// - error: 认证错误
func pollDeviceCode(cfg OAuthProviderConfig, deviceAuthID, userCode string) (*AuthCredential, error) {
	reqBody, _ := json.Marshal(map[string]string{
		"device_auth_id": deviceAuthID,
		"user_code":      userCode,
	})

	resp, err := http.Post(
		cfg.Issuer+"/api/accounts/deviceauth/token",
		"application/json",
		strings.NewReader(string(reqBody)),
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pending")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading device token response: %w", err)
	}

	var tokenResp struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeChallenge     string `json:"code_challenge"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, err
	}

	redirectURI := cfg.Issuer + "/deviceauth/callback"
	return ExchangeCodeForTokens(cfg, tokenResp.AuthorizationCode, tokenResp.CodeVerifier, redirectURI)
}

// RefreshAccessToken 刷新访问令牌
// 使用刷新令牌获取新的访问令牌
//
// 参数：
// - cred: 当前凭证（包含 RefreshToken）
// - cfg: OAuth 提供商配置
//
// 返回：
// - *AuthCredential: 刷新后的凭证
// - error: 刷新错误
func RefreshAccessToken(cred *AuthCredential, cfg OAuthProviderConfig) (*AuthCredential, error) {
	if cred.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	data := url.Values{
		"client_id":     {cfg.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {cred.RefreshToken},
		"scope":         {"openid profile email"},
	}
	if cfg.ClientSecret != "" {
		data.Set("client_secret", cfg.ClientSecret)
	}

	tokenURL := cfg.Issuer + "/oauth/token"
	if cfg.TokenURL != "" {
		tokenURL = cfg.TokenURL
	}

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("refreshing token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed: %s", string(body))
	}

	refreshed, err := parseTokenResponse(body, cred.Provider)
	if err != nil {
		return nil, err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = cred.RefreshToken
	}
	if refreshed.AccountID == "" {
		refreshed.AccountID = cred.AccountID
	}
	if cred.Email != "" && refreshed.Email == "" {
		refreshed.Email = cred.Email
	}
	if cred.ProjectID != "" && refreshed.ProjectID == "" {
		refreshed.ProjectID = cred.ProjectID
	}
	return refreshed, nil
}

// BuildAuthorizeURL 构建 OAuth 授权 URL（公开方法）
//
// 参数：
// - cfg: OAuth 提供商配置
// - pkce: PKCE 代码
// - state: CSRF 保护状态
// - redirectURI: 重定向 URI
//
// 返回：
// - string: 授权 URL
func BuildAuthorizeURL(cfg OAuthProviderConfig, pkce PKCECodes, state, redirectURI string) string {
	return buildAuthorizeURL(cfg, pkce, state, redirectURI)
}

// buildAuthorizeURL 构建 OAuth 授权 URL（内部方法）
// 根据提供商不同构建不同的 URL 参数
//
// 参数：
// - cfg: OAuth 提供商配置
// - pkce: PKCE 代码
// - state: CSRF 保护状态
// - redirectURI: 重定向 URI
//
// 返回：
// - string: 授权 URL
func buildAuthorizeURL(cfg OAuthProviderConfig, pkce PKCECodes, state, redirectURI string) string {
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {cfg.Scopes},
		"code_challenge":        {pkce.CodeChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}

	isGoogle := strings.Contains(strings.ToLower(cfg.Issuer), "accounts.google.com")
	if isGoogle {
		// Google OAuth requires these for refresh token support
		params.Set("access_type", "offline")
		params.Set("prompt", "consent")
	} else {
		// OpenAI-specific parameters
		params.Set("id_token_add_organizations", "true")
		params.Set("codex_cli_simplified_flow", "true")
		if strings.Contains(strings.ToLower(cfg.Issuer), "auth.openai.com") {
			params.Set("originator", "picoclaw")
		}
		if cfg.Originator != "" {
			params.Set("originator", cfg.Originator)
		}
	}

	// Google uses /auth path, OpenAI uses /oauth/authorize
	if isGoogle {
		return cfg.Issuer + "/auth?" + params.Encode()
	}
	return cfg.Issuer + "/oauth/authorize?" + params.Encode()
}

// ExchangeCodeForTokens 用授权码交换令牌
// OAuth 2.0 授权码流程的核心步骤
//
// 参数：
// - cfg: OAuth 提供商配置
// - code: 授权码
// - codeVerifier: PKCE 验证器
// - redirectURI: 重定向 URI
//
// 返回：
// - *AuthCredential: OAuth 凭证
// - error: 交换错误
func ExchangeCodeForTokens(cfg OAuthProviderConfig, code, codeVerifier, redirectURI string) (*AuthCredential, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {codeVerifier},
	}
	if cfg.ClientSecret != "" {
		data.Set("client_secret", cfg.ClientSecret)
	}

	tokenURL := cfg.Issuer + "/oauth/token"
	if cfg.TokenURL != "" {
		tokenURL = cfg.TokenURL
	}

	// Determine provider name from config
	provider := "openai"
	if cfg.TokenURL != "" && strings.Contains(cfg.TokenURL, "googleapis.com") {
		provider = "google-antigravity"
	}

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("exchanging code for tokens: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token exchange response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	return parseTokenResponse(body, provider)
}

// parseTokenResponse 解析令牌响应
// 从 OAuth 令牌响应中提取访问令牌、刷新令牌等信息
//
// 参数：
// - body: 响应体 JSON
// - provider: 提供商名称
//
// 返回：
// - *AuthCredential: OAuth 凭证
// - error: 解析错误
func parseTokenResponse(body []byte, provider string) (*AuthCredential, error) {
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("no access token in response")
	}

	var expiresAt time.Time
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}

	cred := &AuthCredential{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt,
		Provider:     provider,
		AuthMethod:   "oauth",
	}

	if accountID := extractAccountID(tokenResp.IDToken); accountID != "" {
		cred.AccountID = accountID
	} else if accountID := extractAccountID(tokenResp.AccessToken); accountID != "" {
		cred.AccountID = accountID
	} else if accountID := extractAccountID(tokenResp.IDToken); accountID != "" {
		// Recent OpenAI OAuth responses may only include chatgpt_account_id in id_token claims.
		cred.AccountID = accountID
	}

	return cred, nil
}

// extractAccountID 从 JWT Token 中提取账户 ID
// 支持多种 OpenAI JWT 声明格式
//
// 参数：
// - token: JWT Token（access_token 或 id_token）
//
// 返回：
// - string: 账户 ID（提取失败返回空字符串）
func extractAccountID(token string) string {
	claims, err := parseJWTClaims(token)
	if err != nil {
		return ""
	}

	if accountID, ok := claims["chatgpt_account_id"].(string); ok && accountID != "" {
		return accountID
	}

	if accountID, ok := claims["https://api.openai.com/auth.chatgpt_account_id"].(string); ok && accountID != "" {
		return accountID
	}

	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if accountID, ok := authClaim["chatgpt_account_id"].(string); ok && accountID != "" {
			return accountID
		}
	}

	if orgs, ok := claims["organizations"].([]any); ok {
		for _, org := range orgs {
			if orgMap, ok := org.(map[string]any); ok {
				if accountID, ok := orgMap["id"].(string); ok && accountID != "" {
					return accountID
				}
			}
		}
	}

	return ""
}

// parseJWTClaims 解析 JWT Token 的 Claims
//
// 参数：
// - token: JWT Token 字符串
//
// 返回：
// - map[string]any: Claims 映射
// - error: 解析错误
func parseJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("token is not a JWT")
	}

	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64URLDecode(payload)
	if err != nil {
		return nil, err
	}

	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, err
	}

	return claims, nil
}

// base64URLDecode Base64URL 解码
// 将 URL 安全的 Base64 字符串解码为字节切片
//
// 参数：
// - s: Base64URL 编码字符串
//
// 返回：
// - []byte: 解码后的字节
// - error: 解码错误
func base64URLDecode(s string) ([]byte, error) {
	s = strings.NewReplacer("-", "+", "_", "/").Replace(s)
	return base64.StdEncoding.DecodeString(s)
}

// OpenBrowser 在用户默认浏览器中打开 URL
// 支持 macOS、Linux、Windows 平台
//
// 参数：
// - url: 要打开的 URL
//
// 返回：
// - error: 打开错误（不支持的平台或命令失败）
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
