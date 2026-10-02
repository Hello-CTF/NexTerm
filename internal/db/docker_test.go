package db

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const dockerIntegrationFlag = "NEXTERM_DB_DOCKER"

type dockerTestContainer struct {
	name string
	host string
	port uint16
}

func requireDockerIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(dockerIntegrationFlag) != "1" {
		t.Skipf("set %s=1 to run isolated MySQL 8.4/Redis 7.4 Docker tests", dockerIntegrationFlag)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Fatalf("docker is unavailable: %v: %s", err, output)
	}
}

func startDockerTestContainer(t *testing.T, containerPort string, args ...string) dockerTestContainer {
	t.Helper()
	name := fmt.Sprintf("nexterm-m18-%d", time.Now().UnixNano())
	runArgs := []string{"run", "--detach", "--rm", "--name", name, "--publish", "127.0.0.1::" + containerPort}
	runArgs = append(runArgs, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	output, err := exec.CommandContext(ctx, "docker", runArgs...).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("start test container: %v: %s", err, output)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "--force", name).Run()
	})

	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	output, err = exec.CommandContext(ctx, "docker", "port", name, containerPort+"/tcp").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("inspect test container port: %v: %s", err, output)
	}
	address := strings.TrimSpace(strings.Split(string(output), "\n")[0])
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parse docker port %q: %v", address, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatalf("parse docker port %q: %v", address, err)
	}
	return dockerTestContainer{name: name, host: host, port: uint16(port)}
}

func (c dockerTestContainer) logs(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "logs", "--tail", "80", c.name).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("docker logs failed: %v: %s", err, output)
	}
	return string(output)
}

type testTLSFiles struct {
	dir     string
	options *TLSOptions
}

func makeTestTLSFiles(t *testing.T) testTLSFiles {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "NexTerm M18 Test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}

	writePEM := func(name, blockType string, der []byte, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	caFile := writePEM("ca.crt", "CERTIFICATE", caDER, 0o644)
	writePEM("server.crt", "CERTIFICATE", serverDER, 0o644)
	writePEM("server.key", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey), 0o600)
	return testTLSFiles{
		dir: dir,
		options: &TLSOptions{
			Mode:       "required",
			ServerName: "127.0.0.1",
			CAFile:     caFile,
		},
	}
}

func waitForMySQL(t *testing.T, container dockerTestContainer, config MySQLConfig) {
	t.Helper()
	config.Host = container.host
	config.Port = container.port
	deadline := time.Now().Add(120 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		connection, err := connectMySQL(ctx, config)
		cancel()
		if err == nil {
			_ = connection.Close()
			return
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("MySQL did not become ready: %v\n%s", lastErr, container.logs(t))
}

func waitForRedis(t *testing.T, container dockerTestContainer, config RedisConfig) {
	t.Helper()
	config.Host = container.host
	config.Port = container.port
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		connection, err := connectRedis(ctx, config)
		cancel()
		if err == nil {
			_ = connection.Close()
			return
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Redis did not become ready: %v\n%s", lastErr, container.logs(t))
}
