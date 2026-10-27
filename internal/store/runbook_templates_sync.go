package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SyncTombstoneKindRunbookTemplate marks a deleted runbook template in sync_tombstone.
const SyncTombstoneKindRunbookTemplate = "runbook_template"

// validateSyncedRunbookTemplate applies the save-time bounds to a template
// received through sync. Steps already carry their command snapshot, so the
// snippet ID is kept as provenance instead of being resolved again.
func validateSyncedRunbookTemplate(template RunbookTemplate) error {
	if err := EnsureID(template.ID); err != nil {
		return err
	}
	name := strings.TrimSpace(template.Name)
	if name == "" || name != template.Name || len(name) > RunbookTemplateMaxNameBytes {
		return badParam(fmt.Errorf("runbook template name must be 1-%d bytes", RunbookTemplateMaxNameBytes))
	}
	if len(template.Description) > RunbookTemplateMaxDescBytes {
		return badParam(fmt.Errorf("runbook template description exceeds %d bytes", RunbookTemplateMaxDescBytes))
	}
	if len(template.Steps) == 0 || len(template.Steps) > RunbookTemplateMaxSteps {
		return badParam(fmt.Errorf("runbook template steps must contain 1-%d entries", RunbookTemplateMaxSteps))
	}
	if template.Version <= 0 {
		return badParam(errors.New("runbook template version must be positive"))
	}
	seen := make(map[string]struct{}, len(template.Steps))
	for _, step := range template.Steps {
		if step.ID == "" || len(step.ID) > 96 || strings.TrimSpace(step.ID) != step.ID {
			return badParam(errors.New("invalid runbook template step id"))
		}
		if _, dup := seen[step.ID]; dup {
			return badParam(fmt.Errorf("duplicate runbook template step id %s", step.ID))
		}
		seen[step.ID] = struct{}{}
		if len(step.Name) > 512 || len(step.Description) > 4096 || len(step.SnippetID) > 128 {
			return badParam(errors.New("runbook template step metadata is too large"))
		}
		if strings.TrimSpace(step.Command) == "" || len(step.Command) > RunbookTemplateMaxCommandBytes {
			return badParam(fmt.Errorf("runbook template step command must be non-empty and at most %d bytes", RunbookTemplateMaxCommandBytes))
		}
	}
	return nil
}

// RunbookTemplateSyncPut writes a template received through sync with its
// peer version and revision. The write is bound to expected, the template
// the caller decided on (nil: it must not exist); a template that moved
// since reports ErrWriteConflict. The template's deletion tombstone is
// cleared in the same transaction.
func (s *Store) RunbookTemplateSyncPut(ctx context.Context, template RunbookTemplate, expected *RunbookTemplate) error {
	if err := validateSyncedRunbookTemplate(template); err != nil {
		return err
	}
	encoded, err := json.Marshal(template.Steps)
	if err != nil {
		return dbError(err)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t := query.Use(tx).RunbookTemplateRow
		current, err := t.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(t.ID.Eq(template.ID)).
			Where(ownerConditions(ctx, t.TableName())...).
			First()
		if isNoRows(err) {
			current = nil
		} else if err != nil {
			return err
		}
		if (current == nil) != (expected == nil) || current != nil && current.UpdatedAt != expected.UpdatedAt {
			return ErrWriteConflict
		}
		if current == nil {
			count, err := t.WithContext(ctx).Where(ownerConditions(ctx, t.TableName())...).Count()
			if err != nil {
				return err
			}
			if count >= runbookTemplateMaxTemplates {
				return badParam(fmt.Errorf("runbook template limit of %d reached", runbookTemplateMaxTemplates))
			}
			row := RunbookTemplateRow{
				OwnerID: ipc.OwnerID(ctx), ID: template.ID, Name: template.Name, Description: template.Description,
				Steps: string(encoded), Version: template.Version, CreatedAt: template.CreatedAt, UpdatedAt: template.UpdatedAt,
			}
			if err := t.WithContext(ctx).Create(&row); err != nil {
				return err
			}
		} else if _, err := t.WithContext(ctx).
			Where(t.ID.Eq(template.ID), t.UpdatedAt.Eq(current.UpdatedAt)).
			Where(ownerConditions(ctx, t.TableName())...).
			Updates(map[string]any{
				"name": template.Name, "description": template.Description, "steps": string(encoded),
				"version": template.Version, "created_at": template.CreatedAt, "updated_at": template.UpdatedAt,
			}); err != nil {
			return err
		}
		return deleteSyncTombstone(ctx, tx, template.ID)
	})
	if err != nil {
		if errors.Is(err, ErrWriteConflict) {
			return err
		}
		return dbError(err)
	}
	return nil
}

// RunbookTemplateSyncDelete applies a synced deletion: the owner's template
// is removed unless it changed after deletedAt, and the tombstone is kept.
// It reports whether the tombstone took effect.
func (s *Store) RunbookTemplateSyncDelete(ctx context.Context, id string, deletedAt int64) (bool, error) {
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t := query.Use(tx).RunbookTemplateRow
		current, err := t.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(t.ID.Eq(id)).
			Where(ownerConditions(ctx, t.TableName())...).
			First()
		if err != nil && !isNoRows(err) {
			return err
		}
		if err == nil {
			if current.UpdatedAt > deletedAt {
				return nil
			}
			if _, err := t.WithContext(ctx).Where(t.ID.Eq(id)).Where(ownerConditions(ctx, t.TableName())...).Delete(); err != nil {
				return err
			}
		}
		applied = true
		return s.syncTombstonePut(ctx, tx, id, SyncTombstoneKindRunbookTemplate, deletedAt)
	})
	if err != nil {
		return false, dbError(err)
	}
	return applied, nil
}
