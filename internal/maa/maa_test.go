package maa

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// writeMXUProject 在临时目录构造一个可被 DetectProject 识别的 MXU 项目
func writeMXUProject(t *testing.T, interfaceJSON, mxuJSON string) (dir string, proj *ProjectInfo) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "interface.json"), []byte(interfaceJSON), 0644); err != nil {
		t.Fatal(err)
	}
	var pi struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(interfaceJSON), &pi); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config", fmt.Sprintf("mxu-%s.json", pi.Name))
	if err := os.WriteFile(cfgPath, []byte(mxuJSON), 0644); err != nil {
		t.Fatal(err)
	}
	proj, err := DetectProject(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return dir, proj
}

func TestMAADetectAndConfigLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// 1. 创建 interface.json
	interfaceContent := `{
  "interface_version": 2,
  "name": "TestGame",
  "version": "v1.0.0",
  "task": [
    {
      "name": "TaskA",
      "label": "🚀 任务A",
      "description": "这是任务A说明",
      "default_check": true
    },
    {
      "name": "TaskB",
      "label": "💌 任务B",
      "description": "这是任务B说明",
      "default_check": false
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644); err != nil {
		t.Fatalf("Write interface.json failed: %v", err)
	}

	// 2. 创建 config/mxu-TestGame.json
	configDir := filepath.Join(tempDir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Mkdir config failed: %v", err)
	}

	mxuContent := `{
  "version": "1.0",
  "instances": [
    {
      "id": "inst_1",
      "name": "默认配置",
      "controllerName": "MuMu模拟器",
      "tasks": [
        {
          "id": "t1",
          "taskName": "TaskA",
          "enabled": true,
          "enabledByController": { "MuMu模拟器": true }
        },
        {
          "id": "t2",
          "taskName": "TaskB",
          "enabled": false,
          "enabledByController": { "MuMu模拟器": false }
        }
      ]
    }
  ],
  "settings": {
    "theme": "dark",
    "keepThis": "yes"
  },
  "lastActiveInstanceId": "inst_1"
}`
	configFile := filepath.Join(configDir, "mxu-TestGame.json")
	if err := os.WriteFile(configFile, []byte(mxuContent), 0644); err != nil {
		t.Fatalf("Write mxu config failed: %v", err)
	}

	// 3. 测试 DetectProject
	proj, err := DetectProject(tempDir, filepath.Join(tempDir, "TestGame.exe"))
	if err != nil {
		t.Fatalf("DetectProject failed: %v", err)
	}
	if proj.ProjectName != "TestGame" || proj.ConfigType != "mxu" {
		t.Fatalf("Unexpected project info: %+v", proj)
	}

	// 4. 测试 GetConfigView
	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatalf("GetConfigView failed: %v", err)
	}
	if len(view.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(view.Tasks))
	}
	if view.Tasks[0].Name != "TaskA" || !view.Tasks[0].Enabled {
		t.Fatalf("TaskA should be enabled")
	}
	if view.Tasks[1].Name != "TaskB" || view.Tasks[1].Enabled {
		t.Fatalf("TaskB should be disabled")
	}

	// 5. 测试 SaveConfig：将 TaskA 改为 false，将 TaskB 改为 true
	updateReq := &TaskUpdateRequest{
		Tasks: []TaskUpdateItem{
			{Name: "TaskA", Enabled: false},
			{Name: "TaskB", Enabled: true},
		},
	}
	if err := SaveConfig(proj, updateReq); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// 6. 重新读取并验证更新生效，同时验证 settings 未被破坏
	viewAfter, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatalf("GetConfigView after update failed: %v", err)
	}
	if viewAfter.Tasks[0].Enabled {
		t.Fatalf("TaskA should now be disabled")
	}
	if !viewAfter.Tasks[1].Enabled {
		t.Fatalf("TaskB should now be enabled")
	}

	// 检查原始文件 settings
	updatedBytes, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(updatedBytes, &root); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	settings, ok := root["settings"].(map[string]any)
	if !ok || settings["keepThis"] != "yes" {
		t.Fatalf("Settings field was corrupted: %+v", root)
	}
}

func TestMAADetectRealProjectIfExists(t *testing.T) {
	realPath := `D:\Projects\Go\MaNikki4\install`
	if _, err := os.Stat(realPath); err != nil {
		t.Skip("Real project path does not exist, skipping")
	}

	proj, err := DetectProject(realPath, filepath.Join(realPath, "MaNikki4.exe"))
	if err != nil {
		t.Fatalf("Detect real project failed: %v", err)
	}
	if proj.ProjectName != "MaNikki4" || proj.ConfigType != "mxu" {
		t.Fatalf("Real project detect mismatch: %+v", proj)
	}

	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatalf("GetConfigView on real project failed: %v", err)
	}
	if len(view.Tasks) == 0 {
		t.Fatalf("Real project tasks should not be empty")
	}
	t.Logf("Detected real project MaNikki4 with %d tasks", len(view.Tasks))
}

func TestMAA_MFAALifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// 1. 创建 interface.json 与 tasks 子文件
	interfaceContent := `{
  "name": "TestMFAA",
  "version": "1.0.0",
  "import": ["tasks/task1.json"]
}`
	if err := os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644); err != nil {
		t.Fatal(err)
	}

	tasksDir := filepath.Join(tempDir, "tasks")
	_ = os.MkdirAll(tasksDir, 0755)
	task1Content := `{
  "task": [
    {
      "name": "StartGame",
      "label": "启动游戏",
      "default_check": true
    },
    {
      "name": "DailyReward",
      "label": "每日签到",
      "default_check": false
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(tasksDir, "task1.json"), []byte(task1Content), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. 创建 config/multi_config.json 与 config/configs/c_default.json
	cfgDir := filepath.Join(tempDir, "config", "configs")
	_ = os.MkdirAll(cfgDir, 0755)

	multiCfg := `{
  "curr_config_id": "c_default",
  "config_list": ["c_default"]
}`
	if err := os.WriteFile(filepath.Join(tempDir, "config", "multi_config.json"), []byte(multiCfg), 0644); err != nil {
		t.Fatal(err)
	}

	cDefault := `{
  "name": "Default Config",
  "tasks": [
    {
      "name": "StartGame",
      "is_checked": true,
      "task_option": {"speed": 2}
    },
    {
      "name": "DailyReward",
      "is_checked": false,
      "task_option": {}
    }
  ],
  "extra_setting": "keep_intact"
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "c_default.json"), []byte(cDefault), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. 探测项目
	proj, err := DetectProject(tempDir, filepath.Join(tempDir, "MFW.exe"))
	if err != nil {
		t.Fatalf("DetectProject failed: %v", err)
	}
	if proj.ConfigType != "mfaa" {
		t.Fatalf("Expected ConfigType mfaa, got %s", proj.ConfigType)
	}

	// 4. 读取视图
	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatalf("GetConfigView failed: %v", err)
	}
	if len(view.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(view.Tasks))
	}
	if !view.Tasks[0].Enabled || view.Tasks[1].Enabled {
		t.Fatalf("Task state mismatch: %+v", view.Tasks)
	}

	// 5. 修改配置
	req := &TaskUpdateRequest{
		Tasks: []TaskUpdateItem{
			{Name: "StartGame", Enabled: false},
			{Name: "DailyReward", Enabled: true},
		},
	}
	if err := SaveConfig(proj, req); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// 6. 验证持久化
	view2, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatalf("GetConfigView after update failed: %v", err)
	}
	if view2.Tasks[0].Enabled || !view2.Tasks[1].Enabled {
		t.Fatalf("Task state after update mismatch: %+v", view2.Tasks)
	}

	// 验证 extra_setting 未被破坏
	rawUpdated, _ := os.ReadFile(proj.ConfigPath)
	var root map[string]any
	_ = json.Unmarshal(rawUpdated, &root)
	if root["extra_setting"] != "keep_intact" {
		t.Fatalf("extra_setting was lost: %+v", root)
	}
}

func TestMAADetectMDAAndMFW(t *testing.T) {
	// 测试 MDA (NIKKE)
	mdaPath := `D:\Game\NIKKE\install`
	if _, err := os.Stat(mdaPath); err == nil {
		proj, err := DetectProject(mdaPath, filepath.Join(mdaPath, "MDA.exe"))
		if err != nil {
			t.Fatalf("Detect MDA failed: %v", err)
		}
		if proj.ConfigType != "mxu" {
			t.Fatalf("MDA config type should be mxu, got %s", proj.ConfigType)
		}
		view, err := GetConfigView(proj, "")
		if err != nil {
			t.Fatalf("GetConfigView MDA failed: %v", err)
		}
		if len(view.Tasks) == 0 {
			t.Fatalf("MDA tasks should not be empty")
		}
		if len(view.Profiles) < 2 {
			t.Fatalf("MDA should have multiple profiles, got: %+v", view.Profiles)
		}
		t.Logf("MDA detected successfully: name=%s, profiles=%d, active=%s, tasks=%d",
			view.ProjectName, len(view.Profiles), view.ActiveConfigID, len(view.Tasks))
	}

	// 测试 MFW (Maa_bbb)
	mfwPath := `D:\Game\Maa_bbb-win-x86_64-v1.11.1`
	if _, err := os.Stat(mfwPath); err == nil {
		proj, err := DetectProject(mfwPath, filepath.Join(mfwPath, "MFW.exe"))
		if err != nil {
			t.Fatalf("Detect MFW failed: %v", err)
		}
		if proj.ConfigType != "mfaa" {
			t.Fatalf("MFW config type should be mfaa, got %s", proj.ConfigType)
		}
		view, err := GetConfigView(proj, "")
		if err != nil {
			t.Fatalf("GetConfigView MFW failed: %v", err)
		}
		if len(view.Tasks) == 0 {
			t.Fatalf("MFW tasks should not be empty")
		}
		if len(view.Profiles) < 2 {
			t.Fatalf("MFW should have multiple profiles, got: %+v", view.Profiles)
		}
		t.Logf("MFW detected successfully: name=%s, profiles=%d, active=%s, tasks=%d",
			view.ProjectName, len(view.Profiles), view.ActiveConfigID, len(view.Tasks))
	}
}

func TestMAAMultiProfileLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// 1. 创建 interface.json
	interfaceContent := `{
  "name": "MultiGame",
  "task": [
    {"name": "Task1", "label": "任务1"},
    {"name": "Task2", "label": "任务2"}
  ]
}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)

	// 2. 创建包含 2 个实例的 mxu 配置
	_ = os.MkdirAll(filepath.Join(tempDir, "config"), 0755)
	mxuContent := `{
  "version": "1.0",
  "instances": [
    {
      "id": "profile_daily",
      "name": "日常模式",
      "tasks": [
        {"taskName": "Task1", "enabled": true},
        {"taskName": "Task2", "enabled": false}
      ]
    },
    {
      "id": "profile_event",
      "name": "活动模式",
      "tasks": [
        {"taskName": "Task1", "enabled": false},
        {"taskName": "Task2", "enabled": true}
      ]
    }
  ],
  "lastActiveInstanceId": "profile_daily"
}`
	_ = os.WriteFile(filepath.Join(tempDir, "config", "mxu-MultiGame.json"), []byte(mxuContent), 0644)

	proj, err := DetectProject(tempDir, filepath.Join(tempDir, "MultiGame.exe"))
	if err != nil {
		t.Fatal(err)
	}

	// 3. 默认读取活跃预设 (profile_daily)
	view1, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if view1.ActiveConfigID != "profile_daily" || len(view1.Profiles) != 2 {
		t.Fatalf("Unexpected view1: %+v", view1)
	}
	if !view1.Tasks[0].Enabled || view1.Tasks[1].Enabled {
		t.Fatalf("profile_daily tasks state mismatch: %+v", view1.Tasks)
	}

	// 4. 查看活动模式预设 (profile_event)
	view2, err := GetConfigView(proj, "profile_event")
	if err != nil {
		t.Fatal(err)
	}
	if view2.ActiveConfigID != "profile_event" {
		t.Fatalf("Expected profile_event, got %s", view2.ActiveConfigID)
	}
	if view2.Tasks[0].Enabled || !view2.Tasks[1].Enabled {
		t.Fatalf("profile_event tasks state mismatch: %+v", view2.Tasks)
	}

	// 5. 保存时切换生效预设为 profile_event 并修改 Task1 为 true
	req := &TaskUpdateRequest{
		ConfigID: "profile_event",
		Tasks: []TaskUpdateItem{
			{Name: "Task1", Enabled: true},
			{Name: "Task2", Enabled: true},
		},
	}
	if err := SaveConfig(proj, req); err != nil {
		t.Fatal(err)
	}

	// 验证磁盘上活跃预设已切换为 profile_event
	view3, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if view3.ActiveConfigID != "profile_event" {
		t.Fatalf("Active profile should now be profile_event, got %s", view3.ActiveConfigID)
	}
	if !view3.Tasks[0].Enabled || !view3.Tasks[1].Enabled {
		t.Fatalf("Task states not updated: %+v", view3.Tasks)
	}

	// 6. 测试 EnsureActiveProfile
	if err := EnsureActiveProfile(tempDir, filepath.Join(tempDir, "MultiGame.exe"), "profile_daily"); err != nil {
		t.Fatal(err)
	}
	view4, _ := GetConfigView(proj, "")
	if view4.ActiveConfigID != "profile_daily" {
		t.Fatalf("EnsureActiveProfile failed to switch to profile_daily: %s", view4.ActiveConfigID)
	}
}

func TestMAABugFixesAndSubOptions(t *testing.T) {
	tempDir := t.TempDir()

	// 1. 创建带有 option 声明的 interface.json
	interfaceContent := `{
  "interface_version": 2,
  "name": "SubOptTest",
  "version": "v1.0.0",
  "task": [
    {
      "name": "Fight",
      "label": "战斗任务",
      "default_check": true,
      "option": ["AutoBattleSwitch", "InputPath"]
    },
    {
      "name": "Shop",
      "label": "商店任务",
      "default_check": true
    }
  ],
  "option": {
    "AutoBattleSwitch": {
      "type": "switch",
      "label": "自动战斗",
      "description": "是否开启全自动连招",
      "default_case": "Yes"
    },
    "InputPath": {
      "type": "input",
      "inputs": [{"name":"path_key", "default":"C:\\\\default"}]
    }
  }
}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)
	configDir := filepath.Join(tempDir, "config")
	_ = os.MkdirAll(configDir, 0755)

	// 2. 测试 PI 配置文件: config/maa_pi_config.json
	piConfigFile := filepath.Join(configDir, "maa_pi_config.json")
	initialPI := `{
  "custom_field": "keep_me",
  "task": [
    {
      "name": "Fight",
      "preserved_attr": 12345,
      "option": {
        "AutoBattleSwitch": false,
        "InputPath": {"values":{"path_key":"C:\\\\old"}, "value":"legacy", "keep":"sidecar"},
        "existing_custom_param": "must_not_lost"
      }
    }
  ]
}`
	_ = os.WriteFile(piConfigFile, []byte(initialPI), 0644)

	proj, err := DetectProject(tempDir, "")
	if err != nil {
		t.Fatalf("DetectProject failed: %v", err)
	}

	// 验证 GetConfigView 能正确读出 SubOptions 及其当前值
	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatalf("GetConfigView failed: %v", err)
	}
	if len(view.Tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(view.Tasks))
	}
	fightTask := view.Tasks[0]
	if len(fightTask.SubOptions) != 2 {
		t.Fatalf("Expected 2 sub-options for Fight, got %d", len(fightTask.SubOptions))
	}
	if fightTask.SubOptions[0].Name != "AutoBattleSwitch" || fightTask.SubOptions[0].Value != false {
		t.Fatalf("SubOption value mismatch: %+v", fightTask.SubOptions[0])
	}
	// 验证 Shop 因为不在 pi task 列表中，因此 enabled 应为 false（Bug 4 验证：非 default_check）
	shopTask := view.Tasks[1]
	if shopTask.Enabled {
		t.Fatalf("Shop should be disabled since runtime config exists but task is missing from list")
	}

	// Bug 1 测试：修改 Fight 的子选项为 true，并保存，验证原有 existing_custom_param 及 custom_field 未丢失
	saveReq := &TaskUpdateRequest{
		Tasks: []TaskUpdateItem{
			{
				Name:    "Fight",
				Enabled: true,
				SubOptions: []SubOptionUpdate{
					{Name: "AutoBattleSwitch", Value: true},
					{Name: "InputPath", Type: "input", InputKey: "path_key", Value: "D:\\\\updated"},
				},
			},
		},
	}
	if err := SaveConfig(proj, saveReq); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// 读取磁盘校验
	savedPIData, _ := os.ReadFile(piConfigFile)
	var savedPI map[string]any
	_ = json.Unmarshal(savedPIData, &savedPI)
	if savedPI["custom_field"] != "keep_me" {
		t.Fatalf("custom_field was lost: %+v", savedPI)
	}
	tasksArr, _ := savedPI["task"].([]any)
	if len(tasksArr) != 1 {
		t.Fatalf("Expected 1 task in saved PI, got %d", len(tasksArr))
	}
	t0 := tasksArr[0].(map[string]any)
	if t0["preserved_attr"] != float64(12345) {
		t.Fatalf("preserved_attr was lost: %+v", t0)
	}
	opt0 := t0["option"].(map[string]any)
	if opt0["existing_custom_param"] != "must_not_lost" {
		t.Fatalf("existing_custom_param was lost! Bug 1 regressed: %+v", opt0)
	}
	if opt0["AutoBattleSwitch"] != true {
		t.Fatalf("AutoBattleSwitch was not updated to true: %+v", opt0)
	}
	inOpt := opt0["InputPath"].(map[string]any)
	inVals := inOpt["values"].(map[string]any)
	if inVals["path_key"] != "D:\\\\updated" || inOpt["value"] != "legacy" || inOpt["keep"] != "sidecar" {
		t.Fatalf("InputPath round trip or sidecar preservation failed: %+v", inOpt)
	}
	viewAfterInput, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := viewAfterInput.Tasks[0].SubOptions[1].Value; got != "D:\\\\updated" {
		t.Fatalf("InputPath view should reflect saved value, got %v", got)
	}

	// Bug 4 测试：清空所有任务保存（全部禁用），再次查看 GetConfigView，两任务都应为 disabled，而不是 default_check true
	emptyReq := &TaskUpdateRequest{
		Tasks: []TaskUpdateItem{
			{Name: "Fight", Enabled: false},
			{Name: "Shop", Enabled: false},
		},
	}
	if err := SaveConfig(proj, emptyReq); err != nil {
		t.Fatalf("SaveConfig empty failed: %v", err)
	}
	viewEmpty, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if viewEmpty.Tasks[0].Enabled || viewEmpty.Tasks[1].Enabled {
		t.Fatalf("Bug 4 regressed: all tasks should be disabled, got %+v", viewEmpty.Tasks)
	}

	// Bug 2 测试：MXU 无效预设 ID 报错且不修改默认配置
	_ = os.Remove(piConfigFile) // 移除 PI 配置，换用 MXU
	mxuContent := `{
  "instances": [
    {"id": "inst_1", "name": "正常预设", "tasks": [{"taskName": "Fight", "enabled": true}]}
  ],
  "lastActiveInstanceId": "inst_1"
}`
	mxuPath := filepath.Join(configDir, "mxu-SubOptTest.json")
	_ = os.WriteFile(mxuPath, []byte(mxuContent), 0644)
	projMXU, _ := DetectProject(tempDir, "")

	invalidReq := &TaskUpdateRequest{
		ConfigID: "non_existent_id",
		Tasks: []TaskUpdateItem{
			{Name: "Fight", Enabled: false},
		},
	}
	err = SaveConfig(projMXU, invalidReq)
	if err == nil {
		t.Fatalf("Bug 2 regressed: SaveConfig should return error for non-existent MXU profile ID")
	}
	// 验证 inst_1 未被改动
	mxuRaw, _ := os.ReadFile(mxuPath)
	if !strings.Contains(string(mxuRaw), `"lastActiveInstanceId": "inst_1"`) {
		t.Fatalf("Bug 2 regressed: lastActiveInstanceId was modified despite invalid ID!")
	}
	if !strings.Contains(string(mxuRaw), `"enabled": true`) {
		t.Fatalf("Bug 2 regressed: inst_1 tasks were modified despite invalid ID!")
	}

	// 验证 EnsureActiveProfile 对无效 ID 也报错
	err = EnsureActiveProfile(tempDir, "", "non_existent_id")
	if err == nil {
		t.Fatalf("Bug 2 regressed: EnsureActiveProfile should fail for non-existent profile ID")
	}
}

func TestInputValuesRoundTripPIAndMFAA(t *testing.T) {
	for _, mode := range []string{"pi", "mfaa"} {
		for _, shape := range []string{"values", "value"} {
			t.Run(mode+"/"+shape, func(t *testing.T) {
				dir := t.TempDir()
				interfaceJSON := `{"name":"InputRoundTrip","task":[{"name":"TaskA","default_check":true,"option":["Input"]}],"option":{"Input":{"type":"input","inputs":[{"name":"path","default":"default"}]}}}`
				if err := os.WriteFile(filepath.Join(dir, "interface.json"), []byte(interfaceJSON), 0644); err != nil {
					t.Fatal(err)
				}
				opt := `{"value":"old","sidecar":"preserved"}`
				if shape == "values" {
					opt = `{"values":{"path":"old","other":"keep"},"sidecar":"preserved"}`
				}
				var taskConfig, configID string
				if mode == "pi" {
					taskConfig = `{"task":[{"name":"TaskA","option":{"Input":` + opt + `}}]}`
					configID = "default"
					_ = os.MkdirAll(filepath.Join(dir, "config"), 0755)
					if err := os.WriteFile(filepath.Join(dir, "config", "maa_pi_config.json"), []byte(taskConfig), 0644); err != nil {
						t.Fatal(err)
					}
				} else {
					_ = os.MkdirAll(filepath.Join(dir, "config", "configs"), 0755)
					if err := os.WriteFile(filepath.Join(dir, "config", "multi_config.json"), []byte(`{"curr_config_id":"default","config_list":["default"]}`), 0644); err != nil {
						t.Fatal(err)
					}
					taskConfig = `{"tasks":[{"name":"TaskA","is_checked":true,"task_option":{"Input":` + opt + `}}]}`
					if err := os.WriteFile(filepath.Join(dir, "config", "configs", "default.json"), []byte(taskConfig), 0644); err != nil {
						t.Fatal(err)
					}
					configID = "default"
				}
				proj, err := DetectProject(dir, "")
				if err != nil {
					t.Fatal(err)
				}
				view, err := GetConfigView(proj, "")
				if err != nil {
					t.Fatal(err)
				}
				if got := view.Tasks[0].SubOptions[0].Value; got != "old" {
					t.Fatalf("initial input = %v", got)
				}
				req := &TaskUpdateRequest{ConfigID: configID, Tasks: []TaskUpdateItem{{Name: "TaskA", Enabled: true, SubOptions: []SubOptionUpdate{{Name: "Input", Type: "input", InputKey: "path", Value: "new"}}}}}
				if err := SaveConfig(proj, req); err != nil {
					t.Fatal(err)
				}
				view, err = GetConfigView(proj, "")
				if err != nil {
					t.Fatal(err)
				}
				if got := view.Tasks[0].SubOptions[0].Value; got != "new" {
					t.Fatalf("saved input = %v", got)
				}
				data, err := os.ReadFile(proj.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				var root map[string]any
				if err := json.Unmarshal(data, &root); err != nil {
					t.Fatal(err)
				}
				var stored map[string]any
				if mode == "pi" {
					task := root["task"].([]any)[0].(map[string]any)
					stored = task["option"].(map[string]any)["Input"].(map[string]any)
				} else {
					task := root["tasks"].([]any)[0].(map[string]any)
					stored = task["task_option"].(map[string]any)["Input"].(map[string]any)
				}
				if stored["sidecar"] != "preserved" {
					t.Fatalf("object sidecar lost: %+v", stored)
				}
				if shape == "values" {
					values := stored["values"].(map[string]any)
					if values["path"] != "new" || values["other"] != "keep" {
						t.Fatalf("values not preserved/updated: %+v", stored)
					}
				} else if stored["value"] != "new" {
					t.Fatalf("value field not updated: %+v", stored)
				}
			})
		}
	}
}

func TestMAAOptionTypesAndNonBooleanCases(t *testing.T) {
	tempDir := t.TempDir()

	interfaceContent := `{
  "name": "OptTypeTest",
  "task": [
    {
      "name": "BattleTask",
      "label": "战斗任务",
      "default_check": true,
      "option": ["Difficulty", "InputPath", "AutoBattle"]
    }
  ],
  "option": {
    "Difficulty": {
      "label": "关卡难度",
      "cases": ["Easy", "Hard"],
      "default_case": "Easy"
    },
    "InputPath": {
      "type": "input",
      "label": "路径配置",
      "inputs": [
        {"name": "path_key", "default": "C:\\default"}
      ]
    },
    "AutoBattle": {
      "cases": ["Yes", "No"],
      "default_case": "Yes"
    }
  }
}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)
	configDir := filepath.Join(tempDir, "config")
	_ = os.MkdirAll(configDir, 0755)

	// 测试 MXU 模式下 Difficulty="Hard"（select类型），InputPath="D:\\Custom"（input类型），AutoBattle=false（switch）
	mxuContent := `{
  "instances": [
    {
      "id": "inst_1",
      "name": "默认配置",
      "tasks": [
        {
          "taskName": "BattleTask",
          "enabled": true,
          "optionValues": {
            "Difficulty": {
              "type": "select",
              "caseName": "Hard"
            },
            "InputPath": {
              "type": "input",
              "values": {
                "path_key": "D:\\Custom"
              }
            },
            "AutoBattle": {
              "type": "switch",
              "value": false
            }
          }
        }
      ]
    }
  ],
  "lastActiveInstanceId": "inst_1"
}`
	_ = os.WriteFile(filepath.Join(configDir, "mxu-OptTypeTest.json"), []byte(mxuContent), 0644)

	proj, err := DetectProject(tempDir, "")
	if err != nil {
		t.Fatal(err)
	}

	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Tasks) != 1 || len(view.Tasks[0].SubOptions) != 3 {
		t.Fatalf("Unexpected view suboptions: %+v", view.Tasks)
	}

	subMap := make(map[string]SubOptionItem)
	for _, sub := range view.Tasks[0].SubOptions {
		subMap[sub.Name] = sub
	}

	// 验证 Difficulty 被正确识别为 select（而不是错误地识别为 switch）
	diffSub := subMap["Difficulty"]
	if diffSub.Type != "select" {
		t.Fatalf("Difficulty with Easy/Hard cases should be identified as 'select', got: %s", diffSub.Type)
	}
	if diffSub.Value != "Hard" {
		t.Fatalf("Difficulty value should be 'Hard', got: %v", diffSub.Value)
	}

	// 验证 InputPath 被识别为 input
	inputSub := subMap["InputPath"]
	if inputSub.Type != "input" || inputSub.InputKey != "path_key" || inputSub.Value != "D:\\Custom" {
		t.Fatalf("InputPath suboption mismatch: %+v", inputSub)
	}

	// 验证 AutoBattle 为 switch
	battleSub := subMap["AutoBattle"]
	if battleSub.Type != "switch" || battleSub.Value != false {
		t.Fatalf("AutoBattle suboption mismatch: %+v", battleSub)
	}

	// 执行直接保存（模拟前端打开后直接保存，或者将 Difficulty 选为 Easy）
	saveReq := &TaskUpdateRequest{
		ConfigID: "inst_1",
		Tasks: []TaskUpdateItem{
			{
				Name:    "BattleTask",
				Enabled: true,
				SubOptions: []SubOptionUpdate{
					{Name: "Difficulty", Type: "select", Value: "Hard"},
					{Name: "InputPath", Type: "input", Value: "D:\\Custom_Updated", InputKey: "path_key"},
					{Name: "AutoBattle", Type: "switch", Value: true},
				},
			},
		},
	}
	if err := SaveConfig(proj, saveReq); err != nil {
		t.Fatal(err)
	}

	// 校验磁盘上的值：Difficulty 必须保留为 "Hard" 字符串，不能被转成布尔值 false
	savedBytes, _ := os.ReadFile(proj.ConfigPath)
	var root map[string]any
	_ = json.Unmarshal(savedBytes, &root)
	insts := root["instances"].([]any)
	tasks := insts[0].(map[string]any)["tasks"].([]any)
	optVals := tasks[0].(map[string]any)["optionValues"].(map[string]any)
	diffOpt := optVals["Difficulty"].(map[string]any)
	if diffOpt["caseName"] != "Hard" {
		t.Fatalf("Bug 2 regressed: Difficulty was not saved as Hard! Got: %+v", diffOpt)
	}
	inputOpt := optVals["InputPath"].(map[string]any)
	valuesMap := inputOpt["values"].(map[string]any)
	if valuesMap["path_key"] != "D:\\Custom_Updated" {
		t.Fatalf("InputPath was not updated: %+v", inputOpt)
	}
	autoOpt := optVals["AutoBattle"].(map[string]any)
	if autoOpt["value"] != true {
		t.Fatalf("AutoBattle was not updated to true: %+v", autoOpt)
	}
}

func TestMAAPISavePreservesTaskOrder(t *testing.T) {
	tempDir := t.TempDir()

	// interface.json 中顺序为 TaskA -> TaskB -> TaskC
	interfaceContent := `{
  "name": "OrderTest",
  "task": [
    {"name": "TaskA", "label": "任务A"},
    {"name": "TaskB", "label": "任务B"},
    {"name": "TaskC", "label": "任务C"}
  ]
}`
	_ = os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceContent), 0644)
	configDir := filepath.Join(tempDir, "config")
	_ = os.MkdirAll(configDir, 0755)

	// 原始 PI 配置文件中顺序为 TaskB -> TaskA（存在前后依赖）
	initialPI := `{
  "task": [
    {"name": "TaskB", "option": {"foo": "bar"}},
    {"name": "TaskA", "option": {"baz": 123}}
  ]
}`
	piPath := filepath.Join(configDir, "maa_pi_config.json")
	_ = os.WriteFile(piPath, []byte(initialPI), 0644)

	proj, err := DetectProject(tempDir, "")
	if err != nil {
		t.Fatal(err)
	}

	// 前端按 interface.json 的任务顺序提交 TaskA, TaskB, TaskC
	req := &TaskUpdateRequest{
		Tasks: []TaskUpdateItem{
			{Name: "TaskA", Enabled: true},
			{Name: "TaskB", Enabled: true},
			{Name: "TaskC", Enabled: true}, // 新增加的任务
		},
	}
	if err := SaveConfig(proj, req); err != nil {
		t.Fatal(err)
	}

	// 读取保存后的文件，验证顺序必须保持 TaskB -> TaskA -> TaskC，而不是被重排为 TaskA -> TaskB -> TaskC
	savedBytes, _ := os.ReadFile(piPath)
	var root map[string]any
	_ = json.Unmarshal(savedBytes, &root)
	tasksArr := root["task"].([]any)
	if len(tasksArr) != 3 {
		t.Fatalf("Expected 3 tasks, got %d", len(tasksArr))
	}
	t0 := tasksArr[0].(map[string]any)["name"].(string)
	t1 := tasksArr[1].(map[string]any)["name"].(string)
	t2 := tasksArr[2].(map[string]any)["name"].(string)

	if t0 != "TaskB" || t1 != "TaskA" || t2 != "TaskC" {
		t.Fatalf("Bug 3 regressed: Task order was altered! Got: [%s, %s, %s], expected [TaskB, TaskA, TaskC]", t0, t1, t2)
	}
}

// TestEnsureActiveProfileMXUMissingListsAvailable 检查 MXU 预设不存在时报错携带当前可用实例列表，
// 且错误前缀「指定的 MXU 预设不存在: <id>」保持原样（上层 engine 依赖激活失败链路）
func TestEnsureActiveProfileMXUMissingListsAvailable(t *testing.T) {
	interfaceJSON := `{"name":"BindTest","task":[{"name":"TaskA","label":"任务A"}]}`
	mxuJSON := `{
	  "instances": [
	    {"id": "k2qskdc", "name": "默认配置", "tasks": []}
	  ],
	  "lastActiveInstanceId": "k2qskdc"
	}`
	dir, _ := writeMXUProject(t, interfaceJSON, mxuJSON)

	err := EnsureActiveProfile(dir, "", "zjjlz3y")
	if err == nil {
		t.Fatal("Expected error for non-existent MXU profile ID")
	}
	msg := err.Error()
	if !strings.Contains(msg, "指定的 MXU 预设不存在: zjjlz3y") {
		t.Fatalf("Error prefix must stay intact, got: %s", msg)
	}
	if !strings.Contains(msg, "k2qskdc=默认配置") {
		t.Fatalf("Error should list available instances (id=name), got: %s", msg)
	}
	if !strings.Contains(msg, "当前可用") {
		t.Fatalf("Error should carry 当前可用 marker, got: %s", msg)
	}

	// 报错路径不得改写活跃实例
	raw, err := os.ReadFile(filepath.Join(dir, "config", "mxu-BindTest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"lastActiveInstanceId": "k2qskdc"`) {
		t.Fatalf("Active instance should be untouched on failure: %s", raw)
	}
}

func TestResolveActiveProfileMigratesUniqueMXUProfile(t *testing.T) {
	interfaceJSON := `{"name":"BindTest","task":[]}`
	mxuJSON := `{"instances":[{"id":"ve74oyg","name":"默认配置","tasks":[]}],"lastActiveInstanceId":"ve74oyg"}`
	dir, _ := writeMXUProject(t, interfaceJSON, mxuJSON)
	id, name, rebound, err := ResolveActiveProfile(dir, "", "calof4n", "旧名称")
	if err != nil {
		t.Fatal(err)
	}
	if id != "ve74oyg" || name != "默认配置" || !rebound {
		t.Fatalf("unexpected migration: id=%q name=%q rebound=%v", id, name, rebound)
	}
}

func TestResolveActiveProfileUsesUniqueNameAndRejectsAmbiguity(t *testing.T) {
	interfaceJSON := `{"name":"BindTest","task":[]}`
	dir, _ := writeMXUProject(t, interfaceJSON, `{"instances":[{"id":"newid01","name":"Work","tasks":[]},{"id":"other01","name":"Other","tasks":[]}]}`)
	id, name, rebound, err := ResolveActiveProfile(dir, "", "oldid01", "Work")
	if err != nil || id != "newid01" || name != "Work" || !rebound {
		t.Fatalf("unique name match failed: %q %q %v %v", id, name, rebound, err)
	}
	dir, _ = writeMXUProject(t, interfaceJSON, `{"instances":[{"id":"newid01","name":"Work","tasks":[]},{"id":"other01","name":"Work","tasks":[]}],"lastActiveInstanceId":"other01"}`)
	if _, _, _, err := ResolveActiveProfile(dir, "", "oldid01", "Work"); err == nil || !strings.Contains(err.Error(), "多个 MXU 预设") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	assertMXUActiveID(t, dir, "BindTest", "other01")
}

func TestResolveActiveProfilePrefersExistingIDOverName(t *testing.T) {
	interfaceJSON := `{"name":"BindTest","task":[]}`
	dir, _ := writeMXUProject(t, interfaceJSON, `{"instances":[{"id":"saved01","name":"Renamed","tasks":[]},{"id":"other01","name":"Work","tasks":[]}],"lastActiveInstanceId":"other01"}`)
	id, name, rebound, err := ResolveActiveProfile(dir, "", "saved01", "Work")
	if err != nil || id != "saved01" || name != "Renamed" || rebound {
		t.Fatalf("existing ID must win over stale name: id=%q name=%q rebound=%v err=%v", id, name, rebound, err)
	}
	assertMXUActiveID(t, dir, "BindTest", "saved01")
}

func TestResolveActiveProfileRejectsCorruptMXUWithoutMigration(t *testing.T) {
	interfaceJSON := `{"name":"BindTest","task":[]}`
	for _, tc := range []struct{ name, data string }{
		{name: "invalid JSON", data: `{"instances":`},
		{name: "invalid sole instance", data: `{"instances":[null]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, proj := writeMXUProject(t, interfaceJSON, `{"instances":[{"id":"valid01","name":"Only","tasks":[]}],"lastActiveInstanceId":"valid01"}`)
			if err := os.WriteFile(proj.ConfigPath, []byte(tc.data), 0644); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("ResolveActiveProfile panicked: %v", recovered)
				}
			}()
			if _, _, _, err := ResolveActiveProfile(dir, "", "missing1", "Old"); err == nil {
				t.Fatal("expected malformed MXU config error")
			}
			got, err := os.ReadFile(proj.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.data {
				t.Fatalf("malformed config must remain untouched, got %s", got)
			}
		})
	}
}

func TestResolveActiveProfileMFAAMatchesDisplayName(t *testing.T) {
	dir := writeMFAAProject(t, "current", `{"config_list":["cfg_a","cfg_b"],"curr_config_id":"current"}`, map[string]string{
		"cfg_a":   `{"name":"Daily","tasks":[]}`,
		"cfg_b":   `{"name":"Weekly","tasks":[]}`,
		"current": `{"name":"Current","tasks":[]}`,
	})
	id, name, rebound, err := ResolveActiveProfile(dir, "", "old_id", "Daily")
	if err != nil || id != "cfg_a" || name != "Daily" || !rebound {
		t.Fatalf("MFAA should resolve saved display name, got id=%q name=%q rebound=%v err=%v", id, name, rebound, err)
	}
	assertMFAAActiveID(t, dir, "cfg_a")
}

func TestResolveActiveProfileMFAARejectsDuplicateDisplayNames(t *testing.T) {
	dir := writeMFAAProject(t, "current", `{"config_list":["cfg_a","cfg_b"],"curr_config_id":"current"}`, map[string]string{
		"cfg_a":   `{"name":"Daily","tasks":[]}`,
		"cfg_b":   `{"name":"Daily","tasks":[]}`,
		"current": `{"name":"Current","tasks":[]}`,
	})
	if _, _, _, err := ResolveActiveProfile(dir, "", "old_id", "Daily"); err == nil || !strings.Contains(err.Error(), "多个 MFAA 预设") || !strings.Contains(err.Error(), "cfg_a=Daily") || !strings.Contains(err.Error(), "cfg_b=Daily") {
		t.Fatalf("expected duplicate display-name error with available names, got %v", err)
	}
	assertMFAAActiveID(t, dir, "current")
}

func TestResolveActiveProfileMFAAPrefersExistingID(t *testing.T) {
	dir := writeMFAAProject(t, "other", `{"config_list":["saved_id","other"],"curr_config_id":"other"}`, map[string]string{
		"saved_id": `{"name":"Current Name","tasks":[]}`,
		"other":    `{"name":"Saved Name","tasks":[]}`,
	})
	id, name, rebound, err := ResolveActiveProfile(dir, "", "saved_id", "Saved Name")
	if err != nil || id != "saved_id" || name != "Current Name" || rebound {
		t.Fatalf("existing MFAA ID must win over saved display name, got id=%q name=%q rebound=%v err=%v", id, name, rebound, err)
	}
	assertMFAAActiveID(t, dir, "saved_id")
}

func TestResolveActiveProfileMFAAPropagatesCandidateParseErrors(t *testing.T) {
	dir := writeMFAAProject(t, "current", `{"config_list":["cfg_a","cfg_b"],"curr_config_id":"current"}`, map[string]string{
		"cfg_a":   `{"name":"Daily","tasks":[]}`,
		"cfg_b":   `{"name":`,
		"current": `{"name":"Current","tasks":[]}`,
	})
	if _, _, _, err := ResolveActiveProfile(dir, "", "old_id", "Daily"); err == nil || !strings.Contains(err.Error(), "解析 MFAA 预设 cfg_b 失败") {
		t.Fatalf("malformed candidate must not be silently ignored, got %v", err)
	}
	assertMFAAActiveID(t, dir, "current")
}

func writeMFAAProject(t *testing.T, currentID, multiJSON string, configs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "interface.json"), []byte(`{"name":"MFAAResolve"}`), 0644); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(dir, "config")
	profilesDir := filepath.Join(configDir, "configs")
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "multi_config.json"), []byte(multiJSON), 0644); err != nil {
		t.Fatal(err)
	}
	for id, data := range configs {
		if err := os.WriteFile(filepath.Join(profilesDir, id+".json"), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(profilesDir, currentID+".json")); err != nil {
		t.Fatalf("current MFAA config fixture missing: %v", err)
	}
	return dir
}

func assertMFAAActiveID(t *testing.T, dir, expected string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config", "multi_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Current string `json:"curr_config_id"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root.Current != expected {
		t.Fatalf("curr_config_id changed: got %q, want %q", root.Current, expected)
	}
}

func TestConfigFileLockKeyNormalizesPathAliases(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".maa-lock-key-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(workingDir, absolute)
	if err != nil {
		t.Fatal(err)
	}
	if configFileLockKey(absolute) != configFileLockKey(relative) {
		t.Fatalf("absolute and relative aliases differ: %q vs %q", configFileLockKey(absolute), configFileLockKey(relative))
	}
	caseVariant := strings.ToUpper(absolute)
	if runtime.GOOS == "windows" {
		if configFileLockKey(absolute) != configFileLockKey(caseVariant) {
			t.Fatalf("Windows case aliases differ")
		}
	} else if absolute != caseVariant && configFileLockKey(absolute) == configFileLockKey(caseVariant) {
		t.Fatalf("case-sensitive platform folded distinct paths")
	}
}

func TestDuplicateTaskEntrySerializesWindowsPathAliases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path alias semantics")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, ".maa-lock-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "interface.json"), []byte(`{"name":"LockAlias"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config", "mxu-LockAlias.json")
	if err := os.WriteFile(cfgPath, []byte(`{"instances":[{"id":"profile1","name":"Default","tasks":[{"id":"entry01","taskName":"Task","enabled":true}]}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	proj, err := DetectProject(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	absPath, err := filepath.Abs(proj.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	relPath, err := filepath.Rel(root, absPath)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{absPath, strings.ToUpper(absPath), relPath, filepath.ToSlash(strings.ToUpper(relPath))}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	for _, alias := range paths {
		wg.Add(1)
		go func(alias string) {
			defer wg.Done()
			copy := *proj
			copy.ConfigPath = alias
			if _, err := DuplicateTaskEntry(&copy, "profile1", "entry01"); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}(alias)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Instances []struct {
			Tasks []any `json:"tasks"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Instances) != 1 {
		t.Fatalf("unexpected instance count after concurrent copies: got %d, want 1", len(saved.Instances))
	}
	if len(saved.Instances[0].Tasks) != 5 {
		t.Fatalf("concurrent copies lost updates: got task count %d, want 5", len(saved.Instances[0].Tasks))
	}
}

func assertMXUActiveID(t *testing.T, dir, projectName, expected string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config", "mxu-"+projectName+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		LastActive string `json:"lastActiveInstanceId"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root.LastActive != expected {
		t.Fatalf("lastActiveInstanceId changed: got %q, want %q", root.LastActive, expected)
	}
}

// TestEnsureActiveProfileMFAAMissingListsAvailable 检查 MFAA 预设不存在时报错携带可用列表。
func TestEnsureActiveProfileMFAAMissingListsAvailable(t *testing.T) {
	tempDir := t.TempDir()
	interfaceJSON := `{"name":"MFAABind","task":[{"name":"TaskA","label":"任务A"}]}`
	if err := os.WriteFile(filepath.Join(tempDir, "interface.json"), []byte(interfaceJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "config", "configs"), 0755); err != nil {
		t.Fatal(err)
	}
	multiCfg := `{"curr_config_id":"c_default","config_list":["c_default","c_profile_a"]}`
	if err := os.WriteFile(filepath.Join(tempDir, "config", "multi_config.json"), []byte(multiCfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "config", "configs", "c_default.json"), []byte(`{"name":"Default","tasks":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "config", "configs", "c_profile_a.json"), []byte(`{"name":"ProfileA","tasks":[]}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := EnsureActiveProfile(tempDir, "", "c_ghost")
	if err == nil {
		t.Fatal("Expected error for non-existent MFAA profile ID")
	}
	msg := err.Error()
	if !strings.Contains(msg, "指定的 MFAA 预设不存在: c_ghost") {
		t.Fatalf("Error prefix must stay intact, got: %s", msg)
	}
	if !strings.Contains(msg, "当前可用: c_default, c_profile_a") {
		t.Fatalf("Error should list available configs, got: %s", msg)
	}

	// 目录不存在时摘要为空（调用方保持原报错）
	if got := summarizeMFAAConfigs(filepath.Join(tempDir, "config", "not-exist")); got != "" {
		t.Fatalf("summarizeMFAAConfigs should return empty for missing dir, got %q", got)
	}
}

// TestSaveConfigMXUMissingProfileListsAvailable 检查保存不存在的 MXU 预设时同样追加可用列表。
func TestSaveConfigMXUMissingProfileListsAvailable(t *testing.T) {
	interfaceJSON := `{"name":"BindSave","task":[{"name":"TaskA","label":"任务A"}]}`
	mxuJSON := `{
	  "instances": [
	    {"id": "inst_1", "name": "正常预设", "tasks": [{"id":"e1","taskName":"TaskA","enabled":true}]}
	  ],
	  "lastActiveInstanceId": "inst_1"
	}`
	_, proj := writeMXUProject(t, interfaceJSON, mxuJSON)

	req := &TaskUpdateRequest{
		ConfigID: "ghost_id",
		Tasks:    []TaskUpdateItem{{Name: "TaskA", Enabled: false}},
	}
	err := SaveConfig(proj, req)
	if err == nil {
		t.Fatal("Expected error for non-existent MXU profile ID")
	}
	msg := err.Error()
	if !strings.Contains(msg, "未找到指定的 MXU 配置预设: ghost_id") {
		t.Fatalf("Error prefix must stay intact, got: %s", msg)
	}
	if !strings.Contains(msg, "inst_1=正常预设") {
		t.Fatalf("Error should list available instances, got: %s", msg)
	}

	// 保存失败时原文件不得被改动
	raw, _ := os.ReadFile(proj.ConfigPath)
	if !strings.Contains(string(raw), `"lastActiveInstanceId": "inst_1"`) || !strings.Contains(string(raw), `"taskName":"TaskA"`) {
		t.Fatalf("Config file should be untouched on failure: %s", raw)
	}
}

// TestGetConfigViewNestedDependentOptions 检查 case 级嵌套依赖选项渲染为带 depends_on/show_when 的
// SubOptionItem，值仍按扁平 optionValues 键读写（如 key=方法）
func TestGetConfigViewNestedDependentOptions(t *testing.T) {
	interfaceJSON := `{
	  "name": "NestTest",
	  "task": [{"name":"Fight","label":"战斗","default_check":true,"option":["指示"]}],
	  "option": {
	    "指示": {
	      "type": "switch",
	      "label": "作战指示",
	      "cases": [
	        {"name": "Yes", "label": "开启", "option": ["方法"]},
	        {"name": "No", "label": "关闭"}
	      ]
	    },
	    "方法": {
	      "type": "select",
	      "label": "攻击方法",
	      "cases": ["A", "B"],
	      "default_case": "A"
	    }
	  }
	}`
	mxuJSON := `{
	  "instances": [{"id":"inst_1","name":"默认","tasks":[
	    {"id":"e1","taskName":"Fight","enabled":true,
	     "optionValues":{"指示":{"type":"switch","value":true},"方法":{"type":"select","caseName":"B"}}}
	  ]}],
	  "lastActiveInstanceId":"inst_1"
	}`
	_, proj := writeMXUProject(t, interfaceJSON, mxuJSON)

	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Tasks) != 1 {
		t.Fatalf("Expected 1 task, got %d", len(view.Tasks))
	}
	subs := view.Tasks[0].SubOptions
	if len(subs) != 2 {
		t.Fatalf("Expected parent + nested sub-options, got %+v", subs)
	}
	parent := subs[0]
	if parent.Name != "指示" || parent.DependsOn != "" || len(parent.ShowWhen) != 0 {
		t.Fatalf("Top-level option must not carry depends_on/show_when: %+v", parent)
	}
	child := subs[1]
	if child.Name != "方法" {
		t.Fatalf("Expected nested option 方法, got %+v", child)
	}
	if child.DependsOn != "指示" {
		t.Fatalf("depends_on should point to parent option, got %q", child.DependsOn)
	}
	if len(child.ShowWhen) != 1 || child.ShowWhen[0] != "Yes" {
		t.Fatalf("show_when should be parent case names, got %v", child.ShowWhen)
	}
	if child.Value != "B" {
		t.Fatalf("Nested option value should be read by flat optionValues key, got %v", child.Value)
	}

	// 扁平键写回：按 taskName 匹配（更新项未携带 entry_id，回退旧兼容路径）
	req := &TaskUpdateRequest{
		ConfigID: "inst_1",
		Tasks: []TaskUpdateItem{{
			Name:       "Fight",
			Enabled:    true,
			SubOptions: []SubOptionUpdate{{Name: "方法", Type: "select", Value: "A"}},
		}},
	}
	if err := SaveConfig(proj, req); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(proj.ConfigPath)
	var disk struct {
		Instances []struct {
			Tasks []map[string]any `json:"tasks"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	optVals := disk.Instances[0].Tasks[0]["optionValues"].(map[string]any)
	methodOpt := optVals["方法"].(map[string]any)
	if methodOpt["caseName"] != "A" {
		t.Fatalf("Nested option should be saved under flat key, got %+v", methodOpt)
	}
	if _, exists := optVals["指示"]; !exists {
		t.Fatalf("Parent option value must be preserved: %+v", optVals)
	}

	viewAfter, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := viewAfter.Tasks[0].SubOptions[1]; got.Value != "A" || got.DependsOn != "指示" || len(got.ShowWhen) != 1 {
		t.Fatalf("Nested option round trip mismatch: %+v", got)
	}
}

// TestGetConfigViewNestedOptionCycleGuard 防环：A→B→A 循环引用不发散，同一条渲染链上已出现的选项不重复展开
func TestGetConfigViewNestedOptionCycleGuard(t *testing.T) {
	interfaceJSON := `{
	  "name": "CycleTest",
	  "task": [{"name":"Work","label":"工作","default_check":true,"option":["X"]}],
	  "option": {
	    "X": {"type":"switch","cases":[{"name":"Yes","option":["Y"]},{"name":"No"}]},
	    "Y": {"type":"switch","cases":[{"name":"On","option":["X"]},{"name":"Off"}]}
	  }
	}`
	mxuJSON := `{
	  "instances": [{"id":"inst_1","name":"默认","tasks":[
	    {"id":"e1","taskName":"Work","enabled":true}
	  ]}],
	  "lastActiveInstanceId":"inst_1"
	}`
	_, proj := writeMXUProject(t, interfaceJSON, mxuJSON)

	view, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	subs := view.Tasks[0].SubOptions
	if len(subs) != 2 {
		t.Fatalf("Cycle should be cut: expected 2 sub-options, got %+v", subs)
	}
	if subs[0].Name != "X" || subs[1].Name != "Y" {
		t.Fatalf("Unexpected sub-options: %+v", subs)
	}
	if subs[1].DependsOn != "X" || subs[1].ShowWhen[0] != "Yes" {
		t.Fatalf("Nested Y metadata mismatch: %+v", subs[1])
	}
}

// TestGetConfigViewMXUPerEntryRendering 多条目渲染：同名任务的多条目渲染为多个 TaskItem
// （entry_id 各异、各自 optionValues 独立、display_name 来自别名映射）
func TestGetConfigViewMXUPerEntryRendering(t *testing.T) {
	interfaceJSON := `{
	  "name": "EntryTest",
	  "task": [
	    {"name":"登陆","label":"登陆游戏","default_check":true},
	    {"name":"制衣","label":"🧵 制衣","description":"可自行添加额外的制衣任务","default_check":true,
	     "option":["ClothSlotOption"]},
	    {"name":"任务","label":"任务奖励","default_check":true}
	  ],
	  "option": {
	    "ClothSlotOption": {"type":"select","cases":["Slot1","Slot2","Slot5"],"default_case":"Slot1"}
	  }
	}`
	mxuJSON := `{
	  "instances": [{"id":"k2qskdc","name":"默认配置","tasks":[
	    {"id":"aaa1111","taskName":"登陆","enabled":true},
	    {"id":"q4vuql0","taskName":"制衣","enabled":true,
	     "optionValues":{"ClothSlotOption":{"type":"select","caseName":"Slot2"}}},
	    {"id":"vwd11s4","taskName":"制衣","enabled":false,
	     "optionValues":{"ClothSlotOption":{"type":"select","caseName":"Slot5"}}}
	  ]}],
	  "lastActiveInstanceId":"k2qskdc"
	}`
	_, proj := writeMXUProject(t, interfaceJSON, mxuJSON)

	aliases := map[string]string{"q4vuql0": "制衣-上衣"}
	view, err := GetConfigViewWithAliases(proj, "", aliases)
	if err != nil {
		t.Fatal(err)
	}
	// 4 个 TaskItem：登陆 + 制衣×2（逐条渲染，不再按 taskName 去重）+ 任务
	if len(view.Tasks) != 4 {
		t.Fatalf("Expected 4 TaskItems (entry-level rendering), got %d: %+v", len(view.Tasks), view.Tasks)
	}
	login := view.Tasks[0]
	if login.Name != "登陆" || login.EntryID != "aaa1111" || !login.Enabled {
		t.Fatalf("Login entry mismatch: %+v", login)
	}
	first := view.Tasks[1]
	second := view.Tasks[2]
	if first.Name != "制衣" || second.Name != "制衣" {
		t.Fatalf("Both 制衣 entries should render, got %+v / %+v", first, second)
	}
	if first.EntryID != "q4vuql0" || second.EntryID != "vwd11s4" {
		t.Fatalf("entry_id must differ per entry, got %s / %s", first.EntryID, second.EntryID)
	}
	if first.Enabled != true || second.Enabled != false {
		t.Fatalf("Each entry keeps its own enabled state, got %+v / %+v", first.Enabled, second.Enabled)
	}
	if got := first.SubOptions[0]; got.Name != "ClothSlotOption" || got.Value != "Slot2" {
		t.Fatalf("First entry option values mismatch: %+v", got)
	}
	if got := second.SubOptions[0]; got.Name != "ClothSlotOption" || got.Value != "Slot5" {
		t.Fatalf("Second entry option values must not be merged/overwritten, got %+v", got)
	}
	if first.DisplayName != "制衣-上衣" {
		t.Fatalf("display_name should come from alias map (key=entry_id), got %q", first.DisplayName)
	}
	if second.DisplayName != "" {
		t.Fatalf("Entries without alias should have empty display_name, got %q", second.DisplayName)
	}
	noEntry := view.Tasks[3]
	if noEntry.Name != "任务" || noEntry.EntryID != "" || noEntry.Enabled {
		t.Fatalf("Task without entry should render disabled with empty entry_id, got %+v", noEntry)
	}

	// JSON 层面：entry_id/display_name 为 omitempty，无别名条目不输出 display_name
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"entry_id":"q4vuql0"`) || !strings.Contains(string(data), `"entry_id":"vwd11s4"`) {
		t.Fatalf("entry_id missing in JSON output: %s", data)
	}
	if !strings.Contains(string(data), `"display_name":"制衣-上衣"`) {
		t.Fatalf("display_name missing in JSON output: %s", data)
	}
	if strings.Count(string(data), `"display_name"`) != 1 {
		t.Fatalf("display_name should only appear for aliased entries: %s", data)
	}

	// 普通 GetConfigView（不带别名）不输出 display_name
	plain, err := GetConfigView(proj, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range plain.Tasks {
		if task.DisplayName != "" {
			t.Fatalf("Plain GetConfigView must not fill display_name: %+v", task)
		}
	}
}

// TestSaveConfigMXUByEntryID 按 entry_id 各自更新同名多条目，互不覆盖；旧兼容与误伤防护
func TestSaveConfigMXUByEntryID(t *testing.T) {
	interfaceJSON := `{
	  "name": "SaveEntry",
	  "task": [{"name":"制衣","label":"制衣","default_check":true,"option":["ClothSlotOption"]}],
	  "option": {
	    "ClothSlotOption": {"type":"select","cases":["Slot1","Slot2","Slot3","Slot5"],"default_case":"Slot1"}
	  }
	}`
	mxuJSON := `{
	  "instances": [{"id":"k2qskdc","name":"默认配置","tasks":[
	    {"id":"q4vuql0","taskName":"制衣","enabled":true,
	     "optionValues":{"ClothSlotOption":{"type":"select","caseName":"Slot2"}}},
	    {"id":"vwd11s4","taskName":"制衣","enabled":true,
	     "optionValues":{"ClothSlotOption":{"type":"select","caseName":"Slot5"}}}
	  ]}],
	  "lastActiveInstanceId":"k2qskdc"
	}`

	newFixture := func(t *testing.T) *ProjectInfo {
		t.Helper()
		_, proj := writeMXUProject(t, interfaceJSON, mxuJSON)
		return proj
	}
	readEntry := func(t *testing.T, proj *ProjectInfo, entryID string) (bool, string) {
		t.Helper()
		raw, err := os.ReadFile(proj.ConfigPath)
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
		for _, entry := range root.Instances[0].Tasks {
			if id, _ := entry["id"].(string); id == entryID {
				enabled, _ := entry["enabled"].(bool)
				caseName := ""
				if optVals, ok := entry["optionValues"].(map[string]any); ok {
					if opt, ok := optVals["ClothSlotOption"].(map[string]any); ok {
						caseName, _ = opt["caseName"].(string)
					}
				}
				return enabled, caseName
			}
		}
		t.Fatalf("entry %s not found on disk", entryID)
		return false, ""
	}

	t.Run("entry_id 精确匹配互不覆盖", func(t *testing.T) {
		proj := newFixture(t)
		req := &TaskUpdateRequest{
			ConfigID: "k2qskdc",
			Tasks: []TaskUpdateItem{
				{Name: "制衣", EntryID: "q4vuql0", Enabled: true,
					SubOptions: []SubOptionUpdate{{Name: "ClothSlotOption", Type: "select", Value: "Slot3"}}},
				{Name: "制衣", EntryID: "vwd11s4", Enabled: false,
					SubOptions: []SubOptionUpdate{{Name: "ClothSlotOption", Type: "select", Value: "Slot1"}}},
			},
		}
		if err := SaveConfig(proj, req); err != nil {
			t.Fatal(err)
		}
		if enabled, caseName := readEntry(t, proj, "q4vuql0"); !enabled || caseName != "Slot3" {
			t.Fatalf("q4vuql0 should keep its own update, got enabled=%v case=%s", enabled, caseName)
		}
		if enabled, caseName := readEntry(t, proj, "vwd11s4"); enabled || caseName != "Slot1" {
			t.Fatalf("vwd11s4 should keep its own update, got enabled=%v case=%s", enabled, caseName)
		}
	})

	t.Run("无 entry_id 回退按 taskName 同步全部同名条目", func(t *testing.T) {
		proj := newFixture(t)
		req := &TaskUpdateRequest{
			Tasks: []TaskUpdateItem{{Name: "制衣", Enabled: false}},
		}
		if err := SaveConfig(proj, req); err != nil {
			t.Fatal(err)
		}
		for _, entryID := range []string{"q4vuql0", "vwd11s4"} {
			if enabled, _ := readEntry(t, proj, entryID); enabled {
				t.Fatalf("Legacy taskName update should hit entry %s", entryID)
			}
		}
	})

	t.Run("未知 entry_id 不误伤其他条目", func(t *testing.T) {
		proj := newFixture(t)
		req := &TaskUpdateRequest{
			Tasks: []TaskUpdateItem{
				{Name: "制衣", EntryID: "ghost_entry", Enabled: false,
					SubOptions: []SubOptionUpdate{{Name: "ClothSlotOption", Type: "select", Value: "Slot1"}}},
			},
		}
		if err := SaveConfig(proj, req); err != nil {
			t.Fatal(err)
		}
		if enabled, caseName := readEntry(t, proj, "q4vuql0"); !enabled || caseName != "Slot2" {
			t.Fatalf("Stale entry_id must not clobber other entries, got enabled=%v case=%s", enabled, caseName)
		}
		if enabled, caseName := readEntry(t, proj, "vwd11s4"); !enabled || caseName != "Slot5" {
			t.Fatalf("Stale entry_id must not clobber other entries, got enabled=%v case=%s", enabled, caseName)
		}
	})
}

// TestDuplicateTaskEntryAndEntryExists 条目复制：紧随原条目插入深拷贝、id 重新生成、其余字段保留
func TestDuplicateTaskEntryAndEntryExists(t *testing.T) {
	interfaceJSON := `{
	  "name": "DupTest",
	  "task": [{"name":"制衣","label":"制衣","default_check":true,"option":["ClothSlotOption"]}],
	  "option": {
	    "ClothSlotOption": {"type":"select","cases":["Slot1","Slot2"],"default_case":"Slot1"}
	  }
	}`
	mxuJSON := `{
	  "instances": [{"id":"k2qskdc","name":"默认配置","tasks":[
	    {"id":"e1","taskName":"制衣","enabled":true,"expanded":true,
	     "enabledByController":{"MuMu模拟器":true},
	     "optionValues":{"ClothSlotOption":{"type":"select","caseName":"Slot2"}}},
	    {"id":"e2","taskName":"登陆","enabled":false}
	  ]}],
	  "lastActiveInstanceId":"k2qskdc"
	}`
	_, proj := writeMXUProject(t, interfaceJSON, mxuJSON)

	// 复制前存在性校验
	if ok, err := TaskEntryExists(proj, "k2qskdc", "e1"); err != nil || !ok {
		t.Fatalf("e1 should exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := TaskEntryExists(proj, "", "ghost"); err != nil || ok {
		t.Fatalf("ghost should not exist, got ok=%v err=%v", ok, err)
	}

	newID, err := DuplicateTaskEntry(proj, "k2qskdc", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if newID == "" || newID == "e1" || len(newID) != 7 {
		t.Fatalf("New entry id should be a fresh 7-char id, got %q", newID)
	}
	for _, ch := range newID {
		if !strings.ContainsRune(mxuEntryIDChars, ch) {
			t.Fatalf("New id should be lowercase alphanumeric, got %q", newID)
		}
	}

	raw, err := os.ReadFile(proj.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Instances []struct {
			ID    string           `json:"id"`
			Tasks []map[string]any `json:"tasks"`
		} `json:"instances"`
		LastActiveInstanceID string `json:"lastActiveInstanceId"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Instances) != 1 || root.Instances[0].ID != "k2qskdc" {
		t.Fatalf("Instance layout should be preserved: %+v", root.Instances)
	}
	tasks := root.Instances[0].Tasks
	if len(tasks) != 3 {
		t.Fatalf("Expected 3 entries after duplicate, got %d", len(tasks))
	}
	// 紧随原条目插入
	if tasks[0]["id"] != "e1" || tasks[1]["id"] != newID || tasks[2]["id"] != "e2" {
		t.Fatalf("Copy should be inserted right after the original, got ids %v/%v/%v", tasks[0]["id"], tasks[1]["id"], tasks[2]["id"])
	}
	copied := tasks[1]
	if copied["taskName"] != "制衣" {
		t.Fatalf("Copy should keep taskName, got %+v", copied)
	}
	if copied["enabled"] != true || copied["expanded"] != true {
		t.Fatalf("Copy should keep enabled/expanded, got %+v", copied)
	}
	if ctrl, ok := copied["enabledByController"].(map[string]any); !ok || ctrl["MuMu模拟器"] != true {
		t.Fatalf("Copy should deep-keep enabledByController, got %+v", copied["enabledByController"])
	}
	optVals, ok := copied["optionValues"].(map[string]any)
	if !ok {
		t.Fatalf("Copy should keep optionValues, got %+v", copied)
	}
	if opt, ok := optVals["ClothSlotOption"].(map[string]any); !ok || opt["caseName"] != "Slot2" {
		t.Fatalf("Copy should deep-keep optionValues, got %+v", optVals)
	}
	if root.LastActiveInstanceID != "k2qskdc" {
		t.Fatalf("Active instance should be untouched: %s", root.LastActiveInstanceID)
	}

	// 复制后新 id 可被校验到
	if ok, err := TaskEntryExists(proj, "k2qskdc", newID); err != nil || !ok {
		t.Fatalf("new entry should exist, got ok=%v err=%v", ok, err)
	}

	// 未知 entry_id -> ErrEntryNotFound
	if _, err := DuplicateTaskEntry(proj, "k2qskdc", "ghost"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("Expected ErrEntryNotFound for unknown entry, got %v", err)
	}
}
