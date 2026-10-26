package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
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
	if _, err := instance.service.LinkGet(context.Background()); err == nil || !strings.Contains(err.Error(), "解密失败") {
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

// A pre-1.3.0 plaintext link is never read or merged: a patch that does not re-enter the credentials is rejected
// visibly and leaves the stored value untouched, instead of silently discarding the old fields.
func TestLinkSetRejectsPartialPatchOverLegacyPlaintext(t *testing.T) {
	instance := newTestInstance(t, true)
	legacy := `{"url":"https://sync.example.com","username":"alice","password":"super-secret"}`
	if err := instance.db.SettingSet(context.Background(), settingLink, legacy); err != nil {
		t.Fatal(err)
	}
	_, err := instance.service.LinkSet(context.Background(), LinkPatch{URL: testPtr("https://sync.example.com"), Username: testPtr("alice")})
	requireCode(t, err, ipc.CodeDecrypt)
	if !strings.Contains(err.Error(), "1.3.0") {
		t.Fatalf("legacy link rejection does not explain the format change: %v", err)
	}
	if _, err := instance.service.LinkGet(context.Background()); err == nil || !strings.Contains(err.Error(), "1.3.0") {
		t.Fatalf("legacy link read error = %v", err)
	}
	value, found, err := instance.db.SettingGet(context.Background(), settingLink)
	if err != nil || !found || value != legacy {
		t.Fatalf("rejected patch changed the legacy link: %q %v %v", value, found, err)
	}
}
