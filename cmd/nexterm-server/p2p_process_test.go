package main

import (
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pion/stun/v4"
)

func TestServerProcessRejectsInvalidP2PEnvironment(t *testing.T) {
	binary := buildServerBinary(t)
	for _, entry := range []string{"NEXTERM_P2P=sometimes", "NEXTERM_STUN_LISTEN=no-port", "NEXTERM_STUN_ADVERTISE=nx.example.com", "NEXTERM_STUN_EXTERNAL=stun.example.net:0"} {
		t.Run(entry, func(t *testing.T) {
			cmd := exec.Command(binary, "--listen", "127.0.0.1:0", "--data-dir", t.TempDir())
			cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=", entry)
			output, err := cmd.CombinedOutput()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("%s = %v, output %q", entry, err, output)
			}
			name, _, _ := strings.Cut(entry, "=")
			if !strings.Contains(string(output), name) {
				t.Fatalf("error does not name %s: %q", name, output)
			}
		})
	}
}

func TestServerProcessAnswersSTUNOnTheConfiguredAddress(t *testing.T) {
	binary := buildServerBinary(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stunAddress := probe.LocalAddr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(binary, "--listen", address, "--data-dir", t.TempDir())
	cmd.Env = serverProcessEnv(t, "NEXTERM_WEB_ROOT=", "NEXTERM_STUN_LISTEN="+stunAddress)
	buffer := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buffer, buffer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &testServerProcess{cmd: cmd, wait: make(chan error, 1), output: buffer}
	go func() { process.wait <- cmd.Wait() }()
	defer process.stop(t)

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target, err := net.ResolveUDPAddr("udp", stunAddress)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	buffer2 := make([]byte, 1500)
	for {
		if err := process.err(); err != nil {
			t.Fatalf("server exited: %v\n%s", err, buffer.String())
		}
		request, err := stun.Build(stun.TransactionID, stun.BindingRequest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.WriteTo(request.Raw, target); err != nil {
			t.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := client.ReadFrom(buffer2)
		if err == nil {
			response := &stun.Message{Raw: append([]byte(nil), buffer2[:n]...)}
			var mapped stun.XORMappedAddress
			if err := response.Decode(); err != nil || response.Type != stun.BindingSuccess || mapped.GetFrom(response) != nil {
				t.Fatalf("response = %v, %v", response, err)
			}
			if mapped.Port != client.LocalAddr().(*net.UDPAddr).Port {
				t.Fatalf("mapped = %s, client = %s", mapped.String(), client.LocalAddr())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no STUN response: %v\n%s", err, buffer.String())
		}
	}
}
