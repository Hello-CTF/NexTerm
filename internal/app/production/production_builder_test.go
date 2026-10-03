//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/tasks"
)

func TestConcreteProductionCompositionAndPersistentTaskReopen(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if production.PersistentTasks() == nil || production.TokenStore() == nil || production.SyncRPCHandler() == nil {
		t.Fatal("production capabilities are not composed")
	}
	for _, command := range []string{
		"app_info", "app_platform", "session_connect", "session_connect_local", "terminal_attach", "terminal_attach_tab",
		"terminal_resize", "terminal_resize_flush", "fs_list", "fs_write", "asset_list", "asset_update", "layout_put",
		"vault_set_credential", "credential_update", "ai_model_save", "ai_conversation_list", "docker_exec_attach",
		"docker_action", "db_connect", "mount_create", "forward_create", "sync_digest",
	} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production is missing %s", command)
		}
	}
	if got := len(production.Dispatcher.Commands()); got < 120 {
		t.Fatalf("production command count = %d, commands = %v", got, production.Dispatcher.Commands())
	}
	for _, command := range []string{
		"ai_chat", "ai_cancel", "ai_confirm", "ai_answer", "ai_get_permission", "ai_set_permission",
		"ai_takeover_enter", "ai_takeover_exit", "ai_takeover_run",
		"ai_models", "ai_test_provider", "ai_get_provider", "ai_set_provider", "ai_presets",
		"ai_model_profiles", "ai_model_save", "ai_model_delete", "ai_model_activate", "ai_model_refresh", "ai_model_preset",
		"ai_conversation_list", "ai_conversation_create", "ai_conversation_delete", "ai_messages",
	} {
		if !slices.Contains(production.Dispatcher.Commands(), command) {
			t.Fatalf("production is missing AI command %s", command)
		}
	}
	if production.Services.Guard == nil || production.Services.Agent == nil || production.Services.Takeover == nil {
		t.Fatal("AI runtime services are not composed")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	owner := tasks.Owner{Kind: "test", ID: "owner"}
	result, err := production.PersistentTasks().Run(t.Context(), owner, tasks.Command{
		Path: "/bin/sh", Args: []string{"-c", "sleep 30"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Detached {
		t.Fatal("task did not detach")
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{Logger: logger}, DataDir: dataDir, Desktop: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := reopened.PersistentTasks().Get(t.Context(), owner, result.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != tasks.StateInterrupted || info.ExitCode != nil {
		t.Fatalf("reopened task = %+v", info)
	}
	infos, err := reopened.PersistentTasks().List(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != result.Info.ID {
		t.Fatalf("tasks were rerun or duplicated = %+v", infos)
	}
	if err := reopened.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reopened.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcreteProductionNormalizesRelativeDataDir(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	production, err := NewProduction(t.Context(), ProductionConfig{DataDir: "state", Desktop: true})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.dataDir != root+"/state" {
		t.Fatalf("data dir = %q, want %q", production.Services.dataDir, root+"/state")
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
