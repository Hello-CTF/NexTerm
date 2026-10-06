package production

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func requireProductionNull(t *testing.T, response ipc.Response) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	if string(response.Data) != "null" {
		t.Fatalf("dispatch data = %s, want null", response.Data)
	}
}
