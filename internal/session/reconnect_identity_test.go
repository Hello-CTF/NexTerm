package session

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestAttachDurableReplacementKnownIdentityRequiresCheckedProvider(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	attachment, _, err := attachDurableReplacement(context.Background(), provider, "known-identity", base.DurableIdentity{Incarnation: "original"}, 12)
	if attachment != nil {
		_ = attachment.Close()
		t.Fatal("unchecked provider returned an attachment for a known identity")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("checked attach capability error = %v; want ErrUnsupported", err)
	}
	if provider.attachCalls != 0 {
		t.Fatalf("known identity fell back to %d unchecked attach calls; want 0", provider.attachCalls)
	}
}

func TestAttachDurableReplacementIdentityMismatchDoesNotFallBack(t *testing.T) {
	provider := &identityMismatchProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	attachment, _, err := attachDurableReplacement(context.Background(), provider, "known-identity", base.DurableIdentity{Incarnation: "original"}, 12)
	if attachment != nil {
		_ = attachment.Close()
		t.Fatal("identity mismatch returned an attachment")
	}
	if !errors.Is(err, durable.ErrIdentity) {
		t.Fatalf("identity mismatch error = %v; want durable.ErrIdentity", err)
	}
	if provider.checkedCalls != 1 || provider.attachCalls != 0 {
		t.Fatalf("identity mismatch calls: checked=%d unchecked=%d; want 1, 0", provider.checkedCalls, provider.attachCalls)
	}
}

func TestAttachDurableReplacementLegacyProviderRequiresNoIdentity(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	if _, err := provider.Create(context.Background(), base.DurableCreateOptions{ID: "legacy-tab", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	attachment, offsetResume, err := attachDurableReplacement(context.Background(), provider, "legacy-tab", base.DurableIdentity{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if attachment == nil {
		t.Fatal("legacy provider without an identity returned no attachment")
	}
	_ = attachment.Close()
	if offsetResume || provider.attachCalls != 1 {
		t.Fatalf("legacy compatibility path: offsetResume=%v attachCalls=%d; want false, 1", offsetResume, provider.attachCalls)
	}
}

type identityMismatchProvider struct {
	*listingDurableProvider
	checkedCalls int
}

func (p *identityMismatchProvider) AttachFromWithIdentity(context.Context, string, base.DurableIdentity, uint64) (base.DurableAttachment, error) {
	p.checkedCalls++
	return nil, durable.ErrIdentity
}

func (p *identityMismatchProvider) AttachWithIdentity(context.Context, string, base.DurableIdentity) (base.DurableAttachment, error) {
	return nil, durable.ErrIdentity
}
