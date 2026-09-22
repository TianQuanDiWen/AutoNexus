//go:build windows

package procjob

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsJob struct {
	mu     sync.Mutex
	handle windows.Handle
	closed bool
}

// NewJob 创建一个新的 Windows Job Object，并配置 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE 属性
func NewJob() (Job, error) {
	hJob, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("windows.CreateJobObject failed: %w", err)
	}

	// 配置内核参数：当 Job 句柄关闭时，内核自动强制终止 Job 内所有进程
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	_, err = windows.SetInformationJobObject(
		hJob,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(hJob)
		return nil, fmt.Errorf("windows.SetInformationJobObject failed: %w", err)
	}

	return &windowsJob{handle: hJob}, nil
}

func (j *windowsJob) AssignProcess(pid int) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.closed {
		return fmt.Errorf("job is already closed")
	}

	// 获取进程句柄，需要 PROCESS_SET_QUOTA 和 PROCESS_TERMINATE 权限
	const desiredAccess = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	hProcess, err := windows.OpenProcess(desiredAccess, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("windows.OpenProcess(%d) failed: %w", pid, err)
	}
	defer windows.CloseHandle(hProcess)

	if err := windows.AssignProcessToJobObject(j.handle, hProcess); err != nil {
		return fmt.Errorf("windows.AssignProcessToJobObject failed for pid %d: %w", pid, err)
	}

	return nil
}

func (j *windowsJob) Terminate(exitCode uint32) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.closed {
		return nil
	}

	// 终止 Job 内所有关联进程
	if err := windows.TerminateJobObject(j.handle, exitCode); err != nil {
		return fmt.Errorf("windows.TerminateJobObject failed: %w", err)
	}
	return nil
}

func (j *windowsJob) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.closed {
		return nil
	}
	j.closed = true

	// 关闭 Job 句柄。由于配置了 KILL_ON_JOB_CLOSE，任何尚存的子孙进程将在此刻被操作系统内核直接杀死
	if err := windows.CloseHandle(j.handle); err != nil {
		return fmt.Errorf("windows.CloseHandle failed: %w", err)
	}
	return nil
}
