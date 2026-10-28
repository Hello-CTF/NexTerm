package vault

import (
	"sync/atomic"
	"testing"
)

// Argon2Params are the argon2id costs for keys derived from a password.
type Argon2Params struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

var productionArgon2 = Argon2Params{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

var testArgon2 atomic.Pointer[Argon2Params]

// PasswordArgon2 returns the costs new password-derived keys and hashes use.
// Outside a test binary that opted into UseMinimumArgon2ForTesting this is
// always the production cost.
func PasswordArgon2() Argon2Params {
	if params := testArgon2.Load(); params != nil {
		return *params
	}
	return productionArgon2
}

// UseMinimumArgon2ForTesting lowers the argon2id cost of new password hashes
// and wrapped keys to the smallest values the verifiers still accept, so test
// binaries that create accounts or unlock vaults in most tests do not spend
// their time in key derivation. Call it from TestMain before any test runs;
// it panics outside a test binary so production code can never select it.
// Stored hashes and envelopes record their own parameters, so verification
// stays the production code path.
func UseMinimumArgon2ForTesting() {
	if !testing.Testing() {
		panic("vault: UseMinimumArgon2ForTesting called outside a test binary")
	}
	params := Argon2Params{Time: 1, MemoryKiB: minKDFMemoryKiB, Threads: 1}
	testArgon2.Store(&params)
}
