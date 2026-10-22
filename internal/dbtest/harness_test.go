package dbtest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestFixtureUseReportContainsOnlyTestAndBackend(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "fixtures.jsonl")
	t.Setenv("NEXTERM_DBTEST_REPORT", reportPath)
	t.Setenv("NEXTERM_TEST_DB_BACKEND", "sqlite")
	t.Setenv("NEXTERM_TEST_DB_DSN", "")
	t.Run("contract", func(t *testing.T) {
		fixture := NewFixture(t)
		if fixture.Backend != "sqlite" {
			t.Fatalf("fixture backend = %s, want sqlite", fixture.Backend)
		}
		if _, err := os.Stat(reportPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fixture report exists before OpenStore: %v", err)
		}
		fixture.OpenStore(t)
	})
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"test\":\"TestFixtureUseReportContainsOnlyTestAndBackend/contract\",\"backend\":\"sqlite\"}\n"
	if string(contents) != want {
		t.Fatalf("fixture report = %q, want %q", contents, want)
	}
}

func TestOpenStoreFailureDoesNotWriteReport(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "fixtures.jsonl")
	t.Setenv("NEXTERM_DBTEST_REPORT", reportPath)
	fixture := &Fixture{Backend: "invalid", DSN: "unused"}
	database, err := fixture.openStore(t)
	if err == nil {
		t.Fatal("invalid backend opened a store")
	}
	if database != nil {
		t.Fatal("invalid backend returned a store")
	}
	if _, err := os.Stat(reportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture report exists after OpenStore failure: %v", err)
	}
}

func TestOpenStoreReportFailureClosesStore(t *testing.T) {
	t.Setenv("NEXTERM_TEST_DB_BACKEND", "sqlite")
	t.Setenv("NEXTERM_TEST_DB_DSN", "")
	var opened *store.Store
	t.Run("report failure", func(t *testing.T) {
		fixture := NewFixture(t)
		t.Setenv("NEXTERM_DBTEST_REPORT", t.TempDir())
		var err error
		opened, err = fixture.openStore(t)
		if err == nil {
			t.Fatal("report failure was ignored")
		}
		if opened == nil {
			t.Fatal("report failure did not return the opened store for cleanup verification")
		}
	})
	if opened == nil {
		t.Fatal("no store was opened")
	}
	if _, err := opened.DB().Exec("SELECT 1"); err == nil {
		t.Fatal("store is still usable after report failure cleanup")
	}
}

func TestOpenRawWritesOneReport(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "fixtures.jsonl")
	t.Setenv("NEXTERM_DBTEST_REPORT", reportPath)
	t.Setenv("NEXTERM_TEST_DB_BACKEND", "sqlite")
	t.Setenv("NEXTERM_TEST_DB_DSN", "")
	if raw := NewFixture(t).OpenRaw(t); raw == nil {
		t.Fatal("OpenRaw returned nil")
	}
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"test\":\"TestOpenRawWritesOneReport\",\"backend\":\"sqlite\"}\n"
	if string(contents) != want {
		t.Fatalf("fixture report = %q, want one record %q", contents, want)
	}
}
