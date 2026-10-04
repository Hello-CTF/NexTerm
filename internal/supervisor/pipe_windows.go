//go:build windows

package supervisor

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const pipeBufferSize = 64 * 1024

func pipeEndpointName(stateDir string) (string, error) {
	identity, err := currentUserIdentity()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(identity + "\x00" + stateDir))
	return fmt.Sprintf(`\\.\pipe\nexterm-supervisor-v%d-%s-%x`, ProtocolVersion, identity, sum[:8]), nil
}

func currentUserSIDString() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func currentUserPipeSDDL() (string, error) {
	sid, err := currentUserSIDString()
	if err != nil {
		return "", err
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;" + sid + ")", nil
}

func listenPipe(path string) (net.Listener, error) {
	sddl, err := currentUserPipeSDDL()
	if err != nil {
		return nil, fmt.Errorf("supervisor pipe security: %w", err)
	}
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    pipeBufferSize,
		OutputBufferSize:   pipeBufferSize,
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("%w: supervisor pipe %s is already serving", ErrAlreadyExists, path)
		}
		return nil, err
	}
	return listener, nil
}

func dialPipe(ctx context.Context, path string) (net.Conn, error) {
	if !strings.HasPrefix(path, pipePrefix) {
		return nil, fmt.Errorf("%w: local IPC transport on %s", ErrUnsupported, path)
	}
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
