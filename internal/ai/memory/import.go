package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const legacyArchiveSuffix = ".migrated"

type LegacyImportReport struct {
	SourceFound       bool
	EntriesInserted   int
	EntriesUpdated    int
	EntriesSkipped    int
	EntryConflicts    int
	SettingsImported  bool
	SettingsPreserved bool
	ArchivePath       string
}

// ImportLegacySidecar imports an offline sidecar after the caller stops legacy writers.
// Its cross-process lock serializes cooperating importers; older binaries require single-instance upgrade orchestration.
func (s *Store) ImportLegacySidecar(ctx context.Context, path string, sourceScope, targetScope Scope) (LegacyImportReport, error) {
	return s.importLegacySidecar(ctx, path, sourceScope, targetScope, archiveLegacySidecar)
}

func (s *Store) importLegacySidecar(
	ctx context.Context,
	path string,
	sourceScope, targetScope Scope,
	archive func(string) (string, error),
) (report LegacyImportReport, returnErr error) {
	if strings.TrimSpace(path) == "" {
		return report, fmt.Errorf("%w: empty legacy memory path", ErrInvalidInput)
	}
	if filepath.IsAbs(path) {
		path = filepath.Clean(path)
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return report, fmt.Errorf("%w: resolve legacy memory path: %v", ErrInvalidInput, err)
		}
		path = absolute
	}
	if path == "." || path == string(filepath.Separator) {
		return report, fmt.Errorf("%w: empty legacy memory path", ErrInvalidInput)
	}
	sourceScope, err := normalizeScope(sourceScope)
	if err != nil {
		return report, err
	}
	targetScope, err = normalizeScope(targetScope)
	if err != nil {
		return report, err
	}
	pending, err := legacyMigrationPending(path)
	if err != nil {
		return report, err
	}
	if !pending {
		return report, nil
	}
	release, err := acquireLegacyMigrationLock(ctx, path+".migration.lock")
	if err != nil {
		return report, fmt.Errorf("lock legacy semantic memory migration: %w", err)
	}
	defer func() {
		if err := release(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("unlock legacy semantic memory migration: %w", err))
		}
	}()
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return report, fmt.Errorf("inspect legacy semantic memory: %w", err)
		}
		return resumeLegacyArchive(path, archive)
	}
	report.SourceFound = true
	legacy, err := Open(ctx, path)
	if err != nil {
		return report, err
	}
	defer func() {
		if legacy != nil {
			if err := legacy.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close legacy semantic memory: %w", err))
			}
		}
	}()

	entries, settings, err := readLegacyRows(ctx, legacy.db, sourceScope)
	if err != nil {
		return report, err
	}
	for index := range entries {
		entries[index].Tenant = targetScope.Tenant
		entries[index].Subject = targetScope.Subject
	}
	if settings != nil {
		settings.Tenant = targetScope.Tenant
		settings.Subject = targetScope.Subject
	}
	var imported LegacyImportReport
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index := range entries {
			if err := importLegacyEntry(ctx, tx, entries[index], targetScope, &imported); err != nil {
				return err
			}
		}
		if settings != nil {
			if err := importLegacySettings(ctx, tx, *settings, &imported); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return report, fmt.Errorf("import legacy semantic memory: %w", err)
	}
	imported.SourceFound = true
	report = imported
	if err := legacy.Close(); err != nil {
		legacy = nil
		return report, fmt.Errorf("close imported legacy semantic memory: %w", err)
	}
	legacy = nil
	archivePath, err := archive(path)
	if err != nil {
		return report, fmt.Errorf("archive imported legacy semantic memory: %w", err)
	}
	report.ArchivePath = archivePath
	return report, nil
}

func readLegacyRows(ctx context.Context, db *gorm.DB, scope Scope) ([]entryRow, *settingsRow, error) {
	var entries []entryRow
	var settings *settingsRow
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range []any{&entryRow{}, &settingsRow{}} {
			var foreign int64
			if err := tx.Model(row).
				Where("tenant <> ? OR subject <> ?", scope.Tenant, scope.Subject).
				Count(&foreign).Error; err != nil {
				return fmt.Errorf("inspect legacy semantic memory scopes: %w", err)
			}
			if foreign != 0 {
				return fmt.Errorf("%w: legacy memory contains rows outside the source scope", ErrInvalidInput)
			}
		}
		if err := tx.Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).
			Order("id").Find(&entries).Error; err != nil {
			return fmt.Errorf("read legacy semantic memory entries: %w", err)
		}
		var row settingsRow
		err := tx.Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read legacy semantic memory settings: %w", err)
		}
		settings = &row
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return entries, settings, nil
}

func importLegacyEntry(ctx context.Context, tx *gorm.DB, row entryRow, targetScope Scope, report *LegacyImportReport) error {
	var existing entryRow
	err := tx.WithContext(ctx).Where("id = ?", row.ID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return fmt.Errorf("insert legacy memory entry %s: %w", row.ID, result.Error)
		}
		if result.RowsAffected == 1 {
			report.EntriesInserted++
		} else {
			report.EntriesSkipped++
			report.EntryConflicts++
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read target memory entry %s: %w", row.ID, err)
	}
	if existing == row {
		report.EntriesSkipped++
		return nil
	}
	if (Scope{Tenant: existing.Tenant, Subject: existing.Subject}) != targetScope ||
		row.Version <= existing.Version || row.UpdatedAt < existing.UpdatedAt {
		report.EntriesSkipped++
		report.EntryConflicts++
		return nil
	}
	result := tx.WithContext(ctx).Model(&entryRow{}).
		Where("id = ? AND tenant = ? AND subject = ? AND version = ? AND updated_at = ?",
			row.ID, targetScope.Tenant, targetScope.Subject, existing.Version, existing.UpdatedAt).
		Updates(map[string]any{
			"topic": row.Topic, "content": row.Content, "version": row.Version, "redacted": row.Redacted,
			"created_at": row.CreatedAt, "updated_at": row.UpdatedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("update target memory entry %s: %w", row.ID, result.Error)
	}
	if result.RowsAffected == 1 {
		report.EntriesUpdated++
	} else {
		report.EntriesSkipped++
		report.EntryConflicts++
	}
	return nil
}

func importLegacySettings(ctx context.Context, tx *gorm.DB, row settingsRow, report *LegacyImportReport) error {
	var existing settingsRow
	err := tx.WithContext(ctx).Where("tenant = ? AND subject = ?", row.Tenant, row.Subject).First(&existing).Error
	if err == nil {
		report.SettingsPreserved = true
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("read target memory settings: %w", err)
	}
	result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return fmt.Errorf("insert legacy memory settings: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		report.SettingsImported = true
	} else {
		report.SettingsPreserved = true
	}
	return nil
}

func resumeLegacyArchive(path string, archive func(string) (string, error)) (LegacyImportReport, error) {
	var report LegacyImportReport
	archivePath := path + legacyArchiveSuffix
	if _, err := os.Stat(archivePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
		return report, fmt.Errorf("inspect archived legacy semantic memory: %w", err)
	}
	archived, err := archive(path)
	if err != nil {
		return report, fmt.Errorf("resume legacy semantic memory archive: %w", err)
	}
	report.ArchivePath = archived
	return report, nil
}

func legacyMigrationPending(path string) (bool, error) {
	for _, candidate := range []string{path, path + legacyArchiveSuffix} {
		if _, err := os.Lstat(candidate); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect legacy semantic memory migration state: %w", err)
		}
	}
	return false, nil
}

func archiveLegacySidecar(path string) (string, error) {
	archivePath := path + legacyArchiveSuffix
	moved := make([]string, 0, 3)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := path + suffix
		destination := archivePath + suffix
		if _, err := os.Lstat(source); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if suffix == "" {
					if _, archiveErr := os.Lstat(destination); errors.Is(archiveErr, os.ErrNotExist) {
						return "", fmt.Errorf("legacy memory database is missing")
					} else if archiveErr != nil {
						return "", archiveRollback(path, archivePath, moved, archiveErr)
					}
				}
				continue
			}
			return "", archiveRollback(path, archivePath, moved, err)
		}
		if err := moveLegacyFileNoReplace(source, destination); err != nil {
			return "", archiveRollback(path, archivePath, moved, err)
		}
		moved = append(moved, suffix)
	}
	return archivePath, nil
}

func moveLegacyFileNoReplace(source, destination string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("legacy memory source is not a regular file: %s", source)
	}
	if destinationInfo, err := os.Lstat(destination); err == nil {
		if !os.SameFile(sourceInfo, destinationInfo) {
			return fmt.Errorf("archive destination already exists: %s", destination)
		}
		if err := os.Remove(source); err != nil {
			return fmt.Errorf("complete interrupted legacy memory archive %s: %w", source, err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(source, destination); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		return fmt.Errorf("remove archived legacy memory source %s: %w", source, err)
	}
	return nil
}

func archiveRollback(path, archivePath string, moved []string, cause error) error {
	err := cause
	for index := len(moved) - 1; index >= 0; index-- {
		suffix := moved[index]
		if rollbackErr := moveLegacyFileNoReplace(archivePath+suffix, path+suffix); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("restore legacy memory file %s: %w", path+suffix, rollbackErr))
		}
	}
	return err
}
