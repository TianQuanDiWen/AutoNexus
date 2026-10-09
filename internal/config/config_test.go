package config

import (
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
