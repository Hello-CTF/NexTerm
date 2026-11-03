package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// LegacyRunbookSnippetID derives the snippet ID a runbook template becomes.
// It is deterministic so that every device migrating or receiving the same
// template ends up with the same snippet and sync never duplicates it.
func LegacyRunbookSnippetID(templateID string) string {
	return deriveObjectID("nexterm:runbook-template-snippet:" + templateID)
}

// deriveObjectID maps a seed to a valid object ID: the first 16 bytes of its
// sha256, Crockford-encoded the way ids.New encodes its 128 bits.
func deriveObjectID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
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

// runbookSnippetMapKey names the setting that remembers which snippet an owner's
// template became when the derived ID was already taken by another owner.
func runbookSnippetMapKey(ownerID, templateID string) string {
	return "runbook_template.snippet." + ownerID + "." + templateID
}

// snippetOwnerOf reads who holds a snippet ID. A locking read returns the latest committed row even under
// REPEATABLE READ (MySQL/MariaDB), where a plain read would keep seeing the transaction's older snapshot;
// SQLite ignores the clause.
func snippetOwnerOf(tx *gorm.DB, id string, locking bool) (owner string, found bool, err error) {
	var owners []string
	q := tx.Model(&model.SnippetRow{}).Where("id = ?", id)
	if locking {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Limit(1).Pluck("owner_id", &owners).Error; err != nil {
		return "", false, err
	}
	if len(owners) == 0 {
		return "", false, nil
	}
	return owners[0], true, nil
}

// resolveTemplateSnippetID is the single place that decides which snippet ID a runbook template converts to
// for an owner: a remembered mapping first, then the derived ID, and when another owner holds the derived ID
// a fresh ID that is remembered (only when allocate is set).
func (s *Store) resolveTemplateSnippetID(ctx context.Context, tx *gorm.DB, ownerID, templateID string, allocate bool) (id string, found bool, err error) {
	setting := query.Use(tx).Setting
	mapped, err := setting.WithContext(ctx).Where(setting.Key.Eq(runbookSnippetMapKey(ownerID, templateID))).First()
	if err == nil {
		return mapped.Value, true, nil
	}
	if !isNoRows(err) {
		return "", false, err
	}
	derived := LegacyRunbookSnippetID(templateID)
	owner, taken, err := snippetOwnerOf(tx, derived, false)
	if err != nil {
		return "", false, err
	}
	if !taken || owner == ownerID {
		return derived, true, nil
	}
	if !allocate {
		return "", false, nil
	}
	fresh := ids.New()
	if err := settingUpsert(ctx, tx, runbookSnippetMapKey(ownerID, templateID), fresh, ids.NowMS()); err != nil {
		return "", false, err
	}
	return fresh, true, nil
}

// ResolveRunbookTemplateSnippetID returns the snippet ID the caller's runbook template converts to,
// allocating and remembering a fresh one if another owner holds the derived ID. Startup migration and
// the sync conversion of templates from older clients both use it.
func (s *Store) ResolveRunbookTemplateSnippetID(ctx context.Context, templateID string) (string, error) {
	var id string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		id, _, err = s.resolveTemplateSnippetID(ctx, tx, ipc.OwnerID(ctx), templateID, true)
		return err
	})
	if err != nil {
		return "", dbError(err)
	}
	return id, nil
}

// LookupRunbookTemplateSnippetID is ResolveRunbookTemplateSnippetID without allocation: found is false when the
// derived ID belongs to another owner and the template was never converted for this one.
func (s *Store) LookupRunbookTemplateSnippetID(ctx context.Context, templateID string) (string, bool, error) {
	id, found, err := s.resolveTemplateSnippetID(ctx, s.db, ipc.OwnerID(ctx), templateID, false)
	if err != nil {
		return "", false, dbError(err)
	}
	return id, found, nil
}

// RunbookTemplateUnmigratedCount counts the caller's legacy templates that are still in the table, that is
// the ones the startup migration could not turn into snippets.
func (s *Store) RunbookTemplateUnmigratedCount(ctx context.Context) (int64, error) {
	statement := s.db.WithContext(ctx).Model(&model.RunbookTemplateRow{})
	if owner := ipc.OwnerID(ctx); owner != "" {
		statement = statement.Where("owner_id = ?", owner)
	}
	var count int64
	if err := statement.Count(&count).Error; err != nil {
		return 0, dbError(err)
	}
	return count, nil
}

// migrateRunbookTemplatesToSnippets turns every owner's runbook templates
// into multi-step snippets, one transaction per template, and removes the
// template row. It is idempotent: an already migrated template only has its
// leftover row removed. No sync tombstone is written, so other devices keep
// their template until they migrate it to the same snippet ID.
func (s *Store) migrateRunbookTemplatesToSnippets(ctx context.Context) error {
	var templates []model.RunbookTemplateRow
	if err := s.db.WithContext(ctx).Order("owner_id, name, id").Find(&templates).Error; err != nil {
		return err
	}
	for _, template := range templates {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.migrateRunbookTemplate(ctx, tx, template)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateRunbookTemplate(ctx context.Context, tx *gorm.DB, template model.RunbookTemplateRow) error {
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
	id, _, err := s.resolveTemplateSnippetID(ctx, tx, template.OwnerID, template.ID, true)
	if err != nil {
		return err
	}
	if owner, held, err := snippetOwnerOf(tx, id, false); err != nil {
		return err
	} else if !held || owner != template.OwnerID {
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
		if _, err := s.placeTemplateSnippet(ctx, tx, template.OwnerID, template.ID, row); err != nil {
			return err
		}
	}
	return tx.Where("owner_id = ? AND id = ?", template.OwnerID, template.ID).Delete(&model.RunbookTemplateRow{}).Error
}

// placeTemplateSnippet inserts the converted snippet. If the ID was taken in the meantime (a row count of zero
// from the do-nothing insert, or any owner other than the template's after it), the owner is re-read: an ID the
// owner already holds means the template was migrated before, any other owner sends the snippet to a fresh ID
// that is remembered like the one resolveTemplateSnippetID allocates. The final ID is returned.
func (s *Store) placeTemplateSnippet(ctx context.Context, tx *gorm.DB, ownerID, templateID string, row model.SnippetRow) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return "", err
		}
		// The owner is re-read whatever the row count says, because drivers differ in what a skipped insert reports.
		owner, held, err := snippetOwnerOf(tx, row.ID, true)
		if err != nil {
			return "", err
		}
		if held && owner == ownerID {
			return row.ID, nil
		}
		row.ID = ids.New()
		if err := settingUpsert(ctx, tx, runbookSnippetMapKey(ownerID, templateID), row.ID, ids.NowMS()); err != nil {
			return "", err
		}
	}
	return "", errors.New("runbook template snippet id kept colliding")
}
