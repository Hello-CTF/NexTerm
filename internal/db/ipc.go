package db

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type connectionRequest struct {
	ConnID string `json:"connId"`
}

type disconnectRequest struct {
	ConnID string `json:"connId"`
}

type tablesRequest struct {
	ConnID string `json:"connId"`
	Schema string `json:"schema,omitempty"`
}

type columnsRequest struct {
	ConnID string `json:"connId"`
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table"`
}

type queryRequest struct {
	ConnID    string  `json:"connId"`
	SQL       string  `json:"sql"`
	Limit     *uint64 `json:"limit,omitempty"`
	TimeoutMS *uint64 `json:"timeoutMs,omitempty"`
}

type redisScanRequest struct {
	ConnID  string  `json:"connId"`
	Cursor  *uint64 `json:"cursor,omitempty"`
	Pattern *string `json:"pattern,omitempty"`
	Count   *uint64 `json:"count,omitempty"`
}

type redisKeyRequest struct {
	ConnID string `json:"connId"`
	Key    string `json:"key"`
}

type redisCommandRequest struct {
	ConnID string   `json:"connId"`
	Args   []string `json:"args"`
}

type redisTTLRequest struct {
	ConnID  string `json:"connId"`
	Key     string `json:"key"`
	Seconds int64  `json:"seconds"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := registerCommand(dispatcher, "db_connect", true, func(ctx context.Context, input ConnectArgs) (ConnectResult, error) {
		return s.Connect(ctx, input)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "db_disconnect", false, func(_ context.Context, input disconnectRequest) (any, error) {
		return nil, s.Disconnect(input.ConnID)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "db_schemas", false, func(ctx context.Context, input connectionRequest) ([]string, error) {
		return s.Schemas(ctx, input.ConnID)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "db_tables", false, func(ctx context.Context, input tablesRequest) ([]string, error) {
		return s.Tables(ctx, input.ConnID, input.Schema)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "db_columns", false, func(ctx context.Context, input columnsRequest) (TableDescribe, error) {
		return s.Describe(ctx, input.ConnID, input.Schema, input.Table)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "db_query", false, func(ctx context.Context, input queryRequest) (QueryResult, error) {
		var limit uint64
		if input.Limit != nil {
			limit = *input.Limit
		}
		var timeout time.Duration
		if input.TimeoutMS != nil && *input.TimeoutMS != 0 {
			if *input.TimeoutMS > uint64(math.MaxInt64/int64(time.Millisecond)) {
				return QueryResult{}, badParam(errors.New("timeoutMs 超出范围"))
			}
			timeout = time.Duration(*input.TimeoutMS) * time.Millisecond
		}
		return s.Query(ctx, input.ConnID, input.SQL, limit, timeout)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "redis_scan", false, func(ctx context.Context, input redisScanRequest) (RedisScanResult, error) {
		var cursor, count uint64
		pattern := "*"
		if input.Cursor != nil {
			cursor = *input.Cursor
		}
		if input.Pattern != nil {
			pattern = *input.Pattern
		}
		if input.Count != nil {
			count = *input.Count
		}
		return s.RedisScan(ctx, input.ConnID, cursor, pattern, count)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "redis_inspect", false, func(ctx context.Context, input redisKeyRequest) (RedisKeyView, error) {
		return s.RedisInspect(ctx, input.ConnID, input.Key)
	}); err != nil {
		return err
	}
	if err := registerCommand(dispatcher, "redis_command", false, func(ctx context.Context, input redisCommandRequest) (string, error) {
		return s.RedisCommand(ctx, input.ConnID, input.Args)
	}); err != nil {
		return err
	}
	return registerCommand(dispatcher, "redis_set_ttl", false, func(ctx context.Context, input redisTTLRequest) (any, error) {
		return nil, s.RedisSetTTL(ctx, input.ConnID, input.Key, input.Seconds)
	})
}

func registerCommand[In, Out any](dispatcher *ipc.Dispatcher, name string, nested bool, handler func(context.Context, In) (Out, error)) error {
	command := func(ctx context.Context, _ *ipc.Call, input In) (Out, error) {
		return handler(ctx, input)
	}
	if nested {
		return ipc.RegisterNested(dispatcher, name, command)
	}
	return ipc.Register(dispatcher, name, command)
}
