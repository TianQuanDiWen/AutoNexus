package server

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"autonexus/internal/config"
	"autonexus/internal/engine"
	"autonexus/internal/executor"
	"autonexus/internal/maa"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/redesign.css
var redesignCSS []byte

//go:embed web/workspace.js
var workspaceJS []byte

// Server HTTP 与 WebSocket Web 服务
type Server struct {
	configMgr   *config.Manager
	engine      *engine.Engine
	broadcaster *executor.Broadcaster
	mux         *http.ServeMux
	authMu      sync.Mutex
	authFails   int // 连续认证失败次数（访问密码可能为弱口令，用于拖慢爆破）
}

// NewServer 创建 Web 服务实例并挂载路由
func NewServer(cfgMgr *config.Manager, eng *engine.Engine, b *executor.Broadcaster) *Server {
	s := &Server{
		configMgr:   cfgMgr,
		engine:      eng,
		broadcaster: b,
		mux:         http.NewServeMux(),
	}

	s.setupRoutes()
	return s
}

// Handler 返回 HTTP 处理器（外层包裹局域网访问令牌校验）
func (s *Server) Handler() http.Handler {
	return s.withAuthToken(s.mux)
}

// isPublicPath 静态页面壳不含任何敏感数据，放行以支持手机首次通过配对链接打开；API 与 WebSocket 一律校验令牌
func isPublicPath(p string) bool {
	return p == "/" || strings.HasPrefix(p, "/assets/")
}

// withAuthToken 访问控制中间件，判定顺序：
//  0. 状态变更请求先过同源校验 (在回环豁免之前) —— 本机浏览器里的恶意网页向 127.0.0.1
//     发 no-cors POST 恰好满足「回环来源+回环 Host」，若不过此关可免密改配置/创建任务；
//  1. 本机请求 (127.0.0.1/::1) 永远放行 —— 桌面窗口与宿主机浏览器是物理可控的可信位置；
//  2. 局域网开关关闭时拒绝一切外部访问；
//  3. 未设置访问密码时开放 (设置界面会红字提醒风险)；
//  4. 已设密码时校验 Bearer 头 / token 查询参数 (浏览器 WebSocket 无法自定义请求头)。
func (s *Server) withAuthToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			s.writeError(w, http.StatusForbidden, "跨站请求被拒绝")
			return
		}
		if isLoopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		cfg := s.configMgr.Get()
		if !cfg.LANEnabled {
			s.writeError(w, http.StatusForbidden, "局域网访问已关闭，请在电脑端控制台的设置中开启")
			return
		}
		if cfg.AuthToken == "" || isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// 访问密码可能为用户自设的弱口令：使用常量时间比较消除时序侧信道
		if subtle.ConstantTimeCompare([]byte(requestToken(r)), []byte(cfg.AuthToken)) == 1 {
			s.recordAuthResult(true)
			next.ServeHTTP(w, r)
			return
		}
		// 访问密码可能为用户自设的弱口令：连续失败达到阈值后每次失败延迟响应，拖慢局域网内的爆破尝试
		if s.recordAuthResult(false) >= 5 {
			time.Sleep(2 * time.Second)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="autonexus"`)
		s.writeError(w, http.StatusUnauthorized, "访问密码错误或缺失，请在打开的页面中输入访问密码")
	})
}

// sameOriginRequest 校验状态变更请求的来源：浏览器对 POST/PUT/DELETE 恒携带 Origin，
// 同源请求的 Origin 主机部分必然等于 r.Host (本机浏览器与回环、局域网手机与自身地址皆然)；
// 非浏览器客户端 (curl/脚本) 不携带 Origin，凭据校验由后续鉴权逻辑兜底
func sameOriginRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// isLoopbackRequest 判断请求是否为本机可信请求：来源地址与 Host 头必须同为回环。
// 仅看 RemoteAddr 会留下 DNS rebinding 面——恶意网页把域名解析到 127.0.0.1 后，
// 请求虽来自回环但 Host 仍是攻击者域名，不能享受免密豁免。
func isLoopbackRequest(r *http.Request) bool {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	ip := net.ParseIP(remoteHost)
	if ip == nil || !ip.IsLoopback() {
		return false
	}

	reqHost := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		reqHost = h
	}
	reqHost = strings.ToLower(strings.Trim(reqHost, "[]"))
	if reqHost == "localhost" {
		return true
	}
	hostIP := net.ParseIP(reqHost)
	return hostIP != nil && hostIP.IsLoopback()
}

// recordAuthResult 记录认证结果：成功清零连续失败计数并返回 0，失败自增并返回当前连续失败次数
func (s *Server) recordAuthResult(success bool) int {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if success {
		s.authFails = 0
		return 0
	}
	s.authFails++
	return s.authFails
}

// requestToken 提取访问令牌：优先 Authorization: Bearer 请求头；浏览器 WebSocket 无法自定义请求头，故同时接受 token 查询参数
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

func (s *Server) setupRoutes() {
	// REST API
	s.mux.HandleFunc("GET /api/v1/status", s.handleGetStatus)
	s.mux.HandleFunc("POST /api/v1/queue/start", s.handleStartQueue)
	s.mux.HandleFunc("POST /api/v1/queue/stop", s.handleStopQueue)
	s.mux.HandleFunc("POST /api/v1/queue/skip-wait", s.handleSkipWait)
	s.mux.HandleFunc("POST /api/v1/schedule", s.handleUpdateSchedule)
	s.mux.HandleFunc("POST /api/v1/settings", s.handleUpdateSettings)
	s.mux.HandleFunc("POST /api/v1/auth/token", s.handleUpdateAuthToken)
	s.mux.HandleFunc("POST /api/v1/tasks", s.handleCreateTask)
	s.mux.HandleFunc("PUT /api/v1/tasks/{id}", s.handleUpdateTask)
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.handleDeleteTask)
	s.mux.HandleFunc("GET /api/v1/tasks/{id}/logs", s.handleGetTaskLogs)
	s.mux.HandleFunc("GET /api/v1/tasks/{id}/maa", s.handleGetTaskMAAConfig)
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/maa", s.handleUpdateTaskMAAConfig)
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/run", s.handleRunTask)
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/toggle", s.handleToggleTask)
	s.mux.HandleFunc("POST /api/v1/tasks/reorder", s.handleReorderTasks)

	// 本地/宿主机文件浏览器 API
	s.mux.HandleFunc("GET /api/v1/fs/browse", s.handleBrowseFS)

	// WebSocket 日志流
	s.mux.HandleFunc("GET /ws/logs", s.handleWSLogs)

	// 嵌入式 Web 控制台
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	s.mux.HandleFunc("GET /assets/redesign.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(redesignCSS)
	})
	s.mux.HandleFunc("GET /assets/workspace.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(workspaceJS)
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	status := s.engine.GetStatus()
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleStartQueue(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if r.Header.Get("Content-Type") == "application/json" {
		var req struct {
			Force bool `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Force {
			force = true
		}
	}

	if err := s.engine.StartQueue(force); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "队列调度已触发启动"})
}

func (s *Server) handleStopQueue(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.StopQueue(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "急停指令已发送"})
}

func (s *Server) handleSkipWait(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.SkipScheduleWait(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "已跳过刷新等待，立即开跑"})
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ScheduleEnabled bool   `json:"schedule_enabled"`
		ScheduleTime    string `json:"schedule_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "解析请求参数失败")
		return
	}

	if err := s.configMgr.UpdateSchedule(req.ScheduleEnabled, req.ScheduleTime); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.engine.NotifyScheduleChanged()
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "定时调度配置已更新"})
}

// handleUpdateSettings 更新局域网访问等通用设置
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LANEnabled *bool `json:"lan_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "解析请求参数失败")
		return
	}
	if req.LANEnabled != nil {
		if err := s.configMgr.UpdateLANEnabled(*req.LANEnabled); err != nil {
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "设置已保存"})
}

// handleUpdateAuthToken 设置自定义访问密码；传空串表示清除密码回到免密访问
func (s *Server) handleUpdateAuthToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "解析请求参数失败")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		if err := s.configMgr.UpdateAuthToken(""); err != nil {
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]string{"message": "访问密码已清除，所有设备可直接访问"})
		return
	}
	// 按字符数而非字节数校验，与界面文案「6~128 位」语义一致 (UTF-8 下一个汉字占 3 字节)
	if n := len([]rune(req.Token)); n < 6 || n > 128 {
		s.writeError(w, http.StatusBadRequest, "访问密码长度需在 6~128 个字符之间")
		return
	}
	if err := s.configMgr.UpdateAuthToken(req.Token); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "访问密码已更新，其他设备需重新验证"})
}

func (s *Server) handleRunTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}

	if err := s.engine.RunSingleTask(id); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("单项任务 [%s] 已启动", id)})
}

func (s *Server) handleToggleTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}

	// 查找当前状态并翻转
	cfg := s.configMgr.Get()
	var currentVal bool
	found := false
	for _, t := range cfg.Tasks {
		if t.ID == id {
			currentVal = t.Enabled
			found = true
			break
		}
	}
	if !found {
		s.writeError(w, http.StatusNotFound, "未找到该任务")
		return
	}

	newVal := !currentVal
	// 支持请求体覆盖
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.Enabled != nil {
		newVal = *body.Enabled
	}

	if err := s.configMgr.SetTaskEnabled(id, newVal); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"enabled": newVal,
	})
}

func (s *Server) handleReorderTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.TaskIDs) == 0 {
		s.writeError(w, http.StatusBadRequest, "无效的任务顺序请求体")
		return
	}

	cfg := s.configMgr.Get()
	taskMap := make(map[string]*config.TaskConfig)
	for _, t := range cfg.Tasks {
		taskMap[t.ID] = t
	}

	var newTasks []*config.TaskConfig
	seen := make(map[string]bool)

	for _, id := range body.TaskIDs {
		id = strings.TrimSpace(id)
		if t, ok := taskMap[id]; ok && !seen[id] {
			newTasks = append(newTasks, t)
			seen[id] = true
		}
	}

	// 把未包含进来的原有任务追加在后面
	for _, t := range cfg.Tasks {
		if !seen[t.ID] {
			newTasks = append(newTasks, t)
		}
	}

	if err := s.configMgr.UpdateTasks(newTasks); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"tasks": newTasks,
	})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var task config.TaskConfig
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		s.writeError(w, http.StatusBadRequest, "解析任务 JSON 失败: "+err.Error())
		return
	}
	if err := s.configMgr.AddTask(&task); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}
	var task config.TaskConfig
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		s.writeError(w, http.StatusBadRequest, "解析任务 JSON 失败: "+err.Error())
		return
	}
	task.ID = id
	if err := s.configMgr.UpdateTask(&task); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}
	if err := s.configMgr.DeleteTask(id); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"message": "任务已删除"})
}

func (s *Server) handleGetTaskLogs(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}

	cfg := s.configMgr.Get()
	var taskName string
	for _, t := range cfg.Tasks {
		if t.ID == taskID {
			taskName = t.Name
			break
		}
	}
	if taskName == "" {
		taskName = taskID
	}

	logs := s.broadcaster.GetTaskHistory(taskID)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"task_id":   taskID,
		"task_name": taskName,
		"total":     len(logs),
		"logs":      logs,
	})
}

func (s *Server) handleGetTaskMAAConfig(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}

	cfg := s.configMgr.Get()
	var targetTask *config.TaskConfig
	for _, t := range cfg.Tasks {
		if t.ID == taskID {
			targetTask = t
			break
		}
	}
	if targetTask == nil {
		s.writeError(w, http.StatusNotFound, "未找到任务")
		return
	}

	proj, err := maa.DetectProject(targetTask.WorkingDir, targetTask.Executable)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"is_maa": false,
			"error":  err.Error(),
		})
		return
	}

	selectedConfigID := r.URL.Query().Get("config_id")
	if selectedConfigID == "" && targetTask.MAAConfigID != "" {
		selectedConfigID = targetTask.MAAConfigID
	}

	view, err := maa.GetConfigView(proj, selectedConfigID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "解析 MAA 配置失败: "+err.Error())
		return
	}

	// 若任务当前未绑定 MAAConfigName 或已漂移，自动补齐
	if view.ActiveConfigID != "" && targetTask.MAAConfigName == "" {
		for _, p := range view.Profiles {
			if p.ID == view.ActiveConfigID {
				targetTask.MAAConfigID = p.ID
				targetTask.MAAConfigName = p.Name
				_ = s.configMgr.UpdateTask(targetTask)
				break
			}
		}
	}

	s.writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleUpdateTaskMAAConfig(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		s.writeError(w, http.StatusBadRequest, "缺少 task id")
		return
	}

	cfg := s.configMgr.Get()
	var targetTask *config.TaskConfig
	for _, t := range cfg.Tasks {
		if t.ID == taskID {
			targetTask = t
			break
		}
	}
	if targetTask == nil {
		s.writeError(w, http.StatusNotFound, "未找到任务")
		return
	}

	proj, err := maa.DetectProject(targetTask.WorkingDir, targetTask.Executable)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "该任务未检测到有效的 MAA 项目: "+err.Error())
		return
	}

	var req maa.TaskUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "解析请求 JSON 失败: "+err.Error())
		return
	}

	if err := maa.SaveConfig(proj, &req); err != nil {
		s.writeError(w, http.StatusInternalServerError, "保存 MAA 配置失败: "+err.Error())
		return
	}

	// 记录选定的执行配置预设
	if req.ConfigID != "" {
		targetTask.MAAConfigID = req.ConfigID
		if view, err := maa.GetConfigView(proj, req.ConfigID); err == nil {
			for _, p := range view.Profiles {
				if p.ID == req.ConfigID {
					targetTask.MAAConfigName = p.Name
					break
				}
			}
		}
		_ = s.configMgr.UpdateTask(targetTask)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"message":            "MAA 自动化配置已成功保存并在执行时生效",
		"active_config_id":   targetTask.MAAConfigID,
		"active_config_name": targetTask.MAAConfigName,
	})
}

// FSItem 文件系统项目（目录或文件）
type FSItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BrowseFSResponse 文件浏览器响应结构
type BrowseFSResponse struct {
	Current string   `json:"current"`
	Parent  string   `json:"parent,omitempty"`
	Drives  []string `json:"drives,omitempty"`
	Folders []FSItem `json:"folders"`
	Files   []FSItem `json:"files"`
}

func listSystemDrives() []string {
	var drives []string
	for _, drive := range "CDEFGHIJKLMNOPQRSTUVWXYZ" {
		root := string(drive) + ":\\"
		if _, err := os.Stat(root); err == nil {
			drives = append(drives, string(drive)+":")
		}
	}
	if len(drives) == 0 {
		drives = append(drives, "C:")
	}
	return drives
}

func (s *Server) handleBrowseFS(w http.ResponseWriter, r *http.Request) {
	dirPath := strings.TrimSpace(r.URL.Query().Get("path"))

	resp := BrowseFSResponse{
		Folders: []FSItem{},
		Files:   []FSItem{},
	}

	if dirPath == "" {
		resp.Drives = listSystemDrives()
		s.writeJSON(w, http.StatusOK, resp)
		return
	}

	cleanPath := filepath.Clean(dirPath)
	if len(cleanPath) == 2 && cleanPath[1] == ':' {
		cleanPath += string(filepath.Separator)
	}

	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "无法读取目录: "+err.Error())
		return
	}

	resp.Current = cleanPath
	parent := filepath.Dir(cleanPath)
	if parent != cleanPath {
		resp.Parent = parent
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "$") || strings.HasPrefix(name, ".") {
			continue
		}

		fullPath := filepath.Join(cleanPath, name)
		if entry.IsDir() {
			resp.Folders = append(resp.Folders, FSItem{Name: name, Path: fullPath})
		} else {
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".exe" || ext == ".bat" || ext == ".cmd" || ext == ".ps1" || ext == ".py" || ext == ".sh" {
				resp.Files = append(resp.Files, FSItem{Name: name, Path: fullPath})
			}
		}
	}

	s.writeJSON(w, http.StatusOK, resp)
}
