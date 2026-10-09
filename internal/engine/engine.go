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
	StateIdle            State = "IDLE"             // 空闲待命
	StatePrepare         State = "PREPARE"          // 准备就绪中
	StateRunning         State = "RUNNING"          // 任务执行中
	StateWaitingSchedule State = "WAITING_SCHEDULE" // 等待日常刷新时间到达
	StateCooling         State = "COOLING"          // 任务间冷却缓冲中 (释放显存与句柄)
	StateStopping        State = "STOPPING"         // 正在急停中断
)

// StatusSnapshot 引擎状态快照，用于 REST API 和前端展示
type StatusSnapshot struct {
	State                 State                  `json:"state"`
	CurrentTaskID         string                 `json:"current_task_id,omitempty"`
	CurrentTaskName       string                 `json:"current_task_name,omitempty"`
	CurrentTaskIndex      int                    `json:"current_task_index"`
	TotalTasksCount       int                    `json:"total_tasks_count"`
	ElapsedSeconds        int64                  `json:"elapsed_seconds"`
	CooldownRemaining     int                    `json:"cooldown_remaining"`
	ScheduleWaitRemaining int64                  `json:"schedule_wait_remaining,omitempty"`
	ScheduleWaitTarget    string                 `json:"schedule_wait_target,omitempty"`
	ScheduleEnabled       bool                   `json:"schedule_enabled"`
	ScheduleTime          string                 `json:"schedule_time"`
	ScheduleNextRun       string                 `json:"schedule_next_run,omitempty"`
	Tasks                 []*config.TaskConfig   `json:"tasks"`
	LastResults           []*executor.TaskResult `json:"last_results"`
}

// Engine 核心串行状态机调度引擎
type Engine struct {
	mu                    sync.RWMutex
	state                 State
	currentTaskID         string
	currentTaskName       string
	currentTaskIndex      int
	taskStartTime         time.Time
	cooldownRemaining     int
	scheduleWaitRemaining int64
	scheduleWaitTarget    string
	skipWaitChan          chan struct{}

	cancelFunc  context.CancelFunc
	configMgr   *config.Manager
	runner      *executor.Runner
	broadcaster *executor.Broadcaster

	lastResults []*executor.TaskResult

	scheduleReloadChan chan struct{}
	stopSchedulerChan  chan struct{}
}

// NewEngine 创建调度引擎
func NewEngine(cfgMgr *config.Manager, runner *executor.Runner, b *executor.Broadcaster) *Engine {
	eng := &Engine{
		state:              StateIdle,
		configMgr:          cfgMgr,
		runner:             runner,
		broadcaster:        b,
		lastResults:        make([]*executor.TaskResult, 0),
		skipWaitChan:       make(chan struct{}, 1),
		scheduleReloadChan: make(chan struct{}, 1),
		stopSchedulerChan:  make(chan struct{}),
	}

	go eng.schedulerLoop()
	return eng
}

// NotifyScheduleChanged 当自动调度配置更新时唤醒后台定时器重新计算
func (e *Engine) NotifyScheduleChanged() {
	select {
	case e.scheduleReloadChan <- struct{}{}:
	default:
	}
}

// Close 停止后台调度器
func (e *Engine) Close() {
	close(e.stopSchedulerChan)
	_ = e.StopQueue()
}

// GetStatus 获取当前引擎状态快照
func (e *Engine) GetStatus() StatusSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cfg := e.configMgr.Get()
	now := time.Now()

	var elapsed int64
	if !e.taskStartTime.IsZero() && (e.state == StateRunning || e.state == StateCooling) {
		elapsed = int64(time.Since(e.taskStartTime).Seconds())
	}

	resultsCopy := make([]*executor.TaskResult, len(e.lastResults))
	copy(resultsCopy, e.lastResults)

	// 动态计算每个任务在当前日常周期内是否已完成
	tasksCopy := make([]*config.TaskConfig, len(cfg.Tasks))
	for i, t := range cfg.Tasks {
		item := *t
		item.IsCompletedToday = config.IsTaskCompletedInCurrentCycle(t, now)
		tasksCopy[i] = &item
	}

	var nextRunStr string
	if cfg.ScheduleEnabled {
		nextRun := CalculateNextScheduleRun(cfg.ScheduleTime, now)
		nextRunStr = nextRun.Format("2006-01-02 15:04:05")
	}

	return StatusSnapshot{
		State:                 e.state,
		CurrentTaskID:         e.currentTaskID,
		CurrentTaskName:       e.currentTaskName,
		CurrentTaskIndex:      e.currentTaskIndex,
		TotalTasksCount:       len(cfg.Tasks),
		ElapsedSeconds:        elapsed,
		CooldownRemaining:     e.cooldownRemaining,
		ScheduleWaitRemaining: e.scheduleWaitRemaining,
		ScheduleWaitTarget:    e.scheduleWaitTarget,
		ScheduleEnabled:       cfg.ScheduleEnabled,
		ScheduleTime:          cfg.ScheduleTime,
		ScheduleNextRun:       nextRunStr,
		Tasks:                 tasksCopy,
		LastResults:           resultsCopy,
	}
}

// StartQueue 启动串行任务队列 (force=true 时忽略今日完成状态强制全跑，force=false 时智能跳过今日已完成任务)
func (e *Engine) StartQueue(force bool) error {
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

	PreventSleep()

	if force {
		e.broadcaster.EmitSystemLog("SCHEDULER", "================ 调度队列已启动 (强制全跑模式) ================")
	} else {
		e.broadcaster.EmitSystemLog("SCHEDULER", "================ 调度队列已启动 (智能日常模式) ================")
	}

	go e.runQueueLoop(ctx, force)
	return nil
}

// SkipScheduleWait 跳过当前等待刷新时刻，立即开跑
func (e *Engine) SkipScheduleWait() error {
	e.mu.RLock()
	st := e.state
	e.mu.RUnlock()

	if st != StateWaitingSchedule {
		return fmt.Errorf("当前未处于等待刷新时间状态")
	}

	select {
	case e.skipWaitChan <- struct{}{}:
	default:
	}
	return nil
}

// RunSingleTask 单独触发运行某一个任务（便于独立调试与补跑）
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

	PreventSleep()
	e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf(">>> 独立执行单个任务: %s", targetTask.Name))

	go func() {
		defer func() {
			AllowSleep()
			e.mu.Lock()
			e.state = StateIdle
			e.currentTaskID = ""
			e.currentTaskName = ""
			e.currentTaskIndex = 0
			e.cooldownRemaining = 0
			e.scheduleWaitRemaining = 0
			e.scheduleWaitTarget = ""
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
func (e *Engine) runQueueLoop(ctx context.Context, force bool) {
	defer func() {
		AllowSleep()
		e.mu.Lock()
		e.state = StateIdle
		e.currentTaskID = ""
		e.currentTaskName = ""
		e.currentTaskIndex = 0
		e.taskStartTime = time.Time{}
		e.cooldownRemaining = 0
		e.scheduleWaitRemaining = 0
		e.scheduleWaitTarget = ""
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

		now := time.Now()

		// 1. 智能清日常模式下检查：该任务在当前日常周期内是否已成功完成？
		if !force && config.IsTaskCompletedInCurrentCycle(task, now) {
			e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("[-] 任务 [%s] 在当前日常周期内已完成，自动跳过", task.Name))
			continue
		}

		// 2. 检查是否需要等待到达该任务的日常刷新时刻
		waitDur := config.GetWaitDurationUntilRefresh(task.RefreshTime, now)
		if !force && waitDur > 0 {
			waitSec := int64(waitDur.Seconds())
			e.mu.Lock()
			e.state = StateWaitingSchedule
			e.currentTaskID = task.ID
			e.currentTaskName = task.Name
			e.currentTaskIndex = idx + 1
			e.scheduleWaitTarget = task.RefreshTime
			e.scheduleWaitRemaining = waitSec
			e.mu.Unlock()

			e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("⏳ 任务 [%s] 尚未到达日常刷新时刻 (%s)，进入静默休眠等待 (剩余 %s)...", task.Name, task.RefreshTime, waitDur.Round(time.Second)))

			ticker := time.NewTicker(1 * time.Second)
			skipWait := false

		waitLoop:
			for {
				select {
				case <-ctx.Done():
					ticker.Stop()
					return
				case <-e.skipWaitChan:
					ticker.Stop()
					skipWait = true
					e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("⚡ 收到跳过等待指令，立即唤醒执行任务 [%s]！", task.Name))
					break waitLoop
				case <-ticker.C:
					e.mu.Lock()
					e.scheduleWaitRemaining--
					rem := e.scheduleWaitRemaining
					e.mu.Unlock()

					if rem <= 0 {
						ticker.Stop()
						break waitLoop
					}
				}
			}

			e.mu.Lock()
			e.scheduleWaitRemaining = 0
			e.scheduleWaitTarget = ""
			e.mu.Unlock()

			if !skipWait {
				e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("⏰ 刷新时刻已到，开始拉起任务 [%s]...", task.Name))
			}
		}

		// 3. 执行任务
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
	startTime := time.Now()

	e.mu.Lock()
	e.state = StateRunning
	e.currentTaskID = task.ID
	e.currentTaskName = task.Name
	e.currentTaskIndex = idx + 1
	e.taskStartTime = startTime
	e.mu.Unlock()

	e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("[%d/%d] 开始执行: %s", idx+1, total, task.Name))

	res := e.runner.Run(ctx, task)

	// 记录并落盘最近一次启动时间与运行时长
	startTimeStr := startTime.Format("2006-01-02 15:04:05")
	var durationStr string
	if res.Duration < time.Second {
		durationStr = res.Duration.Round(time.Millisecond).String()
	} else {
		durationStr = res.Duration.Round(time.Second).String()
	}
	_ = e.configMgr.UpdateTaskRunStats(task.ID, startTimeStr, durationStr, res.Success, res.ErrorMsg)

	e.mu.Lock()
	e.lastResults = append(e.lastResults, res)
	e.mu.Unlock()

	return res
}

// CalculateNextScheduleRun 计算下一次自动调度的触发时间点
func CalculateNextScheduleRun(scheduleTimeStr string, now time.Time) time.Time {
	h, m := config.ParseHourMinute(scheduleTimeStr, 4, 5)
	target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !now.Before(target) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// schedulerLoop 后台每日定时调度循环
func (e *Engine) schedulerLoop() {
	for {
		cfg := e.configMgr.Get()
		if !cfg.ScheduleEnabled {
			select {
			case <-e.stopSchedulerChan:
				return
			case <-e.scheduleReloadChan:
				continue
			}
		}

		now := time.Now()
		nextRun := CalculateNextScheduleRun(cfg.ScheduleTime, now)
		waitDuration := nextRun.Sub(now)

		timer := time.NewTimer(waitDuration)

		select {
		case <-e.stopSchedulerChan:
			timer.Stop()
			return
		case <-e.scheduleReloadChan:
			timer.Stop()
			continue
		case <-timer.C:
			e.broadcaster.EmitSystemLog("SCHEDULER", fmt.Sprintf("⏰ 到达每日自动调度时刻 (%s)，自动启动日常任务队列...", cfg.ScheduleTime))
			_ = e.StartQueue(false)
			// 休眠 2 秒避免同 1 秒内重复触发
			time.Sleep(2 * time.Second)
		}
	}
}
