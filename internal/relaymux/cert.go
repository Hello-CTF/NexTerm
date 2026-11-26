package relaymux

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// PinnedServerName is the TLS server name of a self-signed relay certificate; the client checks the pinned
// key, not the name.
const PinnedServerName = "nexterm-relay"

// ServerCertificate is the certificate the QUIC relay presents and, for a self-signed one, the pin clients
// learn from the authenticated config endpoint.
type ServerCertificate struct {
	Certificate tls.Certificate
	// Pin is the base64url SHA-256 of the certificate's SubjectPublicKeyInfo; empty for a CA-issued
	// certificate, which clients verify against their system roots and the advertised host name instead.
	Pin string
}

// SPKIPin returns the base64url (no padding) SHA-256 of a DER certificate's SubjectPublicKeyInfo.
func SPKIPin(der []byte) (string, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// LoadServerCertificate loads a CA-issued PEM certificate chain and key, for a server that terminates TLS
// with a certificate clients already trust.
func LoadServerCertificate(certFile, keyFile string) (ServerCertificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return ServerCertificate{}, fmt.Errorf("relaymux: load certificate: %w", err)
	}
	return ServerCertificate{Certificate: cert}, nil
}

// LoadOrCreatePinnedCertificate returns a self-signed certificate over the Ed25519 key stored at keyPath,
// creating the key (mode 0600) on first use, so the pin stays stable across restarts; an empty keyPath uses a
// fresh in-memory key. The certificate itself is regenerated on every start; only its key is pinned.
func LoadOrCreatePinnedCertificate(keyPath string) (ServerCertificate, error) {
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return ServerCertificate{}, err
	}
	return PinnedCertificate(key)
}

// PinnedCertificate wraps key in a self-signed certificate and returns it with its pin.
func PinnedCertificate(key ed25519.PrivateKey) (ServerCertificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return ServerCertificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: PinnedServerName},
		DNSNames:     []string{PinnedServerName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return ServerCertificate{}, err
	}
	pin, err := SPKIPin(der)
	if err != nil {
		return ServerCertificate{}, err
	}
	return ServerCertificate{Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, Pin: pin}, nil
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		return key, err
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("relaymux: %s holds no PEM key", path)
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("relaymux: parse %s: %w", path, err)
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("relaymux: %s is not an Ed25519 key", path)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	return key, nil
}

// ServerTLSConfig is the QUIC relay's TLS configuration.
func ServerTLSConfig(cert ServerCertificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert.Certificate},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{ALPN},
	}
}

var errPinMismatch = errors.New("relaymux: server key does not match the pinned key")

// clientTLSConfig authenticates the QUIC server: with a pin it accepts exactly the pinned key (TLS 1.3's
// CertificateVerify proves possession, so names and dates of the self-signed certificate do not matter);
// without one it verifies the chain against roots (nil: system roots) and serverName. There is no mode that
// accepts an unauthenticated server.
func clientTLSConfig(pin, serverName string, roots *x509.CertPool) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN}}
	if pin == "" {
		if serverName == "" {
			return nil, errors.New("relaymux: a QUIC endpoint without a pin needs a server name")
		}
		config.ServerName = serverName
		config.RootCAs = roots
		return config, nil
	}
	want, err := base64.RawURLEncoding.DecodeString(pin)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("relaymux: malformed server pin")
	}
	config.ServerName = PinnedServerName
	// Chain verification is replaced by the pin check below, which runs on every handshake.
	config.InsecureSkipVerify = true
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errPinMismatch
		}
		sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if subtle.ConstantTimeCompare(sum[:], want) != 1 {
			return errPinMismatch
		}
		return nil
	}
	return config, nil
}
