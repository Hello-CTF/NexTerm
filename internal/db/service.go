package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	redisDriver "github.com/redis/go-redis/v9"
)

type managedConnection interface {
	Close() error
}

type Service struct {
	resolver AssetResolver

	mu                   sync.RWMutex
	conns                map[string]managedConnection
	owners               map[string]string
	loginSessions        map[string]map[string]struct{}
	revokedLoginSessions map[string]struct{}
	closed               bool

	openMySQL    func(context.Context, MySQLConfig) (*mysqlConnection, error)
	openPostgres func(context.Context, PostgresConfig) (*postgresConnection, error)
	openRedis    func(context.Context, RedisConfig) (*redisConnection, error)
	newID        func() (string, error)
}

func NewService(resolver AssetResolver) *Service {
	return &Service{
		resolver:             resolver,
		conns:                make(map[string]managedConnection),
		owners:               make(map[string]string),
		loginSessions:        make(map[string]map[string]struct{}),
		revokedLoginSessions: make(map[string]struct{}),
		openMySQL:            connectMySQL,
		openPostgres:         connectPostgres,
		openRedis:            connectRedis,
		newID:                randomID,
	}
}

func (s *Service) Start(context.Context) error {
	return nil
}

func (s *Service) Shutdown(context.Context) error {
	return s.Close()
}

func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	connections := make([]managedConnection, 0, len(s.conns))
	for id, connection := range s.conns {
		connections = append(connections, connection)
		delete(s.conns, id)
		delete(s.owners, id)
		delete(s.loginSessions, id)
	}
	s.mu.Unlock()

	var errs []error
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) Connect(ctx context.Context, args ConnectArgs) (ConnectResult, error) {
	inline, err := s.connectionArgs(ctx, args)
	if err != nil {
		return ConnectResult{}, err
	}
	tlsConfig, err := makeTLSConfig(inline.Host, inline.TLS)
	if err != nil {
		return ConnectResult{}, badParam(err)
	}

	var connection managedConnection
	switch strings.ToLower(inline.Kind) {
	case "mysql":
		port := uint16(3306)
		if inline.Port != nil {
			port = *inline.Port
		}
		connection, err = s.openMySQL(ctx, MySQLConfig{
			Host:      inline.Host,
			Port:      port,
			Username:  inline.Username,
			Password:  inline.Password,
			Database:  inline.Database,
			TLSConfig: tlsConfig,
		})
	case "postgres":
		port := uint16(5432)
		if inline.Port != nil {
			port = *inline.Port
		}
		connection, err = s.openPostgres(ctx, PostgresConfig{
			Host:      inline.Host,
			Port:      port,
			Username:  inline.Username,
			Password:  inline.Password,
			Database:  inline.Database,
			TLSConfig: tlsConfig,
		})
	case "redis":
		port := uint16(6379)
		if inline.Port != nil {
			port = *inline.Port
		}
		database := 0
		if inline.Database != "" {
			database, err = strconv.Atoi(inline.Database)
			if err != nil || database < 0 {
				return ConnectResult{}, badParam(fmt.Errorf("Redis 数据库编号必须是非负整数"))
			}
		}
		connection, err = s.openRedis(ctx, RedisConfig{
			Host:      inline.Host,
			Port:      port,
			Username:  inline.Username,
			Password:  inline.Password,
			Database:  database,
			TLSConfig: tlsConfig,
		})
	default:
		return ConnectResult{}, badParam(fmt.Errorf("不支持的数据库类型 %s", inline.Kind))
	}
	if err != nil {
		return ConnectResult{}, err
	}

	id, err := s.newID()
	if err != nil {
		_ = connection.Close()
		return ConnectResult{}, internalError("生成数据库连接 ID", err)
	}
	if err := ipc.CheckAccountSession(ctx); err != nil {
		return ConnectResult{}, errors.Join(err, connection.Close())
	}
	s.mu.Lock()
	loginSessionID := ipc.AccountSessionID(ctx)
	if _, revoked := s.revokedLoginSessions[loginSessionID]; loginSessionID != "" && revoked {
		s.mu.Unlock()
		return ConnectResult{}, errors.Join(ipc.NewError(ipc.CodeForbidden, "登录会话已失效"), connection.Close())
	}
	if s.closed {
		s.mu.Unlock()
		_ = connection.Close()
		return ConnectResult{}, ipc.NewError(ipc.CodeDisconnected, "连接已断开：数据库服务已关闭，请重新连接")
	}
	s.conns[id] = connection
	s.owners[id] = ipc.OwnerID(ctx)
	if loginSessionID != "" {
		s.loginSessions[id] = map[string]struct{}{loginSessionID: {}}
	}
	s.mu.Unlock()
	return ConnectResult{ConnID: id}, nil
}

func (s *Service) Disconnect(ctx context.Context, id string) error {
	s.mu.Lock()
	connection := s.conns[id]
	if connection == nil {
		delete(s.owners, id)
		s.mu.Unlock()
		return nil
	}
	if !connectionRequestAllowed(ctx, s.owners[id]) {
		s.mu.Unlock()
		return forbiddenConnection()
	}
	delete(s.conns, id)
	delete(s.owners, id)
	delete(s.loginSessions, id)
	s.mu.Unlock()
	if err := connection.Close(); err != nil {
		return internalError("关闭数据库连接", err)
	}
	return nil
}

// CloseAccountSession releases only database handles not adopted by another login session.
// A revoked session is remembered so a dial that finishes after logout cannot register a handle.
func (s *Service) CloseAccountSession(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	s.revokedLoginSessions[sessionID] = struct{}{}
	var connections []managedConnection
	for id, sessions := range s.loginSessions {
		delete(sessions, sessionID)
		if len(sessions) != 0 {
			continue
		}
		connections = append(connections, s.conns[id])
		delete(s.conns, id)
		delete(s.owners, id)
		delete(s.loginSessions, id)
	}
	s.mu.Unlock()
	var errs []error
	for _, connection := range connections {
		if err := connection.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) connectionArgs(ctx context.Context, args ConnectArgs) (InlineConnection, error) {
	if args.AssetID == "" {
		if args.Inline == nil {
			return InlineConnection{}, badParam(errors.New("需要提供数据库资产或连接配置"))
		}
		return *args.Inline, nil
	}
	if s.resolver == nil {
		return InlineConnection{}, badParam(errors.New("数据库资产解析器未配置"))
	}
	asset, err := s.resolver.ResolveDBAsset(ctx, args.AssetID)
	if err != nil {
		return InlineConnection{}, err
	}

	var options struct {
		Database string      `json:"database"`
		TLS      *TLSOptions `json:"tls"`
	}
	if strings.TrimSpace(asset.OptionsJSON) != "" {
		if err := json.Unmarshal([]byte(asset.OptionsJSON), &options); err != nil {
			return InlineConnection{}, badParam(fmt.Errorf("数据库资产配置无效: %w", err))
		}
	}
	inline := InlineConnection{
		Kind:     asset.Kind,
		Password: asset.Password,
		Database: options.Database,
		TLS:      options.TLS,
	}
	if asset.Host != nil {
		inline.Host = *asset.Host
	}
	if asset.Username != nil {
		inline.Username = *asset.Username
	}
	if asset.Port != nil {
		if *asset.Port < 0 || *asset.Port > 65535 {
			return InlineConnection{}, badParam(fmt.Errorf("数据库端口超出范围"))
		}
		port := uint16(*asset.Port)
		inline.Port = &port
	}
	return inline, nil
}

type openAccessContextKey struct{}

func withOpenAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAccessContextKey{}, true)
}

func openAccessFromContext(ctx context.Context) bool {
	open, _ := ctx.Value(openAccessContextKey{}).(bool)
	return open
}

func connectionRequestAllowed(ctx context.Context, owner string) bool {
	if requester, ok := ipc.UserIDFromContext(ctx); ok {
		return owner != "" && owner == requester
	}
	if openAccessFromContext(ctx) {
		return true
	}
	return owner == ""
}

func forbiddenConnection() *ipc.Error {
	return ipc.NewError(ipc.CodeForbidden, "没有权限访问该数据库连接")
}

func (s *Service) connOf(ctx context.Context, id string) (managedConnection, error) {
	s.mu.Lock()
	connection := s.conns[id]
	owner := s.owners[id]
	if connection == nil {
		s.mu.Unlock()
		return nil, notFound(id)
	}
	if !connectionRequestAllowed(ctx, owner) {
		s.mu.Unlock()
		return nil, forbiddenConnection()
	}
	if sessionID := ipc.AccountSessionID(ctx); sessionID != "" {
		if _, revoked := s.revokedLoginSessions[sessionID]; revoked {
			s.mu.Unlock()
			return nil, ipc.NewError(ipc.CodeForbidden, "登录会话已失效")
		}
		if s.loginSessions[id] == nil {
			s.loginSessions[id] = make(map[string]struct{})
		}
		s.loginSessions[id][sessionID] = struct{}{}
	}
	s.mu.Unlock()
	return connection, nil
}

func (s *Service) Schemas(ctx context.Context, connID string) ([]string, error) {
	connection, err := s.connOf(ctx, connID)
	if err != nil {
		return nil, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return mysqlSchemas(ctx, typed)
	case *postgresConnection:
		return postgresSchemas(ctx, typed)
	default:
		return nil, badParam(errors.New("Redis 连接不支持此操作"))
	}
}

func (s *Service) Tables(ctx context.Context, connID, schema string) ([]string, error) {
	connection, err := s.connOf(ctx, connID)
	if err != nil {
		return nil, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return mysqlTables(ctx, typed, schema)
	case *postgresConnection:
		return postgresTables(ctx, typed, schema)
	default:
		return nil, badParam(errors.New("Redis 连接不支持此操作"))
	}
}

func (s *Service) Describe(ctx context.Context, connID, schema, table string) (TableDescribe, error) {
	connection, err := s.connOf(ctx, connID)
	if err != nil {
		return TableDescribe{}, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return mysqlDescribe(ctx, typed, schema, table)
	case *postgresConnection:
		return postgresDescribe(ctx, typed, schema, table)
	default:
		return TableDescribe{}, badParam(errors.New("Redis 连接不支持此操作"))
	}
}

func (s *Service) Query(ctx context.Context, connID, statement string, limit uint64, timeout time.Duration) (QueryResult, error) {
	connection, err := s.connOf(ctx, connID)
	if err != nil {
		return QueryResult{}, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return queryMySQL(ctx, typed.db, statement, limit, timeout)
	case *postgresConnection:
		return queryPostgres(ctx, typed.db, statement, limit, timeout)
	default:
		return QueryResult{}, badParam(errors.New("Redis 连接不支持此操作"))
	}
}

func (s *Service) mysqlOf(ctx context.Context, id string) (*mysqlConnection, error) {
	connection, err := s.connOf(ctx, id)
	if err != nil {
		return nil, err
	}
	mysql, ok := connection.(*mysqlConnection)
	if !ok {
		return nil, badParam(errors.New("该连接不是 MySQL"))
	}
	return mysql, nil
}

func (s *Service) postgresOf(ctx context.Context, id string) (*postgresConnection, error) {
	connection, err := s.connOf(ctx, id)
	if err != nil {
		return nil, err
	}
	postgres, ok := connection.(*postgresConnection)
	if !ok {
		return nil, badParam(errors.New("该连接不是 PostgreSQL"))
	}
	return postgres, nil
}

func (s *Service) redisOf(id string) (*redisConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	connection := s.conns[id]
	if connection == nil {
		return nil, notFound(id)
	}
	redis, ok := connection.(*redisConnection)
	if !ok {
		return nil, badParam(errors.New("该连接不是 Redis"))
	}
	if _, ok := redis.client.(*requestRedisAPI); !ok {
		redis.client = &requestRedisAPI{service: s, connID: id, next: redis.client}
	}
	if _, ok := redis.console.(*requestRedisAPI); !ok {
		redis.console = &requestRedisAPI{service: s, connID: id, next: redis.console}
	}
	return redis, nil
}

type requestRedisAPI struct {
	service *Service
	connID  string
	next    redisAPI
}

func (r *requestRedisAPI) authorize(ctx context.Context) error {
	_, err := r.service.connOf(ctx, r.connID)
	return err
}

func (r *requestRedisAPI) Scan(ctx context.Context, cursor uint64, match string, count int64) *redisDriver.ScanCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewScanCmd(ctx, nil)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.Scan(ctx, cursor, match, count)
}

func (r *requestRedisAPI) Type(ctx context.Context, key string) *redisDriver.StatusCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewStatusCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.Type(ctx, key)
}

func (r *requestRedisAPI) TTL(ctx context.Context, key string) *redisDriver.DurationCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewDurationCmd(ctx, time.Second)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.TTL(ctx, key)
}

func (r *requestRedisAPI) Get(ctx context.Context, key string) *redisDriver.StringCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewStringCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.Get(ctx, key)
}

func (r *requestRedisAPI) HGetAll(ctx context.Context, key string) *redisDriver.MapStringStringCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewMapStringStringCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.HGetAll(ctx, key)
}

func (r *requestRedisAPI) LRange(ctx context.Context, key string, start, stop int64) *redisDriver.StringSliceCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewStringSliceCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.LRange(ctx, key, start, stop)
}

func (r *requestRedisAPI) SMembers(ctx context.Context, key string) *redisDriver.StringSliceCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewStringSliceCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.SMembers(ctx, key)
}

func (r *requestRedisAPI) ZRangeWithScores(ctx context.Context, key string, start, stop int64) *redisDriver.ZSliceCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewZSliceCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.ZRangeWithScores(ctx, key, start, stop)
}

func (r *requestRedisAPI) XRangeN(ctx context.Context, key, start, end string, count int64) *redisDriver.XMessageSliceCmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewXMessageSliceCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.XRangeN(ctx, key, start, end, count)
}

func (r *requestRedisAPI) Do(ctx context.Context, args ...any) *redisDriver.Cmd {
	if err := r.authorize(ctx); err != nil {
		cmd := redisDriver.NewCmd(ctx)
		cmd.SetErr(err)
		return cmd
	}
	return r.next.Do(ctx, args...)
}

func randomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func badParam(err error) *ipc.Error {
	return ipc.WrapError(ipc.CodeBadParam, "参数错误: "+err.Error(), err)
}

func notFound(id string) *ipc.Error {
	return ipc.NewError(ipc.CodeNotFound, "数据库连接 "+id+" 不存在或已关闭")
}

func internalError(action string, err error) *ipc.Error {
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) && ipcErr.Code == ipc.CodeForbidden {
		return ipcErr
	}
	return ipc.WrapError(ipc.CodeInternal, fmt.Sprintf("内部错误: %s: %v", action, err), err)
}
