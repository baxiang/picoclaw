// Package cron 提供定时任务服务功能
// 支持三种调度类型：
// - at: 一次性任务，在指定时间执行一次
// - every: 周期性任务，每隔固定时间执行
// - cron: Cron 表达式调度（如 "0 9 * * *" 每天 9 点）
package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/adhocore/gronx"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// CronSchedule 定时任务调度配置
// 支持三种调度类型：at（一次性）、every（周期）、cron（表达式）
//
// 字段说明：
// - Kind: 调度类型（"at"、"every"、"cron"）
// - AtMS: 一次性任务的执行时间（毫秒时间戳）
// - EveryMS: 周期性任务的间隔（毫秒）
// - Expr: Cron 表达式（如 "0 9 * * *"）
// - TZ: 时区（可选）
type CronSchedule struct {
	Kind    string `json:"kind"`
	AtMS    *int64 `json:"atMs,omitempty"`
	EveryMS *int64 `json:"everyMs,omitempty"`
	Expr    string `json:"expr,omitempty"`
	TZ      string `json:"tz,omitempty"`
}

// CronPayload 定时任务负载
// 包含任务执行时需要的参数
//
// 字段说明：
// - Kind: 负载类型（如 "agent_turn"）
// - Message: 任务消息内容
// - Command: 可选的命令
// - Deliver: 是否发送给用户
// - Channel: 目标渠道
// - To: 目标用户 ID
type CronPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Command string `json:"command,omitempty"`
	Deliver bool   `json:"deliver"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
}

// CronJobState 定时任务状态
// 记录任务的执行历史和下次执行时间
type CronJobState struct {
	NextRunAtMS *int64 `json:"nextRunAtMs,omitempty"` // 下次执行时间
	LastRunAtMS *int64 `json:"lastRunAtMs,omitempty"` // 上次执行时间
	LastStatus  string `json:"lastStatus,omitempty"`  // 上次执行状态（"ok"、"error"）
	LastError   string `json:"lastError,omitempty"`   // 上次执行错误信息
}

// CronJob 定时任务
// 完整的任务定义，包含调度、负载和状态
//
// 字段说明：
// - ID: 任务唯一标识符
// - Name: 任务名称
// - Enabled: 是否启用
// - Schedule: 调度配置
// - Payload: 任务负载
// - State: 任务状态
// - CreatedAtMS: 创建时间
// - UpdatedAtMS: 更新时间
// - DeleteAfterRun: 执行后是否删除（一次性任务）
type CronJob struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Enabled        bool         `json:"enabled"`
	Schedule       CronSchedule `json:"schedule"`
	Payload        CronPayload  `json:"payload"`
	State          CronJobState `json:"state"`
	CreatedAtMS    int64        `json:"createdAtMs"`
	UpdatedAtMS    int64        `json:"updatedAtMs"`
	DeleteAfterRun bool         `json:"deleteAfterRun"`
}

// CronStore 定时任务存储
// 持久化所有任务的 JSON 结构
type CronStore struct {
	Version int       `json:"version"` // 存储版本
	Jobs    []CronJob `json:"jobs"`    // 任务列表
}

// JobHandler 任务处理器函数类型
// 当任务到期时调用
//
// 参数：
// - job: 到期的任务
//
// 返回：
// - string: 执行结果
// - error: 执行错误
type JobHandler func(job *CronJob) (string, error)

// CronService 定时任务服务
// 管理任务的生命周期、调度和执行
//
// 字段说明：
// - storePath: 存储文件路径
// - store: 任务存储
// - onJob: 任务处理器回调
// - mu: 保护并发访问的读写锁
// - running: 服务是否正在运行
// - stopChan: 停止信号通道
// - gronx: Cron 表达式解析器
type CronService struct {
	storePath string
	store     *CronStore
	onJob     JobHandler
	mu        sync.RWMutex
	running   bool
	stopChan  chan struct{}
	gronx     *gronx.Gronx
}

// NewCronService 创建新的定时任务服务
//
// 参数：
// - storePath: 存储文件路径（JSON 文件）
// - onJob: 任务处理器回调
//
// 返回：
// - *CronService: 定时任务服务实例
func NewCronService(storePath string, onJob JobHandler) *CronService {
	cs := &CronService{
		storePath: storePath,
		onJob:     onJob,
		gronx:     gronx.New(),
	}
	// 创建时初始化并加载存储
	cs.loadStore()
	return cs
}

// Start 启动定时任务服务
// 加载存储、重新计算下次执行时间、启动检查循环
//
// 返回：
// - error: 启动错误
func (cs *CronService) Start() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.running {
		return nil
	}

	if err := cs.loadStore(); err != nil {
		return fmt.Errorf("failed to load store: %w", err)
	}

	// 重新计算所有任务的下次执行时间
	cs.recomputeNextRuns()
	if err := cs.saveStoreUnsafe(); err != nil {
		return fmt.Errorf("failed to save store: %w", err)
	}

	cs.stopChan = make(chan struct{})
	cs.running = true
	go cs.runLoop(cs.stopChan)

	return nil
}

// Stop 停止定时任务服务
// 关闭停止通道，退出检查循环
func (cs *CronService) Stop() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if !cs.running {
		return
	}

	cs.running = false
	if cs.stopChan != nil {
		close(cs.stopChan)
		cs.stopChan = nil
	}
}

// runLoop 运行任务检查循环
// 每秒检查一次是否有任务到期
//
// 参数：
// - stopChan: 停止信号通道
func (cs *CronService) runLoop(stopChan chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			cs.checkJobs()
		}
	}
}

// checkJobs 检查并执行到期的任务
// 在锁内收集到期任务 ID，在锁外执行以避免阻塞
func (cs *CronService) checkJobs() {
	cs.mu.Lock()

	if !cs.running {
		cs.mu.Unlock()
		return
	}

	now := time.Now().UnixMilli()
	var dueJobIDs []string

	// 收集到期的任务
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled && job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
			dueJobIDs = append(dueJobIDs, job.ID)
		}
	}

	// 在解锁前重置到期任务的 NextRunAtMS，避免重复执行
	dueMap := make(map[string]bool, len(dueJobIDs))
	for _, jobID := range dueJobIDs {
		dueMap[jobID] = true
	}
	for i := range cs.store.Jobs {
		if dueMap[cs.store.Jobs[i].ID] {
			cs.store.Jobs[i].State.NextRunAtMS = nil
		}
	}

	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}

	cs.mu.Unlock()

	// 在锁外执行任务
	for _, jobID := range dueJobIDs {
		cs.executeJobByID(jobID)
	}
}

// executeJobByID 执行指定 ID 的任务
// 调用 JobHandler 回调，更新任务状态
//
// 参数：
// - jobID: 任务 ID
func (cs *CronService) executeJobByID(jobID string) {
	startTime := time.Now().UnixMilli()

	// 复制任务用于回调（避免锁竞争）
	cs.mu.RLock()
	var callbackJob *CronJob
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID {
			jobCopy := *job
			callbackJob = &jobCopy
			break
		}
	}
	cs.mu.RUnlock()

	if callbackJob == nil {
		log.Printf("[cron] job %s not found, skipping", jobID)
		return
	}

	// 记录任务执行开始
	log.Printf("[cron] ▶ executing job '%s' (id: %s, schedule: %s, channel: %s)",
		callbackJob.Name, jobID, callbackJob.Schedule.Kind, callbackJob.Payload.Channel)

	// 调用任务处理器
	var err error
	if cs.onJob != nil {
		_, err = cs.onJob(callbackJob)
	}

	execDuration := time.Now().UnixMilli() - startTime

	// 获取锁更新状态
	cs.mu.Lock()
	defer cs.mu.Unlock()

	var job *CronJob
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			job = &cs.store.Jobs[i]
			break
		}
	}
	if job == nil {
		log.Printf("[cron] job %s disappeared before state update", jobID)
		return
	}

	job.State.LastRunAtMS = &startTime
	job.UpdatedAtMS = time.Now().UnixMilli()

	// 更新执行状态
	if err != nil {
		job.State.LastStatus = "error"
		job.State.LastError = err.Error()
		log.Printf("[cron] ✗ job '%s' failed after %dms: %v", job.Name, execDuration, err)
	} else {
		job.State.LastStatus = "ok"
		job.State.LastError = ""
	}

	// 计算下次执行时间
	var nextRunStr string
	if job.Schedule.Kind == "at" {
		// 一次性任务
		if job.DeleteAfterRun {
			cs.removeJobUnsafe(job.ID)
			nextRunStr = "(deleted)"
		} else {
			job.Enabled = false
			job.State.NextRunAtMS = nil
			nextRunStr = "(disabled)"
		}
	} else {
		// 周期性任务或 Cron 表达式
		nextRun := cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
		job.State.NextRunAtMS = nextRun
		if nextRun != nil {
			nextRunStr = time.UnixMilli(*nextRun).Format("2006-01-02 15:04:05")
		} else {
			nextRunStr = "(none)"
		}
	}

	if err == nil {
		log.Printf("[cron] ✓ job '%s' completed in %dms, next run: %s", job.Name, execDuration, nextRunStr)
	}

	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}
}

// computeNextRun 计算任务的下次执行时间
//
// 参数：
// - schedule: 调度配置
// - nowMS: 当前时间（毫秒时间戳）
//
// 返回：
// - *int64: 下次执行时间（毫秒时间戳），nil 表示无下次执行
func (cs *CronService) computeNextRun(schedule *CronSchedule, nowMS int64) *int64 {
	if schedule.Kind == "at" {
		// 一次性任务
		if schedule.AtMS != nil && *schedule.AtMS > nowMS {
			return schedule.AtMS
		}
		return nil
	}

	if schedule.Kind == "every" {
		// 周期性任务
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return nil
		}
		next := nowMS + *schedule.EveryMS
		return &next
	}

	if schedule.Kind == "cron" {
		// Cron 表达式任务
		if schedule.Expr == "" {
			return nil
		}

		// 使用 gronx 计算下次执行时间
		now := time.UnixMilli(nowMS)
		nextTime, err := gronx.NextTickAfter(schedule.Expr, now, false)
		if err != nil {
			log.Printf("[cron] failed to compute next run for expr '%s': %v", schedule.Expr, err)
			return nil
		}

		nextMS := nextTime.UnixMilli()
		return &nextMS
	}

	return nil
}

// recomputeNextRuns 重新计算所有任务的下次执行时间
// 在服务启动或任务更新后调用
func (cs *CronService) recomputeNextRuns() {
	now := time.Now().UnixMilli()
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled {
			job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, now)
		}
	}
}

// getNextWakeMS 获取下次唤醒时间（最早的任务执行时间）
// 用于优化检查间隔
//
// 返回：
// - *int64: 下次唤醒时间，nil 表示没有启用的任务
func (cs *CronService) getNextWakeMS() *int64 {
	var nextWake *int64
	for _, job := range cs.store.Jobs {
		if job.Enabled && job.State.NextRunAtMS != nil {
			if nextWake == nil || *job.State.NextRunAtMS < *nextWake {
				nextWake = job.State.NextRunAtMS
			}
		}
	}
	return nextWake
}

// Load 从文件加载任务存储
//
// 返回：
// - error: 加载错误
func (cs *CronService) Load() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.loadStore()
}

// SetOnJob 设置任务处理器回调
//
// 参数：
// - handler: 任务处理器函数
func (cs *CronService) SetOnJob(handler JobHandler) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJob = handler
}

// loadStore 从文件加载任务存储（内部方法，需持有锁）
//
// 返回：
// - error: 加载错误
func (cs *CronService) loadStore() error {
	cs.store = &CronStore{
		Version: 1,
		Jobs:    []CronJob{},
	}

	data, err := os.ReadFile(cs.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, cs.store)
}

// saveStoreUnsafe 保存任务存储到文件（内部方法，需持有锁）
// 使用原子写入确保数据安全
//
// 返回：
// - error: 保存错误
func (cs *CronService) saveStoreUnsafe() error {
	data, err := json.MarshalIndent(cs.store, "", "  ")
	if err != nil {
		return err
	}

	// 使用原子写入，权限设置为 0o600（仅所有者可读写）
	return fileutil.WriteFileAtomic(cs.storePath, data, 0o600)
}

// AddJob 添加新的定时任务
//
// 参数：
// - name: 任务名称
// - schedule: 调度配置
// - message: 任务消息
// - deliver: 是否发送给用户
// - channel: 目标渠道
// - to: 目标用户 ID
//
// 返回：
// - *CronJob: 添加的任务
// - error: 添加错误
func (cs *CronService) AddJob(
	name string,
	schedule CronSchedule,
	message string,
	deliver bool,
	channel, to string,
) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	now := time.Now().UnixMilli()

	// 一次性任务执行后删除
	deleteAfterRun := (schedule.Kind == "at")

	job := CronJob{
		ID:       generateID(),
		Name:     name,
		Enabled:  true,
		Schedule: schedule,
		Payload: CronPayload{
			Kind:    "agent_turn",
			Message: message,
			Deliver: deliver,
			Channel: channel,
			To:      to,
		},
		State: CronJobState{
			NextRunAtMS: cs.computeNextRun(&schedule, now),
		},
		CreatedAtMS:    now,
		UpdatedAtMS:    now,
		DeleteAfterRun: deleteAfterRun,
	}

	cs.store.Jobs = append(cs.store.Jobs, job)
	if err := cs.saveStoreUnsafe(); err != nil {
		return nil, err
	}

	return &job, nil
}

// UpdateJob 更新现有任务
//
// 参数：
// - job: 更新后的任务
//
// 返回：
// - error: 更新错误
func (cs *CronService) UpdateJob(job *CronJob) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i] = *job
			cs.store.Jobs[i].UpdatedAtMS = time.Now().UnixMilli()
			return cs.saveStoreUnsafe()
		}
	}
	return fmt.Errorf("job not found")
}

// RemoveJob 删除任务
//
// 参数：
// - jobID: 任务 ID
//
// 返回：
// - bool: 是否成功删除
func (cs *CronService) RemoveJob(jobID string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	return cs.removeJobUnsafe(jobID)
}

// removeJobUnsafe 删除任务（内部方法，需持有锁）
//
// 参数：
// - jobID: 任务 ID
//
// 返回：
// - bool: 是否成功删除
func (cs *CronService) removeJobUnsafe(jobID string) bool {
	before := len(cs.store.Jobs)
	var jobs []CronJob
	for _, job := range cs.store.Jobs {
		if job.ID != jobID {
			jobs = append(jobs, job)
		}
	}
	cs.store.Jobs = jobs
	removed := len(cs.store.Jobs) < before

	if removed {
		if err := cs.saveStoreUnsafe(); err != nil {
			log.Printf("[cron] failed to save store after remove: %v", err)
		}
	}

	return removed
}

// EnableJob 启用或禁用任务
//
// 参数：
// - jobID: 任务 ID
// - enabled: true 启用，false 禁用
//
// 返回：
// - *CronJob: 更新后的任务，nil 表示未找到
func (cs *CronService) EnableJob(jobID string, enabled bool) *CronJob {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID {
			job.Enabled = enabled
			job.UpdatedAtMS = time.Now().UnixMilli()

			if enabled {
				// 启用时重新计算下次执行时间
				job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
			} else {
				// 禁用时清除下次执行时间
				job.State.NextRunAtMS = nil
			}

			if err := cs.saveStoreUnsafe(); err != nil {
				log.Printf("[cron] failed to save store after enable: %v", err)
			}
			return job
		}
	}

	return nil
}

// ListJobs 列出所有任务
//
// 参数：
// - includeDisabled: 是否包含已禁用的任务
//
// 返回：
// - []CronJob: 任务列表
func (cs *CronService) ListJobs(includeDisabled bool) []CronJob {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if includeDisabled {
		return cs.store.Jobs
	}

	var enabled []CronJob
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabled = append(enabled, job)
		}
	}

	return enabled
}

// Status 返回服务状态信息
//
// 返回：
// - map[string]any: 状态映射（enabled、jobs、nextWakeAtMS）
func (cs *CronService) Status() map[string]any {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var enabledCount int
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabledCount++
		}
	}

	return map[string]any{
		"enabled":      cs.running,
		"jobs":         len(cs.store.Jobs),
		"nextWakeAtMS": cs.getNextWakeMS(),
	}
}

// generateID 生成唯一的任务 ID
// 使用 crypto/rand 确保并发下的唯一性
//
// 返回：
// - string: 16 字符十六进制 ID
func generateID() string {
	// 使用 crypto/rand 提高并发下的唯一性
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// 如果 crypto/rand 失败，回退到基于时间的 ID
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
