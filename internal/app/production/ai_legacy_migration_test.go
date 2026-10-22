package production

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestProductionMemoryLegacyMigrationDesktopAndOpen(t *testing.T) {
	for _, test := range []struct {
		name    string
		desktop bool
		ctx     context.Context
	}{
		{name: "desktop", desktop: true, ctx: context.Background()},
		{name: "open", desktop: false, ctx: owner.WithOpenAccess(context.Background())},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, "memory.db")
			legacy := writeProductionLegacyMemory(t, path, "shared-legacy-memory")
			memoryStore, err := prepareProductionMemory(test.ctx, &ProductionServices{Store: database, dataDir: dataDir, desktop: test.desktop})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := memoryStore.Get(context.Background(), productionMemoryScope, legacy.ID)
			if err != nil || entry.Content != "shared-legacy-memory" {
				t.Fatalf("shared migrated entry = %+v, err = %v", entry, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("shared legacy source remains: %v", err)
			}
			if _, err := os.Stat(path + ".migrated"); err != nil {
				t.Fatalf("shared legacy archive missing: %v", err)
			}
		})
	}
}
