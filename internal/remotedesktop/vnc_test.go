package remotedesktop

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"math/big"
	"math/bits"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func dialTo(host string, port int) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
}

func writeReason(w io.Writer, reason string) {
	head := make([]byte, 4)
	binary.BigEndian.PutUint32(head, uint32(len(reason)))
	_, _ = w.Write(append(head, reason...))
}

// rfbServerStart sends the banner, reads the client version and offers the given types (RFB 3.8).
func rfbServerStart(t testing.TB, conn net.Conn, types ...byte) {
	t.Helper()
	_, _ = conn.Write([]byte("RFB 003.008\n"))
	if got := string(mustRead(t, conn, 12)); got != "RFB 003.008\n" {
		t.Errorf("client version = %q", got)
	}
	_, _ = conn.Write(append([]byte{byte(len(types))}, types...))
}

func result(conn net.Conn, ok bool, reason string) {
	if ok {
		_, _ = conn.Write([]byte{0, 0, 0, 0})
		return
	}
	_, _ = conn.Write([]byte{0, 0, 0, 1})
	writeReason(conn, reason)
}

func TestVNCSecurityNone(t *testing.T) {
	host, port := listen(t, func(conn net.Conn) {
		rfbServerStart(t, conn, rfbSecNone)
		if choice := mustRead(t, conn, 1); choice[0] != rfbSecNone {
			t.Errorf("choice = %d", choice[0])
		}
		result(conn, true, "")
		if init := mustRead(t, conn, 1); init[0] != 1 {
			t.Errorf("ClientInit = %d", init[0])
		}
	})
	conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestVNCAuthResponseIsVerifiableByServer(t *testing.T) {
	challenge := []byte("0123456789abcdef")
	for _, tc := range []struct {
		name, serverPassword string
		wantErr              string
	}{
		{"correct", "secret", ""},
		{"wrong", "other", "bad password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, port := listen(t, func(conn net.Conn) {
				rfbServerStart(t, conn, rfbSecVNCAuth)
				mustRead(t, conn, 1)
				_, _ = conn.Write(challenge)
				response := mustRead(t, conn, 16)
				// Independent check: DES-decrypt the response with the bit reversed key of the server's password.
				key := make([]byte, 8)
				for i := 0; i < len(tc.serverPassword) && i < 8; i++ {
					key[i] = bits.Reverse8(tc.serverPassword[i])
				}
				block, _ := des.NewCipher(key)
				plain := make([]byte, 16)
				block.Decrypt(plain[:8], response[:8])
				block.Decrypt(plain[8:], response[8:])
				result(conn, bytes.Equal(plain, challenge), "bad password")
			})
			conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Password: "secret"})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestVNCAppleRemoteDesktop(t *testing.T) {
	prime, err := rand.Prime(rand.Reader, 256)
	if err != nil {
		t.Fatal(err)
	}
	host, port := listen(t, func(conn net.Conn) {
		rfbServerStart(t, conn, rfbSecARD)
		mustRead(t, conn, 1)
		privRaw := make([]byte, 32)
		_, _ = rand.Read(privRaw)
		priv := new(big.Int).SetBytes(privRaw)
		pub := new(big.Int).Exp(big.NewInt(2), priv, prime)
		head := []byte{0, 2, 0, 32}
		_, _ = conn.Write(append(append(head, prime.FillBytes(make([]byte, 32))...), pub.FillBytes(make([]byte, 32))...))
		encrypted := mustRead(t, conn, 128)
		clientPub := new(big.Int).SetBytes(mustRead(t, conn, 32))
		shared := new(big.Int).Exp(clientPub, priv, prime).FillBytes(make([]byte, 32))
		key := md5.Sum(shared)
		block, _ := aes.NewCipher(key[:])
		plain := make([]byte, 128)
		for i := 0; i < 128; i += 16 {
			block.Decrypt(plain[i:i+16], encrypted[i:i+16])
		}
		user := string(plain[:bytes.IndexByte(plain[:64], 0)])
		pass := string(plain[64 : 64+bytes.IndexByte(plain[64:], 0)])
		result(conn, user == "admin" && pass == "hunter2", "ard denied")
	})
	conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Username: "admin", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Username: "admin", Password: "wrong"}); err == nil || !strings.Contains(err.Error(), "ard denied") {
		t.Fatalf("wrong credentials err = %v", err)
	}
}

// veNCryptServer offers the subtypes, then runs the chosen sub-authentication behind TLS (except Plain).
func veNCryptServer(t testing.TB, cert tls.Certificate, subtypes []uint32, onCreds func(user, pass string) bool) func(net.Conn) {
	return func(conn net.Conn) {
		rfbServerStart(t, conn, rfbSecVeNCrypt)
		mustRead(t, conn, 1)
		_, _ = conn.Write([]byte{0, 2})
		mustRead(t, conn, 2)
		_, _ = conn.Write([]byte{0})
		list := []byte{byte(len(subtypes))}
		for _, s := range subtypes {
			list = binary.BigEndian.AppendUint32(list, s)
		}
		_, _ = conn.Write(list)
		chosen := binary.BigEndian.Uint32(mustRead(t, conn, 4))
		_, _ = conn.Write([]byte{1})
		var secured net.Conn = conn
		if chosen != vencryptPlain {
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			secured = tlsConn
		}
		switch chosen {
		case vencryptX509Plain, vencryptPlain:
			head := make([]byte, 8)
			if _, err := io.ReadFull(secured, head); err != nil {
				return
			}
			user := mustRead(t, secured, int(binary.BigEndian.Uint32(head[:4])))
			pass := mustRead(t, secured, int(binary.BigEndian.Uint32(head[4:])))
			result(secured, onCreds(string(user), string(pass)), "bad credentials")
		case vencryptX509None:
			result(secured, true, "")
		}
	}
}

func TestVNCVeNCryptX509PlainPinsCertificate(t *testing.T) {
	cert, der := newTestCertificate(t, "vnc.test")
	var mu sync.Mutex
	var got [2]string
	host, port := listen(t, veNCryptServer(t, cert, []uint32{vencryptTLSNone, vencryptX509Plain}, func(user, pass string) bool {
		mu.Lock()
		got = [2]string{user, pass}
		mu.Unlock()
		return true
	}))
	var seen []byte
	conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{
		Username: "u", Password: "p",
		VerifyCert: func(chain [][]byte, leaf *x509.Certificate) error { seen = chain[0]; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !bytes.Equal(seen, der) {
		t.Fatal("verifier did not receive the served certificate")
	}
	mu.Lock()
	defer mu.Unlock()
	if got != [2]string{"u", "p"} {
		t.Fatalf("credentials = %v", got)
	}
}

func TestVNCVeNCryptRefusedCertificateSendsNoCredentials(t *testing.T) {
	cert, _ := newTestCertificate(t, "vnc.test")
	var called bool
	var mu sync.Mutex
	host, port := listen(t, veNCryptServer(t, cert, []uint32{vencryptX509Plain}, func(string, string) bool {
		mu.Lock()
		called = true
		mu.Unlock()
		return true
	}))
	sentinel := userErr("pin refused")
	_, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{
		Username: "u", Password: "p",
		VerifyCert: func([][]byte, *x509.Certificate) error { return sentinel },
	})
	if err == nil || !strings.Contains(err.Error(), "pin refused") {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Fatal("credentials were sent before the certificate was accepted")
	}
}

func TestVNCVeNCryptPlainAndNoneVariants(t *testing.T) {
	cert, _ := newTestCertificate(t, "vnc.test")
	host, port := listen(t, veNCryptServer(t, cert, []uint32{vencryptX509None}, nil))
	conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Password: "p", VerifyCert: func([][]byte, *x509.Certificate) error { return nil }})
	if err != nil {
		t.Fatalf("X509None: %v", err)
	}
	conn.Close()
	host, port = listen(t, veNCryptServer(t, cert, []uint32{vencryptPlain}, func(u, p string) bool { return u == "u" && p == "p" }))
	conn, err = vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Username: "u", Password: "p"})
	if err != nil {
		t.Fatalf("Plain: %v", err)
	}
	conn.Close()
}

func TestVNCAnonymousTLSOnlyIsRejectedClearly(t *testing.T) {
	// The client hangs up after reading the offer, so this server must not treat short reads as failures.
	host, port := listen(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		_, _ = io.ReadFull(conn, make([]byte, 12))
		_, _ = conn.Write([]byte{1, rfbSecVeNCrypt})
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		_, _ = conn.Write([]byte{0, 2})
		_, _ = io.ReadFull(conn, make([]byte, 2))
		list := []byte{0, 3}
		list = list[:1]
		list[0] = 3
		for _, s := range []uint32{vencryptTLSNone, vencryptTLSVnc, vencryptTLSPlain} {
			list = binary.BigEndian.AppendUint32(list, s)
		}
		_, _ = conn.Write([]byte{0})
		_, _ = conn.Write(list)
		_, _ = io.ReadFull(conn, make([]byte, 4))
	})
	_, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Password: "p"})
	if err == nil || !strings.Contains(err.Error(), "匿名 TLS") {
		t.Fatalf("err = %v", err)
	}
}

func TestVNCUnsupportedAndMissingPassword(t *testing.T) {
	host, port := listen(t, func(conn net.Conn) { rfbServerStart(t, conn, 5, 6) })
	if _, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Password: "p"}); err == nil || !strings.Contains(err.Error(), "不支持的安全类型") {
		t.Fatalf("unsupported err = %v", err)
	}
	host, port = listen(t, func(conn net.Conn) { rfbServerStart(t, conn, rfbSecVNCAuth) })
	if _, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{}); err == nil || !strings.Contains(err.Error(), "需要密码") {
		t.Fatalf("missing password err = %v", err)
	}
	host, port = listen(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("SSH-2.0-OpenSSH\n"))
	})
	if _, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{}); err == nil || !strings.Contains(err.Error(), "不是 VNC") {
		t.Fatalf("non-vnc err = %v", err)
	}
}

func TestVNCLegacyVersion33(t *testing.T) {
	host, port := listen(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("RFB 003.003\n"))
		if got := string(mustRead(t, conn, 12)); got != "RFB 003.003\n" {
			t.Errorf("client version = %q", got)
		}
		_, _ = conn.Write([]byte{0, 0, 0, rfbSecNone})
		mustRead(t, conn, 1)
	})
	conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestVNCRequireTLSRefusesEveryPlainPath(t *testing.T) {
	for name, handler := range map[string]func(net.Conn){
		"none only":     func(conn net.Conn) { rfbServerStart(t, conn, rfbSecNone) },
		"vnc auth only": func(conn net.Conn) { rfbServerStart(t, conn, rfbSecVNCAuth) },
		"version 3.3": func(conn net.Conn) {
			_, _ = conn.Write([]byte("RFB 003.003\n"))
			_, _ = io.ReadFull(conn, make([]byte, 12))
			_, _ = conn.Write([]byte{0, 0, 0, rfbSecVNCAuth})
		},
		"vencrypt plain only": func(conn net.Conn) {
			_, _ = conn.Write([]byte("RFB 003.008\n"))
			_, _ = io.ReadFull(conn, make([]byte, 12))
			_, _ = conn.Write([]byte{1, rfbSecVeNCrypt})
			_, _ = io.ReadFull(conn, make([]byte, 1))
			_, _ = conn.Write([]byte{0, 2})
			_, _ = io.ReadFull(conn, make([]byte, 2))
			_, _ = conn.Write([]byte{0, 1, 0, 0, 1, 0})
			if n, _ := io.ReadFull(conn, make([]byte, 4)); n == 4 {
				t.Error("a subtype was chosen although TLS is required")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			host, port := listen(t, handler)
			_, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Username: "u", Password: "p", RequireTLS: true})
			if err == nil || !strings.Contains(err.Error(), "不再提供 TLS") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVNCDefaultTigerVNCOffersAnonymousTLSAndVncAuth(t *testing.T) {
	// SecurityTypes=VeNCrypt(TLSVnc),VncAuth: no pin, so the VncAuth fallback must connect.
	var connections int
	var mu sync.Mutex
	host, port := listen(t, func(conn net.Conn) {
		mu.Lock()
		connections++
		n := connections
		mu.Unlock()
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		_, _ = io.ReadFull(conn, make([]byte, 12))
		if n == 1 {
			_, _ = conn.Write([]byte{2, rfbSecVeNCrypt, rfbSecVNCAuth})
			if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
				return
			}
			_, _ = conn.Write([]byte{0, 2})
			_, _ = io.ReadFull(conn, make([]byte, 2))
			list := []byte{1}
			list = binary.BigEndian.AppendUint32(list, vencryptTLSVnc)
			_, _ = conn.Write([]byte{0})
			_, _ = conn.Write(list)
			_, _ = io.ReadFull(conn, make([]byte, 4))
			return
		}
		_, _ = conn.Write([]byte{1, rfbSecVNCAuth})
		_, _ = io.ReadFull(conn, make([]byte, 1))
		_, _ = conn.Write(make([]byte, 16))
		_, _ = io.ReadFull(conn, make([]byte, 16))
		result(conn, true, "")
	})
	conn, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestVNCAnonymousTLSMessageOnlyWithPassword(t *testing.T) {
	offer := func(conn net.Conn) {
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		_, _ = io.ReadFull(conn, make([]byte, 12))
		_, _ = conn.Write([]byte{1, rfbSecVeNCrypt})
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		_, _ = conn.Write([]byte{0, 2})
		_, _ = io.ReadFull(conn, make([]byte, 2))
		list := []byte{1}
		list = binary.BigEndian.AppendUint32(list, vencryptTLSVnc)
		_, _ = conn.Write([]byte{0})
		_, _ = conn.Write(list)
		_, _ = io.ReadFull(conn, make([]byte, 4))
	}
	host, port := listen(t, offer)
	if _, err := vncDialUpstream(background(), dialTo(host, port), vncUpstreamConfig{}); err == nil || !strings.Contains(err.Error(), "需要密码") || strings.Contains(err.Error(), "匿名") {
		t.Fatalf("no password: err = %v", err)
	}
}
