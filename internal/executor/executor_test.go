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
