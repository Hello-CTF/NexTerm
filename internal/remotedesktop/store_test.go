package remotedesktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type stubDecryptor struct{ plaintext string }

func (d stubDecryptor) DecryptCredentialString(context.Context, store.CredentialRow) (string, error) {
	return d.plaintext, nil
}

func TestStoreBackedFlow(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	owner := ipc.WithUserID(ctx, "owner-a")
	other := ipc.WithUserID(ctx, "owner-b")
	if _, err := database.CredentialPut(owner, store.CredentialInput{ID: "c1", Name: "rdp", Kind: "password", Nonce: []byte{1}, Blob: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	host, user, cred := "win.internal", "Administrator", "c1"
	row, err := database.AssetCreate(owner, store.AssetInput{Kind: KindRDP, Name: "win", Host: &host, Username: &user, CredID: &cred, OptionsJSON: `{"domain":"CORP"}`})
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(NewStoreResolver(database, stubDecryptor{"from-vault"}),
		WithCertificateStore(NewStoreCertificates(database)), WithAuditor(NewStoreAuditor(database)))
	t.Cleanup(svc.Close)

	prepared, err := svc.Prepare(owner, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Port != DefaultRDPPort || prepared.Domain != "CORP" || prepared.Username != user {
		t.Fatalf("prepared = %+v", prepared)
	}
	if _, err := svc.Prepare(other, row.ID); err == nil {
		t.Fatal("another owner prepared the asset")
	}
	secret, err := svc.ReleaseSecret(owner, prepared.Ticket)
	if err != nil || secret.Password != "from-vault" {
		t.Fatalf("release = %+v, %v", secret, err)
	}
	audits, err := database.AuditQuery(owner, store.AuditQuery{})
	if err != nil || len(audits) != 1 || audits[0].Kind != AuditSecretRelease || audits[0].Source != "remotedesktop" {
		t.Fatalf("audit rows = %+v, %v", audits, err)
	}
	if raw, _ := json.Marshal(audits[0]); containsText(raw, "from-vault") {
		t.Fatal("secret written to the audit log")
	}

	// Certificate pins live in the known-hosts ledger under their own key type, owner scoped.
	certs := NewStoreCertificates(database)
	if _, found, _ := certs.Get(owner, host, 3389, certKeyTypeRDP); found {
		t.Fatal("pin exists before first use")
	}
	if err := certs.Accept(owner, host, 3389, certKeyTypeRDP, "SHA256:aaa"); err != nil {
		t.Fatal(err)
	}
	if fp, found, _ := certs.Get(owner, host, 3389, certKeyTypeRDP); !found || fp != "SHA256:aaa" {
		t.Fatalf("pin = %q %v", fp, found)
	}
	if _, found, _ := certs.Get(other, host, 3389, certKeyTypeRDP); found {
		t.Fatal("pin visible to another owner")
	}
	if _, found, _ := certs.Get(owner, host, 3389, certKeyTypeVNC); found {
		t.Fatal("rdp pin visible as a vnc pin")
	}
	if err := certs.Accept(owner, host, 3389, certKeyTypeRDP, "SHA256:bbb"); err != nil {
		t.Fatal(err)
	}
	if fp, _, _ := certs.Get(owner, host, 3389, certKeyTypeRDP); fp != "SHA256:bbb" {
		t.Fatalf("re-confirmed pin = %q", fp)
	}
	rows, _ := database.KnownHostList(owner)
	if len(rows) != 1 || rows[0].Key != "" {
		t.Fatalf("known host rows = %+v", rows)
	}
}

func containsText(haystack []byte, needle string) bool {
	return len(needle) > 0 && strings.Contains(string(haystack), needle)
}
