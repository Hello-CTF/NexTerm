package tools

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
)

func TestRealMySQLAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_MYSQL") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_MYSQL=1 and MySQL environment to run")
	}
	service := db.NewService(nil)
	defer service.Close()
	port := acceptancePort("NEXTERM_AI_MYSQL_PORT", 3306)
	connected, err := service.Connect(context.Background(), db.ConnectArgs{Inline: &db.InlineConnection{
		Kind: "mysql", Host: os.Getenv("NEXTERM_AI_MYSQL_HOST"), Port: port,
		Username: os.Getenv("NEXTERM_AI_MYSQL_USER"), Password: os.Getenv("NEXTERM_AI_MYSQL_PASSWORD"), Database: os.Getenv("NEXTERM_AI_MYSQL_DATABASE"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(Dependencies{Database: service})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := registry.Execute(ctx, "acceptance", Scope{ConnID: connected.ConnID}, Call{ID: "q", Name: "db_query", Args: json.RawMessage(`{"sql":"SELECT 1"}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "1") {
		t.Fatalf("query result=%+v", result)
	}
	result = registry.Execute(ctx, "acceptance", Scope{ConnID: connected.ConnID}, Call{ID: "t", Name: "db_list_tables", Args: json.RawMessage(`{}`)}, nil)
	if !result.OK {
		t.Fatalf("tables result=%+v", result)
	}
}

func TestRealRedisAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_REDIS") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_REDIS=1 and Redis environment to run")
	}
	service := db.NewService(nil)
	defer service.Close()
	port := acceptancePort("NEXTERM_AI_REDIS_PORT", 6379)
	connected, err := service.Connect(context.Background(), db.ConnectArgs{Inline: &db.InlineConnection{
		Kind: "redis", Host: os.Getenv("NEXTERM_AI_REDIS_HOST"), Port: port,
		Username: os.Getenv("NEXTERM_AI_REDIS_USER"), Password: os.Getenv("NEXTERM_AI_REDIS_PASSWORD"), Database: os.Getenv("NEXTERM_AI_REDIS_DATABASE"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(Dependencies{Database: service})
	result := registry.Execute(context.Background(), "acceptance", Scope{ConnID: connected.ConnID}, Call{ID: "s", Name: "redis_scan", Args: json.RawMessage(`{"pattern":"*","count":1}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "cursor=") {
		t.Fatalf("scan result=%+v", result)
	}
}

func TestRealSSHAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_SSH") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_SSH=1 and SSH environment to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	auth := ssh.AuthConfig{Method: ssh.AuthPassword, Password: os.Getenv("NEXTERM_AI_SSH_PASSWORD")}
	if keyPath := os.Getenv("NEXTERM_AI_SSH_KEY"); keyPath != "" {
		auth.Method, auth.KeyPath = ssh.AuthKey, keyPath
	}
	client, err := ssh.Connect(ctx, ssh.Config{
		Host: os.Getenv("NEXTERM_AI_SSH_HOST"), Port: int(*acceptancePort("NEXTERM_AI_SSH_PORT", 22)), User: os.Getenv("NEXTERM_AI_SSH_USER"),
		Auth: auth, HostKeys: ssh.NewMemoryHostKeyStore(), AutoAcceptUnknown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return client, nil }})
	scope := Scope{SessionID: "acceptance"}
	path := "/tmp/nexterm-ai-acceptance-" + ids.New()
	read := Call{ID: "read", Name: "read_file", Args: json.RawMessage(`{"path":` + strconv.Quote(path) + `}`)}
	if result := registry.Execute(ctx, "acceptance", scope, read, nil); result.OK {
		t.Fatal("new acceptance file already exists")
	}
	write := Call{ID: "write", Name: "write_file", Args: json.RawMessage(`{"path":` + strconv.Quote(path) + `,"content":"safe"}`)}
	preparation, err := registry.Prepare(ctx, "acceptance", scope, write)
	if err != nil {
		t.Fatal(err)
	}
	result := registry.Execute(ctx, "acceptance", scope, write, preparation)
	if !result.OK || result.Change == nil {
		t.Fatalf("write result=%+v", result)
	}
	files, err := client.FileSystem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = files.Delete(context.Background(), path, false)
		_ = files.Delete(context.Background(), path+".nexterm-bak", false)
	})
}

func acceptancePort(name string, fallback int) *uint16 {
	value := fallback
	if raw := os.Getenv(name); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			value = parsed
		}
	}
	port := uint16(value)
	return &port
}
