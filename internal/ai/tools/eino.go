package tools

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type ExecCommandsArgs struct {
	Commands []string `json:"commands" jsonschema:"required" jsonschema_description:"Shell commands to run in order (1-20); each runs in its own shell, so state does not carry over"`
	Timeout  int      `json:"timeout,omitempty" jsonschema_description:"Per-command timeout in seconds (1-300; default 60)"`
	AssetIDs []string `json:"asset_ids,omitempty" jsonschema_description:"Run on these already-connected assets (asset ids from list_assets; device/<id> for managed devices) instead of the current host; at most 64 targets"`
	Tags     []string `json:"tags,omitempty" jsonschema_description:"Run on connected SSH sessions whose tags all match; combined with asset_ids means their intersection; not usable for devices"`
}
type ReadFileArgs struct {
	Path     string `json:"path" jsonschema:"required" jsonschema_description:"File path on the current host"`
	MaxBytes int64  `json:"max_bytes,omitempty" jsonschema_description:"Maximum bytes to read (1 to 2097152; default 122880)"`
	Offset   int    `json:"offset,omitempty" jsonschema_description:"First line to show (1-based; default 1)"`
	Limit    int    `json:"limit,omitempty" jsonschema_description:"Number of lines to show (default 400; at most 2000)"`
}
type WriteFileArgs struct {
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"File path on the current host; an existing file must have been read first"`
	Content string `json:"content" jsonschema:"required" jsonschema_description:"Complete new UTF-8 file content"`
}
type ListDirArgs struct {
	Path string `json:"path" jsonschema:"required" jsonschema_description:"Directory path on the current host"`
}
type ReadScreenArgs struct {
	TabID        string `json:"tab_id,omitempty" jsonschema_description:"Terminal tab handle from terminal_list or terminal_create (default the current tab)"`
	SinceSeq     uint64 `json:"since_seq,omitempty" jsonschema_description:"Read only output after this screen sequence number; cannot be combined with history_lines"`
	MaxBytes     int64  `json:"max_bytes,omitempty" jsonschema_description:"Page size in bytes when reading with since_seq or cursor (1 to 122880; default 8192)"`
	Cursor       string `json:"cursor,omitempty" jsonschema_description:"Opaque cursor from a previous page to continue reading; invalid after a terminal resize"`
	HistoryLines int    `json:"history_lines,omitempty" jsonschema_description:"Also replay this many scrollback lines above the screen (0-400); cannot be combined with since_seq, cursor or max_bytes"`
	Unwrap       bool   `json:"unwrap,omitempty" jsonschema_description:"Join soft-wrapped lines into logical lines"`
	Full         bool   `json:"full,omitempty" jsonschema_description:"Return the whole visible screen even when this conversation already read this normal-screen terminal; cannot be combined with since_seq, cursor or history_lines"`
}
type SendKeysArgs struct {
	Keys          string `json:"keys" jsonschema:"required" jsonschema_description:"Text to type; may include key tags such as <enter> and <ctrl+c>"`
	Enter         bool   `json:"enter,omitempty" jsonschema_description:"Press Enter after the keys"`
	WaitMS        int    `json:"wait_ms,omitempty" jsonschema_description:"Milliseconds to wait for the command to finish (0-10000; default 0 returns right after sending)"`
	TabID         string `json:"tab_id,omitempty" jsonschema_description:"Terminal tab handle from terminal_list or terminal_create (default the current tab)"`
	Paste         bool   `json:"paste,omitempty" jsonschema_description:"Send keys as literal text in bracketed paste; key tags are not interpreted"`
	SubmitDelayMS int    `json:"submit_delay_ms,omitempty" jsonschema_description:"Delay before Enter in milliseconds (0-10000; default 150); only with paste=true and enter=true"`
	Force         bool   `json:"force,omitempty" jsonschema_description:"Send even though the shell input line changed since you last read the screen"`
	AllowControl  bool   `json:"allow_control,omitempty" jsonschema_description:"With paste=true, allow deliberate control bytes in keys"`
}
type WaitForArgs struct {
	Pattern   *string  `json:"pattern,omitempty" jsonschema_description:"Regular expression to match against the last 30 lines; may be omitted only when quiet_ms or lines is set"`
	TimeoutMS int64    `json:"timeout_ms,omitempty" jsonschema_description:"Maximum wait in milliseconds (1-120000; default 10000)"`
	SinceSeq  uint64   `json:"since_seq,omitempty" jsonschema_description:"Match only output after this screen sequence number (anchor from send_keys or read_screen)"`
	TabID     string   `json:"tab_id,omitempty" jsonschema_description:"Terminal tab handle (default the current tab); not combinable with tab_ids"`
	QuietMS   int64    `json:"quiet_ms,omitempty" jsonschema_description:"Also require no terminal output for this many milliseconds (0-120000; default 0)"`
	TabIDs    []string `json:"tab_ids,omitempty" jsonschema_description:"Wait across several tab handles; matches only output after each tab's anchor"`
	Mode      string   `json:"mode,omitempty" jsonschema:"enum=any,enum=all" jsonschema_description:"With several tab_ids: any returns on the first satisfied tab, all waits for every tab (default all)"`
	Lines     int      `json:"lines,omitempty" jsonschema_description:"Wait for at least this many new output lines instead of or together with pattern (0-100000)"`
}
type EmptyArgs struct{}
type DockerLogsArgs struct {
	ContainerID string `json:"container_id" jsonschema:"required" jsonschema_description:"Container id or name from docker_ps"`
	Tail        int    `json:"tail,omitempty" jsonschema_description:"Number of most recent log lines to read (1-5000; default 200)"`
	Grep        string `json:"grep,omitempty" jsonschema_description:"Keep only lines containing this substring (case-sensitive, not a regex)"`
}
type DockerExecArgs struct {
	ContainerID string `json:"container_id" jsonschema:"required" jsonschema_description:"Container id or name from docker_ps"`
	Command     string `json:"cmd" jsonschema:"required" jsonschema_description:"Shell command line, run inside the container with sh -c"`
}
type DockerControlArgs struct {
	ContainerID string `json:"container_id" jsonschema:"required" jsonschema_description:"Container id or name from docker_ps"`
	Action      string `json:"action" jsonschema:"required,enum=start,enum=stop,enum=restart,enum=rm" jsonschema_description:"Operation to apply to the container; rm removes it"`
}
type DBListTablesArgs struct {
	Schema string `json:"schema,omitempty" jsonschema_description:"Schema or database name to list (default the connection's current one)"`
}
type DBDescribeArgs struct {
	Schema string `json:"schema,omitempty" jsonschema_description:"Schema or database name containing the table (default the connection's current one)"`
	Table  string `json:"table" jsonschema:"required" jsonschema_description:"Table name"`
}
type DBQueryArgs struct {
	SQL string `json:"sql" jsonschema:"required" jsonschema_description:"A single SQL statement for the current connection"`
}
type RedisScanArgs struct {
	Pattern string `json:"pattern,omitempty" jsonschema_description:"Glob-style key pattern (default *)"`
	Count   uint64 `json:"count,omitempty" jsonschema_description:"SCAN COUNT hint per iteration (1-1000; default 100)"`
	Cursor  uint64 `json:"cursor,omitempty" jsonschema_description:"Cursor returned by a previous redis_scan to continue from (default 0 starts a new scan)"`
}
type AskUserArgs struct {
	Question string   `json:"question" jsonschema:"required" jsonschema_description:"Question to show the user"`
	Options  []string `json:"options,omitempty" jsonschema_description:"Suggested answers the user can pick from; each non-empty"`
}
type EditFileArgs struct {
	Path       string `json:"path" jsonschema:"required" jsonschema_description:"File path on the current host; the file must have been read first"`
	OldString  string `json:"old_string" jsonschema:"required" jsonschema_description:"Exact text to replace; must match exactly one place unless replace_all is true"`
	NewString  string `json:"new_string" jsonschema:"required" jsonschema_description:"Replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema_description:"Replace every occurrence of old_string instead of requiring a single match (default false)"`
}
type TodoWriteArgs struct {
	Todos []TodoItem `json:"todos" jsonschema:"required" jsonschema_description:"The complete todo list (at most 50 items); replaces the previous list"`
}
type ExitPlanModeArgs struct {
	Plan string `json:"plan" jsonschema:"required" jsonschema_description:"The plan to present to the user for review"`
}
type TerminalCreateArgs struct {
	Title      string `json:"title,omitempty" jsonschema_description:"Tab title"`
	Command    string `json:"command,omitempty" jsonschema_description:"Startup command written to the new shell's stdin (ssh, docker, local and device sessions only); newlines run several commands"`
	Background bool   `json:"background,omitempty" jsonschema_description:"Create the tab detached in the background instead of the user's foreground (default false)"`
	AssetID    string `json:"asset_id,omitempty" jsonschema_description:"Asset id from list_assets to open the tab on (default the current session); always needs user confirmation"`
	Connect    bool   `json:"connect,omitempty" jsonschema_description:"With asset_id, connect the asset first when it has no live session (default false)"`
}
type TerminalRenameArgs struct {
	TabID string `json:"tab_id,omitempty" jsonschema_description:"Terminal tab handle (default the current tab)"`
	Title string `json:"title" jsonschema:"required" jsonschema_description:"New tab title"`
}
type TerminalListArgs struct {
	Cursor string `json:"cursor,omitempty" jsonschema_description:"Cursor from the previous page to continue listing"`
	Limit  int    `json:"limit,omitempty" jsonschema_description:"Tabs per page (1-200; default 50)"`

	IncludeExited bool `json:"include_exited,omitempty" jsonschema_description:"Also list tabs whose shell has exited (hidden by default)"`
}
type OperationResultArgs struct {
	OperationID string `json:"operation_id" jsonschema:"required" jsonschema_description:"operation_id returned by the original mutating tool result"`
}

// LoadSkillArgs is the load_skill tool payload. The JSON shape matches the
// skills service contract (name only).
type LoadSkillArgs struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"Skill name"`
}

var einoNames = []string{
	"exec_commands", "read_file", "write_file", "list_dir", FindFilesTool, GrepFilesTool, "read_screen", "send_keys", "wait_for",
	"shell_history",
	"docker_ps", "docker_logs", "docker_exec", "docker_control", "db_list_tables", "db_describe", "db_query", "redis_scan",
	"list_assets", "ask_user", "edit_file", "todo_write", "exit_plan_mode", ReminderTool, NotifyTool,
	"terminal_create", "terminal_rename", "terminal_list", "operation_result",
}

// fileWriteBoundHelp states fileToolTimeout and fileWriteTimeout for write_file and edit_file.
const fileWriteBoundHelp = "Its lookups stop after 60 seconds and the write itself after 120 seconds, or when the run is cancelled. A write stopped while the remote side had not answered a write step fails with an unknown outcome: the file may or may not have been written or replaced, so read it again with read_file before any retry or further edit."

var einoDescriptions = map[string]string{
	"exec_commands":    "Execute shell commands sequentially on the current session's host (1-20 commands). Each command runs in its own non-interactive shell without stdin, so cd, export and other shell state do not carry over to the next command (chain with && inside one command when needed), and a command that waits for input (such as a sudo password prompt) hangs until the timeout. Every command runs even after an earlier one fails; exitCode is the first non-zero exit code and the result names that command. The timeout applies to each command: when one times out, the output gathered so far is kept, that command is marked as possibly still running with an unknown exit code, and the remaining commands are not run. Optional asset_ids and tags instead select the current owner's already-connected SSH sessions without auto-connecting; tags must all match, and asset_ids plus tags means their intersection. Managed devices can be targeted only by naming their asset id (device/<id>) in asset_ids, never by tags; devices have no environment tag, so they are always treated as production and every device needs typed confirmation by the user (shown to the user as 受管设备, no environment tag, treated as production). Production targets require confirmation. For a remote SSH/device host, use this tool to inspect that host's shell history; shell_history is limited to the current desktop process OS user in a local scope.",
	"read_file":        "Read a UTF-8 text file and record the whole file's content version for subsequent writes. Shows a window of lines (at most 120 KiB per call); when more lines remain, the result states the range shown, the total line count, and the offset and limit that read the next range.",
	"write_file":       "Create a new file atomically without overwriting existing files; overwriting an existing file may need user confirmation depending on the permission mode and may overwrite external changes made after confirmation; before writing, the file must be read or confirmed to not exist. A .nexterm-bak backup is made only when the asset enables it; the result states whether one was made. " + fileWriteBoundHelp,
	"list_dir":         "List directory contents, at most 500 entries (60-second timeout).",
	FindFilesTool:      "Find files by name or path glob on the current session's host, read-only and without confirmation. Use it to locate files (\"where is nginx.conf\", \"all *.go under src\"); use grep_files to search inside files. Runs ripgrep (rg --files) when the host has it, else find, and PowerShell on Windows; sessions without a command channel fall back to a slower file-by-file walk (30 seconds, 6 directory levels, 10000 entries). A search stops after 60 seconds and returns what it found, flagged as partial. Skips .git, node_modules and similar VCS or dependency directories and, with ripgrep, files ignored by .gitignore, unless include_ignored=true. Returns paths relative to the searched directory, newest first when modification times are available, head_limit per call (default 100); the result names the searched directory and backend, says when more results remain and which offset continues, and reports paths that could not be searched.",
	GrepFilesTool:      "Search file contents for a regular expression on the current session's host, read-only and without confirmation, like ripgrep. output_mode=files_with_matches (default) lists matching files, content shows path:line:text with optional context lines, count shows matching lines per file; narrow with glob and/or type, case_insensitive for case-blind matching. Runs ripgrep when the host has it (Rust regex syntax), else grep -P (PCRE) or grep -E (POSIX extended, where \\d and lazy quantifiers do not work), .NET regex through PowerShell on Windows, and Go RE2 in the file-by-file fallback for sessions without a command channel; prefer portable syntax such as [0-9], \\s, \\b and (a|b). multiline=true needs ripgrep. Binary files and .git, node_modules and similar directories are skipped (and, with ripgrep, .gitignore'd files) unless include_ignored=true. A search stops after 60 seconds and returns what it found, flagged as partial. Results are sorted by path and paged with head_limit/offset (default 100 entries, or 200 lines in content mode); lines are clipped at 1000 characters; the result names the searched directory and backend, says when more remain and which offset continues, and reports paths that could not be searched.",
	"read_screen":      "Read the visible screen and screen sequence number of the terminal in the current scope, or of the tab named by tab_id (a terminal_list handle, a tab this task created, or a read-only handle of a terminal the user @-referenced). A full-screen program (AltScreen, such as vim or top) always returns its whole screen; on a normal screen the first read in this conversation (and the first after the history was compacted) returns the whole screen without trailing blank rows, and a later plain read of the same terminal returns only the output printed since the previous read (or a note that nothing changed), so pass full=true when you need the whole screen again; subagents always get the whole screen; pass since_seq to read only new output after that sequence number, paged by max_bytes, or pass the opaque cursor from a previous page instead (a cursor is bound to the terminal geometry and fails explicitly after a resize); history_lines replays up to that many scrollback lines above the screen from the raw output stream and cannot be combined with since_seq, cursor, or max_bytes; a since_seq newer than the tab's latest sequence (for example an anchor from another terminal) is rejected; unwrap=true joins soft-wrapped lines into logical lines. The header carries the tab identity (title, kind, shell-reported cwd, stable handle) when available. Includes command state when the terminal supports OSC 133.",
	"send_keys":        "Send keys to the terminal in the current scope; may include key tags such as <enter> and <ctrl+c>. " + SendKeysTagHelp + " Returns the screen sequence read before the write as an anchor, so wait_for or read_screen with since_seq set to it sees all output the keys produced; optional tab_id selects a tab created by this task's terminal_create or an unmarked terminal_list handle; @-referenced terminals on other connections are read-only and refuse send_keys. If the shell input line changed after this task last read the screen (new user input), the write is refused unless force=true confirms you know about the new input; the check is skipped while a command is running, on the alternate screen, or when no shell prompt line is recognized. Writes to a terminal that a terminal takeover currently owns are refused. With paste=true, keys are literal text wrapped in bracketed-paste markers when the terminal mode is on: carriage returns and control bytes are rejected (allow_control=true acknowledges deliberate control bytes), <...> sequences are sent literally, enter submits after submit_delay_ms (default 150, paste only), and when the terminal supports it the paste, delay, and Enter share one write lock so user input cannot interleave. submitted_seq is the post-write output anchor and write_confirmed is only transport acknowledgment, not remote application acceptance. When wait_ms>0 and the terminal supports OSC 133, waits for the command to finish and returns the exit code and screen tail; otherwise returns right after sending and notes that the result is unverified.",
	"wait_for":         "Wait for the last 30 lines of the current terminal to match a regex (^ and $ match at line boundaries, ignoring a row's trailing spaces); optional tab_id selects a tab created by this task, an unmarked terminal_list handle, or a read-only handle of a terminal the user @-referenced. Pass since_seq to match only new output after that screen sequence anchor; a since_seq newer than the tab's latest sequence (for example an anchor from another terminal) is rejected. With quiet_ms>0, return only when the pattern also remains satisfied and the terminal has had no output for at least that many milliseconds; pattern may be omitted or empty only when quiet_ms or lines is set. lines waits for at least that many new output lines (counted by line feeds after the anchor) instead of a pattern; both may be combined. When lines>0 or several tab_ids are given, the pattern is matched only against output produced after each tab's anchor, never against text already on the screen. tab_ids waits across multiple tabs anchored at each tab's latest output sequence, with mode=any returning on the first satisfied tab and mode=all (default) waiting for every tab; per-tab anchors, line counts, and idle state are reported in the wait result metadata. Can be canceled by the task.",
	"shell_history":    "Read only the shell history (zsh/bash/fish) of the OS user running the current NexTerm desktop process; available only in a desktop local scope or with no remote scope. Returns the newest entries, redacted. May need user confirmation depending on the permission mode. For a remote SSH/device host, use exec_commands on that host instead; never use the desktop user's history as a substitute for remote history.",
	"docker_ps":        "List Docker containers in the current session.",
	"docker_logs":      "Read container logs and filter locally by substring.",
	"docker_exec":      "Execute a command inside a container using sh -c.",
	"docker_control":   "Start, stop, restart, or remove a container.",
	"db_list_tables":   "List MySQL or PostgreSQL tables (30-second timeout).",
	"db_describe":      "Read MySQL or PostgreSQL table fields and indexes (30-second timeout).",
	"db_query":         "Execute SQL; write operations go through the permission check and may need user confirmation depending on the permission mode.",
	"redis_scan":       "List Redis keys matching a pattern with SCAN, repeating SCAN until the iteration completes, 300 keys are found, or about 5 seconds pass. A returned cursor other than 0 means more keys may still match, even when few or no keys were returned: pass it as cursor to continue.",
	"list_assets":      "List available connection assets with asset_id and tags for terminal_create and exec_commands targeting.",
	"ask_user":         "Pause the agent and wait for the user to answer the question.",
	"edit_file":        "Replace text exactly; existing files are overwritten non-transactionally (may need user confirmation depending on the permission mode) and may overwrite external changes made after confirmation; the file must be read before writing. A .nexterm-bak backup is made only when the asset enables it; the result states whether one was made. " + fileWriteBoundHelp,
	"todo_write":       "Replace the full todo list of the current task.",
	"exit_plan_mode":   "Submit the plan for user review; the plan is not executed automatically.",
	ReminderTool:       "Create a one-shot reminder (may need user confirmation depending on the permission mode); when the time comes, the message is delivered to the current conversation.",
	NotifyTool:         "Send the user an in-app notification, for long-task completion or matters needing the user's immediate attention; the content must also be written into the current reply body, because the popup may be missed.",
	"terminal_create":  "Create a new terminal tab in the current session or on optional asset_id (an owner-checked, supported, live session; assets are never auto-connected unless connect=true explicitly opts in, in which case a missing live session is established first and a creation failure after a successful connect names the residual session). Starts a new shell and may need user confirmation depending on the permission mode (a target asset_id always needs it); optional title sets the tab title, optional command is the startup command (supported only by ssh/docker/local/device sessions; it is written verbatim to the new shell's standard input, and when it contains newlines it runs as multiple commands in sequence; unsupported kinds such as winrm are rejected, in which case create the tab first and then send the command with send_keys passing tab_id). Returns the new tab's tab_id and stable handle; by default the tab is shown in the foreground of the user's workspace, the AI works in it, and the user can take over input at any time; with background=true the tab is created detached in the background instead, and the user can take over in 「后台会话」 (Background Sessions). Either way, send_keys/read_screen/wait_for with tab_id can later operate tabs created by this task, including tabs on another owner-checked session.",
	"terminal_rename":  "Rename a terminal tab in the current scope. Custom titles are not overwritten by remote title sequences and are preserved after background session reconnection.",
	"terminal_list":    "List the open terminal tabs visible to the current owner with their stable handles, kinds, assets, titles, shell-reported cwds, and lifecycle state, paged by cursor/limit; exited tabs are hidden unless include_exited=true (the result reports how many were hidden); handles resolve to the exact tab and session they were issued for and can be passed as tab_id. Unmarked tabs accept send_keys/read_screen/wait_for/terminal_rename; tabs marked read_only were @-referenced by the user and accept only read_screen/wait_for; tabs marked outside_scope cannot be used until the user @-references them, so ask the user instead of retrying.",
	"operation_result": "Query the recorded outcome of one uncertain mutation by the operation_id a tool result returned (terminal_create, send_keys, and replayed or unrecorded results of other mutating tools): the operation's state (initiated/succeeded/failed), failure stage, residual session or resource left behind, error, and the recorded result payload. Use it to check the original operation's result before deciding whether to retry; it never implies the operation ran exactly once. No record means the operation was not recorded or is still running, not that it never ran: check the actual state before retrying.",
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
		FindFilesTool: func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[FindFilesArgs](FindFilesTool, einoDescriptions[FindFilesTool])
		},
		GrepFilesTool: func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[GrepFilesArgs](GrepFilesTool, einoDescriptions[GrepFilesTool])
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
		"terminal_list": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[TerminalListArgs]("terminal_list", einoDescriptions["terminal_list"])
		},
		"operation_result": func() (*schema.ToolInfo, error) {
			return utils.GoStruct2ToolInfo[OperationResultArgs]("operation_result", einoDescriptions["operation_result"])
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
	// PermissionSource, when set, is read at every tool call so a mode or rule
	// change applies to a run already in progress; Permission is then only the
	// value the run started with.
	PermissionSource func(context.Context) (guard.Config, error)
	Memory           *guard.Memory
	PlanMode         bool
	// Subagent marks the tool execution of a spawned subagent. It cannot pause
	// for the user, so a call that would ask for confirmation fails instead.
	Subagent  bool
	Subagents *SubagentConfig

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
	// Detail is technical context for Reason (such as a parser error) that
	// the card shows only on request.
	Detail   string
	Preview  *Preview
	Question *Question

	// Commands carries the full command list of exec_commands for the confirm dialog to display in full.
	Commands []string
	// RulePattern is the path scope an "always allow" decision will grant; empty means the action is not path-limited.
	RulePattern string
	// SessionScope describes what a session approval covers; empty means the
	// card offers no session approval.
	SessionScope string
	// PermanentScope describes what a permanent grant of this call covers; empty
	// means the card offers no permanent grant and the backend refuses one.
	PermanentScope string
	// RequiredEnvironmentNames are the production environment names the
	// confirmation must type back, computed from the real target set (for
	// example a multi-target exec batch challenge).
	RequiredEnvironmentNames []string
}

type InteractionState struct {
	Kind           string
	CallID         string
	MemoryKind     guard.Kind
	MemoryKinds    []guard.Kind
	MemoryPrograms []string
	TerminalInput  string
	TerminalCursor int
	Info           Interaction

	AuthorizationID     string
	Authorization       *guard.AuthorizationBinding
	Target              *TargetBinding
	Resource            guard.Resource
	IsolatedTarget      bool
	ProductionChallenge *ExecBatchProductionChallenge
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
			return utils.InferTool(FindFilesTool, einoDescriptions[FindFilesTool], func(ctx context.Context, input FindFilesArgs) (Output, error) {
				return e.run(ctx, FindFilesTool, input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool(GrepFilesTool, einoDescriptions[GrepFilesTool], func(ctx context.Context, input GrepFilesArgs) (Output, error) {
				return e.run(ctx, GrepFilesTool, input)
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
		func() (tool.InvokableTool, error) {
			return utils.InferTool("terminal_list", einoDescriptions["terminal_list"], func(ctx context.Context, input TerminalListArgs) (Output, error) {
				return e.run(ctx, "terminal_list", input)
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool("operation_result", einoDescriptions["operation_result"], func(ctx context.Context, input OperationResultArgs) (Output, error) {
				return e.run(ctx, "operation_result", input)
			})
		},
	}
	result := make([]tool.BaseTool, 0, len(makers)+2)
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
	// load_skill is not in einoNames: the registry only advertises it when a
	// skills backend is configured, and the description comes from that
	// backend's schema so the model sees one contract on both surfaces.
	if e.enabled("load_skill") {
		loadSkill, err := utils.InferTool("load_skill", e.Registry.deps.Skills.LoadSkillSchema().Description, func(ctx context.Context, input LoadSkillArgs) (Output, error) {
			return e.run(ctx, "load_skill", input)
		})
		if err != nil {
			return nil, err
		}
		result = append(result, loadSkill)
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

// DeprecatedToolHandler answers calls to the deprecated search_files, which is no longer offered to the model but
// may still be called from an older conversation, by running it as find_files or grep_files. Every other unknown
// name goes to next.
func (e *Execution) DeprecatedToolHandler(next UnknownToolHandler) UnknownToolHandler {
	return func(ctx context.Context, name, input string) (string, error) {
		if name != SearchFilesAlias || !e.enabled(SearchFilesAlias) {
			return next(ctx, name, input)
		}
		var args SearchFilesArgs
		output := Output{}
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			output = searchInvalid("search_files 的参数不是有效 JSON；search_files 已弃用，请改用 find_files 或 grep_files",
				"the search_files arguments are not valid JSON; search_files is deprecated, use find_files or grep_files instead")
		} else {
			var err error
			output, err = e.run(ctx, SearchFilesAlias, args)
			if err != nil {
				// The unknown-tool path cannot pause for a confirmation; the new tools can.
				output = searchFailure("search_files 已弃用，本次调用未执行，请改用 find_files 或 grep_files: "+err.Error(),
					"search_files is deprecated and this call was not run; use find_files or grep_files instead: "+err.Error())
			}
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return "", err
		}
		// Unknown tools bypass the tool middleware, so adk emits no result event; send it here so the run loop
		// persists and shows the result like any other.
		message := schema.ToolMessage(string(encoded), compose.GetToolCallID(ctx), schema.WithToolName(name))
		adk.EnsureMessageID(message)
		_ = adk.SendEvent(ctx, adk.EventFromMessage(message, nil, schema.Tool, name))
		return string(encoded), nil
	}
}

func (e *Execution) enabled(name string) bool {
	if e.Registry == nil || e.PlanMode && !PlanAllowed(name) {
		return false
	}
	deps := e.Registry.deps
	switch name {
	case "exec_commands":
		return e.Scope.SessionID != "" && (deps.Transport != nil || deps.ExecBatch != nil && deps.ResolveExecTargets != nil)
	case "read_file", "write_file", "list_dir", FindFilesTool, GrepFilesTool, SearchFilesAlias, "edit_file":
		return deps.Transport != nil && e.Scope.SessionID != ""
	case "read_screen", "send_keys", "wait_for":
		return deps.Terminal != nil && e.Scope.TabID != ""
	case "terminal_rename":
		_, ok := deps.Terminal.(RenameTerminal)
		return ok && e.Scope.TabID != ""
	case "terminal_list":
		if e.Scope.SessionID == "" {
			return false
		}
		_, ok := deps.Terminal.(ListTabsTerminal)
		return ok
	case "operation_result":
		return deps.DurableOutcomes != nil
	case "terminal_create":
		if e.Scope.SessionID == "" {
			return false
		}
		_, ok := deps.Terminal.(CreateTerminal)
		return ok
	case "shell_history":
		return e.Registry.shellHistoryScopeError(context.Background(), e.Scope) == nil
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
	case "load_skill":
		return skillLoaderConfigured(deps.Skills)
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
	limits.ToolSafety = func(allowed []string) (adk.ChatModelAgentMiddleware, func(context.Context, string, string) (string, error)) {
		handler, unknown := NewToolSafety(allowed)
		child := &Execution{JobID: e.JobID, ConversationID: e.ConversationID, Registry: e.Registry, Scope: e.Scope, Permission: e.Permission, PermissionSource: e.PermissionSource, Memory: e.Memory, PlanMode: e.PlanMode, Subagent: true}
		if !slices.Contains(allowed, FindFilesTool) || !slices.Contains(allowed, GrepFilesTool) {
			return handler, unknown
		}
		return handler, child.DeprecatedToolHandler(unknown)
	}
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
		// A subagent failure must not end the parent run; only the parent's
		// own cancellation propagates as an error.
		if ctx.Err() != nil {
			return "", err
		}
		encoded, marshalErr := json.Marshal(Fail(err))
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(encoded), nil
	}
	current := OK(result)
	var spawned subagent.SpawnOutput
	if json.Unmarshal([]byte(result), &spawned) == nil {
		current = Output{OK: spawned.OK, Text: spawned.Text, ModelText: spawned.ModelText, ExitCode: spawned.ExitCode}
		if !current.OK && current.ExitCode == 0 {
			current.ExitCode = 1
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (e *Execution) scopedSubagentTools(ctx context.Context, manager *subagent.Manager) ([]tool.BaseTool, error) {
	child := &Execution{JobID: e.JobID, ConversationID: e.ConversationID, Registry: e.Registry, Scope: e.Scope, Permission: e.Permission, PermissionSource: e.PermissionSource, Memory: e.Memory, PlanMode: e.PlanMode, Subagent: true}
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
	// A list written before search_files was split names the old tool; it grants both of its replacements.
	expanded := make([]string, 0, len(configured)+1)
	for _, name := range configured {
		if strings.TrimSpace(name) == SearchFilesAlias {
			expanded = append(expanded, FindFilesTool, GrepFilesTool)
			continue
		}
		expanded = append(expanded, name)
	}
	allowed := make([]string, 0, len(expanded))
	seen := make(map[string]struct{}, len(expanded))
	for _, name := range expanded {
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
	return runWatched(ctx, name, toolWatchdog(input), func(ctx context.Context) (Output, error) {
		return e.runInner(ctx, name, input)
	})
}

func (e *Execution) runInner(ctx context.Context, name string, input any) (Output, error) {
	if e.PermissionSource != nil {
		live, err := e.PermissionSource(ctx)
		if err != nil {
			return Fail(errors.New("读取 AI 权限设置失败: " + err.Error())), nil
		}
		// A per-call copy keeps the shared execution free of writes from concurrent calls.
		current := *e
		current.Permission = live.Normalized()
		e = &current
	}
	ctx = e.screenMemoryContext(ctx)
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
		// The resume payload is structured: the decision plus the user's own
		// typed environment names, validated against the challenge's required
		// set by the hitl manager before the resume proceeds.
		var resume hitl.ResumeValue
		if err := json.Unmarshal([]byte(data), &resume); err != nil {
			return Fail(errors.New("确认结果无效，操作未执行")), nil
		}
		decision := resume.Decision
		if decision == "deny" {
			return Fail(errors.New("用户拒绝了此操作")), nil
		}
		if decision != "allow" && decision != "allow_session" && decision != "allow_persistent" {
			return Fail(errors.New("确认结果无效，操作未执行")), nil
		}
		if state.ProductionChallenge != nil && decision != "allow" {
			return Fail(errors.New("生产环境多主机执行只允许本次确认，操作未执行")), nil
		}
		if state.IsolatedTarget && decision == "allow_persistent" {
			return Fail(errors.New("跨资产操作不能使用当前主机的永久授权，请仅本次确认")), nil
		}
		if call.Name == "send_keys" {
			var input SendKeysArgs
			if err := json.Unmarshal(call.Args, &input); err != nil {
				return Fail(err), nil
			}
			tabID, err := e.Registry.resolveTab(ctx, e.JobID, e.Scope, input.TabID)
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
			// The user confirmed against exactly this input; sync the
			// arbitration baseline so the confirmed write is not refused as
			// unseen new input.
			e.Registry.state(e.JobID).observeInput(tabID, buffered, cursor)
		}
		if decision == "allow_session" {
			if guard.RuleActionNeedsPath(state.Resource.Action) && state.Resource.Path != "" {
				e.Memory.AddPathRule(state.Resource.Action, guard.DirPattern(state.Resource.Path))
			} else {
				e.Memory.Remember(state.SessionRuling())
			}
		}
		if decision == "allow_persistent" {
			if err := e.persistPermanentGrant(ctx, state); err != nil {
				return Fail(err), nil
			}
		}
		call.AuthorizationID = state.AuthorizationID
		call.Authorization = state.Authorization
		call.Target = state.Target
		call.OneTimeConfirmation = true
		if state.ProductionChallenge != nil {
			call.OriginID = state.Info.CallID
			call.ID += ":production"
			// Fill every confirmation with the user's typed names from the
			// resume value — never echoed from the challenge — so the exec
			// batch's exact-name validation sees the actual user input.
			confirmations := append([]execbatch.ProductionConfirmation(nil), state.ProductionChallenge.Confirmations...)
			for index := range confirmations {
				confirmations[index].EnvironmentNames = append([]string(nil), resume.EnvironmentNames...)
			}
			call.ProductionConfirmations = append(call.ProductionConfirmations, confirmations...)
		}
		if call.Name == ReminderTool {
			return e.executeReminder(ctx, call), nil
		}
		return e.executionResult(ctx, call, e.Registry.Execute(ctx, e.JobID, e.Scope, call, nil))
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
	if err := e.Registry.bindExecutionTarget(ctx, e.JobID, e.Scope, &call); err != nil {
		return Fail(err), nil
	}
	var terminalInput TerminalInput
	isolatedTarget := false
	ruling := guard.ClassifyTool(call.Name, call.Args, e.Permission)
	if call.Name == "shell_history" {
		if err := e.Registry.shellHistoryScopeError(ctx, e.Scope); err != nil {
			return Fail(err), nil
		}
		ruling = guard.Confirm(shellHistoryKind, "读取本机 shell 历史文件（历史常包含敏感命令，需要确认）")
	}
	if call.Name == "terminal_create" {
		var input TerminalCreateArgs
		if json.Unmarshal(call.Args, &input) == nil {
			isolatedTarget = input.AssetID != ""
		}
	}
	if call.Name == "exec_commands" {
		var input ExecCommandsArgs
		if json.Unmarshal(call.Args, &input) == nil {
			isolatedTarget = len(input.AssetIDs) > 0 || len(input.Tags) > 0
		}
	}
	if call.Name == "send_keys" {
		var input SendKeysArgs
		if err := json.Unmarshal(call.Args, &input); err != nil {
			return Fail(err), nil
		}
		tabID, err := e.Registry.resolveTab(ctx, e.JobID, e.Scope, input.TabID)
		if err != nil {
			return Fail(err), nil
		}
		// Refuse before asking for confirmation: these calls can never write.
		if err := e.Registry.takeoverWriteError(tabID); err != nil {
			return Fail(err), nil
		}
		if !input.Paste {
			if _, err := EncodeKeys(input.Keys, input.Enter); err != nil {
				return Fail(err), nil
			}
		}
		if input.TabID != "" && input.TabID != e.Scope.TabID {
			sessionID, err := e.Registry.tabSession(ctx, tabID)
			if err != nil {
				return Fail(err), nil
			}
			isolatedTarget = sessionID != e.Scope.SessionID
		}
		screen, err := e.Registry.deps.Terminal.Snapshot(ctx, tabID)
		if err != nil {
			return Fail(err), nil
		}
		terminalInput = ReadTerminalInput(screen)
		ruling = guard.Worst(ruling, ClassifyTerminalKeys(input.Keys, input.Enter, terminalInput, e.Permission.DangerRules))
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
	if isolatedTarget && ruling.Risk != guard.Safe && ruling.Risk != guard.Forbidden {
		reason := "跨资产或多主机操作需要针对本次目标确认，不能沿用当前主机授权"
		if e.Permission.Mode == guard.ReadOnly || e.Permission.Mode == guard.Unattended {
			decision.Action = guard.ActionDeny
		} else {
			decision.Action = guard.ActionAsk
		}
		ruling.Reason = reason
		ruling.Detail = ""
		decision.Ruling.Reason = reason
		decision.Reason = reason
	}
	if decision.Action == guard.ActionDeny {
		return Fail(errors.New("权限策略已拒绝: " + decision.Ruling.Reason)), nil
	}
	if decision.Action == guard.ActionAsk && e.Subagent {
		return Fail(subagentConfirmationError(ruling.Reason)), nil
	}
	preparation, err := e.Registry.Prepare(ctx, e.JobID, e.Scope, call)
	if err != nil {
		return Fail(err), nil
	}
	if decision.Action == guard.ActionAsk {
		resource := resourceForCall(call)
		rendered := DisplayCall(call)
		if call.Name == "send_keys" {
			pending, cursor := terminalInput.DisplayInput()
			rendered = DisplaySendKeys(call, pending, cursor)
		}
		info := Interaction{Kind: "confirm", CallID: call.ID, Tool: call.Name, Args: string(call.Args), Risk: ruling.Risk.String(), Rendered: rendered, Reason: ruling.Reason, Detail: ruling.Detail, Preview: preparation.Preview}
		if ruling.Risk == guard.NeedsConfirm && guard.RuleActionSupported(resource.Action) {
			info.RulePattern = persistentRulePattern(resource)
			if call.Name == "exec_commands" {
				var input ExecCommandsArgs
				if json.Unmarshal(call.Args, &input) == nil {
					info.Commands = input.Commands
				}
			}
		}
		if !forcedConfirm && !isolatedTarget && !decision.Production {
			info.SessionScope = SessionScope(ruling, resource)
		}
		if ruling.Risk == guard.NeedsConfirm && guard.RuleActionSupported(resource.Action) && !forcedConfirm && !isolatedTarget && !decision.Production && e.grantDeviceID(ctx) != "" {
			info.PermanentScope = guard.PermanentScope(ruling, resource)
		}
		state := InteractionState{Kind: "confirm", CallID: call.ID, MemoryKind: ruling.Kind, MemoryKinds: ruling.ApprovalKinds(), MemoryPrograms: ruling.Programs, TerminalInput: terminalInput.Text, TerminalCursor: terminalInput.Cursor, Info: info, AuthorizationID: GuardAuthorizationID(decision), Authorization: decision.Authorization, Target: call.Target, Resource: resource, IsolatedTarget: isolatedTarget}
		if forcedConfirm || isolatedTarget {
			state.MemoryKind = ""
			state.MemoryKinds = nil
			state.MemoryPrograms = nil
		}
		return Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	call.AuthorizationID = GuardAuthorizationID(decision)
	call.Authorization = decision.Authorization
	if call.Name == ReminderTool {
		return e.executeReminder(ctx, call), nil
	}
	result := e.Registry.Execute(ctx, e.JobID, e.Scope, call, preparation)
	return e.executionResult(ctx, call, result)
}

func (e *Execution) executionResult(ctx context.Context, call Call, result Output) (Output, error) {
	if challenge := result.ProductionChallenge; challenge != nil {
		if e.Permission.Mode == guard.Unattended {
			return Fail(errors.New("生产环境多主机执行需要用户确认，无人值守运行禁止此操作")), nil
		}
		if e.Subagent {
			return Fail(subagentConfirmationError(result.Text)), nil
		}
		targets := make([]string, 0, len(challenge.Detail.ProductionTargets))
		for _, target := range challenge.Detail.ProductionTargets {
			targets = append(targets, target.SessionName+" ("+target.Environment+")")
		}
		reason := result.Text
		if len(targets) > 0 {
			reason += "；生产目标：" + strings.Join(targets, ", ")
		}
		info := Interaction{Kind: "confirm", CallID: call.ID, Tool: call.Name, Args: string(call.Args), Risk: guard.Danger.String(), Rendered: DisplayCall(call), Reason: reason, RequiredEnvironmentNames: append([]string(nil), challenge.Detail.RequiredEnvironmentNames...)}
		if call.Name == "exec_commands" {
			var input ExecCommandsArgs
			if json.Unmarshal(call.Args, &input) == nil {
				info.Commands = input.Commands
			}
		}
		state := InteractionState{Kind: "confirm", CallID: call.ID, Info: info, AuthorizationID: call.AuthorizationID, Authorization: call.Authorization, Target: call.Target, Resource: resourceForCall(call), IsolatedTarget: true, ProductionChallenge: challenge}
		return Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	if result.Question != nil {
		info := Interaction{Kind: "question", CallID: call.ID, Tool: call.Name, Args: string(call.Args), Question: result.Question}
		state := InteractionState{Kind: "question", CallID: call.ID, Info: info}
		return Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	return result, nil
}

// subagentConfirmationError tells a subagent model that a call needing user
// confirmation was not run, so it hands the action back to the parent agent,
// which can ask the user.
func subagentConfirmationError(reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "未说明原因"
	}
	return errors.New("此操作需要用户确认（" + reason + "），但子代理内无法请求确认，操作未执行。不要重试；请在结果中说明需要执行的操作，由主代理执行（主代理可以请求用户确认）")
}

// SessionRuling rebuilds the ordinary confirmation a session approval records.
func (s InteractionState) SessionRuling() guard.Ruling {
	return guard.Ruling{Risk: guard.NeedsConfirm, Kind: s.MemoryKind, Kinds: s.MemoryKinds, Programs: s.MemoryPrograms}
}

// SessionScope describes what "本会话允许" covers for an ordinary confirmation:
// writes under the file's directory, or the ruling's kinds and programs.
func SessionScope(ruling guard.Ruling, resource guard.Resource) string {
	if ruling.Risk != guard.NeedsConfirm {
		return ""
	}
	if guard.RuleActionNeedsPath(resource.Action) && resource.Path != "" {
		pattern := guard.DirPattern(resource.Path)
		if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
			return "修改 " + dir + " 下的文件"
		}
		return "修改 " + pattern
	}
	return guard.SessionScope(ruling)
}

func errorsNewPlanModeOnly() error {
	return errors.New("exit_plan_mode 仅在计划模式可用")
}
