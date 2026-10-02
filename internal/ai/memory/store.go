package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const entryColumns = "topic, content, version, redacted, created_at, updated_at, tenant, subject"

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadEntry(ctx context.Context, queryer rowQueryer, id string) (Entry, Scope, error) {
	entry := Entry{ID: id}
	var owner Scope
	err := queryer.QueryRowContext(ctx, "SELECT "+entryColumns+" FROM memory_entry WHERE id = ?", id).Scan(
		&entry.Topic, &entry.Content, &entry.Version, &entry.Redacted, &entry.CreatedAt, &entry.UpdatedAt,
		&owner.Tenant, &owner.Subject,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, Scope{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, Scope{}, fmt.Errorf("read semantic memory: %w", err)
	}
	return entry, owner, nil
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO memory_entry
		(id, tenant, subject, topic, content, version, redacted, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, entry.ID, scope.Tenant, scope.Subject, entry.Topic, entry.Content,
		entry.Version, entry.Redacted, entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("begin semantic memory edit: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	current, owner, err := loadEntry(ctx, tx, id)
	if err != nil {
		return Entry{}, err
	}
	if err := authorize(owner, scope); err != nil {
		return Entry{}, err
	}
	if current.Version != expectedVersion {
		return Entry{}, &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
	}
	next := current
	if input.Topic != nil {
		next.Topic = topic
	}
	if input.Content != nil {
		next.Content = content
		next.Redacted = redacted
	}
	next.Version++
	next.UpdatedAt = s.now()
	result, err := tx.ExecContext(ctx, `UPDATE memory_entry
		SET topic = ?, content = ?, version = ?, redacted = ?, updated_at = ?
		WHERE id = ? AND tenant = ? AND subject = ? AND version = ?`,
		next.Topic, next.Content, next.Version, next.Redacted, next.UpdatedAt,
		id, scope.Tenant, scope.Subject, expectedVersion)
	if err != nil {
		return Entry{}, fmt.Errorf("edit semantic memory: %w", err)
	}
	if err := exactlyOneRow(result); err != nil {
		return Entry{}, &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, fmt.Errorf("commit semantic memory edit: %w", err)
	}
	committed = true
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin semantic memory delete: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
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
	result, err := tx.ExecContext(ctx,
		"DELETE FROM memory_entry WHERE id = ? AND tenant = ? AND subject = ? AND version = ?",
		id, scope.Tenant, scope.Subject, expectedVersion)
	if err != nil {
		return fmt.Errorf("delete semantic memory: %w", err)
	}
	if err := exactlyOneRow(result); err != nil {
		return &VersionConflictError{ID: id, Expected: expectedVersion, Actual: current.Version}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit semantic memory delete: %w", err)
	}
	committed = true
	return nil
}

func exactlyOneRow(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("expected one affected row, got %d", count)
	}
	return nil
}

func (s *Store) Index(ctx context.Context, scope Scope) ([]TopicIndex, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT topic, id, version, redacted, updated_at
		FROM memory_entry WHERE tenant = ? AND subject = ? ORDER BY topic, id`, scope.Tenant, scope.Subject)
	if err != nil {
		return nil, fmt.Errorf("index semantic memory: %w", err)
	}
	defer rows.Close()
	result := []TopicIndex{}
	for rows.Next() {
		var topic string
		var item IndexEntry
		if err := rows.Scan(&topic, &item.ID, &item.Version, &item.Redacted, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan semantic memory index: %w", err)
		}
		if len(result) == 0 || result[len(result)-1].Topic != topic {
			result = append(result, TopicIndex{Topic: topic, Entries: []IndexEntry{}})
		}
		result[len(result)-1].Entries = append(result[len(result)-1].Entries, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate semantic memory index: %w", err)
	}
	return result, nil
}

func (s *Store) Settings(ctx context.Context, scope Scope) (Settings, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Settings{}, err
	}
	var settings Settings
	err = s.db.QueryRowContext(ctx, `SELECT injection_enabled, version FROM memory_settings
		WHERE tenant = ? AND subject = ?`, scope.Tenant, scope.Subject).Scan(&settings.InjectionEnabled, &settings.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read semantic memory settings: %w", err)
	}
	return settings, nil
}

func (s *Store) SetInjectionEnabled(ctx context.Context, scope Scope, enabled bool, expectedVersion uint64) (Settings, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Settings{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("begin semantic memory settings update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var actual uint64
	err = tx.QueryRowContext(ctx, "SELECT version FROM memory_settings WHERE tenant = ? AND subject = ?",
		scope.Tenant, scope.Subject).Scan(&actual)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Settings{}, fmt.Errorf("read semantic memory settings: %w", err)
	}
	if actual != expectedVersion {
		return Settings{}, &VersionConflictError{ID: "injection-settings", Expected: expectedVersion, Actual: actual}
	}
	next := Settings{InjectionEnabled: enabled, Version: actual + 1}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_settings
		(tenant, subject, injection_enabled, version, updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(tenant, subject) DO UPDATE SET
		injection_enabled = excluded.injection_enabled, version = excluded.version, updated_at = excluded.updated_at`,
		scope.Tenant, scope.Subject, next.InjectionEnabled, next.Version, s.now())
	if err != nil {
		return Settings{}, fmt.Errorf("update semantic memory settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, fmt.Errorf("commit semantic memory settings update: %w", err)
	}
	committed = true
	return next, nil
}
