package sync

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestTokenLifecycleIndependentClients(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)

	issuedA, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "desktop-a"})
	if err != nil {
		t.Fatal(err)
	}
	issuedB, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "desktop-b", Purpose: PurposeSync})
	if err != nil {
		t.Fatal(err)
	}
	if issuedA.Secret == issuedB.Secret || issuedA.Token.ID == issuedB.Token.ID {
		t.Fatal("tokens must be unique per client")
	}
	if issuedA.Token.Purpose != PurposeSync || issuedB.Token.Purpose != PurposeSync {
		t.Fatalf("default purpose mismatch: %+v %+v", issuedA.Token, issuedB.Token)
	}
	for _, secret := range []string{issuedA.Secret, issuedB.Secret} {
		if valid, err := instance.service.VerifyToken(ctx, secret); err != nil || !valid {
			t.Fatalf("issued token %q valid=%v err=%v", secret, valid, err)
		}
	}

	tokens, err := instance.service.TokenList(ctx)
	if err != nil || len(tokens) != 2 {
		t.Fatalf("token list=%+v err=%v", tokens, err)
	}
	encoded, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), issuedA.Secret) || strings.Contains(string(encoded), issuedB.Secret) {
		t.Fatal("token list leaked secret material")
	}

	rotatedA, err := instance.service.TokenRotate(ctx, issuedA.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedA == issuedA.Secret {
		t.Fatal("rotation kept the old secret")
	}
	if valid, _ := instance.service.VerifyToken(ctx, issuedA.Secret); valid {
		t.Fatal("rotated-out token must fail immediately (no grace period)")
	}
	if valid, _ := instance.service.VerifyToken(ctx, rotatedA); !valid {
		t.Fatal("rotated token rejected")
	}
	if valid, _ := instance.service.VerifyToken(ctx, issuedB.Secret); !valid {
		t.Fatal("independent rotation must not disturb other clients")
	}

	if err := instance.service.TokenRevoke(ctx, issuedB.Token.ID); err != nil {
		t.Fatal(err)
	}
	if valid, _ := instance.service.VerifyToken(ctx, issuedB.Secret); valid {
		t.Fatal("revoked token still verifies")
	}
	if valid, _ := instance.service.VerifyToken(ctx, rotatedA); !valid {
		t.Fatal("revoking one token must not disturb others")
	}
	if err := instance.service.TokenRevoke(ctx, issuedB.Token.ID); err != nil {
		t.Fatalf("revoke must be idempotent: %v", err)
	}
	if _, err := instance.service.TokenRotate(ctx, issuedB.Token.ID); err == nil {
		t.Fatal("rotating a revoked token must fail")
	}
	if _, err := instance.service.TokenRotate(ctx, "missing"); err == nil {
		t.Fatal("rotating an unknown token must fail")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	if err := instance.service.TokenRevoke(ctx, "missing"); err == nil {
		t.Fatal("revoking an unknown token must fail")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}

	tokens, _ = instance.service.TokenList(ctx)
	byID := map[string]Token{}
	for _, token := range tokens {
		byID[token.ID] = token
	}
	revoked := byID[issuedB.Token.ID]
	if revoked.RevokedAt == nil {
		t.Fatal("revoked token lost its revocation marker")
	}
	if byID[issuedA.Token.ID].LastUsedAt == nil {
		t.Fatal("successful verification did not record last_used_at")
	}
}

func TestTokenPurposeAndExpiryEnforced(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)

	metrics, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "metrics-agent", Purpose: "metrics"})
	if err != nil {
		t.Fatal(err)
	}
	if valid, _ := instance.service.VerifyToken(ctx, metrics.Secret); valid {
		t.Fatal("token of a foreign purpose must not pass sync verification")
	}
	if valid, err := instance.service.verifyToken(ctx, metrics.Secret, "metrics"); err != nil || !valid {
		t.Fatalf("purpose-scoped verification valid=%v err=%v", valid, err)
	}

	short, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "short-lived", TTLMs: 200})
	if err != nil {
		t.Fatal(err)
	}
	if short.Token.ExpiresAt == 0 {
		t.Fatal("ttl was not recorded")
	}
	if valid, _ := instance.service.VerifyToken(ctx, short.Secret); !valid {
		t.Fatal("token expired before its TTL")
	}
	time.Sleep(300 * time.Millisecond)
	if valid, _ := instance.service.VerifyToken(ctx, short.Secret); valid {
		t.Fatal("expired token still verifies")
	}
	if _, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "bad ttl", TTLMs: -1}); err == nil {
		t.Fatal("negative ttl accepted")
	}
	if _, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: " padded "}); err == nil {
		t.Fatal("padded client id accepted")
	}
	if _, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "x", Purpose: "Bad Purpose"}); err == nil {
		t.Fatal("invalid purpose accepted")
	}
}

func TestLegacySharedTokenMigratesToAdminToken(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	const legacy = "legacy-shared-token-value"
	if err := instance.db.SettingSet(ctx, settingToken, legacy); err != nil {
		t.Fatal(err)
	}

	if valid, err := instance.service.VerifyToken(ctx, legacy); err != nil || !valid {
		t.Fatalf("legacy token must keep verifying after migration: valid=%v err=%v", valid, err)
	}
	if valid, _ := instance.service.VerifyToken(ctx, "wrong"); valid {
		t.Fatal("wrong token accepted")
	}
	tokens, err := instance.service.TokenList(ctx)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("migrated tokens=%+v err=%v", tokens, err)
	}
	migrated := tokens[0]
	if migrated.ID != adminTokenID || migrated.ClientID != adminClientID || migrated.Purpose != PurposeSync {
		t.Fatalf("legacy token did not become the administrator token: %+v", migrated)
	}
	plaintext, err := instance.service.Token(ctx)
	if err != nil || plaintext != legacy {
		t.Fatalf("migration must preserve the existing token value: %q err=%v", plaintext, err)
	}
	backup, found, err := instance.db.SettingGet(ctx, settingTokenBackup)
	if err != nil || !found || backup != legacy {
		t.Fatalf("plaintext backup=%q found=%v err=%v", backup, found, err)
	}

	rotated, err := instance.service.RotateToken(ctx)
	if err != nil || rotated == legacy {
		t.Fatalf("rotation=%q err=%v", rotated, err)
	}
	if valid, _ := instance.service.VerifyToken(ctx, legacy); valid {
		t.Fatal("legacy token survived rotation")
	}
	if valid, _ := instance.service.VerifyToken(ctx, rotated); !valid {
		t.Fatal("rotated admin token rejected")
	}
	backup, _, _ = instance.db.SettingGet(ctx, settingTokenBackup)
	if backup != rotated {
		t.Fatalf("backup did not follow rotation: %q", backup)
	}
}

func TestTokenAuditRecordsClientIDOnly(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	issued, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "audit-client"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.TokenRotate(ctx, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	if err := instance.service.TokenRevoke(ctx, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := instance.db.AuditQuery(ctx, store.AuditQuery{Source: testPtr(auditSourceSync), Kind: testPtr(auditKindToken), Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("audit rows=%d err=%v", len(rows), err)
	}
	actions := map[string]bool{}
	for _, row := range rows {
		payload := string(row.PayloadJSON)
		if strings.Contains(payload, issued.Secret) {
			t.Fatalf("audit payload leaked token material: %s", payload)
		}
		if !strings.Contains(payload, `"clientId":"audit-client"`) {
			t.Fatalf("audit payload lacks client id: %s", payload)
		}
		for _, action := range []string{"issue", "rotate", "revoke"} {
			if strings.Contains(payload, `"action":"`+action+`"`) {
				actions[action] = true
			}
		}
	}
	if len(actions) != 3 {
		t.Fatalf("audit actions incomplete: %+v", actions)
	}
}

func TestMigrationCreatesSyncTokensTable(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var name string
	if err := db.DB().QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='sync_tokens'").Scan(&name); err != nil {
		t.Fatalf("sync_tokens table missing: %v", err)
	}
	var unique int
	if err := db.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_sync_tokens_hash' AND sql LIKE '%UNIQUE%'").Scan(&unique); err != nil || unique != 1 {
		t.Fatalf("unique hash index missing: count=%d err=%v", unique, err)
	}
}

func TestVerifyTokenConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	issued, err := instance.service.TokenIssue(ctx, TokenIssueRequest{ClientID: "race-client"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan string, 64)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if valid, err := instance.service.VerifyToken(ctx, issued.Secret); err != nil || !valid {
					failures <- "valid token rejected"
					return
				}
				if valid, _ := instance.service.VerifyToken(ctx, "wrong"); valid {
					failures <- "wrong token accepted"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal(failure)
	}
}
