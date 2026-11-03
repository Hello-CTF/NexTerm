package store

import "context"

// MigrateRunbookTemplatesForTest re-runs the startup data migration.
func (s *Store) MigrateRunbookTemplatesForTest(ctx context.Context) error {
	return s.migrateRunbookTemplatesToSnippets(ctx)
}
