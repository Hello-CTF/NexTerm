package update

import (
	"os"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/releasesig"
	"github.com/Hello-CTF/NexTerm/internal/releasesig/releasesigtest"
)

// fixtureKey stands in for the release signing key that release builds embed: the tests run as a release build,
// and linuxFixture signs its checksums with this key.
var fixtureKey = releasesigtest.NewKey(7)

func TestMain(m *testing.M) {
	releasesig.EmbeddedPublicKey = fixtureKey.EncodedPublic
	os.Exit(m.Run())
}

// withoutEmbeddedKey runs the rest of the test as a development build without a trusted key.
func withoutEmbeddedKey(t *testing.T) {
	t.Helper()
	previous := releasesig.EmbeddedPublicKey
	releasesig.EmbeddedPublicKey = ""
	t.Cleanup(func() { releasesig.EmbeddedPublicKey = previous })
}
