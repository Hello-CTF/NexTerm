package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestPrintAccountInitCodeLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := account.New(database.GORM())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var console bytes.Buffer
	if err := printAccountInitCode(ctx, accounts, &console, logger); err != nil {
		t.Fatal(err)
	}
	output := console.String()
	const marker = "一次性初始化码: "
	index := strings.Index(output, marker)
	if index < 0 {
		t.Fatalf("initialization code was not printed to console: %q", output)
	}
	code := output[index+len(marker):]
	if lineEnd := strings.Index(code, "\n"); lineEnd >= 0 {
		code = code[:lineEnd]
	}
	code = strings.TrimSpace(code)
	if code == "" || strings.ContainsAny(code, " \n\t") {
		t.Fatalf("invalid initialization code format: %q", code)
	}

	console.Reset()
	if err := printAccountInitCode(ctx, accounts, &console, logger); err != nil {
		t.Fatal(err)
	}
	if console.Len() != 0 {
		t.Fatalf("initialization code must not be printed again after generation: %q", console.String())
	}

	if _, err := accounts.InitSuperadmin(ctx, code, "root", "password-123"); err != nil {
		t.Fatalf("initialization code printed to console cannot complete initialization: %v", err)
	}

	console.Reset()
	if err := printAccountInitCode(ctx, accounts, &console, logger); err != nil {
		t.Fatal(err)
	}
	if console.Len() != 0 {
		t.Fatalf("initialization code must not be printed after initialization: %q", console.String())
	}
}
