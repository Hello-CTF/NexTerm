package app

import (
	"strings"
	"testing"
)

func TestParseCLICommandShapesAndFlagPositions(t *testing.T) {
	environment := map[string]string{
		"NEXTERM_LISTEN":   "127.0.0.1:9000",
		"NEXTERM_DATA_DIR": "/env/data",
		"NEXTERM_WEB_ROOT": "/env/web",
	}
	getenv := func(key string) string { return environment[key] }

	invocation, err := ParseCLI([]string{"--data-dir", "/flag/data", "serve", "--sync-only=false"}, CommandServe, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Command != CommandServe || invocation.DataDir != "/flag/data" || invocation.SyncOnly {
		t.Fatalf("invocation = %+v", invocation)
	}
	if invocation.Listen != "127.0.0.1:9000" || invocation.WebRoot != "/env/web" {
		t.Fatalf("environment fallback = %+v", invocation)
	}

	invocation, err = ParseCLI(nil, CommandServe, nil)
	if err != nil || invocation.Command != CommandServe || invocation.Listen != "0.0.0.0:8080" {
		t.Fatalf("bare server = %+v, %v", invocation, err)
	}
	invocation, err = ParseCLI([]string{"desktop", "--listen=127.0.0.1:0"}, CommandServe, nil)
	if err != nil || invocation.Command != CommandDesktop {
		t.Fatalf("desktop = %+v, %v", invocation, err)
	}
}

func TestParseCLIRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"},
		{"serve", "token"},
		{"--data-dir"},
		{"--master-key", "legacy-secret"},
		{"--master-key=legacy-secret"},
		{"--listen", "missing-port"},
		{"--listen", "127.0.0.1:70000"},
		{"--sync-only=perhaps"},
		{"--db", "mysql"},
		{"--db-dsn", "postgres://u@h/db"},
		{"--db", "postgres"},
		{"--db", "postgres", "--db-password-file", "/tmp/pw"},
		{"--db", "postgres", "--db-dsn", "postgres://u@h/db", "--db-max-open-conns", "0"},
		{"--db", "postgres", "--db-dsn", "postgres://u@h/db", "--db-max-open-conns", "abc"},
	} {
		if _, err := ParseCLI(args, CommandServe, nil); err == nil {
			t.Fatalf("ParseCLI(%v) succeeded", args)
		}
	}
}

func TestParseCLIDatabaseBackend(t *testing.T) {
	invocation, err := ParseCLI([]string{"--db", "postgres", "--db-dsn", "postgres://u@h:5432/db", "--db-max-open-conns", "32"}, CommandServe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.DB != "postgres" || invocation.DBDSN != "postgres://u@h:5432/db" || invocation.DBMaxOpenConns != 32 {
		t.Fatalf("invocation = %+v", invocation)
	}
	environment := map[string]string{
		"NEXTERM_DB":                "postgres",
		"NEXTERM_DB_DSN":            "postgres://env@h/db",
		"NEXTERM_DB_PASSWORD_FILE":  "/env/pgpass",
		"NEXTERM_DB_MAX_OPEN_CONNS": "24",
	}
	invocation, err = ParseCLI(nil, CommandServe, func(key string) string { return environment[key] })
	if err != nil {
		t.Fatal(err)
	}
	if invocation.DB != "postgres" || invocation.DBDSN != "postgres://env@h/db" || invocation.DBPasswordFile != "/env/pgpass" || invocation.DBMaxOpenConns != 24 {
		t.Fatalf("environment fallback = %+v", invocation)
	}
	bare, err := ParseCLI(nil, CommandServe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bare.DB != "sqlite" || bare.DBMaxOpenConns != 0 {
		t.Fatalf("bare defaults = %+v", bare)
	}
	if _, err := ParseCLI(nil, CommandServe, func(key string) string {
		if key == "NEXTERM_DB_MAX_OPEN_CONNS" {
			return "abc"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid NEXTERM_DB_MAX_OPEN_CONNS was accepted")
	}
	if _, err := ParseCLI(nil, CommandServe, func(key string) string {
		if key == "NEXTERM_DB" {
			return "bogus"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid NEXTERM_DB was accepted")
	}
}

func TestParseCLIAuthMode(t *testing.T) {
	invocation, err := ParseCLI([]string{"--auth=loopback", "--require-vault"}, CommandServe, nil)
	if err != nil || invocation.Auth != AuthLoopback || !invocation.RequireVault {
		t.Fatalf("auth flags = %+v, %v", invocation, err)
	}
	invocation, err = ParseCLI(nil, CommandServe, func(key string) string {
		if key == "NEXTERM_AUTH" {
			return AuthOff
		}
		return ""
	})
	if err != nil || invocation.Auth != AuthOff {
		t.Fatalf("auth environment = %+v, %v", invocation, err)
	}
	if _, err := ParseCLI([]string{"--auth=bogus"}, CommandServe, nil); err == nil {
		t.Fatal("invalid --auth value was accepted")
	}
	if _, err := ParseCLI(nil, CommandServe, func(key string) string {
		if key == "NEXTERM_AUTH" {
			return "bogus"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid NEXTERM_AUTH was accepted")
	}
}

func TestUsageIncludesSharedServerFlags(t *testing.T) {
	usage := Usage("nexterm-server", CommandServe)
	for _, want := range []string{"NEXTERM_MASTER_KEY_FILE", "platform", "--require-vault"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing %q: %q", want, usage)
		}
	}
	for _, unwanted := range []string{"rotate-token", "--master-key "} {
		if strings.Contains(usage, unwanted) {
			t.Fatalf("usage contains removed option %q: %q", unwanted, usage)
		}
	}
}
