package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"autonexus/internal/config"
	"autonexus/internal/engine"
	"autonexus/internal/executor"
)

func setupTestServer(t *testing.T) (*Server, *config.Manager, *engine.Engine) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "config.json")

	cfgMgr, err := config.NewManager(cfgPath)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	broadcaster := executor.NewBroadcaster(50)
	runner := executor.NewRunner(broadcaster)
	eng := engine.NewEngine(cfgMgr, runner, broadcaster)
	srv := NewServer(cfgMgr, eng, broadcaster)

	return srv, cfgMgr, eng
}

func TestServerGetStatusAndWebUI(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// 测试 GET /api/v1/status
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	var status engine.StatusSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("Failed to parse status response: %v", err)
	}
	if status.State != engine.StateIdle {
		t.Fatalf("Expected state IDLE, got %s", status.State)
	}

	// 测试 GET / 访问嵌入的 Web 控制台
	reqUI := httptest.NewRequest("GET", "/", nil)
	recUI := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recUI, reqUI)

	if recUI.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for index.html, got %d", recUI.Code)
	}
	if !bytes.Contains(recUI.Body.Bytes(), []byte("AutoNexus")) {
		t.Fatalf("Expected body to contain AutoNexus")
	}
}

func TestServerTaskCRUDAndOperations(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	// 1. 创建任务 A (POST /api/v1/tasks)
	taskA := config.TaskConfig{
		ID:         "task_a",
		Name:       "测试黑盒任务 A",
		Executable: "cmd.exe",
		Args:       []string{"/c", "echo A"},
		Enabled:    true,
	}
	dataA, _ := json.Marshal(taskA)
	reqA := httptest.NewRequest("POST", "/api/v1/tasks", bytes.NewReader(dataA))
	recA := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recA, reqA)
	if recA.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created, got %d: %s", recA.Code, recA.Body.String())
	}

	// 2. 创建任务 B
	taskB := config.TaskConfig{
		ID:         "task_b",
		Name:       "测试黑盒任务 B",
		Executable: "cmd.exe",
		Args:       []string{"/c", "echo B"},
		Enabled:    true,
	}
	dataB, _ := json.Marshal(taskB)
	reqB := httptest.NewRequest("POST", "/api/v1/tasks", bytes.NewReader(dataB))
	recB := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recB, reqB)
	if recB.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created, got %d", recB.Code)
	}

	// 3. 更新任务 A (PUT /api/v1/tasks/task_a)
	taskA.Name = "更新后的黑盒任务 A"
	dataAUpdate, _ := json.Marshal(taskA)
	reqUpdate := httptest.NewRequest("PUT", "/api/v1/tasks/task_a", bytes.NewReader(dataAUpdate))
	recUpdate := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recUpdate, reqUpdate)
	if recUpdate.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on update, got %d", recUpdate.Code)
	}

	// 4. 切换开关 Toggle (POST /api/v1/tasks/task_a/toggle)
	reqToggle := httptest.NewRequest("POST", "/api/v1/tasks/task_a/toggle", nil)
	recToggle := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recToggle, reqToggle)
	if recToggle.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on toggle, got %d", recToggle.Code)
	}

	for _, tsk := range cfgMgr.Get().Tasks {
		if tsk.ID == "task_a" && tsk.Enabled != false {
			t.Fatalf("Expected task_a to be disabled after toggle")
		}
	}

	// 5. 调整顺序 Reorder (POST /api/v1/tasks/reorder)
	reorderData, _ := json.Marshal(map[string]any{"task_ids": []string{"task_b", "task_a"}})
	reqReorder := httptest.NewRequest("POST", "/api/v1/tasks/reorder", bytes.NewReader(reorderData))
	recReorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recReorder, reqReorder)
	if recReorder.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on reorder, got %d", recReorder.Code)
	}
	tasksNow := cfgMgr.Get().Tasks
	if len(tasksNow) != 2 || tasksNow[0].ID != "task_b" {
		t.Fatalf("Expected task_b to be first after reorder")
	}

	// 6. 删除任务 (DELETE /api/v1/tasks/task_b)
	reqDel := httptest.NewRequest("DELETE", "/api/v1/tasks/task_b", nil)
	recDel := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDel, reqDel)
	if recDel.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on delete, got %d", recDel.Code)
	}

	tasksAfterDel := cfgMgr.Get().Tasks
	if len(tasksAfterDel) != 1 || tasksAfterDel[0].ID != "task_a" {
		t.Fatalf("Expected only task_a remaining after deletion")
	}
}

func TestServerBrowseFS(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// 1. 测试列出根盘符
	reqRoot := httptest.NewRequest("GET", "/api/v1/fs/browse", nil)
	recRoot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recRoot, reqRoot)

	if recRoot.Code != http.StatusOK {
		t.Fatalf("Expected 200 for root browse, got %d", recRoot.Code)
	}

	var rootResp BrowseFSResponse
	if err := json.Unmarshal(recRoot.Body.Bytes(), &rootResp); err != nil {
		t.Fatalf("Failed to parse browse root response: %v", err)
	}
	if len(rootResp.Drives) == 0 {
		t.Fatalf("Expected at least one drive in drives list")
	}

	// 2. 测试浏览特定目录
	tempDir := t.TempDir()
	testSubDir := filepath.Join(tempDir, "SubDir")
	_ = os.Mkdir(testSubDir, 0755)
	testExe := filepath.Join(tempDir, "agent.exe")
	_ = os.WriteFile(testExe, []byte("dummy"), 0755)

	reqDir := httptest.NewRequest("GET", "/api/v1/fs/browse?path="+tempDir, nil)
	recDir := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDir, reqDir)

	if recDir.Code != http.StatusOK {
		t.Fatalf("Expected 200 for dir browse, got %d: %s", recDir.Code, recDir.Body.String())
	}

	var dirResp BrowseFSResponse
	if err := json.Unmarshal(recDir.Body.Bytes(), &dirResp); err != nil {
		t.Fatalf("Failed to parse dir browse response: %v", err)
	}
	if len(dirResp.Folders) == 0 || dirResp.Folders[0].Name != "SubDir" {
		t.Fatalf("Expected to find SubDir folder, got: %+v", dirResp.Folders)
	}
	if len(dirResp.Files) == 0 || dirResp.Files[0].Name != "agent.exe" {
		t.Fatalf("Expected to find agent.exe file, got: %+v", dirResp.Files)
	}
}

func TestServerScheduleAPI(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	// 测试更新定时调度配置 (POST /api/v1/schedule)
	schedBody := map[string]any{
		"schedule_enabled": true,
		"schedule_time":    "04:30",
	}
	bodyBytes, _ := json.Marshal(schedBody)
	req := httptest.NewRequest("POST", "/api/v1/schedule", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on schedule update, got %d", rec.Code)
	}

	cfg := cfgMgr.Get()
	if !cfg.ScheduleEnabled || cfg.ScheduleTime != "04:30" {
		t.Fatalf("Schedule config not updated properly: %+v", cfg)
	}
}

func TestServerTaskLogsAPI(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	// 创建一个测试任务
	task := &config.TaskConfig{
		ID:         "task_log_test",
		Name:       "日志测试任务",
		Executable: "cmd.exe",
		Enabled:    true,
	}
	if err := cfgMgr.AddTask(task); err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	// 模拟写入该任务的日志
	srv.broadcaster.StartTaskSession(task.ID)
	srv.broadcaster.Broadcast(executor.LogEntry{
		Timestamp: "10:00:00.000",
		TaskID:    task.ID,
		Stream:    "stdout",
		Message:   "任务启动并输出日志行 1",
	})
	srv.broadcaster.Broadcast(executor.LogEntry{
		Timestamp: "10:00:01.000",
		TaskID:    task.ID,
		Stream:    "stderr",
		Message:   "警告信息",
	})
	srv.broadcaster.EndTaskSession(task.ID)

	// 发送 GET /api/v1/tasks/task_log_test/logs
	req := httptest.NewRequest("GET", "/api/v1/tasks/task_log_test/logs", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		TaskID   string              `json:"task_id"`
		TaskName string              `json:"task_name"`
		Total    int                 `json:"total"`
		Logs     []executor.LogEntry `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.TaskID != "task_log_test" || resp.TaskName != "日志测试任务" {
		t.Fatalf("Unexpected task info: %+v", resp)
	}
	if resp.Total != 2 || len(resp.Logs) != 2 {
		t.Fatalf("Expected 2 logs, got %d", resp.Total)
	}
	if resp.Logs[0].Message != "任务启动并输出日志行 1" || resp.Logs[1].Stream != "stderr" {
		t.Fatalf("Logs content mismatch: %+v", resp.Logs)
	}
}


