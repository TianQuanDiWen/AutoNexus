package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"autonexus/internal/config"
	"autonexus/internal/executor"
)

// State 状态机状态枚举
type State string

const (
	StateIdle     State = "IDLE"     // 空闲待命
	StatePrepare  State = "PREPARE"  // 准备就绪中
	StateRunning  State = "RUNNING"  // 任务执行中
	StateCooling  State = "COOLING"  // 任务间冷却缓冲中 (释放显存与句柄)
	StateStopping State = "STOPPING" // 正在急停中断
)

// StatusSnapshot 引擎状态快照，用于 REST API 和前端展示
type StatusSnapshot struct {
	State             State                  `json:"state"`
	CurrentTaskID     string                 `json:"current_task_id,omitempty"`
	CurrentTaskName   string                 `json:"current_task_name,omitempty"`
	CurrentTaskIndex  int                    `json:"current_task_index"`
	TotalTasksCount   int                    `json:"total_tasks_count"`
	ElapsedSeconds    int64                  `json:"elapsed_seconds"`
	CooldownRemaining int                    `json:"cooldown_remaining"`
	Tasks             []*config.TaskConfig   `json:"tasks"`
	LastResults       []*executor.TaskResult `json:"last_results"`
}

// Engine 核心串行状态机调度引擎
type Engine struct {
	mu                sync.RWMutex
	state             State
	currentTaskID     string
	currentTaskName   string
	currentTaskIndex  int
	taskStartTime     time.Time
	cooldownRemaining int

	cancelFunc  context.CancelFunc
	configMgr   *config.Manager
	runner      *executor.Runner
	broadcaster *executor.Broadcaster

	lastResults []*executor.TaskResult
}

// NewEngine 创建调度引擎
func NewEngine(cfgMgr *config.Manager, runner *executor.Runner, b *executor.Broadcaster) *Engine {
	return &Engine{
		state:       StateIdle,
		configMgr:   cfgMgr,
		runner:      runner,
		broadcaster: b,
		lastResults: make([]*executor.TaskResult, 0),
	}
}

// GetStatus 获取当前引擎状态快照
func (e *Engine) GetStatus() StatusSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cfg := e.configMgr.Get()
	var elapsed int64
	if !e.taskStartTime.IsZero() && (e.state == StateRunning || e.state == StateCooling) {
		elapsed = int64(time.Since(e.taskStartTime).Seconds())
	}

	resultsCopy := make([]*executor.TaskResult, len(e.lastResults))
	copy(resultsCopy, e.lastResults)

	return StatusSnapshot{
		State:             e.state,
		CurrentTaskID:     e.currentTaskID,
		CurrentTaskName:   e.currentTaskName,
		CurrentTaskIndex:  e.currentTaskIndex,
		TotalTasksCount:   len(cfg.Tasks),
		ElapsedSeconds:    elapsed,
		CooldownRemaining: e.cooldownRemaining,
		Tasks:             cfg.Tasks,
		LastResults:       resultsCopy,
	}
}

// StartQueue 启动串行任务队列
func (e *Engine) StartQueue() error {
	e.mu.Lock()
	if e.state != StateIdle {
		e.mu.Unlock()
		return fmt.Errorf("当前引擎正在运行中 (状态: %s)，无法重复启动", e.state)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.cancelFunc = cancel
	e.state = StatePrepare
	e.lastResults = make([]*executor.TaskResult, 0)
	e.mu.Unlock()

	e.broadcaster.EmitSystemLog("SCHEDULER", "================ 调度队列已启动 ================")

	go e.runQueueLoop(ctx)
	return nil
}

// RunSingleTask 单独触发运行某一个任务（便于独立调试）
func (e *Engine) RunSingleTask(taskID string) error {
	e.mu.Lock()
	if e.state != StateIdle {
		e.mu.Unlock()
		return fmt.Errorf("当前引擎正在运行中 (状态: %s)，无法单独启动", e.state)
	}

	cfg := e.configMgr.Get()
	var targetTask *config.TaskConfig
	for _, t := range cfg.Tasks {
		if t.ID == taskID {
			targetTask = t
			break
		}
	}
	if targetTask == nil {
		e.mu.Unlock()
		return fmt.Errorf("未找到 ID 为 %s 的任务", taskID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.cancelFunc = cancel
	e.state = StatePrepare
	e.lastResults = make([]*executor.TaskResult, 0)
	e.mu.Unlock()

	e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf(">>> 独立执行单个任务: %s", targetTask.Name))

	go func() {
		defer func() {
			e.mu.Lock()
			e.state = StateIdle
			e.currentTaskID = ""
			e.currentTaskName = ""
			e.currentTaskIndex = 0
			e.cooldownRemaining = 0
			e.taskStartTime = time.Time{}
			e.cancelFunc = nil
			e.mu.Unlock()
			e.broadcaster.EmitSystemLog("SCHEDULER", "================ 单项任务执行完毕，回到待命状态 ================")
		}()

		e.executeOneTask(ctx, targetTask, 0, 1)
	}()

	return nil
}

// StopQueue 急停当前任务与中断后续所有队列
func (e *Engine) StopQueue() error {
	e.mu.Lock()
	if e.state == StateIdle {
		e.mu.Unlock()
		return nil
	}

	e.state = StateStopping
	cancel := e.cancelFunc
	e.mu.Unlock()

	e.broadcaster.EmitSystemLog("SCHEDULER", "!!! 收到急停指令，正在强杀当前进程并中止队列...")
	if cancel != nil {
		cancel()
	}
	return nil
}

// runQueueLoop 串行执行队列循环
func (e *Engine) runQueueLoop(ctx context.Context) {
	defer func() {
		e.mu.Lock()
		e.state = StateIdle
		e.currentTaskID = ""
		e.currentTaskName = ""
		e.currentTaskIndex = 0
		e.taskStartTime = time.Time{}
		e.cooldownRemaining = 0
		e.cancelFunc = nil
		e.mu.Unlock()
		e.broadcaster.EmitSystemLog("SCHEDULER", "================ 队列全部完成，系统已就绪 ================")
	}()

	cfg := e.configMgr.Get()
	total := len(cfg.Tasks)

	for idx, task := range cfg.Tasks {
		select {
		case <-ctx.Done():
			e.broadcaster.EmitSystemLog("SCHEDULER", "队列调度已被用户中断退出")
			return
		default:
		}

		if !task.Enabled {
			e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("[-] 跳过已禁用任务: %s", task.Name))
			continue
		}

		e.executeOneTask(ctx, task, idx, total)

		// 检查中断
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 检查后续是否还有已启用的任务，且用户配置了大于 0 的冷却时间
		hasMoreEnabled := false
		for _, nextTask := range cfg.Tasks[idx+1:] {
			if nextTask.Enabled {
				hasMoreEnabled = true
				break
			}
		}

		cooldown := task.CooldownSeconds
		if hasMoreEnabled && cooldown > 0 {
			e.mu.Lock()
			e.state = StateCooling
			e.cooldownRemaining = cooldown
			e.mu.Unlock()

			e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("进入系统冷却缓冲 (%d 秒)，等待显存与系统句柄彻底释放...", cooldown))

			for c := cooldown; c > 0; c-- {
				e.mu.Lock()
				e.cooldownRemaining = c
				e.mu.Unlock()

				select {
				case <-ctx.Done():
					return
				case <-time.After(1 * time.Second):
				}
			}

			e.mu.Lock()
			e.cooldownRemaining = 0
			e.mu.Unlock()
		}
	}
}

func (e *Engine) executeOneTask(ctx context.Context, task *config.TaskConfig, idx, total int) *executor.TaskResult {
	e.mu.Lock()
	e.state = StateRunning
	e.currentTaskID = task.ID
	e.currentTaskName = task.Name
	e.currentTaskIndex = idx + 1
	e.taskStartTime = time.Now()
	e.mu.Unlock()

	e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("[%d/%d] 开始执行: %s", idx+1, total, task.Name))

	res := e.runner.Run(ctx, task)

	e.mu.Lock()
	e.lastResults = append(e.lastResults, res)
	e.mu.Unlock()

	return res
}
