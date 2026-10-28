package server

import (
	"bytes"
	"encoding/json"
	"io"
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
