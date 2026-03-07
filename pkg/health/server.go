// Package health 提供健康检查服务
// 用于容器编排（Kubernetes、Docker）的健康和就绪探测
// 支持：
// - /health: 健康检查端点，返回服务运行状态
// - /ready: 就绪检查端点，返回服务是否准备好接收流量
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"
)

// Server 健康检查服务器
// 管理健康状态和检查项，提供 HTTP 端点
//
// 字段说明：
// - server: HTTP 服务器实例
// - mu: 保护 ready 和 checks 的读写锁
// - ready: 服务就绪标志
// - checks: 注册的健康检查项映射
// - startTime: 服务启动时间（用于计算运行时间）
type Server struct {
	server    *http.Server
	mu        sync.RWMutex
	ready     bool
	checks    map[string]Check
	startTime time.Time
}

// Check 健康检查项
// 表示单个组件的健康状态
type Check struct {
	Name      string    `json:"name"`      // 检查项名称
	Status    string    `json:"status"`    // 状态："ok"（正常）| "fail"（失败）
	Message   string    `json:"message,omitempty"` // 附加消息（可选）
	Timestamp time.Time `json:"timestamp"` // 检查时间戳
}

// StatusResponse 健康状态响应
// HTTP 端点返回的 JSON 结构
type StatusResponse struct {
	Status string           `json:"status"` // 总体状态
	Uptime string           `json:"uptime"` // 运行时间
	Checks map[string]Check `json:"checks,omitempty"` // 检查项详情（仅 /ready 端点）
}

// NewServer 创建新的健康检查服务器
//
// 参数：
// - host: 监听主机（如 "0.0.0.0" 或 "127.0.0.1"）
// - port: 监听端口
//
// 返回：
// - 初始化好的 Server 指针
func NewServer(host string, port int) *Server {
	mux := http.NewServeMux()
	s := &Server{
		ready:     false,
		checks:    make(map[string]Check),
		startTime: time.Now(),
	}

	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)

	addr := fmt.Sprintf("%s:%d", host, port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	return s
}

// Start 启动健康检查服务器
// 阻塞调用，直到服务器停止
//
// 返回：
// - error: 启动错误
func (s *Server) Start() error {
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return s.server.ListenAndServe()
}

// StartContext 带上下文控制的启动
// 支持通过上下文取消来优雅关闭服务器
//
// 参数：
// - ctx: 上下文用于取消控制
//
// 返回：
// - error: 启动或关闭错误
func (s *Server) StartContext(ctx context.Context) error {
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return s.server.Shutdown(context.Background())
	}
}

// Stop 停止健康检查服务器
//
// 参数：
// - ctx: 上下文用于关闭超时控制
//
// 返回：
// - error: 关闭错误
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	return s.server.Shutdown(ctx)
}

// SetReady 设置服务就绪状态
//
// 参数：
// - ready: 是否就绪
func (s *Server) SetReady(ready bool) {
	s.mu.Lock()
	s.ready = ready
	s.mu.Unlock()
}

// RegisterCheck 注册健康检查项
// 立即执行检查函数并记录结果
//
// 参数：
// - name: 检查项名称
// - checkFn: 检查函数，返回 (是否健康，附加消息)
func (s *Server) RegisterCheck(name string, checkFn func() (bool, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	status, msg := checkFn()
	s.checks[name] = Check{
		Name:      name,
		Status:    statusString(status),
		Message:   msg,
		Timestamp: time.Now(),
	}
}

// healthHandler /health 端点处理器
// 返回基本健康状态和运行时间
// 不检查具体组件状态，只确认服务进程在运行
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	uptime := time.Since(s.startTime)
	resp := StatusResponse{
		Status: "ok",
		Uptime: uptime.String(),
	}

	json.NewEncoder(w).Encode(resp)
}

// readyHandler /ready 端点处理器
// 返回服务就绪状态和所有检查项详情
// 如果任何检查项失败，返回 503 Service Unavailable
func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	s.mu.RLock()
	ready := s.ready
	checks := make(map[string]Check)
	maps.Copy(checks, s.checks)
	s.mu.RUnlock()

	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(StatusResponse{
			Status: "not ready",
			Checks: checks,
		})
		return
	}

	for _, check := range checks {
		if check.Status == "fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(StatusResponse{
				Status: "not ready",
				Checks: checks,
			})
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	uptime := time.Since(s.startTime)
	json.NewEncoder(w).Encode(StatusResponse{
		Status: "ready",
		Uptime: uptime.String(),
		Checks: checks,
	})
}

// RegisterOnMux 将健康检查端点注册到共享的 HTTP 服务器
// 当健康检查与其他服务共享同一个 HTTP 服务器时使用
//
// 参数：
// - mux: HTTP 多路复用器
func (s *Server) RegisterOnMux(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)
}

// statusString 将布尔值转换为状态字符串
func statusString(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}
