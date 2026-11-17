package winrm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcmturner/gofork/encoding/asn1"
	"github.com/jcmturner/gokrb5/v8/asn1tools"
	"github.com/jcmturner/gokrb5/v8/crypto"
	"github.com/jcmturner/gokrb5/v8/gssapi"
	"github.com/jcmturner/gokrb5/v8/iana/errorcode"
	"github.com/jcmturner/gokrb5/v8/iana/etypeID"
	"github.com/jcmturner/gokrb5/v8/iana/keyusage"
	"github.com/jcmturner/gokrb5/v8/iana/nametype"
	"github.com/jcmturner/gokrb5/v8/krberror"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/spnego"
	"github.com/jcmturner/gokrb5/v8/types"
)

func TestKerberosConfigDerivesRealmAndSPN(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		username string
		realm    string
		spn      string
	}{
		{"user principal", Config{User: "alice@corp.example.com"}, "alice", "CORP.EXAMPLE.COM", "HTTP/win01.corp.example.com"},
		{"down-level name", Config{User: `CORP\alice`}, "alice", "CORP", "HTTP/win01.corp.example.com"},
		{"domain field", Config{User: "alice", Domain: "corp.example.com"}, "alice", "CORP.EXAMPLE.COM", "HTTP/win01.corp.example.com"},
		{"explicit realm wins", Config{User: `CORP\alice`, Kerberos: KerberosConfig{Realm: "corp.example.com"}}, "alice", "CORP.EXAMPLE.COM", "HTTP/win01.corp.example.com"},
		{"SPN override", Config{User: "alice@corp.example.com", Kerberos: KerberosConfig{SPN: "WSMAN/win01"}}, "alice", "CORP.EXAMPLE.COM", "WSMAN/win01"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := test.config
			config.Host, config.Password, config.Auth = "WIN01.corp.example.com", "secret", "Kerberos"
			normalized, err := config.normalized()
			if err != nil {
				t.Fatal(err)
			}
			if normalized.Auth != AuthKerberos {
				t.Fatalf("auth = %q", normalized.Auth)
			}
			got := normalized.Kerberos
			if got.username != test.username || got.Realm != test.realm || got.SPN != test.spn {
				t.Fatalf("got user %q realm %q spn %q", got.username, got.Realm, got.SPN)
			}
			if len(got.KDCs) != 0 {
				t.Fatalf("KDCs = %v, want DNS discovery", got.KDCs)
			}
		})
	}
}

func TestKerberosConfigNormalizesKDCs(t *testing.T) {
	config, err := (Config{
		Host: "win01.corp.example.com", User: "alice@corp.example.com", Password: "secret", Auth: AuthKerberos,
		Kerberos: KerberosConfig{KDCs: []string{" dc1.corp.example.com ", "", "dc2.corp.example.com:750", "10.0.0.5", "fe80::1", "[fe80::2]:88"}},
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dc1.corp.example.com:88", "dc2.corp.example.com:750", "10.0.0.5:88", "[fe80::1]:88", "[fe80::2]:88"}
	if strings.Join(config.Kerberos.KDCs, ",") != strings.Join(want, ",") {
		t.Fatalf("KDCs = %v, want %v", config.Kerberos.KDCs, want)
	}
}

func TestKerberosConfigRejectsUnusableSettings(t *testing.T) {
	base := Config{Host: "win01.corp.example.com", User: "alice@corp.example.com", Password: "secret", Auth: AuthKerberos}
	tests := map[string]func(*Config){
		"no password":       func(c *Config) { c.Password = "" },
		"no realm":          func(c *Config) { c.User = "alice" },
		"IP host":           func(c *Config) { c.Host = "10.0.0.8" },
		"IPv6 host":         func(c *Config) { c.Host = "fe80::8" },
		"SPN without host":  func(c *Config) { c.Kerberos.SPN = "HTTP" },
		"SPN with realm":    func(c *Config) { c.Kerberos.SPN = "HTTP/win01@CORP.EXAMPLE.COM" },
		"KDC port":          func(c *Config) { c.Kerberos.KDCs = []string{"dc1:kerberos"} },
		"KDC out of range":  func(c *Config) { c.Kerberos.KDCs = []string{"dc1:70000"} },
		"realm with spaces": func(c *Config) { c.Kerberos.Realm = "CORP EXAMPLE" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := config.normalized(); err == nil {
				t.Fatalf("expected %+v to fail", config)
			}
		})
	}
	ipWithSPN := base
	ipWithSPN.Host, ipWithSPN.Kerberos.SPN = "10.0.0.8", "HTTP/win01.corp.example.com"
	if _, err := ipWithSPN.normalized(); err != nil {
		t.Fatalf("an IP host with an explicit SPN should be accepted: %v", err)
	}
}

func TestKerberosConfigSelectsSealingTransport(t *testing.T) {
	config, err := (Config{Host: "win01.corp.example.com", User: "alice@corp.example.com", Password: "secret", Auth: AuthKerberos}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := newLibraryRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := runner.httpClient.Transport.(*kerberosSealingTransport)
	if !ok {
		t.Fatalf("Kerberos config produced %T", runner.httpClient.Transport)
	}
	if runner.httpTransport.MaxConnsPerHost != 1 {
		t.Fatalf("Kerberos context must stay on one connection, MaxConnsPerHost = %d", runner.httpTransport.MaxConnsPerHost)
	}
	source, ok := transport.tickets.(*gokrb5TicketSource)
	if !ok {
		t.Fatalf("ticket source = %T", transport.tickets)
	}
	cfg := source.client.Config
	if cfg.LibDefaults.DefaultRealm != "CORP.EXAMPLE.COM" || !cfg.LibDefaults.DNSLookupKDC || cfg.LibDefaults.UDPPreferenceLimit != 1 {
		t.Fatalf("unexpected krb5 defaults: %+v", cfg.LibDefaults)
	}
	for _, id := range cfg.LibDefaults.PermittedEnctypeIDs {
		if id != etypeID.AES256_CTS_HMAC_SHA1_96 && id != etypeID.AES128_CTS_HMAC_SHA1_96 {
			t.Fatalf("permitted enctype %d is not AES", id)
		}
	}
	if source.client.Credentials.UserName() != "alice" || source.spn != "HTTP/win01.corp.example.com" {
		t.Fatalf("client user %q spn %q", source.client.Credentials.UserName(), source.spn)
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}

	explicit, err := (Config{
		Host: "win01.corp.example.com", User: "alice@corp.example.com", Password: "secret", Auth: AuthKerberos,
		Kerberos: KerberosConfig{KDCs: []string{"dc1.corp.example.com"}},
	}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	explicitSource := newGokrb5TicketSource(explicit)
	defer explicitSource.close()
	if explicitSource.client.Config.LibDefaults.DNSLookupKDC {
		t.Fatal("explicit KDCs must disable DNS discovery")
	}
	_, kdcs, err := explicitSource.client.Config.GetKDCs("CORP.EXAMPLE.COM", true)
	if err != nil || len(kdcs) != 1 || kdcs[1] != "dc1.corp.example.com:88" {
		t.Fatalf("KDCs = %v, %v", kdcs, err)
	}
}

func TestKerberosErrorClassification(t *testing.T) {
	krbError := func(code int32) messages.KRBError {
		return messages.NewKRBError(types.PrincipalName{}, "CORP.EXAMPLE.COM", code, "")
	}
	tests := []struct {
		name          string
		err           error
		kdcConfigured bool
		want          kerberosFailure
	}{
		{"clock skew", krbError(errorcode.KRB_AP_ERR_SKEW), false, kerberosFailureClockSkew},
		{"unknown user", krbError(errorcode.KDC_ERR_C_PRINCIPAL_UNKNOWN), false, kerberosFailurePrincipalUnknown},
		{"unknown SPN", krbError(errorcode.KDC_ERR_S_PRINCIPAL_UNKNOWN), false, kerberosFailureSPNUnknown},
		{"wrong password", krbError(errorcode.KDC_ERR_PREAUTH_FAILED), false, kerberosFailureBadPassword},
		{"ticket not decryptable", krbError(errorcode.KRB_AP_ERR_MODIFIED), false, kerberosFailureTicketNotAccepted},
		{"flattened KDC error", krberror.Errorf(krbError(errorcode.KDC_ERR_PREAUTH_FAILED), krberror.KDCError, "AS Exchange Error"), false, kerberosFailureBadPassword},
		{"wrapped as text", fmt.Errorf("could not get TGT: %v", krbError(errorcode.KDC_ERR_CLIENT_REVOKED)), false, kerberosFailureAccountRevoked},
		{"no preauth, bad key", krberror.NewErrorf(krberror.KRBMsgError, "AS Exchange Error: AS_REP is not valid or client password/keytab incorrect"), false, kerberosFailureBadPassword},
		{"KDC down", krberror.Errorf(errors.New("error sending to a KDC: dial tcp 10.0.0.5:88: i/o timeout"), krberror.NetworkingError, "AS Exchange Error"), true, kerberosFailureKDCUnreachable},
		{"no SRV records", krberror.Errorf(errors.New("communication error with KDC via TCP: no KDC SRV records found for realm CORP.EXAMPLE.COM"), krberror.NetworkingError, "AS Exchange Error"), false, kerberosFailureKDCNotFound},
		{"unclassified", errors.New("something else"), false, kerberosFailureUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyKerberosClientError(test.err, test.kdcConfigured)
			var classified *kerberosError
			if !errors.As(err, &classified) || classified.failure != test.want {
				t.Fatalf("classified %v as %v, want %v", test.err, err, test.want)
			}
			if test.want == kerberosFailureUnknown && !strings.Contains(err.Error(), test.err.Error()) {
				t.Fatalf("unclassified error %q hides its cause", err)
			}
			if test.want != kerberosFailureUnknown && err.Error() != kerberosFailureMessages[test.want] {
				t.Fatalf("message = %q", err)
			}
		})
	}
}

func testAESKey(t *testing.T, keyType int32, size int) types.EncryptionKey {
	t.Helper()
	key := make([]byte, size)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return types.EncryptionKey{KeyType: keyType, KeyValue: key}
}

func TestKerberosWrapTokenLayout(t *testing.T) {
	key := testAESKey(t, etypeID.AES256_CTS_HMAC_SHA1_96, 32)
	client, err := newKerberosMessageProtector(key, true, true, 7, 9)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("<s:Envelope>payload</s:Envelope>")
	framed, err := client.Wrap(message)
	if err != nil {
		t.Fatal(err)
	}
	headerLength := int(binary.LittleEndian.Uint32(framed[:4]))
	// 16-byte token header, then the rotated encrypted header copy (16), checksum (12) and confounder (16).
	if headerLength != 60 || len(framed) != 4+headerLength+len(message) {
		t.Fatalf("header length %d, framed %d bytes for a %d-byte message", headerLength, len(framed), len(message))
	}
	token := framed[4:]
	wantHeader := []byte{0x05, 0x04, kerberosWrapSealed | kerberosWrapAcceptorSubkey, 0xFF, 0, 0, 0, 28, 0, 0, 0, 0, 0, 0, 0, 7}
	if !bytes.Equal(token[:16], wantHeader) {
		t.Fatalf("token header = % x, want % x", token[:16], wantHeader)
	}
	ciphertext := append(append([]byte(nil), token[16+28:]...), token[16:16+28]...)
	et, err := crypto.GetEtype(key.KeyType)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := et.DecryptMessage(key.KeyValue, ciphertext, kerberosKeyUsageInitiatorSeal)
	if err != nil {
		t.Fatal(err)
	}
	headerCopy := append(append([]byte(nil), wantHeader[:6]...), 0, 0)
	headerCopy = append(headerCopy, wantHeader[8:]...)
	if !bytes.Equal(plaintext, append(append([]byte(nil), message...), headerCopy...)) {
		t.Fatalf("decrypted token = %q", plaintext)
	}
}

func TestKerberosWrapRoundTripAndTampering(t *testing.T) {
	for _, keyType := range []struct {
		id   int32
		size int
	}{{etypeID.AES128_CTS_HMAC_SHA1_96, 16}, {etypeID.AES256_CTS_HMAC_SHA1_96, 32}} {
		key := testAESKey(t, keyType.id, keyType.size)
		client, err := newKerberosMessageProtector(key, true, false, 100, 200)
		if err != nil {
			t.Fatal(err)
		}
		server, err := newKerberosMessageProtector(key, false, false, 200, 100)
		if err != nil {
			t.Fatal(err)
		}
		clientEncryption, _ := newWinRMMessageEncryption("kerberos", client)
		serverEncryption, _ := newWinRMMessageEncryption("kerberos", server)
		for _, size := range []int{0, 1, 15, 16, 17, 4096, maxEncryptedPartSize + 3} {
			message := bytes.Repeat([]byte("x"), size)
			request, err := clientEncryption.encrypt(message)
			if err != nil {
				t.Fatal(err)
			}
			got, err := serverEncryption.decrypt(request)
			if err != nil || !bytes.Equal(got, message) {
				t.Fatalf("etype %d size %d: server got %d bytes, %v", keyType.id, size, len(got), err)
			}
			reply, err := serverEncryption.encrypt(append([]byte("echo:"), message...))
			if err != nil {
				t.Fatal(err)
			}
			got, err = clientEncryption.decrypt(reply)
			if err != nil || !bytes.Equal(got, append([]byte("echo:"), message...)) {
				t.Fatalf("etype %d size %d: client got %d bytes, %v", keyType.id, size, len(got), err)
			}
		}
		if !strings.Contains(clientEncryption.contentType(), "application/HTTP-SPNEGO-session-encrypted") {
			t.Fatalf("content type = %q", clientEncryption.contentType())
		}

		replayed, err := server.Wrap([]byte("first"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Unwrap(replayed, 5); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Unwrap(replayed, 5); err == nil {
			t.Fatal("a replayed token was accepted")
		}
		tampered, err := server.Wrap([]byte("second"))
		if err != nil {
			t.Fatal(err)
		}
		tampered[len(tampered)-1] ^= 0x01
		if _, err := client.Unwrap(tampered, 6); err == nil {
			t.Fatal("a tampered token was accepted")
		}
		own, err := client.Wrap([]byte("reflected"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Unwrap(own, 9); err == nil {
			t.Fatal("a token in the client's own direction was accepted")
		}
	}
	if _, err := newKerberosMessageProtector(types.EncryptionKey{KeyType: etypeID.RC4_HMAC, KeyValue: make([]byte, 16)}, true, false, 0, 0); err == nil {
		t.Fatal("RC4-HMAC session keys must be refused for message encryption")
	}
}

// kerberosTestSource issues a ticket whose session key the test acceptor knows.
type kerberosTestSource struct {
	sessionKey types.EncryptionKey
	err        error
	mu         sync.Mutex
	issued     int
	closed     bool
}

func (s *kerberosTestSource) serviceTicket(ctx context.Context) (kerberosTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return kerberosTicket{}, s.err
	}
	s.issued++
	return kerberosTicket{
		ticket: messages.Ticket{
			TktVNO:  5,
			Realm:   "CORP.EXAMPLE.COM",
			SName:   types.NewPrincipalName(nametype.KRB_NT_SRV_INST, "HTTP/win01.corp.example.com"),
			EncPart: types.EncryptedData{EType: s.sessionKey.KeyType, KVNO: 2, Cipher: []byte("opaque to the client")},
		},
		sessionKey: s.sessionKey,
		realm:      "CORP.EXAMPLE.COM",
		cname:      types.NewPrincipalName(nametype.KRB_NT_PRINCIPAL, "alice"),
	}, nil
}

func (s *kerberosTestSource) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// kerberosTestServer is a WinRM acceptor: it reads the AP-REQ with the ticket session key, answers with an AP-REP
// and echoes sealed messages.
type kerberosTestServer struct {
	t              *testing.T
	sessionKey     types.EncryptionKey
	acceptorSubkey bool
	rejectWith     int32
	mu             sync.Mutex
	encryption     *winRMMessageEncryption
	handshakes     int
	sealed         int
	contentTypes   []string
}

func (s *kerberosTestServer) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/") {
		s.sealed++
		s.contentTypes = append(s.contentTypes, request.Header.Get("Content-Type"))
		if s.encryption == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		plaintext, err := s.encryption.decrypt(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reply, err := s.encryption.encrypt(append([]byte("echo:"), plaintext...))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", s.encryption.contentType())
		_, _ = w.Write(reply)
		return
	}
	token, status, err := s.accept(request.Header.Get("Authorization"))
	if err != nil {
		s.t.Errorf("acceptor: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("WWW-Authenticate", "Negotiate "+base64.StdEncoding.EncodeToString(token))
	w.WriteHeader(status)
}

func (s *kerberosTestServer) accept(authorization string) ([]byte, int, error) {
	scheme, encoded, _ := strings.Cut(authorization, " ")
	if scheme != "Negotiate" {
		return nil, 0, fmt.Errorf("scheme %q", scheme)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, 0, err
	}
	var init spnego.SPNEGOToken
	if err := init.Unmarshal(raw); err != nil || !init.Init {
		return nil, 0, fmt.Errorf("SPNEGO init token: %v", err)
	}
	if len(init.NegTokenInit.MechTypes) == 0 || !init.NegTokenInit.MechTypes[0].Equal(gssapi.OIDKRB5.OID()) {
		return nil, 0, fmt.Errorf("mech types %v", init.NegTokenInit.MechTypes)
	}
	tokenID, apReqBytes, err := parseKRB5Token(init.NegTokenInit.MechTokenBytes)
	if err != nil || tokenID != krb5TokenAPReq {
		return nil, 0, fmt.Errorf("mech token %s: %v", tokenID, err)
	}
	var apReq messages.APReq
	if err := apReq.Unmarshal(apReqBytes); err != nil {
		return nil, 0, err
	}
	if !types.IsFlagSet(&apReq.APOptions, 2) {
		return nil, 0, errors.New("AP-REQ does not require mutual authentication")
	}
	if err := apReq.DecryptAuthenticator(s.sessionKey); err != nil {
		return nil, 0, err
	}
	authenticator := apReq.Authenticator
	if flags := binary.LittleEndian.Uint32(authenticator.Cksum.Checksum[20:24]); flags&gssapi.ContextFlagConf == 0 || flags&gssapi.ContextFlagMutual == 0 {
		return nil, 0, fmt.Errorf("context flags %#x", flags)
	}
	if s.rejectWith != 0 {
		krbError := messages.NewKRBError(types.PrincipalName{}, "CORP.EXAMPLE.COM", s.rejectWith, "")
		errorBytes, err := krbError.Marshal()
		if err != nil {
			return nil, 0, err
		}
		mech, err := marshalKRB5Token(krb5TokenKRBError, errorBytes)
		if err != nil {
			return nil, 0, err
		}
		response := spnego.NegTokenResp{NegState: asn1.Enumerated(spnego.NegStateReject), SupportedMech: gssapi.OIDKRB5.OID(), ResponseToken: mech}
		token, err := response.Marshal()
		return token, http.StatusUnauthorized, err
	}
	serverSeq := int64(0x1234)
	part := messages.EncAPRepPart{CTime: authenticator.CTime, Cusec: authenticator.Cusec, SequenceNumber: serverSeq}
	key := authenticator.SubKey
	if s.acceptorSubkey {
		key = testAESKey(s.t, s.sessionKey.KeyType, len(s.sessionKey.KeyValue))
		part.Subkey = key
	}
	partBytes, err := asn1.Marshal(part)
	if err != nil {
		return nil, 0, err
	}
	encPart, err := crypto.GetEncryptedData(asn1tools.AddASNAppTag(partBytes, 27), s.sessionKey, keyusage.AP_REP_ENCPART, 0)
	if err != nil {
		return nil, 0, err
	}
	apRepBytes, err := asn1.Marshal(messages.APRep{PVNO: 5, MsgType: 15, EncPart: encPart})
	if err != nil {
		return nil, 0, err
	}
	mech, err := marshalKRB5Token(krb5TokenAPRep, asn1tools.AddASNAppTag(apRepBytes, 15))
	if err != nil {
		return nil, 0, err
	}
	protector, err := newKerberosMessageProtector(key, false, s.acceptorSubkey, uint64(serverSeq), uint64(authenticator.SeqNumber))
	if err != nil {
		return nil, 0, err
	}
	s.encryption, err = newWinRMMessageEncryption("kerberos", protector)
	if err != nil {
		return nil, 0, err
	}
	s.handshakes++
	response := spnego.NegTokenResp{NegState: asn1.Enumerated(spnego.NegStateAcceptCompleted), SupportedMech: gssapi.OIDKRB5.OID(), ResponseToken: mech}
	token, err := response.Marshal()
	return token, http.StatusOK, err
}

func postThrough(t *testing.T, transport http.RoundTripper, url, body string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	response, err := transport.RoundTrip(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.StatusCode, reply)
	}
	return string(reply), nil
}

func TestKerberosHandshakeAndSealedExchange(t *testing.T) {
	for _, acceptorSubkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("acceptor subkey %v", acceptorSubkey), func(t *testing.T) {
			sessionKey := testAESKey(t, etypeID.AES256_CTS_HMAC_SHA1_96, 32)
			server := &kerberosTestServer{t: t, sessionKey: sessionKey, acceptorSubkey: acceptorSubkey}
			httpServer := httptest.NewServer(server)
			defer httpServer.Close()
			inner := &http.Transport{MaxConnsPerHost: 1}
			defer inner.CloseIdleConnections()
			source := &kerberosTestSource{sessionKey: sessionKey}
			transport := newKerberosSealingTransport(inner, source)
			for i := range 3 {
				body := fmt.Sprintf("<s:Envelope>%d</s:Envelope>", i)
				reply, err := postThrough(t, transport, httpServer.URL+"/wsman", body)
				if err != nil {
					t.Fatal(err)
				}
				if reply != "echo:"+body {
					t.Fatalf("reply = %q", reply)
				}
			}
			server.mu.Lock()
			handshakes, sealed, contentType := server.handshakes, server.sealed, server.contentTypes[0]
			server.mu.Unlock()
			if handshakes != 1 || sealed != 3 {
				t.Fatalf("handshakes %d sealed %d", handshakes, sealed)
			}
			if !strings.HasPrefix(contentType, `multipart/encrypted;protocol="application/HTTP-SPNEGO-session-encrypted"`) {
				t.Fatalf("content type = %q", contentType)
			}

			// The host dropping the context answers 401; the transport authenticates again with a new AP-REQ.
			server.mu.Lock()
			server.encryption = nil
			server.mu.Unlock()
			if _, err := postThrough(t, transport, httpServer.URL+"/wsman", "after reset"); err != nil {
				t.Fatal(err)
			}
			server.mu.Lock()
			handshakes = server.handshakes
			server.mu.Unlock()
			if handshakes != 2 {
				t.Fatalf("handshakes after reset = %d", handshakes)
			}
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
			source.mu.Lock()
			closed := source.closed
			source.mu.Unlock()
			if !closed {
				t.Fatal("closing the transport did not release the Kerberos client")
			}
		})
	}
}

func TestKerberosHandshakeReportsAcceptorErrors(t *testing.T) {
	sessionKey := testAESKey(t, etypeID.AES128_CTS_HMAC_SHA1_96, 16)
	server := &kerberosTestServer{t: t, sessionKey: sessionKey, rejectWith: errorcode.KRB_AP_ERR_SKEW}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	inner := &http.Transport{}
	defer inner.CloseIdleConnections()
	transport := newKerberosSealingTransport(inner, &kerberosTestSource{sessionKey: sessionKey})
	_, err := postThrough(t, transport, httpServer.URL+"/wsman", "body")
	var classified *kerberosError
	if !errors.As(err, &classified) || classified.failure != kerberosFailureClockSkew {
		t.Fatalf("error = %v, want clock skew", err)
	}

	ticketErr := newKerberosError(kerberosFailureBadPassword, errors.New("preauth"))
	failing := newKerberosSealingTransport(inner, &kerberosTestSource{err: ticketErr})
	if _, err := postThrough(t, failing, httpServer.URL+"/wsman", "body"); !errors.Is(err, ticketErr) {
		t.Fatalf("ticket error = %v", err)
	}
}

func TestKerberosHandshakeRequiresMutualAuthentication(t *testing.T) {
	sessionKey := testAESKey(t, etypeID.AES256_CTS_HMAC_SHA1_96, 32)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()
	inner := &http.Transport{}
	defer inner.CloseIdleConnections()
	transport := newKerberosSealingTransport(inner, &kerberosTestSource{sessionKey: sessionKey})
	_, err := postThrough(t, transport, httpServer.URL+"/wsman", "body")
	var classified *kerberosError
	if !errors.As(err, &classified) || classified.failure != kerberosFailureNoMutualAuth {
		t.Fatalf("error = %v, want missing mutual authentication", err)
	}
}
