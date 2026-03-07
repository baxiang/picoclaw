// Package memory 提供记忆存储功能
// 本文件包含 JSONL 文件存储实现

package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	// numLockShards 锁分片数量（固定 64 个）
	// 使用分片数组而非 map 来序列化每个会话的访问
	// 这在进程生命周期内保持内存有界，对于长期运行的守护进程非常重要
	numLockShards = 64

	// maxLineSize JSONL 文件中单行的最大大小（10 MB）
	// 工具结果（read_file、web search 等）可能很大，因此设置较大的限制
	// Scanner 从 64 KB 开始，根据需要增长到此上限
	maxLineSize = 10 * 1024 * 1024 // 10 MB
)

// sessionMeta 会话元数据结构
// 存储在 .meta.json 文件中的每个会话的元数据
type sessionMeta struct {
	Key       string    `json:"key"`        // 会话标识符
	Summary   string    `json:"summary"`    // 会话摘要
	Skip      int       `json:"skip"`       // 逻辑上跳过（截断）的消息数
	Count     int       `json:"count"`      // 消息总数
	CreatedAt time.Time `json:"created_at"` // 创建时间
	UpdatedAt time.Time `json:"updated_at"` // 最后更新时间
}

// JSONLStore JSONL 文件存储实现
// 使用追加写入的 JSONL 文件实现持久化存储
//
// 每个会话存储为两个文件：
//
//	{sanitized_key}.jsonl      — 每行一个 JSON 编码的消息，仅追加写入
//	{sanitized_key}.meta.json  — 会话元数据（摘要、逻辑截断偏移量）
//
// 消息永远不会从 JSONL 文件中物理删除。
// TruncateHistory 在元数据文件中记录 "skip" 偏移量，
// GetHistory 会忽略该偏移量之前的行。
// 这使所有写入都是追加式的，既快速又具有崩溃安全性。
type JSONLStore struct {
	dir   string                  // 存储目录
	locks [numLockShards]sync.Mutex // 锁分片数组
}

// NewJSONLStore 创建 JSONL 存储
// 在指定目录创建 JSONL 存储
//
// 参数：
// - dir: 存储目录路径
//
// 返回：
// - *JSONLStore: JSONL 存储指针
// - error: 创建错误
func NewJSONLStore(dir string) (*JSONLStore, error) {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return nil, fmt.Errorf("memory: create directory: %w", err)
	}
	return &JSONLStore{dir: dir}, nil
}

// sessionLock 返回给定会话键的互斥锁
// 通过 FNV 哈希将会话键映射到固定的分片池
// 因此无论总共有多少会话，内存使用都是 O(1)
func (s *JSONLStore) sessionLock(key string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &s.locks[h.Sum32()%numLockShards]
}

// jsonlPath 返回 JSONL 文件路径
func (s *JSONLStore) jsonlPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".jsonl")
}

// metaPath 返回元数据文件路径
func (s *JSONLStore) metaPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+".meta.json")
}

// sanitizeKey 将会话键转换为安全的文件名片段
// 镜像 pkg/session.sanitizeFilename 以便迁移路径匹配
//
// 注意：这是一个有损映射 — "telegram:123" 和 "telegram_123"
// 会生成相同的文件名。这是一个有意的权衡：
// 带冒号的键（如来自渠道的）是最常见的情况，
// 双向编码（如 URL 编码）会使文件列表和调试复杂化。
func sanitizeKey(key string) string {
	return strings.ReplaceAll(key, ":", "_")
}

// readMeta 读取会话的元数据文件
// 如果文件不存在，返回零值的 sessionMeta
//
// 参数：
// - key: 会话标识符
//
// 返回：
// - sessionMeta: 会话元数据
// - error: 读取错误
func (s *JSONLStore) readMeta(key string) (sessionMeta, error) {
	data, err := os.ReadFile(s.metaPath(key))
	if os.IsNotExist(err) {
		return sessionMeta{Key: key}, nil
	}
	if err != nil {
		return sessionMeta{}, fmt.Errorf("memory: read meta: %w", err)
	}
	var meta sessionMeta
	err = json.Unmarshal(data, &meta)
	if err != nil {
		return sessionMeta{}, fmt.Errorf("memory: decode meta: %w", err)
	}
	return meta, nil
}

// writeMeta 使用项目的标准 WriteFileAtomic 原子写入元数据文件
// （临时文件 + fsync + 重命名模式）
//
// 参数：
// - key: 会话标识符
// - meta: 会话元数据
//
// 返回：
// - error: 写入错误
func (s *JSONLStore) writeMeta(key string, meta sessionMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode meta: %w", err)
	}
	return fileutil.WriteFileAtomic(s.metaPath(key), data, 0o644)
}

// readMessages 读取 .jsonl 文件中的有效 JSON 行
// 跳过前 `skip` 行而不反序列化它们，避免对逻辑上已截断的消息进行 json.Unmarshal 的开销
// 格式错误的尾部行（如崩溃导致的部分写入）会被静默跳过
//
// 参数：
// - path: JSONL 文件路径
// - skip: 要跳过的行数
//
// 返回：
// - []providers.Message: 消息列表
// - error: 读取错误
func readMessages(path string, skip int) ([]providers.Message, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []providers.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	var msgs []providers.Message
	scanner := bufio.NewScanner(f)
	// 允许大行用于工具结果（read_file、web search 等）
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	lineNum := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lineNum++
		if lineNum <= skip {
			continue
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// 格式错误的行 — 可能是崩溃导致的部分写入
			// 记录日志以便操作员知道数据被跳过，但不要让
			// 整个读取失败；这是标准的 JSONL 恢复模式
			log.Printf("memory: skipping corrupt line %d in %s: %v",
				lineNum, filepath.Base(path), err)
			continue
		}
		msgs = append(msgs, msg)
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("memory: scan jsonl: %w", scanner.Err())
	}

	if msgs == nil {
		msgs = []providers.Message{}
	}
	return msgs, nil
}

// countLines 计算 .jsonl 文件中非空行的总数
// 用于 TruncateHistory 来校正陈旧的 meta.Count 而无需
// 反序列化每条消息的开销
//
// 参数：
// - path: JSONL 文件路径
//
// 返回：
// - int: 行数
// - error: 计数错误
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("memory: open jsonl: %w", err)
	}
	defer f.Close()

	n := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			n++
		}
	}
	return n, scanner.Err()
}

// AddMessage 添加简单消息到会话
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
// - role: 消息角色
// - content: 消息内容
//
// 返回：
// - error: 添加错误
func (s *JSONLStore) AddMessage(
	_ context.Context, sessionKey, role, content string,
) error {
	return s.addMsg(sessionKey, providers.Message{
		Role:    role,
		Content: content,
	})
}

// AddFullMessage 添加完整消息到会话
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
// - msg: 完整消息对象
//
// 返回：
// - error: 添加错误
func (s *JSONLStore) AddFullMessage(
	_ context.Context, sessionKey string, msg providers.Message,
) error {
	return s.addMsg(sessionKey, msg)
}

// addMsg AddMessage 和 AddFullMessage 的共享实现
func (s *JSONLStore) addMsg(sessionKey string, msg providers.Message) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	// 将消息追加为单行 JSON
	line, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("memory: marshal message: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(
		s.jsonlPath(sessionKey),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0o644,
	)
	if err != nil {
		return fmt.Errorf("memory: open jsonl for append: %w", err)
	}
	_, writeErr := f.Write(line)
	if writeErr != nil {
		f.Close()
		return fmt.Errorf("memory: append message: %w", writeErr)
	}
	// 在关闭前刷新到物理存储。这匹配
	// writeMeta 和 rewriteJSONL 的持久性保证（它们使用
	// WriteFileAtomic 和 fsync）。如果没有 Sync，断电可能
	// 使追加仅保留在内核页面缓存中 — 重启后丢失。
	if syncErr := f.Sync(); syncErr != nil {
		f.Close()
		return fmt.Errorf("memory: sync jsonl: %w", syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("memory: close jsonl: %w", closeErr)
	}

	// 更新元数据
	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.Count == 0 && meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Count++
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

// GetHistory 获取会话历史消息
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
//
// 返回：
// - []providers.Message: 历史消息列表
// - error: 读取错误
func (s *JSONLStore) GetHistory(
	_ context.Context, sessionKey string,
) ([]providers.Message, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return nil, err
	}

	// 传递 meta.Skip 给 readMessages，以便跳过那些行而
	// 不反序列化它们 — 避免在已截断消息上浪费 CPU
	msgs, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return nil, err
	}

	return msgs, nil
}

// GetSummary 获取会话摘要
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
//
// 返回：
// - string: 会话摘要
// - error: 读取错误
func (s *JSONLStore) GetSummary(
	_ context.Context, sessionKey string,
) (string, error) {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return "", err
	}
	return meta.Summary, nil
}

// SetSummary 设置会话摘要
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
// - summary: 摘要内容
//
// 返回：
// - error: 写入错误
func (s *JSONLStore) SetSummary(
	_ context.Context, sessionKey, summary string,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Summary = summary
	meta.UpdatedAt = now

	return s.writeMeta(sessionKey, meta)
}

// TruncateHistory 截断会话历史，保留最后 keepLast 条消息
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
// - keepLast: 要保留的消息数量（<=0 表示删除所有消息）
//
// 返回：
// - error: 截断错误
func (s *JSONLStore) TruncateHistory(
	_ context.Context, sessionKey string, keepLast int,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}

	// 始终将 meta.Count 与磁盘上的实际行数校正
	// 如果在 addMsg 的 JSONL 追加和元数据更新之间发生崩溃，
	// 会使 meta.Count 陈旧（如文件有 101 行但 meta 说 100）
	// 计数行数很便宜 — 不需要反序列化，只需扫描 — 而且
	// TruncateHistory 不是热点路径，所以始终重新计数
	n, countErr := countLines(s.jsonlPath(sessionKey))
	if countErr != nil {
		return countErr
	}
	meta.Count = n

	if keepLast <= 0 {
		meta.Skip = meta.Count
	} else {
		effective := meta.Count - meta.Skip
		if keepLast < effective {
			meta.Skip = meta.Count - keepLast
		}
	}
	meta.UpdatedAt = time.Now()

	return s.writeMeta(sessionKey, meta)
}

// SetHistory 设置会话历史消息
// 用提供的历史消息替换会话中的所有消息
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
// - history: 历史消息列表
//
// 返回：
// - error: 写入错误
func (s *JSONLStore) SetHistory(
	_ context.Context,
	sessionKey string,
	history []providers.Message,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	now := time.Now()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.Skip = 0
	meta.Count = len(history)
	meta.UpdatedAt = now

	// 在重写 JSONL 文件之前先写入元数据。如果在两者之间发生崩溃，
	// meta 的 Skip=0 且旧文件仍然完好，
	// 所以 GetHistory 从第 1 行读取 — 返回"太多"消息而不是丢失数据
	// 下一次 SetHistory 调用会校正这个问题
	err = s.writeMeta(sessionKey, meta)
	if err != nil {
		return err
	}

	return s.rewriteJSONL(sessionKey, history)
}

// Compact 物理重写 JSONL 文件，删除所有逻辑上已跳过的行
// 回收在多次 TruncateHistory 调用后累积的磁盘空间
//
// 任何时候调用都是安全的；如果没有什么可压缩的（skip == 0），
// 此方法会立即返回
//
// 参数：
// - ctx: 上下文用于取消控制
// - sessionKey: 会话标识符
//
// 返回：
// - error: 压缩错误
func (s *JSONLStore) Compact(
	_ context.Context, sessionKey string,
) error {
	l := s.sessionLock(sessionKey)
	l.Lock()
	defer l.Unlock()

	meta, err := s.readMeta(sessionKey)
	if err != nil {
		return err
	}
	if meta.Skip == 0 {
		return nil
	}

	// 仅读取活动消息，跳过已截断的行而不反序列化它们
	active, err := readMessages(s.jsonlPath(sessionKey), meta.Skip)
	if err != nil {
		return err
	}

	// 在重写 JSONL 文件之前先写入元数据。如果进程
	// 在两者之间崩溃，meta 的 Skip=0 且旧的
	// （未压缩）文件仍然完好，所以 GetHistory 从第 1 行读取 —
	// 返回之前被截断的消息而不是丢失数据
	// 下一次 Compact 或 TruncateHistory 会校正这个问题
	meta.Skip = 0
	meta.Count = len(active)
	meta.UpdatedAt = time.Now()

	err = s.writeMeta(sessionKey, meta)
	if err != nil {
		return err
	}

	return s.rewriteJSONL(sessionKey, active)
}

// rewriteJSONL 使用项目的标准 WriteFileAtomic 原子替换 JSONL 文件
// （临时文件 + fsync + 重命名模式）
//
// 参数：
// - sessionKey: 会话标识符
// - msgs: 消息列表
//
// 返回：
// - error: 写入错误
func (s *JSONLStore) rewriteJSONL(
	sessionKey string, msgs []providers.Message,
) error {
	var buf bytes.Buffer
	for i, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("memory: marshal message %d: %w", i, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return fileutil.WriteFileAtomic(s.jsonlPath(sessionKey), buf.Bytes(), 0o644)
}

// Close 关闭存储，释放资源
//
// 返回：
// - error: 关闭错误（JSONL 存储无需特殊清理，返回 nil）
func (s *JSONLStore) Close() error {
	return nil
}
