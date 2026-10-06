package app

import (
	"testing"
)

func TestParseCLICommandShapesAndFlagPositions(t *testing.T) {
	environment := map[string]string{
		"NEXTERM_LISTEN":     "127.0.0.1:9000",
		"NEXTERM_DATA_DIR":   "/env/data",
		"NEXTERM_WEB_ROOT":   "/env/web",
		"NEXTERM_MASTER_KEY": "env-secret",
	}
	getenv := func(key string) string { return environment[key] }

	invocation, err := ParseCLI([]string{"--data-dir", "/flag/data", "serve", "--sync-only=false"}, CommandServe, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Command != CommandServe || invocation.DataDir != "/flag/data" || invocation.SyncOnly {
		t.Fatalf("invocation = %+v", invocation)
	}
	if invocation.Listen != "127.0.0.1:9000" || invocation.WebRoot != "/env/web" || invocation.MasterKey != "env-secret" {
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
		{"--listen", "missing-port"},
		{"--listen", "127.0.0.1:70000"},
		{"--sync-only=perhaps"},
	} {
		if _, err := ParseCLI(args, CommandServe, nil); err == nil {
			t.Fatalf("ParseCLI(%v) succeeded", args)
		}
	}
}
