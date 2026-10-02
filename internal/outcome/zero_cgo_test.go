package outcome

import (
	"context"
	"fmt"
	"testing"
)

func TestZeroCGOCommandFileAndDatabaseLedger(t *testing.T) {
	store := newMemoryStore()
	ledger := newTestLedger(t, store, &memoryAuditor{})
	for _, kind := range []Kind{KindCommand, KindFile, KindDatabase} {
		t.Run(string(kind), func(t *testing.T) {
			request := testRequest(fmt.Sprintf("zero-cgo-%s", kind))
			request.Kind = kind
			record, err := ledger.Execute(context.Background(), request, func(context.Context) (Completion, error) {
				if kind == KindCommand {
					return Completion{ExitCode: intPointer(0)}, nil
				}
				return Completion{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if record.Kind != kind || record.Outcome != OutcomeAccepted || record.Audit.State != AuditPersisted {
				t.Fatalf("record = %+v", record)
			}
		})
	}
}
