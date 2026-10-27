package privacy

// UserError contains only fixed, safe remediation text suitable for a UI.
// Filesystem, database and native-library diagnostics must not use this type.
type UserError struct{ Message string }

func (e *UserError) Error() string   { return e.Message }
func userError(message string) error { return &UserError{Message: message} }
