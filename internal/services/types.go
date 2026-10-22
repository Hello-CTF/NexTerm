package services

import (
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

var (
	ErrInvalid     = errors.New("invalid host services request")
	ErrUnsupported = errors.New("host services executor unavailable")
)

type ListRequest struct {
	BatchID                string                            `json:"batchId"`
	SessionIDs             []string                          `json:"sessionIds"`
	Sudo                   bool                              `json:"sudo,omitempty"`
	TimeoutMS              int64                             `json:"timeoutMs,omitempty"`
	MaxConcurrent          int                               `json:"maxConcurrent,omitempty"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type ActionRequest struct {
	BatchID                string                            `json:"batchId"`
	SessionIDs             []string                          `json:"sessionIds"`
	Unit                   string                            `json:"unit"`
	Action                 string                            `json:"action"`
	Sudo                   bool                              `json:"sudo,omitempty"`
	TimeoutMS              int64                             `json:"timeoutMs,omitempty"`
	MaxConcurrent          int                               `json:"maxConcurrent,omitempty"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type FindRequest struct {
	BatchID                string                            `json:"batchId"`
	SessionIDs             []string                          `json:"sessionIds"`
	Query                  FinderQuery                       `json:"query"`
	Sudo                   bool                              `json:"sudo,omitempty"`
	TimeoutMS              int64                             `json:"timeoutMs,omitempty"`
	MaxConcurrent          int                               `json:"maxConcurrent,omitempty"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type KillRequest struct {
	BatchID                string                            `json:"batchId"`
	SessionID              string                            `json:"sessionId"`
	PID                    int                               `json:"pid"`
	Sudo                   bool                              `json:"sudo,omitempty"`
	TimeoutMS              int64                             `json:"timeoutMs,omitempty"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type ServiceUnit struct {
	Name        string `json:"name"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

type ServiceHostResult struct {
	Result      execbatch.Result `json:"result"`
	Systemd     string           `json:"systemd"`
	FailureKind string           `json:"failureKind,omitempty"`
	Units       []ServiceUnit    `json:"units"`
}

type ServiceOperationBatch struct {
	BatchID string              `json:"batchId"`
	Command string              `json:"command"`
	Hosts   []ServiceHostResult `json:"hosts"`
}

type FinderQuery struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Listener struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Program string `json:"program"`
}

type Process struct {
	PID     int    `json:"pid"`
	User    string `json:"user"`
	CPU     string `json:"cpu"`
	Memory  string `json:"memory"`
	Elapsed string `json:"elapsed"`
	Name    string `json:"name"`
	Args    string `json:"args"`
}

type FinderMatch struct {
	Kind     string    `json:"kind"`
	Listener *Listener `json:"listener,omitempty"`
	Process  *Process  `json:"process,omitempty"`
}

type FinderHostResult struct {
	Result        execbatch.Result `json:"result"`
	ListenerTool  string           `json:"listenerTool,omitempty"`
	ListenerError string           `json:"listenerError,omitempty"`
	ProcessError  string           `json:"processError,omitempty"`
	Matches       []FinderMatch    `json:"matches"`
}

type FinderBatch struct {
	BatchID string             `json:"batchId"`
	Command string             `json:"command"`
	Hosts   []FinderHostResult `json:"hosts"`
}
