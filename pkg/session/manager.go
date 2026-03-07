// Package session 提供会话管理功能
// 本文件包含会话管理器（SessionManager）的实现
// 用于保存和加载 AI 对话历史

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// Session 会话结构
// 保存单次对话的完整历史记录
type Session struct {
	Key      string              `json:"key"`      // 会话唯一标识符
	Messages []providers.Message `json:"messages"` // 消息历史列表
	Summary  string              `json:"summary,omitempty"` // 会话摘要（可选）
	Created  time.Time           `json:"created"`   // 创建时间
	Updated  time.Time           `json:"updated"`   // 最后更新时间
}

// SessionManager 会话管理器
// 负责管理所有会话的内存缓存和持久化存储
type SessionManager struct {
	sessions map[string]*Session // 会话映射表（key -> Session）
	mu       sync.RWMutex        // 读写锁，保护并发访问
	storage  string              // 存储目录路径
}

// NewSessionManager 创建会话管理器
//
// 参数：
// - storage: 会话文件存储目录（空字符串表示仅内存存储）
//
// 返回：
// - *SessionManager: 会话管理器指针
func NewSessionManager(storage string) *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
		storage:  storage,
	}

	if storage != "" {
		os.MkdirAll(storage, 0o755)
		sm.loadSessions()
	}

	return sm
}

// GetOrCreate 获取或创建会话
// 如果会话不存在则创建新会话
//
// 参数：
// - key: 会话标识符
//
// 返回：
// - *Session: 会话指针
func (sm *SessionManager) GetOrCreate(key string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if ok {
		return session
	}

	session = &Session{
		Key:      key,
		Messages: []providers.Message{},
		Created:  time.Now(),
		Updated:  time.Now(),
	}
	sm.sessions[key] = session

	return session
}

// AddMessage 添加简单消息到会话
//
// 参数：
// - sessionKey: 会话标识符
// - role: 消息角色（user/assistant/tool/system）
// - content: 消息内容
func (sm *SessionManager) AddMessage(sessionKey, role, content string) {
	sm.AddFullMessage(sessionKey, providers.Message{
		Role:    role,
		Content: content,
	})
}

// AddFullMessage 添加完整消息到会话
// 支持工具调用（ToolCalls）和工具调用 ID（ToolCallID）
// 用于保存完整的对话流程，包括工具调用和工具结果
//
// 参数：
// - sessionKey: 会话标识符
// - msg: 完整消息对象
func (sm *SessionManager) AddFullMessage(sessionKey string, msg providers.Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionKey]
	if !ok {
		session = &Session{
			Key:      sessionKey,
			Messages: []providers.Message{},
			Created:  time.Now(),
		}
		sm.sessions[sessionKey] = session
	}

	session.Messages = append(session.Messages, msg)
	session.Updated = time.Now()
}

// GetHistory 获取会话历史消息列表
//
// 参数：
// - key: 会话标识符
//
// 返回：
// - []providers.Message: 历史消息列表（副本）
func (sm *SessionManager) GetHistory(key string) []providers.Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok {
		return []providers.Message{}
	}

	history := make([]providers.Message, len(session.Messages))
	copy(history, session.Messages)
	return history
}

// GetSummary 获取会话摘要
//
// 参数：
// - key: 会话标识符
//
// 返回：
// - string: 会话摘要（如果不存在则返回空字符串）
func (sm *SessionManager) GetSummary(key string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok {
		return ""
	}
	return session.Summary
}

// SetSummary 设置会话摘要
//
// 参数：
// - key: 会话标识符
// - summary: 摘要内容
func (sm *SessionManager) SetSummary(key string, summary string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if ok {
		session.Summary = summary
		session.Updated = time.Now()
	}
}

// TruncateHistory 截断会话历史，保留最近的消息
// 用于控制会话大小，避免上下文过长
//
// 参数：
// - key: 会话标识符
// - keepLast: 要保留的消息数量（<=0 表示清空所有消息）
func (sm *SessionManager) TruncateHistory(key string, keepLast int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if !ok {
		return
	}

	if keepLast <= 0 {
		session.Messages = []providers.Message{}
		session.Updated = time.Now()
		return
	}

	if len(session.Messages) <= keepLast {
		return
	}

	session.Messages = session.Messages[len(session.Messages)-keepLast:]
	session.Updated = time.Now()
}

// sanitizeFilename 将-session key 转换为跨平台安全的文件名
// Session key 使用 "channel:chatID" 格式（如 "telegram:123456"）
// 但 ':' 在 Windows 上是卷分隔符，filepath.Base 会误解这个 key
// 因此将其替换为 '_'。原始 key 保存在 JSON 文件中，
// 所以 loadSessions 仍然可以映射回正确的内存 key
//
// 参数：
// - key: 会话标识符
//
// 返回：
// - string: 安全的文件名
func sanitizeFilename(key string) string {
	return strings.ReplaceAll(key, ":", "_")
}

// Save 保存会话到磁盘
// 使用原子写入模式：写入临时文件 -> sync -> 重命名
//
// 参数：
// - key: 会话标识符
//
// 返回：
// - error: 保存错误（如果 storage 为空则返回 nil）
func (sm *SessionManager) Save(key string) error {
	if sm.storage == "" {
		return nil
	}

	filename := sanitizeFilename(key)

	// filepath.IsLocal 拒绝空名称、".."、绝对路径和
	// OS 保留的设备名称（Windows 上的 NUL、COM1 等）
	// 额外检查拒绝 "." 和任何目录分隔符，确保
	// 会话文件总是直接写入到 sm.storage 目录下
	if filename == "." || !filepath.IsLocal(filename) || strings.ContainsAny(filename, `/\`) {
		return os.ErrInvalid
	}

	// 在读锁下创建快照，然后在解锁后执行慢速文件 I/O
	sm.mu.RLock()
	stored, ok := sm.sessions[key]
	if !ok {
		sm.mu.RUnlock()
		return nil
	}

	snapshot := Session{
		Key:     stored.Key,
		Summary: stored.Summary,
		Created: stored.Created,
		Updated: stored.Updated,
	}
	if len(stored.Messages) > 0 {
		snapshot.Messages = make([]providers.Message, len(stored.Messages))
		copy(snapshot.Messages, stored.Messages)
	} else {
		snapshot.Messages = []providers.Message{}
	}
	sm.mu.RUnlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	sessionPath := filepath.Join(sm.storage, filename+".json")
	tmpFile, err := os.CreateTemp(sm.storage, "session-*.tmp")
	if err != nil {
		return err
	}

	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(0o644); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, sessionPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// loadSessions 从磁盘加载所有会话
// 遍历存储目录中的所有 .json 文件并反序列化
//
// 返回：
// - error: 加载错误
func (sm *SessionManager) loadSessions() error {
	files, err := os.ReadDir(sm.storage)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		sessionPath := filepath.Join(sm.storage, file.Name())
		data, err := os.ReadFile(sessionPath)
		if err != nil {
			continue
		}

		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			continue
		}

		sm.sessions[session.Key] = &session
	}

	return nil
}

// SetHistory 设置会话的历史消息
// 用于批量更新或恢复会话历史
//
// 参数：
// - key: 会话标识符
// - history: 历史消息列表
func (sm *SessionManager) SetHistory(key string, history []providers.Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if ok {
		// 创建深拷贝以严格隔离内部状态和调用者的切片
		msgs := make([]providers.Message, len(history))
		copy(msgs, history)
		session.Messages = msgs
		session.Updated = time.Now()
	}
}
