package executor

import (
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

// Broadcaster 日志广播中心，维护历史环形缓冲并分发给所有 WebSocket 订阅者
type Broadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan LogEntry]struct{}
	history     []LogEntry
	maxHistory  int
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
	}
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

// Broadcast 推送一条新日志
func (b *Broadcaster) Broadcast(entry LogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 记录到历史缓冲区 (原地覆盖，杜绝切片缩容与反复重新分配)
	if len(b.history) >= b.maxHistory {
		copy(b.history, b.history[1:])
		b.history[len(b.history)-1] = entry
	} else {
		b.history = append(b.history, entry)
	}

	// 非阻塞推送到各个订阅者
	for ch := range b.subscribers {
		select {
		case ch <- entry:
		default:
			// 若订阅通道已满，丢弃该订阅者的最新帧以防阻塞主进程（客户端网络卡顿保护）
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
