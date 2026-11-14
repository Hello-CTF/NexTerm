package purpose

import (
	"context"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
)

// FileServer serves device filesystem operations, one operation per bridge,
// as the agent user.
type FileServer struct {
	bridge BridgeDialer
	server *remote.Server
}

func NewFileServer(bridge BridgeDialer) *FileServer {
	return &FileServer{bridge: bridge, server: remote.NewServer()}
}

// Serve claims the bridge and serves its operation; it matches the agent
// runtime's purpose handler signature.
func (s *FileServer) Serve(ctx context.Context, bridgeID string) error {
	conn, err := s.bridge.DialBridge(ctx, bridgeID, deviceproto.PurposeFile)
	if err != nil {
		return err
	}
	return s.ServeConn(ctx, conn)
}

// ServeMux claims a file-mux bridge and serves an operation per stream on it
// until the client closes it.
func (s *FileServer) ServeMux(ctx context.Context, bridgeID string) error {
	conn, err := s.bridge.DialBridge(ctx, bridgeID, deviceproto.PurposeFileMux)
	if err != nil {
		return err
	}
	return remote.ServeMux(ctx, conn)
}

// ServeConn serves the single operation requested on conn and closes conn.
func (s *FileServer) ServeConn(ctx context.Context, conn net.Conn) error {
	return s.server.ServeConn(ctx, conn)
}
