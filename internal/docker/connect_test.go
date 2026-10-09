package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestSSHStreamlocalClientUsesExistingDialer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)
	var network, address string
	dialer := DialerFunc(func(ctx context.Context, gotNetwork, gotAddress string) (net.Conn, error) {
		network, address = gotNetwork, gotAddress
		return (&net.Dialer{}).DialContext(ctx, "tcp", serverURL.Host)
	})
	cli, err := NewSSHStreamlocalClient("/custom/docker.sock", dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	_, err = cli.ContainerList(t.Context(), client.ContainerListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if network != "unix" || address != "/custom/docker.sock" {
		t.Fatalf("dial = %s %q", network, address)
	}
}

func TestDialStdioClientUsesChannelPerConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/_ping" {
			writer.Header().Set("API-Version", "1.56")
			writer.WriteHeader(http.StatusOK)
			return
		}
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)
	var opens atomic.Int32
	opener := StdioOpenerFunc(func(ctx context.Context) (io.ReadWriteCloser, error) {
		opens.Add(1)
		return net.Dial("tcp", serverURL.Host)
	})
	cli, err := NewSSHDialStdioClient(opener)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.ContainerList(t.Context(), client.ContainerListOptions{}); err != nil {
		t.Fatal(err)
	}
	if opens.Load() == 0 {
		t.Fatal("dial-stdio opener was not used")
	}
}

func TestTLSClientUsesInMemoryConfig(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	serverURL, _ := url.Parse(server.URL)
	cli, err := NewClient(ClientConfig{
		Host:       "tcp://" + serverURL.Host,
		TLSConfig:  &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		APIVersion: "1.56",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.ContainerList(t.Context(), client.ContainerListOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestChannelConnCloseWriteAndDeadlines(t *testing.T) {
	channel := &deadlineChannel{closed: make(chan struct{})}
	conn := NewChannelConn(channel)
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if channel.closeWrite.Load() != 1 {
		t.Fatal("CloseWrite was not forwarded")
	}
	if err := conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read deadline error = %v", err)
	}

	channel2 := &deadlineChannel{closed: make(chan struct{})}
	conn2 := NewChannelConn(channel2)
	if err := conn2.SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-channel2.closed:
	case <-time.After(time.Second):
		t.Fatal("deadline did not force channel close")
	}
	if _, err := conn2.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after close = %v", err)
	}
	if err := conn2.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("set deadline after close = %v", err)
	}
}

type deadlineChannel struct {
	closed     chan struct{}
	closeWrite atomic.Int32
	closeOnce  atomic.Bool
}

func (c *deadlineChannel) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}
func (c *deadlineChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *deadlineChannel) Close() error {
	if c.closeOnce.CompareAndSwap(false, true) {
		close(c.closed)
	}
	return nil
}
func (c *deadlineChannel) CloseWrite() error {
	c.closeWrite.Add(1)
	return nil
}
