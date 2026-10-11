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
		ID: row.ID, Topic: row.Topic, Content: row.Content, Version: row.Version, Redacted: row.Redacted,
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
		Version: entry.Version, Redacted: entry.Redacted, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
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
		next.UpdatedAt = s.now()
		result := tx.Model(&entryRow{}).
			Where("id = ? AND tenant = ? AND subject = ? AND version = ?", id, scope.Tenant, scope.Subject, expectedVersion).
			Updates(map[string]any{
				"topic": next.Topic, "content": next.Content, "version": next.Version,
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
		return nil
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
		item := IndexEntry{ID: row.ID, Version: row.Version, Redacted: row.Redacted, UpdatedAt: row.UpdatedAt}
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

func (s *Store) Settings(ctx context.Context, scope Scope) (Settings, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Settings{}, err
	}
	var row settingsRow
	err = s.db.WithContext(ctx).Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read semantic memory settings: %w", err)
	}
	return Settings{InjectionEnabled: row.InjectionEnabled, ToolsEnabled: row.ToolsEnabled, Version: row.Version}, nil
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
	var next Settings
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Settings
		var row settingsRow
		err := tx.Where("tenant = ? AND subject = ?", scope.Tenant, scope.Subject).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			current = defaultSettings()
		} else if err != nil {
			return fmt.Errorf("read semantic memory settings: %w", err)
		} else {
			current = Settings{InjectionEnabled: row.InjectionEnabled, ToolsEnabled: row.ToolsEnabled, Version: row.Version}
		}
		if current.Version != expectedVersion {
			return &VersionConflictError{ID: "memory-settings", Expected: expectedVersion, Actual: current.Version}
		}
		next = current
		if input.InjectionEnabled != nil {
			next.InjectionEnabled = *input.InjectionEnabled
		}
		if input.ToolsEnabled != nil {
			next.ToolsEnabled = *input.ToolsEnabled
		}
		next.Version = current.Version + 1
		write := settingsRow{
			Tenant: scope.Tenant, Subject: scope.Subject, InjectionEnabled: next.InjectionEnabled,
			ToolsEnabled: next.ToolsEnabled, Version: next.Version, UpdatedAt: s.now(),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant"}, {Name: "subject"}},
			DoUpdates: clause.AssignmentColumns([]string{"injection_enabled", "tools_enabled", "version", "updated_at"}),
		}).Create(&write).Error; err != nil {
			return fmt.Errorf("update semantic memory settings: %w", err)
		}
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	return next, nil
}
