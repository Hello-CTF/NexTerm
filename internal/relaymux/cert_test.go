package relaymux

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A CA-issued relay certificate is verified against the given roots and the server name; there is no pin.
func TestDialQUICVerifiesCAIssuedCertificate(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "relay.test"}, DNSNames: []string{"relay.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := LoadServerCertificate(certFile, keyFile)
	if err != nil || cert.Pin != "" {
		t.Fatalf("load = pin %q, %v", cert.Pin, err)
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := ListenUDPMux(conn, ServerTLSConfig(cert))
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	go func() {
		for {
			if _, err := mux.Accept(context.Background()); err != nil {
				return
			}
		}
	}()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := DialQUIC(ctx, mux.Addr().String(), DialConfig{ServerName: "relay.test", RootCAs: roots})
	if err != nil {
		t.Fatalf("dial with the issuing root: %v", err)
	}
	_ = session.Close()
	if _, err := DialQUIC(ctx, mux.Addr().String(), DialConfig{ServerName: "other.test", RootCAs: roots}); err == nil {
		t.Fatal("dial accepted a certificate for another name")
	}
	if _, err := DialQUIC(ctx, mux.Addr().String(), DialConfig{ServerName: "relay.test"}); err == nil {
		t.Fatal("dial accepted a certificate outside the system roots")
	}
}
