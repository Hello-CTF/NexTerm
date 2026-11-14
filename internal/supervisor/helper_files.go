package supervisor

import (
	"context"
	"fmt"
	"os"

	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
)

// FilesOption starts the helper as a file channel server on stdin and stdout.
const FilesOption = "--files"

func runFileChannel() int {
	if err := remote.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, HelperCommand+":", err)
		return 1
	}
	return 0
}
