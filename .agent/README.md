# 🤖 AutoNexus - AI 协作者上下文与知识基座 (AI Context & Project Knowledge Base)

> **⚠️ 致后续所有接手本项目的 AI Agent（强制执行准则）**：  
> 1. **开局必读**：每次新开对话介入本项目时，首先通读本目录（`.agent/`）下的文档，即可全面掌握项目架构、模块分层、关键细节与核心避坑法则，**严禁在无必要的情况下盲目全盘扫描代码库**。  
> 2. **闭环更新（极其重要）**：如果在你的对话轮次中对项目的**架构设计、数据结构、配置项、API 接口、核心算法或关键避坑机制**进行了任何增删改动，**你必须在外壳代码修改完毕后，主动、即时地同步更新本文件**，确保下一个 AI 助手看到的上下文永远是最新、最准确的真实状态！

---

## 一、 项目定位与业务愿景

- **产品定位**：一个**极轻量、超低资源占用（常驻 15MB~30MB 内存）、静默常驻在 Windows 后台**的自动化任务批量调度总控服务。
- **调度目标**：深度适配并串行调度基于 **Go + MaaFramework**（如 `MaDOAXVV`）以及第三方 Python/CLI 自动化工具。
- **交付形态**：**静态单可执行文件交付（约 10MB）**，零外部运行环境依赖（无需 Node.js、Nginx、Python 等环境），前端现代深色响应式控制台通过 Go 原生 `//go:embed` 直接打包进单二进制中。
- **操作方式**：提供局域网 Web 控制台，手机、平板或局域网 PC 浏览器打开即可监看实时终端日志、拖拽调整队列、一键启动/急停、新增黑盒任务、直接浏览宿主机磁盘选取可执行程序。

---

## 二、 核心技术栈与架构分层

### 1. 技术选型
- **后端**：Go 1.22+（标准库优先、高并发管道流、Win32 深度系统调用）
- **通信**：Go 1.22+ 原生 `http.ServeMux` RESTful API + Gorilla WebSocket（实时日志推流）
- **前端**：原生 HTML5 + CSS3 + 原生 JavaScript（单文件免构建，体积极小，移动端自适应，零 npm 依赖）
- **进程管理**：Windows 内核级 `Job Object`（作业对象）绑定

### 2. 目录结构与模块分工

```text
AutoNexus/
├── cmd/autonexus/              # 主程序入口与运行环境引导
│   ├── main.go                 # 服务生命周期、信号监听（优雅停机）、HTTP 服务启动
│   ├── admin_windows.go        # Windows UAC 自动提权检测与 ComposeCommandLine 重启
│   └── admin_other.go          # 非 Windows 平台占位
├── internal/
│   ├── procjob/                # 【内核层】Win32 Job Object 封装
│   │   ├── job.go              # Job 接口定义与 StartInJob 启动注入
│   │   ├── job_windows.go      # Windows 原生 JobObject API 绑定（KILL_ON_JOB_CLOSE）
│   │   └── job_other.go        # 非 Windows 平台进程组杀死降级实现
│   ├── executor/               # 【执行层】单任务执行器与日志流分发
│   │   ├── executor.go         # 管道异步消费、Python环境注入、卡死超时检测、双重强杀兜底、taskkill 残留清理
│   │   └── log.go              # Broadcaster 广播中心（原地 copy 环形历史缓冲、非阻塞 WebSocket 分发）
│   ├── engine/                 # 【调度层】串行状态机调度引擎
│   │   └── engine.go           # IDLE/PREPARE/RUNNING/COOLING/STOPPING 状态转移、队列轮转、智能冷却预检
│   ├── server/                 # 【服务层】HTTP API 与 WebSocket 服务
│   │   ├── server.go           # RESTful 路由、任务 CRUD、任务重排、宿主机文件浏览器 (/api/v1/fs/browse)
│   │   ├── ws.go               # WebSocket 连接保持、心跳 Ping/Pong、写超时防泄漏守护
│   │   └── web/index.html      # 嵌入式响应式 Web 控制台（//go:embed 打包）
│   └── config/                 # 【配置层】线程安全配置管理器
│       └── config.go           # RWMutex 读写锁保护、任务模型、自动生成与落盘保存
├── config.json                 # 本地运行时配置（已由 .gitignore 忽略，防止个人绝对路径入库）
├── go.mod / go.sum             # Go 依赖清单
└── README.md                   # 面向用户的项目说明文档
```

---

## 三、 核心业务模型与数据契约

### 1. 任务模型 (`config.TaskConfig`)

```go
type TaskConfig struct {
    ID                  string   `json:"id"`                    // 任务唯一标识符 (例如 task_1790013410952)
    Name                string   `json:"name"`                  // 任务友好展示名称
    Enabled             bool     `json:"enabled"`               // 是否参与队列轮转调度
    Executable          string   `json:"executable"`            // 执行程序路径 (如 D:\Path\agent.exe)
    Args                []string `json:"args"`                  // 启动命令行参数切片
    WorkingDir          string   `json:"working_dir"`           // 工作目录 (留空默认使用可执行文件同级目录)
    GameProcessNames    []string `json:"game_process_names"`    // 关联进程名 (如 DOAXVV.exe，任务结束后由 taskkill 强制清理)
    TimeoutSeconds      int      `json:"timeout_seconds"`       // 最大运行时长限制 (秒，0 为不限)
    NoLogTimeoutSeconds int      `json:"no_log_timeout_seconds"`// 静默无日志卡死检测阈值 (秒，0 为禁用)
    CooldownSeconds     int      `json:"cooldown_seconds"`      // 任务结束后的冷却释放时间 (秒，允许显式配置 0)
}
```

### 2. 调度状态机状态 (`engine.State`)

- `IDLE`: 空闲待命。
- `PREPARE`: 准备就绪，即将进入执行。
- `RUNNING`: 某个子任务正在运行中。
- `COOLING`: 任务间系统冷却缓冲（等待 Windows 句柄和 GPU 显存彻底释放）。
- `STOPPING`: 收到急停（Emergency Stop）指令，正在终止子任务树并退出队列。

### 3. REST API 契约一览

| 方法 | 路径 | 作用 | 请求参数 / 说明 |
| :--- | :--- | :--- | :--- |
| `GET` | `/api/v1/status` | 获取引擎状态快照 | 返回包含当前状态、运行中任务、耗时、冷却倒计时、任务列表 |
| `POST`| `/api/v1/queue/start` | 启动整套任务队列 | 无参，将未禁用的任务按顺序串行执行 |
| `POST`| `/api/v1/queue/stop` | 紧急停止（急停） | 强杀当前子任务进程树，中止后续队列调度 |
| `POST`| `/api/v1/tasks` | 新增任务 | Body: `TaskConfig` JSON |
| `PUT` | `/api/v1/tasks/{id}` | 更新任务配置 | Body: `TaskConfig` JSON |
| `DELETE`| `/api/v1/tasks/{id}`| 删除指定任务 | 路径参数指定任务 ID |
| `POST`| `/api/v1/tasks/{id}/run` | 独立调试单项任务 | 不影响队列，执行单任务后直接返回待命状态 |
| `POST`| `/api/v1/tasks/{id}/toggle`| 启用/禁用任务 | Body (可选): `{"enabled": bool}`，留空则自动取反 |
| `POST`| `/api/v1/tasks/reorder` | 重排任务队列顺序 | Body: `{"task_ids": ["id2", "id1", ...]}` |
| `GET` | `/api/v1/fs/browse?path=` | 宿主机磁盘浏览器 | 传空返回盘符列表 (`C:`, `D:`)；传目录返回子文件夹与可执行文件 |
| `GET` | `/ws/logs` | 实时终端 WebSocket | 建立连接时推送 `type: "history"` 历史日志批处理，后续流式推送单条日志 |

---

## 四、 核心避坑设计法则 (经验沉淀，严禁破坏)

以下设计均为前期踩坑后提炼的关键保护措施，**任何后续修改严禁弱化或倒退**：

1. **Windows Job Object 进程树内核强绑定**：
   - 必须通过 `windows.SetInformationJobObject` 配置 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`。
   - 父进程意外崩溃、退出或触发急停时，Windows 内核自动强杀游戏和 Agent 子孙进程，彻底杜绝孤儿/幽灵进程占用显存和端口。

2. **双重强杀超时兜底（防挂死死锁）**：
   - 在 `executor.Run` 的监控等待循环中，若触发急停、超时或假死，首先调用 `job.Terminate(1)`；
   - 若 3 秒后进程仍未退出，自动触发 `cmd.Process.Kill()` 兜底；
   - 若再过 2 秒仍因管道未释放挂死，强制放弃等待并跳出循环，确保调度引擎永不假死挂起。

3. **Python 管道无缓冲与 UTF-8 注入**：
   - 在 `executor.go` 中，启动子进程前必须显式注入：
     - `PYTHONUNBUFFERED=1`：强制 Python 禁用管道块缓冲，防止日志积压导致假死检测误杀；
     - `PYTHONIOENCODING=utf-8`：防止 Windows 控制台输出中文时遭遇 `UnicodeEncodeError`。

4. **路径引号清洗与 `Zone.Identifier` 移除时序**：
   - Windows 下用户通过“复制为路径”粘贴的文本往往带有外层双引号。
   - 必须在启动命令前通过 `strings.Trim(..., `"'`)` 提前洗净 `exePath` 和 `workingDir`；
   - 必须使用**清洗后**的 `exePath` 执行 `os.Remove(exePath + ":Zone.Identifier")`，否则非法字符会导致解除下载锁定失败而弹窗。

5. **命令行参数与反斜杠 Windows 规范兼容**：
   - 前端 `formatCommandLine` 遇到空字符串必须输出 `""`；遇到路径末尾反斜杠必须双写转义（`\\` -> `\\\\`），以符合 Windows `CommandLineToArgvW` 协议；
   - 前端 `parseCommandLine` 引入 `hasToken` 状态机以区分空字符串 Token，并启发式识别尾部反斜杠闭合。
   - 提权重新拉起使用官方标准库 `windows.ComposeCommandLine(args)`。

6. **进程残留清理名称容错**：
   - `KillProcessesByName` 中必须通过 `filepath.Base` 截取文件名，并自动为缺少扩展名的进程追加 `.exe` 后缀，确保 `taskkill /IM` 100% 命中目标。

7. **Broadcaster 环形缓冲内存优化**：
   - 日志历史记录填满 `maxHistory` 时，必须使用 `copy(b.history, b.history[1:])` 原地移位覆盖，**严禁**使用 `b.history = b.history[1:]`，防止切片容量衰退导致堆内存无限重新分配。

8. **WebSocket 写入超时守护**：
   - 在向 WebSocket 写入历史日志或推流单帧前，必须调用 `conn.SetWriteDeadline(time.Now().Add(5 * time.Second))`，防止手机锁屏或弱网时导致 Goroutine 和订阅者通道永久泄漏。

---

## 五、 常用开发与测试指令

在执行修改后，务必在项目根目录下通过终端进行闭环回归验证：

```powershell
# 1. 运行所有模块的单元测试（必须全部 PASS）
go test -count=1 -v ./...

# 2. 编译主程序二进制（验证无语法/编译错误）
go build -o autonexus.exe ./cmd/autonexus

# 3. 运行本地临时测试（默认端口 18080，可通过 -port 修改）
.\autonexus.exe -no-elevate -port 19090
```

---

> **再次提醒**：当你对代码做出结构性调整后，请务必返回本文件更新对应条款！
