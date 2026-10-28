package vault

import (
	"bytes"
	"encoding/json"
	"testing"
)

// This package never opts into the minimum test cost, so new user DEK
// envelopes here must record and use the production argon2id parameters.
func TestUserDEKEnvelopeUsesProductionArgon2(t *testing.T) {
	if got := PasswordArgon2(); got != productionArgon2 {
		t.Fatalf("default argon2 params = %+v, want %+v", got, productionArgon2)
	}
	dek := bytes.Repeat([]byte{7}, DEKLength)
	envelope, salt, params, err := WrapUserDEK("synthetic-password", dek)
	if err != nil {
		t.Fatal(err)
	}
	var recorded UserDEKKDFParams
	if err := json.Unmarshal([]byte(params), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != (UserDEKKDFParams{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}) {
		t.Fatalf("recorded params = %+v, want t=3 m=65536 p=4", recorded)
	}
	unwrapped, err := UnwrapUserDEK("synthetic-password", envelope, salt, params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unwrapped, dek) {
		t.Fatal("unwrapped DEK differs from the wrapped one")
	}
}

func TestMinimumArgon2StaysWithinVerifierBounds(t *testing.T) {
	previous := testArgon2.Load()
	t.Cleanup(func() { testArgon2.Store(previous) })
	UseMinimumArgon2ForTesting()
	params := PasswordArgon2()
	encoded, err := json.Marshal(UserDEKKDFParams{Time: params.Time, MemoryKiB: params.MemoryKiB, Threads: uint32(params.Threads)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseUserDEKKDFParams(string(encoded)); err != nil {
		t.Fatalf("minimum test params %+v are rejected by the verifier: %v", params, err)
	}
	dek := bytes.Repeat([]byte{9}, DEKLength)
	envelope, salt, recorded, err := WrapUserDEK("synthetic-password", dek)
	if err != nil {
		t.Fatal(err)
	}
	if recorded != string(encoded) {
		t.Fatalf("recorded params = %s, want %s", recorded, encoded)
	}
	if _, err := UnwrapUserDEK("synthetic-password", envelope, salt, recorded); err != nil {
		t.Fatal(err)
	}
}
