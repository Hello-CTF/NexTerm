//go:build unix

package local

import (
	"context"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// replaceFileVersion cannot be implemented honestly on Unix: flock is
// advisory, so a non-cooperating writer can still change the verified inode
// between the final hash and the atomic rename, and no rename primitive
// makes that comparison conditional. Refuse the capability explicitly
// instead of exposing a best-effort window under the conditional contract.
func (f *FileSystem) replaceFileVersion(_ context.Context, path string, _ []byte, _ bool, _ conditional.Expectation) error {
	return fmt.Errorf("conditional replace %s: %w: unix offers no kernel-enforced exclusion or compare-and-rename against non-cooperating writers", path, base.ErrUnsupported)
}
