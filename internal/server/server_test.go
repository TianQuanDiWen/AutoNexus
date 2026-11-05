package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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
	// 默认配置仅本机可访问；API 测试统一模拟局域网开放环境（未设密码=直接放行）
	if err := cfgMgr.UpdateLANEnabled(true); err != nil {
		t.Fatalf("UpdateLANEnabled failed: %v", err)
	}

	broadcaster := executor.NewBroadcaster(50)
	runner := executor.NewRunner(broadcaster)
	eng := engine.NewEngine(cfgMgr, runner, broadcaster)
	srv := NewServer(cfgMgr, eng, broadcaster)

	return srv, cfgMgr, eng
}

// authedRequest 构造携带访问密码的请求 (仅鉴权场景测试使用：TestServerAccessControl / TestServerAuthTokenUpdate)
func authedRequest(method, target string, body io.Reader, token string) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestServerAccessControl(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	// 1. 默认状态 (局域网开启、未设密码)：外部请求直接放行
	recOpen := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recOpen, httptest.NewRequest("GET", "/api/v1/status", nil))
	if recOpen.Code != http.StatusOK {
		t.Fatalf("Expected 200 with LAN on and no password, got %d", recOpen.Code)
	}

	// 2. 设置访问密码后：外部请求无凭据 -> 401
	password := "secret-pass-1"
	body, _ := json.Marshal(map[string]string{"token": password})
	recSet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recSet, httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(body)))
	if recSet.Code != http.StatusOK {
		t.Fatalf("Expected 200 on password set, got %d: %s", recSet.Code, recSet.Body.String())
	}
	if cfgMgr.Get().AuthToken != password {
		t.Fatal("Expected password to be persisted")
	}

	recNoAuth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recNoAuth, httptest.NewRequest("GET", "/api/v1/status", nil))
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 without credentials after password set, got %d", recNoAuth.Code)
	}

	// 3. 错误密码 -> 401
	recWrong := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recWrong, authedRequest("GET", "/api/v1/status", nil, "wrong-pass"))
	if recWrong.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 with wrong password, got %d", recWrong.Code)
	}

	// 4. 正确密码 (Authorization 头 / 查询参数两种通道) -> 200
	recOK := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recOK, authedRequest("GET", "/api/v1/status", nil, password))
	if recOK.Code != http.StatusOK {
		t.Fatalf("Expected 200 with valid bearer password, got %d", recOK.Code)
	}
	recQuery := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recQuery, httptest.NewRequest("GET", "/api/v1/status?token="+password, nil))
	if recQuery.Code != http.StatusOK {
		t.Fatalf("Expected 200 with valid query password, got %d", recQuery.Code)
	}

	// 5. 本机回环请求始终免密放行 (桌面窗口场景；httptest 默认 Host 为 example.com，需显式指定回环 Host)
	recLocal := httptest.NewRecorder()
	reqLocal := httptest.NewRequest("GET", "/api/v1/status", nil)
	reqLocal.RemoteAddr = "127.0.0.1:54321"
	reqLocal.Host = "127.0.0.1:18080"
	srv.Handler().ServeHTTP(recLocal, reqLocal)
	if recLocal.Code != http.StatusOK {
		t.Fatalf("Expected 200 for loopback request without password, got %d", recLocal.Code)
	}

	// 5b. DNS rebinding 封堵：来源是回环但 Host 是外部域名 (恶意网页把域名解析到 127.0.0.1)，不得享受免密豁免
	recRebind := httptest.NewRecorder()
	reqRebind := httptest.NewRequest("GET", "/api/v1/status", nil)
	reqRebind.RemoteAddr = "127.0.0.1:54321"
	reqRebind.Host = "evil.example.com:18080"
	srv.Handler().ServeHTTP(recRebind, reqRebind)
	if recRebind.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 for loopback request with external Host (rebinding), got %d", recRebind.Code)
	}

	// 5c. Host 为 localhost (手动输入访问的习惯场景) 仍享受回环豁免
	recLocalhost := httptest.NewRecorder()
	reqLocalhost := httptest.NewRequest("GET", "/api/v1/status", nil)
	reqLocalhost.RemoteAddr = "127.0.0.1:54321"
	reqLocalhost.Host = "localhost:18080"
	srv.Handler().ServeHTTP(recLocalhost, reqLocalhost)
	if recLocalhost.Code != http.StatusOK {
		t.Fatalf("Expected 200 for loopback request with localhost Host, got %d", recLocalhost.Code)
	}

	// 6. 清除密码后回到免密访问 (改密码请求本身也需要携带当前有效密码)
	clearBody, _ := json.Marshal(map[string]string{"token": ""})
	recClear := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recClear, authedRequest("POST", "/api/v1/auth/token", bytes.NewReader(clearBody), password))
	if recClear.Code != http.StatusOK {
		t.Fatalf("Expected 200 on password clear, got %d", recClear.Code)
	}
	recAfterClear := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recAfterClear, httptest.NewRequest("GET", "/api/v1/status", nil))
	if recAfterClear.Code != http.StatusOK {
		t.Fatalf("Expected 200 after password cleared, got %d", recAfterClear.Code)
	}
}

func TestServerCSRFBlocked(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// 设置密码 (非浏览器请求无 Origin，放行)
	password := "secret-pass-1"
	body, _ := json.Marshal(map[string]string{"token": password})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 on password set, got %d", rec.Code)
	}

	schedBody, _ := json.Marshal(map[string]any{"schedule_enabled": false})

	// 恶意网页借道本机回环发跨站 POST (no-cors)：回环来源+回环 Host，但 Origin 是外部域名 -> 403
	// (证明 CSRF 校验先于回环豁免，设不设密码都拦)
	recEvil := httptest.NewRecorder()
	reqEvil := httptest.NewRequest("POST", "/api/v1/schedule", bytes.NewReader(schedBody))
	reqEvil.RemoteAddr = "127.0.0.1:54321"
	reqEvil.Host = "127.0.0.1:18080"
	reqEvil.Header.Set("Origin", "http://evil.example.com")
	srv.Handler().ServeHTTP(recEvil, reqEvil)
	if recEvil.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 for cross-site POST via loopback, got %d", recEvil.Code)
	}

	// 同源 POST (本机页面自己的 Origin) 正常放行
	recSame := httptest.NewRecorder()
	reqSame := httptest.NewRequest("POST", "/api/v1/schedule", bytes.NewReader(schedBody))
	reqSame.RemoteAddr = "127.0.0.1:54321"
	reqSame.Host = "127.0.0.1:18080"
	reqSame.Header.Set("Origin", "http://127.0.0.1:18080")
	reqSame.Header.Set("Authorization", "Bearer "+password)
	srv.Handler().ServeHTTP(recSame, reqSame)
	if recSame.Code != http.StatusOK {
		t.Fatalf("Expected 200 for same-origin POST, got %d", recSame.Code)
	}

	// 非浏览器客户端 (curl/脚本) 无 Origin，凭据校验兜底 -> 放行
	recNoOrigin := httptest.NewRecorder()
	reqNoOrigin := httptest.NewRequest("POST", "/api/v1/schedule", bytes.NewReader(schedBody))
	reqNoOrigin.Header.Set("Authorization", "Bearer "+password)
	srv.Handler().ServeHTTP(recNoOrigin, reqNoOrigin)
	if recNoOrigin.Code != http.StatusOK {
		t.Fatalf("Expected 200 for non-browser POST without Origin, got %d", recNoOrigin.Code)
	}
}

func TestWSCheckOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws/logs", nil)

	// 无 Origin (curl/桌面脚本) 放行
	if !wsCheckOrigin(req) {
		t.Fatal("Expected non-browser request (no Origin) to be allowed")
	}

	// 同源放行 (本机窗口与局域网手机皆是)
	req.Host = "127.0.0.1:18080"
	req.Header.Set("Origin", "http://127.0.0.1:18080")
	if !wsCheckOrigin(req) {
		t.Fatal("Expected same-origin WebSocket to be allowed")
	}

	// 跨站拒绝 (恶意网页连 ws://127.0.0.1 读执行日志)
	req.Header.Set("Origin", "http://evil.example.com")
	if wsCheckOrigin(req) {
		t.Fatal("Expected cross-origin WebSocket to be rejected")
	}

	// 非法 Origin (sandbox iframe 的 "null") 拒绝
	req.Header.Set("Origin", "null")
	if wsCheckOrigin(req) {
		t.Fatal("Expected null-origin WebSocket to be rejected")
	}
}

func TestServerThemeSetting(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	// 正常保存主题
	body, _ := json.Marshal(map[string]string{"theme": "light"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/settings", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 on theme update, got %d: %s", rec.Code, rec.Body.String())
	}
	if cfgMgr.Get().Theme != "light" {
		t.Fatalf("Expected theme persisted as light, got %q", cfgMgr.Get().Theme)
	}

	// 非法主题被拒绝且不落盘
	badBody, _ := json.Marshal(map[string]string{"theme": "sepia"})
	recBad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recBad, httptest.NewRequest("POST", "/api/v1/settings", bytes.NewReader(badBody)))
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for invalid theme, got %d", recBad.Code)
	}
	if cfgMgr.Get().Theme != "light" {
		t.Fatalf("Theme should be unchanged after invalid update, got %q", cfgMgr.Get().Theme)
	}
}

func TestServerLANDisabled(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	if err := cfgMgr.UpdateLANEnabled(false); err != nil {
		t.Fatalf("UpdateLANEnabled failed: %v", err)
	}

	// 局域网开关关闭：外部请求 (含静态页面) 一律 403
	recExt := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recExt, httptest.NewRequest("GET", "/", nil))
	if recExt.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 for external request with LAN disabled, got %d", recExt.Code)
	}

	// 本机回环不受开关影响
	recLocal := httptest.NewRecorder()
	reqLocal := httptest.NewRequest("GET", "/api/v1/status", nil)
	reqLocal.RemoteAddr = "[::1]:54321"
	reqLocal.Host = "localhost:18080"
	srv.Handler().ServeHTTP(recLocal, reqLocal)
	if recLocal.Code != http.StatusOK {
		t.Fatalf("Expected 200 for loopback request with LAN disabled, got %d", recLocal.Code)
	}
}

func TestServerAuthTokenUpdate(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)

	// 1. 过短密码被拒绝 (按字符数计：3 个汉字 6 字节但仅 3 字符，不足 6)
	for _, weak := range []string{"123", "一二三"} {
		recShort := httptest.NewRecorder()
		shortBody, _ := json.Marshal(map[string]string{"token": weak})
		srv.Handler().ServeHTTP(recShort, httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(shortBody)))
		if recShort.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 for too-short token %q, got %d", weak, recShort.Code)
		}
	}

	// 2. 设置第一版密码
	firstPass := "first-pass-1"
	body, _ := json.Marshal(map[string]string{"token": firstPass})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/auth/token", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 on token update, got %d: %s", rec.Code, rec.Body.String())
	}
	if cfgMgr.Get().AuthToken != firstPass {
		t.Fatal("Expected auth token to be persisted after update")
	}

	// 3. 更换为新密码 (需携带当前有效密码)：旧密码立即失效
	newPass := "second-pass-2"
	body2, _ := json.Marshal(map[string]string{"token": newPass})
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, authedRequest("POST", "/api/v1/auth/token", bytes.NewReader(body2), firstPass))
	if rec2.Code != http.StatusOK {
		t.Fatalf("Expected 200 on token rotation, got %d", rec2.Code)
	}
	recOld := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recOld, authedRequest("GET", "/api/v1/status", nil, firstPass))
	if recOld.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 with old password after rotation, got %d", recOld.Code)
	}
	recNew := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recNew, authedRequest("GET", "/api/v1/status", nil, newPass))
	if recNew.Code != http.StatusOK {
		t.Fatalf("Expected 200 with new password, got %d", recNew.Code)
	}
}

func TestServerGetStatusAndWebUI(t *testing.T) {
	srv, _, _ := setupTestServer(t)

	// 测试 GET /api/v1/status (局域网开放且未设密码，无需凭据；鉴权场景由 TestServerAccessControl 覆盖)
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
		t.Fatalf("Failed to parse browse dir response: %v", err)
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

func TestServerMAAConfigAPI(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)
	tempDir := t.TempDir()

	// 构造 mock MAA 项目
	interfaceContent := `{
  "interface_version": 2,
  "name": "MockGame",
  "task": [
    {
      "name": "DailyReward",
      "label": "🎁 每日签到",
      "default_check": true
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)
	_ = os.MkdirAll(filepath.Join(tempDir, "config"), 0755)
	mxuContent := `{
  "version": "1.0",
  "instances": [
    {
      "id": "i1",
      "tasks": [
        { "taskName": "DailyReward", "enabled": true }
      ]
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(tempDir, "config", "mxu-MockGame.json"), []byte(mxuContent), 0644)

	task := &config.TaskConfig{
		ID:         "task_mock_maa",
		Name:       "Mock MAA 任务",
		Executable: filepath.Join(tempDir, "MockGame.exe"),
		WorkingDir: tempDir,
		Enabled:    true,
	}
	if err := cfgMgr.AddTask(task); err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	// 1. 测试 GET /api/v1/tasks/{id}/maa
	reqGet := httptest.NewRequest("GET", "/api/v1/tasks/task_mock_maa/maa", nil)
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", recGet.Code, recGet.Body.String())
	}

	// 2. 测试 POST /api/v1/tasks/{id}/maa (关闭 DailyReward)
	postBody := map[string]any{
		"tasks": []map[string]any{
			{"name": "DailyReward", "enabled": false},
		},
	}
	postBytes, _ := json.Marshal(postBody)
	reqPost := httptest.NewRequest("POST", "/api/v1/tasks/task_mock_maa/maa", bytes.NewReader(postBytes))
	recPost := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on save, got %d: %s", recPost.Code, recPost.Body.String())
	}

	// 3. 再次 GET 验证已更新为 false
	recGet2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet2, reqGet)
	var respGet struct {
		Tasks []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal(recGet2.Body.Bytes(), &respGet)
	if len(respGet.Tasks) == 0 || respGet.Tasks[0].Enabled {
		t.Fatalf("Task should be disabled after update: %+v", respGet)
	}

	// 4. 测试修改普通设置 (PUT /api/v1/tasks/{id}) 时 MAA 预设绑定不会丢失
	task.MAAConfigID = "i1"
	task.MAAConfigName = "默认配置"
	_ = cfgMgr.UpdateTask(task)

	putBody := map[string]any{
		"id":         "task_mock_maa",
		"name":       "修改后的名称",
		"executable": filepath.Join(tempDir, "MockGame.exe"),
		"enabled":    true,
	}
	putBytes, _ := json.Marshal(putBody)
	reqPut := httptest.NewRequest("PUT", "/api/v1/tasks/task_mock_maa", bytes.NewReader(putBytes))
	recPut := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recPut, reqPut)
	if recPut.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on PUT task, got %d: %s", recPut.Code, recPut.Body.String())
	}

	chkTask := cfgMgr.Get().Tasks[0]
	if chkTask.MAAConfigID != "i1" || chkTask.MAAConfigName != "默认配置" {
		t.Fatalf("Bug 3 regressed: MAA profile lost after editing task settings! ID=%s, Name=%s", chkTask.MAAConfigID, chkTask.MAAConfigName)
	}
}

// setupMXUMockProject 构造带 MXU 项目的 mock 任务（多条目同名任务场景），返回临时项目目录
func setupMXUMockProject(t *testing.T, srv *Server, cfgMgr *config.Manager, taskID string) string {
	t.Helper()
	tempDir := t.TempDir()

	interfaceContent := `{
  "interface_version": 2,
  "name": "MockGame",
  "task": [
    {
      "name": "制衣",
      "label": "🧵 制衣",
      "description": "可自行添加额外的制衣任务",
      "default_check": true,
      "option": ["ClothSlotOption"]
    }
  ],
  "option": {
    "ClothSlotOption": {"type":"select","cases":["Slot1","Slot2"],"default_case":"Slot1"}
  }
}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)
	_ = os.MkdirAll(filepath.Join(tempDir, "config"), 0755)
	mxuContent := `{
  "version": "1.0",
  "instances": [
    {
      "id": "i1",
      "name": "默认配置",
      "tasks": [
        { "id": "e1", "taskName": "制衣", "enabled": true, "expanded": true,
          "enabledByController": { "MuMu模拟器": true },
          "optionValues": { "ClothSlotOption": { "type": "select", "caseName": "Slot2" } } }
      ]
    }
  ],
  "lastActiveInstanceId": "i1"
}`
	_ = os.WriteFile(filepath.Join(tempDir, "config", "mxu-MockGame.json"), []byte(mxuContent), 0644)

	task := &config.TaskConfig{
		ID:         taskID,
		Name:       "Mock MAA 任务",
		Executable: filepath.Join(tempDir, "MockGame.exe"),
		WorkingDir: tempDir,
		Enabled:    true,
	}
	if err := cfgMgr.AddTask(task); err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}
	return tempDir
}

// TestServerTaskMAABindingStaleField 检查配置接口返回绑定漂移状态及原始绑定值。
func TestServerTaskMAABindingStaleField(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)
	setupMXUMockProject(t, srv, cfgMgr, "task_stale")

	// 1. 绑定失效（ID 不在可用列表中）→ binding_stale=true，绑定值原样回显
	task := cfgMgr.Get().Tasks[0]
	task.MAAConfigID = "ghost_id"
	task.MAAConfigName = "旧预设"
	if err := cfgMgr.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	reqGet := httptest.NewRequest("GET", "/api/v1/tasks/task_stale/maa", nil)
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d: %s", recGet.Code, recGet.Body.String())
	}
	var resp struct {
		BindingStale   bool   `json:"binding_stale"`
		BindingID      string `json:"binding_id"`
		BindingName    string `json:"binding_name"`
		ActiveConfigID string `json:"active_config_id"`
		Profiles       []struct {
			ID string `json:"id"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if !resp.BindingStale {
		t.Fatalf("binding_stale should be true for drifted binding: %s", recGet.Body.String())
	}
	if resp.BindingID != "ghost_id" || resp.BindingName != "旧预设" {
		t.Fatalf("binding_id/binding_name should echo stored values, got %q/%q", resp.BindingID, resp.BindingName)
	}
	if resp.ActiveConfigID != "i1" {
		t.Fatalf("View fallback to active profile should be unchanged, got %s", resp.ActiveConfigID)
	}
	if len(resp.Profiles) != 1 || resp.Profiles[0].ID != "i1" {
		t.Fatalf("Profiles mismatch: %+v", resp.Profiles)
	}

	// 2. 重新绑定到可用预设 → binding_stale=false
	task.MAAConfigID = "i1"
	task.MAAConfigName = "默认配置"
	if err := cfgMgr.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	recGet2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet2, reqGet)
	if err := json.Unmarshal(recGet2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.BindingStale || resp.BindingID != "i1" || resp.BindingName != "默认配置" {
		t.Fatalf("Fresh binding should not be stale, got %+v", resp)
	}

	// 3. 未绑定任务 → binding_stale=false 且 binding_id 为空（独立任务，避免 UpdateTask 零值回退重新绑定）
	setupMXUMockProject(t, srv, cfgMgr, "task_fresh")
	recGet3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet3, httptest.NewRequest("GET", "/api/v1/tasks/task_fresh/maa", nil))
	if recGet3.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for unbound task, got %d: %s", recGet3.Code, recGet3.Body.String())
	}
	if err := json.Unmarshal(recGet3.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.BindingStale || resp.BindingID != "" {
		t.Fatalf("Unbound task should not be stale, got %+v", resp)
	}
}

// TestServerDuplicateTaskMAAEntryAPI 复制 MXU 任务条目 API：紧随原条目插入深拷贝、新 id 格式一致
func TestServerDuplicateTaskMAAEntryAPI(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)
	setupMXUMockProject(t, srv, cfgMgr, "task_dup")
	tempDir := cfgMgr.Get().Tasks[0].WorkingDir

	// 1. 正常复制
	body, _ := json.Marshal(map[string]string{"entry_id": "e1"})
	req := httptest.NewRequest("POST", "/api/v1/tasks/task_dup/maa/task-entries/duplicate", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on duplicate, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Message string `json:"message"`
		EntryID string `json:"entry_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if resp.Message == "" || resp.EntryID == "" {
		t.Fatalf("Expected message and entry_id in response, got %+v", resp)
	}
	newID := resp.EntryID
	if len(newID) != 7 || newID == "e1" {
		t.Fatalf("New entry id should be a fresh 7-char id, got %q", newID)
	}
	for _, ch := range newID {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
			t.Fatalf("New id should be lowercase alphanumeric, got %q", newID)
		}
	}

	// 2. 磁盘文件：紧随原条目插入，字段深拷贝、id 重新生成
	raw, err := os.ReadFile(filepath.Join(tempDir, "config", "mxu-MockGame.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Instances []struct {
			Tasks []map[string]any `json:"tasks"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	tasks := root.Instances[0].Tasks
	if len(tasks) != 2 || tasks[0]["id"] != "e1" || tasks[1]["id"] != newID {
		t.Fatalf("Copy should be inserted right after original, got %+v", tasks)
	}
	copied := tasks[1]
	if copied["taskName"] != "制衣" || copied["enabled"] != true || copied["expanded"] != true {
		t.Fatalf("Copy should keep taskName/enabled/expanded, got %+v", copied)
	}
	if ctrl, ok := copied["enabledByController"].(map[string]any); !ok || ctrl["MuMu模拟器"] != true {
		t.Fatalf("Copy should deep-keep enabledByController, got %+v", copied["enabledByController"])
	}

	// 3. GET 视图：同名任务渲染为两条独立条目
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, httptest.NewRequest("GET", "/api/v1/tasks/task_dup/maa", nil))
	var view struct {
		Tasks []struct {
			Name    string `json:"name"`
			EntryID string `json:"entry_id"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal(recGet.Body.Bytes(), &view)
	if len(view.Tasks) != 2 || view.Tasks[0].EntryID != "e1" || view.Tasks[1].EntryID != newID {
		t.Fatalf("Both entries should render separately, got %+v", view.Tasks)
	}

	// 4. entry_id 不存在 → 404
	badBody, _ := json.Marshal(map[string]string{"entry_id": "nope"})
	recBad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recBad, httptest.NewRequest("POST", "/api/v1/tasks/task_dup/maa/task-entries/duplicate", bytes.NewReader(badBody)))
	if recBad.Code != http.StatusNotFound {
		t.Fatalf("Expected 404 for unknown entry_id, got %d: %s", recBad.Code, recBad.Body.String())
	}

	// 5. 缺少 entry_id → 400
	recEmpty := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recEmpty, httptest.NewRequest("POST", "/api/v1/tasks/task_dup/maa/task-entries/duplicate", bytes.NewReader([]byte(`{}`))))
	if recEmpty.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for empty entry_id, got %d", recEmpty.Code)
	}
}

// TestServerTaskMAAEntryAliasAPI 条目显示名别名：存 AutoNexus 自身 config.json，不写第三方文件
func TestServerTaskMAAEntryAliasAPI(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)
	setupMXUMockProject(t, srv, cfgMgr, "task_alias")

	// 1. 设置别名
	body, _ := json.Marshal(map[string]string{"display_name": "制衣-上衣"})
	req := httptest.NewRequest("PUT", "/api/v1/tasks/task_alias/maa/task-entries/e1/alias", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on alias set, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Message     string `json:"message"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Message == "" || resp.DisplayName != "制衣-上衣" {
		t.Fatalf("Unexpected alias response: %+v", resp)
	}
	if got := cfgMgr.Get().Tasks[0].MaaEntryAliases["e1"]; got != "制衣-上衣" {
		t.Fatalf("Alias should be persisted in AutoNexus config, got %q", got)
	}

	// 2. GET 视图带出 display_name（key=entry_id）
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, httptest.NewRequest("GET", "/api/v1/tasks/task_alias/maa", nil))
	var view struct {
		Tasks []struct {
			EntryID     string `json:"entry_id"`
			DisplayName string `json:"display_name"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal(recGet.Body.Bytes(), &view)
	if len(view.Tasks) != 1 || view.Tasks[0].EntryID != "e1" || view.Tasks[0].DisplayName != "制衣-上衣" {
		t.Fatalf("View should render display_name for aliased entry, got %+v", view.Tasks)
	}

	// 3. 不存在的条目 → 404，且别名不被误改
	recBad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recBad, httptest.NewRequest("PUT", "/api/v1/tasks/task_alias/maa/task-entries/ghost/alias", bytes.NewReader(body)))
	if recBad.Code != http.StatusNotFound {
		t.Fatalf("Expected 404 for unknown entry alias, got %d: %s", recBad.Code, recBad.Body.String())
	}
	if got := cfgMgr.Get().Tasks[0].MaaEntryAliases["ghost"]; got != "" {
		t.Fatalf("Alias for unknown entry must not be saved, got %q", got)
	}

	// 4. 空串 = 删除别名
	clearBody, _ := json.Marshal(map[string]string{"display_name": ""})
	recClear := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recClear, httptest.NewRequest("PUT", "/api/v1/tasks/task_alias/maa/task-entries/e1/alias", bytes.NewReader(clearBody)))
	if recClear.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on alias clear, got %d: %s", recClear.Code, recClear.Body.String())
	}
	aliases := cfgMgr.Get().Tasks[0].MaaEntryAliases
	if _, exists := aliases["e1"]; exists {
		t.Fatalf("Empty display_name should delete the alias, got %+v", aliases)
	}
	recGet2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet2, httptest.NewRequest("GET", "/api/v1/tasks/task_alias/maa", nil))
	// 用全新结构体解析：display_name 带 omitempty，缺失时不会覆盖旧结构体里残留的值
	var viewAfter struct {
		Tasks []struct {
			EntryID     string `json:"entry_id"`
			DisplayName string `json:"display_name"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(recGet2.Body.Bytes(), &viewAfter); err != nil {
		t.Fatal(err)
	}
	if len(viewAfter.Tasks) != 1 || viewAfter.Tasks[0].DisplayName != "" {
		t.Fatalf("display_name should disappear after alias clear, got %+v", viewAfter.Tasks)
	}
}

// TestServerEntryOpsUseSelectedConfig 条目级操作必须作用于面板当前选中的预设（config_id 优先），避免"看 A 改 B"。
func TestServerEntryOpsUseSelectedConfig(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)
	tempDir := t.TempDir()

	interfaceContent := `{"interface_version":2,"name":"MockGame","task":[{"name":"制衣","label":"🧵 制衣","option":["ClothSlotOption"]}],"option":{"ClothSlotOption":{"type":"select","cases":["Slot1","Slot2"],"default_case":"Slot1"}}}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)
	_ = os.MkdirAll(filepath.Join(tempDir, "config"), 0755)
	mxuContent := `{"version":"1.0","instances":[
		{"id":"i1","name":"默认配置","tasks":[{"id":"e1","taskName":"制衣","enabled":true}]},
		{"id":"i2","name":"自用","tasks":[{"id":"e2","taskName":"制衣","enabled":true}]}
	],"lastActiveInstanceId":"i1"}`
	_ = os.WriteFile(filepath.Join(tempDir, "config", "mxu-MockGame.json"), []byte(mxuContent), 0644)

	task := &config.TaskConfig{
		ID: "task_sel", Name: "双预设任务",
		Executable: filepath.Join(tempDir, "MockGame.exe"), WorkingDir: tempDir,
		MAAConfigID: "i1", MAAConfigName: "默认配置", Enabled: true,
	}
	if err := cfgMgr.AddTask(task); err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	readInst := func() map[string]int {
		raw, err := os.ReadFile(filepath.Join(tempDir, "config", "mxu-MockGame.json"))
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Instances []struct {
				ID    string           `json:"id"`
				Tasks []map[string]any `json:"tasks"`
			} `json:"instances"`
		}
		if err := json.Unmarshal(raw, &root); err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, inst := range root.Instances {
			out[inst.ID] = len(inst.Tasks)
		}
		return out
	}

	// 1. 面板切到 i2：复制必须发生在 i2（任务绑定仍是 i1）
	body, _ := json.Marshal(map[string]string{"entry_id": "e2", "config_id": "i2"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/tasks/task_sel/maa/task-entries/duplicate", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	counts := readInst()
	if counts["i1"] != 1 || counts["i2"] != 2 {
		t.Fatalf("Expected i2 to gain a copy and i1 untouched, got %+v", counts)
	}

	// 2. config_id 指向不存在的预设 -> 400（不再误回退到任务绑定）
	body2, _ := json.Marshal(map[string]string{"entry_id": "e1", "config_id": "missing"})
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest("POST", "/api/v1/tasks/task_sel/maa/task-entries/duplicate", bytes.NewReader(body2)))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for unknown config_id, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// 3. 别名同样按 config_id 校验条目：i2 的条目带 config_id 时成功
	body3, _ := json.Marshal(map[string]string{"display_name": "制衣-上衣", "config_id": "i2"})
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, httptest.NewRequest("PUT", "/api/v1/tasks/task_sel/maa/task-entries/e2/alias", bytes.NewReader(body3)))
	if rec3.Code != http.StatusOK {
		t.Fatalf("Expected 200 on alias with config_id, got %d: %s", rec3.Code, rec3.Body.String())
	}

	// 4. 不带 config_id 时条目按任务绑定实例（i1）校验：e2 只在 i2 -> 404
	body4, _ := json.Marshal(map[string]string{"display_name": "x"})
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, httptest.NewRequest("PUT", "/api/v1/tasks/task_sel/maa/task-entries/e2/alias", bytes.NewReader(body4)))
	if rec4.Code != http.StatusNotFound {
		t.Fatalf("Expected 404 when entry not in bound instance, got %d: %s", rec4.Code, rec4.Body.String())
	}

	// 5. 别名写入了 AutoNexus 自身配置且带 config_id 的保存可重读
	if got := cfgMgr.Get().Tasks[0].MaaEntryAliases["e2"]; got != "制衣-上衣" {
		t.Fatalf("Expected alias persisted for e2, got %q", got)
	}
}

// TestServerDuplicateTaskMAAEntryConcurrent 并发复制共享同一配置文件必须串行：4 个并发请求各产出一条新条目
func TestServerDuplicateTaskMAAEntryConcurrent(t *testing.T) {
	srv, cfgMgr, _ := setupTestServer(t)
	setupMXUMockProject(t, srv, cfgMgr, "task_race")

	const n = 4
	var wg sync.WaitGroup
	codes := make([]int, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{"entry_id": "e1"})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/tasks/task_race/maa/task-entries/duplicate", bytes.NewReader(body)))
			codes[i] = rec.Code
			var resp struct {
				EntryID string `json:"entry_id"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			ids[i] = resp.EntryID
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("Concurrent duplicate %d: expected 200, got %d", i, code)
		}
	}
	unique := map[string]bool{}
	for _, id := range ids {
		if id == "" || unique[id] {
			t.Fatalf("Expected %d distinct new entry ids, got %v", n, ids)
		}
		unique[id] = true
	}

	tempDir := cfgMgr.Get().Tasks[0].WorkingDir
	raw, err := os.ReadFile(filepath.Join(tempDir, "config", "mxu-MockGame.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Instances []struct {
			Tasks []map[string]any `json:"tasks"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Instances[0].Tasks) != 1+n {
		t.Fatalf("Expected %d entries after concurrent duplicates, got %d", 1+n, len(root.Instances[0].Tasks))
	}
}
