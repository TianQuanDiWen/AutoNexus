package procjob

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestJobObjectTermination(t *testing.T) {
	job, err := NewJob()
	if err != nil {
		t.Fatalf("NewJob failed: %v", err)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "ping", "127.0.0.1", "-n", "20")
	} else {
		cmd = exec.Command("sleep", "20")
	}

	if err := StartInJob(job, cmd); err != nil {
		t.Fatalf("StartInJob failed: %v", err)
	}

	pid := cmd.Process.Pid
	t.Logf("Started child process PID: %d", pid)

	// 等待一小会儿确保进程已稳固启动
	time.Sleep(200 * time.Millisecond)

	// 终止作业对象
	if err := job.Terminate(0); err != nil {
		t.Fatalf("job.Terminate failed: %v", err)
	}

	// 验证进程已退出
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
		t.Logf("Process %d terminated successfully by JobObject", pid)
	case <-time.After(3 * time.Second):
		t.Fatalf("Process %d did not terminate in time after JobObject terminate", pid)
	}

	if err := job.Close(); err != nil {
		t.Fatalf("job.Close failed: %v", err)
	}
}
