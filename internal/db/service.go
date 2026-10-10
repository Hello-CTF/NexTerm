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
)

type managedConnection interface {
	Close() error
}

type Service struct {
	resolver AssetResolver

	mu     sync.RWMutex
	conns  map[string]managedConnection
	closed bool

	openMySQL    func(context.Context, MySQLConfig) (*mysqlConnection, error)
	openPostgres func(context.Context, PostgresConfig) (*postgresConnection, error)
	openRedis    func(context.Context, RedisConfig) (*redisConnection, error)
	newID        func() (string, error)
}

func NewService(resolver AssetResolver) *Service {
	return &Service{
		resolver:     resolver,
		conns:        make(map[string]managedConnection),
		openMySQL:    connectMySQL,
		openPostgres: connectPostgres,
		openRedis:    connectRedis,
		newID:        randomID,
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
				return ConnectResult{}, badParam(fmt.Errorf("Redis database 必须是非负整数"))
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
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = connection.Close()
		return ConnectResult{}, ipc.NewError(ipc.CodeDisconnected, "连接已断开：数据库服务已关闭，请重新连接")
	}
	s.conns[id] = connection
	s.mu.Unlock()
	return ConnectResult{ConnID: id}, nil
}

func (s *Service) Disconnect(id string) error {
	s.mu.Lock()
	connection := s.conns[id]
	delete(s.conns, id)
	s.mu.Unlock()
	if connection == nil {
		return nil
	}
	if err := connection.Close(); err != nil {
		return internalError("关闭数据库连接", err)
	}
	return nil
}

func (s *Service) connectionArgs(ctx context.Context, args ConnectArgs) (InlineConnection, error) {
	if args.AssetID == "" {
		if args.Inline == nil {
			return InlineConnection{}, badParam(errors.New("需要 assetId 或 inline 参数"))
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
			return InlineConnection{}, badParam(fmt.Errorf("资产 optionsJson 无效: %w", err))
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

func (s *Service) connOf(id string) (managedConnection, error) {
	s.mu.RLock()
	connection := s.conns[id]
	s.mu.RUnlock()
	if connection == nil {
		return nil, notFound(id)
	}
	return connection, nil
}

func (s *Service) Schemas(ctx context.Context, connID string) ([]string, error) {
	connection, err := s.connOf(connID)
	if err != nil {
		return nil, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return mysqlSchemas(ctx, typed)
	case *postgresConnection:
		return postgresSchemas(ctx, typed)
	default:
		return nil, badParam(errors.New("该连接是 Redis"))
	}
}

func (s *Service) Tables(ctx context.Context, connID, schema string) ([]string, error) {
	connection, err := s.connOf(connID)
	if err != nil {
		return nil, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return mysqlTables(ctx, typed, schema)
	case *postgresConnection:
		return postgresTables(ctx, typed, schema)
	default:
		return nil, badParam(errors.New("该连接是 Redis"))
	}
}

func (s *Service) Describe(ctx context.Context, connID, schema, table string) (TableDescribe, error) {
	connection, err := s.connOf(connID)
	if err != nil {
		return TableDescribe{}, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return mysqlDescribe(ctx, typed, schema, table)
	case *postgresConnection:
		return postgresDescribe(ctx, typed, schema, table)
	default:
		return TableDescribe{}, badParam(errors.New("该连接是 Redis"))
	}
}

func (s *Service) Query(ctx context.Context, connID, statement string, limit uint64, timeout time.Duration) (QueryResult, error) {
	connection, err := s.connOf(connID)
	if err != nil {
		return QueryResult{}, err
	}
	switch typed := connection.(type) {
	case *mysqlConnection:
		return queryMySQL(ctx, typed.db, statement, limit, timeout)
	case *postgresConnection:
		return queryPostgres(ctx, typed.db, statement, limit, timeout)
	default:
		return QueryResult{}, badParam(errors.New("该连接是 Redis"))
	}
}

func (s *Service) mysqlOf(id string) (*mysqlConnection, error) {
	connection, err := s.connOf(id)
	if err != nil {
		return nil, err
	}
	mysql, ok := connection.(*mysqlConnection)
	if !ok {
		return nil, badParam(errors.New("该连接不是 MySQL"))
	}
	return mysql, nil
}

func (s *Service) postgresOf(id string) (*postgresConnection, error) {
	connection, err := s.connOf(id)
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
	connection, err := s.connOf(id)
	if err != nil {
		return nil, err
	}
	redis, ok := connection.(*redisConnection)
	if !ok {
		return nil, badParam(errors.New("该连接不是 Redis"))
	}
	return redis, nil
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
	return ipc.NewError(ipc.CodeNotFound, "未找到数据库连接 "+id)
}

func internalError(action string, err error) *ipc.Error {
	return ipc.WrapError(ipc.CodeInternal, fmt.Sprintf("内部错误: %s: %v", action, err), err)
}
