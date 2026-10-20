package store

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"gorm.io/gen"
	"gorm.io/gen/field"
)

type aiHistoryInternalAccessKey struct{}

// WithAIHistoryInternalAccess marks trusted run recovery and restored-job
// authorization. Account mode still excludes ownerless quarantine records.
func WithAIHistoryInternalAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, aiHistoryInternalAccessKey{}, true)
}

func aiHistoryInternalAccess(ctx context.Context) bool {
	internal, _ := ctx.Value(aiHistoryInternalAccessKey{}).(bool)
	return internal
}

func (s *Store) SetAIHistoryShared(shared bool) {
	s.aiHistoryShared.Store(shared)
}

func (s *Store) aiHistoryOwnerConditions(ctx context.Context, table string) []gen.Condition {
	if aiHistoryInternalAccess(ctx) {
		if s.aiHistoryShared.Load() {
			return nil
		}
		return []gen.Condition{field.NewString(table, "owner_id").Neq("")}
	}
	ownerID := ipc.OwnerID(ctx)
	if ownerID == "" {
		if s.aiHistoryShared.Load() {
			return nil
		}
		return []gen.Condition{field.NewUnsafeFieldRaw("1 = 0")}
	}
	if role, ok := ipc.RoleFromContext(ctx); ok && role == "superadmin" {
		return []gen.Condition{field.NewString(table, "owner_id").In(ownerID, "")}
	}
	return []gen.Condition{field.NewString(table, "owner_id").Eq(ownerID)}
}
