package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TaskConfig 自动化单项任务配置
type TaskConfig struct {
	ID                  string   `json:"id"`                             // 任务唯一标识符
	Name                string   `json:"name"`                           // 任务友好名称
	Enabled             bool     `json:"enabled"`                        // 是否参与队列轮转调度
	Executable          string   `json:"executable"`                     // 执行程序路径 (如 madoaxvv-agent.exe)
	Args                []string `json:"args"`                           // 启动命令行参数
	WorkingDir          string   `json:"working_dir"`                    // 工作目录 (留空则使用可执行文件所在目录)
	GameProcessNames    []string `json:"game_process_names"`             // 关联游戏进程名，用于任务结束后的残留清理
	TimeoutSeconds      int      `json:"timeout_seconds"`                // 最大运行时长限制 (秒，0 为不限)
	NoLogTimeoutSeconds int      `json:"no_log_timeout_seconds"`         // 静默无日志卡死检测阈值 (秒，0 为禁用)
	CooldownSeconds     int      `json:"cooldown_seconds"`               // 任务结束后的冷却释放时间 (秒，默认 5~10s)
	RefreshTime         string   `json:"refresh_time,omitempty"`         // 每日日常刷新时间 (如 "04:00"，留空不限制)
	LastStartTime       string   `json:"last_start_time,omitempty"`      // 最近一次启动时间 (如 2006-01-02 15:04:05)
	LastDuration        string   `json:"last_duration,omitempty"`        // 最近一次运行时长 (如 12s, 641ms)
	LastSuccess         *bool    `json:"last_success,omitempty"`         // 最近一次运行是否成功 (true=成功, false=失败)
	LastError           string   `json:"last_error,omitempty"`           // 若失败记录具体错误原因
	IsCompletedToday    bool     `json:"is_completed_today,omitempty"`   // 动态状态：在当前日常周期内是否已成功执行
}

// Config 全局服务配置
type Config struct {
	Host            string        `json:"host"`             // 监听地址，默认 0.0.0.0 供局域网访问
	Port            int           `json:"port"`             // 监听端口，默认 18080
	ScheduleEnabled bool          `json:"schedule_enabled"` // 是否开启每日自动定时调度
	ScheduleTime    string        `json:"schedule_time"`    // 每日自动调度启动时间 (如 "04:05")
	Tasks           []*TaskConfig `json:"tasks"`            // 任务列表（按调度顺序排列）
}

// DefaultConfig 生成开箱即用的默认配置模版（黑盒任务由用户通过 Web 控制台灵活添加）
func DefaultConfig() *Config {
	return &Config{
		Host:            "0.0.0.0",
		Port:            18080,
		ScheduleEnabled: false,
		ScheduleTime:    "04:05",
		Tasks:           []*TaskConfig{},
	}
}

// Manager 配置管理器，支持线程安全读写与保存
type Manager struct {
	mu       sync.RWMutex
	filePath string
	cfg      *Config
}

// NewManager 初始化或读取配置文件
func NewManager(filePath string) (*Manager, error) {
	if filePath == "" {
		filePath = "config.json"
	}

	mgr := &Manager{filePath: filePath}
	if err := mgr.loadOrCreate(); err != nil {
		return nil, err
	}
	return mgr, nil
}

func (m *Manager) loadOrCreate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.filePath)
	if os.IsNotExist(err) {
		m.cfg = DefaultConfig()
		return m.saveLocked()
	} else if err != nil {
		return fmt.Errorf("read config file error: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config json error: %w", err)
	}

	if cfg.Port == 0 {
		cfg.Port = 18080
	}
	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}
	if cfg.ScheduleTime == "" {
		cfg.ScheduleTime = "04:05"
	}
	for _, t := range cfg.Tasks {
		if t.RefreshTime == "" {
			t.RefreshTime = "04:00"
		}
	}
	m.cfg = &cfg
	return nil
}

// Get 获取当前配置的浅拷贝
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 复制切片指针引用
	tasksCopy := make([]*TaskConfig, len(m.cfg.Tasks))
	for i, t := range m.cfg.Tasks {
		item := *t
		tasksCopy[i] = &item
	}

	return Config{
		Host:            m.cfg.Host,
		Port:            m.cfg.Port,
		ScheduleEnabled: m.cfg.ScheduleEnabled,
		ScheduleTime:    m.cfg.ScheduleTime,
		Tasks:           tasksCopy,
	}
}

// UpdateTasks 更新任务列表并落盘持久化
func (m *Manager) UpdateTasks(tasks []*TaskConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg.Tasks = tasks
	return m.saveLocked()
}

// UpdateSchedule 更新每日自动定时调度配置并落盘
func (m *Manager) UpdateSchedule(enabled bool, scheduleTime string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg.ScheduleEnabled = enabled
	if scheduleTime != "" {
		m.cfg.ScheduleTime = scheduleTime
	}
	return m.saveLocked()
}

// SetTaskEnabled 设置特定任务的启停开关
func (m *Manager) SetTaskEnabled(taskID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	found := false
	for _, t := range m.cfg.Tasks {
		if t.ID == taskID {
			t.Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("task %s not found", taskID)
	}
	return m.saveLocked()
}

// AddTask 动态新增一个任务并落盘
func (m *Manager) AddTask(task *TaskConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if task.ID == "" {
		task.ID = fmt.Sprintf("task_%d", time.Now().UnixMilli())
	}
	if task.Name == "" {
		task.Name = task.ID
	}
	if task.Executable == "" {
		return fmt.Errorf("可执行文件路径不能为空")
	}
	if task.RefreshTime == "" {
		task.RefreshTime = "04:00"
	}

	for _, t := range m.cfg.Tasks {
		if t.ID == task.ID {
			return fmt.Errorf("任务 ID '%s' 已存在", task.ID)
		}
	}

	m.cfg.Tasks = append(m.cfg.Tasks, task)
	return m.saveLocked()
}

// UpdateTask 更新已有任务配置并落盘
func (m *Manager) UpdateTask(task *TaskConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if task.ID == "" {
		return fmt.Errorf("缺少任务 ID")
	}
	if task.Executable == "" {
		return fmt.Errorf("可执行文件路径不能为空")
	}

	found := false
	for i, t := range m.cfg.Tasks {
		if t.ID == task.ID {
			if task.RefreshTime == "" {
				task.RefreshTime = t.RefreshTime
			}
			if task.LastStartTime == "" {
				task.LastStartTime = t.LastStartTime
			}
			if task.LastDuration == "" {
				task.LastDuration = t.LastDuration
			}
			if task.LastSuccess == nil {
				task.LastSuccess = t.LastSuccess
			}
			if task.LastError == "" {
				task.LastError = t.LastError
			}
			m.cfg.Tasks[i] = task
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("未找到任务 ID '%s'", task.ID)
	}
	return m.saveLocked()
}

// UpdateTaskRunStats 更新任务的最近启动时间、运行时长与执行结果并落盘持久化
func (m *Manager) UpdateTaskRunStats(taskID string, startTime string, duration string, success bool, errorMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.cfg.Tasks {
		if t.ID == taskID {
			t.LastStartTime = startTime
			t.LastDuration = duration
			t.LastSuccess = &success
			t.LastError = errorMsg
			break
		}
	}
	return m.saveLocked()
}

// DeleteTask 删除指定任务并落盘
func (m *Manager) DeleteTask(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	newTasks := make([]*TaskConfig, 0, len(m.cfg.Tasks))
	found := false
	for _, t := range m.cfg.Tasks {
		if t.ID == taskID {
			found = true
			continue
		}
		newTasks = append(newTasks, t)
	}
	if !found {
		return fmt.Errorf("未找到任务 ID '%s'", taskID)
	}
	m.cfg.Tasks = newTasks
	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	dir := filepath.Dir(m.filePath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.filePath, data, 0644)
}

// ParseHourMinute 解析 "HH:MM" 格式，若不符合格式返回默认时分
func ParseHourMinute(s string, defaultHour, defaultMin int) (int, int) {
	if len(s) == 5 && s[2] == ':' {
		var h, m int
		if _, err := fmt.Sscanf(s, "%02d:%02d", &h, &m); err == nil {
			if h >= 0 && h < 24 && m >= 0 && m < 60 {
				return h, m
			}
		}
	}
	return defaultHour, defaultMin
}

// GetCycleStartTime 计算指定刷新时间在当前时刻下的日常周期起始时间
func GetCycleStartTime(refreshTimeStr string, now time.Time) time.Time {
	h, m := ParseHourMinute(refreshTimeStr, 4, 0)
	todayRefresh := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if now.Before(todayRefresh) {
		// 尚未到达今天的刷新时间，属于昨日日常周期
		return todayRefresh.AddDate(0, 0, -1)
	}
	// 已到达或超过今天的刷新时间，属于今日日常周期
	return todayRefresh
}

// IsTaskCompletedInCurrentCycle 判断任务在当前日常周期内是否已成功执行完成
func IsTaskCompletedInCurrentCycle(task *TaskConfig, now time.Time) bool {
	if task == nil || task.LastSuccess == nil || !*task.LastSuccess || task.LastStartTime == "" {
		return false
	}
	lastStart, err := time.ParseInLocation("2006-01-02 15:04:05", task.LastStartTime, now.Location())
	if err != nil {
		return false
	}
	cycleStart := GetCycleStartTime(task.RefreshTime, now)
	return !lastStart.Before(cycleStart)
}

// GetWaitDurationUntilRefresh 计算距任务下一次日常刷新还需等待的时间 (未到刷新时间返回 > 0，已过刷新时间返回 0)
func GetWaitDurationUntilRefresh(refreshTimeStr string, now time.Time) time.Duration {
	if refreshTimeStr == "" {
		return 0
	}
	h, m := ParseHourMinute(refreshTimeStr, 4, 0)
	todayRefresh := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if now.Before(todayRefresh) {
		return todayRefresh.Sub(now)
	}
	return 0
}
