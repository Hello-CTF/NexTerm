package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestLinkSetRequiresInitializedVault(t *testing.T) {
	instance := newTestInstance(t, false)
	_, err := instance.service.LinkSet(context.Background(), LinkPatch{
		URL:      testPtr("https://sync.example.com"),
		Username: testPtr("alice"),
		Password: testPtr("super-secret"),
	})
	requireCode(t, err, ipc.CodeVaultNotInit)

	value, found, err := instance.db.SettingGet(context.Background(), settingLink)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("sync link secret must not be stored while vault is uninitialized, got %q", value)
	}
}

func TestLinkSetEncryptsWithInitializedVault(t *testing.T) {
	instance := newTestInstance(t, true)
	link, err := instance.service.LinkSet(context.Background(), LinkPatch{
		URL:      testPtr("https://sync.example.com"),
		Username: testPtr("alice"),
		Password: testPtr("super-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !link.HasPassword || link.URL != "https://sync.example.com" || link.Username != "alice" {
		t.Fatalf("link view = %+v", link)
	}
	value, found, err := instance.db.SettingGet(context.Background(), settingLink)
	if err != nil || !found {
		t.Fatalf("stored link: found=%v err=%v", found, err)
	}
	if !strings.HasPrefix(value, store.SecretEnvelopePrefix) {
		t.Fatalf("stored link must be a %s envelope: %q", store.SecretEnvelopePrefix, value)
	}
	if strings.Contains(value, "super-secret") {
		t.Fatal("stored link leaks plaintext password")
	}

	view, err := instance.service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasPassword || view.Username != "alice" {
		t.Fatalf("link get = %+v", view)
	}
}

func TestLinkSetRequiresUnlockedVault(t *testing.T) {
	instance := newTestInstance(t, true)
	instance.vault.Lock()
	_, err := instance.service.LinkSet(context.Background(), LinkPatch{Password: testPtr("super-secret")})
	requireCode(t, err, ipc.CodeVaultLocked)
}

func TestLinkGetRejectsLegacyPlaintext(t *testing.T) {
	instance := newTestInstance(t, false)
	legacy := `{"url":"https://sync.example.com","username":"alice","password":"super-secret"}`
	if err := instance.db.SettingSet(context.Background(), settingLink, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.LinkGet(context.Background()); err == nil || !strings.Contains(err.Error(), "重新保存") {
		t.Fatalf("legacy plaintext must not be recognized: %v", err)
	}

	instance.service.recordProbe(context.Background(), nil)
	value, found, err := instance.db.SettingGet(context.Background(), settingLink)
	if err != nil || !found {
		t.Fatalf("legacy link after probe: found=%v err=%v", found, err)
	}
	if value != legacy {
		t.Fatal("probe must not rewrite the legacy link")
	}
}

func TestLinkSetOverwritesLegacyPlaintext(t *testing.T) {
	instance := newTestInstance(t, true)
	legacy := `{"url":"https://sync.example.com","username":"alice","password":"super-secret"}`
	if err := instance.db.SettingSet(context.Background(), settingLink, legacy); err != nil {
		t.Fatal(err)
	}
	link, err := instance.service.LinkSet(context.Background(), LinkPatch{
		URL:      testPtr("https://sync.example.com"),
		Username: testPtr("alice"),
		Password: testPtr("super-secret"),
	})
	if err != nil {
		t.Fatalf("re-saving over a legacy plaintext link must succeed: %v", err)
	}
	if !link.HasPassword || link.Username != "alice" {
		t.Fatalf("link view = %+v", link)
	}
	value, found, err := instance.db.SettingGet(context.Background(), settingLink)
	if err != nil || !found {
		t.Fatalf("stored link: found=%v err=%v", found, err)
	}
	if !strings.HasPrefix(value, store.SecretEnvelopePrefix) {
		t.Fatalf("re-saved link must be a %s envelope: %q", store.SecretEnvelopePrefix, value)
	}
	view, err := instance.service.LinkGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasPassword || view.Username != "alice" {
		t.Fatalf("link get after re-save = %+v", view)
	}
}
