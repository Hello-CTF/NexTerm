package store

import "github.com/Hello-CTF/NexTerm/internal/store/model"

// RunbookTemplateRow is the legacy runbook template table. Templates are no
// longer created; the startup migration turns existing rows into multi-step
// snippets and sync converts templates received from older clients.
type RunbookTemplateRow = model.RunbookTemplateRow

// RunbookTemplateStep is the step shape stored in a legacy template. Command
// is the snapshot taken at save time; SnippetID only records its origin.
type RunbookTemplateStep struct {
	ID          string `json:"id"`
	SnippetID   string `json:"snippetId,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	Interactive bool   `json:"interactive,omitempty"`
}

// SyncTombstoneKindRunbookTemplate names the legacy runbook_template sync kind.
const SyncTombstoneKindRunbookTemplate = "runbook_template"
