package guard

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func connectAllowAll() ConnectAuthorization {
	return ConnectAuthorization{
		OptIn: true, OwnerMatch: true, HostKey: HostKeyKnown, VaultReady: true, Environment: "staging",
	}
}

func TestAuthorizeConnectRequiresEveryGate(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	ctx := context.Background()
	if err := AuthorizeConnect(ctx, settings, connectAllowAll()); err != nil {
		t.Fatalf("fully authorized connect rejected: %v", err)
	}
	if err := AuthorizeConnect(ctx, settings, ConnectAuthorization{}); !errors.Is(err, ErrConnectOptInRequired) {
		t.Fatalf("zero-value connect = %v, want opt-in required", err)
	}

	optOut := connectAllowAll()
	optOut.OptIn = false
	if err := AuthorizeConnect(ctx, settings, optOut); !errors.Is(err, ErrConnectOptInRequired) {
		t.Fatalf("opt-out connect = %v", err)
	}

	crossOwner := connectAllowAll()
	crossOwner.OwnerMatch = false
	if err := AuthorizeConnect(ctx, settings, crossOwner); !errors.Is(err, ErrConnectOwnerMismatch) {
		t.Fatalf("cross-owner connect = %v", err)
	}

	unknownKey := connectAllowAll()
	unknownKey.HostKey = HostKeyUnknown
	if err := AuthorizeConnect(ctx, settings, unknownKey); !errors.Is(err, ErrConnectHostKeyUnknown) {
		t.Fatalf("unknown host key connect = %v", err)
	}
	unknownKey.ProductionConfirmed = true
	if err := AuthorizeConnect(ctx, settings, unknownKey); !errors.Is(err, ErrConnectHostKeyUnknown) {
		t.Fatalf("unknown host key auto-accepted with production confirmation: %v", err)
	}

	changedKey := connectAllowAll()
	changedKey.HostKey = HostKeyChanged
	if err := AuthorizeConnect(ctx, settings, changedKey); !errors.Is(err, ErrConnectHostKeyChanged) {
		t.Fatalf("changed host key connect = %v", err)
	}

	missingKey := connectAllowAll()
	missingKey.HostKey = ""
	if err := AuthorizeConnect(ctx, settings, missingKey); !errors.Is(err, ErrConnectHostKeyUnknown) {
		t.Fatalf("missing host key verdict = %v", err)
	}

	vaultLocked := connectAllowAll()
	vaultLocked.VaultReady = false
	if err := AuthorizeConnect(ctx, settings, vaultLocked); !errors.Is(err, ErrConnectVaultUnavailable) {
		t.Fatalf("vault-unavailable connect = %v", err)
	}
}

func TestAuthorizeConnectProductionNeedsConfirmation(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	ctx := ipc.WithUserID(context.Background(), "user-a")
	production := connectAllowAll()
	production.Environment = "production"
	if err := AuthorizeConnect(ctx, settings, production); !errors.Is(err, ErrConnectProductionConfirmationRequired) {
		t.Fatalf("production connect without confirmation = %v", err)
	}
	production.ProductionConfirmed = true
	if err := AuthorizeConnect(ctx, settings, production); err != nil {
		t.Fatalf("confirmed production connect rejected: %v", err)
	}

	if err := SetProductionEnvironments(ctx, settings, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	custom := connectAllowAll()
	custom.Environment = "live"
	if err := AuthorizeConnect(ctx, settings, custom); !errors.Is(err, ErrConnectProductionConfirmationRequired) {
		t.Fatalf("configured production connect without confirmation = %v", err)
	}
	custom.ProductionConfirmed = true
	if err := AuthorizeConnect(ctx, settings, custom); err != nil {
		t.Fatalf("confirmed configured-production connect rejected: %v", err)
	}
	other := ipc.WithUserID(context.Background(), "user-b")
	if err := AuthorizeConnect(other, settings, custom); err != nil {
		t.Fatalf("user B connect to staging-classified asset rejected: %v", err)
	}
}
