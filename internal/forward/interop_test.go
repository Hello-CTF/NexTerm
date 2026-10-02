package forward

import (
	"bufio"
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
)

func TestRealSSHForwardingInterop(t *testing.T) {
	if os.Getenv("NEXTERM_FORWARD_INTEROP") != "1" {
		t.Skip("opt-in real SSH forwarding skipped: set NEXTERM_FORWARD_INTEROP=1, the M12 SSH test variables, and NEXTERM_FORWARD_TEST_TARGET to an SSH endpoint reachable from the remote host")
	}
	targetHost, targetPortText, err := net.SplitHostPort(os.Getenv("NEXTERM_FORWARD_TEST_TARGET"))
	if err != nil {
		t.Fatalf("NEXTERM_FORWARD_TEST_TARGET must be host:port: %v", err)
	}
	targetPort, err := strconv.ParseUint(targetPortText, 10, 16)
	if err != nil || targetPort == 0 {
		t.Fatalf("invalid target port %q", targetPortText)
	}
	host, portText, err := net.SplitHostPort(os.Getenv("NEXTERM_SSH_TEST_HOST"))
	if err != nil {
		t.Fatalf("NEXTERM_SSH_TEST_HOST must be host:port: %v", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := os.Getenv("NEXTERM_SSH_TEST_HOST_KEY")
	if fingerprint == "" {
		t.Fatal("NEXTERM_SSH_TEST_HOST_KEY is required; forwarding tests never disable host-key verification")
	}
	auth := ssh.AuthConfig{}
	if password := os.Getenv("NEXTERM_SSH_TEST_PASSWORD"); password != "" {
		auth.Method = ssh.AuthPassword
		auth.Password = password
	} else if keyPath := os.Getenv("NEXTERM_SSH_TEST_KEY"); keyPath != "" {
		auth.Method = ssh.AuthKey
		auth.KeyPath = keyPath
		auth.Passphrase = os.Getenv("NEXTERM_SSH_TEST_PASSPHRASE")
	} else {
		t.Fatal("set NEXTERM_SSH_TEST_PASSWORD or NEXTERM_SSH_TEST_KEY")
	}
	connectCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	client, err := ssh.Connect(connectCtx, ssh.Config{
		Host:            host,
		Port:            int(port),
		User:            os.Getenv("NEXTERM_SSH_TEST_USER"),
		Auth:            auth,
		HostKeys:        ssh.NewMemoryHostKeyStore(),
		HostKeyApproval: &ssh.HostKeyApproval{Fingerprint: fingerprint},
		ConnectTimeout:  15 * time.Second,
	})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	service := NewService(Config{
		Provider: DialerProviderFunc(func(context.Context, string) (base.Dialer, error) {
			return client, nil
		}),
		Policy: Policy{Desktop: true},
	})
	defer service.Close()

	static, err := service.CreateLocal(t.Context(), CreateLocalArgs{
		SessionID:  "real-ssh",
		TargetHost: targetHost,
		TargetPort: uint16(targetPort),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSSHBanner(t, dialForward(t, static))

	socks, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "real-ssh"})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, socks)
	negotiateTestSOCKS(t, conn)
	addressType := byte(3)
	if ip := net.ParseIP(targetHost); ip != nil {
		addressType = 1
		if ip.To4() == nil {
			addressType = 4
		}
	}
	writeTestSOCKSRequest(t, conn, 1, addressType, targetHost, uint16(targetPort))
	if reply := readTestSOCKSReply(t, conn); reply != 0 {
		t.Fatalf("real SOCKS reply = %d", reply)
	}
	assertSSHBanner(t, conn)
}

func assertSSHBanner(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	banner, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(banner, "SSH-") {
		t.Fatalf("forwarded banner = %q", banner)
	}
}
