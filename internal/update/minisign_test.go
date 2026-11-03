package update

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/releasesig"
	"github.com/Hello-CTF/NexTerm/internal/releasesig/releasesigtest"
)

func TestMinisignTrustConfiguration(t *testing.T) {
	legacy := &Manager{}
	if _, required, err := legacy.minisignTrust(); err != nil || required {
		t.Fatalf("legacy policy = required %v, err %v", required, err)
	}
	requiredWithoutKey := &Manager{RequireSignedChecksums: true}
	if _, _, err := requiredWithoutKey.minisignTrust(); err == nil {
		t.Fatal("required signatures without a trusted key must fail")
	}
	invalidKey := &Manager{MinisignPublicKey: "not-a-key"}
	if _, _, err := invalidKey.minisignTrust(); err == nil {
		t.Fatal("an invalid configured key must fail")
	}

	key := releasesigtest.NewKey(42)
	previousDefault := releasesig.EmbeddedPublicKey
	releasesig.EmbeddedPublicKey = key.EncodedPublic
	t.Cleanup(func() { releasesig.EmbeddedPublicKey = previousDefault })
	trust, required, err := (&Manager{}).minisignTrust()
	if err != nil || !required || trust.KeyID() != key.KeyID {
		t.Fatalf("build-time key policy = key ID %x, required %v, err %v", trust.KeyID(), required, err)
	}
}
