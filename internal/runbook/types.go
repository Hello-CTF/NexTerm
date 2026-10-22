package runbook

import "github.com/Hello-CTF/NexTerm/internal/execbatch"

const (
	MaxSteps   = 64
	MaxIDBytes = 96
)

type ConfirmationPolicy string

const (
	ConfirmEveryStep      ConfirmationPolicy = "every_step"
	ConfirmProductionOnly ConfirmationPolicy = "production_only"
)

type RunStatus string

const (
	RunAwaitingRender                 RunStatus = "awaiting_render"
	RunAwaitingConfirmation           RunStatus = "awaiting_confirmation"
	RunRunning                        RunStatus = "running"
	RunAwaitingProductionConfirmation RunStatus = "awaiting_production_confirmation"
	RunSuccess                        RunStatus = "success"
	RunFailed                         RunStatus = "failed"
	RunCanceled                       RunStatus = "canceled"
)

type StepStatus string

const (
	StepPending                        StepStatus = "pending"
	StepAwaitingRender                 StepStatus = "awaiting_render"
	StepAwaitingConfirmation           StepStatus = "awaiting_confirmation"
	StepRunning                        StepStatus = "running"
	StepAwaitingProductionConfirmation StepStatus = "awaiting_production_confirmation"
	StepSuccess                        StepStatus = "success"
	StepFailed                         StepStatus = "failed"
	StepCanceled                       StepStatus = "canceled"
	StepSkipped                        StepStatus = "skipped"
)

type Step struct {
	ID          string `json:"id"`
	SnippetID   string `json:"snippetId,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

type StartRequest struct {
	RunID              string             `json:"runId"`
	RunbookID          string             `json:"runbookId"`
	RunbookName        string             `json:"runbookName"`
	Steps              []Step             `json:"steps"`
	SessionIDs         []string           `json:"sessionIds"`
	ConfirmationPolicy ConfirmationPolicy `json:"confirmationPolicy,omitempty"`
	StopOnFailure      *bool              `json:"stopOnFailure,omitempty"`
	TimeoutMS          int64              `json:"timeoutMs,omitempty"`
	MaxConcurrent      int                `json:"maxConcurrent,omitempty"`
}

type RenderedCommand struct {
	SessionID string `json:"sessionId"`
	Command   string `json:"command"`
}

type PrepareRequest struct {
	RunID      string            `json:"runId"`
	StepNumber int32             `json:"stepNumber"`
	Commands   []RenderedCommand `json:"commands"`
}

type AdvanceRequest struct {
	RunID                  string                            `json:"runId"`
	StepNumber             int32                             `json:"stepNumber"`
	Confirmed              bool                              `json:"confirmed,omitempty"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type RunRequest struct {
	RunID string `json:"runId"`
}

type StepState struct {
	Number           int32              `json:"number"`
	Step             Step               `json:"step"`
	Status           StepStatus         `json:"status"`
	RenderedCommands []RenderedCommand  `json:"renderedCommands,omitempty"`
	Results          []execbatch.Result `json:"results"`
	BatchIDs         []string           `json:"batchIds"`
	Error            string             `json:"error,omitempty"`
}

type RunState struct {
	RunID               string                                `json:"runId"`
	RunbookID           string                                `json:"runbookId"`
	RunbookName         string                                `json:"runbookName"`
	Status              RunStatus                             `json:"status"`
	ConfirmationPolicy  ConfirmationPolicy                    `json:"confirmationPolicy"`
	StopOnFailure       bool                                  `json:"stopOnFailure"`
	SessionIDs          []string                              `json:"sessionIds"`
	CurrentStep         int32                                 `json:"currentStep"`
	StepCount           int32                                 `json:"stepCount"`
	Steps               []StepState                           `json:"steps"`
	ActiveBatchID       string                                `json:"activeBatchId,omitempty"`
	ProductionChallenge *execbatch.ConfirmationRequiredDetail `json:"productionChallenge,omitempty"`
	ProductionCommand   string                                `json:"productionCommand,omitempty"`
	Error               string                                `json:"error,omitempty"`
}
