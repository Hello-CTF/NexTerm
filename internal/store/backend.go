package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"database/sql"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Backend string

const (
	BackendSQLite   Backend = "sqlite"
	BackendPostgres Backend = "postgres"
	BackendMySQL    Backend = "mysql"
	BackendMariaDB  Backend = "mariadb"
)

func ParseBackend(raw string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "sqlite":
		return BackendSQLite, nil
	case "postgres", "postgresql":
		return BackendPostgres, nil
	case "mysql":
		return BackendMySQL, nil
	case "mariadb":
		return BackendMariaDB, nil
	}
	return "", fmt.Errorf("invalid database backend %q (want sqlite, postgres, mysql or mariadb)", raw)
}

type BackendConfig struct {
	Backend        Backend
	DSN            string
	Password       string
	MaxOpenConns   int
	ConnectTimeout time.Duration
}

func OpenBackend(ctx context.Context, cfg BackendConfig, options OpenOptions) (*Store, error) {
	backend, err := ParseBackend(string(cfg.Backend))
	if err != nil {
		return nil, badParam(err)
	}
	if backend == BackendSQLite {
		return OpenWithOptions(ctx, cfg.DSN, options)
	}
	if cfg.DSN == "" {
		return nil, badParam(fmt.Errorf("%s backend requires a DSN", backend))
	}
	var dialector gorm.Dialector
	var connection *sql.DB
	switch backend {
	case BackendPostgres:
		config, err := pgx.ParseConfig(cfg.DSN)
		if err != nil {
			return nil, badParam(err)
		}
		if err := applyPostgresDefaults(config, cfg.Password); err != nil {
			return nil, badParam(err)
		}
		connection = sql.OpenDB(stdlib.GetConnector(*config))
		dialector = postgres.New(postgres.Config{Conn: connection})
	case BackendMySQL, BackendMariaDB:
		config, err := mysqldriver.ParseDSN(cfg.DSN)
		if err != nil {
			return nil, badParam(err)
		}
		if cfg.Password != "" {
			if config.Passwd != "" {
				return nil, badParam(fmt.Errorf("database DSN already contains a password; remove it from the DSN or drop the password file"))
			}
			config.Passwd = cfg.Password
		}
		config.Collation = "utf8mb4_bin"
		config.ParseTime = true
		connector, err := mysqldriver.NewConnector(config)
		if err != nil {
			return nil, badParam(err)
		}
		connection = sql.OpenDB(connector)
		dialector = mysql.New(mysql.Config{Conn: connection})
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 16
	}
	connection.SetMaxOpenConns(maxOpen)
	connection.SetMaxIdleConns(maxOpen)
	connection.SetConnMaxLifetime(30 * time.Minute)
	connection.SetConnMaxIdleTime(5 * time.Minute)
	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := connection.PingContext(pingCtx); err != nil {
		_ = connection.Close()
		return nil, dbError(fmt.Errorf("%s startup ping: %w", backend, err))
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = connection.Close()
		return nil, dbError(err)
	}
	s := newStore(db, options.Logger)
	s.backend = backend
	if err := s.migrate(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func applyPostgresDefaults(config *pgx.ConnConfig, password string) error {
	if password != "" {
		if config.Password != "" {
			return fmt.Errorf("postgres DSN already contains a password; remove it from the DSN or drop the password file")
		}
		config.Password = password
	}
	for key, value := range map[string]string{"lock_timeout": "5000", "statement_timeout": "30000", "idle_in_transaction_session_timeout": "60000"} {
		if _, ok := config.RuntimeParams[key]; !ok {
			config.RuntimeParams[key] = value
		}
	}
	return nil
}
