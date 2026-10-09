package executor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogEntry 单条结构化日志
type LogEntry struct {
	Timestamp string `json:"timestamp"` // 格式化时间 15:04:05.000
	TaskID    string `json:"task_id"`   // 关联任务 ID
	Stream    string `json:"stream"`    // "stdout", "stderr", "system"
	Message   string `json:"message"`   // 日志文本内容
}

// Broadcaster 日志广播中心，维护历史环形缓冲并分发给所有 WebSocket 订阅者，同时为每个任务维护独立日志
type Broadcaster struct {
	mu             sync.RWMutex
	subscribers    map[chan LogEntry]struct{}
	history        []LogEntry
	maxHistory     int
	taskLogs       map[string][]LogEntry // 单任务独立日志缓冲
	maxTaskLogs    int
	logDir         string
	activeTaskID   string
	activeTaskFile *os.File
}

// NewBroadcaster 创建日志广播器
func NewBroadcaster(maxHistory int) *Broadcaster {
	if maxHistory <= 0 {
		maxHistory = 1000
	}
	return &Broadcaster{
		subscribers: make(map[chan LogEntry]struct{}),
		history:     make([]LogEntry, 0, maxHistory),
		maxHistory:  maxHistory,
		taskLogs:    make(map[string][]LogEntry),
		maxTaskLogs: 2000,
		logDir:      "logs",
	}
}

// sanitizeTaskID 清洗任务 ID 用于生成安全的文件名
func sanitizeTaskID(id string) string {
	var sb strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	res := sb.String()
	if res == "" {
		return "unknown"
	}
	return res
}

// StartTaskSession 开启某个任务的专属执行日志会话，清空其旧日志并打开/覆盖文件
func (b *Broadcaster) StartTaskSession(taskID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.activeTaskFile != nil {
		_ = b.activeTaskFile.Close()
		b.activeTaskFile = nil
	}

	b.activeTaskID = taskID
	b.taskLogs[taskID] = make([]LogEntry, 0, 100)

	if b.logDir != "" {
		_ = os.MkdirAll(b.logDir, 0755)
		filename := filepath.Join(b.logDir, sanitizeTaskID(taskID)+".log")
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			b.activeTaskFile = f
		}
	}
}

// EndTaskSession 结束任务专属日志会话并冲刷关闭文件
func (b *Broadcaster) EndTaskSession(taskID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.activeTaskFile != nil {
		_ = b.activeTaskFile.Sync()
		_ = b.activeTaskFile.Close()
		b.activeTaskFile = nil
	}
	b.activeTaskID = ""
}

// Subscribe 注册一个订阅通道，并返回历史日志切片
func (b *Broadcaster) Subscribe(bufferSize int) (chan LogEntry, []LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan LogEntry, bufferSize)
	b.subscribers[ch] = struct{}{}

	// 复制历史日志副本
	historyCopy := make([]LogEntry, len(b.history))
	copy(historyCopy, b.history)

	return ch, historyCopy
}

// Unsubscribe 取消订阅
func (b *Broadcaster) Unsubscribe(ch chan LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(ch)
	}
}

// GetHistory 获取历史日志切片副本
func (b *Broadcaster) GetHistory() []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	historyCopy := make([]LogEntry, len(b.history))
	copy(historyCopy, b.history)
	return historyCopy
}

// GetTaskHistory 获取特定任务的专属执行日志（内存优先，若为空尝试从持久化文件恢复）
func (b *Broadcaster) GetTaskHistory(taskID string) []LogEntry {
	b.mu.RLock()
	tLogs, ok := b.taskLogs[taskID]
	if ok && len(tLogs) > 0 {
		historyCopy := make([]LogEntry, len(tLogs))
		copy(historyCopy, tLogs)
		b.mu.RUnlock()
		return historyCopy
	}
	logDir := b.logDir
	b.mu.RUnlock()

	if logDir == "" {
		return []LogEntry{}
	}
	filename := filepath.Join(logDir, sanitizeTaskID(taskID)+".log")
	f, err := os.Open(filename)
	if err != nil {
		return []LogEntry{}
	}
	defer f.Close()

	var fileLogs []LogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) == 3 {
			stream := strings.Trim(parts[1], "[]")
			fileLogs = append(fileLogs, LogEntry{
				Timestamp: parts[0],
				TaskID:    taskID,
				Stream:    stream,
				Message:   parts[2],
			})
		} else if len(parts) > 0 && strings.TrimSpace(line) != "" {
			fileLogs = append(fileLogs, LogEntry{
				Timestamp: "",
				TaskID:    taskID,
				Stream:    "stdout",
				Message:   line,
			})
		}
	}
	return fileLogs
}

// Broadcast 推送一条新日志
func (b *Broadcaster) Broadcast(entry LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 1. 记录到全局历史缓冲区 (原地覆盖)
	if len(b.history) >= b.maxHistory {
		copy(b.history, b.history[1:])
		b.history[len(b.history)-1] = entry
	} else {
		b.history = append(b.history, entry)
	}

	// 2. 记录到单任务专属缓冲区与持久化文件
	if entry.TaskID != "" && entry.TaskID != "SCHEDULER" {
		tLogs := b.taskLogs[entry.TaskID]
		if len(tLogs) >= b.maxTaskLogs {
			copy(tLogs, tLogs[1:])
			tLogs[len(tLogs)-1] = entry
		} else {
			tLogs = append(tLogs, entry)
		}
		b.taskLogs[entry.TaskID] = tLogs

		if b.activeTaskFile != nil && b.activeTaskID == entry.TaskID {
			_, _ = fmt.Fprintf(b.activeTaskFile, "%s\t[%s]\t%s\n", entry.Timestamp, entry.Stream, entry.Message)
		}
	}

	// 3. 非阻塞推送到各个订阅者
	for ch := range b.subscribers {
		select {
		case ch <- entry:
		default:
		}
	}
}

// EmitSystemLog 快速输出一条调度系统级日志
func (b *Broadcaster) EmitSystemLog(taskID, message string) {
	b.Broadcast(LogEntry{
		Timestamp: time.Now().Format("15:04:05.000"),
		TaskID:    taskID,
		Stream:    "system",
		Message:   message,
	})
}
