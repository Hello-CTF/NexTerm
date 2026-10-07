package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type BackendConfig struct {
	Backend        Backend
	DSN            string
	Password       string
	MaxOpenConns   int
	ConnectTimeout time.Duration
}

func OpenBackend(ctx context.Context, cfg BackendConfig, options OpenOptions) (*Store, error) {
	switch cfg.Backend {
	case BackendSQLite:
		return OpenWithOptions(ctx, cfg.DSN, options)
	case BackendPostgres:
		return openPostgres(ctx, cfg, options)
	}
	return nil, badParam(fmt.Errorf("invalid database backend %q", cfg.Backend))
}

func openPostgres(ctx context.Context, cfg BackendConfig, options OpenOptions) (*Store, error) {
	if cfg.DSN == "" {
		return nil, badParam(fmt.Errorf("postgres backend requires a DSN"))
	}
	config, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, badParam(fmt.Errorf("parse postgres DSN: %w", err))
	}
	if err := applyPostgresDefaults(config, cfg.Password); err != nil {
		return nil, badParam(err)
	}
	db := sql.OpenDB(&rebindConnector{inner: stdlib.GetConnector(*config)})
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 16
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, dbError(fmt.Errorf("postgres startup ping: %w", err))
	}
	s := newStore(db, options.Logger, Dialect{backend: BackendPostgres})
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
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
	for param, value := range map[string]string{
		"lock_timeout":                        "5000",
		"statement_timeout":                   "30000",
		"idle_in_transaction_session_timeout": "60000",
	} {
		if _, ok := config.RuntimeParams[param]; !ok {
			config.RuntimeParams[param] = value
		}
	}
	return nil
}
