package db

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	redisDriver "github.com/redis/go-redis/v9"
)

type redisAPI interface {
	Scan(context.Context, uint64, string, int64) *redisDriver.ScanCmd
	Type(context.Context, string) *redisDriver.StatusCmd
	TTL(context.Context, string) *redisDriver.DurationCmd
	Get(context.Context, string) *redisDriver.StringCmd
	HGetAll(context.Context, string) *redisDriver.MapStringStringCmd
	LRange(context.Context, string, int64, int64) *redisDriver.StringSliceCmd
	SMembers(context.Context, string) *redisDriver.StringSliceCmd
	ZRangeWithScores(context.Context, string, int64, int64) *redisDriver.ZSliceCmd
	XRangeN(context.Context, string, string, string, int64) *redisDriver.XMessageSliceCmd
	Do(context.Context, ...any) *redisDriver.Cmd
}

type redisConnection struct {
	client  redisAPI
	console redisAPI
	closeFn func() error

	consoleMu sync.Mutex
}

func (c *redisConnection) Close() error {
	if c.closeFn == nil {
		return nil
	}
	return c.closeFn()
}

func connectRedis(ctx context.Context, config RedisConfig) (*redisConnection, error) {
	if config.Port == 0 {
		config.Port = 6379
	}
	options := &redisDriver.Options{
		Addr:                  net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))),
		Username:              config.Username,
		Password:              config.Password,
		DB:                    config.Database,
		Protocol:              2,
		DialTimeout:           10 * time.Second,
		ReadTimeout:           -1,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
		PoolSize:              4,
		MaxActiveConns:        4,
		MinIdleConns:          1,
	}
	if config.TLSConfig != nil {
		options.TLSConfig = config.TLSConfig.Clone()
	}
	client := redisDriver.NewClient(options)
	pingContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := client.Ping(pingContext).Err()
	cancel()
	if err != nil {
		_ = client.Close()
		return nil, internalError("Redis 连接失败", err)
	}

	console := client.Conn()
	return &redisConnection{
		client:  client,
		console: console,
		closeFn: func() error {
			return errors.Join(console.Close(), client.Close())
		},
	}, nil
}

func (s *Service) RedisScan(ctx context.Context, connID string, cursor uint64, pattern string, count uint64) (RedisScanResult, error) {
	connection, err := s.redisOf(connID)
	if err != nil {
		return RedisScanResult{}, err
	}
	if pattern == "" {
		pattern = "*"
	}
	if count == 0 {
		count = defaultRedisScanCount
	}
	if count > maxRedisScanCount {
		count = maxRedisScanCount
	}
	keys, next, err := connection.client.Scan(ctx, cursor, pattern, int64(count)).Result()
	if err != nil {
		return RedisScanResult{}, internalError("Redis SCAN 失败", err)
	}
	if keys == nil {
		keys = []string{}
	}
	return RedisScanResult{Cursor: next, Keys: keys}, nil
}

func (s *Service) RedisInspect(ctx context.Context, connID, key string) (RedisKeyView, error) {
	connection, err := s.redisOf(connID)
	if err != nil {
		return RedisKeyView{}, err
	}
	view := RedisKeyView{Key: key}
	view.KeyType, err = connection.client.Type(ctx, key).Result()
	if err != nil {
		return RedisKeyView{}, internalError("Redis TYPE 失败", err)
	}
	ttl, err := connection.client.TTL(ctx, key).Result()
	if err != nil {
		return RedisKeyView{}, internalError("Redis TTL 失败", err)
	}
	if ttl < 0 {
		view.TTL = int64(ttl)
	} else {
		view.TTL = int64(ttl / time.Second)
	}

	switch view.KeyType {
	case "string":
		var value string
		value, err = connection.client.Get(ctx, key).Result()
		if errors.Is(err, redisDriver.Nil) {
			err = nil
		}
		view.Value = value
	case "hash":
		var value map[string]string
		value, err = connection.client.HGetAll(ctx, key).Result()
		if value == nil {
			value = map[string]string{}
		}
		view.Value = value
	case "list":
		var value []string
		value, err = connection.client.LRange(ctx, key, 0, 99).Result()
		if value == nil {
			value = []string{}
		}
		view.Value = value
	case "set":
		var value []string
		value, err = connection.client.SMembers(ctx, key).Result()
		if value == nil {
			value = []string{}
		}
		view.Value = value
	case "zset":
		var values []redisDriver.Z
		values, err = connection.client.ZRangeWithScores(ctx, key, 0, 99).Result()
		members := make([]RedisSortedMember, 0, len(values))
		for _, value := range values {
			members = append(members, RedisSortedMember{Member: fmt.Sprint(value.Member), Score: value.Score})
		}
		view.Value = members
	case "stream":
		var values []redisDriver.XMessage
		values, err = connection.client.XRangeN(ctx, key, "-", "+", 50).Result()
		entries := make([]RedisStreamEntry, 0, len(values))
		for _, value := range values {
			fields := value.Values
			if fields == nil {
				fields = map[string]any{}
			}
			entries = append(entries, RedisStreamEntry{ID: value.ID, Values: fields})
		}
		view.Value = entries
	case "none":
		view.Value = nil
	default:
		view.Value = "<" + view.KeyType + ">"
	}
	if err != nil {
		return RedisKeyView{}, internalError("Redis 读取键值失败", err)
	}
	return view, nil
}

func (s *Service) RedisSetTTL(ctx context.Context, connID, key string, seconds int64) error {
	connection, err := s.redisOf(connID)
	if err != nil {
		return err
	}
	args := []any{"EXPIRE", key, seconds}
	if seconds < 0 {
		args = []any{"PERSIST", key}
	}
	_, err = connection.client.Do(ctx, args...).Int64()
	if err != nil {
		return internalError("Redis 设置 TTL 失败", err)
	}
	return nil
}

func (s *Service) RedisCommand(ctx context.Context, connID string, args []string) (string, error) {
	connection, err := s.redisOf(connID)
	if err != nil {
		return "", err
	}
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", badParam(errors.New("Redis 命令为空"))
	}
	argv := make([]any, len(args))
	for i, arg := range args {
		argv[i] = arg
	}

	connection.consoleMu.Lock()
	defer connection.consoleMu.Unlock()
	value, err := connection.console.Do(ctx, argv...).Result()
	if errors.Is(err, redisDriver.Nil) {
		return "(nil)", nil
	}
	if err != nil {
		return "", internalError("Redis 命令失败", err)
	}
	return formatRedisValue(value), nil
}

func formatRedisValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "(nil)"
	case string:
		return typed
	case []byte:
		if utf8.Valid(typed) {
			return string(typed)
		}
		return base64.StdEncoding.EncodeToString(typed)
	case int:
		return strconv.Itoa(typed)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case []string:
		values := make([]any, len(typed))
		for i := range typed {
			values[i] = typed[i]
		}
		return formatRedisValue(values)
	case []any:
		lines := make([]string, len(typed))
		for i, item := range typed {
			lines[i] = fmt.Sprintf("%d) %s", i+1, formatRedisValue(item))
		}
		return strings.Join(lines, "\n")
	case map[string]any, map[any]any, map[string]string:
		return "<map>"
	default:
		return fmt.Sprint(typed)
	}
}
