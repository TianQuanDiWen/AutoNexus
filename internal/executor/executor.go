package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"autonexus/internal/config"
	"autonexus/internal/procjob"
)

// TaskResult 任务执行结果报告
type TaskResult struct {
	TaskID    string        `json:"task_id"`
	Success   bool          `json:"success"`
	ExitCode  int           `json:"exit_code"`
	Duration  time.Duration `json:"duration"`
	ErrorMsg  string        `json:"error_msg,omitempty"`
	CleanedUp []string      `json:"cleaned_up,omitempty"`
}

// Runner 负责单项任务的安全隔离执行与管道流式消费
type Runner struct {
	broadcaster *Broadcaster
}

// NewRunner 创建任务运行器
func NewRunner(b *Broadcaster) *Runner {
	return &Runner{broadcaster: b}
}

// Run 串行运行单个自动化任务
func (r *Runner) Run(ctx context.Context, task *config.TaskConfig) (res *TaskResult) {
	startTime := time.Now()
	res = &TaskResult{
		TaskID:  task.ID,
		Success: false,
	}

	r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf(">>> 准备启动任务 [%s] (%s)", task.Name, task.Executable))

	// 解除 Windows 下载文件锁定，防止系统弹出 "打开文件 - 安全警告"
	if runtime.GOOS == "windows" {
		_ = os.Remove(task.Executable + ":Zone.Identifier")
	}

	// 1. 创建专用 Win32 Job Object
	job, err := procjob.NewJob()
	if err != nil {
		res.ErrorMsg = fmt.Sprintf("创建 Win32 JobObject 失败: %v", err)
		r.broadcaster.EmitSystemLog(task.ID, res.ErrorMsg)
		res.Duration = time.Since(startTime)
		return res
	}
	defer func() {
		// 任务结束后务必关闭 Job 句柄，触发内核最终防线清理
		_ = job.Close()
	}()

	// 2. 准备执行命令（自动剥离多余的外层引号，防止 Windows 子进程接收到字面量双引号）
	cleanArgs := make([]string, len(task.Args))
	for i, arg := range task.Args {
		if len(arg) >= 2 && ((arg[0] == '"' && arg[len(arg)-1] == '"') || (arg[0] == '\'' && arg[len(arg)-1] == '\'')) {
			cleanArgs[i] = arg[1 : len(arg)-1]
		} else {
			cleanArgs[i] = arg
		}
	}
	cmd := exec.Command(task.Executable, cleanArgs...)
	if task.WorkingDir != "" {
		cmd.Dir = task.WorkingDir
	} else {
		cmd.Dir = filepath.Dir(task.Executable)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		res.ErrorMsg = fmt.Sprintf("获取 stdout 管道失败: %v", err)
		r.broadcaster.EmitSystemLog(task.ID, res.ErrorMsg)
		res.Duration = time.Since(startTime)
		return res
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		res.ErrorMsg = fmt.Sprintf("获取 stderr 管道失败: %v", err)
		r.broadcaster.EmitSystemLog(task.ID, res.ErrorMsg)
		res.Duration = time.Since(startTime)
		return res
	}

	// 3. 将子进程启动并立即加入内核作业对象
	if err := procjob.StartInJob(job, cmd); err != nil {
		res.ErrorMsg = fmt.Sprintf("启动子进程或绑定 JobObject 失败: %v", err)
		r.broadcaster.EmitSystemLog(task.ID, res.ErrorMsg)
		res.Duration = time.Since(startTime)
		return res
	}

	r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("子进程已成功启动并纳管入 JobObject，PID: %d", cmd.Process.Pid))

	// 4. 异步独立消费 stdout/stderr 管道（防缓冲区死锁）
	var lastLogUnix atomic.Int64
	lastLogUnix.Store(time.Now().Unix())

	var pipeWg sync.WaitGroup
	pipeWg.Add(2)

	consumePipe := func(reader io.Reader, stream string) {
		defer pipeWg.Done()
		buf := bufio.NewReader(reader)
		for {
			line, err := buf.ReadString('\n')
			if len(line) > 0 {
				lastLogUnix.Store(time.Now().Unix())
				r.broadcaster.Broadcast(LogEntry{
					Timestamp: time.Now().Format("15:04:05.000"),
					TaskID:    task.ID,
					Stream:    stream,
					Message:   strings.TrimRight(line, "\r\n"),
				})
			}
			if err != nil {
				break
			}
		}
	}

	go consumePipe(stdoutPipe, "stdout")
	go consumePipe(stderrPipe, "stderr")

	// 5. 守护监控器（超时检测、静默卡死检测、外部中断）
	doneChan := make(chan error, 1)
	go func() {
		doneChan <- cmd.Wait()
	}()

	var maxTimeoutChan <-chan time.Time
	if task.TimeoutSeconds > 0 {
		timer := time.NewTimer(time.Duration(task.TimeoutSeconds) * time.Second)
		defer timer.Stop()
		maxTimeoutChan = timer.C
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var terminationReason string
	var stopping bool
	var killFallbackChan <-chan time.Time
	var killForcedGiveUpChan <-chan time.Time
	ctxDone := ctx.Done()

waitLoop:
	for {
		select {
		case err := <-doneChan:
			// 进程正常或异常退出了
			pipeWg.Wait() // 等待管道中的残余日志全部写完
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					res.ExitCode = exitErr.ExitCode()
				} else {
					res.ExitCode = -1
				}
				if terminationReason != "" {
					res.ErrorMsg = terminationReason
				} else {
					res.ErrorMsg = fmt.Sprintf("进程异常退出: %v", err)
				}
			} else {
				res.Success = true
				res.ExitCode = 0
			}
			break waitLoop

		case <-ctxDone:
			if !stopping {
				stopping = true
				terminationReason = "用户手动触发急停 (Emergency Stop)"
				r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("[中断] %s，正在强杀子进程树...", terminationReason))
				_ = job.Terminate(1)
				ctxDone = nil // 将已触发的 channel 置为 nil，彻底杜绝 closed channel 在 select 中持续就绪导致的空转刷屏
				killFallbackChan = time.After(3 * time.Second)
			}

		case <-maxTimeoutChan:
			if !stopping {
				stopping = true
				terminationReason = fmt.Sprintf("任务执行已达设定的最大时间上限 (%d 秒)，触发超时强杀", task.TimeoutSeconds)
				r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("[超时] %s", terminationReason))
				_ = job.Terminate(1)
				maxTimeoutChan = nil
				killFallbackChan = time.After(3 * time.Second)
			}

		case <-ticker.C:
			// 静默卡死无日志检测 (No-log Timeout)
			if !stopping && task.NoLogTimeoutSeconds > 0 {
				elapsedSinceLog := time.Now().Unix() - lastLogUnix.Load()
				if elapsedSinceLog >= int64(task.NoLogTimeoutSeconds) {
					stopping = true
					terminationReason = fmt.Sprintf("检测到持续 %d 秒无任何日志输出（超过允许的 %d 秒），判定为静默假死", elapsedSinceLog, task.NoLogTimeoutSeconds)
					r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("[假死] %s，强制终结...", terminationReason))
					_ = job.Terminate(1)
					killFallbackChan = time.After(3 * time.Second)
				}
			}

		case <-killFallbackChan:
			killFallbackChan = nil
			r.broadcaster.EmitSystemLog(task.ID, "[警告] 进程树未能通过 JobObject 及时退出，执行 cmd.Process.Kill() 强制兜底...")
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			killForcedGiveUpChan = time.After(2 * time.Second)

		case <-killForcedGiveUpChan:
			killForcedGiveUpChan = nil
			r.broadcaster.EmitSystemLog(task.ID, "[错误] 进程终结超时（可能因子孙进程残留继承管道句柄），已强制放弃等待并恢复调度")
			res.Success = false
			res.ExitCode = -1
			res.ErrorMsg = terminationReason + " (强制终结超时)"
			break waitLoop
		}
	}

	// 6. 任务结束后的外部残留进程清理 (如游戏本体)
	if len(task.GameProcessNames) > 0 {
		cleaned := KillProcessesByName(task.GameProcessNames)
		if len(cleaned) > 0 {
			res.CleanedUp = cleaned
			r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("清理残留关联进程: %v", cleaned))
		}
	}

	res.Duration = time.Since(startTime)
	if res.Success {
		r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("<<< 任务 [%s] 执行成功完成，耗时: %s", task.Name, res.Duration.Round(time.Millisecond)))
	} else {
		r.broadcaster.EmitSystemLog(task.ID, fmt.Sprintf("<<< 任务 [%s] 执行终止，耗时: %s，原因: %s", task.Name, res.Duration.Round(time.Millisecond), res.ErrorMsg))
	}

	return res
}

// KillProcessesByName 尝试按进程名强制终结残留的外部进程（如 DOAXVV.exe）
func KillProcessesByName(processNames []string) []string {
	var killed []string
	for _, name := range processNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// /F 强制终止，/T 终止该进程及其派生子进程，/IM 指定映像名称
		cmd := exec.Command("taskkill", "/F", "/T", "/IM", name)
		if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "SUCCESS") || strings.Contains(string(out), "成功") {
			killed = append(killed, fmt.Sprintf("%s (killed)", name))
		}
	}
	return killed
}

