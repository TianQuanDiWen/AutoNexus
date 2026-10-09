package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"autonexus/internal/config"
	"autonexus/internal/engine"
	"autonexus/internal/executor"
)

//go:embed web/index.html
var indexHTML []byte

// Server HTTP 与 WebSocket Web 服务
type Server struct {
	configMgr   *config.Manager
	engine      *engine.Engine
	broadcaster *executor.Broadcaster
	mux         *http.ServeMux
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

// Handler 返回 HTTP 处理器
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) setupRoutes() {
	// REST API
	s.mux.HandleFunc("GET /api/v1/status", s.handleGetStatus)
	s.mux.HandleFunc("POST /api/v1/queue/start", s.handleStartQueue)
	s.mux.HandleFunc("POST /api/v1/queue/stop", s.handleStopQueue)
	s.mux.HandleFunc("POST /api/v1/queue/skip-wait", s.handleSkipWait)
	s.mux.HandleFunc("POST /api/v1/schedule", s.handleUpdateSchedule)
	s.mux.HandleFunc("POST /api/v1/tasks", s.handleCreateTask)
	s.mux.HandleFunc("PUT /api/v1/tasks/{id}", s.handleUpdateTask)
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.handleDeleteTask)
	s.mux.HandleFunc("GET /api/v1/tasks/{id}/logs", s.handleGetTaskLogs)
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


