package procjob

import (
	"fmt"
	"os/exec"
)

// Job 定义了进程作业管家接口，用于内核级纳管子进程树的生命周期
type Job interface {
	// AssignProcess 将指定 PID 的进程加入此 Job Object
	AssignProcess(pid int) error
	// Terminate 强制终止 Job 内的所有进程
	Terminate(exitCode uint32) error
	// Close 关闭 Job 句柄（在配置了 KillOnJobClose 的情况下，未退出的子进程树会被内核自动强杀）
	Close() error
}

// StartInJob 启动一个 exec.Cmd 并立即将其纳管入指定的 Job 中
func StartInJob(job Job, cmd *exec.Cmd) error {
	if job == nil {
		return fmt.Errorf("job cannot be nil")
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	if err := job.AssignProcess(cmd.Process.Pid); err != nil {
		// 注入 Job 失败时立即杀死进程，防止逃逸
		_ = cmd.Process.Kill()
		return fmt.Errorf("failed to assign process %d to job: %w", cmd.Process.Pid, err)
	}
	return nil
}
