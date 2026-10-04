package store

import "context"

const SecretEnvelopePrefix = "enc:v1:"

type SecretProtector interface {
	EncryptSecret(ctx context.Context, plaintext string) (string, error)
	DecryptSecret(ctx context.Context, envelope string) (string, error)
}

func (s *Store) SetSecretProtector(protector SecretProtector) {
	s.protectorMu.Lock()
	defer s.protectorMu.Unlock()
	s.protector = protector
}

func (s *Store) SecretProtector() SecretProtector {
	s.protectorMu.RLock()
	defer s.protectorMu.RUnlock()
	return s.protector
}
