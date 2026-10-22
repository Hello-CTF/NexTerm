package production

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"gorm.io/gorm"
)

type productionMemoryMigrationReport struct {
	Import                    memory.LegacyImportReport
	SkippedNoActiveSuperadmin bool
}

func prepareProductionMemory(ctx context.Context, services *ProductionServices) (*memory.Store, error) {
	if services.dataDir == "" {
		return nil, nil
	}
	memoryStore, err := memory.NewStore(services.Store.GORM())
	if err != nil {
		return nil, err
	}
	path := filepath.Join(services.dataDir, "memory.db")
	report, err := migrateProductionLegacyMemory(ctx, services.Store.GORM(), memoryStore, path, services.desktop || owner.OpenAccess(ctx))
	if err != nil {
		return nil, err
	}
	if report.SkippedNoActiveSuperadmin {
		message := "旧版 AI 记忆暂未迁移：没有 active superadmin；原始数据已保留，将在下次启动重试"
		slog.Warn("legacy semantic memory migration deferred", "reason", "no active superadmin", "path", path)
		_ = ipc.Emit(ctx, services.Events, ipc.TopicAppError, ipc.AppErrorEvent{Code: "memory_migration", Message: message})
		return memoryStore, nil
	}
	if report.Import.SourceFound {
		values := []any{
			"entries_inserted", report.Import.EntriesInserted,
			"entries_updated", report.Import.EntriesUpdated,
			"entries_skipped", report.Import.EntriesSkipped,
			"entry_conflicts", report.Import.EntryConflicts,
			"settings_imported", report.Import.SettingsImported,
			"settings_preserved", report.Import.SettingsPreserved,
			"archived", report.Import.ArchivePath != "",
		}
		if report.Import.EntryConflicts > 0 {
			slog.Warn("legacy semantic memory migration completed with conflicts", values...)
		} else {
			slog.Info("migrated legacy semantic memory", values...)
		}
	} else if report.Import.ArchivePath != "" {
		slog.Info("completed legacy semantic memory archive", "path", report.Import.ArchivePath)
	}
	return memoryStore, nil
}

func migrateProductionLegacyMemory(ctx context.Context, mainDB *gorm.DB, memoryStore *memory.Store, path string, shared bool) (productionMemoryMigrationReport, error) {
	var report productionMemoryMigrationReport
	target := productionMemoryScope
	if !shared {
		pending, sourceFound, err := legacyMemoryMigrationPending(path)
		if err != nil || !pending {
			return report, err
		}
		if sourceFound {
			ownerID, err := firstActiveSuperadminForMemory(ctx, mainDB)
			if err != nil {
				return report, err
			}
			if ownerID == "" {
				report.Import.SourceFound = true
				report.SkippedNoActiveSuperadmin = true
				return report, nil
			}
			target = memory.Scope{Tenant: "user", Subject: ownerID}
		}
	}
	imported, err := memoryStore.ImportLegacySidecar(ctx, path, productionMemoryScope, target)
	report.Import = imported
	if err != nil {
		return report, fmt.Errorf("migrate legacy semantic memory: %w", err)
	}
	return report, nil
}

func legacyMemoryMigrationPending(path string) (pending, sourceFound bool, returnErr error) {
	for _, candidate := range []string{path, path + ".migrated"} {
		_, err := os.Stat(candidate)
		if err == nil {
			pending = true
			sourceFound = sourceFound || candidate == path
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, false, fmt.Errorf("inspect legacy semantic memory migration state: %w", err)
		}
	}
	return pending, sourceFound, nil
}

func firstActiveSuperadminForMemory(ctx context.Context, database *gorm.DB) (string, error) {
	var user store.AppUser
	err := database.WithContext(ctx).
		Select("id").
		Where("role = ? AND state = ?", "superadmin", "active").
		Order("created_at").Order("id").
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("select first active superadmin for legacy semantic memory: %w", err)
	}
	return user.ID, nil
}
