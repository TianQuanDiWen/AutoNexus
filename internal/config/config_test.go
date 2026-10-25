package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyCycleCalculations(t *testing.T) {
	loc := time.Local

	// 1. 测试 ParseHourMinute
	h, m := ParseHourMinute("04:00", 0, 0)
	if h != 4 || m != 0 {
		t.Fatalf("ParseHourMinute failed: got %d:%d, want 4:0", h, m)
	}
	h, m = ParseHourMinute("invalid", 4, 30)
	if h != 4 || m != 30 {
		t.Fatalf("ParseHourMinute fallback failed: got %d:%d, want 4:30", h, m)
	}

	// 2. 测试 GetCycleStartTime
	// 场景 A: 当前时间 10月10日 05:00，刷新时间 04:00 -> 周期起始为 10月10日 04:00
	nowA := time.Date(2026, 10, 10, 5, 0, 0, 0, loc)
	cycleA := GetCycleStartTime("04:00", nowA)
	expectedA := time.Date(2026, 10, 10, 4, 0, 0, 0, loc)
	if !cycleA.Equal(expectedA) {
		t.Fatalf("GetCycleStartTime Case A failed: got %v, want %v", cycleA, expectedA)
	}

	// 场景 B: 当前时间 10月10日 02:00，刷新时间 04:00 -> 尚未到达今日 04:00，周期起始为 10月9日 04:00
	nowB := time.Date(2026, 10, 10, 2, 0, 0, 0, loc)
	cycleB := GetCycleStartTime("04:00", nowB)
	expectedB := time.Date(2026, 10, 9, 4, 0, 0, 0, loc)
	if !cycleB.Equal(expectedB) {
		t.Fatalf("GetCycleStartTime Case B failed: got %v, want %v", cycleB, expectedB)
	}

	// 3. 测试 IsTaskCompletedInCurrentCycle
	successTrue := true
	successFalse := false

	// 任务在 10月10日 04:30 成功运行过
	taskCompletedToday := &TaskConfig{
		RefreshTime:   "04:00",
		LastStartTime: "2026-10-10 04:30:00",
		LastSuccess:   &successTrue,
	}
	// 在 10月10日 05:00 判断，应为已完成
	if !IsTaskCompletedInCurrentCycle(taskCompletedToday, nowA) {
		t.Fatalf("taskCompletedToday should be completed at 05:00")
	}

	// 任务在昨天 10月9日 05:00 运行过，在今天 10月10日 05:00 判断，跨过了 04:00，应为未完成
	taskCompletedYesterday := &TaskConfig{
		RefreshTime:   "04:00",
		LastStartTime: "2026-10-09 05:00:00",
		LastSuccess:   &successTrue,
	}
	if IsTaskCompletedInCurrentCycle(taskCompletedYesterday, nowA) {
		t.Fatalf("taskCompletedYesterday should NOT be completed at 10月10日 05:00")
	}

	// 任务运行失败的情况，即使在今天也不能算作完成
	taskFailedToday := &TaskConfig{
		RefreshTime:   "04:00",
		LastStartTime: "2026-10-10 04:30:00",
		LastSuccess:   &successFalse,
	}
	if IsTaskCompletedInCurrentCycle(taskFailedToday, nowA) {
		t.Fatalf("taskFailedToday should NOT be completed")
	}

	// 4. 测试 GetWaitDurationUntilRefresh
	// 在 02:00 等待 04:00，应等待 2 小时
	waitDur := GetWaitDurationUntilRefresh("04:00", nowB)
	if waitDur != 2*time.Hour {
		t.Fatalf("wait duration should be 2h, got %v", waitDur)
	}

	// 在 05:00 等待 04:00，已到点，应为 0
	waitDurPassed := GetWaitDurationUntilRefresh("04:00", nowA)
	if waitDurPassed != 0 {
		t.Fatalf("wait duration passed should be 0, got %v", waitDurPassed)
	}
}

func TestUpdateTaskPreservesMAABindings(t *testing.T) {
	tempDir := t.TempDir()
	// 创建 interface.json 模拟有效的 MAA 项目目录
	if err := os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(`{"name":"TestGame"}`), 0644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(filepath.Join(tempDir, "config.json"))
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	task := &TaskConfig{
		ID:            "test_task_1",
		Name:          "初始任务名",
		Executable:    filepath.Join(tempDir, "app.exe"),
		WorkingDir:    tempDir,
		MAAConfigID:   "profile_daily",
		MAAConfigName: "日常预设",
	}
	if err := mgr.AddTask(task); err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	// 模拟前端编辑启动设置（只更新了名称与超时，未传递 MAAConfigID）
	updateReq := &TaskConfig{
		ID:             "test_task_1",
		Name:           "修改后的任务名",
		Executable:     filepath.Join(tempDir, "app.exe"),
		WorkingDir:     tempDir,
		TimeoutSeconds: 3600,
	}
	if err := mgr.UpdateTask(updateReq); err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	updated := mgr.Get().Tasks[0]
	if updated.Name != "修改后的任务名" {
		t.Fatalf("Name not updated: %s", updated.Name)
	}
	if updated.MAAConfigID != "profile_daily" || updated.MAAConfigName != "日常预设" {
		t.Fatalf("Bug 3 regressed: MAA profile binding lost on task update! ID=%s, Name=%s", updated.MAAConfigID, updated.MAAConfigName)
	}
}

func TestUpdateTaskClearsMAABindingsWhenProgramChangedOrNonMAA(t *testing.T) {
	tempDir := t.TempDir()
	// 创建 interface.json 模拟有效的 MAA 项目目录
	if err := os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(`{"name":"TestGame"}`), 0644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(filepath.Join(tempDir, "config.json"))
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	task := &TaskConfig{
		ID:            "test_task_2",
		Name:          "MAA任务",
		Executable:    filepath.Join(tempDir, "app.exe"),
		WorkingDir:    tempDir,
		MAAConfigID:   "profile_daily",
		MAAConfigName: "日常预设",
	}
	if err := mgr.AddTask(task); err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	// 场景 1: 用户修改了执行程序为普通程序（没有 interface.json）
	updateNonMAA := &TaskConfig{
		ID:          "test_task_2",
		Name:        "改为普通程序",
		Executable:  "cmd.exe",
		WorkingDir:  "",
		MAAConfigID: "profile_daily", // 前端可能残留或携带旧绑定
	}
	if err := mgr.UpdateTask(updateNonMAA); err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	updated := mgr.Get().Tasks[0]
	if updated.MAAConfigID != "" || updated.MAAConfigName != "" {
		t.Fatalf("MAA bindings should be cleared for non-MAA executable! Got ID=%s, Name=%s", updated.MAAConfigID, updated.MAAConfigName)
	}
}

func TestUpdateTaskCanClearWorkingDirWhenChangingProgram(t *testing.T) {
	for _, changeProgram := range []bool{false, true} {
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.json")
		mgr, err := NewManager(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.AddTask(&TaskConfig{ID: "clear-dir", Executable: filepath.Join(tempDir, "old.exe"), WorkingDir: tempDir}); err != nil {
			t.Fatal(err)
		}
		executable := filepath.Join(tempDir, "old.exe")
		if changeProgram {
			executable = "cmd.exe"
		}
		if err := mgr.UpdateTask(&TaskConfig{ID: "clear-dir", Executable: executable, WorkingDir: ""}); err != nil {
			t.Fatal(err)
		}
		if got := mgr.Get().Tasks[0].WorkingDir; got != "" {
			t.Fatalf("WorkingDir should clear, got %q", got)
		}
		reloaded, err := NewManager(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := reloaded.Get().Tasks[0].WorkingDir; got != "" {
			t.Fatalf("persisted WorkingDir should clear, got %q", got)
		}
	}
}

func TestUpdateTaskMAABindingUsesEffectiveWorkingDir(t *testing.T) {
	tempDir := t.TempDir()
	makeProject := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "interface.json"), []byte(`{"name":"TestGame"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("empty directory equals executable directory", func(t *testing.T) {
		projectDir := filepath.Join(tempDir, "same")
		makeProject(projectDir)
		mgr, err := NewManager(filepath.Join(projectDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		executable := filepath.Join(projectDir, "app.exe")
		if err := mgr.AddTask(&TaskConfig{ID: "same", Executable: executable, WorkingDir: projectDir, MAAConfigID: "daily", MAAConfigName: "Daily"}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.UpdateTask(&TaskConfig{ID: "same", Executable: executable, WorkingDir: ""}); err != nil {
			t.Fatal(err)
		}
		got := mgr.Get().Tasks[0]
		if got.WorkingDir != "" || got.MAAConfigID != "daily" {
			t.Fatalf("expected empty directory and retained binding, got %+v", got)
		}
	})

	t.Run("different MAA directory clears binding", func(t *testing.T) {
		programDir := filepath.Join(tempDir, "program")
		oldDir, newDir := filepath.Join(tempDir, "a"), filepath.Join(tempDir, "b")
		makeProject(oldDir)
		makeProject(newDir)
		mgr, err := NewManager(filepath.Join(tempDir, "other.json"))
		if err != nil {
			t.Fatal(err)
		}
		executable := filepath.Join(programDir, "app.exe")
		if err := mgr.AddTask(&TaskConfig{ID: "different", Executable: executable, WorkingDir: oldDir, MAAConfigID: "daily", MAAConfigName: "Daily"}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.UpdateTask(&TaskConfig{ID: "different", Executable: executable, WorkingDir: newDir}); err != nil {
			t.Fatal(err)
		}
		got := mgr.Get().Tasks[0]
		if got.MAAConfigID != "" || got.MAAConfigName != "" {
			t.Fatalf("expected cleared binding after directory change, got %+v", got)
		}
	})
}
