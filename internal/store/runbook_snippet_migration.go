package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// LegacyRunbookSnippetID derives the snippet ID a runbook template becomes.
// It is deterministic so that every device migrating or receiving the same
// template ends up with the same snippet and sync never duplicates it.
func LegacyRunbookSnippetID(templateID string) string {
	sum := sha256.Sum256([]byte("nexterm:runbook-template-snippet:" + templateID))
	raw := sum[:16]
	var encoded [26]byte
	for i := range encoded {
		var value byte
		for bit := 0; bit < 5; bit++ {
			value <<= 1
			sourceBit := i*5 + bit - 2
			if sourceBit >= 0 {
				value |= (raw[sourceBit/8] >> (7 - sourceBit%8)) & 1
			}
		}
		encoded[i] = crockfordAlphabet[value]
	}
	return string(encoded[:])
}

// SnippetStepsFromRunbookTemplate converts stored template steps into snippet
// steps: confirmation starts off and the originating snippet reference is dropped.
func SnippetStepsFromRunbookTemplate(steps []RunbookTemplateStep) []SnippetStep {
	out := make([]SnippetStep, len(steps))
	for i, step := range steps {
		out[i] = SnippetStep{ID: step.ID, Name: step.Name, Description: step.Description, Command: step.Command, Interactive: step.Interactive}
	}
	return out
}

// migrateRunbookTemplatesToSnippets turns every owner's runbook templates
// into multi-step snippets, one transaction per template, and removes the
// template row. It is idempotent: an already migrated template only has its
// leftover row removed. No sync tombstone is written, so other devices keep
// their template until they migrate it to the same derived snippet ID.
func (s *Store) migrateRunbookTemplatesToSnippets(ctx context.Context) error {
	var templates []model.RunbookTemplateRow
	if err := s.db.WithContext(ctx).Order("owner_id, id").Find(&templates).Error; err != nil {
		return err
	}
	for _, template := range templates {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return migrateRunbookTemplate(tx, template)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func migrateRunbookTemplate(tx *gorm.DB, template model.RunbookTemplateRow) error {
	legacy := []RunbookTemplateStep{}
	if err := json.Unmarshal([]byte(template.Steps), &legacy); err != nil {
		slog.Warn("runbook template left unmigrated: steps cannot be decoded", "template", template.ID, "error", err)
		return nil
	}
	steps := SnippetStepsFromRunbookTemplate(legacy)
	if len(steps) == 0 || ValidateSnippetSteps(steps) != nil || ValidateSnippetDescription(template.Description) != nil {
		slog.Warn("runbook template left unmigrated: content is not a valid snippet", "template", template.ID)
		return nil
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	id := LegacyRunbookSnippetID(template.ID)
	var owner []string
	if err := tx.Model(&model.SnippetRow{}).Where("id = ?", id).Limit(1).Pluck("owner_id", &owner).Error; err != nil {
		return err
	}
	if len(owner) > 0 && owner[0] != template.OwnerID {
		// Another owner already holds the derived ID (multi-user server only).
		id = ids.New()
		owner = nil
	}
	if len(owner) == 0 {
		var maxSort *int64
		if err := tx.Model(&model.SnippetRow{}).
			Where("owner_id = ? AND group_id IS NULL", template.OwnerID).
			Select("MAX(sort)").Scan(&maxSort).Error; err != nil {
			return err
		}
		sort := int64(0)
		if maxSort != nil {
			sort = *maxSort + 1
		}
		row := model.SnippetRow{
			ID: id, OwnerID: template.OwnerID, Name: template.Name, Description: template.Description,
			Steps: string(encoded), Body: SnippetStepsBody(steps), Sort: sort,
			CreatedAt: template.CreatedAt, UpdatedAt: template.UpdatedAt,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return tx.Where("owner_id = ? AND id = ?", template.OwnerID, template.ID).Delete(&model.RunbookTemplateRow{}).Error
}
