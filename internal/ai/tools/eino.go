package tools

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
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
	TabID    string `json:"tab_id,omitempty"`
	SinceSeq uint64 `json:"since_seq,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}
type SendKeysArgs struct {
	Keys   string `json:"keys" jsonschema:"required"`
	Enter  bool   `json:"enter,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
	TabID  string `json:"tab_id,omitempty"`
}
type WaitForArgs struct {
	Pattern   string `json:"pattern" jsonschema:"required"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	SinceSeq  uint64 `json:"since_seq,omitempty"`
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
type TerminalCreateArgs struct {
	Title      string `json:"title,omitempty"`
	Command    string `json:"command,omitempty"`
	Background bool   `json:"background,omitempty"`
}
type TerminalRenameArgs struct {
	TabID string `json:"tab_id,omitempty"`
	Title string `json:"title" jsonschema:"required"`
}

var einoNames = []string{
	"exec_commands", "read_file", "write_file", "list_dir", "search_files", "read_screen", "send_keys", "wait_for",
	"shell_history",
	"docker_ps", "docker_logs", "docker_exec", "docker_control", "db_list_tables", "db_describe", "db_query", "redis_scan",
	"list_assets", "ask_user", "edit_file", "todo_write", "exit_plan_mode", ReminderTool, NotifyTool,
	"terminal_create", "terminal_rename",
}

var einoDescriptions = map[string]string{
	"exec_commands":   "Execute shell commands sequentially in the current session (1-20 commands).",
	"read_file":       "Read a UTF-8 text file and record the content version for subsequent writes.",
	"write_file":      "Create a new file atomically without overwriting existing files; overwriting an existing file requires user authorization, keeps a backup, and may overwrite external changes made after confirmation; before writing, the file must be read or confirmed to not exist.",
	"list_dir":        "List directory contents, at most 500 entries.",
	"search_files":    "Search by file name glob or file content regex, without going through the shell.",
	"read_screen":     "Read the visible screen and screen sequence number of the terminal in the current scope; pass since_seq to read only new output after that sequence number, paged by max_bytes; optional tab_id selects a tab created by this task's terminal_create. Includes command state when the terminal supports OSC 133.",
	"send_keys":       "Send keys to the terminal in the current scope; may include <enter> and <ctrl+c>; returns a screen sequence anchor; optional tab_id selects a tab created by this task's terminal_create. When wait_ms>0 and the terminal supports OSC 133, waits for the command to finish and returns the exit code and screen tail; otherwise returns right after sending and notes that the result is unverified.",
	"wait_for":        "Wait for the last 30 lines of the current terminal to match a regex; pass since_seq to match only new output after that screen sequence anchor (old content cannot be matched by mistake). Can be canceled by the task.",
	"shell_history":   "Read the shell history (zsh/bash/fish) of the local machine running NexTerm; returns the newest N entries (default 50, at most 500), redacted. Reads only the local machine and does not sync by default; remote sessions still read the local history and label it explicitly. Requires user confirmation.",
	"docker_ps":       "List Docker containers in the current session.",
	"docker_logs":     "Read container logs and filter locally by substring.",
	"docker_exec":     "Execute a command inside a container using sh -c.",
	"docker_control":  "Start, stop, restart, or remove a container.",
	"db_list_tables":  "List MySQL tables.",
	"db_describe":     "Read MySQL table fields and indexes.",
	"db_query":        "Execute SQL; write operations must go through permission confirmation.",
	"redis_scan":      "Run one Redis SCAN starting from cursor 0.",
	"list_assets":     "List available connection assets.",
	"ask_user":        "Pause the agent and wait for the user to answer the question.",
	"edit_file":       "Replace text exactly and keep a backup; existing files are overwritten non-transactionally with user authorization and may overwrite external changes made after confirmation; the file must be read before writing.",
	"todo_write":      "Replace the full todo list of the current task.",
	"exit_plan_mode":  "Submit the plan for user review; the plan is not executed automatically.",
	ReminderTool:      "Create a one-shot reminder (requires user confirmation); when the time comes, the message is delivered to the current conversation; message is the reminder content, and the time is specified by either delay_minutes or at (RFC3339).",
	NotifyTool:        "Send the user an in-app notification, for long-task completion or matters needing the user's immediate attention; message is the notification content (no more than 200 characters); the content must also be written into the current reply body, because the popup may be missed.",
	"terminal_create": "Create a new terminal tab in the current session (starts a new shell, requires user confirmation); optional title sets the tab title, optional command is the startup command (supported only by ssh/docker/local/device sessions; it is written verbatim to the new shell's standard input, and when it contains newlines it runs as multiple commands in sequence; unsupported kinds such as winrm are rejected, in which case create the tab first and then send the command with send_keys passing tab_id). Returns the new tab's tab_id; by default the tab is shown in the foreground of the user's workspace, the AI works in it, and the user can take over input at any time; with background=true the tab is created detached in the background instead, and the user can take over in 「后台会话」 (Background Sessions). Either way, send_keys/read_screen with tab_id can later operate the tabs created by this task.",
	"terminal_rename": "Rename the terminal tab in the current scope; title is the new title (required), tab_id defaults to the current tab. Custom titles are not overwritten by remote title sequences and are preserved after background session reconnection.",
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
		"shell_history": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[ShellHistoryArgs]("shell_history", einoDescriptions["shell_history"])
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
		ReminderTool: func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[SendReminderArgs](ReminderTool, einoDescriptions[ReminderTool])
		},
		NotifyTool: func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[NotifyArgs](NotifyTool, einoDescriptions[NotifyTool])
		},
		"terminal_create": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[TerminalCreateArgs]("terminal_create", einoDescriptions["terminal_create"])
		},
		"terminal_rename": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[TerminalRenameArgs]("terminal_rename", einoDescriptions["terminal_rename"])
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
	JobID          string
	ConversationID string
	Registry       *Registry
	Scope          Scope
	Permission     guard.Config
	Memory         *guard.Memory
	PlanMode       bool
	Subagents      *SubagentConfig

	SubagentManager *subagent.Manager

	SubagentEvents SubagentEventSink
}

type SubagentEventSink func(context.Context, string, int, subagent.Event)

type SubagentConfig struct {
	Model           subagent.ModelFactory
	ModelForProfile subagent.ProfileModelFactory
	AllowedTools    []string
	Limits          subagent.Config

	Runs            SubagentRunStore
	ConversationID  string
	ActiveProfileID func() string
}

type SubagentRunStore interface {
	RunInsert(ctx context.Context, row store.RunRow) error
	RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error
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

	// Commands carries the full command list of exec_commands for the confirm dialog to display in full.
	Commands []string
	// RulePattern is the path scope an "always allow" decision will grant; empty means the action is not path-limited.
	RulePattern string
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
	Resource        guard.Resource
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
			return utils.InferTool("shell_history", einoDescriptions["shell_history"], func(ctx context.Context, input ShellHistoryArgs) (Output, error) {
				return e.run(ctx, "shell_history", input)
			})
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
		func() (tool.InvokableTool, error) {
			return utils.InferTool(ReminderTool, einoDescriptions[ReminderTool], func(ctx context.Context, input SendReminderArgs) (Output, error) {
				return e.run(ctx, ReminderTool, input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool(NotifyTool, einoDescriptions[NotifyTool], func(ctx context.Context, input NotifyArgs) (Output, error) {
				return e.run(ctx, NotifyTool, input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("terminal_create", einoDescriptions["terminal_create"], func(ctx context.Context, input TerminalCreateArgs) (Output, error) {
				return e.run(ctx, "terminal_create", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("terminal_rename", einoDescriptions["terminal_rename"], func(ctx context.Context, input TerminalRenameArgs) (Output, error) {
				return e.run(ctx, "terminal_rename", input)
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
	case "terminal_rename":
		if _, ok := deps.Terminal.(RenameTerminal); !ok || e.Scope.TabID == "" {
			return false
		}
		return e.Scope.SessionID == "" || deps.TabSession == nil || deps.TabSession(e.Scope.TabID) == e.Scope.SessionID
	case "terminal_create":
		if e.Scope.SessionID == "" {
			return false
		}
		_, ok := deps.Terminal.(CreateTerminal)
		return ok
	case "shell_history":
		return true
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
	case ReminderTool:
		return deps.Reminders != nil
	case NotifyTool:
		return deps.Notify != nil
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
	limits.NewModelForProfile = e.Subagents.ModelForProfile
	limits.ResolveDefaultProfileID = e.Subagents.ActiveProfileID
	limits.OnFinish = e.persistSubagentRun
	var manager *subagent.Manager
	limits.NewTools = func(ctx context.Context, _ subagent.Scope) ([]tool.BaseTool, error) {
		return e.scopedSubagentTools(ctx, manager)
	}
	composed, err := subagent.NewManager(limits)
	if err != nil {
		return nil, err
	}
	manager = composed
	e.SubagentManager = composed
	spawn, err := subagent.NewSpawnTool(manager, subagent.Scope{AllowedTools: e.subagentAllowedTools()}, e.subagentObserver)
	if err != nil {
		return nil, err
	}
	return spawnOutputTool{InvokableTool: spawn}, nil
}

func (e *Execution) persistSubagentRun(ctx context.Context, request subagent.Request, result subagent.Result) error {
	return e.writeSubagentRun(ctx, request, result)
}

func (e *Execution) writeSubagentRun(ctx context.Context, request subagent.Request, result subagent.Result) error {
	config := e.Subagents
	if config == nil || config.Runs == nil || config.ConversationID == "" {
		return nil
	}
	status := store.RunStatusCompleted
	switch result.Status {
	case subagent.StatusFailed:
		status = store.RunStatusFailed
	case subagent.StatusCanceled:
		status = store.RunStatusCanceled
	}
	now := ids.NowMS()
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return config.Runs.RunInsert(writeCtx, store.RunRow{
		ID: ids.New(), ConversationID: config.ConversationID, Status: status, Source: "subagent", ProfileID: request.ModelProfileID,
		Turns: result.Turns, TokensIn: usage.SaturatingInt64(result.Usage.PromptTokens), TokensOut: usage.SaturatingInt64(result.Usage.CompletionTokens),
		CacheCreationTokens: usage.SaturatingInt64(result.Usage.CacheCreationTokens), LatencyMS: result.Usage.LatencyMS,
		Error: result.Error, CreatedAt: now, UpdatedAt: now, FinishedAt: &now,
	})
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
		encoded, marshalErr := json.Marshal(panicOutput(subagent.SpawnToolName, recovered))
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
	child := &Execution{JobID: e.JobID, ConversationID: e.ConversationID, Registry: e.Registry, Scope: e.Scope, Permission: e.Permission, Memory: e.Memory, PlanMode: e.PlanMode}
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
		if info.Name == "exit_plan_mode" || info.Name == ReminderTool {
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
		if name == "" || name == "ask_user" || name == "exit_plan_mode" || name == ReminderTool {
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
	if call.Name == "write_file" || call.Name == "edit_file" {
		normalized, err := normalizeWritePathArgs(call.Args)
		if err != nil {
			return Fail(err), nil
		}
		call.Args = normalized
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
		if data != "allow" && data != "allow_session" && data != "allow_persistent" {
			return Fail(errors.New("确认结果无效，操作未执行")), nil
		}
		if call.Name == "send_keys" {
			var input SendKeysArgs
			if err := json.Unmarshal(call.Args, &input); err != nil {
				return Fail(err), nil
			}
			tabID, err := e.Registry.resolveTab(e.JobID, e.Scope, input.TabID)
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
			if guard.RuleActionNeedsPath(state.Resource.Action) && state.Resource.Path != "" {
				e.Memory.AddPathRule(state.Resource.Action, guard.DirPattern(state.Resource.Path))
			} else {
				for _, kind := range state.MemoryKinds {
					e.Memory.Add(kind)
				}
				if len(state.MemoryKinds) == 0 {
					e.Memory.Add(state.MemoryKind)
				}
			}
		}
		if data == "allow_persistent" {
			if err := e.persistPermanentGrant(ctx, state); err != nil {
				return Fail(err), nil
			}
		}
		call.AuthorizationID = state.AuthorizationID
		if call.Name == ReminderTool {
			return e.executeReminder(ctx, call), nil
		}
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
	if call.Name == "shell_history" {
		ruling = guard.Confirm(shellHistoryKind, "读取本机 shell 历史文件（历史常包含敏感命令，需要确认）")
	}
	if call.Name == "send_keys" {
		var input SendKeysArgs
		if err := json.Unmarshal(call.Args, &input); err != nil {
			return Fail(err), nil
		}
		tabID, err := e.Registry.resolveTab(e.JobID, e.Scope, input.TabID)
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
	memory := e.Memory
	forcedConfirm := call.Name == "shell_history" && e.Registry.remoteAsset(ctx, e.Scope)
	if forcedConfirm {
		memory = guard.NewMemory()
	}
	decision := e.decide(ctx, call, ruling, memory)
	if forcedConfirm && decision.Action == guard.ActionAllow {
		decision.Action = guard.ActionAsk
	}
	if decision.Action == guard.ActionDeny {
		return Fail(errors.New("权限策略已拒绝: " + decision.Ruling.Reason)), nil
	}
	preparation, err := e.Registry.Prepare(ctx, e.JobID, e.Scope, call)
	if err != nil {
		return Fail(err), nil
	}
	if decision.Action == guard.ActionAsk {
		resource := resourceForCall(call)
		rendered := DisplayCall(call)
		if call.Name == "send_keys" {
			rendered = DisplaySendKeys(call, terminalInput, cursor)
		}
		info := Interaction{Kind: "confirm", CallID: call.ID, Tool: call.Name, Args: string(call.Args), Risk: ruling.Risk.String(), Rendered: rendered, Reason: ruling.Reason, Preview: preparation.Preview}
		if ruling.Risk == guard.NeedsConfirm && guard.RuleActionSupported(resource.Action) {
			info.RulePattern = persistentRulePattern(resource)
			if call.Name == "exec_commands" {
				var input ExecCommandsArgs
				if json.Unmarshal(call.Args, &input) == nil {
					info.Commands = input.Commands
				}
			}
		}
		state := InteractionState{Kind: "confirm", CallID: call.ID, MemoryKind: ruling.Kind, MemoryKinds: ruling.ApprovalKinds(), TerminalInput: terminalInput, TerminalCursor: cursor, Info: info, AuthorizationID: GuardAuthorizationID(decision), Resource: resource}
		if forcedConfirm {
			state.MemoryKind = ""
			state.MemoryKinds = nil
		}
		return Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	call.AuthorizationID = GuardAuthorizationID(decision)
	if call.Name == ReminderTool {
		return e.executeReminder(ctx, call), nil
	}
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
