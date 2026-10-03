//go:build !windows

package procjob

import (
	"os"
	"sync"
	"syscall"
)

type fallbackJob struct {
	mu   sync.Mutex
	pids []int
}

// NewJob 返回非 Windows 平台的降级进程组管理器
func NewJob() (Job, error) {
	return &fallbackJob{}, nil
}

func (j *fallbackJob) AssignProcess(pid int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pids = append(j.pids, pid)
	return nil
}

func (j *fallbackJob) Terminate(exitCode uint32) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, pid := range j.pids {
		// 尝试杀死整个进程组，若失败则杀死单个进程
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		p, err := os.FindProcess(pid)
		if err == nil {
			_ = p.Kill()
		}
	}
	return nil
}

func (j *fallbackJob) Close() error {
	return j.Terminate(1)
}
