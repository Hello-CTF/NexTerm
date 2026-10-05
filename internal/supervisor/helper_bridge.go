package supervisor

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
)

func runBridge(ctx context.Context, config HelperConfig) error {
	endpoint, err := helperEndpoint(config.StateDir)
	if err != nil {
		return fmt.Errorf("resolve supervisor helper endpoint: %w", err)
	}
	conn, err := dialSocket(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("%w: dial supervisor helper: %v", ErrUnavailable, err)
	}
	return bridgeIO(os.Stdin, os.Stdout, conn)
}

func bridgeIO(stdin io.Reader, stdout io.Writer, conn net.Conn) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdout, conn)
		if file, ok := stdin.(*os.File); ok {
			_ = file.Close()
		}
	}()
	go func() {
		_, _ = io.Copy(conn, stdin)
		_ = conn.Close()
	}()
	wg.Wait()
	return nil
}
