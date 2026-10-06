package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
)

type Registry struct {
	deps    Dependencies
	mu      sync.Mutex
	jobs    map[string]*jobState
	schemas []Schema
}

func NewRegistry(deps Dependencies) *Registry {
	return &Registry{deps: deps, jobs: make(map[string]*jobState), schemas: toolSchemas()}
}

func PlanAllowed(name string) bool {
	switch name {
	case "read_file", "list_dir", "search_files", "read_screen", "wait_for", "list_assets", "docker_ps", "docker_logs", "db_list_tables", "db_describe", "redis_scan", "ask_user", "todo_write", "exit_plan_mode":
		return true
	default:
		return false
	}
}

func (r *Registry) Release(jobID string) {
	r.mu.Lock()
	delete(r.jobs, jobID)
	r.mu.Unlock()
}

func (r *Registry) state(jobID string) *jobState {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.jobs[jobID]
	if state == nil {
		state = newJobState()
		r.jobs[jobID] = state
	}
	return state
}

func ValidateCall(schema Schema, args json.RawMessage) error {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid("JSON: %v", err)
	}
	if decoder.More() {
		return invalid("JSON 后存在多余内容")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return invalid("参数必须是 JSON 对象")
	}
	properties, _ := schema.Parameters["properties"].(map[string]any)
	for key := range object {
		if _, ok := properties[key]; !ok {
			return invalid("未知参数 %s", key)
		}
	}
	required := requiredStrings(schema.Parameters["required"])
	for _, key := range required {
		if _, ok := object[key]; !ok {
			return invalid("缺少必填参数 %s", key)
		}
	}
	for key, raw := range object {
		definition, _ := properties[key].(map[string]any)
		kind, _ := definition["type"].(string)
		if !validJSONType(raw, kind) {
			return invalid("参数 %s 应为 %s", key, kind)
		}
		if enum, ok := definition["enum"].([]any); ok {
			text, _ := raw.(string)
			valid := false
			for _, candidate := range enum {
				valid = valid || text == candidate
			}
			if !valid {
				return invalid("参数 %s 的值 %q 不在允许范围", key, text)
			}
		}
	}
	return nil
}

func requiredStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func validJSONType(value any, kind string) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Float64()
		return err == nil
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	default:
		return true
	}
}

func (r *Registry) Validate(call Call) error {
	for _, schema := range r.schemas {
		if schema.Name == call.Name {
			return ValidateCall(schema, call.Args)
		}
	}
	return invalid("未知工具 %s", call.Name)
}

func (r *Registry) Prepare(ctx context.Context, jobID string, scope Scope, call Call) (*Preparation, error) {
	if err := r.Validate(call); err != nil {
		return nil, err
	}
	if call.Name != "write_file" && call.Name != "edit_file" {
		return &Preparation{}, nil
	}
	change, preview, err := r.prepareFile(ctx, jobID, scope, call)
	if err != nil {
		return nil, err
	}
	if change != nil {
		r.state(jobID).putPreparation(call.ID, change)
	}
	return &Preparation{Preview: preview, change: change}, nil
}

func (r *Registry) Execute(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) (result Output) {
	started := r.deps.now()
	if err := r.Validate(call); err != nil {
		return Fail(err)
	}
	if ctx.Err() != nil {
		return Fail(ctx.Err())
	}
	if r.deps.Outcome != nil && call.AuthorizationID != "" && sideEffectKind(call.Name) != "" {
		result = r.executeRecorded(ctx, jobID, scope, call, preparation)
	} else {
		result = r.execute(ctx, jobID, scope, call, preparation)
	}
	if r.deps.Audit != nil {
		entry := AuditEntry{SessionID: scope.SessionID, AssetID: scope.AssetID, Kind: "exec", ExitCode: result.ExitCode,
			DurationMS: r.deps.now().Sub(started).Milliseconds(), Payload: map[string]any{"tool": call.Name, "args": redactArguments(call.Args), "run_id": jobID, "call_id": call.ID}}
		_ = r.deps.Audit(context.WithoutCancel(ctx), entry)
	}
	return result
}

func sideEffectKind(name string) outcome.Kind {
	switch name {
	case "exec_commands", "send_keys", "docker_exec", "docker_control":
		return outcome.KindCommand
	case "write_file", "edit_file":
		return outcome.KindFile
	case "db_query":
		return outcome.KindDatabase
	default:
		return ""
	}
}

func OutcomeKey(jobID, callID string) string {
	return strconv.Itoa(len(jobID)) + ":" + jobID + callID
}

func GuardAuthorizationID(decision guard.Decision) string {
	encoded, err := json.Marshal(decision)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "guard:" + hex.EncodeToString(sum[:])
}

func (r *Registry) executeRecorded(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	request := outcome.Request{
		IdempotenceKey:  OutcomeKey(jobID, call.ID),
		AuthorizationID: call.AuthorizationID,
		Kind:            sideEffectKind(call.Name),
		Arguments:       call.Args,
	}
	var (
		ranEffect bool
		branch    Output
	)
	record, err := r.deps.Outcome.Execute(ctx, request, func(effectCtx context.Context) (outcome.Completion, error) {
		ranEffect = true
		branch = r.execute(effectCtx, jobID, scope, call, preparation)
		return completionFromOutput(branch)
	})
	switch {
	case err == nil && !ranEffect:
		return replayOutput(record)
	case err == nil:
		return branch
	case errors.Is(err, outcome.ErrInProgress):
		return Fail(fmt.Errorf("相同调用正在执行中，未重复执行: %w", err))
	case errors.Is(err, outcome.ErrIdempotenceConflict):
		return Fail(fmt.Errorf("调用与已记录的执行冲突（授权或参数不一致）: %w", err))
	case !ranEffect:
		return Fail(fmt.Errorf("执行结果记录失败: %w", err))
	case record.State != outcome.ExecutionFinished || record.Audit.State == outcome.AuditPending:

		return Fail(fmt.Errorf("执行已发生但结果未能记录: %w", err))
	case record.Audit.State == outcome.AuditFailed:
		branch.Text += "\n[结果审计写入失败: " + record.Audit.Error + "]"
		return branch
	default:
		return branch
	}
}

const execFailureMarker = "[执行失败:"

func completionFromOutput(output Output) (outcome.Completion, error) {
	if !output.OK {
		return outcome.Completion{}, errors.New(output.Text)
	}
	if output.Truncated || strings.Contains(output.Text, execFailureMarker) {
		return outcome.Completion{}, errors.New("执行输出被截断或传输失败，外部结果不明确")
	}
	code := output.ExitCode
	if code != 0 {
		return outcome.Completion{Outcome: outcome.OutcomeFailed, ExitCode: &code}, nil
	}
	return outcome.Completion{Outcome: outcome.OutcomeAccepted, ExitCode: &code}, nil
}

func replayOutput(record outcome.Record) Output {
	text := fmt.Sprintf("调用已执行过，返回账本记录的结果（outcome=%s）", record.Outcome)
	switch record.Outcome {
	case outcome.OutcomeAccepted:
		return Output{OK: true, Text: text}
	case outcome.OutcomeFailed:
		code := 1
		if record.Result.ExitCode != nil {
			code = *record.Result.ExitCode
		}
		return Output{OK: true, Text: text, ExitCode: code}
	default:
		if record.Result.Error != "" {
			text += ": " + record.Result.Error
		}
		return Output{Text: text, ExitCode: 1}
	}
}

func (r *Registry) execute(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	output, err := Guarded(ctx, call.Name, func() (Output, error) {
		return r.executeTool(ctx, jobID, scope, call, preparation), nil
	})
	if err != nil {
		return Fail(err)
	}
	return output
}

func (r *Registry) executeTool(ctx context.Context, jobID string, scope Scope, call Call, preparation *Preparation) Output {
	switch call.Name {
	case "exec_commands":
		return r.execCommands(ctx, scope, call.Args)
	case "read_file":
		return r.readFile(ctx, jobID, scope, call.Args)
	case "write_file", "edit_file":
		return r.writePrepared(ctx, jobID, scope, call, preparation)
	case "list_dir":
		return r.listDir(ctx, scope, call.Args)
	case "search_files":
		return r.searchFiles(ctx, scope, call.Args)
	case "read_screen":
		return r.readScreen(ctx, scope, call.Args)
	case "send_keys":
		return r.sendKeys(ctx, scope, call.Args)
	case "wait_for":
		return r.waitFor(ctx, scope, call.Args, 120)
	case "shell_history":
		return r.shellHistory(ctx, scope, call.Args)
	case "docker_ps":
		return r.dockerPS(ctx, scope)
	case "docker_logs":
		return r.dockerLogs(ctx, scope, call.Args)
	case "docker_exec":
		return r.dockerExec(ctx, scope, call.Args)
	case "docker_control":
		return r.dockerControl(ctx, scope, call.Args)
	case "db_list_tables":
		return r.dbListTables(ctx, scope, call.Args)
	case "db_describe":
		return r.dbDescribe(ctx, scope, call.Args)
	case "db_query":
		return r.dbQuery(ctx, scope, call.Args)
	case "redis_scan":
		return r.redisScan(ctx, scope, call.Args)
	case "list_assets":
		return r.listAssets(ctx)
	case "ask_user":
		return askUser(call.Args)
	case "todo_write":
		return r.todoWrite(jobID, call.Args)
	case "exit_plan_mode":
		return r.exitPlanMode(jobID, call.Args)
	default:
		return Fail(fmt.Errorf("未知工具 %s", call.Name))
	}
}

func decode(args json.RawMessage, target any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid("%v", err)
	}
	return nil
}

func toolSchemas() []Schema {
	return buildSchemas()
}
