package store

import (
	"context"

	"gorm.io/gorm"
)

// MigrateRunbookTemplatesForTest re-runs the startup data migration.
func (s *Store) MigrateRunbookTemplatesForTest(ctx context.Context) error {
	return s.migrateRunbookTemplatesToSnippets(ctx)
}

// PlaceRunbookTemplateSnippetForTest runs the insert-with-collision step of the migration.
func (s *Store) PlaceRunbookTemplateSnippetForTest(ctx context.Context, ownerID, templateID string, row SnippetRow) (string, error) {
	var placed string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		id, err := s.placeTemplateSnippet(ctx, tx, ownerID, templateID, row)
		placed = id
		return err
	})
	return placed, err
}
