package maa

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrEntryNotFound 指定 id 的 MXU 任务条目在配置文件中不存在（调用方据此映射 404）
var ErrEntryNotFound = errors.New("未找到指定的任务条目")

// ErrMXUInstanceNotFound 指定 id 的 MXU 实例（配置预设）在配置文件中不存在（调用方据此映射 400）
var ErrMXUInstanceNotFound = errors.New("未找到指定的 MXU 配置预设")

// 对同一配置文件的"读取—修改—写回"序列必须串行：并发的条目复制/配置保存/预设切换
// 否则后写者胜，先写者的修改会静默丢失。按文件路径键控，不同文件互不阻塞。
var (
	configFileLockMu sync.Mutex
	configFileLocks  = map[string]*sync.Mutex{}
)

// lockConfigFile 锁住指定配置文件，返回解锁函数
func lockConfigFile(path string) func() {
	key := configFileLockKey(path)
	configFileLockMu.Lock()
	mu, ok := configFileLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		configFileLocks[key] = mu
	}
	configFileLockMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// configFileLockKey canonicalizes aliases before choosing the per-file mutex.
// Windows paths are case-insensitive; preserve case on case-sensitive platforms.
func configFileLockKey(path string) string {
	key, err := filepath.Abs(path)
	if err != nil {
		key = path
	}
	key = filepath.Clean(key)
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = resolved
	}
	key = filepath.Clean(key)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

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
	Name        string       `json:"name"`                 // 选项唯一标识/键名
	Label       string       `json:"label"`                // 显示标签
	Description string       `json:"description"`          // 选项详细说明
	Type        string       `json:"type"`                 // 选项类型: "switch", "select", "input"
	Value       any          `json:"value"`                // 当前值 (switch: bool; select: string; input: string)
	Cases       []OptionCase `json:"cases,omitempty"`      // select 时的候选项
	InputKey    string       `json:"input_key,omitempty"`  // input 时的键名 (如 "mumu_path")
	DependsOn   string       `json:"depends_on,omitempty"` // 父选项键名，空表示顶层选项（MaaFramework case 级依赖选项）
	ShowWhen    []string     `json:"show_when,omitempty"`  // 父 case 的 name 列表，命中任一即显示
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
	EntryID      string          `json:"entry_id,omitempty"`     // MXU 实例内任务条目的唯一随机 id（pi/mfaa 分支留空）
	DisplayName  string          `json:"display_name,omitempty"` // 条目显示名（来自任务别名映射，空则不输出）
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
	EntryID    string            `json:"entry_id,omitempty"` // MXU 条目精确匹配；为空时回退按 taskName 匹配（旧兼容）
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

// mxuTaskEntry MXU 实例内的一条任务条目（instance.tasks[] 数组元素）
type mxuTaskEntry struct {
	id        string         // 条目唯一随机 id（缺失时为空）
	enabled   bool           // 条目 enabled 字段值
	enabledOK bool           // 条目是否显式声明了 enabled
	optVals   map[string]any // 条目自身的扁平 optionValues（可能为 nil）
}

// GetConfigView 解析 interface.json 与当前生效配置，生成前端所需的视图
// targetConfigID 指定要查看/编辑的配置预设 ID，若为空则自动读取当前生效的活跃预设
func GetConfigView(info *ProjectInfo, targetConfigID string) (*ConfigView, error) {
	return GetConfigViewWithAliases(info, targetConfigID, nil)
}

// GetConfigViewWithAliases 同 GetConfigView，并按 entryAliases（key=MXU 条目 id）为任务条目补充 display_name 显示名。
// 别名数据只存放在 AutoNexus 自身的 config.json 中，绝不写第三方文件。
func GetConfigViewWithAliases(info *ProjectInfo, targetConfigID string, entryAliases map[string]string) (*ConfigView, error) {
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
	// MXU 分支专用：按 interface.json 任务名归组的条目列表（同名任务允许多条目，逐条渲染）
	mxuEntries := make(map[string][]mxuTaskEntry)

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
									entryID, _ := tMap["id"].(string)
									if tName != "" {
										// 同名任务允许多条目（官方设计支持复制条目），逐条保留而非按 taskName 合并
										mxuEntries[tName] = append(mxuEntries[tName], mxuTaskEntry{
											id:        entryID,
											enabled:   tEnabled,
											enabledOK: okEn,
											optVals:   optVals,
										})
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
				cfgName, err := readMFAAProfileName(cfgPath, cid)
				if err != nil {
					// The view retains its existing ID fallback for unreadable files;
					// recovery uses the same naming rule but must surface read errors.
					cfgName = cid
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

		// MXU：按实例条目逐条渲染，同名任务的多条目各自独立（entry_id 各异、optionValues 互不覆盖）
		if info.ConfigType == "mxu" && hasRuntimeConfig {
			if entries := mxuEntries[t.Name]; len(entries) > 0 {
				for _, e := range entries {
					item := TaskItem{
						Name:         t.Name,
						Label:        displayName,
						Description:  desc,
						DefaultCheck: t.DefaultCheck,
						Enabled:      e.enabledOK && e.enabled,
						EntryID:      e.id,
						Options:      t.Option,
						SubOptions:   buildTaskSubOptions(pi, t, e.optVals, localeMap),
					}
					if e.id != "" {
						if alias, ok := entryAliases[e.id]; ok && alias != "" {
							item.DisplayName = alias
						}
					}
					taskList = append(taskList, item)
				}
				continue
			}
			// 运行时配置存在但该任务在实例条目中不存在：视为禁用（保持既有语义）
			taskList = append(taskList, TaskItem{
				Name:         t.Name,
				Label:        displayName,
				Description:  desc,
				DefaultCheck: t.DefaultCheck,
				Enabled:      false,
				Options:      t.Option,
				SubOptions:   buildTaskSubOptions(pi, t, nil, localeMap),
			})
			continue
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

		taskList = append(taskList, TaskItem{
			Name:         t.Name,
			Label:        displayName,
			Description:  desc,
			DefaultCheck: t.DefaultCheck,
			Enabled:      enabled,
			Options:      t.Option,
			SubOptions:   buildTaskSubOptions(pi, t, taskOptionValues[t.Name], localeMap),
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

// buildTaskSubOptions 渲染一个 interface.json 任务定义关联的全部子选项（含 case 级嵌套依赖选项）。
// rtVals 为条目自身的扁平 optionValues；chain 记录渲染链上已出现的选项键，防止循环引用导致重复展开。
func buildTaskSubOptions(pi *PIFile, t PITask, rtVals map[string]any, localeMap map[string]string) []SubOptionItem {
	var subOptions []SubOptionItem
	if len(t.Option) == 0 {
		return nil
	}
	chain := make(map[string]bool)
	for _, optKey := range t.Option {
		subOptions = append(subOptions, renderOptionWithDeps(pi, optKey, rtVals, localeMap, chain, "", nil)...)
	}
	return subOptions
}

// renderOptionWithDeps 渲染单个选项定义，并递归展开其 case 里嵌套声明的 "option" 依赖选项
// （MaaFramework 官方协议：case 级依赖选项，如 switch 的 "Yes" case 下挂更多选项）。
// dependsOn/showWhen 描述该选项与父选项的依赖关系（顶层选项两者皆空）。
// 防环：chain 中已出现的选项不再重复展开；同一任务内的每个选项键至多渲染一次。
func renderOptionWithDeps(pi *PIFile, optKey string, rtVals map[string]any, localeMap map[string]string, chain map[string]bool, dependsOn string, showWhen []string) []SubOptionItem {
	if chain[optKey] {
		return nil
	}
	optDefRaw, exists := pi.Option[optKey]
	if !exists {
		return nil
	}
	optDef, ok := optDefRaw.(map[string]any)
	if !ok {
		return nil
	}
	chain[optKey] = true

	var casesList []OptionCase
	// case 级嵌套依赖：按出现顺序收集 (case name -> 依赖选项键列表)
	type caseDeps struct {
		caseName string
		opts     []string
	}
	var nestedDeps []caseDeps
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
					if rawDeps, ok := c["option"].([]any); ok {
						deps := make([]string, 0, len(rawDeps))
						for _, d := range rawDeps {
							if ds, ok := d.(string); ok && ds != "" {
								deps = append(deps, ds)
							}
						}
						if len(deps) > 0 {
							nestedDeps = append(nestedDeps, caseDeps{caseName: name, opts: deps})
						}
					}
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
		return nil
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

	item := SubOptionItem{
		Name:        optKey,
		Label:       subLabel,
		Description: subDesc,
		Type:        finalType,
		Value:       currentVal,
		Cases:       casesList,
		InputKey:    inputKey,
		DependsOn:   dependsOn,
		ShowWhen:    showWhen,
	}
	items := []SubOptionItem{item}

	// 展开 case 级嵌套依赖选项：同一子选项被父选项的多个 case 引用时合并 show_when（命中任一即显示）
	if len(nestedDeps) > 0 {
		childOrder := make([]string, 0, len(nestedDeps))
		childWhen := make(map[string][]string)
		for _, cd := range nestedDeps {
			for _, dep := range cd.opts {
				if _, seen := childWhen[dep]; !seen {
					childOrder = append(childOrder, dep)
				}
				already := false
				for _, w := range childWhen[dep] {
					if w == cd.caseName {
						already = true
						break
					}
				}
				if !already {
					childWhen[dep] = append(childWhen[dep], cd.caseName)
				}
			}
		}
		for _, childKey := range childOrder {
			items = append(items, renderOptionWithDeps(pi, childKey, rtVals, localeMap, chain, optKey, childWhen[childKey])...)
		}
	}
	return items
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
	targetMap := make(map[string]taskUpdateData)        // 按 taskName 匹配（mfaa/pi 分支）
	mxuNameTargetMap := make(map[string]taskUpdateData) // MXU 旧兼容：仅未携带 entry_id 的更新项按 taskName 匹配
	entryTargetMap := make(map[string]taskUpdateData)   // MXU 条目 entry_id 精确匹配（多条目同名任务各自独立更新）
	for _, item := range req.Tasks {
		subMap := make(map[string]SubOptionUpdate)
		for _, sub := range item.SubOptions {
			subMap[sub.Name] = sub
		}
		data := taskUpdateData{
			enabled:    item.Enabled,
			subOptions: subMap,
		}
		targetMap[item.Name] = data
		if item.EntryID != "" {
			entryTargetMap[item.EntryID] = data
		} else {
			mxuNameTargetMap[item.Name] = data
		}
	}

	if info.ConfigType == "mxu" {
		// 分支内是完整"读取—修改—写回"，与条目复制/预设切换共享文件锁；所有路径均在本分支内 return
		unlock := lockConfigFile(info.ConfigPath)
		defer unlock()

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

		targetInst, found := findMXUInstance(instancesRaw, targetID)
		if !found {
			return errors.New(withAvailableSuffix(
				fmt.Sprintf("未找到指定的 MXU 配置预设: %s", targetID),
				summarizeMXUInstances(instancesRaw)))
		}

		root["lastActiveInstanceId"] = targetID

		tasksRaw, ok := targetInst["tasks"].([]any)
		if !ok {
			return fmt.Errorf("MXU 实例缺少 tasks 列表")
		}

		// 更新已有条目的 enabled 与 optionValues：
		// 更新项携带 entry_id 时按条目精确匹配（同名多条目互不覆盖）；仅当更新项未携带 entry_id 时回退按 taskName 匹配（旧兼容）
		for _, tRaw := range tasksRaw {
			if tMap, ok := tRaw.(map[string]any); ok {
				tName, _ := tMap["taskName"].(string)
				entryID, _ := tMap["id"].(string)
				update, found := taskUpdateData{}, false
				if entryID != "" {
					update, found = entryTargetMap[entryID]
				}
				if !found {
					update, found = mxuNameTargetMap[tName]
				}
				if found {
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
		// multi_config.json 的读改与 EnsureActiveProfile(MFAA) 共享文件锁
		unlockMulti := lockConfigFile(multiCfgPath)
		defer unlockMulti()
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
		// 分支内是完整"读取—修改—写回"，持文件锁到写回完成
		unlock := lockConfigFile(info.ConfigPath)
		defer unlock()

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
		// 活跃实例切换与条目复制/配置保存写同一文件，共享文件锁
		unlock := lockConfigFile(proj.ConfigPath)
		defer unlock()

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
		if _, found := findMXUInstance(instancesRaw, targetConfigID); !found {
			// 携带当前可用实例列表，便于一眼诊断"绑定失效"（ID 已被 MXU 重建换代）
			return errors.New(withAvailableSuffix(
				fmt.Sprintf("指定的 MXU 预设不存在: %s", targetConfigID),
				summarizeMXUInstances(instancesRaw)))
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
			return errors.New(withAvailableSuffix(
				fmt.Sprintf("指定的 MFAA 预设不存在: %s", targetConfigID),
				summarizeMFAAConfigs(filepath.Join(proj.BaseDir, "config", "configs"))))
		}

		multiCfgPath := filepath.Join(proj.BaseDir, "config", "multi_config.json")
		// multi_config.json 的读改与 SaveConfig(MFAA) 共享文件锁
		unlock := lockConfigFile(multiCfgPath)
		defer unlock()

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

// ResolveActiveProfile keeps a saved MAA binding usable after MAA regenerates profile IDs.
// It prefers the saved ID, then a unique exact name match, then the sole available profile.
// Ambiguous or empty profile sets remain errors. EnsureActiveProfile keeps its strict contract.
func ResolveActiveProfile(workingDir, exePath, targetID, targetName string) (string, string, bool, error) {
	proj, err := DetectProject(workingDir, exePath)
	if err != nil {
		return "", "", false, err
	}
	resolvedID, resolvedName := targetID, targetName
	if proj.ConfigType == "mxu" && proj.ConfigPath != "" {
		unlock := lockConfigFile(proj.ConfigPath)
		data, err := os.ReadFile(proj.ConfigPath)
		if err != nil {
			unlock()
			return "", "", false, err
		}
		var root map[string]any
		if err := json.Unmarshal(data, &root); err != nil {
			unlock()
			return "", "", false, err
		}
		unlock()
		instances, ok := root["instances"].([]any)
		if !ok || len(instances) == 0 {
			return "", "", false, fmt.Errorf("MXU 配置文件缺少 instances 列表")
		}
		if inst, found := findMXUInstance(instances, targetID); found {
			resolvedName, _ = inst["name"].(string)
		} else {
			matches := make([]map[string]any, 0, 1)
			for _, raw := range instances {
				inst, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				name, _ := inst["name"].(string)
				id, _ := inst["id"].(string)
				if id != "" && targetName != "" && name == targetName {
					matches = append(matches, inst)
				}
			}
			if len(matches) == 1 {
				resolvedID, _ = matches[0]["id"].(string)
				resolvedName = targetName
			} else if len(matches) > 1 {
				return "", "", false, fmt.Errorf("MAA 预设绑定名称 %q 对应多个 MXU 预设，无法自动重绑定（当前可用: %s）", targetName, summarizeMXUInstances(instances))
			} else if len(instances) == 1 {
				inst, ok := instances[0].(map[string]any)
				if !ok {
					return "", "", false, fmt.Errorf("MXU 配置文件包含无效实例")
				}
				resolvedID, _ = inst["id"].(string)
				resolvedName, _ = inst["name"].(string)
				if resolvedID == "" {
					return "", "", false, fmt.Errorf("MXU 配置文件中的唯一实例缺少 ID")
				}
			} else {
				return "", "", false, errors.New(withAvailableSuffix(fmt.Sprintf("指定的 MXU 预设不存在: %s", targetID), summarizeMXUInstances(instances)))
			}
		}
	} else if proj.ConfigType == "mfaa" && proj.BaseDir != "" {
		resolvedID, resolvedName, err = resolveMFAAProfile(proj.BaseDir, targetID, targetName)
		if err != nil {
			return "", "", false, err
		}
	} else {
		return targetID, targetName, false, nil
	}
	if resolvedID == "" {
		return "", "", false, fmt.Errorf("重新绑定 MAA 预设失败：新预设 ID 为空")
	}
	if err := EnsureActiveProfile(workingDir, exePath, resolvedID); err != nil {
		return "", "", false, err
	}
	return resolvedID, resolvedName, resolvedID != targetID, nil
}

type namedMFAAProfile struct {
	id   string
	name string
}

// resolveMFAAProfile follows GetConfigView's display-name rule: use the JSON name,
// falling back to the file ID. The stale-ID recovery path reads every candidate
// strictly so unreadable or malformed files cannot silently become non-matches.
func resolveMFAAProfile(baseDir, targetID, targetName string) (string, string, error) {
	configDir := filepath.Join(baseDir, "config")
	profilesDir := filepath.Join(configDir, "configs")
	multiPath := filepath.Join(configDir, "multi_config.json")
	unlock := lockConfigFile(multiPath)
	multiData, err := os.ReadFile(multiPath)
	if err != nil {
		unlock()
		return "", "", fmt.Errorf("读取 MFAA 多配置索引失败: %w", err)
	}
	var multi struct {
		ConfigList []string `json:"config_list"`
	}
	if err := json.Unmarshal(multiData, &multi); err != nil {
		unlock()
		return "", "", fmt.Errorf("解析 MFAA 多配置索引失败: %w", err)
	}
	unlock()

	// Keep the old ID authoritative when its file still exists, regardless of a
	// changed or duplicate display name. Still parse it to return the real name.
	if targetID != "" {
		if info, statErr := os.Stat(filepath.Join(profilesDir, targetID+".json")); statErr == nil {
			if info.IsDir() {
				return "", "", fmt.Errorf("指定的 MFAA 预设不是文件: %s", targetID)
			}
			name, err := readMFAAProfileName(filepath.Join(profilesDir, targetID+".json"), targetID)
			return targetID, name, err
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", statErr
		}
	}

	ids := multi.ConfigList
	if len(ids) == 0 {
		entries, err := os.ReadDir(profilesDir)
		if err != nil {
			return "", "", err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				ids = append(ids, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
			}
		}
	}
	profiles := make([]namedMFAAProfile, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		name, err := readMFAAProfileName(filepath.Join(profilesDir, id+".json"), id)
		if err != nil {
			return "", "", err
		}
		profiles = append(profiles, namedMFAAProfile{id: id, name: name})
	}
	if targetName != "" {
		var matches []namedMFAAProfile
		for _, profile := range profiles {
			if profile.name == targetName {
				matches = append(matches, profile)
			}
		}
		if len(matches) == 1 {
			return matches[0].id, matches[0].name, nil
		}
		if len(matches) > 1 {
			return "", "", fmt.Errorf("MAA 预设绑定名称 %q 对应多个 MFAA 预设，无法自动重绑定（当前可用: %s）", targetName, summarizeNamedMFAAProfiles(profiles))
		}
	}
	if len(profiles) == 1 {
		return profiles[0].id, profiles[0].name, nil
	}
	return "", "", errors.New(withAvailableSuffix(fmt.Sprintf("指定的 MFAA 预设不存在: %s", targetID), summarizeNamedMFAAProfiles(profiles)))
}

func readMFAAProfileName(path, id string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 MFAA 预设 %s 失败: %w", id, err)
	}
	var profile struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return "", fmt.Errorf("解析 MFAA 预设 %s 失败: %w", id, err)
	}
	if profile.Name == "" {
		return id, nil
	}
	return profile.Name, nil
}

func summarizeNamedMFAAProfiles(profiles []namedMFAAProfile) string {
	parts := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		parts = append(parts, profile.id+"="+profile.name)
	}
	return strings.Join(parts, ", ")
}

// summarizeMXUInstances 将 MXU 实例列表摘要为 "id=name" 形式（name 为空则只写 id），多项以 ", " 连接
func summarizeMXUInstances(instancesRaw []any) string {
	parts := make([]string, 0, len(instancesRaw))
	for _, instRaw := range instancesRaw {
		instMap, ok := instRaw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := instMap["id"].(string)
		if id == "" {
			continue
		}
		name, _ := instMap["name"].(string)
		if name == "" {
			parts = append(parts, id)
		} else {
			parts = append(parts, id+"="+name)
		}
	}
	return strings.Join(parts, ", ")
}

// summarizeMFAAConfigs 列出 MFAA config/configs 目录下可用预设（*.json 去扩展名，逗号分隔）；
// 目录不存在或无 .json 文件时返回空串（调用方保持原报错不变）
func summarizeMFAAConfigs(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		parts = append(parts, strings.TrimSuffix(name, filepath.Ext(name)))
	}
	return strings.Join(parts, ", ")
}

// withAvailableSuffix 在报错信息后追加「（当前可用: ...）」后缀；可用列表为空时不追加
func withAvailableSuffix(msg, available string) string {
	if available == "" {
		return msg
	}
	return fmt.Sprintf("%s（当前可用: %s）", msg, available)
}

// findMXUInstance 在 MXU 实例列表中查找指定 id 的实例
func findMXUInstance(instancesRaw []any, targetID string) (map[string]any, bool) {
	for _, instRaw := range instancesRaw {
		if instMap, ok := instRaw.(map[string]any); ok {
			if id, ok := instMap["id"].(string); ok && id == targetID {
				return instMap, true
			}
		}
	}
	return nil, false
}

// loadMXUInstance 读取 MXU 配置文件并定位实例；instanceID 为空时回退 lastActiveInstanceId / 首个实例。
// 返回根对象、实例列表与目标实例。
func loadMXUInstance(configPath, instanceID string) (map[string]any, []any, map[string]any, error) {
	cfgData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取 MXU 配置文件失败: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(cfgData, &root); err != nil {
		return nil, nil, nil, fmt.Errorf("解析 MXU 配置文件失败: %w", err)
	}
	instancesRaw, ok := root["instances"].([]any)
	if !ok || len(instancesRaw) == 0 {
		return nil, nil, nil, fmt.Errorf("MXU 配置文件缺少 instances 节点")
	}

	targetID := instanceID
	if targetID == "" {
		targetID, _ = root["lastActiveInstanceId"].(string)
		if targetID == "" {
			if instMap, ok := instancesRaw[0].(map[string]any); ok {
				targetID, _ = instMap["id"].(string)
			}
		}
	}
	inst, found := findMXUInstance(instancesRaw, targetID)
	if !found {
		return nil, nil, nil, fmt.Errorf("%w: %s%s",
			ErrMXUInstanceNotFound, targetID,
			withAvailableSuffix("", summarizeMXUInstances(instancesRaw)))
	}
	return root, instancesRaw, inst, nil
}

// mxuEntryIDChars MXU 任务条目 id 的字符集：7 位小写字母数字，与 MXU 自身生成的格式一致
const mxuEntryIDChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// generateMXUEntryID 生成实例内未占用的 7 位小写字母数字随机 id
func generateMXUEntryID(existing map[string]bool) string {
	buf := make([]byte, 7)
	for {
		if _, err := cryptorand.Read(buf); err != nil {
			// 随机源异常时以纳秒时钟兜底，仍保证 id 可用
			now := time.Now().UnixNano()
			for i := range buf {
				buf[i] = byte(now >> uint(i*8))
			}
		}
		for i := range buf {
			buf[i] = mxuEntryIDChars[int(buf[i])%len(mxuEntryIDChars)]
		}
		id := string(buf)
		if !existing[id] {
			return id
		}
	}
}

// TaskEntryExists 实时读取 MXU 配置文件，校验指定实例（空则取当前活跃实例）中是否存在指定 id 的任务条目。
// 仅支持 mxu 类型项目。
func TaskEntryExists(info *ProjectInfo, instanceID, entryID string) (bool, error) {
	if info == nil || info.ConfigPath == "" {
		return false, fmt.Errorf("未找到对应的 MAA 配置文件")
	}
	if info.ConfigType != "mxu" {
		return false, fmt.Errorf("任务条目操作仅支持 MXU 类型项目，当前类型: %s", info.ConfigType)
	}
	if entryID == "" {
		return false, nil
	}
	_, _, inst, err := loadMXUInstance(info.ConfigPath, instanceID)
	if err != nil {
		return false, err
	}
	tasksRaw, _ := inst["tasks"].([]any)
	for _, tRaw := range tasksRaw {
		if tMap, ok := tRaw.(map[string]any); ok {
			if id, _ := tMap["id"].(string); id == entryID {
				return true, nil
			}
		}
	}
	return false, nil
}

// DuplicateTaskEntry 在 MXU 实例的 tasks 数组中紧随原条目插入其深拷贝（新条目重新生成 7 位随机 id，
// enabledByController/expanded 等其余字段原样保留）。instanceID 为空时使用当前活跃实例。
// 这是唯一允许写入第三方 MXU 配置文件的条目级操作；返回新条目的 id。
// entryID 不存在时返回可被 errors.Is 识别为 ErrEntryNotFound 的错误。
func DuplicateTaskEntry(info *ProjectInfo, instanceID, entryID string) (string, error) {
	if info == nil || info.ConfigPath == "" {
		return "", fmt.Errorf("未找到对应的 MAA 配置文件，无法保存")
	}
	if info.ConfigType != "mxu" {
		return "", fmt.Errorf("任务条目操作仅支持 MXU 类型项目，当前类型: %s", info.ConfigType)
	}
	if entryID == "" {
		return "", fmt.Errorf("%w: entry_id 为空", ErrEntryNotFound)
	}

	// 复制是完整的"读取—修改—写回"，持文件锁到写回完成，并发复制互不覆盖
	unlock := lockConfigFile(info.ConfigPath)
	defer unlock()

	root, _, inst, err := loadMXUInstance(info.ConfigPath, instanceID)
	if err != nil {
		return "", err
	}
	tasksRaw, ok := inst["tasks"].([]any)
	if !ok {
		return "", fmt.Errorf("MXU 实例缺少 tasks 列表")
	}

	idx := -1
	for i, tRaw := range tasksRaw {
		if tMap, ok := tRaw.(map[string]any); ok {
			if id, _ := tMap["id"].(string); id == entryID {
				idx = i
				break
			}
		}
	}
	if idx == -1 {
		return "", fmt.Errorf("%w: %s", ErrEntryNotFound, entryID)
	}

	// 深拷贝原条目（经 JSON 序列化往返，enabledByController/expanded 等一并保留）
	rawCopy, err := json.Marshal(tasksRaw[idx])
	if err != nil {
		return "", fmt.Errorf("序列化任务条目失败: %w", err)
	}
	var newEntry map[string]any
	if err := json.Unmarshal(rawCopy, &newEntry); err != nil {
		return "", fmt.Errorf("解析任务条目副本失败: %w", err)
	}

	existing := make(map[string]bool, len(tasksRaw))
	for _, tRaw := range tasksRaw {
		if tMap, ok := tRaw.(map[string]any); ok {
			if id, _ := tMap["id"].(string); id != "" {
				existing[id] = true
			}
		}
	}
	newID := generateMXUEntryID(existing)
	newEntry["id"] = newID

	// 紧随原条目插入，保持其余条目顺序不变
	newTasks := make([]any, 0, len(tasksRaw)+1)
	newTasks = append(newTasks, tasksRaw[:idx+1]...)
	newTasks = append(newTasks, newEntry)
	newTasks = append(newTasks, tasksRaw[idx+1:]...)
	inst["tasks"] = newTasks

	newData, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化更新后的 MXU 配置失败: %w", err)
	}
	if err := os.WriteFile(info.ConfigPath, newData, 0644); err != nil {
		return "", err
	}
	return newID, nil
}
