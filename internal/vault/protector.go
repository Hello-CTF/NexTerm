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
