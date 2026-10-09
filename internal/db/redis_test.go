package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	redisDriver "github.com/redis/go-redis/v9"
)

type fakeRedis struct {
	keyType string
	ttl     time.Duration

	stringValue string
	stringErr   error
	hashValue   map[string]string
	listValue   []string
	setValue    []string
	zsetValue   []redisDriver.Z
	streamValue []redisDriver.XMessage

	scanCursor uint64
	scanKeys   []string
	scanErr    error
	lastMatch  string
	lastCount  int64

	doFunc  func(...any) (any, error)
	doCalls [][]any
}

func (f *fakeRedis) Scan(_ context.Context, _ uint64, match string, count int64) *redisDriver.ScanCmd {
	f.lastMatch = match
	f.lastCount = count
	cmd := redisDriver.NewScanCmd(context.Background(), nil)
	cmd.SetVal(f.scanKeys, f.scanCursor)
	cmd.SetErr(f.scanErr)
	return cmd
}

func (f *fakeRedis) Type(context.Context, string) *redisDriver.StatusCmd {
	cmd := redisDriver.NewStatusCmd(context.Background())
	cmd.SetVal(f.keyType)
	return cmd
}

func (f *fakeRedis) TTL(context.Context, string) *redisDriver.DurationCmd {
	cmd := redisDriver.NewDurationCmd(context.Background(), time.Second)
	cmd.SetVal(f.ttl)
	return cmd
}

func (f *fakeRedis) Get(context.Context, string) *redisDriver.StringCmd {
	cmd := redisDriver.NewStringCmd(context.Background())
	cmd.SetVal(f.stringValue)
	cmd.SetErr(f.stringErr)
	return cmd
}

func (f *fakeRedis) HGetAll(context.Context, string) *redisDriver.MapStringStringCmd {
	cmd := redisDriver.NewMapStringStringCmd(context.Background())
	cmd.SetVal(f.hashValue)
	return cmd
}

func (f *fakeRedis) LRange(context.Context, string, int64, int64) *redisDriver.StringSliceCmd {
	cmd := redisDriver.NewStringSliceCmd(context.Background())
	cmd.SetVal(f.listValue)
	return cmd
}

func (f *fakeRedis) SMembers(context.Context, string) *redisDriver.StringSliceCmd {
	cmd := redisDriver.NewStringSliceCmd(context.Background())
	cmd.SetVal(f.setValue)
	return cmd
}

func (f *fakeRedis) ZRangeWithScores(context.Context, string, int64, int64) *redisDriver.ZSliceCmd {
	cmd := redisDriver.NewZSliceCmd(context.Background())
	cmd.SetVal(f.zsetValue)
	return cmd
}

func (f *fakeRedis) XRangeN(context.Context, string, string, string, int64) *redisDriver.XMessageSliceCmd {
	cmd := redisDriver.NewXMessageSliceCmd(context.Background())
	cmd.SetVal(f.streamValue)
	return cmd
}

func (f *fakeRedis) Do(_ context.Context, args ...any) *redisDriver.Cmd {
	f.doCalls = append(f.doCalls, append([]any(nil), args...))
	cmd := redisDriver.NewCmd(context.Background())
	if f.doFunc != nil {
		value, err := f.doFunc(args...)
		cmd.SetVal(value)
		cmd.SetErr(err)
	}
	return cmd
}

func serviceWithFakeRedis(fake *fakeRedis) *Service {
	service := NewService(nil)
	service.conns["redis"] = &redisConnection{client: fake, console: fake}
	return service
}

func TestRedisScanDefaultsCapAndTupleDTO(t *testing.T) {
	fake := &fakeRedis{scanCursor: 7}
	service := serviceWithFakeRedis(fake)
	result, err := service.RedisScan(context.Background(), "redis", 0, "", 0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if fake.lastMatch != "*" || fake.lastCount != defaultRedisScanCount || result.Cursor != 7 || result.Keys == nil {
		t.Fatalf("result=%+v match=%q count=%d", result, fake.lastMatch, fake.lastCount)
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != `[7,[]]` {
		t.Fatalf("scan DTO=%s err=%v", encoded, err)
	}
	_, _ = service.RedisScan(context.Background(), "redis", 0, "prefix:*", 9000)
	if fake.lastCount != maxRedisScanCount || fake.lastMatch != "prefix:*" {
		t.Fatalf("scan arguments were not preserved/capped: match=%q count=%d", fake.lastMatch, fake.lastCount)
	}
}

func TestRedisInspectAllCurrentTypes(t *testing.T) {
	tests := []struct {
		keyType string
		setup   func(*fakeRedis)
		want    any
	}{
		{keyType: "string", setup: func(f *fakeRedis) { f.stringValue = "value" }, want: "value"},
		{keyType: "hash", setup: func(f *fakeRedis) { f.hashValue = map[string]string{"field": "value"} }, want: map[string]string{"field": "value"}},
		{keyType: "list", setup: func(f *fakeRedis) { f.listValue = []string{"a", "b"} }, want: []string{"a", "b"}},
		{keyType: "set", setup: func(f *fakeRedis) { f.setValue = []string{"a"} }, want: []string{"a"}},
		{keyType: "zset", setup: func(f *fakeRedis) { f.zsetValue = []redisDriver.Z{{Member: "member", Score: 1.5}} }, want: []RedisSortedMember{{Member: "member", Score: 1.5}}},
		{keyType: "stream", setup: func(f *fakeRedis) {
			f.streamValue = []redisDriver.XMessage{{ID: "1-1", Values: map[string]any{"field": "value"}}}
		}, want: []RedisStreamEntry{{ID: "1-1", Values: map[string]any{"field": "value"}}}},
		{keyType: "none", setup: func(*fakeRedis) {}, want: nil},
		{keyType: "vector", setup: func(*fakeRedis) {}, want: "<vector>"},
	}
	for _, test := range tests {
		t.Run(test.keyType, func(t *testing.T) {
			fake := &fakeRedis{keyType: test.keyType, ttl: 42 * time.Second}
			test.setup(fake)
			view, err := serviceWithFakeRedis(fake).RedisInspect(context.Background(), "redis", "key")
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			if view.Key != "key" || view.KeyType != test.keyType || view.TTL != 42 || !reflect.DeepEqual(view.Value, test.want) {
				t.Fatalf("view=%#v want value=%#v", view, test.want)
			}
		})
	}
}

func TestRedisInspectSpecialTTLAndMissingString(t *testing.T) {
	fake := &fakeRedis{keyType: "string", ttl: -2, stringErr: redisDriver.Nil}
	view, err := serviceWithFakeRedis(fake).RedisInspect(context.Background(), "redis", "expired")
	if err != nil || view.TTL != -2 || view.Value != "" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestRedisRawArgvAndFormatting(t *testing.T) {
	fake := &fakeRedis{}
	fake.doFunc = func(...any) (any, error) {
		return []any{"one", int64(2), nil}, nil
	}
	service := serviceWithFakeRedis(fake)
	args := []string{"CUSTOM", "key with space", `value "quoted"`}
	output, err := service.RedisCommand(context.Background(), "redis", args)
	if err != nil || output != "1) one\n2) 2\n3) (nil)" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if len(fake.doCalls) != 1 || len(fake.doCalls[0]) != 3 || fake.doCalls[0][1] != args[1] || fake.doCalls[0][2] != args[2] {
		t.Fatalf("raw argv changed: %#v", fake.doCalls)
	}

	_, err = service.RedisCommand(context.Background(), "redis", nil)
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeBadParam {
		t.Fatalf("empty command error: %#v", err)
	}
}

func TestRedisSetTTLUsesIntegerArgv(t *testing.T) {
	fake := &fakeRedis{doFunc: func(...any) (any, error) { return int64(1), nil }}
	service := serviceWithFakeRedis(fake)
	if err := service.RedisSetTTL(context.Background(), "redis", "key", int64(^uint64(0)>>1)); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := service.RedisSetTTL(context.Background(), "redis", "key", -1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if len(fake.doCalls) != 2 || fake.doCalls[0][0] != "EXPIRE" || fake.doCalls[0][2] != int64(^uint64(0)>>1) || fake.doCalls[1][0] != "PERSIST" || len(fake.doCalls[1]) != 2 {
		t.Fatalf("TTL argv: %#v", fake.doCalls)
	}
}
