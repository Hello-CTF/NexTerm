package account

import (
	"context"
	"testing"

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
	return New(db.DB(), WithNow(func() int64 { return now })), &now
}
