package maa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectInfo MAA 自动化项目元数据与配置文件位置信息
type ProjectInfo struct {
	InterfacePath string `json:"interface_path"`
	ConfigPath    string `json:"config_path"`
	ProjectName   string `json:"project_name"`
	Version       string `json:"version"`
	ConfigType    string `json:"config_type"` // "mxu", "mfaa", "pi", "generic"
	BaseDir       string `json:"base_dir"`
}

// PITask interface.json 中的任务定义
type PITask struct {
	Name         string   `json:"name"`
	Label        string   `json:"label"`
	Entry        string   `json:"entry"`
	Description  string   `json:"description"`
	DefaultCheck bool     `json:"default_check"`
	Option       []string `json:"option"`
}

// PIFile interface.json 核心数据结构
type PIFile struct {
	InterfaceVersion int               `json:"interface_version"`
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Languages        map[string]string `json:"languages,omitempty"`
	Import           []string          `json:"import,omitempty"`
	Task             []PITask          `json:"task"`
	Option           map[string]any    `json:"option"`
}

// findInterfacePath 统一搜索 workingDir 与 exeDir 下的 interface.json 路径及根目录
func findInterfacePath(workingDir, exePath string) (string, string) {
	searchDirs := make([]string, 0, 2)
	if workingDir != "" {
		searchDirs = append(searchDirs, workingDir)
	}
	if exePath != "" {
		exeDir := filepath.Dir(exePath)
		if exeDir != workingDir {
			searchDirs = append(searchDirs, exeDir)
		}
	}
	for _, dir := range searchDirs {
		p1 := filepath.Join(dir, "interface.json")
		if info, err := os.Stat(p1); err == nil && !info.IsDir() {
			return p1, dir
		}
		p2 := filepath.Join(dir, "resource", "interface.json")
		if info, err := os.Stat(p2); err == nil && !info.IsDir() {
			return p2, dir
		}
	}
	return "", ""
}

// HasInterface 极轻量探测是否存在 interface.json，仅做 os.Stat，用于状态轮询，耗时 < 0.01ms
func HasInterface(workingDir, exePath string) bool {
	p, _ := findInterfacePath(workingDir, exePath)
	return p != ""
}

// loadFullPI 完整加载 interface.json 并递归合并由 import 引入的子任务与选项
func loadFullPI(interfacePath string) (*PIFile, error) {
	data, err := os.ReadFile(interfacePath)
	if err != nil {
		return nil, fmt.Errorf("读取 interface.json 失败: %w", err)
	}

	var pi PIFile
	if err := json.Unmarshal(data, &pi); err != nil {
		return nil, fmt.Errorf("解析 interface.json 失败: %w", err)
	}

	baseDir := filepath.Dir(interfacePath)
	if len(pi.Import) > 0 {
		for _, imp := range pi.Import {
			impPath := filepath.Join(baseDir, imp)
			subData, err := os.ReadFile(impPath)
			if err != nil {
				continue
			}
			var subPI struct {
				Task   []PITask       `json:"task"`
				Option map[string]any `json:"option"`
			}
			if err := json.Unmarshal(subData, &subPI); err == nil {
				if len(subPI.Task) > 0 {
					pi.Task = append(pi.Task, subPI.Task...)
				}
				if subPI.Option != nil {
					if pi.Option == nil {
						pi.Option = make(map[string]any)
					}
					for k, v := range subPI.Option {
						pi.Option[k] = v
					}
				}
			}
		}
	}

	return &pi, nil
}

// OptionCase select 类型的候选项
type OptionCase struct {
	Name  string `json:"name"`  // 选项内部值，如 "None", "Donate10", "Hard"
	Label string `json:"label"` // 显示标签
}

// SubOptionItem 任务下的子选项定义与当前状态
type SubOptionItem struct {
	Name        string       `json:"name"`                // 选项唯一标识/键名
	Label       string       `json:"label"`               // 显示标签
	Description string       `json:"description"`         // 选项详细说明
	Type        string       `json:"type"`                // 选项类型: "switch", "select", "input"
	Value       any          `json:"value"`               // 当前值 (switch: bool; select: string; input: string)
	Cases       []OptionCase `json:"cases,omitempty"`     // select 时的候选项
	InputKey    string       `json:"input_key,omitempty"` // input 时的键名 (如 "mumu_path")
}

// SubOptionUpdate 前端提交的子选项值修改
type SubOptionUpdate struct {
	Name     string `json:"name"`                // 选项名
	Type     string `json:"type,omitempty"`      // "switch", "select", "input"
	Value    any    `json:"value"`               // 当前值 (bool 或 string)
	InputKey string `json:"input_key,omitempty"` // input 键名
}

// TaskItem 返回给前端展示的单个子任务项
type TaskItem struct {
	Name         string          `json:"name"`
	Label        string          `json:"label"`
	Description  string          `json:"description"`
	DefaultCheck bool            `json:"default_check"`
	Enabled      bool            `json:"enabled"`
	Options      []string        `json:"options,omitempty"`
	SubOptions   []SubOptionItem `json:"sub_options,omitempty"`
}

// ConfigProfile MAA 项目中的单个配置预设/实例
type ConfigProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
}

// ConfigView 前端配置视图
type ConfigView struct {
	IsMAA          bool            `json:"is_maa"`
	ProjectName    string          `json:"project_name"`
	Version        string          `json:"version"`
	ConfigType     string          `json:"config_type"`
	ActiveConfigID string          `json:"active_config_id"`
	Profiles       []ConfigProfile `json:"profiles"`
	Tasks          []TaskItem      `json:"tasks"`
}

// TaskUpdateItem 前端提交的单个子任务启用状态及子选项变更
type TaskUpdateItem struct {
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	SubOptions []SubOptionUpdate `json:"sub_options,omitempty"`
}

// TaskUpdateRequest 前端提交的子任务启用状态变更请求
type TaskUpdateRequest struct {
	ConfigID string           `json:"config_id,omitempty"` // 选中的配置预设 ID
	Tasks    []TaskUpdateItem `json:"tasks"`
}

// DetectProject 检测指定工作目录或可执行文件目录下是否存在 MAA 项目与 interface.json
func DetectProject(workingDir, exePath string) (*ProjectInfo, error) {
	interfacePath, baseDir := findInterfacePath(workingDir, exePath)
	if interfacePath == "" {
		return nil, fmt.Errorf("未检测到 interface.json")
	}

	data, err := os.ReadFile(interfacePath)
	if err != nil {
		return nil, fmt.Errorf("读取 interface.json 失败: %w", err)
	}

	var pi PIFile
	if err := json.Unmarshal(data, &pi); err != nil {
		return nil, fmt.Errorf("解析 interface.json 失败: %w", err)
	}

	projectName := pi.Name
	if projectName == "" {
		projectName = filepath.Base(baseDir)
	}

	info := &ProjectInfo{
		InterfacePath: interfacePath,
		ProjectName:   projectName,
		Version:       pi.Version,
		BaseDir:       baseDir,
	}

	// 查找配置文件
	// 1. 优先检查 MXU 配置文件: config/mxu-{projectName}.json
	mxuPath := filepath.Join(baseDir, "config", fmt.Sprintf("mxu-%s.json", projectName))
	if f, err := os.Stat(mxuPath); err == nil && !f.IsDir() {
		info.ConfigPath = mxuPath
		info.ConfigType = "mxu"
		return info, nil
	}
	if matches, err := filepath.Glob(filepath.Join(baseDir, "config", "mxu-*.json")); err == nil && len(matches) > 0 {
		info.ConfigPath = matches[0]
		info.ConfigType = "mxu"
		return info, nil
	}

	// 2. 检查 MFAA (PySide6 / QFluentWidgets 官方多配置系统): config/multi_config.json
	multiCfgPath := filepath.Join(baseDir, "config", "multi_config.json")
	if f, err := os.Stat(multiCfgPath); err == nil && !f.IsDir() {
		if mcData, err := os.ReadFile(multiCfgPath); err == nil {
			var mc struct {
				CurrConfigID string   `json:"curr_config_id"`
				ConfigList   []string `json:"config_list"`
			}
			if json.Unmarshal(mcData, &mc) == nil {
				targetID := mc.CurrConfigID
				if targetID == "" && len(mc.ConfigList) > 0 {
					targetID = mc.ConfigList[0]
				}
				if targetID != "" {
					targetCfg := filepath.Join(baseDir, "config", "configs", targetID+".json")
					if cf, err := os.Stat(targetCfg); err == nil && !cf.IsDir() {
						info.ConfigPath = targetCfg
						info.ConfigType = "mfaa"
						return info, nil
					}
				}
			}
		}
	}

	// 3. 检查标准 MaaFramework PI 配置: config/maa_pi_config.json
	piPath := filepath.Join(baseDir, "config", "maa_pi_config.json")
	if f, err := os.Stat(piPath); err == nil && !f.IsDir() {
		info.ConfigPath = piPath
		info.ConfigType = "pi"
		return info, nil
	}

	// 4. 通用兜底: config/config.json 或 config.json
	cfgPath := filepath.Join(baseDir, "config", "config.json")
	if f, err := os.Stat(cfgPath); err == nil && !f.IsDir() {
		info.ConfigPath = cfgPath
		info.ConfigType = "generic"
		return info, nil
	}
	cfgPath2 := filepath.Join(baseDir, "config.json")
	if f, err := os.Stat(cfgPath2); err == nil && !f.IsDir() {
		info.ConfigPath = cfgPath2
		info.ConfigType = "generic"
		return info, nil
	}

	return info, nil
}

func isCaseTrue(val string) bool {
	return strings.EqualFold(val, "Yes") || strings.EqualFold(val, "true") || strings.EqualFold(val, "on") || val == "开启"
}

// updateInputValue follows the reader's precedence while preserving unrelated fields.
func updateInputValue(option map[string]any, key, value string) {
	if values, ok := option["values"].(map[string]any); ok {
		values[key] = value
	} else if _, ok := option["value"]; ok {
		option["value"] = value
	} else {
		values := make(map[string]any)
		option["values"] = values
		values[key] = value
	}
}

func isBooleanCases(c1, c2 string) bool {
	pair := strings.ToLower(c1) + "|" + strings.ToLower(c2)
	switch pair {
	case "yes|no", "no|yes",
		"true|false", "false|true",
		"on|off", "off|on",
		"开启|关闭", "关闭|开启":
		return true
	}
	return false
}

// GetConfigView 解析 interface.json 与当前生效配置，生成前端所需的视图
// targetConfigID 指定要查看/编辑的配置预设 ID，若为空则自动读取当前生效的活跃预设
func GetConfigView(info *ProjectInfo, targetConfigID string) (*ConfigView, error) {
	if info == nil || info.InterfacePath == "" {
		return nil, fmt.Errorf("无效的 MAA 项目信息")
	}

	pi, err := loadFullPI(info.InterfacePath)
	if err != nil {
		return nil, fmt.Errorf("加载 interface.json 失败: %w", err)
	}

	// 尝试加载多语言本地化文件（如 zh_cn）
	localeMap := make(map[string]string)
	if pi.Languages != nil {
		zhRelPath := ""
		for langKey, path := range pi.Languages {
			normKey := strings.ToLower(strings.ReplaceAll(langKey, "_", "-"))
			if normKey == "zh-cn" || normKey == "zh" || strings.HasPrefix(normKey, "zh") {
				zhRelPath = path
				break
			}
		}
		if zhRelPath == "" {
			for _, path := range pi.Languages {
				zhRelPath = path
				break
			}
		}
		if zhRelPath != "" {
			baseDir := filepath.Dir(info.InterfacePath)
			if locData, err := os.ReadFile(filepath.Join(baseDir, zhRelPath)); err == nil {
				_ = json.Unmarshal(locData, &localeMap)
			}
		}
	}

	var profiles []ConfigProfile
	activeConfigID := targetConfigID
	enabledMap := make(map[string]bool)
	hasRuntimeConfig := false
	taskOptionValues := make(map[string]map[string]any)

	if info.ConfigPath != "" {
		if info.ConfigType == "mxu" {
			cfgData, err := os.ReadFile(info.ConfigPath)
			if err == nil {
				var mxuCfg struct {
					Instances            []map[string]any `json:"instances"`
					LastActiveInstanceID string           `json:"lastActiveInstanceId"`
				}
				if json.Unmarshal(cfgData, &mxuCfg) == nil && len(mxuCfg.Instances) > 0 {
					currentDiskActive := mxuCfg.LastActiveInstanceID
					if currentDiskActive == "" {
						if firstID, ok := mxuCfg.Instances[0]["id"].(string); ok {
							currentDiskActive = firstID
						}
					}

					if activeConfigID == "" {
						activeConfigID = currentDiskActive
					}

					var targetInst map[string]any
					for _, inst := range mxuCfg.Instances {
						id, _ := inst["id"].(string)
						name, _ := inst["name"].(string)
						if name == "" {
							name = id
						}
						profiles = append(profiles, ConfigProfile{
							ID:       id,
							Name:     name,
							IsActive: (id == currentDiskActive),
						})
						if id == activeConfigID {
							targetInst = inst
						}
					}

					if targetInst == nil && len(mxuCfg.Instances) > 0 {
						targetInst = mxuCfg.Instances[0]
						if id, ok := targetInst["id"].(string); ok {
							activeConfigID = id
						}
					}

					if targetInst != nil {
						if tasksRaw, ok := targetInst["tasks"].([]any); ok {
							hasRuntimeConfig = true
							for _, tRaw := range tasksRaw {
								if tMap, ok := tRaw.(map[string]any); ok {
									tName, _ := tMap["taskName"].(string)
									tEnabled, okEn := tMap["enabled"].(bool)
									optVals, _ := tMap["optionValues"].(map[string]any)
									if tName != "" {
										if optVals != nil {
											taskOptionValues[tName] = optVals
										}
										if okEn {
											enabledMap[tName] = tEnabled
										}
									}
								}
							}
						}
					}
				}
			}
		} else if info.ConfigType == "mfaa" {
			// 读取 multi_config.json 获取配置列表与当前活跃配置
			multiCfgPath := filepath.Join(info.BaseDir, "config", "multi_config.json")
			var mc struct {
				CurrConfigID string   `json:"curr_config_id"`
				ConfigList   []string `json:"config_list"`
			}
			if mcData, err := os.ReadFile(multiCfgPath); err == nil {
				_ = json.Unmarshal(mcData, &mc)
			}

			currentDiskActive := mc.CurrConfigID
			if currentDiskActive == "" && len(mc.ConfigList) > 0 {
				currentDiskActive = mc.ConfigList[0]
			}

			if activeConfigID == "" {
				activeConfigID = currentDiskActive
			}

			for _, cid := range mc.ConfigList {
				cfgPath := filepath.Join(info.BaseDir, "config", "configs", cid+".json")
				cfgName := cid
				if cData, err := os.ReadFile(cfgPath); err == nil {
					var item struct {
						Name string `json:"name"`
					}
					if json.Unmarshal(cData, &item) == nil && item.Name != "" {
						cfgName = item.Name
					}
				}
				profiles = append(profiles, ConfigProfile{
					ID:       cid,
					Name:     cfgName,
					IsActive: (cid == currentDiskActive),
				})
			}

			// 读取选定配置文件的 tasks
			targetCfgPath := filepath.Join(info.BaseDir, "config", "configs", activeConfigID+".json")
			if _, err := os.Stat(targetCfgPath); err != nil && info.ConfigPath != "" {
				targetCfgPath = info.ConfigPath
			}
			if cfgData, err := os.ReadFile(targetCfgPath); err == nil {
				var mfaaCfg struct {
					Tasks []struct {
						Name       string         `json:"name"`
						IsChecked  bool           `json:"is_checked"`
						TaskOption map[string]any `json:"task_option"`
					} `json:"tasks"`
				}
				if json.Unmarshal(cfgData, &mfaaCfg) == nil {
					hasRuntimeConfig = true
					for _, t := range mfaaCfg.Tasks {
						if t.Name != "" {
							enabledMap[t.Name] = t.IsChecked
							if t.TaskOption != nil {
								taskOptionValues[t.Name] = t.TaskOption
							}
						}
					}
				}
			}
		} else if info.ConfigType == "pi" {
			profiles = append(profiles, ConfigProfile{
				ID:       "default",
				Name:     "默认配置",
				IsActive: true,
			})
			activeConfigID = "default"
			if cfgData, err := os.ReadFile(info.ConfigPath); err == nil {
				var piCfg struct {
					Task []struct {
						Name   string         `json:"name"`
						Option map[string]any `json:"option"`
					} `json:"task"`
				}
				if json.Unmarshal(cfgData, &piCfg) == nil {
					hasRuntimeConfig = true
					for _, t := range piCfg.Task {
						if t.Name != "" {
							enabledMap[t.Name] = true
							if t.Option != nil {
								taskOptionValues[t.Name] = t.Option
							}
						}
					}
				}
			}
		}
	}

	taskList := make([]TaskItem, 0, len(pi.Task))
	for _, t := range pi.Task {
		displayName := t.Label
		if strings.HasPrefix(displayName, "$") {
			key := strings.TrimPrefix(displayName, "$")
			if locVal, ok := localeMap[key]; ok {
				displayName = locVal
			} else {
				displayName = t.Name
			}
		}
		if displayName == "" {
			displayName = t.Name
		}

		desc := t.Description
		if strings.HasPrefix(desc, "$") {
			key := strings.TrimPrefix(desc, "$")
			if locVal, ok := localeMap[key]; ok {
				desc = locVal
			} else {
				desc = ""
			}
		}

		enabled := t.DefaultCheck
		if hasRuntimeConfig {
			if val, exists := enabledMap[t.Name]; exists {
				enabled = val
			} else {
				// 如果运行时配置中存在任务列表但该任务不在列表中，则视为禁用
				enabled = false
			}
		}

		// 解析关联的子选项
		var subOptions []SubOptionItem
		if len(t.Option) > 0 && pi.Option != nil {
			rtVals := taskOptionValues[t.Name]
			for _, optKey := range t.Option {
				optDefRaw, exists := pi.Option[optKey]
				if !exists {
					continue
				}
				optDef, ok := optDefRaw.(map[string]any)
				if !ok {
					continue
				}

				var casesList []OptionCase
				if rawCases, ok := optDef["cases"].([]any); ok {
					for _, rc := range rawCases {
						switch c := rc.(type) {
						case string:
							cLabel := c
							if strings.HasPrefix(cLabel, "$") {
								if locVal, ok := localeMap[strings.TrimPrefix(cLabel, "$")]; ok {
									cLabel = locVal
								}
							}
							casesList = append(casesList, OptionCase{Name: c, Label: cLabel})
						case map[string]any:
							name, _ := c["name"].(string)
							label, _ := c["label"].(string)
							if strings.HasPrefix(label, "$") {
								if locVal, ok := localeMap[strings.TrimPrefix(label, "$")]; ok {
									label = locVal
								}
							}
							if label == "" {
								label = name
							}
							if name != "" {
								casesList = append(casesList, OptionCase{Name: name, Label: label})
							}
						}
					}
				}

				inputsRaw, _ := optDef["inputs"].([]any)

				optType, _ := optDef["type"].(string)
				finalType := ""
				switch optType {
				case "switch":
					finalType = "switch"
				case "select":
					finalType = "select"
				case "input":
					finalType = "input"
				default:
					if len(inputsRaw) > 0 {
						finalType = "input"
					} else if len(casesList) > 0 {
						if len(casesList) == 2 && isBooleanCases(casesList[0].Name, casesList[1].Name) {
							finalType = "switch"
						} else {
							finalType = "select"
						}
					}
				}

				if finalType == "" {
					continue
				}

				subLabel, _ := optDef["label"].(string)
				if strings.HasPrefix(subLabel, "$") {
					if locVal, ok := localeMap[strings.TrimPrefix(subLabel, "$")]; ok {
						subLabel = locVal
					}
				}
				if subLabel == "" {
					subLabel = optKey
				}

				subDesc, _ := optDef["description"].(string)
				if strings.HasPrefix(subDesc, "$") {
					if locVal, ok := localeMap[strings.TrimPrefix(subDesc, "$")]; ok {
						subDesc = locVal
					}
				}

				defaultCase, _ := optDef["default_case"].(string)
				inputKey := ""
				var currentVal any

				switch finalType {
				case "switch":
					valBool := isCaseTrue(defaultCase)
					if rtVals != nil {
						if optValRaw, exists := rtVals[optKey]; exists {
							switch v := optValRaw.(type) {
							case bool:
								valBool = v
							case string:
								valBool = isCaseTrue(v)
							case map[string]any:
								if innerVal, ok := v["value"]; ok {
									switch iv := innerVal.(type) {
									case bool:
										valBool = iv
									case string:
										valBool = isCaseTrue(iv)
									}
								}
							}
						}
					}
					currentVal = valBool

				case "select":
					valStr := defaultCase
					if valStr == "" && len(casesList) > 0 {
						valStr = casesList[0].Name
					}
					if rtVals != nil {
						if optValRaw, exists := rtVals[optKey]; exists {
							switch v := optValRaw.(type) {
							case string:
								valStr = v
							case map[string]any:
								if cn, ok := v["caseName"].(string); ok && cn != "" {
									valStr = cn
								} else if iv, ok := v["value"].(string); ok && iv != "" {
									valStr = iv
								}
							}
						}
					}
					currentVal = valStr

				case "input":
					defaultVal := ""
					if len(inputsRaw) > 0 {
						if firstInput, ok := inputsRaw[0].(map[string]any); ok {
							inputKey, _ = firstInput["name"].(string)
							defaultVal, _ = firstInput["default"].(string)
						}
					}
					if inputKey == "" {
						inputKey = optKey
					}
					valStr := defaultVal
					if rtVals != nil {
						if optValRaw, exists := rtVals[optKey]; exists {
							switch v := optValRaw.(type) {
							case string:
								valStr = v
							case map[string]any:
								if valsMap, ok := v["values"].(map[string]any); ok {
									if iv, ok := valsMap[inputKey].(string); ok {
										valStr = iv
									}
								} else if iv, ok := v["value"].(string); ok {
									valStr = iv
								}
							}
						}
					}
					currentVal = valStr
				}

				subOptions = append(subOptions, SubOptionItem{
					Name:        optKey,
					Label:       subLabel,
					Description: subDesc,
					Type:        finalType,
					Value:       currentVal,
					Cases:       casesList,
					InputKey:    inputKey,
				})
			}
		}

		taskList = append(taskList, TaskItem{
			Name:         t.Name,
			Label:        displayName,
			Description:  desc,
			DefaultCheck: t.DefaultCheck,
			Enabled:      enabled,
			Options:      t.Option,
			SubOptions:   subOptions,
		})
	}

	return &ConfigView{
		IsMAA:          true,
		ProjectName:    info.ProjectName,
		Version:        info.Version,
		ConfigType:     info.ConfigType,
		ActiveConfigID: activeConfigID,
		Profiles:       profiles,
		Tasks:          taskList,
	}, nil
}

// SaveConfig 将前端修改的子任务启用状态与子选项外科手术式更新回本地配置文件
func SaveConfig(info *ProjectInfo, req *TaskUpdateRequest) error {
	if info == nil || info.ConfigPath == "" {
		return fmt.Errorf("未找到对应的 MAA 配置文件，无法保存")
	}
	if req == nil {
		return fmt.Errorf("请求体为空")
	}

	type taskUpdateData struct {
		enabled    bool
		subOptions map[string]SubOptionUpdate
	}
	targetMap := make(map[string]taskUpdateData)
	for _, item := range req.Tasks {
		subMap := make(map[string]SubOptionUpdate)
		for _, sub := range item.SubOptions {
			subMap[sub.Name] = sub
		}
		targetMap[item.Name] = taskUpdateData{
			enabled:    item.Enabled,
			subOptions: subMap,
		}
	}

	if info.ConfigType == "mxu" {
		cfgData, err := os.ReadFile(info.ConfigPath)
		if err != nil {
			return fmt.Errorf("读取 MXU 配置文件失败: %w", err)
		}
		var root map[string]any
		if err := json.Unmarshal(cfgData, &root); err != nil {
			return fmt.Errorf("解析 MXU 配置文件失败: %w", err)
		}

		instancesRaw, ok := root["instances"].([]any)
		if !ok || len(instancesRaw) == 0 {
			return fmt.Errorf("MXU 配置文件缺少 instances 节点")
		}

		targetID := req.ConfigID
		if targetID == "" {
			targetID, _ = root["lastActiveInstanceId"].(string)
			if targetID == "" && len(instancesRaw) > 0 {
				if instMap, ok := instancesRaw[0].(map[string]any); ok {
					targetID, _ = instMap["id"].(string)
				}
			}
		}

		targetIdx := -1
		for i, instRaw := range instancesRaw {
			if instMap, ok := instRaw.(map[string]any); ok {
				if id, ok := instMap["id"].(string); ok && id == targetID {
					targetIdx = i
					break
				}
			}
		}

		if targetIdx == -1 {
			return fmt.Errorf("未找到指定的 MXU 配置预设: %s", targetID)
		}

		root["lastActiveInstanceId"] = targetID

		targetInst, ok := instancesRaw[targetIdx].(map[string]any)
		if !ok {
			return fmt.Errorf("MXU 实例格式错误")
		}

		tasksRaw, ok := targetInst["tasks"].([]any)
		if !ok {
			return fmt.Errorf("MXU 实例缺少 tasks 列表")
		}

		// 更新已有任务的 enabled 与 optionValues
		for _, tRaw := range tasksRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				tName, _ := tMap["taskName"].(string)
				if update, found := targetMap[tName]; found {
					tMap["enabled"] = update.enabled
					if ctrlMap, ok := tMap["enabledByController"].(map[string]any); ok {
						for k := range ctrlMap {
							ctrlMap[k] = update.enabled
						}
					}
					if len(update.subOptions) > 0 {
						optVals, ok := tMap["optionValues"].(map[string]any)
						if !ok || optVals == nil {
							optVals = make(map[string]any)
							tMap["optionValues"] = optVals
						}
						for optName, sub := range update.subOptions {
							existingOpt, _ := optVals[optName].(map[string]any)
							if existingOpt == nil {
								existingOpt = make(map[string]any)
								optVals[optName] = existingOpt
							}
							switch sub.Type {
							case "switch":
								bVal := false
								if b, ok := sub.Value.(bool); ok {
									bVal = b
								} else if s, ok := sub.Value.(string); ok {
									bVal = isCaseTrue(s)
								}
								existingOpt["type"] = "switch"
								existingOpt["value"] = bVal
							case "select":
								sVal, _ := sub.Value.(string)
								existingOpt["type"] = "select"
								existingOpt["caseName"] = sVal
							case "input":
								sVal, _ := sub.Value.(string)
								existingOpt["type"] = "input"
								valsMap, ok := existingOpt["values"].(map[string]any)
								if !ok || valsMap == nil {
									valsMap = make(map[string]any)
									existingOpt["values"] = valsMap
								}
								key := sub.InputKey
								if key == "" {
									key = optName
								}
								updateInputValue(existingOpt, key, sVal)
							default:
								if b, ok := sub.Value.(bool); ok {
									existingOpt["type"] = "switch"
									existingOpt["value"] = b
								} else if s, ok := sub.Value.(string); ok {
									existingOpt["type"] = "select"
									existingOpt["caseName"] = s
								}
							}
						}
					}
				}
			}
		}

		// 格式化写回文件
		newData, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化更新后的 MXU 配置失败: %w", err)
		}
		return os.WriteFile(info.ConfigPath, newData, 0644)
	}

	if info.ConfigType == "mfaa" {
		targetID := req.ConfigID
		multiCfgPath := filepath.Join(info.BaseDir, "config", "multi_config.json")
		if targetID == "" {
			if mcData, err := os.ReadFile(multiCfgPath); err == nil {
				var mc struct {
					CurrConfigID string   `json:"curr_config_id"`
					ConfigList   []string `json:"config_list"`
				}
				if json.Unmarshal(mcData, &mc) == nil {
					targetID = mc.CurrConfigID
					if targetID == "" && len(mc.ConfigList) > 0 {
						targetID = mc.ConfigList[0]
					}
				}
			}
		}

		if targetID == "" {
			return fmt.Errorf("未指定且未检测到有效的 MFAA 配置预设")
		}

		// 严格校验预设配置文件是否存在，禁止改错或回退覆盖其他配置
		targetCfgPath := filepath.Join(info.BaseDir, "config", "configs", targetID+".json")
		if fi, err := os.Stat(targetCfgPath); err != nil || fi.IsDir() {
			return fmt.Errorf("未找到指定的 MFAA 预设配置文件: %s", targetID)
		}

		// 1. 更新 multi_config.json 中的 curr_config_id
		if mcData, err := os.ReadFile(multiCfgPath); err == nil {
			var mcRoot map[string]any
			if json.Unmarshal(mcData, &mcRoot) == nil {
				mcRoot["curr_config_id"] = targetID
				if newMC, err := json.MarshalIndent(mcRoot, "", "    "); err == nil {
					_ = os.WriteFile(multiCfgPath, newMC, 0644)
				}
			}
		}

		// 2. 更新对应 config/configs/{targetID}.json 中的 tasks 与 task_option
		cfgData, err := os.ReadFile(targetCfgPath)
		if err != nil {
			return fmt.Errorf("读取 MFAA 配置文件失败: %w", err)
		}

		var root map[string]any
		if err := json.Unmarshal(cfgData, &root); err != nil {
			return fmt.Errorf("解析 MFAA 配置文件失败: %w", err)
		}

		tasksRaw, ok := root["tasks"].([]any)
		if !ok || len(tasksRaw) == 0 {
			return fmt.Errorf("MFAA 配置文件缺少 tasks 列表")
		}

		for _, tRaw := range tasksRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				tName, _ := tMap["name"].(string)
				if update, found := targetMap[tName]; found {
					tMap["is_checked"] = update.enabled
					if len(update.subOptions) > 0 {
						taskOpt, ok := tMap["task_option"].(map[string]any)
						if !ok || taskOpt == nil {
							taskOpt = make(map[string]any)
							tMap["task_option"] = taskOpt
						}
						for optName, sub := range update.subOptions {
							existingOpt, _ := taskOpt[optName].(map[string]any)
							if existingOpt == nil {
								existingOpt = make(map[string]any)
								taskOpt[optName] = existingOpt
							}
							switch sub.Type {
							case "switch":
								bVal := false
								if b, ok := sub.Value.(bool); ok {
									bVal = b
								} else if s, ok := sub.Value.(string); ok {
									bVal = isCaseTrue(s)
								}
								caseVal := "No"
								if bVal {
									caseVal = "Yes"
								}
								existingOpt["value"] = caseVal
							case "select":
								sVal, _ := sub.Value.(string)
								existingOpt["value"] = sVal
							case "input":
								sVal, _ := sub.Value.(string)
								key := sub.InputKey
								if key == "" {
									key = optName
								}
								updateInputValue(existingOpt, key, sVal)
							default:
								if b, ok := sub.Value.(bool); ok {
									caseVal := "No"
									if b {
										caseVal = "Yes"
									}
									existingOpt["value"] = caseVal
								} else if s, ok := sub.Value.(string); ok {
									existingOpt["value"] = s
								}
							}
						}
					}
				}
			}
		}

		newData, err := json.MarshalIndent(root, "", "    ")
		if err != nil {
			return fmt.Errorf("序列化更新后的 MFAA 配置失败: %w", err)
		}
		return os.WriteFile(targetCfgPath, newData, 0644)
	}

	if info.ConfigType == "pi" {
		cfgData, err := os.ReadFile(info.ConfigPath)
		if err != nil {
			return fmt.Errorf("读取配置文件失败: %w", err)
		}
		var root map[string]any
		if err := json.Unmarshal(cfgData, &root); err != nil {
			return fmt.Errorf("解析 PI 配置文件失败: %w", err)
		}

		// 1. 保留已有任务原始顺序，仅更新已有任务状态
		existingTaskNames := make(map[string]bool)
		var newTasks []any
		if oldTasksRaw, ok := root["task"].([]any); ok {
			for _, oldTRaw := range oldTasksRaw {
				if oldTMap, ok := oldTRaw.(map[string]any); ok {
					name, _ := oldTMap["name"].(string)
					if name == "" {
						continue
					}
					existingTaskNames[name] = true
					if update, found := targetMap[name]; found {
						if update.enabled {
							optMap, ok := oldTMap["option"].(map[string]any)
							if !ok || optMap == nil {
								optMap = make(map[string]any)
								oldTMap["option"] = optMap
							}
							for optName, sub := range update.subOptions {
								switch sub.Type {
								case "switch":
									bVal := false
									if b, ok := sub.Value.(bool); ok {
										bVal = b
									} else if s, ok := sub.Value.(string); ok {
										bVal = isCaseTrue(s)
									}
									optMap[optName] = bVal
								case "select":
									sVal, _ := sub.Value.(string)
									optMap[optName] = sVal
								case "input":
									sVal, _ := sub.Value.(string)
									if inMap, ok := optMap[optName].(map[string]any); ok {
										key := sub.InputKey
										if key == "" {
											key = optName
										}
										updateInputValue(inMap, key, sVal)
									} else {
										optMap[optName] = sVal
									}
								default:
									if b, ok := sub.Value.(bool); ok {
										optMap[optName] = b
									} else if s, ok := sub.Value.(string); ok {
										optMap[optName] = s
									}
								}
							}
							newTasks = append(newTasks, oldTMap)
						}
					} else {
						newTasks = append(newTasks, oldTMap)
					}
				}
			}
		}

		// 2. 将之前不在 oldTasksRaw 中但被新启用的任务追加到末尾
		for _, item := range req.Tasks {
			if item.Enabled && !existingTaskNames[item.Name] {
				optMap := make(map[string]any)
				if update, found := targetMap[item.Name]; found {
					for optName, sub := range update.subOptions {
						switch sub.Type {
						case "switch":
							bVal := false
							if b, ok := sub.Value.(bool); ok {
								bVal = b
							} else if s, ok := sub.Value.(string); ok {
								bVal = isCaseTrue(s)
							}
							optMap[optName] = bVal
						case "select", "input":
							sVal, _ := sub.Value.(string)
							optMap[optName] = sVal
						default:
							if b, ok := sub.Value.(bool); ok {
								optMap[optName] = b
							} else if s, ok := sub.Value.(string); ok {
								optMap[optName] = s
							}
						}
					}
				}
				newTasks = append(newTasks, map[string]any{
					"name":   item.Name,
					"option": optMap,
				})
			}
		}
		root["task"] = newTasks

		newData, err := json.MarshalIndent(root, "", "    ")
		if err != nil {
			return fmt.Errorf("序列化更新后的 PI 配置失败: %w", err)
		}
		return os.WriteFile(info.ConfigPath, newData, 0644)
	}

	return fmt.Errorf("暂不支持的配置类型: %s", info.ConfigType)
}

// EnsureActiveProfile 在启动任务前，确保 MAA 项目的本地活跃配置切换为指定的 targetConfigID
func EnsureActiveProfile(workingDir, exePath, targetConfigID string) error {
	if targetConfigID == "" {
		return nil
	}
	proj, err := DetectProject(workingDir, exePath)
	if err != nil {
		return err
	}
	if proj.ConfigType == "mxu" && proj.ConfigPath != "" {
		cfgData, err := os.ReadFile(proj.ConfigPath)
		if err != nil {
			return err
		}
		var root map[string]any
		if err := json.Unmarshal(cfgData, &root); err != nil {
			return err
		}

		// 验证 targetConfigID 必须存在
		instancesRaw, ok := root["instances"].([]any)
		if !ok || len(instancesRaw) == 0 {
			return fmt.Errorf("MXU 配置文件缺少 instances 列表")
		}
		found := false
		for _, instRaw := range instancesRaw {
			if instMap, ok := instRaw.(map[string]any); ok {
				if id, _ := instMap["id"].(string); id == targetConfigID {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("指定的 MXU 预设不存在: %s", targetConfigID)
		}

		if currID, _ := root["lastActiveInstanceId"].(string); currID != targetConfigID {
			root["lastActiveInstanceId"] = targetConfigID
			if newData, err := json.MarshalIndent(root, "", "  "); err == nil {
				return os.WriteFile(proj.ConfigPath, newData, 0644)
			}
		}
		return nil
	}
	if proj.ConfigType == "mfaa" && proj.BaseDir != "" {
		// 验证 targetConfigID 对应的预设配置文件是否存在
		targetCfg := filepath.Join(proj.BaseDir, "config", "configs", targetConfigID+".json")
		if fi, err := os.Stat(targetCfg); err != nil || fi.IsDir() {
			return fmt.Errorf("指定的 MFAA 预设不存在: %s", targetConfigID)
		}

		multiCfgPath := filepath.Join(proj.BaseDir, "config", "multi_config.json")
		mcData, err := os.ReadFile(multiCfgPath)
		if err != nil {
			return err
		}
		var mcRoot map[string]any
		if err := json.Unmarshal(mcData, &mcRoot); err != nil {
			return err
		}
		if currID, _ := mcRoot["curr_config_id"].(string); currID != targetConfigID {
			mcRoot["curr_config_id"] = targetConfigID
			if newData, err := json.MarshalIndent(mcRoot, "", "    "); err == nil {
				return os.WriteFile(multiCfgPath, newData, 0644)
			}
		}
		return nil
	}
	return nil
}
