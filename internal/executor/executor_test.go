package executor

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"autonexus/internal/config"
)

func TestExecutorRunAndLog(t *testing.T) {
	b := NewBroadcaster(100)
	ch, _ := b.Subscribe(100)
	defer b.Unsubscribe(ch)

	runner := NewRunner(b)

	var task *config.TaskConfig
	if runtime.GOOS == "windows" {
		task = &config.TaskConfig{
			ID:                  "test-task",
			Name:                "Windows Ping Test",
			Executable:          "cmd.exe",
			Args:                []string{"/c", "echo line1 && echo line2"},
			TimeoutSeconds:      10,
			NoLogTimeoutSeconds: 5,
		}
	} else {
		task = &config.TaskConfig{
			ID:                  "test-task",
			Name:                "Unix Echo Test",
			Executable:          "sh",
			Args:                []string{"-c", "echo line1 && echo line2"},
			TimeoutSeconds:      10,
			NoLogTimeoutSeconds: 5,
		}
	}

	ctx := context.Background()
	res := runner.Run(ctx, task)

	if !res.Success {
		t.Fatalf("Expected task success, got error: %s", res.ErrorMsg)
	}

	// 验证日志广播中是否接收到了条目
	receivedLogs := 0
readLoop:
	for {
		select {
		case <-ch:
			receivedLogs++
		default:
			break readLoop
		}
	}

	if receivedLogs == 0 {
		t.Fatalf("Expected to receive logs through broadcaster, but received none")
	}
	t.Logf("Task executed successfully, received %d log events", receivedLogs)

	// 验证单任务专属日志获取
	taskLogs := b.GetTaskHistory(task.ID)
	if len(taskLogs) == 0 {
		t.Fatalf("Expected GetTaskHistory to return logs for task %s, but got 0", task.ID)
	}
	t.Logf("GetTaskHistory returned %d logs for task %s", len(taskLogs), task.ID)
}

func TestBroadcasterTaskLogs(t *testing.T) {
	b := NewBroadcaster(10)
	b.logDir = t.TempDir() // 使用测试隔离临时目录

	taskID := "task_isolated_test"
	b.StartTaskSession(taskID)

	b.Broadcast(LogEntry{
		Timestamp: "12:00:00.000",
		TaskID:    taskID,
		Stream:    "stdout",
		Message:   "hello from isolated task",
	})
	b.Broadcast(LogEntry{
		Timestamp: "12:00:01.000",
		TaskID:    "another_task",
		Stream:    "stdout",
		Message:   "hello from another task",
	})

	b.EndTaskSession(taskID)

	// 验证 task_isolated_test 仅包含自己的日志
	logs := b.GetTaskHistory(taskID)
	if len(logs) != 1 {
		t.Fatalf("Expected 1 log for %s, got %d", taskID, len(logs))
	}
	if logs[0].Message != "hello from isolated task" {
		t.Fatalf("Unexpected message: %s", logs[0].Message)
	}

	// 清空内存缓存，验证从持久化日志文件恢复的能力
	b.mu.Lock()
	delete(b.taskLogs, taskID)
	b.mu.Unlock()

	restoredLogs := b.GetTaskHistory(taskID)
	if len(restoredLogs) != 1 {
		t.Fatalf("Expected 1 restored log from file, got %d", len(restoredLogs))
	}
	if restoredLogs[0].Message != "hello from isolated task" {
		t.Fatalf("Unexpected restored message: %s", restoredLogs[0].Message)
	}
}

func TestExecutorEmergencyStop(t *testing.T) {
	b := NewBroadcaster(50)
	runner := NewRunner(b)

	var task *config.TaskConfig
	if runtime.GOOS == "windows" {
		task = &config.TaskConfig{
			ID:                  "cancel-test",
			Name:                "Windows Sleep Test",
			Executable:          "cmd.exe",
			Args:                []string{"/c", "ping 127.0.0.1 -n 10"},
			TimeoutSeconds:      30,
			NoLogTimeoutSeconds: 0,
		}
	} else {
		task = &config.TaskConfig{
			ID:                  "cancel-test",
			Name:                "Unix Sleep Test",
			Executable:          "sleep",
			Args:                []string{"10"},
			TimeoutSeconds:      30,
			NoLogTimeoutSeconds: 0,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	// 500ms 后触发急停取消
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res := runner.Run(ctx, task)
	duration := time.Since(start)

	if res.Success {
		t.Fatalf("Expected task to fail due to emergency stop, but it succeeded")
	}
	if duration > 3*time.Second {
		t.Fatalf("Task cancel took too long: %v", duration)
	}

	history := b.GetHistory()
	cancelLogCount := 0
	for _, entry := range history {
		if strings.Contains(entry.Message, "[中断]") {
			cancelLogCount++
		}
	}
	if cancelLogCount != 1 {
		t.Fatalf("Expected exactly 1 cancel log, got %d (select spin detected!)", cancelLogCount)
	}

	t.Logf("Emergency stop verified successfully in %v, error msg: %s, cancel log count: %d", duration, res.ErrorMsg, cancelLogCount)
}
