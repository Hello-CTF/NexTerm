package production

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// writeFileExpected replaces a file only while it still is the version the
// caller read. Backends without conditional writes, and device agents that
// predate them, are checked right before the write instead, which narrows
// the window but cannot close it.
func writeFileExpected(ctx context.Context, filesystem base.FileSystem, input fsRequest, data []byte) error {
	if input.ExpectedSize == nil || input.ExpectedSHA256 == "" {
		return ipc.BadParam(errors.New("条件写入需要同时提供 expectedSize 和 expectedSha256"))
	}
	expected, err := conditional.Existing(*input.ExpectedSize, input.ExpectedSHA256)
	if err != nil {
		return ipc.BadParam(err)
	}
	if writer, ok := filesystem.(conditional.Writer); ok {
		err := writer.WriteFileVersion(ctx, input.Path, data, input.Backup, expected)
		if !errors.Is(err, base.ErrUnsupported) {
			return err
		}
	}
	size, err := filesystem.Size(ctx, input.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", input.Path, conditional.ErrVersionMismatch)
	}
	if err != nil {
		return err
	}
	if size != expected.Size {
		return conditional.ErrVersionMismatch
	}
	sum, err := filesystem.Checksum(ctx, input.Path, "sha256")
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, expected.SHA256) {
		return conditional.ErrVersionMismatch
	}
	return filesystem.WriteFile(ctx, input.Path, data, input.Backup)
}
