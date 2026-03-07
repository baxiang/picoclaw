// Package routing 提供消息路由功能
// 本文件实现会话键构建和解析
package routing

import (
	"fmt"
	"strings"
)

// DMScope 直接消息会话隔离粒度
type DMScope string

const (
	DMScopeMain                  DMScope = "main"                   // 主会话（共享）
	DMScopePerPeer               DMScope = "per-peer"               // 每对等方独立会话
	DMScopePerChannelPeer        DMScope = "per-channel-peer"       // 每渠道 + 对等方独立会话
	DMScopePerAccountChannelPeer DMScope = "per-account-channel-peer" // 每账户 + 渠道 + 对等方独立会话
)

// RoutePeer 路由对等方
// 表示聊天的类型和标识符
type RoutePeer struct {
	Kind string // "direct"（直接消息）, "group"（群聊）, "channel"（频道）
	ID   string // 对等方 ID
}

// SessionKeyParams 会话键构建参数
type SessionKeyParams struct {
	AgentID       string            // 代理 ID
	Channel       string            // 渠道名称
	AccountID     string            // 账户 ID
	Peer          *RoutePeer        // 路由对等方
	DMScope       DMScope           // DM 会话隔离粒度
	IdentityLinks map[string][]string // 身份链接（跨平台映射）
}

// ParsedSessionKey 解析后的会话键
type ParsedSessionKey struct {
	AgentID string // 代理 ID
	Rest    string // 剩余部分
}

// BuildAgentMainSessionKey 构建代理主会话键
// 格式："agent:<agentId>:main"
//
// 参数：
// - agentID: 代理 ID
//
// 返回：
// - string: 主会话键
func BuildAgentMainSessionKey(agentID string) string {
	return fmt.Sprintf("agent:%s:%s", NormalizeAgentID(agentID), DefaultMainKey)
}

// BuildAgentPeerSessionKey 构建代理对等方会话键
// 根据代理、渠道、对等方和 DM 作用域构建
//
// 参数：
// - params: 会话键参数
//
// 返回：
// - string: 会话键
func BuildAgentPeerSessionKey(params SessionKeyParams) string {
	agentID := NormalizeAgentID(params.AgentID)

	peer := params.Peer
	if peer == nil {
		peer = &RoutePeer{Kind: "direct"}
	}
	peerKind := strings.TrimSpace(peer.Kind)
	if peerKind == "" {
		peerKind = "direct"
	}

	if peerKind == "direct" {
		dmScope := params.DMScope
		if dmScope == "" {
			dmScope = DMScopeMain
		}
		peerID := strings.TrimSpace(peer.ID)

		// Resolve identity links (cross-platform collapse)
		if dmScope != DMScopeMain && peerID != "" {
			if linked := resolveLinkedPeerID(params.IdentityLinks, params.Channel, peerID); linked != "" {
				peerID = linked
			}
		}
		peerID = strings.ToLower(peerID)

		switch dmScope {
		case DMScopePerAccountChannelPeer:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				accountID := NormalizeAccountID(params.AccountID)
				return fmt.Sprintf("agent:%s:%s:%s:direct:%s", agentID, channel, accountID, peerID)
			}
		case DMScopePerChannelPeer:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				return fmt.Sprintf("agent:%s:%s:direct:%s", agentID, channel, peerID)
			}
		case DMScopePerPeer:
			if peerID != "" {
				return fmt.Sprintf("agent:%s:direct:%s", agentID, peerID)
			}
		}
		return BuildAgentMainSessionKey(agentID)
	}

	// Group/channel peers always get per-peer sessions
	channel := normalizeChannel(params.Channel)
	peerID := strings.ToLower(strings.TrimSpace(peer.ID))
	if peerID == "" {
		peerID = "unknown"
	}
	return fmt.Sprintf("agent:%s:%s:%s:%s", agentID, channel, peerKind, peerID)
}

// ParseAgentSessionKey 解析代理会话键
// 从 "agent:<agentId>:<rest>" 格式中提取 agentId 和 rest
//
// 参数：
// - sessionKey: 会话键字符串
//
// 返回：
// - *ParsedSessionKey: 解析结果（无效返回 nil）
func ParseAgentSessionKey(sessionKey string) *ParsedSessionKey {
	raw := strings.TrimSpace(sessionKey)
	if raw == "" {
		return nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	if parts[0] != "agent" {
		return nil
	}
	agentID := strings.TrimSpace(parts[1])
	rest := parts[2]
	if agentID == "" || rest == "" {
		return nil
	}
	return &ParsedSessionKey{AgentID: agentID, Rest: rest}
}

// IsSubagentSessionKey 检查会话键是否为子代理会话键
//
// 参数：
// - sessionKey: 会话键字符串
//
// 返回：
// - bool: true 表示是子代理会话键
func IsSubagentSessionKey(sessionKey string) bool {
	raw := strings.TrimSpace(sessionKey)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(raw), "subagent:") {
		return true
	}
	parsed := ParseAgentSessionKey(raw)
	if parsed == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(parsed.Rest), "subagent:")
}

func normalizeChannel(channel string) string {
	c := strings.TrimSpace(strings.ToLower(channel))
	if c == "" {
		return "unknown"
	}
	return c
}

func resolveLinkedPeerID(identityLinks map[string][]string, channel, peerID string) string {
	if len(identityLinks) == 0 {
		return ""
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}

	candidates := make(map[string]bool)
	rawCandidate := strings.ToLower(peerID)
	if rawCandidate != "" {
		candidates[rawCandidate] = true
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "" {
		scopedCandidate := fmt.Sprintf("%s:%s", channel, strings.ToLower(peerID))
		candidates[scopedCandidate] = true
	}

	// If peerID is already in canonical "platform:id" format, also add the
	// bare ID part as a candidate for backward compatibility with identity_links
	// that use raw IDs (e.g. "123" instead of "telegram:123").
	if idx := strings.Index(rawCandidate, ":"); idx > 0 && idx < len(rawCandidate)-1 {
		bareID := rawCandidate[idx+1:]
		candidates[bareID] = true
	}

	if len(candidates) == 0 {
		return ""
	}

	for canonical, ids := range identityLinks {
		canonicalName := strings.TrimSpace(canonical)
		if canonicalName == "" {
			continue
		}
		for _, id := range ids {
			normalized := strings.ToLower(strings.TrimSpace(id))
			if normalized != "" && candidates[normalized] {
				return canonicalName
			}
		}
	}
	return ""
}
