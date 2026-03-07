// Package skills 提供技能系统功能（本文件定义注册表接口）
package skills

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultMaxConcurrentSearches = 2 // 默认最大并发搜索数
)

// SearchResult 技能注册表搜索结果
type SearchResult struct {
	Score        float64 `json:"score"`        // 相关性分数
	Slug         string  `json:"slug"`         // 技能标识符
	DisplayName  string  `json:"display_name"` // 显示名称
	Summary      string  `json:"summary"`      // 摘要
	Version      string  `json:"version"`      // 版本
	RegistryName string  `json:"registry_name"`// 注册表名称
}

// SkillMeta 技能注册表中的技能元数据
type SkillMeta struct {
	Slug             string `json:"slug"`             // 技能标识符
	DisplayName      string `json:"display_name"`     // 显示名称
	Summary          string `json:"summary"`          // 摘要
	LatestVersion    string `json:"latest_version"`   // 最新版本
	IsMalwareBlocked bool   `json:"is_malware_blocked"`// 是否被恶意软件阻止
	IsSuspicious     bool   `json:"is_suspicious"`    // 是否可疑
	RegistryName     string `json:"registry_name"`    // 注册表名称
}

// InstallResult 技能安装结果
// 包含安装后的元数据，用于审核和用户消息
type InstallResult struct {
	Version          string // 安装的版本
	IsMalwareBlocked bool   // 是否被恶意软件阻止
	IsSuspicious     bool   // 是否可疑
	Summary          string // 摘要
}

// SkillRegistry 技能注册表接口
// 所有技能注册表必须实现此接口
// 每个注册表代表不同的技能来源（如 clawhub.ai）
type SkillRegistry interface {
	// Name 返回注册表的唯一名称（如 "clawhub"）
	Name() string
	// Search 搜索匹配查询的技能
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
	// GetSkillMeta 通过 slug 获取技能元数据
	GetSkillMeta(ctx context.Context, slug string) (*SkillMeta, error)
	// DownloadAndInstall 获取元数据、解析版本、下载并安装技能到 targetDir
	// 返回 InstallResult 包含元数据供调用者用于审核和用户消息
	DownloadAndInstall(ctx context.Context, slug, version, targetDir string) (*InstallResult, error)
}

// RegistryConfig 技能注册表配置
// This is the input to NewRegistryManagerFromConfig.
type RegistryConfig struct {
	ClawHub               ClawHubConfig
	MaxConcurrentSearches int
}

// ClawHubConfig configures the ClawHub registry.
type ClawHubConfig struct {
	Enabled         bool
	BaseURL         string
	AuthToken       string
	SearchPath      string // e.g. "/api/v1/search"
	SkillsPath      string // e.g. "/api/v1/skills"
	DownloadPath    string // e.g. "/api/v1/download"
	Timeout         int    // seconds, 0 = default (30s)
	MaxZipSize      int    // bytes, 0 = default (50MB)
	MaxResponseSize int    // bytes, 0 = default (2MB)
}

// RegistryManager coordinates multiple skill registries.
// It fans out search requests and routes installs to the correct registry.
type RegistryManager struct {
	registries    []SkillRegistry
	maxConcurrent int
	mu            sync.RWMutex
}

// NewRegistryManager creates an empty RegistryManager.
func NewRegistryManager() *RegistryManager {
	return &RegistryManager{
		registries:    make([]SkillRegistry, 0),
		maxConcurrent: defaultMaxConcurrentSearches,
	}
}

// NewRegistryManagerFromConfig builds a RegistryManager from config,
// instantiating only the enabled registries.
func NewRegistryManagerFromConfig(cfg RegistryConfig) *RegistryManager {
	rm := NewRegistryManager()
	if cfg.MaxConcurrentSearches > 0 {
		rm.maxConcurrent = cfg.MaxConcurrentSearches
	}
	if cfg.ClawHub.Enabled {
		rm.AddRegistry(NewClawHubRegistry(cfg.ClawHub))
	}
	return rm
}

// AddRegistry adds a registry to the manager.
func (rm *RegistryManager) AddRegistry(r SkillRegistry) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.registries = append(rm.registries, r)
}

// GetRegistry returns a registry by name, or nil if not found.
func (rm *RegistryManager) GetRegistry(name string) SkillRegistry {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	for _, r := range rm.registries {
		if r.Name() == name {
			return r
		}
	}
	return nil
}

// SearchAll fans out the query to all registries concurrently
// and merges results sorted by score descending.
func (rm *RegistryManager) SearchAll(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	rm.mu.RLock()
	regs := make([]SkillRegistry, len(rm.registries))
	copy(regs, rm.registries)
	rm.mu.RUnlock()

	if len(regs) == 0 {
		return nil, fmt.Errorf("no registries configured")
	}

	type regResult struct {
		results []SearchResult
		err     error
	}

	// Semaphore: limit concurrency.
	sem := make(chan struct{}, rm.maxConcurrent)
	resultsCh := make(chan regResult, len(regs))

	var wg sync.WaitGroup
	for _, reg := range regs {
		wg.Add(1)
		go func(r SkillRegistry) {
			defer wg.Done()

			// Acquire semaphore slot.
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				resultsCh <- regResult{err: ctx.Err()}
				return
			}

			searchCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
			defer cancel()

			results, err := r.Search(searchCtx, query, limit)
			if err != nil {
				slog.Warn("registry search failed", "registry", r.Name(), "error", err)
				resultsCh <- regResult{err: err}
				return
			}
			resultsCh <- regResult{results: results}
		}(reg)
	}

	// Close results channel after all goroutines complete.
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var merged []SearchResult
	var lastErr error

	var anyRegistrySucceeded bool
	for rr := range resultsCh {
		if rr.err != nil {
			lastErr = rr.err
			continue
		}
		anyRegistrySucceeded = true
		merged = append(merged, rr.results...)
	}

	// If all registries failed, return the last error.
	if !anyRegistrySucceeded && lastErr != nil {
		return nil, fmt.Errorf("all registries failed: %w", lastErr)
	}

	// Sort by score descending.
	sortByScoreDesc(merged)

	// Clamp to limit.
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}

	return merged, nil
}

// sortByScoreDesc sorts SearchResults by Score in descending order (insertion sort — small slices).
func sortByScoreDesc(results []SearchResult) {
	for i := 1; i < len(results); i++ {
		key := results[i]
		j := i - 1
		for j >= 0 && results[j].Score < key.Score {
			results[j+1] = results[j]
			j--
		}
		results[j+1] = key
	}
}
