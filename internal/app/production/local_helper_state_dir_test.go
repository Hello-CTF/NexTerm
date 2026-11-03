package production

import (
	"path/filepath"
	"testing"
)

func TestLocalHelperStateDirsShareOnlyWhenAskedTo(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	own := filepath.Join(dataDir, "durable", "supervisor")

	stateDir, legacy := localHelperStateDirs(ProductionConfig{DataDir: dataDir})
	if stateDir != own || legacy != "" {
		t.Fatalf("isolated: %q, %q", stateDir, legacy)
	}

	shared := filepath.Join(t.TempDir(), "shared")
	stateDir, legacy = localHelperStateDirs(ProductionConfig{DataDir: dataDir, SharedHelperStateDir: shared})
	if stateDir != shared || legacy != own {
		t.Fatalf("shared: %q, %q", stateDir, legacy)
	}
}
