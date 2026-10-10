package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

var syncObjectWriteCommands = map[string]bool{
	"asset_create": true, "asset_update": true, "asset_delete": true,
	"group_create": true, "group_update": true, "group_delete": true,
	"snippet_create": true, "snippet_update": true, "snippet_delete": true,
	"vault_set_credential": true, "credential_update": true, "vault_delete_credential": true,
	"known_host_accept": true, "known_host_remove": true,
	"ai_model_save": true, "ai_model_delete": true,
	"terminal_record_stop": true, "transcript_delete": true, "transcript_sync_opt_in": true,
	"sync_link_set": true, "sync_kind_opt_in_set": true, "sync_import": true, "ssh_import_apply": true,
}

type Environment struct {
	ClientID string
	Events   Emitter
	Streams  StreamFactory
}

type Call struct {
	Command  string
	Args     json.RawMessage
	Channel  ChannelRef
	ClientID string
	Events   Emitter
	Streams  StreamFactory
}

type Handler func(context.Context, *Call) (any, error)

type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: make(map[string]Handler)}
}

func (d *Dispatcher) RegisterRaw(name string, handler Handler) error {
	if name == "" {
		return fmt.Errorf("command name is empty")
	}
	if handler == nil {
		return fmt.Errorf("command %s has no handler", name)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.handlers[name]; exists {
		return fmt.Errorf("command %s is already registered", name)
	}
	d.handlers[name] = handler
	return nil
}

func (d *Dispatcher) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers)
}

func (d *Dispatcher) Commands() []string {
	d.mu.RLock()
	commands := make([]string, 0, len(d.handlers))
	for name := range d.handlers {
		commands = append(commands, name)
	}
	d.mu.RUnlock()
	sort.Strings(commands)
	return commands
}

func (d *Dispatcher) Dispatch(ctx context.Context, request Request, environment Environment) (response Response) {
	response = Failure(NormalizeError(fmt.Errorf("command dispatch did not run")))
	defer func() {
		if recovered := recover(); recovered != nil {
			response = Failure(WrapError(CodeInternal, "处理请求时发生内部异常，请重试", fmt.Errorf("command %s panicked: %v", request.Command, recovered)))
		}
	}()

	d.mu.RLock()
	handler, ok := d.handlers[request.Command]
	d.mu.RUnlock()
	if !ok {
		return Failure(WrapError(CodeNotFound, "当前版本不支持该操作，请刷新或更新应用后重试", fmt.Errorf("unknown command %q", request.Command)))
	}

	args := request.Args
	if len(args) == 0 {
		args = json.RawMessage("null")
	}
	call := &Call{
		Command:  request.Command,
		Args:     args,
		Channel:  request.Channel,
		ClientID: environment.ClientID,
		Events:   environment.Events,
		Streams:  environment.Streams,
	}
	if call.ClientID == "" {
		call.ClientID = request.ClientID
	}

	result, err := handler(ctx, call)
	if err != nil {
		return Failure(NormalizeError(err))
	}
	if syncObjectWriteCommands[request.Command] {
		_ = Emit(ctx, call.Events, TopicSyncStatus, struct{}{})
	}
	data, err := json.Marshal(result)
	if err != nil {
		return Failure(NormalizeError(fmt.Errorf("marshal command %s result: %w", request.Command, err)))
	}
	return Response{OK: true, Data: data}
}

func Failure(err *Error) Response {
	return Response{OK: false, Error: err}
}
