package db

import (
	"context"
	"crypto/tls"
	"encoding/json"
)

const (
	MaxRows               = 5_000
	DefaultQueryTimeoutMS = 30_000
	defaultRedisScanCount = 100
	maxRedisScanCount     = 1_000
)

type QueryResult struct {
	Columns      []string `json:"columns"`
	Rows         [][]any  `json:"rows"`
	RowsAffected uint64   `json:"rowsAffected"`
	DurationMS   uint64   `json:"durationMs"`
	Truncated    bool     `json:"truncated"`
	Error        *string  `json:"error"`
}

type TableColumn struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Key      string  `json:"key"`
	Default  *string `json:"default"`
	Extra    string  `json:"extra"`
}

type TableIndex struct {
	Name   string `json:"name"`
	Unique bool   `json:"unique"`
	Column string `json:"column"`
	Seq    int64  `json:"seq"`
}

type TableDescribe struct {
	Columns []TableColumn `json:"columns"`
	Indexes []TableIndex  `json:"indexes"`
}

type RedisKeyView struct {
	Key     string `json:"key"`
	KeyType string `json:"keyType"`
	TTL     int64  `json:"ttl"`
	Value   any    `json:"value"`
}

type RedisStreamEntry struct {
	ID     string         `json:"id"`
	Values map[string]any `json:"values"`
}

type RedisSortedMember struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

type RedisScanResult struct {
	Cursor uint64
	Keys   []string
}

func (r RedisScanResult) MarshalJSON() ([]byte, error) {
	keys := r.Keys
	if keys == nil {
		keys = []string{}
	}
	return json.Marshal([2]any{r.Cursor, keys})
}

type TLSOptions struct {
	Mode               string `json:"mode,omitempty"`
	ServerName         string `json:"serverName,omitempty"`
	CAFile             string `json:"caFile,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
}

type InlineConnection struct {
	Kind       string      `json:"kind"`
	Host       string      `json:"host"`
	Port       *uint16     `json:"port,omitempty"`
	Username   string      `json:"username,omitempty"`
	Password   string      `json:"password,omitempty"`
	Database   string      `json:"database,omitempty"`
	ViaSession string      `json:"viaSession,omitempty"`
	TLS        *TLSOptions `json:"tls,omitempty"`
}

type ConnectArgs struct {
	AssetID string            `json:"assetId,omitempty"`
	Inline  *InlineConnection `json:"inline,omitempty"`
}

type ConnectResult struct {
	ConnID string `json:"connId"`
}

type Asset struct {
	Kind        string
	Host        *string
	Port        *int
	Username    *string
	OptionsJSON string
	Password    string
}

type AssetResolver interface {
	ResolveDBAsset(context.Context, string) (Asset, error)
}

type AssetResolverFunc func(context.Context, string) (Asset, error)

func (f AssetResolverFunc) ResolveDBAsset(ctx context.Context, id string) (Asset, error) {
	return f(ctx, id)
}

type MySQLConfig struct {
	Host      string
	Port      uint16
	Username  string
	Password  string
	Database  string
	TLSConfig *tls.Config
}

type RedisConfig struct {
	Host      string
	Port      uint16
	Username  string
	Password  string
	Database  int
	TLSConfig *tls.Config
}
