// Package memory 提供记忆存储功能
// 本文件包含从旧版 JSON 格式迁移会话的功能

package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// jsonSession JSON 会话结构
// 镜像 pkg/session.Session 用于迁移目的
type jsonSession struct {
	Key      string              `json:"key"`        // 会话标识符
	Messages []providers.Message `json:"messages"`   // 消息列表
	Summary  string              `json:"summary,omitempty"` // 会话摘要
	Created  time.Time           `json:"created"`    // 创建时间
	Updated  time.Time           `json:"updated"`    // 最后更新时间
}

// MigrateFromJSON 从旧版 sessions/*.json 文件迁移会话到 Store
// 读取 sessionsDir 中的 JSON 文件，写入 Store，并将每个已迁移的文件重命名为
// .json.migrated 作为备份。返回迁移的会话数量。
//
// 解析失败的文件会被记录并跳过。
// 已迁移的文件（.json.migrated）会被忽略，使此函数具有幂等性。
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionsDir: 旧版会话文件目录
// - store: 目标存储接口
//
// 返回：
// - int: 迁移的会话数量
// - error: 迁移错误
func MigrateFromJSON(
	ctx context.Context, sessionsDir string, store Store,
) (int, error) {
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("memory: read sessions dir: %w", err)
	}

	migrated := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		// 跳过已迁移的文件
		if strings.HasSuffix(name, ".migrated") {
			continue
		}

		srcPath := filepath.Join(sessionsDir, name)

		data, readErr := os.ReadFile(srcPath)
		if readErr != nil {
			log.Printf("memory: migrate: skip %s: %v", name, readErr)
			continue
		}

		var sess jsonSession
		if parseErr := json.Unmarshal(data, &sess); parseErr != nil {
			log.Printf("memory: migrate: skip %s: %v", name, parseErr)
			continue
		}

		// 使用 JSON 内容中的 key，而不是文件名
		// 文件名已被清理（":" → "_"），但 key 不是
		key := sess.Key
		if key == "" {
			key = strings.TrimSuffix(name, ".json")
		}

		// 使用 SetHistory（原子替换）而不是逐个消息的
		// AddFullMessage。这使得迁移具有幂等性：如果在写入消息后但
		// 在下面的重命名之前进程崩溃，重试会替换部分数据而不会
		// 重复消息
		if setErr := store.SetHistory(ctx, key, sess.Messages); setErr != nil {
			return migrated, fmt.Errorf(
				"memory: migrate %s: set history: %w",
				name, setErr,
			)
		}

		if sess.Summary != "" {
			if sumErr := store.SetSummary(ctx, key, sess.Summary); sumErr != nil {
				return migrated, fmt.Errorf(
					"memory: migrate %s: set summary: %w",
					name, sumErr,
				)
			}
		}

		// 重命名为 .migrated 作为备份（不删除）
		renameErr := os.Rename(srcPath, srcPath+".migrated")
		if renameErr != nil {
			log.Printf("memory: migrate: rename %s: %v", name, renameErr)
		}

		migrated++
	}

	return migrated, nil
}
