package db

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRedis74DockerIntegration(t *testing.T) {
	requireDockerIntegration(t)
	tlsFiles := makeTestTLSFiles(t)
	const password = "m18-integration-password"
	container := startDockerTestContainer(t, "6380",
		"--volume", tlsFiles.dir+":/tls:ro",
		"redis:7.4",
		"redis-server",
		"--port", "0",
		"--tls-port", "6380",
		"--tls-cert-file", "/tls/server.crt",
		"--tls-key-file", "/tls/server.key",
		"--tls-ca-cert-file", "/tls/ca.crt",
		"--tls-auth-clients", "no",
		"--requirepass", password,
		"--appendonly", "no",
	)
	tlsConfig, err := makeTLSConfig(container.host, tlsFiles.options)
	if err != nil {
		t.Fatalf("TLS config: %v", err)
	}
	waitForRedis(t, container, RedisConfig{Password: password, Database: 2, TLSConfig: tlsConfig})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = connectRedis(ctx, RedisConfig{Host: container.host, Port: container.port, Password: "wrong", TLSConfig: tlsConfig})
	cancel()
	if err == nil {
		t.Fatal("wrong Redis password unexpectedly succeeded")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	_, err = connectRedis(ctx, RedisConfig{Host: container.host, Port: container.port, Password: password})
	cancel()
	if err == nil {
		t.Fatal("plaintext connection unexpectedly succeeded against TLS-only Redis")
	}

	service := NewService(nil)
	t.Cleanup(func() { _ = service.Close() })
	connected, err := service.Connect(context.Background(), ConnectArgs{Inline: &InlineConnection{
		Kind: "redis", Host: container.host, Port: &container.port, Password: password, Database: "2", TLS: tlsFiles.options,
	}})
	if err != nil {
		t.Fatalf("connect with verified TLS: %v\n%s", err, container.logs(t))
	}
	connID := connected.ConnID

	info, err := service.RedisCommand(context.Background(), connID, []string{"INFO", "server"})
	if err != nil || !strings.Contains(info, "redis_version:7.4.") {
		t.Fatalf("Redis INFO does not report 7.4.x: %v\n%s", err, info)
	}
	key := "m18:key with space"
	value := `value with spaces and "quotes"`
	if output, err := service.RedisCommand(context.Background(), connID, []string{"SET", key, value}); err != nil || output != "OK" {
		t.Fatalf("SET output=%q err=%v", output, err)
	}
	view, err := service.RedisInspect(context.Background(), connID, key)
	if err != nil || view.KeyType != "string" || view.Value != value {
		t.Fatalf("string view=%+v err=%v", view, err)
	}

	commands := [][]string{
		{"HSET", "m18:hash", "field", "value"},
		{"RPUSH", "m18:list", "a", "b"},
		{"SADD", "m18:set", "a", "b"},
		{"ZADD", "m18:zset", "1.5", "member"},
		{"XADD", "m18:stream", "*", "field", "value"},
	}
	for _, command := range commands {
		if _, err := service.RedisCommand(context.Background(), connID, command); err != nil {
			t.Fatalf("command %v: %v", command, err)
		}
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:hash")
	if err != nil || view.Value.(map[string]string)["field"] != "value" {
		t.Fatalf("hash view=%+v err=%v", view, err)
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:list")
	if err != nil || len(view.Value.([]string)) != 2 {
		t.Fatalf("list view=%+v err=%v", view, err)
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:set")
	if err != nil || len(view.Value.([]string)) != 2 {
		t.Fatalf("set view=%+v err=%v", view, err)
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:zset")
	if err != nil || len(view.Value.([]RedisSortedMember)) != 1 || view.Value.([]RedisSortedMember)[0].Score != 1.5 {
		t.Fatalf("zset view=%+v err=%v", view, err)
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:stream")
	if err != nil || len(view.Value.([]RedisStreamEntry)) != 1 || view.Value.([]RedisStreamEntry)[0].Values["field"] != "value" {
		t.Fatalf("stream view=%+v err=%v", view, err)
	}

	if err := service.RedisSetTTL(context.Background(), connID, key, 120); err != nil {
		t.Fatalf("set TTL: %v", err)
	}
	view, err = service.RedisInspect(context.Background(), connID, key)
	if err != nil || view.TTL <= 0 || view.TTL > 120 {
		t.Fatalf("TTL view=%+v err=%v", view, err)
	}
	if err := service.RedisSetTTL(context.Background(), connID, key, -1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	view, err = service.RedisInspect(context.Background(), connID, key)
	if err != nil || view.TTL != -1 {
		t.Fatalf("persistent view=%+v err=%v", view, err)
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:missing")
	if err != nil || view.KeyType != "none" || view.TTL != -2 || view.Value != nil {
		t.Fatalf("missing view=%+v err=%v", view, err)
	}

	found := map[string]bool{}
	cursor := uint64(0)
	for iterations := 0; ; iterations++ {
		if iterations > 100 {
			t.Fatal("SCAN did not terminate")
		}
		var page RedisScanResult
		page, err = service.RedisScan(context.Background(), connID, cursor, "m18:*", 100)
		if err != nil {
			t.Fatalf("SCAN: %v", err)
		}
		for _, scanned := range page.Keys {
			found[scanned] = true
		}
		cursor = page.Cursor
		if cursor == 0 {
			break
		}
	}
	for _, expected := range []string{key, "m18:hash", "m18:list", "m18:set", "m18:zset", "m18:stream"} {
		if !found[expected] {
			t.Errorf("SCAN missed %q: %v", expected, found)
		}
	}

	if output, err := service.RedisCommand(context.Background(), connID, []string{"SELECT", "3"}); err != nil || output != "OK" {
		t.Fatalf("SELECT 3 output=%q err=%v", output, err)
	}
	if _, err := service.RedisCommand(context.Background(), connID, []string{"SET", "m18:console", "state"}); err != nil {
		t.Fatalf("console SET: %v", err)
	}
	if output, err := service.RedisCommand(context.Background(), connID, []string{"GET", "m18:console"}); err != nil || output != "state" {
		t.Fatalf("console GET output=%q err=%v", output, err)
	}
	view, err = service.RedisInspect(context.Background(), connID, "m18:console")
	if err != nil || view.KeyType != "none" {
		t.Fatalf("pooled client changed database with console SELECT: view=%+v err=%v", view, err)
	}
	if _, err := service.RedisCommand(context.Background(), connID, []string{"SELECT", "2"}); err != nil {
		t.Fatalf("restore console database: %v", err)
	}
	if output, err := service.RedisCommand(context.Background(), connID, []string{"GET", key}); err != nil || output != value {
		t.Fatalf("console state was lost: output=%q err=%v", output, err)
	}

	if err := service.Disconnect(connID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, err := service.RedisCommand(context.Background(), connID, []string{"PING"}); err == nil {
		t.Fatal("Redis command succeeded after disconnect")
	}
}
