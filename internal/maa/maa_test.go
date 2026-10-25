package maa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
