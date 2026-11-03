package vault

type Protector interface {
	Protect(data []byte) ([]byte, error)
	Unprotect(envelope []byte) ([]byte, error)
}

type systemProtector struct{}

func (systemProtector) Protect(data []byte) ([]byte, error) {
	return systemProtect(data)
}

func (systemProtector) Unprotect(envelope []byte) ([]byte, error) {
	return systemUnprotect(envelope)
}

// legacyEnvelopeDetector is optionally implemented by a Protector whose envelope format
// has been superseded; the vault rewrites such envelopes after a verified unlock.
type legacyEnvelopeDetector interface {
	IsLegacyEnvelope(envelope []byte) bool
}

func (systemProtector) IsLegacyEnvelope(envelope []byte) bool {
	return systemEnvelopeIsLegacy(envelope)
}
