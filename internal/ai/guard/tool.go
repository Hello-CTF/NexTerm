package guard

import (
	"encoding/json"
	"strings"
)

func ClassifyTool(name string, args json.RawMessage, config Config) Ruling {
	ruling := classifyTool(name, args, config)
	if name == "ask_user" || name == "todo_write" || name == "exit_plan_mode" {
		return ruling
	}
	if matchDangerRule(string(args), config.DangerRules) {
		return Worst(ruling, Dangerous("工具参数命中自定义危险规则"))
	}
	return ruling
}

func classifyTool(name string, args json.RawMessage, config Config) Ruling {
	config = config.Normalized()
	var fields map[string]json.RawMessage
	if len(args) != 0 {
		if err := json.Unmarshal(args, &fields); err != nil {
			return Confirm(KindUnknown, "工具参数不是有效 JSON 对象")
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	switch name {
	case "read_file", "list_dir", "search_files", "read_screen", "wait_for", "list_assets", "docker_ps", "docker_logs", "db_list_tables", "db_describe", "redis_scan", "ask_user", "todo_write", "exit_plan_mode":
		return Allow()
	case "write_file", "edit_file":
		if matchDangerRule(stringField(fields, "path"), config.DangerRules) {
			return Dangerous("文件路径命中自定义危险规则")
		}
		return Confirm(KindWriteFS, "写入或编辑文件")
	case "exec_commands":
		var commands []string
		if err := json.Unmarshal(fields["commands"], &commands); err != nil || len(commands) == 0 {
			return Confirm(KindUnknown, "命令列表无效")
		}
		result := Allow()
		for _, command := range commands {
			result = Worst(result, ClassifyCommand(command, config.DangerRules))
		}
		return result
	case "send_keys":
		return ClassifySendKeys(stringField(fields, "keys"), boolField(fields, "enter"), config.DangerRules)
	case "docker_exec":
		result := Confirm(KindDockerMutate, "容器内执行命令")
		if matchDangerRule(stringField(fields, "container_id"), config.DangerRules) {
			result = Worst(result, Dangerous("容器参数命中自定义危险规则"))
		}
		return Worst(result, ClassifyCommand(stringField(fields, "cmd"), config.DangerRules))
	case "docker_control":
		if stringField(fields, "action") == "rm" {
			return Dangerous("删除容器需要始终确认")
		}
		return Confirm(KindDockerMutate, "Docker 容器状态变更")
	case "db_query":
		return ClassifySQL(stringField(fields, "sql"), config.DangerRules)
	case "redis_command":
		var command []string
		if json.Unmarshal(fields["command"], &command) != nil {
			command = strings.Fields(stringField(fields, "command"))
		}
		return classifyRedis(command)
	default:
		return Confirm(KindUnknown, "未知工具")
	}
}

func ClassifySendKeys(keys string, enter bool, rules []string) Ruling {
	commands, controls, submits := decodeKeyActions(keys, enter)
	result := Allow()
	for _, control := range controls {
		lower := strings.ToLower(control)
		if strings.Contains(lower, "ctrl") || strings.Contains(lower, "c-") {
			result = Worst(result, Confirm(KindProcess, "终端控制键可能中断或改变程序"))
		}
	}
	if !submits {
		return result
	}
	if len(commands) == 0 {
		return Worst(result, Confirm(KindUnknown, "回车会提交终端中已有的命令"))
	}
	for _, command := range commands {
		if strings.TrimSpace(command) == "" {
			result = Worst(result, Confirm(KindUnknown, "回车会提交终端中已有的命令"))
			continue
		}
		result = Worst(result, ClassifyCommand(command, rules))
	}
	return result
}

func decodeKeyActions(keys string, enter bool) ([]string, []string, bool) {
	var commands []string
	var controls []string
	var current strings.Builder
	submits := enter
	runes := []rune(keys)
	flush := func() {
		commands = append(commands, current.String())
		current.Reset()
	}
	for i := 0; i < len(runes); i++ {
		if runes[i] == '<' {
			if end := indexRune(runes[i+1:], '>'); end >= 0 {
				end += i + 1
				tag := string(runes[i+1 : end])
				lower := strings.ToLower(strings.TrimSpace(tag))
				switch lower {
				case "enter", "return", "ctrl+m", "ctrl+j", "c-m", "c-j":
					submits = true
					flush()
				case "lt":
					current.WriteRune('<')
				case "space":
					current.WriteRune(' ')
				case "tab":
					current.WriteRune('\t')
				default:
					if terminalActionTag(lower) {
						controls = append(controls, tag)
					} else {
						current.WriteString("<" + tag + ">")
					}
				}
				i = end
				continue
			}
		}
		if runes[i] == '\r' || runes[i] == '\n' {
			submits = true
			flush()
			if runes[i] == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			continue
		}
		current.WriteRune(runes[i])
	}
	if current.Len() != 0 {
		if submits {
			commands = append(commands, current.String())
		}
	} else if len(commands) > 1 && commands[len(commands)-1] == "" {
		commands = commands[:len(commands)-1]
	}
	return commands, controls, submits
}

func indexRune(values []rune, target rune) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func stringField(fields map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(fields[name], &value)
	return value
}

func boolField(fields map[string]json.RawMessage, name string) bool {
	var value bool
	_ = json.Unmarshal(fields[name], &value)
	return value
}
