package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

func TestAIExecCommandsRejectsStaleConfirmationAfterTargetBecomesNonproduction(t *testing.T) {
	for _, test := range []struct {
		name             string
		confirmedSession string
		serviceSession   string
		environment      string
	}{
		{name: "replacement target", confirmedSession: "session-staging", serviceSession: "session-prod", environment: "staging"},
		{name: "production label removed", confirmedSession: "session-prod", serviceSession: "session-prod", environment: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			productionTransport := successfulBatchTransport()
			stagingTransport := successfulBatchTransport()
			targets := map[string]execbatch.Target{
				"session-prod":    batchTarget(productionTransport, "asset-prod", "production"),
				"session-staging": batchTarget(stagingTransport, "asset-staging", "staging"),
			}
			service := batchToolService(targets, &batchAuditRecorder{})
			initial := true
			registry := NewRegistry(Dependencies{
				ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
					if initial {
						initial = false
						return ExecTargetSet{SessionIDs: []string{"session-prod"}, Production: true}, nil
					}
					return ExecTargetSet{SessionIDs: []string{test.confirmedSession}}, nil
				},
				ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
					return service.Execute(ctx, "owner-a", request)
				},
			})
			call := Call{ID: "production", Name: "exec_commands", Args: json.RawMessage(`{"commands":["echo one"],"asset_ids":["asset-prod"]}`)}
			challenged := registry.Execute(context.Background(), "job", Scope{}, call, nil)
			if challenged.ProductionChallenge == nil {
				t.Fatalf("initial challenge = %+v", challenged)
			}
			target := targets[test.serviceSession]
			target.Environment = test.environment
			targets[test.serviceSession] = target
			confirmed := call
			confirmed.ID += ":production"
			confirmed.OriginID = call.ID
			confirmed.ProductionConfirmations = challenged.ProductionChallenge.Confirmations
			result := registry.Execute(context.Background(), "job", Scope{}, confirmed, nil)
			if result.OK || !strings.Contains(result.Text, execbatch.ErrStaleProductionConfirmation.Error()) {
				t.Fatalf("stale confirmation result = %+v", result)
			}
			if productionTransport.calls() != 0 || stagingTransport.calls() != 0 {
				t.Fatalf("stale confirmation executed: production=%v staging=%v", productionTransport.execCalls, stagingTransport.execCalls)
			}
		})
	}
}
