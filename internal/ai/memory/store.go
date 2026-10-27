package memory

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func entryFromRow(row entryRow) Entry {
	return Entry{
		ID: row.ID, Topic: row.Topic, Content: row.Content, Version: uint64(row.Version), Redacted: row.Redacted,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func loadEntry(ctx context.Context, db *gorm.DB, id string) (Entry, Scope, error) {
	var row entryRow
	err := db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Entry{}, Scope{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, Scope{}, fmt.Errorf("read semantic memory: %w", err)
	}
	return entryFromRow(row), Scope{Tenant: row.Tenant, Subject: row.Subject}, nil
}

func authorize(owner, scope Scope) error {
	if owner != scope {
		return ErrPermissionDenied
	}
	return nil
}

func (s *Store) Create(ctx context.Context, scope Scope, input CreateInput) (Entry, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Entry{}, err
	}
	topic, err := normalizeTopic(input.Topic)
	if err != nil {
		return Entry{}, err
	}
	if err := validateContent(input.Content); err != nil {
		return Entry{}, err
	}
	content, redacted, err := sanitize(input.Content, input.Secrets)
	if err != nil {
		return Entry{}, err
	}
	now := s.now()
	entry := Entry{
		ID: s.newID(), Topic: topic, Content: content, Version: 1, Redacted: redacted,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := validateID(entry.ID); err != nil {
		return Entry{}, fmt.Errorf("generate semantic memory ID: %w", err)
	}
	row := entryRow{
		ID: entry.ID, Tenant: scope.Tenant, Subject: scope.Subject, Topic: entry.Topic, Content: entry.Content,
		Version: int64(entry.Version), Redacted: entry.Redacted, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return Entry{}, fmt.Errorf("create semantic memory: %w", err)
	}
	return entry, nil
}

func (s *Store) Get(ctx context.Context, scope Scope, id string) (Entry, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Entry{}, err
	}
	if err := validateID(id); err != nil {
		return Entry{}, err
	}
	entry, owner, err := loadEntry(ctx, s.db, id)
	if err != nil {
		return Entry{}, err
	}
	if err := authorize(owner, scope); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (s *Store) Edit(ctx context.Context, scope Scope, id string, expectedVersion uint64, input EditInput) (Entry, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Entry{}, err
	}
	if err := validateID(id); err != nil {
		return Entry{}, err
	}
	if expectedVersion == 0 {
		return Entry{}, fmt.Errorf("%w: expected version must be positive", ErrInvalidInput)
	}
	if input.Topic == nil && input.Content == nil {
		return Entry{}, fmt.Errorf("%w: edit requires topic or content", ErrInvalidInput)
	}
	var topic string
	if input.Topic != nil {
		topic, err = normalizeTopic(*input.Topic)
		if err != nil {
			return Entry{}, err
		}
	}
	var content string
	var redacted bool
	if input.Content != nil {
		if err := validateContent(*input.Content); err != nil {
			return Entry{}, err
		}
		content, redacted, err = sanitize(*input.Content, input.Secrets)
		if err != nil {
			return Entry{}, err
		}
	} else if input.Secrets != SecretReject {
		return Entry{}, fmt.Errorf("%w: secret policy requires content", ErrInvalidInput)
	}

	var next Entry
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, owner, err := loadEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := authorize(owner, scope); err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
		}
		next = current
		if input.Topic != nil {
			next.Topic = topic
		}
		if input.Content != nil {
			next.Content = content
			next.Redacted = redacted
		}
		next.Version++
		// Keep updated_at strictly increasing so a newer edit always wins a sync.
		next.UpdatedAt = max(s.now(), current.UpdatedAt+1)
		result := tx.Model(&entryRow{}).
			Where("id = ? AND tenant = ? AND subject = ? AND version = ?", id, scope.Tenant, scope.Subject, expectedVersion).
			Updates(map[string]any{
				"topic": next.Topic, "content": next.Content, "version": int64(next.Version),
				"redacted": next.Redacted, "updated_at": next.UpdatedAt,
			})
		if result.Error != nil {
			return fmt.Errorf("edit semantic memory: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
		}
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return next, nil
}

func (s *Store) Delete(ctx context.Context, scope Scope, id string, expectedVersion uint64) error {
	scope, err := normalizeScope(scope)
	if err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if expectedVersion == 0 {
		return fmt.Errorf("%w: expected version must be positive", ErrInvalidInput)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, owner, err := loadEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := authorize(owner, scope); err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
		}
		result := tx.Where("id = ? AND tenant = ? AND subject = ? AND version = ?",
			id, scope.Tenant, scope.Subject, expectedVersion).Delete(&entryRow{})
		if result.Error != nil {
			return fmt.Errorf("delete semantic memory: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
		}
		return s.recordDeleteTombstone(ctx, tx, id)
	})
}

func (s *Store) Index(ctx context.Context, scope Scope) ([]TopicIndex, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	var rows []entryRow
	if err := s.db.WithContext(ctx).
		Select("topic", "id", "version", "redacted", "updated_at").
		Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).
		Order("topic, id").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("index semantic memory: %w", err)
	}
	result := []TopicIndex{}
	for _, row := range rows {
		item := IndexEntry{ID: row.ID, Version: uint64(row.Version), Redacted: row.Redacted, UpdatedAt: row.UpdatedAt}
		if len(result) == 0 || result[len(result)-1].Topic != row.Topic {
			result = append(result, TopicIndex{Topic: row.Topic, Entries: []IndexEntry{}})
		}
		result[len(result)-1].Entries = append(result[len(result)-1].Entries, item)
	}
	return result, nil
}

func defaultSettings() Settings {
	return Settings{InjectionEnabled: true, ToolsEnabled: true}
}

func loadSettings(ctx context.Context, db *gorm.DB, scope Scope) (Settings, bool, error) {
	var row settingsRow
	err := db.WithContext(ctx).Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultSettings(), false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("read semantic memory settings: %w", err)
	}
	return Settings{InjectionEnabled: row.InjectionEnabled, ToolsEnabled: row.ToolsEnabled, Version: uint64(row.Version)}, true, nil
}

func (s *Store) Settings(ctx context.Context, scope Scope) (Settings, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Settings{}, err
	}
	settings, _, err := loadSettings(ctx, s.db, scope)
	return settings, err
}

func (s *Store) SetInjectionEnabled(ctx context.Context, scope Scope, enabled bool, expectedVersion uint64) (Settings, error) {
	return s.UpdateSettings(ctx, scope, SettingsInput{InjectionEnabled: &enabled}, expectedVersion)
}

func (s *Store) UpdateSettings(ctx context.Context, scope Scope, input SettingsInput, expectedVersion uint64) (Settings, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Settings{}, err
	}
	if input.InjectionEnabled == nil && input.ToolsEnabled == nil {
		return Settings{}, fmt.Errorf("%w: settings update requires at least one flag", ErrInvalidInput)
	}
	current, exists, err := loadSettings(ctx, s.db, scope)
	if err != nil {
		return Settings{}, err
	}
	if current.Version != expectedVersion {
		return Settings{}, &VersionConflictError{ID: "memory-settings", Expected: expectedVersion, Actual: current.Version}
	}
	if current.Version >= uint64(1<<63-1) {
		return Settings{}, fmt.Errorf("%w: settings version exhausted", ErrInvalidInput)
	}
	next := current
	if input.InjectionEnabled != nil {
		next.InjectionEnabled = *input.InjectionEnabled
	}
	if input.ToolsEnabled != nil {
		next.ToolsEnabled = *input.ToolsEnabled
	}
	next.Version++
	write := settingsRow{
		Tenant: scope.Tenant, Subject: scope.Subject, InjectionEnabled: next.InjectionEnabled,
		ToolsEnabled: next.ToolsEnabled, Version: int64(next.Version), UpdatedAt: s.now(),
	}
	var result *gorm.DB
	if exists {
		result = s.db.WithContext(ctx).Model(&settingsRow{}).
			Where("tenant = ? AND subject = ? AND version = ?", scope.Tenant, scope.Subject, int64(expectedVersion)).
			Updates(map[string]any{
				"injection_enabled": next.InjectionEnabled, "tools_enabled": next.ToolsEnabled,
				"version": int64(next.Version), "updated_at": write.UpdatedAt,
			})
	} else {
		result = s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&write)
	}
	if result.Error != nil {
		return Settings{}, fmt.Errorf("update semantic memory settings: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return next, nil
	}
	current, _, err = loadSettings(ctx, s.db, scope)
	if err != nil {
		return Settings{}, err
	}
	return Settings{}, &VersionConflictError{ID: "memory-settings", Expected: expectedVersion, Actual: current.Version}
}
