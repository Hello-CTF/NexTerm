package account

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func testAccounts(t *testing.T) (*Accounts, *int64) {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := int64(1_700_000_000_000)
	return New(db.DB(), WithNow(func() int64 { return now }), WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key"))), &now
}

func testFileAccounts(t *testing.T) *Accounts {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db.DB(), WithNow(ids.NowMS))
}
