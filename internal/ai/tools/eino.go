package tools

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type ExecCommandsArgs struct {
	Commands []string `json:"commands" jsonschema:"required"`
	Timeout  int      `json:"timeout,omitempty"`
}
type ReadFileArgs struct {
	Path     string `json:"path" jsonschema:"required"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}
type WriteFileArgs struct {
	Path    string `json:"path" jsonschema:"required"`
	Content string `json:"content" jsonschema:"required"`
}
type ListDirArgs struct {
	Path string `json:"path" jsonschema:"required"`
}
type SearchFilesArgs struct {
	Path    string `json:"path" jsonschema:"required"`
	Pattern string `json:"pattern" jsonschema:"required"`
	By      string `json:"by" jsonschema:"required,enum=name,enum=content"`
}
type ReadScreenArgs struct {
	TabID string `json:"tab_id,omitempty"`
}
type SendKeysArgs struct {
	Keys  string `json:"keys" jsonschema:"required"`
	Enter bool   `json:"enter,omitempty"`
}
type WaitForArgs struct {
	Pattern   string `json:"pattern" jsonschema:"required"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}
type EmptyArgs struct{}
type DockerLogsArgs struct {
	ContainerID string `json:"container_id" jsonschema:"required"`
	Tail        int    `json:"tail,omitempty"`
	Grep        string `json:"grep,omitempty"`
}
type DockerExecArgs struct {
	ContainerID string `json:"container_id" jsonschema:"required"`
	Command     string `json:"cmd" jsonschema:"required"`
}
type DockerControlArgs struct {
	ContainerID string `json:"container_id" jsonschema:"required"`
	Action      string `json:"action" jsonschema:"required,enum=start,enum=stop,enum=restart,enum=rm"`
}
type DBListTablesArgs struct {
	Schema string `json:"schema,omitempty"`
}
type DBDescribeArgs struct {
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table" jsonschema:"required"`
}
type DBQueryArgs struct {
	SQL string `json:"sql" jsonschema:"required"`
}
type RedisScanArgs struct {
	Pattern string `json:"pattern,omitempty"`
	Count   uint64 `json:"count,omitempty"`
}
type AskUserArgs struct {
	Question string   `json:"question" jsonschema:"required"`
	Options  []string `json:"options,omitempty"`
}
type EditFileArgs struct {
	Path       string `json:"path" jsonschema:"required"`
	OldString  string `json:"old_string" jsonschema:"required"`
	NewString  string `json:"new_string" jsonschema:"required"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}
type TodoWriteArgs struct {
	Todos []TodoItem `json:"todos" jsonschema:"required"`
}
type ExitPlanModeArgs struct {
	Plan string `json:"plan" jsonschema:"required"`
}

var einoNames = []string{
	"exec_commands", "read_file", "write_file", "list_dir", "search_files", "read_screen", "send_keys", "wait_for",
	"docker_ps", "docker_logs", "docker_exec", "docker_control", "db_list_tables", "db_describe", "db_query", "redis_scan",
	"list_assets", "ask_user", "edit_file", "todo_write", "exit_plan_mode",
}

var einoDescriptions = map[string]string{
	"exec_commands":  "在当前会话按顺序执行 shell 命令（1-20 条）。",
	"read_file":      "读取 UTF-8 文本文件，并为后续写入记录内容版本。",
	"write_file":     "新文件以原子 no-clobber 创建；现有文件为用户授权的非事务覆盖并保留备份，可能覆盖确认后的外部修改；写入前必须读取或确认不存在。",
	"list_dir":       "列出目录内容，最多 500 项。",
	"search_files":   "按文件名 glob 或文件内容正则搜索，不经过 shell。",
	"read_screen":    "读取当前作用域终端的可见屏幕。",
	"send_keys":      "向当前作用域终端发送按键，可包含 <enter> 和 <ctrl+c>。",
	"wait_for":       "等待当前终端最近 30 行匹配正则，可被任务取消。",
	"docker_ps":      "列出当前会话中的 Docker 容器。",
	"docker_logs":    "读取容器日志并在本地按子串过滤。",
	"docker_exec":    "在容器内使用 sh -c 执行命令。",
	"docker_control": "启动、停止、重启或删除容器。",
	"db_list_tables": "列出 MySQL 表。",
	"db_describe":    "读取 MySQL 表字段和索引。",
	"db_query":       "执行 SQL；写操作必须经过权限确认。",
	"redis_scan":     "从 cursor 0 开始执行一次 Redis SCAN。",
	"list_assets":    "列出可用连接资产。",
	"ask_user":       "暂停代理并等待用户回答问题。",
	"edit_file":      "精确替换文本并保留备份；现有文件为用户授权的非事务覆盖，可能覆盖确认后的外部修改；写入前必须读取。",
	"todo_write":     "替换当前任务的完整待办列表。",
	"exit_plan_mode": "提交计划供用户审核，不自动执行。",
}

func einoToolSchemas() []Schema {
	builders := map[string]func() (*schema.ToolInfo, error){
		"exec_commands": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[ExecCommandsArgs]("exec_commands", einoDescriptions["exec_commands"])
		},
		"read_file": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[ReadFileArgs]("read_file", einoDescriptions["read_file"])
		},
		"write_file": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[WriteFileArgs]("write_file", einoDescriptions["write_file"])
		},
		"list_dir": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[ListDirArgs]("list_dir", einoDescriptions["list_dir"])
		},
		"search_files": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[SearchFilesArgs]("search_files", einoDescriptions["search_files"])
		},
		"read_screen": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[ReadScreenArgs]("read_screen", einoDescriptions["read_screen"])
		},
		"send_keys": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[SendKeysArgs]("send_keys", einoDescriptions["send_keys"])
		},
		"wait_for": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[WaitForArgs]("wait_for", einoDescriptions["wait_for"])
		},
		"docker_ps": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[EmptyArgs]("docker_ps", einoDescriptions["docker_ps"])
		},
		"docker_logs": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[DockerLogsArgs]("docker_logs", einoDescriptions["docker_logs"])
		},
		"docker_exec": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[DockerExecArgs]("docker_exec", einoDescriptions["docker_exec"])
		},
		"docker_control": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[DockerControlArgs]("docker_control", einoDescriptions["docker_control"])
		},
		"db_list_tables": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[DBListTablesArgs]("db_list_tables", einoDescriptions["db_list_tables"])
		},
		"db_describe": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[DBDescribeArgs]("db_describe", einoDescriptions["db_describe"])
		},
		"db_query": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[DBQueryArgs]("db_query", einoDescriptions["db_query"])
		},
		"redis_scan": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[RedisScanArgs]("redis_scan", einoDescriptions["redis_scan"])
		},
		"list_assets": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[EmptyArgs]("list_assets", einoDescriptions["list_assets"])
		},
		"ask_user": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[AskUserArgs]("ask_user", einoDescriptions["ask_user"])
		},
		"edit_file": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[EditFileArgs]("edit_file", einoDescriptions["edit_file"])
		},
		"todo_write": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[TodoWriteArgs]("todo_write", einoDescriptions["todo_write"])
		},
		"exit_plan_mode": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[ExitPlanModeArgs]("exit_plan_mode", einoDescriptions["exit_plan_mode"])
		},
	}
	result := make([]Schema, 0, len(einoNames))
	for _, name := range einoNames {
		info, err := builders[name]()
		if err != nil {
			panic(err)
		}
		converted, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			panic(err)
		}
		var parameters map[string]any
		encoded, _ := json.Marshal(converted)
		_ = json.Unmarshal(encoded, &parameters)
		result = append(result, Schema{Name: name, Description: info.Desc, Parameters: parameters})
	}
	return result
}

type Execution struct {
	JobID      string
	Registry   *Registry
	Scope      Scope
	Permission guard.Config
	Memory     *guard.Memory
	PlanMode   bool
	Subagents  *SubagentConfig

	SubagentEvents SubagentEventSink
}

type SubagentEventSink func(context.Context, string, int, subagent.Event)

type SubagentConfig struct {
	Model        subagent.ModelFactory
	AllowedTools []string
	Limits       subagent.Config
}

type Interaction struct {
	Kind     string
	CallID   string
	Tool     string
	Args     string
	Risk     string
	Rendered string
	Reason   string
	Preview  *Preview
	Question *Question
}

type InteractionState struct {
	Kind           string
	CallID         string
	MemoryKind     guard.Kind
	MemoryKinds    []guard.Kind
	TerminalInput  string
	TerminalCursor int
	Info           Interaction

	AuthorizationID string
}

func init() {
	gob.Register(Interaction{})
	gob.Register(InteractionState{})
	gob.Register(Preview{})
	gob.Register(Question{})
}

func (e *Execution) Tools() ([]tool.BaseTool, error) {
	makers := []func() (tool.InvokableTool, error){
		func() (tool.InvokableTool, error) {
			return utils.InferTool("exec_commands", einoDescriptions["exec_commands"], func(ctx context.Context, input ExecCommandsArgs) (Output, error) {
				return e.run(ctx, "exec_commands", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("read_file", einoDescriptions["read_file"], func(ctx context.Context, input ReadFileArgs) (Output, error) { return e.run(ctx, "read_file", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("write_file", einoDescriptions["write_file"], func(ctx context.Context, input WriteFileArgs) (Output, error) { return e.run(ctx, "write_file", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("list_dir", einoDescriptions["list_dir"], func(ctx context.Context, input ListDirArgs) (Output, error) { return e.run(ctx, "list_dir", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("search_files", einoDescriptions["search_files"], func(ctx context.Context, input SearchFilesArgs) (Output, error) {
				return e.run(ctx, "search_files", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("read_screen", einoDescriptions["read_screen"], func(ctx context.Context, input ReadScreenArgs) (Output, error) {
				return e.run(ctx, "read_screen", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("send_keys", einoDescriptions["send_keys"], func(ctx context.Context, input SendKeysArgs) (Output, error) { return e.run(ctx, "send_keys", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("wait_for", einoDescriptions["wait_for"], func(ctx context.Context, input WaitForArgs) (Output, error) { return e.run(ctx, "wait_for", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("docker_ps", einoDescriptions["docker_ps"], func(ctx context.Context, input EmptyArgs) (Output, error) { return e.run(ctx, "docker_ps", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("docker_logs", einoDescriptions["docker_logs"], func(ctx context.Context, input DockerLogsArgs) (Output, error) {
				return e.run(ctx, "docker_logs", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("docker_exec", einoDescriptions["docker_exec"], func(ctx context.Context, input DockerExecArgs) (Output, error) {
				return e.run(ctx, "docker_exec", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("docker_control", einoDescriptions["docker_control"], func(ctx context.Context, input DockerControlArgs) (Output, error) {
				return e.run(ctx, "docker_control", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("db_list_tables", einoDescriptions["db_list_tables"], func(ctx context.Context, input DBListTablesArgs) (Output, error) {
				return e.run(ctx, "db_list_tables", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("db_describe", einoDescriptions["db_describe"], func(ctx context.Context, input DBDescribeArgs) (Output, error) {
				return e.run(ctx, "db_describe", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("db_query", einoDescriptions["db_query"], func(ctx context.Context, input DBQueryArgs) (Output, error) { return e.run(ctx, "db_query", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("redis_scan", einoDescriptions["redis_scan"], func(ctx context.Context, input RedisScanArgs) (Output, error) { return e.run(ctx, "redis_scan", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("list_assets", einoDescriptions["list_assets"], func(ctx context.Context, input EmptyArgs) (Output, error) { return e.run(ctx, "list_assets", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("ask_user", einoDescriptions["ask_user"], func(ctx context.Context, input AskUserArgs) (Output, error) { return e.run(ctx, "ask_user", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("edit_file", einoDescriptions["edit_file"], func(ctx context.Context, input EditFileArgs) (Output, error) { return e.run(ctx, "edit_file", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("todo_write", einoDescriptions["todo_write"], func(ctx context.Context, input TodoWriteArgs) (Output, error) { return e.run(ctx, "todo_write", input) })
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("exit_plan_mode", einoDescriptions["exit_plan_mode"], func(ctx context.Context, input ExitPlanModeArgs) (Output, error) {
				return e.run(ctx, "exit_plan_mode", input)
			})
		},
	}
	result := make([]tool.BaseTool, 0, len(makers)+1)
	for index, makeTool := range makers {
		if !e.enabled(einoNames[index]) {
			continue
		}
		current, err := makeTool()
		if err != nil {
			return nil, err
		}
		result = append(result, current)
	}
	if e.enabled(subagent.SpawnToolName) {
		spawn, err := e.subagentSpawnTool()
		if err != nil {
			return nil, err
		}
		result = append(result, spawn)
	}
	return result, nil
}

func (e *Execution) enabled(name string) bool {
	if e.Registry == nil || e.PlanMode && !PlanAllowed(name) {
		return false
	}
	deps := e.Registry.deps
	switch name {
	case "exec_commands", "read_file", "write_file", "list_dir", "search_files", "edit_file":
		return deps.Transport != nil && e.Scope.SessionID != ""
	case "read_screen", "send_keys", "wait_for":
		if deps.Terminal == nil || e.Scope.TabID == "" {
			return false
		}
		return e.Scope.SessionID == "" || deps.TabSession == nil || deps.TabSession(e.Scope.TabID) == e.Scope.SessionID
	case "docker_ps":
		return deps.DockerPS != nil && e.Scope.SessionID != ""
	case "docker_logs":
		return deps.DockerLogs != nil && e.Scope.SessionID != ""
	case "docker_exec":
		return deps.DockerExec != nil && e.Scope.SessionID != ""
	case "docker_control":
		return deps.DockerAct != nil && e.Scope.SessionID != ""
	case "db_list_tables", "db_describe", "db_query", "redis_scan":
		return deps.Database != nil && e.Scope.ConnID != ""
	case "list_assets":
		return deps.ListAssets != nil
	case "exit_plan_mode":
		return e.PlanMode
	case subagent.SpawnToolName:
		return e.Subagents != nil
	default:
		return true
	}
}

func (e *Execution) subagentSpawnTool() (tool.InvokableTool, error) {
	if e.Subagents == nil || e.Subagents.Model == nil {
		return nil, errors.New("subagent model factory is not configured")
	}
	limits := e.Subagents.Limits
	limits.NewModel = e.Subagents.Model
	var manager *subagent.Manager
	limits.NewTools = func(ctx context.Context, _ subagent.Scope) ([]tool.BaseTool, error) {
		return e.scopedSubagentTools(ctx, manager)
	}
	composed, err := subagent.NewManager(limits)
	if err != nil {
		return nil, err
	}
	manager = composed
	spawn, err := subagent.NewSpawnTool(manager, subagent.Scope{AllowedTools: e.subagentAllowedTools()}, e.subagentObserver)
	if err != nil {
		return nil, err
	}
	return spawnOutputTool{InvokableTool: spawn}, nil
}

func (e *Execution) subagentObserver(ctx context.Context) subagent.Observer {
	return func(event subagent.Event) {
		if e.SubagentEvents == nil {
			return
		}
		parentCallID := compose.GetToolCallID(ctx)
		if parentCallID == "" {
			return
		}
		e.SubagentEvents(ctx, parentCallID, 1, event)
	}
}

type spawnOutputTool struct {
	tool.InvokableTool
}

func (s spawnOutputTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (output string, err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			output = ""
			err = ctxErr
			return
		}
		encoded, marshalErr := json.Marshal(Fail(fmt.Errorf("工具 %s 执行崩溃: %v", subagent.SpawnToolName, recovered)))
		if marshalErr != nil {
			err = marshalErr
			return
		}
		output = string(encoded)
	}()
	result, err := s.InvokableTool.InvokableRun(ctx, arguments, opts...)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(OK(result))
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (e *Execution) scopedSubagentTools(ctx context.Context, manager *subagent.Manager) ([]tool.BaseTool, error) {
	child := &Execution{JobID: e.JobID, Registry: e.Registry, Scope: e.Scope, Permission: e.Permission, Memory: e.Memory, PlanMode: e.PlanMode}
	available, err := child.Tools()
	if err != nil {
		return nil, err
	}
	scoped := make([]tool.BaseTool, 0, len(available)+1)
	for _, candidate := range available {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info.Name == "exit_plan_mode" {
			continue
		}
		scoped = append(scoped, candidate)
	}
	if manager != nil {
		spawn, err := subagent.NewSpawnTool(manager, subagent.Scope{})
		if err != nil {
			return nil, err
		}
		scoped = append(scoped, spawn)
	}
	return scoped, nil
}

func (e *Execution) subagentAllowedTools() []string {
	configured := e.Subagents.AllowedTools
	if len(configured) == 0 {
		configured = einoNames
	}
	allowed := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, name := range configured {
		name = strings.TrimSpace(name)
		if name == "" || name == "ask_user" || name == "exit_plan_mode" {
			continue
		}
		if _, duplicate := seen[name]; duplicate || !e.enabled(name) {
			continue
		}
		seen[name] = struct{}{}
		allowed = append(allowed, name)
	}
	return allowed
}

func (e *Execution) run(ctx context.Context, name string, input any) (Output, error) {
	return Guarded(ctx, name, func() (Output, error) {
		return e.runInner(ctx, name, input)
	})
}

func (e *Execution) runInner(ctx context.Context, name string, input any) (Output, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return Output{}, err
	}
	call := Call{ID: compose.GetToolCallID(ctx), Name: name, Args: encoded}
	if call.ID == "" {
		return Output{}, errors.New("Eino tool call id is empty")
	}
	if wasInterrupted, _, state := tool.GetInterruptState[InteractionState](ctx); wasInterrupted {
		target, hasData, data := tool.GetResumeContext[string](ctx)
		if !target {
			return Output{}, tool.StatefulInterrupt(ctx, state.Info, state)
		}
		if state.Kind == "question" {
			if !hasData || data == "" {
				return Output{}, tool.StatefulInterrupt(ctx, state.Info, state)
			}
			return Output{OK: true, Text: "用户回答：" + data}, nil
		}
		if !hasData {
			return Output{}, tool.StatefulInterrupt(ctx, state.Info, state)
		}
		if data == "deny" {
			return Fail(errors.New("用户拒绝了此操作")), nil
		}
		if data != "allow" && data != "allow_session" {
			return Fail(errors.New("确认结果无效，操作未执行")), nil
		}
		if call.Name == "send_keys" {
			tabID, err := e.Registry.resolveTab(e.Scope, "")
			if err != nil {
				return Fail(err), nil
			}
			screen, err := e.Registry.deps.Terminal.Snapshot(ctx, tabID)
			if err != nil {
				return Fail(err), nil
			}
			buffered, cursor := TerminalInputCursor(screen)
			if buffered != state.TerminalInput || cursor != state.TerminalCursor {
				return Fail(ErrTerminalInputChanged), nil
			}
		}
		if data == "allow_session" {
			for _, kind := range state.MemoryKinds {
				e.Memory.Add(kind)
			}
			if len(state.MemoryKinds) == 0 {
				e.Memory.Add(state.MemoryKind)
			}
		}
		call.AuthorizationID = state.AuthorizationID
		return e.Registry.Execute(ctx, e.JobID, e.Scope, call, nil), nil
	}
	return e.initial(ctx, call)
}

func (e *Execution) initial(ctx context.Context, call Call) (Output, error) {
	if err := e.Registry.Validate(call); err != nil {
		return Fail(err), nil
	}
	if e.PlanMode && !PlanAllowed(call.Name) {
		return Fail(errors.New("计划模式只允许只读工具")), nil
	}
	if call.Name == "exit_plan_mode" && !e.PlanMode {
		return Fail(errorsNewPlanModeOnly()), nil
	}
	terminalInput := ""
	cursor := 0
	ruling := guard.ClassifyTool(call.Name, call.Args, e.Permission)
	if call.Name == "send_keys" {
		var input SendKeysArgs
		if err := json.Unmarshal(call.Args, &input); err != nil {
			return Fail(err), nil
		}
		tabID, err := e.Registry.resolveTab(e.Scope, "")
		if err != nil {
			return Fail(err), nil
		}
		screen, err := e.Registry.deps.Terminal.Snapshot(ctx, tabID)
		if err != nil {
			return Fail(err), nil
		}
		terminalInput, cursor = TerminalInputCursor(screen)
		ruling = guard.Worst(ruling, guard.ClassifySendKeysWithCursor(input.Keys, terminalInput, cursor, input.Enter, e.Permission.DangerRules))
	}
	decision := guard.Decide(e.Permission, ruling, e.Memory)
	if decision.Action == guard.ActionDeny {
		return Fail(errors.New("权限策略已拒绝: " + decision.Ruling.Reason)), nil
	}
	preparation, err := e.Registry.Prepare(ctx, e.JobID, e.Scope, call)
	if err != nil {
		return Fail(err), nil
	}
	if decision.Action == guard.ActionAsk {
		rendered := DisplayCall(call)
		if call.Name == "send_keys" {
			rendered = DisplaySendKeys(call, terminalInput, cursor)
		}
		info := Interaction{Kind: "confirm", CallID: call.ID, Tool: call.Name, Args: string(call.Args), Risk: ruling.Risk.String(), Rendered: rendered, Reason: ruling.Reason, Preview: preparation.Preview}
		state := InteractionState{Kind: "confirm", CallID: call.ID, MemoryKind: ruling.Kind, MemoryKinds: ruling.ApprovalKinds(), TerminalInput: terminalInput, TerminalCursor: cursor, Info: info, AuthorizationID: GuardAuthorizationID(decision)}
		return Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	call.AuthorizationID = GuardAuthorizationID(decision)
	result := e.Registry.Execute(ctx, e.JobID, e.Scope, call, preparation)
	if result.Question != nil {
		info := Interaction{Kind: "question", CallID: call.ID, Tool: call.Name, Args: string(call.Args), Question: result.Question}
		state := InteractionState{Kind: "question", CallID: call.ID, Info: info}
		return Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	return result, nil
}

func errorsNewPlanModeOnly() error {
	return errors.New("exit_plan_mode 仅在计划模式可用")
}
