package store_test

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestExecBatchHistoryShellDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctx := execBatchDatabaseContext("owner-a")

	// A writer that does not name a shell stores posix.
	legacy := execBatchDatabaseRow("legacy", 1000, store.ExecBatchStatusSuccess)
	if err := database.ExecBatchHistorySave(ctx, legacy, []store.ExecBatchResultRow{execBatchDatabaseResult("s1", 0, "success")}); err != nil {
		t.Fatal(err)
	}
	history, err := database.ExecBatchHistoryGet(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Shell != store.ExecBatchShellPOSIX {
		t.Fatalf("default shell = %q, want posix", history.Batch.Shell)
	}
	// A row written before the column existed reads as posix as well.
	if _, err := database.DB().ExecContext(ctx, `UPDATE exec_batch SET shell = '' WHERE id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	history, err = database.ExecBatchHistoryGet(ctx, "legacy")
	if err != nil || history.Batch.Shell != store.ExecBatchShellPOSIX {
		t.Fatalf("empty shell read = %q, %v", history.Batch.Shell, err)
	}

	windows := execBatchDatabaseRow("windows", 2000, store.ExecBatchStatusSuccess)
	windows.Shell = store.ExecBatchShellPowerShell
	windows.TargetCount = 2
	skipped := execBatchDatabaseResult("linux", 1, "skipped")
	skipped.ErrorKind = store.ExecBatchErrorShellUnsupported
	skipped.StartedAt, skipped.DurationMS = nil, 0
	if err := database.ExecBatchHistorySave(ctx, windows, []store.ExecBatchResultRow{execBatchDatabaseResult("win", 0, "success"), skipped}); err != nil {
		t.Fatal(err)
	}
	history, err = database.ExecBatchHistoryGet(ctx, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if history.Batch.Shell != store.ExecBatchShellPowerShell || len(history.Results) != 2 ||
		history.Results[1].Status != "skipped" || history.Results[1].ErrorKind != store.ExecBatchErrorShellUnsupported {
		t.Fatalf("powershell history = %+v", history)
	}
	page, err := database.ExecBatchHistoryList(ctx, store.ExecBatchHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	shells := map[string]string{}
	for _, item := range page.Items {
		shells[item.ID] = item.Shell
	}
	if shells["windows"] != store.ExecBatchShellPowerShell || shells["legacy"] != store.ExecBatchShellPOSIX {
		t.Fatalf("listed shells = %v", shells)
	}

	// The running row and its finished upsert keep one shell.
	running := execBatchDatabaseRow("cmd", 3000, store.ExecBatchStatusRunning)
	running.Shell = store.ExecBatchShellCmd
	if err := database.ExecBatchHistorySave(ctx, running, nil); err != nil {
		t.Fatal(err)
	}
	finished := execBatchDatabaseRow("cmd", 3000, store.ExecBatchStatusFailed)
	finished.Shell = store.ExecBatchShellCmd
	if err := database.ExecBatchHistorySave(ctx, finished, []store.ExecBatchResultRow{execBatchDatabaseResult("w", 0, "exit_error")}); err != nil {
		t.Fatal(err)
	}
	if history, err = database.ExecBatchHistoryGet(ctx, "cmd"); err != nil || history.Batch.Shell != store.ExecBatchShellCmd {
		t.Fatalf("cmd history = %+v, %v", history.Batch, err)
	}

	invalid := execBatchDatabaseRow("invalid", 4000, store.ExecBatchStatusRunning)
	invalid.Shell = "fish"
	if err := database.ExecBatchHistorySave(ctx, invalid, nil); err == nil {
		t.Fatal("an unknown shell was stored")
	}
}
