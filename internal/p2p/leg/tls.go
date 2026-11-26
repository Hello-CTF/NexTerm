package leg

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"
)

const tlsServerName = "nexterm-p2p"

// selfSignedCertificate wraps an Ed25519 key in a throwaway certificate.
// Only its public key matters: the peer pins it and ignores names and dates.
func selfSignedCertificate(key ed25519.PrivateKey) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: tlsServerName},
		DNSNames:     []string{tlsServerName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// peerVerifier accepts exactly one Ed25519 public key. TLS 1.3 still checks
// the CertificateVerify signature against the leaf key, so pinning the key is
// enough even though chain verification is skipped.
type peerVerifier struct {
	key      ed25519.PublicKey
	mismatch atomic.Bool
}

var errPeerKeyMismatch = errors.New("peer certificate key is not the pinned key")

func (v *peerVerifier) verify(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		v.mismatch.Store(true)
		return errPeerKeyMismatch
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok || subtle.ConstantTimeCompare(pub, v.key) != 1 {
		v.mismatch.Store(true)
		return errPeerKeyMismatch
	}
	return nil
}

func (v *peerVerifier) mismatched() bool { return v.mismatch.Load() }

func baseTLSConfig(key ed25519.PrivateKey, v *peerVerifier) (*tls.Config, error) {
	cert, err := selfSignedCertificate(key)
	if err != nil {
		return nil, fmt.Errorf("p2p leg: certificate: %w", err)
	}
	return &tls.Config{
		Certificates:           []tls.Certificate{cert},
		MinVersion:             tls.VersionTLS13,
		NextProtos:             []string{alpn},
		InsecureSkipVerify:     true, // replaced by VerifyConnection key pinning
		VerifyConnection:       v.verify,
		SessionTicketsDisabled: true,
	}, nil
}

func clientTLSConfig(key ed25519.PrivateKey, v *peerVerifier) (*tls.Config, error) {
	c, err := baseTLSConfig(key, v)
	if err != nil {
		return nil, err
	}
	c.ServerName = tlsServerName
	return c, nil
}

func serverTLSConfig(key ed25519.PrivateKey, v *peerVerifier) (*tls.Config, error) {
	c, err := baseTLSConfig(key, v)
	if err != nil {
		return nil, err
	}
	c.ClientAuth = tls.RequireAnyClientCert
	return c, nil
}
