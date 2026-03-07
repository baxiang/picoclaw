// Package tools 提供 AI 工具的实现
// 本文件包含文件系统工具的实现：
// - ReadFileTool: 读取文件内容
// - WriteFileTool: 写入文件内容
// - ListDirTool: 列出目录内容
//
// 安全特性：
// - 工作空间限制：可配置是否限制在工作空间内
// - 路径验证：防止路径穿越攻击
// - 白名单支持：支持正则表达式白名单

package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil" // 文件工具（原子写入等）
)

// validatePath 验证给定路径是否安全
// 如果 restrict 为 true，确保路径在工作空间内
//
// 参数：
// - path: 要验证的路径
// - workspace: 工作空间目录
// - restrict: 是否限制在工作空间内
//
// 返回：
// - 清理后的绝对路径
// - 错误信息（如果路径不安全）
func validatePath(path, workspace string, restrict bool) (string, error) {
	if workspace == "" {
		return path, fmt.Errorf("workspace is not defined")
	}

	// 获取工作空间的绝对路径
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace path: %w", err)
	}

	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath, err = filepath.Abs(filepath.Join(absWorkspace, path))
		if err != nil {
			return "", fmt.Errorf("failed to resolve file path: %w", err)
		}
	}

	if restrict {
		// 检查路径是否在工作空间内
		if !isWithinWorkspace(absPath, absWorkspace) {
			return "", fmt.Errorf("access denied: path is outside the workspace")
		}

		// 解析符号链接，确保最终目标也在工作空间内
		var resolved string
		workspaceReal := absWorkspace
		if resolved, err = filepath.EvalSymlinks(absWorkspace); err == nil {
			workspaceReal = resolved
		}

		if resolved, err = filepath.EvalSymlinks(absPath); err == nil {
			if !isWithinWorkspace(resolved, workspaceReal) {
				return "", fmt.Errorf("access denied: symlink resolves outside workspace")
			}
		} else if os.IsNotExist(err) {
			// 文件不存在，检查父目录
			var parentResolved string
			if parentResolved, err = resolveExistingAncestor(filepath.Dir(absPath)); err == nil {
				if !isWithinWorkspace(parentResolved, workspaceReal) {
					return "", fmt.Errorf("access denied: symlink resolves outside workspace")
				}
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to resolve path: %w", err)
			}
		} else {
			return "", fmt.Errorf("failed to resolve path: %w", err)
		}
	}

	return absPath, nil
}

// resolveExistingAncestor 递归解析路径的已存在祖先
// 用于处理不存在文件的路径验证
//
// 参数：
// - path: 要解析的路径
//
// 返回：
// - 已解析的祖先路径
// - 错误信息
func resolveExistingAncestor(path string) (string, error) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}

// isWithinWorkspace 检查候选路径是否在工作空间内
//
// 参数：
// - candidate: 候选路径
// - workspace: 工作空间路径
//
// 返回：
// - true: 在工作空间内
// - false: 不在工作空间内
func isWithinWorkspace(candidate, workspace string) bool {
	rel, err := filepath.Rel(filepath.Clean(workspace), filepath.Clean(candidate))
	return err == nil && filepath.IsLocal(rel)
}

// ReadFileTool 读取文件工具
type ReadFileTool struct {
	fs fileSystem
}

// NewReadFileTool 创建读取文件工具
//
// 参数：
// - workspace: 工作空间目录
// - restrict: 是否限制在工作空间内
// - allowPaths: 可选的白名单路径模式列表
//
// 返回：
// - ReadFileTool 指针
func NewReadFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ReadFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &ReadFileTool{fs: buildFs(workspace, restrict, patterns)}
}

// Name 返回工具名称
func (t *ReadFileTool) Name() string {
	return "read_file"
}

// Description 返回工具描述
func (t *ReadFileTool) Description() string {
	return "Read the contents of a file"
}

// Parameters 返回工具的参数 schema
func (t *ReadFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read",
			},
		},
		"required": []string{"path"},
	}
}

// Execute 执行读取文件操作
//
// 参数：
// - ctx: 上下文用于取消控制
// - args: 工具参数（包含 path 字段）
//
// 返回：
// - ToolResult: 文件内容或错误信息
func (t *ReadFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorResult("path is required")
	}

	content, err := t.fs.ReadFile(path)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return NewToolResult(string(content))
}

// WriteFileTool 写入文件工具
type WriteFileTool struct {
	fs fileSystem
}

// NewWriteFileTool 创建写入文件工具
//
// 参数：
// - workspace: 工作空间目录
// - restrict: 是否限制在工作空间内
// - allowPaths: 可选的白名单路径模式列表
func NewWriteFileTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *WriteFileTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &WriteFileTool{fs: buildFs(workspace, restrict, patterns)}
}

// Name 返回工具名称
func (t *WriteFileTool) Name() string {
	return "write_file"
}

// Description 返回工具描述
func (t *WriteFileTool) Description() string {
	return "Write content to a file"
}

// Parameters 返回工具的参数 schema
func (t *WriteFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

// Execute 执行写入文件操作
//
// 参数：
// - ctx: 上下文用于取消控制
// - args: 工具参数（包含 path 和 content 字段）
//
// 返回：
// - ToolResult: 成功或错误信息
func (t *WriteFileTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		return ErrorResult("path is required")
	}

	content, ok := args["content"].(string)
	if !ok {
		return ErrorResult("content is required")
	}

	if err := t.fs.WriteFile(path, []byte(content)); err != nil {
		return ErrorResult(err.Error())
	}

	return SilentResult(fmt.Sprintf("File written: %s", path))
}

// ListDirTool 列出目录工具
type ListDirTool struct {
	fs fileSystem
}

// NewListDirTool 创建列出目录工具
//
// 参数：
// - workspace: 工作空间目录
// - restrict: 是否限制在工作空间内
// - allowPaths: 可选的白名单路径模式列表
func NewListDirTool(workspace string, restrict bool, allowPaths ...[]*regexp.Regexp) *ListDirTool {
	var patterns []*regexp.Regexp
	if len(allowPaths) > 0 {
		patterns = allowPaths[0]
	}
	return &ListDirTool{fs: buildFs(workspace, restrict, patterns)}
}

// Name 返回工具名称
func (t *ListDirTool) Name() string {
	return "list_dir"
}

// Description 返回工具描述
func (t *ListDirTool) Description() string {
	return "List files and directories in a path"
}

// Parameters 返回工具的参数 schema
func (t *ListDirTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to list",
			},
		},
		"required": []string{"path"},
	}
}

// Execute 执行列出目录操作
//
// 参数：
// - ctx: 上下文用于取消控制
// - args: 工具参数（包含 path 字段，可选，默认为 "."）
//
// 返回：
// - ToolResult: 目录列表或错误信息
func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	path, ok := args["path"].(string)
	if !ok {
		path = "."
	}

	entries, err := t.fs.ReadDir(path)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to read directory: %v", err))
	}
	return formatDirEntries(entries)
}

// formatDirEntries 格式化目录条目列表
//
// 参数：
// - entries: 目录条目列表
//
// 返回：
// - ToolResult: 格式化的字符串（每行 "DIR: name" 或 "FILE: name"）
func formatDirEntries(entries []os.DirEntry) *ToolResult {
	var result strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			result.WriteString("DIR:  " + entry.Name() + "\n")
		} else {
			result.WriteString("FILE: " + entry.Name() + "\n")
		}
	}
	return NewToolResult(result.String())
}

// fileSystem 文件系统接口
// 抽象了读取、写入和列出文件的操作
// 支持两种实现：
// 1. 无限制模式（hostFs）：直接访问主机文件系统
// 2. 沙盒模式（sandboxFs）：使用 os.Root 限制在工作空间内
type fileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	ReadDir(path string) ([]os.DirEntry, error)
}

// hostFs 无限制文件系统实现
// 直接在主机文件系统上操作，不使用沙盒限制
type hostFs struct{}

// ReadFile 读取文件内容
func (h *hostFs) ReadFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read file: file not found: %w", err)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("failed to read file: access denied: %w", err)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return content, nil
}

// ReadDir 列出目录内容
func (h *hostFs) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

// WriteFile 写入文件内容
// 使用原子写入确保数据一致性
func (h *hostFs) WriteFile(path string, data []byte) error {
	// 使用统一的原子写入工具，显式 sync 确保闪存存储可靠性
	// 使用 0o600（仅所有者可读写）作为安全默认权限
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

// sandboxFs 沙盒文件系统实现
// 使用 os.Root 严格限制在定义的工作空间内操作
type sandboxFs struct {
	workspace string // 工作空间目录
}

// execute 执行文件系统操作的辅助函数
// 打开工作空间根目录，计算相对路径，执行操作函数
//
// 参数：
// - path: 目标路径
// - fn: 操作函数，接收 os.Root 和相对路径
//
// 返回：
// - 错误信息
func (r *sandboxFs) execute(path string, fn func(root *os.Root, relPath string) error) error {
	if r.workspace == "" {
		return fmt.Errorf("workspace is not defined")
	}

	// 打开工作空间根目录
	root, err := os.OpenRoot(r.workspace)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer root.Close()

	// 计算安全的相对路径
	relPath, err := getSafeRelPath(r.workspace, path)
	if err != nil {
		return err
	}

	return fn(root, relPath)
}

// ReadFile 读取文件内容（沙盒模式）
func (r *sandboxFs) ReadFile(path string) ([]byte, error) {
	var content []byte
	err := r.execute(path, func(root *os.Root, relPath string) error {
		fileContent, err := root.ReadFile(relPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("failed to read file: file not found: %w", err)
			}
			// os.Root 对根目录外的路径返回 "escapes from parent"
			if os.IsPermission(err) || strings.Contains(err.Error(), "escapes from parent") ||
				strings.Contains(err.Error(), "permission denied") {
				return fmt.Errorf("failed to read file: access denied: %w", err)
			}
			return fmt.Errorf("failed to read file: %w", err)
		}
		content = fileContent
		return nil
	})
	return content, err
}

// WriteFile 写入文件内容（沙盒模式）
// 使用原子写入模式：写入临时文件 -> sync -> 重命名
func (r *sandboxFs) WriteFile(path string, data []byte) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		dir := filepath.Dir(relPath)
		if dir != "." && dir != "/" {
			// 创建父目录
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create parent directories: %w", err)
			}
		}

		// 原子写入：先写入临时文件
		tmpRelPath := fmt.Sprintf(".tmp-%d-%d", os.Getpid(), time.Now().UnixNano())

		tmpFile, err := root.OpenFile(tmpRelPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to open temp file: %w", err)
		}

		if _, err := tmpFile.Write(data); err != nil {
			tmpFile.Close()
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to write temp file: %w", err)
		}

		// 关键：强制 sync 到存储介质
		// 确保数据物理写入磁盘，而不仅仅是缓存
		if err := tmpFile.Sync(); err != nil {
			tmpFile.Close()
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to sync temp file: %w", err)
		}

		if err := tmpFile.Close(); err != nil {
			tmpFile.Close()
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to close temp file: %w", err)
		}

		// 重命名临时文件覆盖目标文件
		if err := root.Rename(tmpRelPath, relPath); err != nil {
			root.Remove(tmpRelPath)
			return fmt.Errorf("failed to rename temp file over target: %w", err)
		}

		// Sync 目录确保重命名持久化
		if dirFile, err := root.Open("."); err == nil {
			_ = dirFile.Sync()
			dirFile.Close()
		}

		return nil
	})
}

// ReadDir 列出目录内容（沙盒模式）
func (r *sandboxFs) ReadDir(path string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := r.execute(path, func(root *os.Root, relPath string) error {
		dirEntries, err := fs.ReadDir(root.FS(), relPath)
		if err != nil {
			return err
		}
		entries = dirEntries
		return nil
	})
	return entries, err
}

// whitelistFs 白名单文件系统
// 封装 sandboxFs，允许访问与白名单模式匹配的特定路径
// 即使这些路径在工作空间外
type whitelistFs struct {
	sandbox  *sandboxFs   // 沙盒文件系统
	host     hostFs       // 主机文件系统
	patterns []*regexp.Regexp // 白名单模式列表
}

// matches 检查路径是否匹配任何白名单模式
func (w *whitelistFs) matches(path string) bool {
	for _, p := range w.patterns {
		if p.MatchString(path) {
			return true
		}
	}
	return false
}

// ReadFile 读取文件（白名单模式）
// 如果路径匹配白名单，使用主机文件系统；否则使用沙盒
func (w *whitelistFs) ReadFile(path string) ([]byte, error) {
	if w.matches(path) {
		return w.host.ReadFile(path)
	}
	return w.sandbox.ReadFile(path)
}

// WriteFile 写入文件（白名单模式）
func (w *whitelistFs) WriteFile(path string, data []byte) error {
	if w.matches(path) {
		return w.host.WriteFile(path, data)
	}
	return w.sandbox.WriteFile(path, data)
}

// ReadDir 列出目录（白名单模式）
func (w *whitelistFs) ReadDir(path string) ([]os.DirEntry, error) {
	if w.matches(path) {
		return w.host.ReadDir(path)
	}
	return w.sandbox.ReadDir(path)
}

// buildFs 根据限制设置和可选的白名单模式构建适当的文件系统实现
//
// 参数：
// - workspace: 工作空间目录
// - restrict: 是否限制在工作空间内
// - patterns: 白名单模式列表
//
// 返回：
// - fileSystem 实现
func buildFs(workspace string, restrict bool, patterns []*regexp.Regexp) fileSystem {
	if !restrict {
		return &hostFs{} // 无限制模式
	}
	sandbox := &sandboxFs{workspace: workspace}
	if len(patterns) > 0 {
		// 沙盒 + 白名单模式
		return &whitelistFs{sandbox: sandbox, patterns: patterns}
	}
	return sandbox // 纯沙盒模式
}

// getSafeRelPath 获取安全的相对路径，用于 os.Root 操作
//
// 参数：
// - workspace: 工作空间目录
// - path: 目标路径
//
// 返回：
// - 相对路径
// - 错误信息（如果路径不安全）
func getSafeRelPath(workspace, path string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("workspace is not defined")
	}

	rel := filepath.Clean(path)
	if filepath.IsAbs(rel) {
		var err error
		rel, err = filepath.Rel(workspace, rel)
		if err != nil {
			return "", fmt.Errorf("failed to calculate relative path: %w", err)
		}
	}

	// 检查路径是否逃逸出工作空间
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}

	return rel, nil
}
