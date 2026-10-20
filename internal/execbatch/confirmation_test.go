package execbatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type confirmationAuditRecord struct {
	ownerID string
	input   store.AuditInput
}

type confirmationAuditor struct {
	mu      sync.Mutex
	records []confirmationAuditRecord
	err     error
}

func (a *confirmationAuditor) AuditInsert(ctx context.Context, input store.AuditInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.records = append(a.records, confirmationAuditRecord{ownerID: ipc.OwnerID(ctx), input: input})
	return nil
}

func (a *confirmationAuditor) snapshot() []confirmationAuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]confirmationAuditRecord(nil), a.records...)
}

func newConfirmationService(execCalls *atomic.Int32, auditor Auditor) *Service {
	newTransport := func() *fakeTransport {
		return newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			execCalls.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		})
	}
	production := targetFor("prod", newTransport())
	production.SessionName = "Production API"
	production.Hostname = "api.production.example"
	production.Environment = "production"
	staging := targetFor("staging", newTransport())
	staging.SessionName = "Staging API"
	staging.Hostname = "api.staging.example"
	staging.Environment = "staging"
	return New(Config{
		Resolver: &fakeResolver{targets: map[string]Target{"prod": production, "staging": staging}},
		Auditor:  auditor,
		Now:      func() time.Time { return time.UnixMilli(123456789) },
	})
}

func productionRequest() Request {
	return Request{
		BatchID: "batch-production", SessionIDs: []string{"prod", "staging"},
		Command: "deploy --token=super-secret",
	}
}

func requireProductionChallenge(t *testing.T, service *Service, request Request) ConfirmationRequiredDetail {
	t.Helper()
	_, err := service.Execute(t.Context(), "owner-a", request)
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) || !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("production execute error = %v, want confirmation required", err)
	}
	detail := required.IPCDetail()
	if detail.Kind != ConfirmationRequiredKind || detail.BatchID != request.BatchID || detail.TargetSetHash == "" || detail.CommandHash == "" {
		t.Fatalf("confirmation detail = %+v", detail)
	}
	if len(detail.ProductionTargets) != 1 || detail.ProductionTargets[0].Hostname != "api.production.example" {
		t.Fatalf("production targets = %+v", detail.ProductionTargets)
	}
	return detail
}

func TestExecuteProductionRejectsMissingForgedAndMismatchedConfirmation(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		var execCalls atomic.Int32
		auditor := &confirmationAuditor{}
		service := newConfirmationService(&execCalls, auditor)
		requireProductionChallenge(t, service, productionRequest())
		if got := execCalls.Load(); got != 0 {
			t.Fatalf("executed %d commands without confirmation", got)
		}
		if got := len(auditor.snapshot()); got != 0 {
			t.Fatalf("recorded %d confirmation audits without confirmation", got)
		}
	})

	for _, test := range []struct {
		name  string
		forge func(*Request, ConfirmationRequiredDetail)
	}{
		{
			name: "forged",
			forge: func(request *Request, detail ConfirmationRequiredDetail) {
				request.ProductionConfirmation = &ProductionConfirmation{Confirmed: true, TargetSetHash: "forged", CommandHash: detail.CommandHash}
			},
		},
		{
			name: "not explicitly confirmed",
			forge: func(request *Request, detail ConfirmationRequiredDetail) {
				request.ProductionConfirmation = &ProductionConfirmation{Confirmed: false, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
			},
		},
		{
			name: "target set mismatch",
			forge: func(request *Request, detail ConfirmationRequiredDetail) {
				request.SessionIDs = []string{"prod"}
				request.ProductionConfirmation = &ProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
			},
		},
		{
			name: "command mismatch",
			forge: func(request *Request, detail ConfirmationRequiredDetail) {
				request.Command = "deploy --token=different-secret"
				request.ProductionConfirmation = &ProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var execCalls atomic.Int32
			auditor := &confirmationAuditor{}
			service := newConfirmationService(&execCalls, auditor)
			request := productionRequest()
			detail := requireProductionChallenge(t, service, request)
			test.forge(&request, detail)
			if _, err := service.Execute(t.Context(), "owner-a", request); !errors.Is(err, ErrConfirmationRequired) {
				t.Fatalf("confirmation error = %v, want confirmation required", err)
			}
			if got := execCalls.Load(); got != 0 {
				t.Fatalf("executed %d commands with invalid confirmation", got)
			}
			if got := len(auditor.snapshot()); got != 0 {
				t.Fatalf("recorded %d audits for rejected confirmation", got)
			}
		})
	}
}

func TestExecuteProductionConfirmationCannotBeReplayedForAnotherBatch(t *testing.T) {
	var execCalls atomic.Int32
	auditor := &confirmationAuditor{}
	service := newConfirmationService(&execCalls, auditor)
	request := productionRequest()
	detailA := requireProductionChallenge(t, service, request)

	request.BatchID = "batch-production-b"
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detailA.TargetSetHash, CommandHash: detailA.CommandHash,
	}
	_, err := service.Execute(t.Context(), "owner-a", request)
	if got := execCalls.Load(); got != 0 {
		t.Fatalf("replayed confirmation executed %d commands", got)
	}
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) || !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("replayed confirmation error = %v, want confirmation required", err)
	}
	detailB := required.IPCDetail()
	if detailB.BatchID != request.BatchID || detailB.TargetSetHash == detailA.TargetSetHash {
		t.Fatalf("batch-bound challenge = %+v, previous hash = %q", detailB, detailA.TargetSetHash)
	}

	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detailB.TargetSetHash, CommandHash: detailB.CommandHash,
	}
	if _, err := service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatalf("batch-bound confirmation failed: %v", err)
	}
	if got := execCalls.Load(); got != 2 {
		t.Fatalf("batch-bound confirmation exec calls = %d, want 2", got)
	}
}

func TestExecuteProductionCorrectConfirmationAuditsHashedMetadata(t *testing.T) {
	var execCalls atomic.Int32
	auditor := &confirmationAuditor{}
	service := newConfirmationService(&execCalls, auditor)
	request := productionRequest()
	detail := requireProductionChallenge(t, service, request)
	request.SessionIDs = []string{"staging", "prod"}
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
	}
	batch, err := service.Execute(t.Context(), "owner-a", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 2 || execCalls.Load() != 2 {
		t.Fatalf("confirmed batch results = %+v, exec calls = %d", batch.Results, execCalls.Load())
	}

	records := auditor.snapshot()
	if len(records) != 3 {
		t.Fatalf("audit records = %d, want confirmation plus two executions", len(records))
	}
	var confirmation *productionConfirmationAuditPayload
	var productionExecution *auditPayload
	for _, record := range records {
		if record.ownerID != "owner-a" {
			t.Fatalf("audit owner = %q, want owner-a", record.ownerID)
		}
		encoded, err := json.Marshal(record.input.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "deploy --token") {
			t.Fatalf("production batch audit contains command plaintext: %s", encoded)
		}
		switch record.input.Kind {
		case "exec_batch_production_confirmation":
			var payload productionConfirmationAuditPayload
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			confirmation = &payload
		case "exec_batch":
			if record.input.AssetID != nil && *record.input.AssetID == "asset-prod" {
				var payload auditPayload
				if err := json.Unmarshal(encoded, &payload); err != nil {
					t.Fatal(err)
				}
				productionExecution = &payload
			}
		}
	}
	if confirmation == nil {
		t.Fatal("production confirmation audit not found")
	}
	if confirmation.OwnerID != "owner-a" || confirmation.TargetSetHash != detail.TargetSetHash ||
		confirmation.CommandHash != detail.CommandHash || confirmation.ConfirmedAt != 123456789 ||
		len(confirmation.ProductionTargets) != 1 || confirmation.ProductionTargets[0].Hostname != "api.production.example" {
		t.Fatalf("confirmation audit = %+v", confirmation)
	}
	if productionExecution == nil {
		t.Fatal("production execution audit not found")
	}
	if productionExecution.OwnerID != "owner-a" || productionExecution.Command != "" ||
		productionExecution.TargetSetHash != detail.TargetSetHash || productionExecution.CommandHash != detail.CommandHash ||
		productionExecution.Environment != "production" {
		t.Fatalf("production execution audit = %+v", productionExecution)
	}
}

func TestExecuteMixedProductionBatchRedactsEveryExecutionAudit(t *testing.T) {
	var execCalls atomic.Int32
	auditor := &confirmationAuditor{}
	service := newConfirmationService(&execCalls, auditor)
	request := productionRequest()
	detail := requireProductionChallenge(t, service, request)
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
	}
	if _, err := service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatal(err)
	}

	records := auditor.snapshot()
	if len(records) != 3 {
		t.Fatalf("mixed batch audit records = %d, want confirmation plus two executions", len(records))
	}
	executionRecords := 0
	for _, record := range records {
		encoded, err := json.Marshal(record.input.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "deploy --token") {
			t.Fatalf("mixed production batch audit contains command plaintext: %s", encoded)
		}
		if record.input.Kind != "exec_batch" {
			continue
		}
		executionRecords++
		var payload auditPayload
		if err := json.Unmarshal(encoded, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.OwnerID != "owner-a" || payload.Command != "" ||
			payload.TargetSetHash != detail.TargetSetHash || payload.CommandHash != detail.CommandHash {
			t.Fatalf("mixed production batch execution audit = %+v", payload)
		}
	}
	if executionRecords != 2 {
		t.Fatalf("mixed production batch execution audits = %d, want 2", executionRecords)
	}
}

func TestExecuteNonProductionDoesNotRequireConfirmation(t *testing.T) {
	var execCalls atomic.Int32
	auditor := &confirmationAuditor{}
	service := newConfirmationService(&execCalls, auditor)
	request := productionRequest()
	request.SessionIDs = []string{"staging"}
	if _, err := service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatal(err)
	}
	if execCalls.Load() != 1 {
		t.Fatalf("non-production exec calls = %d", execCalls.Load())
	}
	records := auditor.snapshot()
	if len(records) != 1 || records[0].input.Kind != "exec_batch" {
		t.Fatalf("non-production audit records = %+v", records)
	}
	encoded, err := json.Marshal(records[0].input.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload auditPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Command != request.Command || payload.CommandHash != "" || payload.TargetSetHash != "" {
		t.Fatalf("non-production audit behavior changed: %+v", payload)
	}
}

func TestExecuteProductionConfirmationAuditFailureStopsExecution(t *testing.T) {
	var execCalls atomic.Int32
	auditor := &confirmationAuditor{err: errors.New("audit unavailable")}
	service := newConfirmationService(&execCalls, auditor)
	request := productionRequest()
	detail := requireProductionChallenge(t, service, request)
	request.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
	}
	if _, err := service.Execute(t.Context(), "owner-a", request); err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("confirmation audit failure = %v", err)
	}
	if execCalls.Load() != 0 {
		t.Fatalf("executed %d commands after confirmation audit failure", execCalls.Load())
	}
}
