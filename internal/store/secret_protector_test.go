package store

import (
	"context"
	"testing"
)

type stubProtector struct{}

func (stubProtector) EncryptSecret(context.Context, string) (string, error) { return "", nil }
func (stubProtector) DecryptSecret(context.Context, string) (string, error) { return "", nil }

func TestSecretProtectorAttachment(t *testing.T) {
	database := testStore(t)
	if database.SecretProtector() != nil {
		t.Fatal("fresh store must not carry a protector")
	}
	database.SetSecretProtector(stubProtector{})
	if database.SecretProtector() == nil {
		t.Fatal("protector was not attached")
	}
	database.SetSecretProtector(nil)
	if database.SecretProtector() != nil {
		t.Fatal("protector was not detached")
	}
}
