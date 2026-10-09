package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"autonexus/internal/config"
	"autonexus/internal/executor"
)

func TestEngineQueueFlow(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "config.json")

	var execName string
	var args1, args2 []string
	if runtime.GOOS == "windows" {
		execName = "cmd.exe"
		args1 = []string{"/c", "echo task1"}
		args2 = []string{"/c", "echo task2"}
	} else {
		execName = "sh"
		args1 = []string{"-c", "echo task1"}
		args2 = []string{"-c", "echo task2"}
	}

	cfgMgr, err := config.NewManager(cfgPath)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	tasks := []*config.TaskConfig{
		{
			ID:              "t1",
			Name:            "Task 1",
			Enabled:         true,
			Executable:      execName,
			Args:            args1,
			CooldownSeconds: 1, // 缩短单测等待时间
		},
		{
			ID:              "t2",
			Name:            "Task 2",
			Enabled:         true,
			Executable:      execName,
			Args:            args2,
			CooldownSeconds: 1,
		},
	}
	if err := cfgMgr.UpdateTasks(tasks); err != nil {
		t.Fatalf("UpdateTasks failed: %v", err)
	}

	broadcaster := executor.NewBroadcaster(50)
	runner := executor.NewRunner(broadcaster)
	eng := NewEngine(cfgMgr, runner, broadcaster)

	status := eng.GetStatus()
	if status.State != StateIdle {
		t.Fatalf("Expected initial state IDLE, got %s", status.State)
	}

	if err := eng.StartQueue(false); err != nil {
		t.Fatalf("StartQueue failed: %v", err)
	}

	// 轮询等待队列执行完成 (最多 5 秒)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status = eng.GetStatus()
		if status.State == StateIdle && len(status.LastResults) == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	status = eng.GetStatus()
	if status.State != StateIdle {
		t.Fatalf("Expected final state IDLE, got %s", status.State)
	}
	if len(status.LastResults) != 2 {
		t.Fatalf("Expected 2 completed task results, got %d", len(status.LastResults))
	}
	if !status.LastResults[0].Success || !status.LastResults[1].Success {
		t.Fatalf("Expected all tasks to succeed, got results: %+v", status.LastResults)
	}
	t.Logf("Engine queue finished successfully with 2 tasks")

	// 再次启动队列 (非强制模式)，此时所有任务今日已完成，应立即跳过全部任务直接返回 IDLE
	if err := eng.StartQueue(false); err != nil {
		t.Fatalf("Second StartQueue(false) failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	status = eng.GetStatus()
	if status.State != StateIdle {
		t.Fatalf("Expected IDLE after skipping completed tasks, got %s", status.State)
	}
	if len(status.LastResults) != 0 {
		t.Fatalf("Expected 0 executed task results on skipped run, got %d", len(status.LastResults))
	}
	t.Logf("Engine successfully skipped all completed tasks in routine mode")

	_ = os.Remove(cfgPath)
}
