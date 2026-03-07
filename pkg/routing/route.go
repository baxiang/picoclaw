// Package routing 提供消息路由功能
// 本文件实现基于配置绑定的代理路由
package routing

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// RouteInput 路由输入
// 包含入站消息的路由上下文信息
type RouteInput struct {
	Channel    string     // 渠道名称
	AccountID  string     // 账户 ID
	Peer       *RoutePeer // 路由对等方
	ParentPeer *RoutePeer // 父对等方（回复目标）
	GuildID    string     // Discord Guild ID
	TeamID     string     // Slack Team ID
}

// ResolvedRoute 路由解析结果
type ResolvedRoute struct {
	AgentID        string // 代理 ID
	Channel        string // 渠道名称
	AccountID      string // 账户 ID
	SessionKey     string // 会话键
	MainSessionKey string // 主会话键
	MatchedBy      string // 匹配方式：binding.peer, binding.peer.parent, binding.guild, binding.team, binding.account, binding.channel, default
}

// RouteResolver 路由解析器
// 根据配置绑定确定哪个代理处理消息
type RouteResolver struct {
	cfg *config.Config
}

// NewRouteResolver 创建新的路由解析器
//
// 参数：
// - cfg: 全局配置
//
// 返回：
// - *RouteResolver: 路由解析器
func NewRouteResolver(cfg *config.Config) *RouteResolver {
	return &RouteResolver{cfg: cfg}
}

// ResolveRoute 解析消息路由并构建会话键
// 实现 7 级优先级级联：
// peer > parent_peer > guild > team > account > channel_wildcard > default
//
// 参数：
// - input: 路由输入
//
// 返回：
// - ResolvedRoute: 路由解析结果
func (r *RouteResolver) ResolveRoute(input RouteInput) ResolvedRoute {
	channel := strings.ToLower(strings.TrimSpace(input.Channel))
	accountID := NormalizeAccountID(input.AccountID)
	peer := input.Peer

	dmScope := DMScope(r.cfg.Session.DMScope)
	if dmScope == "" {
		dmScope = DMScopeMain
	}
	identityLinks := r.cfg.Session.IdentityLinks

	bindings := r.filterBindings(channel, accountID)

	choose := func(agentID string, matchedBy string) ResolvedRoute {
		resolvedAgentID := r.pickAgentID(agentID)
		sessionKey := strings.ToLower(BuildAgentPeerSessionKey(SessionKeyParams{
			AgentID:       resolvedAgentID,
			Channel:       channel,
			AccountID:     accountID,
			Peer:          peer,
			DMScope:       dmScope,
			IdentityLinks: identityLinks,
		}))
		mainSessionKey := strings.ToLower(BuildAgentMainSessionKey(resolvedAgentID))
		return ResolvedRoute{
			AgentID:        resolvedAgentID,
			Channel:        channel,
			AccountID:      accountID,
			SessionKey:     sessionKey,
			MainSessionKey: mainSessionKey,
			MatchedBy:      matchedBy,
		}
	}

	// Priority 1: Peer binding
	if peer != nil && strings.TrimSpace(peer.ID) != "" {
		if match := r.findPeerMatch(bindings, peer); match != nil {
			return choose(match.AgentID, "binding.peer")
		}
	}

	// Priority 2: Parent peer binding
	parentPeer := input.ParentPeer
	if parentPeer != nil && strings.TrimSpace(parentPeer.ID) != "" {
		if match := r.findPeerMatch(bindings, parentPeer); match != nil {
			return choose(match.AgentID, "binding.peer.parent")
		}
	}

	// Priority 3: Guild binding
	guildID := strings.TrimSpace(input.GuildID)
	if guildID != "" {
		if match := r.findGuildMatch(bindings, guildID); match != nil {
			return choose(match.AgentID, "binding.guild")
		}
	}

	// Priority 4: Team binding
	teamID := strings.TrimSpace(input.TeamID)
	if teamID != "" {
		if match := r.findTeamMatch(bindings, teamID); match != nil {
			return choose(match.AgentID, "binding.team")
		}
	}

	// Priority 5: Account binding
	if match := r.findAccountMatch(bindings); match != nil {
		return choose(match.AgentID, "binding.account")
	}

	// Priority 6: Channel wildcard binding
	if match := r.findChannelWildcardMatch(bindings); match != nil {
		return choose(match.AgentID, "binding.channel")
	}

	// Priority 7: Default agent
	return choose(r.resolveDefaultAgentID(), "default")
}

func (r *RouteResolver) filterBindings(channel, accountID string) []config.AgentBinding {
	var filtered []config.AgentBinding
	for _, b := range r.cfg.Bindings {
		matchChannel := strings.ToLower(strings.TrimSpace(b.Match.Channel))
		if matchChannel == "" || matchChannel != channel {
			continue
		}
		if !matchesAccountID(b.Match.AccountID, accountID) {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func matchesAccountID(matchAccountID, actual string) bool {
	trimmed := strings.TrimSpace(matchAccountID)
	if trimmed == "" {
		return actual == DefaultAccountID
	}
	if trimmed == "*" {
		return true
	}
	return strings.ToLower(trimmed) == strings.ToLower(actual)
}

func (r *RouteResolver) findPeerMatch(bindings []config.AgentBinding, peer *RoutePeer) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		if b.Match.Peer == nil {
			continue
		}
		peerKind := strings.ToLower(strings.TrimSpace(b.Match.Peer.Kind))
		peerID := strings.TrimSpace(b.Match.Peer.ID)
		if peerKind == "" || peerID == "" {
			continue
		}
		if peerKind == strings.ToLower(peer.Kind) && peerID == peer.ID {
			return b
		}
	}
	return nil
}

func (r *RouteResolver) findGuildMatch(bindings []config.AgentBinding, guildID string) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		matchGuild := strings.TrimSpace(b.Match.GuildID)
		if matchGuild != "" && matchGuild == guildID {
			return &bindings[i]
		}
	}
	return nil
}

func (r *RouteResolver) findTeamMatch(bindings []config.AgentBinding, teamID string) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		matchTeam := strings.TrimSpace(b.Match.TeamID)
		if matchTeam != "" && matchTeam == teamID {
			return &bindings[i]
		}
	}
	return nil
}

func (r *RouteResolver) findAccountMatch(bindings []config.AgentBinding) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		accountID := strings.TrimSpace(b.Match.AccountID)
		if accountID == "*" {
			continue
		}
		if b.Match.Peer != nil || b.Match.GuildID != "" || b.Match.TeamID != "" {
			continue
		}
		return &bindings[i]
	}
	return nil
}

func (r *RouteResolver) findChannelWildcardMatch(bindings []config.AgentBinding) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		accountID := strings.TrimSpace(b.Match.AccountID)
		if accountID != "*" {
			continue
		}
		if b.Match.Peer != nil || b.Match.GuildID != "" || b.Match.TeamID != "" {
			continue
		}
		return &bindings[i]
	}
	return nil
}

func (r *RouteResolver) pickAgentID(agentID string) string {
	trimmed := strings.TrimSpace(agentID)
	if trimmed == "" {
		return NormalizeAgentID(r.resolveDefaultAgentID())
	}
	normalized := NormalizeAgentID(trimmed)
	agents := r.cfg.Agents.List
	if len(agents) == 0 {
		return normalized
	}
	for _, a := range agents {
		if NormalizeAgentID(a.ID) == normalized {
			return normalized
		}
	}
	return NormalizeAgentID(r.resolveDefaultAgentID())
}

func (r *RouteResolver) resolveDefaultAgentID() string {
	agents := r.cfg.Agents.List
	if len(agents) == 0 {
		return DefaultAgentID
	}
	for _, a := range agents {
		if a.Default {
			id := strings.TrimSpace(a.ID)
			if id != "" {
				return NormalizeAgentID(id)
			}
		}
	}
	if id := strings.TrimSpace(agents[0].ID); id != "" {
		return NormalizeAgentID(id)
	}
	return DefaultAgentID
}
