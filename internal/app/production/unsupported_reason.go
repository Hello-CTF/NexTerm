package production

// unsupportedReason shows only its Chinese message to the user while still matching the
// sentinel for errors.Is, so callers can classify it without leaking an English prefix.
type unsupportedReason struct {
	message  string
	sentinel error
}

func (e unsupportedReason) Error() string { return e.message }

func (e unsupportedReason) Unwrap() error { return e.sentinel }
