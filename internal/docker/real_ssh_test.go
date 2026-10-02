package docker

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestRealSSHStreamlocalAndDialStdio(t *testing.T) {
	if os.Getenv("NEXTERM_DOCKER_SSH_E2E") != "1" {
		t.Skip("set NEXTERM_DOCKER_SSH_E2E=1 and SSH connection variables to run SSH Docker integration tests")
	}
	host := os.Getenv("NEXTERM_DOCKER_SSH_HOST")
	user := os.Getenv("NEXTERM_DOCKER_SSH_USER")
	password := os.Getenv("NEXTERM_DOCKER_SSH_PASSWORD")
	knownHosts := os.Getenv("NEXTERM_DOCKER_SSH_KNOWN_HOSTS")
	if host == "" || user == "" || knownHosts == "" {
		t.Skip("NEXTERM_DOCKER_SSH_HOST, NEXTERM_DOCKER_SSH_USER and NEXTERM_DOCKER_SSH_KNOWN_HOSTS are required")
	}
	hostKeyCallback, err := knownhosts.New(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := ssh.Dial("tcp", host, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	socket := os.Getenv("NEXTERM_DOCKER_SSH_SOCKET")
	cli, err := NewSSHStreamlocalClient(socket, connection)
	if err != nil {
		t.Fatal(err)
	}
	backend := NewMobyBackend(cli)
	if _, err := backend.ListContainers(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	_ = cli.Close()
	if os.Getenv("NEXTERM_DOCKER_SSH_DIAL_STDIO") != "1" {
		t.Log("set NEXTERM_DOCKER_SSH_DIAL_STDIO=1 to also test docker system dial-stdio")
		return
	}
	stdioCLI, err := NewSSHDialStdioClient(StdioOpenerFunc(func(ctx context.Context) (io.ReadWriteCloser, error) {
		return openTestSSHStdio(ctx, connection)
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer stdioCLI.Close()
	stdioBackend := NewMobyBackend(stdioCLI)
	if _, err := stdioBackend.ListContainers(t.Context(), true); err != nil {
		t.Fatal(err)
	}
}

func openTestSSHStdio(ctx context.Context, connection *ssh.Client) (io.ReadWriteCloser, error) {
	session, err := connection.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	session.Stderr = io.Discard
	if err := session.Start("docker system dial-stdio"); err != nil {
		_ = session.Close()
		return nil, err
	}
	channel := &testSSHStdio{reader: stdout, writer: stdin, session: session}
	stop := context.AfterFunc(ctx, func() { _ = channel.Close() })
	channel.stop = stop
	return channel, nil
}

type testSSHStdio struct {
	reader  io.Reader
	writer  io.WriteCloser
	session *ssh.Session
	stop    func() bool
}

func (c *testSSHStdio) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *testSSHStdio) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *testSSHStdio) CloseWrite() error           { return c.writer.Close() }
func (c *testSSHStdio) Close() error {
	if c.stop != nil {
		c.stop()
	}
	_ = c.writer.Close()
	return c.session.Close()
}
