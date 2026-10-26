package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/version"
)

// RuntimeMetadata identifies the helper code and protocol behind a running
// daemon. Version differences alone do not imply protocol incompatibility; the
// protocol number alone determines what the helper implements.
type RuntimeMetadata struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Protocol int    `json:"protocol"`
	SHA256   string `json:"sha256"`
}

// CurrentRuntimeMetadata reads the actual executable being run, rather than a
// deployment candidate path. The daemon snapshots this once during startup.
func CurrentRuntimeMetadata() (RuntimeMetadata, error) {
	executable, err := os.Executable()
	if err != nil {
		return RuntimeMetadata{}, fmt.Errorf("resolve running helper executable: %w", err)
	}
	return runtimeMetadataForExecutable(executable)
}

func runtimeMetadataForExecutable(executable string) (RuntimeMetadata, error) {
	file, err := os.Open(executable)
	if err != nil {
		return RuntimeMetadata{}, fmt.Errorf("open running helper executable: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return RuntimeMetadata{}, fmt.Errorf("hash running helper executable: %w", err)
	}
	return RuntimeMetadata{
		Version:  version.Version,
		Commit:   version.Commit,
		Protocol: ProtocolVersion,
		SHA256:   hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

var cachedRuntimeMetadata = sync.OnceValues(CurrentRuntimeMetadata)

func provenRuntimeMetadata() *RuntimeMetadata {
	metadata, err := cachedRuntimeMetadata()
	if err != nil {
		return nil
	}
	return &metadata
}

func validateRuntimeMetadata(metadata RuntimeMetadata, protocol int) error {
	if metadata.Version == "" || metadata.Commit == "" {
		return fmt.Errorf("%w: running helper release or commit is empty", ErrProtocol)
	}
	if metadata.Protocol != protocol {
		return fmt.Errorf("%w: running helper metadata protocol %d does not match hello protocol %d", ErrProtocol, metadata.Protocol, protocol)
	}
	if len(metadata.SHA256) != sha256.Size*2 || metadata.SHA256 != strings.ToLower(metadata.SHA256) {
		return fmt.Errorf("%w: running helper SHA256 is not 64-character lowercase hex", ErrProtocol)
	}
	if _, err := hex.DecodeString(metadata.SHA256); err != nil {
		return fmt.Errorf("%w: running helper SHA256 is not hex", ErrProtocol)
	}
	return nil
}

// RuntimeMetadata returns metadata supplied by the running daemon during the
// hello. known is false only when the daemon could not hash its own executable
// and so cannot prove its identity; callers must not substitute metadata from
// a candidate.
func (c *Client) RuntimeMetadata(ctx context.Context) (metadata RuntimeMetadata, known bool, err error) {
	conn, err := c.dialConn(ctx)
	if err != nil {
		return RuntimeMetadata{}, false, err
	}
	defer func() { _ = conn.Close() }()
	if conn.runtimeMetadata == nil {
		return RuntimeMetadata{}, false, nil
	}
	return *conn.runtimeMetadata, true, nil
}
