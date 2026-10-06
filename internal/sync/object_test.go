package sync

import (
	"bytes"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func TestObjectSealOpenRoundtrip(t *testing.T) {
	dek := vault.GenerateUserDEK()
	plaintext := []byte(`{"id":"obj-1","name":"生产主机"}`)
	blob, err := sealObject(dek, plaintext, "obj-1", KindAsset)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openObject(dek, blob, "obj-1", KindAsset)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("roundtrip mismatch: %q", opened)
	}
	if bytes.Contains(blob, []byte("生产主机")) {
		t.Fatal("blob leaks plaintext")
	}
}

func TestObjectAADSplicesFail(t *testing.T) {
	dek := vault.GenerateUserDEK()
	blob, err := sealObject(dek, []byte(`{"id":"obj-1"}`), "obj-1", KindAsset)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() ([]byte, error){
		"wrong id": func() ([]byte, error) {
			return openObject(dek, blob, "obj-2", KindAsset)
		},
		"wrong kind": func() ([]byte, error) {
			return openObject(dek, blob, "obj-1", KindGroup)
		},
		"empty id": func() ([]byte, error) {
			return openObject(dek, blob, "", KindAsset)
		},
	}
	for name, attempt := range cases {
		if plaintext, err := attempt(); err == nil {
			t.Errorf("%s: expected failure, got %q", name, plaintext)
		}
	}
}

func TestObjectTamperAndWrongKeyFail(t *testing.T) {
	dek := vault.GenerateUserDEK()
	other := vault.GenerateUserDEK()
	blob, err := sealObject(dek, []byte(`{"secret":"s"}`), "obj-1", KindCredential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openObject(other, blob, "obj-1", KindCredential); err == nil {
		t.Fatal("wrong key opened object")
	}
	tampered := bytes.Clone(blob)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := openObject(dek, tampered, "obj-1", KindCredential); err == nil {
		t.Fatal("tampered blob opened")
	}
	kind, plaintext, err := tryOpenObject(dek, blob, "obj-1")
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindCredential || !bytes.Contains(plaintext, []byte("secret")) {
		t.Fatalf("tryOpen = %q %q", kind, plaintext)
	}
	if _, _, err := tryOpenObject(other, blob, "obj-1"); err == nil {
		t.Fatal("tryOpen with wrong key succeeded")
	}
}

func TestObjectPayloadHashDeterministic(t *testing.T) {
	payload := groupObject{ID: "g-1", Name: "分组", Sort: 3, CreatedAt: 1, UpdatedAt: 2}
	first, err := marshalObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	if objectPayloadHash(first) != objectPayloadHash(second) {
		t.Fatal("payload hash not deterministic")
	}
	if !bytes.Equal(first, second) {
		t.Fatal("payload encoding not deterministic")
	}
}
