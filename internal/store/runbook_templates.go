package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RunbookTemplateRow = model.RunbookTemplateRow

const (
	RunbookTemplateMaxSteps        = 64
	RunbookTemplateMaxNameBytes    = 512
	RunbookTemplateMaxDescBytes    = 4096
	RunbookTemplateMaxCommandBytes = 64 << 10
	runbookTemplateMaxTemplates    = 500
)

// RunbookTemplateStep mirrors runbook.Step. Command is the snapshot taken at
// save time; SnippetID only records where it came from.
type RunbookTemplateStep struct {
	ID          string `json:"id"`
	SnippetID   string `json:"snippetId,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

// RunbookTemplate carries a stable id and an updated-at so it can join sync later.
type RunbookTemplate struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Steps       []RunbookTemplateStep `json:"steps"`
	Version     int32                 `json:"version"`
	CreatedAt   int64                 `json:"createdAt"`
	UpdatedAt   int64                 `json:"updatedAt"`
}

type RunbookTemplateInput struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Steps       []RunbookTemplateStep `json:"steps"`
}

func (s *Store) RunbookTemplateList(ctx context.Context) ([]RunbookTemplate, error) {
	t := query.Use(s.db).RunbookTemplateRow
	rows, err := t.WithContext(ctx).
		Where(ownerConditions(ctx, t.TableName())...).
		Order(t.Name, t.ID).Find()
	if err != nil {
		return nil, dbError(err)
	}
	out := make([]RunbookTemplate, 0, len(rows))
	for _, row := range rows {
		item, err := runbookTemplateFromRow(*row)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Store) RunbookTemplateGet(ctx context.Context, id string) (RunbookTemplate, error) {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return RunbookTemplate{}, badParam(fmt.Errorf("invalid runbook template reference"))
	}
	t := query.Use(s.db).RunbookTemplateRow
	row, err := t.WithContext(ctx).
		Where(t.ID.Eq(id)).
		Where(ownerConditions(ctx, t.TableName())...).
		First()
	if isNoRows(err) {
		return RunbookTemplate{}, notFound("Runbook 模板")
	}
	if err != nil {
		return RunbookTemplate{}, dbError(err)
	}
	return runbookTemplateFromRow(*row)
}

func (s *Store) RunbookTemplateCreate(ctx context.Context, input RunbookTemplateInput) (RunbookTemplate, error) {
	name, desc, steps, err := s.normalizeRunbookTemplate(ctx, input)
	if err != nil {
		return RunbookTemplate{}, err
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return RunbookTemplate{}, dbError(err)
	}
	t := query.Use(s.db).RunbookTemplateRow
	count, err := t.WithContext(ctx).Where(ownerConditions(ctx, t.TableName())...).Count()
	if err != nil {
		return RunbookTemplate{}, dbError(err)
	}
	if count >= runbookTemplateMaxTemplates {
		return RunbookTemplate{}, badParam(fmt.Errorf("runbook template limit of %d reached", runbookTemplateMaxTemplates))
	}
	now := ids.NowMS()
	row := RunbookTemplateRow{
		OwnerID: ipc.OwnerID(ctx), ID: ids.New(), Name: name, Description: desc,
		Steps: string(encoded), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := t.WithContext(ctx).Create(&row); err != nil {
		return RunbookTemplate{}, dbError(err)
	}
	return runbookTemplateFromRow(row)
}

// RunbookTemplateUpdate replaces the template content and bumps its version.
func (s *Store) RunbookTemplateUpdate(ctx context.Context, id string, input RunbookTemplateInput) (RunbookTemplate, error) {
	if _, err := s.RunbookTemplateGet(ctx, id); err != nil {
		return RunbookTemplate{}, err
	}
	name, desc, steps, err := s.normalizeRunbookTemplate(ctx, input)
	if err != nil {
		return RunbookTemplate{}, err
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return RunbookTemplate{}, dbError(err)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t := query.Use(tx).RunbookTemplateRow
		current, err := t.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(t.ID.Eq(id)).
			Where(ownerConditions(ctx, t.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("Runbook 模板")
		}
		if err != nil {
			return err
		}
		// Keep updated_at strictly increasing so a newer edit always wins a sync.
		_, err = t.WithContext(ctx).
			Where(t.ID.Eq(id)).
			Where(ownerConditions(ctx, t.TableName())...).
			Updates(map[string]any{
				"name": name, "description": desc, "steps": string(encoded),
				"version": current.Version + 1, "updated_at": max(ids.NowMS(), current.UpdatedAt+1),
			})
		return err
	})
	if err != nil {
		return RunbookTemplate{}, dbError(err)
	}
	return s.RunbookTemplateGet(ctx, id)
}

func (s *Store) RunbookTemplateDelete(ctx context.Context, id string) error {
	if _, err := s.RunbookTemplateGet(ctx, id); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t := query.Use(tx).RunbookTemplateRow
		if _, err := t.WithContext(ctx).
			Where(t.ID.Eq(id)).
			Where(ownerConditions(ctx, t.TableName())...).
			Delete(); err != nil {
			return err
		}
		// The tombstone lets the deletion reach the owner's other devices.
		return s.recordSyncTombstoneNow(ctx, tx, id, SyncTombstoneKindRunbookTemplate)
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func runbookTemplateFromRow(row RunbookTemplateRow) (RunbookTemplate, error) {
	steps := []RunbookTemplateStep{}
	if err := json.Unmarshal([]byte(row.Steps), &steps); err != nil {
		return RunbookTemplate{}, dbError(fmt.Errorf("decode runbook template steps: %w", err))
	}
	return RunbookTemplate{
		ID: row.ID, Name: row.Name, Description: row.Description, Steps: steps,
		Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

// normalizeRunbookTemplate validates the input and snapshots snippet bodies
// into steps that reference a snippet without carrying their own command.
func (s *Store) normalizeRunbookTemplate(ctx context.Context, input RunbookTemplateInput) (string, string, []RunbookTemplateStep, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > RunbookTemplateMaxNameBytes {
		return "", "", nil, badParam(fmt.Errorf("runbook template name must be 1-%d bytes", RunbookTemplateMaxNameBytes))
	}
	if len(input.Description) > RunbookTemplateMaxDescBytes {
		return "", "", nil, badParam(fmt.Errorf("runbook template description exceeds %d bytes", RunbookTemplateMaxDescBytes))
	}
	if len(input.Steps) == 0 || len(input.Steps) > RunbookTemplateMaxSteps {
		return "", "", nil, badParam(fmt.Errorf("runbook template steps must contain 1-%d entries", RunbookTemplateMaxSteps))
	}
	seen := make(map[string]struct{}, len(input.Steps))
	steps := make([]RunbookTemplateStep, len(input.Steps))
	for i, step := range input.Steps {
		if step.ID == "" || len(step.ID) > 96 || strings.TrimSpace(step.ID) != step.ID {
			return "", "", nil, badParam(fmt.Errorf("invalid runbook template step id"))
		}
		if _, dup := seen[step.ID]; dup {
			return "", "", nil, badParam(fmt.Errorf("duplicate runbook template step id %s", step.ID))
		}
		seen[step.ID] = struct{}{}
		if len(step.Name) > 512 || len(step.Description) > 4096 || len(step.SnippetID) > 128 {
			return "", "", nil, badParam(fmt.Errorf("runbook template step metadata is too large"))
		}
		if strings.TrimSpace(step.Command) != "" {
			// An explicit command is not a snapshot of the snippet, so no provenance is claimed.
			step.SnippetID = ""
		} else if step.SnippetID != "" {
			snippet, err := s.SnippetGet(ctx, step.SnippetID)
			if err != nil {
				return "", "", nil, err
			}
			step.Command = snippet.Body
		}
		if strings.TrimSpace(step.Command) == "" || len(step.Command) > RunbookTemplateMaxCommandBytes {
			return "", "", nil, badParam(fmt.Errorf("runbook template step command must be non-empty and at most %d bytes", RunbookTemplateMaxCommandBytes))
		}
		steps[i] = step
	}
	return name, input.Description, steps, nil
}
