package ipc

import (
	"context"
	"encoding/json"
)

type CommandFunc[In, Out any] func(context.Context, *Call, In) (Out, error)

func Register[In, Out any](dispatcher *Dispatcher, name string, handler CommandFunc[In, Out]) error {
	return register(dispatcher, name, handler, false)
}

func RegisterNested[In, Out any](dispatcher *Dispatcher, name string, handler CommandFunc[In, Out]) error {
	return register(dispatcher, name, handler, true)
}

func register[In, Out any](dispatcher *Dispatcher, name string, handler CommandFunc[In, Out], nested bool) error {
	if handler == nil {
		return dispatcher.RegisterRaw(name, nil)
	}
	return dispatcher.RegisterRaw(name, func(ctx context.Context, call *Call) (any, error) {
		raw := call.Args
		if nested {
			var envelope struct {
				Args json.RawMessage `json:"args"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return nil, BadParam(err)
			}
			raw = envelope.Args
			if len(raw) == 0 {
				raw = json.RawMessage("null")
			}
		}

		var input In
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, BadParam(err)
		}
		return handler(ctx, call, input)
	})
}
