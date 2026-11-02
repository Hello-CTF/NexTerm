package remotedesktop

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type fakeResolver struct {
	mu        sync.Mutex
	assets    map[string]Asset
	passwords map[string]string
}

func (r *fakeResolver) ResolveAsset(_ context.Context, id string) (Asset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.assets[id]
	if !ok {
		return Asset{}, ipc.NewError(ipc.CodeNotFound, "资产不存在")
	}
	return asset, nil
}

func (r *fakeResolver) ResolvePassword(_ context.Context, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.passwords[id], nil
}

func (r *fakeResolver) set(asset Asset) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.assets[asset.ID] = asset
}

type fakeCerts struct {
	mu      sync.Mutex
	pinned  map[string]string
	accepts int
}

func (c *fakeCerts) Get(_ context.Context, host string, port int, keyType string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fp, ok := c.pinned[host+":"+strconv.Itoa(port)+"/"+keyType]
	return fp, ok, nil
}

func (c *fakeCerts) Accept(_ context.Context, host string, port int, keyType, fingerprint string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned[host+":"+strconv.Itoa(port)+"/"+keyType] = fingerprint
	c.accepts++
	return nil
}

type fakeAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
	fail   error
}

func (a *fakeAuditor) Audit(_ context.Context, event AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != nil {
		return a.fail
	}
	a.events = append(a.events, event)
	return nil
}

func (a *fakeAuditor) kinds() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var kinds []string
	for _, e := range a.events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

type fixture struct {
	svc      *Service
	resolver *fakeResolver
	certs    *fakeCerts
	audit    *fakeAuditor
	clock    *fakeClock
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newFixture(t testing.TB, options ...Option) *fixture {
	t.Helper()
	f := &fixture{
		resolver: &fakeResolver{assets: map[string]Asset{}, passwords: map[string]string{}},
		certs:    &fakeCerts{pinned: map[string]string{}},
		audit:    &fakeAuditor{},
		clock:    &fakeClock{t: time.Unix(1_700_000_000, 0)},
	}
	all := append([]Option{WithCertificateStore(f.certs), WithAuditor(f.audit), WithClock(f.clock.now)}, options...)
	f.svc = NewService(f.resolver, all...)
	t.Cleanup(f.svc.Close)
	return f
}

func userCtx(user, session string) context.Context {
	ctx := context.Background()
	if user != "" {
		ctx = ipc.WithUserID(ctx, user)
	}
	if session != "" {
		ctx = ipc.WithAccountSessionID(ctx, session)
	}
	return ctx
}

func TestTicketRules(t *testing.T) {
	f := newFixture(t)
	f.resolver.set(Asset{ID: "a1", OwnerID: "u1", Kind: KindVNC, Host: "10.0.0.1"})
	f.resolver.set(Asset{ID: "a2", OwnerID: "u2", Kind: KindVNC, Host: "10.0.0.2"})
	f.resolver.set(Asset{ID: "ssh1", OwnerID: "u1", Kind: "ssh", Host: "10.0.0.3"})

	prepared, err := f.svc.Prepare(userCtx("u1", "s1"), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Port != DefaultVNCPort || prepared.Protocol != KindVNC {
		t.Fatalf("prepared = %+v", prepared)
	}

	if _, err := f.svc.tickets.consume(prepared.Ticket, "u2", "s1"); err == nil {
		t.Fatal("ticket consumed by another user")
	}
	if _, err := f.svc.tickets.consume(prepared.Ticket, "u1", "other-session"); err == nil {
		t.Fatal("ticket consumed by another login session")
	}
	ticket, err := f.svc.tickets.consume(prepared.Ticket, "u1", "s1")
	if err != nil || ticket.assetID != "a1" {
		t.Fatalf("consume = %+v, %v", ticket, err)
	}
	if _, err := f.svc.tickets.consume(prepared.Ticket, "u1", "s1"); err == nil {
		t.Fatal("ticket reused")
	}

	expiring, _ := f.svc.Prepare(userCtx("u1", "s1"), "a1")
	f.clock.advance(defaultTicketTTL + time.Second)
	if _, err := f.svc.tickets.consume(expiring.Ticket, "u1", "s1"); err == nil {
		t.Fatal("expired ticket accepted")
	}

	// Another user's asset and a non-desktop asset never produce a ticket.
	if _, err := f.svc.Prepare(userCtx("u1", "s1"), "a2"); err == nil {
		t.Fatal("ticket issued for a foreign asset")
	}
	if _, err := f.svc.Prepare(userCtx("u1", "s1"), "ssh1"); err == nil {
		t.Fatal("ticket issued for an ssh asset")
	}
	if _, err := f.svc.Prepare(userCtx("u1", "s1"), "missing"); err == nil {
		t.Fatal("ticket issued for a missing asset")
	}
}

func TestServeRejectsWrongUserWithoutDialing(t *testing.T) {
	f := newFixture(t)
	dials := make(chan struct{}, 1)
	f.resolver.set(Asset{ID: "a1", OwnerID: "u1", Kind: KindVNC, Host: "127.0.0.1", Port: 1})
	prepared, err := f.svc.Prepare(userCtx("u1", "s1"), "a1")
	if err != nil {
		t.Fatal(err)
	}
	f.svc.newDialer = nil
	client, server := newMemPair(8)
	defer client.Close()
	err = f.svc.Serve(background(), server, Identity{UserID: "u2", LoginSessionID: "s1"}, prepared.Ticket)
	if err == nil {
		t.Fatal("serve accepted a ticket of another user")
	}
	select {
	case <-dials:
		t.Fatal("dialed")
	default:
	}
	for _, kind := range f.audit.kinds() {
		if kind == AuditConnect {
			t.Fatal("connect audited for a rejected ticket")
		}
	}
}

// vncNoneServer runs a minimal RFB 3.8 server with security None that echoes the first client message back
// prefixed with "echo:" after ServerInit.
func vncNoneServer(t testing.TB) (string, int) {
	return listen(t, func(conn net.Conn) {
		// Clients may hang up at any point, so short reads are not failures here.
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		_, _ = io.ReadFull(conn, make([]byte, 12))
		_, _ = conn.Write([]byte{1, rfbSecNone})
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		result(conn, true, "")
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		_, _ = conn.Write([]byte("SERVERINIT"))
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		_, _ = conn.Write(append([]byte("echo:"), buf[:n]...))
		_, _ = io.Copy(io.Discard, conn)
	})
}

func TestServeVNCPresentsNoneAndRelays(t *testing.T) {
	f := newFixture(t)
	host, port := vncNoneServer(t)
	f.resolver.set(Asset{ID: "v1", OwnerID: "u1", Kind: KindVNC, Host: host, Port: port, HasPassword: true})
	f.resolver.passwords["v1"] = "unused-by-none"
	prepared, err := f.svc.Prepare(userCtx("u1", "s1"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() {
		done <- f.svc.Serve(background(), server, Identity{UserID: "u1", LoginSessionID: "s1"}, prepared.Ticket)
	}()

	noVNC := &frameStream{fc: client}
	if got := string(mustRead(t, noVNC, 12)); got != "RFB 003.008\n" {
		t.Fatalf("banner = %q", got)
	}
	_, _ = noVNC.Write([]byte("RFB 003.008\n"))
	if got := mustRead(t, noVNC, 2); !bytes.Equal(got, []byte{1, rfbSecNone}) {
		t.Fatalf("security types = %v, want only None", got)
	}
	_, _ = noVNC.Write([]byte{rfbSecNone})
	if got := mustRead(t, noVNC, 4); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("SecurityResult = %v", got)
	}
	_, _ = noVNC.Write([]byte{1}) // ClientInit
	if got := string(mustRead(t, noVNC, 10)); got != "SERVERINIT" {
		t.Fatalf("ServerInit = %q", got)
	}
	_, _ = noVNC.Write([]byte("hello"))
	if got := string(mustRead(t, noVNC, 10)); got != "echo:hello" {
		t.Fatalf("relayed = %q", got)
	}
	client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not end after the client closed")
	}
	requireAudit(t, f.audit, AuditConnect, AuditDisconnect)
	if len(f.svc.List(userCtx("u1", ""))) != 0 {
		t.Fatal("connection still listed after disconnect")
	}
}

func TestServeVNCAuthFailureReachesNoVNCAsReason(t *testing.T) {
	f := newFixture(t)
	host, port := listen(t, func(conn net.Conn) {
		rfbServerStart(t, conn, rfbSecVNCAuth)
		mustRead(t, conn, 1)
		_, _ = conn.Write(make([]byte, 16))
		mustRead(t, conn, 16)
		result(conn, false, "denied by test")
	})
	f.resolver.set(Asset{ID: "v1", OwnerID: "", Kind: KindVNC, Host: host, Port: port, HasPassword: true})
	f.resolver.passwords["v1"] = "pw"
	prepared, _ := f.svc.Prepare(background(), "v1")
	client, server := newMemPair(8)
	go func() { _ = f.svc.Serve(background(), server, Identity{}, prepared.Ticket) }()
	noVNC := &frameStream{fc: client}
	mustRead(t, noVNC, 12)
	_, _ = noVNC.Write([]byte("RFB 003.008\n"))
	if zero := mustRead(t, noVNC, 1); zero[0] != 0 {
		t.Fatalf("expected zero security types, got %d", zero[0])
	}
	length := binary.BigEndian.Uint32(mustRead(t, noVNC, 4))
	if reason := string(mustRead(t, noVNC, int(length))); !strings.Contains(reason, "denied by test") {
		t.Fatalf("reason = %q", reason)
	}
	client.Close()
}

func TestServeVNCCertificateTrustOnFirstUse(t *testing.T) {
	f := newFixture(t)
	cert, der := newTestCertificate(t, "tofu")
	host, port := listen(t, veNCryptServer(t, cert, []uint32{vencryptX509Plain}, func(string, string) bool { return true }))
	f.resolver.set(Asset{ID: "v1", Kind: KindVNC, Host: host, Port: port, Username: "u", HasPassword: true})
	f.resolver.passwords["v1"] = "pw"

	attempt := func() (string, error) {
		prepared, err := f.svc.Prepare(background(), "v1")
		if err != nil {
			t.Fatal(err)
		}
		client, server := newMemPair(8)
		errs := make(chan error, 1)
		go func() { errs <- f.svc.Serve(background(), server, Identity{}, prepared.Ticket) }()
		noVNC := &frameStream{fc: client}
		mustRead(t, noVNC, 12)
		_, _ = noVNC.Write([]byte("RFB 003.008\n"))
		first := mustRead(t, noVNC, 1)
		if first[0] == 0 {
			length := binary.BigEndian.Uint32(mustRead(t, noVNC, 4))
			reason := string(mustRead(t, noVNC, int(length)))
			client.Close()
			return reason, <-errs
		}
		client.Close()
		return "", <-errs
	}

	reason, err := attempt()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("first connection err = %v", err)
	}
	if !strings.Contains(reason, fingerprintOf(der)) {
		t.Fatalf("noVNC reason %q does not show the fingerprint", reason)
	}
	status, err := f.svc.CertificateStatus(background(), "v1")
	if err != nil || status.Status != CertNew || status.Fingerprint != fingerprintOf(der) {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if f.certs.accepts != 0 {
		t.Fatal("certificate pinned without confirmation")
	}
	if _, err := f.svc.TrustCertificate(background(), "v1", "SHA256:other"); err == nil {
		t.Fatal("trusted a fingerprint that was never observed")
	}
	if _, err := f.svc.TrustCertificate(background(), "v1", status.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt(); err != nil {
		t.Fatalf("pinned connection failed: %v", err)
	}
	pinned, _ := f.svc.CertificateStatus(background(), "v1")
	if pinned.Status != CertPinned {
		t.Fatalf("status after pin = %+v", pinned)
	}
}

// rdpServer is a fake RDP server: X.224 confirm selecting protocol, then TLS, then "pong:"+data.
func rdpServer(t testing.TB, cert func() tls.Certificate, selected uint32, gotRequest chan<- []byte) (string, int) {
	return listen(t, func(conn net.Conn) {
		head := mustRead(t, conn, 4)
		rest := mustRead(t, conn, int(binary.BigEndian.Uint16(head[2:]))-4)
		if gotRequest != nil {
			gotRequest <- append(head, rest...)
		}
		confirm := []byte{3, 0, 0, 19, 14, x224Confirm, 0, 0, 0x12, 0x34, 0, rdpNegResponse, 0, 8, 0, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(confirm[15:], selected)
		_, _ = conn.Write(confirm)
		c := cert()
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		buf := make([]byte, 256)
		for {
			n, err := tlsConn.Read(buf)
			if err != nil {
				return
			}
			_, _ = tlsConn.Write(append([]byte("pong:"), buf[:n]...))
		}
	})
}

var x224Request = []byte{3, 0, 0, 11, 6, 0xe0, 0, 0, 0, 0, 0}

func rdpClientRequest(ticket, destination string) []byte {
	return (&rdCleanPathPDU{
		Version: rdCleanPathVersion1, Destination: destination, HasDestination: true,
		ProxyAuth: ticket, HasProxyAuth: true, X224: x224Request, HasX224: true,
	}).marshal()
}

func TestServeRDPRelaysWithServerCertificateAndIgnoresClientDestination(t *testing.T) {
	f := newFixture(t)
	cert, der := newTestCertificate(t, "rdp")
	requests := make(chan []byte, 1)
	host, port := rdpServer(t, func() tls.Certificate { return cert }, protoHybrid, requests)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: host, Port: port})
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeRDP] = fingerprintOf(der)

	prepared, err := f.svc.Prepare(userCtx("u1", "s1"), "r1")
	if err != nil {
		t.Fatal(err)
	}
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1", LoginSessionID: "s1"}, "") }()

	// IronRDP's first message carries the ticket as proxy_auth and a hostile destination.
	if err := client.WriteFrame(rdpClientRequest(prepared.Ticket, "169.254.169.254:80")); err != nil {
		t.Fatal(err)
	}
	responseFrame, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	response, err := parseRDCleanPath(responseFrame)
	if err != nil || response.Error != nil {
		t.Fatalf("response = %+v, %v", response, err)
	}
	if got := <-requests; !bytes.Equal(got, x224Request) {
		t.Fatalf("server received %x, want the client's X.224 request", got)
	}
	if len(response.CertChain) == 0 || !bytes.Equal(response.CertChain[0], der) {
		t.Fatal("client was not shown the certificate of the TLS session actually used (CredSSP channel binding)")
	}
	if response.ServerAddr != net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("server_addr = %q", response.ServerAddr)
	}
	if len(response.X224) != 19 || response.X224[5] != x224Confirm {
		t.Fatalf("x224 = %x", response.X224)
	}
	if err := client.WriteFrame([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if reply, _ := client.ReadFrame(); string(reply) != "pong:ping" {
		t.Fatalf("relayed = %q", reply)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve = %v", err)
	}
}

func TestServeRDPCertificateTrustFlow(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	cert, der := newTestCertificate(t, "rdp-1")
	host, port := rdpServer(t, func() tls.Certificate { mu.Lock(); defer mu.Unlock(); return cert }, protoHybrid, nil)
	f.resolver.set(Asset{ID: "r1", Kind: KindRDP, Host: host, Port: port})

	connect := func() (*rdCleanPathPDU, error) {
		prepared, err := f.svc.Prepare(background(), "r1")
		if err != nil {
			t.Fatal(err)
		}
		client, server := newMemPair(8)
		errs := make(chan error, 1)
		go func() { errs <- f.svc.Serve(background(), server, Identity{}, "") }()
		_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
		frame, _ := client.ReadFrame()
		pdu, perr := parseRDCleanPath(frame)
		if perr != nil {
			t.Fatal(perr)
		}
		client.Close()
		return pdu, <-errs
	}

	pdu, err := connect()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNeedsConfirm || pdu.Error == nil || pdu.Error.HTTPStatus != 403 {
		t.Fatalf("new certificate: pdu=%+v err=%v", pdu, err)
	}
	status, _ := f.svc.CertificateStatus(background(), "r1")
	if status.Status != CertNew || status.Fingerprint != fingerprintOf(der) {
		t.Fatalf("status = %+v", status)
	}
	if _, err := f.svc.TrustCertificate(background(), "r1", status.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if pdu, err := connect(); err != nil || pdu.Error != nil {
		t.Fatalf("pinned certificate: pdu=%+v err=%v", pdu, err)
	}

	// The server now presents another certificate: blocked until explicitly re-confirmed, never "ignored".
	mu.Lock()
	cert, der2 := newTestCertificate(t, "rdp-2")
	mu.Unlock()
	pdu, err = connect()
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNeedsConfirm || pdu.Error == nil {
		t.Fatalf("changed certificate: pdu=%+v err=%v", pdu, err)
	}
	changed, _ := f.svc.CertificateStatus(background(), "r1")
	if changed.Status != CertChanged || changed.PreviousFingerprint != fingerprintOf(der) || changed.Fingerprint != fingerprintOf(der2) {
		t.Fatalf("changed status = %+v", changed)
	}
	if _, err := connect(); err == nil {
		t.Fatal("changed certificate accepted without re-confirmation")
	}
	if _, err := f.svc.TrustCertificate(background(), "r1", changed.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := connect(); err != nil {
		t.Fatalf("after re-confirm: %v", err)
	}
	foundTrust := 0
	for _, kind := range f.audit.kinds() {
		if kind == AuditCertTrust {
			foundTrust++
		}
	}
	if foundTrust != 2 {
		t.Fatalf("cert trust audits = %d, want 2", foundTrust)
	}
}

func TestServeRDPRejectsServerWithoutTLS(t *testing.T) {
	f := newFixture(t)
	cert, _ := newTestCertificate(t, "rdp")
	host, port := rdpServer(t, func() tls.Certificate { return cert }, 0, nil)
	f.resolver.set(Asset{ID: "r1", Kind: KindRDP, Host: host, Port: port})
	prepared, _ := f.svc.Prepare(background(), "r1")
	client, server := newMemPair(8)
	errs := make(chan error, 1)
	go func() { errs <- f.svc.Serve(background(), server, Identity{}, "") }()
	_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
	frame, _ := client.ReadFrame()
	pdu, err := parseRDCleanPath(frame)
	if err != nil || pdu.Error == nil || pdu.Error.Code != rdCleanPathNegotiationError {
		t.Fatalf("pdu = %+v, %v", pdu, err)
	}
	if err := <-errs; err == nil {
		t.Fatal("expected an error for standard RDP security")
	}
}

func TestDialPathSelection(t *testing.T) {
	host, port := vncNoneServer(t)
	var mu sync.Mutex
	var viaCalls []string
	var addresses []string
	factory := func(_ context.Context, via string) (DialContextFunc, error) {
		mu.Lock()
		viaCalls = append(viaCalls, via)
		mu.Unlock()
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			addresses = append(addresses, address)
			mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}, nil
	}
	f := newFixture(t, WithDialerFactory(factory))
	f.resolver.set(Asset{ID: "direct", Kind: KindVNC, Host: host, Port: port})
	f.resolver.set(Asset{ID: "jumped", Kind: KindVNC, Host: host, Port: port, OptionsJSON: `{"viaAssetId":"jump-1"}`})

	run := func(id string) {
		prepared, err := f.svc.Prepare(background(), id)
		if err != nil {
			t.Fatal(err)
		}
		client, server := newMemPair(8)
		done := make(chan error, 1)
		go func() { done <- f.svc.Serve(background(), server, Identity{}, prepared.Ticket) }()
		noVNC := &frameStream{fc: client}
		mustRead(t, noVNC, 12)
		_, _ = noVNC.Write([]byte("RFB 003.008\n"))
		mustRead(t, noVNC, 2)
		client.Close()
		<-done
	}
	run("direct")
	mu.Lock()
	if len(viaCalls) != 0 {
		t.Fatalf("direct asset used the jump factory: %v", viaCalls)
	}
	mu.Unlock()
	run("jumped")
	mu.Lock()
	defer mu.Unlock()
	if len(viaCalls) != 1 || viaCalls[0] != "jump-1" {
		t.Fatalf("via calls = %v", viaCalls)
	}
	if len(addresses) != 1 || addresses[0] != net.JoinHostPort(host, strconv.Itoa(port)) {
		t.Fatalf("jump dial addresses = %v", addresses)
	}
}

func TestDialErrorReachesRDPClient(t *testing.T) {
	f := newFixture(t, WithDialerFactory(func(context.Context, string) (DialContextFunc, error) {
		return nil, ipc.NewError(ipc.CodeDisconnected, "SSH 跳板资产没有已连接会话，请先连接该主机")
	}))
	f.resolver.set(Asset{ID: "r1", Kind: KindRDP, Host: "10.0.0.9", OptionsJSON: `{"viaAssetId":"jump-1"}`})
	prepared, _ := f.svc.Prepare(background(), "r1")
	client, server := newMemPair(8)
	err := f.svc.Serve(background(), server, Identity{}, prepared.Ticket)
	_ = client
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeDisconnected {
		t.Fatalf("err = %v", err)
	}
	requireAudit(t, f.audit, AuditFailure)
}

func TestReleaseSecret(t *testing.T) {
	f := newFixture(t)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: "h", Username: "admin", HasPassword: true, OptionsJSON: `{"domain":"CORP"}`})
	f.resolver.set(Asset{ID: "r2", OwnerID: "u1", Kind: KindRDP, Host: "h", HasPassword: true, OptionsJSON: `{"promptPassword":true}`})
	f.resolver.set(Asset{ID: "v1", OwnerID: "u1", Kind: KindVNC, Host: "h", HasPassword: true})
	f.resolver.passwords["r1"], f.resolver.passwords["r2"], f.resolver.passwords["v1"] = "S3cret!", "S3cret!", "S3cret!"
	ctx := userCtx("u1", "s1")

	prepared, err := f.svc.Prepare(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.HasPassword || prepared.PromptPassword || prepared.Domain != "CORP" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if _, err := f.svc.ReleaseSecret(userCtx("u2", "s1"), prepared.Ticket); err == nil {
		t.Fatal("another user released the secret")
	}
	secret, err := f.svc.ReleaseSecret(ctx, prepared.Ticket)
	if err != nil || secret.Password != "S3cret!" || secret.Username != "admin" || secret.Domain != "CORP" {
		t.Fatalf("release = %+v, %v", secret, err)
	}
	if _, err := f.svc.ReleaseSecret(ctx, prepared.Ticket); err == nil {
		t.Fatal("secret released twice for one ticket")
	}
	var released []AuditEvent
	for _, event := range f.audit.events {
		if event.Kind == AuditSecretRelease {
			released = append(released, event)
		}
		for _, v := range event.Payload {
			if s, ok := v.(string); ok && strings.Contains(s, "S3cret!") {
				t.Fatal("password present in an audit payload")
			}
		}
	}
	if len(released) != 1 || released[0].AssetID != "r1" {
		t.Fatalf("release audits = %+v", released)
	}
	if text := (RemoteDesktopSecretResult{Password: "S3cret!"}); strings.Contains(text.String()+" "+strings.TrimSpace(sprint(text)), "S3cret!") {
		t.Fatal("secret formats with its password")
	}

	prompt, _ := f.svc.Prepare(ctx, "r2")
	if !prompt.PromptPassword || prompt.HasPassword {
		t.Fatalf("prompt asset prepared = %+v", prompt)
	}
	if _, err := f.svc.ReleaseSecret(ctx, prompt.Ticket); err == nil {
		t.Fatal("promptPassword asset released a stored secret")
	}
	vnc, _ := f.svc.Prepare(ctx, "v1")
	if _, err := f.svc.ReleaseSecret(ctx, vnc.Ticket); err == nil {
		t.Fatal("a VNC ticket released a secret")
	}
	expiring, _ := f.svc.Prepare(ctx, "r1")
	f.clock.advance(defaultTicketTTL + time.Second)
	if _, err := f.svc.ReleaseSecret(ctx, expiring.Ticket); err == nil {
		t.Fatal("expired ticket released a secret")
	}
	if _, err := f.svc.ReleaseSecret(ctx, "not-a-ticket"); err == nil {
		t.Fatal("unknown ticket released a secret")
	}
}

func TestReleaseSecretFailsClosedWhenAuditFails(t *testing.T) {
	f := newFixture(t)
	f.resolver.set(Asset{ID: "r1", Kind: KindRDP, Host: "h", HasPassword: true})
	f.resolver.passwords["r1"] = "pw"
	prepared, _ := f.svc.Prepare(background(), "r1")
	f.audit.fail = errors.New("disk full")
	secret, err := f.svc.ReleaseSecret(background(), prepared.Ticket)
	if err == nil || secret.Password != "" {
		t.Fatalf("secret released without an audit record: %+v, %v", secret, err)
	}
}

func TestCloseAccountSessionEndsConnectionsAndTickets(t *testing.T) {
	f := newFixture(t)
	cert, der := newTestCertificate(t, "rdp")
	host, port := rdpServer(t, func() tls.Certificate { return cert }, protoTLS, nil)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: host, Port: port})
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeRDP] = fingerprintOf(der)
	ctx := userCtx("u1", "s1")

	spare, _ := f.svc.Prepare(ctx, "r1")
	prepared, _ := f.svc.Prepare(ctx, "r1")
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1", LoginSessionID: "s1"}, "") }()
	_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
	if _, err := client.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	if got := f.svc.List(userCtx("u1", "")); len(got) != 1 || got[0].AssetID != "r1" {
		t.Fatalf("list = %+v", got)
	}
	if got := f.svc.List(userCtx("u2", "")); len(got) != 0 {
		t.Fatalf("another user lists %+v", got)
	}
	if err := f.svc.CloseAccountSession("s1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("live connection survived the end of its account session")
	}
	if _, err := f.svc.tickets.consume(spare.Ticket, "u1", "s1"); err == nil {
		t.Fatal("ticket survived the end of its account session")
	}
	if _, err := f.svc.Prepare(ctx, "r1"); err == nil {
		t.Fatal("revoked login session prepared a new ticket")
	}
}

func TestCloseConnectionOwnerCheck(t *testing.T) {
	f := newFixture(t)
	cert, der := newTestCertificate(t, "rdp")
	host, port := rdpServer(t, func() tls.Certificate { return cert }, protoTLS, nil)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: host, Port: port})
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeRDP] = fingerprintOf(der)
	prepared, _ := f.svc.Prepare(userCtx("u1", ""), "r1")
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1"}, "") }()
	_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
	_, _ = client.ReadFrame()
	id := f.svc.List(userCtx("u1", ""))[0].ID
	if err := f.svc.CloseConnection(userCtx("u2", ""), id); err == nil {
		t.Fatal("another user closed the connection")
	}
	if err := f.svc.CloseConnection(userCtx("u1", ""), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close command did not end the connection")
	}
}

func TestRelayBackpressureDeliversEverythingInOrder(t *testing.T) {
	const chunks, size = 64, relayChunk
	payload := make([]byte, chunks*size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	left, right := net.Pipe()
	client, server := newMemPair(0) // unbuffered: every write waits for the reader
	// Every upstream byte is read by the relay before Write returns, and the
	// relay forwards frames in order, so closing after Write cannot cut data.
	written := make(chan struct{})
	go func() {
		_, _ = right.Write(payload)
		close(written)
		_ = right.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- relayFrames(server, left, nil) }()
	var got []byte
	for len(got) < len(payload) {
		frame, err := client.ReadFrame()
		if err != nil {
			t.Fatalf("read after %d bytes: %v", len(got), err)
		}
		got = append(got, frame...)
		if len(got) == len(frame) {
			// The consumer is blocked until now: the relay can hold at most one
			// chunk in flight, so the upstream write cannot have completed.
			select {
			case <-written:
				t.Fatal("upstream write finished while the consumer had read one chunk: no backpressure")
			default:
			}
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("relayed bytes differ: data was dropped or reordered under backpressure")
	}
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream write never completed")
	}
	<-done
}

func TestRelayClosesBothSides(t *testing.T) {
	left, right := net.Pipe()
	client, server := newMemPair(1)
	done := make(chan error, 1)
	go func() { done <- relayFrames(server, left, nil) }()
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not end when the client closed")
	}
	if _, err := right.Write([]byte("x")); err == nil {
		t.Fatal("upstream stayed open after the client closed")
	}
}

func TestServeFirstFrameVariants(t *testing.T) {
	f := newFixture(t)
	f.resolver.set(Asset{ID: "v1", Kind: KindVNC, Host: "127.0.0.1", Port: 1})
	client, server := newMemPair(8)
	_ = client.WriteFrame([]byte("garbage"))
	if err := f.svc.Serve(background(), server, Identity{}, ""); err == nil {
		t.Fatal("garbage first frame accepted")
	}
	client2, server2 := newMemPair(8)
	_ = client2.WriteFrame([]byte(TicketPreamble + "no-such-ticket"))
	if err := f.svc.Serve(background(), server2, Identity{}, ""); err == nil {
		t.Fatal("unknown ticket accepted")
	}
}

func requireAudit(t testing.TB, a *fakeAuditor, want ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, kind := range a.kinds() {
		have[kind] = true
	}
	for _, kind := range want {
		if !have[kind] {
			t.Errorf("audit log lacks %s (have %v)", kind, a.kinds())
		}
	}
}

func TestTicketCapIsPerUser(t *testing.T) {
	f := newFixture(t)
	f.resolver.set(Asset{ID: "a1", OwnerID: "u1", Kind: KindVNC, Host: "h"})
	f.resolver.set(Asset{ID: "a2", OwnerID: "u2", Kind: KindVNC, Host: "h"})
	for i := 0; i < maxUserTickets; i++ {
		if _, err := f.svc.Prepare(userCtx("u1", ""), "a1"); err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
	}
	if _, err := f.svc.Prepare(userCtx("u1", ""), "a1"); err == nil {
		t.Fatal("user exceeded the per-user ticket cap")
	}
	if _, err := f.svc.Prepare(userCtx("u2", ""), "a2"); err != nil {
		t.Fatalf("another user was blocked by u1: %v", err)
	}
}

func TestRevokedSessionsAgeOut(t *testing.T) {
	f := newFixture(t)
	f.resolver.set(Asset{ID: "a1", OwnerID: "u1", Kind: KindVNC, Host: "h"})
	_ = f.svc.CloseAccountSession("s-old")
	if _, err := f.svc.Prepare(userCtx("u1", "s-old"), "a1"); err == nil {
		t.Fatal("revoked session prepared a ticket")
	}
	f.clock.advance(revokedTTL + time.Minute)
	if _, err := f.svc.Prepare(userCtx("u1", "s-old"), "a1"); err != nil {
		t.Fatalf("aged-out entry still blocks: %v", err)
	}
	f.svc.mu.Lock()
	n := len(f.svc.revoked)
	f.svc.mu.Unlock()
	if n != 0 {
		t.Fatalf("revoked entries = %d", n)
	}
}

func TestCloseAccountUserKeepsExceptedSession(t *testing.T) {
	f := newFixture(t)
	cert, der := newTestCertificate(t, "rdp")
	host, port := rdpServer(t, func() tls.Certificate { return cert }, protoTLS, nil)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: host, Port: port})
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeRDP] = fingerprintOf(der)
	open := func(session string) (*memConn, chan error) {
		prepared, err := f.svc.Prepare(userCtx("u1", session), "r1")
		if err != nil {
			t.Fatal(err)
		}
		client, server := newMemPair(8)
		done := make(chan error, 1)
		go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1", LoginSessionID: session}, "") }()
		_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
		if _, err := client.ReadFrame(); err != nil {
			t.Fatal(err)
		}
		return client, done
	}
	keepClient, keepDone := open("current")
	defer keepClient.Close()
	_, dropDone := open("other")
	leftover, _ := f.svc.Prepare(userCtx("u1", "other"), "r1")
	f.svc.CloseAccountUser("u1", "current")
	select {
	case <-dropDone:
	case <-time.After(5 * time.Second):
		t.Fatal("other session's connection survived")
	}
	select {
	case <-keepDone:
		t.Fatal("the excepted session's connection was closed")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := f.svc.tickets.consume(leftover.Ticket, "u1", "other"); err == nil {
		t.Fatal("ticket of a closed session survived")
	}
}

func TestLiveConnectionEndsWhenLoginSessionExpires(t *testing.T) {
	f := newFixture(t, WithRecheckInterval(10*time.Millisecond))
	cert, der := newTestCertificate(t, "rdp")
	host, port := rdpServer(t, func() tls.Certificate { return cert }, protoTLS, nil)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: host, Port: port})
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeRDP] = fingerprintOf(der)

	var mu sync.Mutex
	var failure error
	ctx := ipc.WithAccountSessionCheck(userCtx("u1", "s1"), func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		return failure
	})
	prepared, err := f.svc.Prepare(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1", LoginSessionID: "s1"}, "") }()
	_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
	_, _ = client.ReadFrame()

	// A transient failure keeps the connection; a confirmed invalid session ends it.
	mu.Lock()
	failure = errors.New("database busy")
	mu.Unlock()
	select {
	case <-done:
		t.Fatal("transient check failure ended the connection")
	case <-time.After(100 * time.Millisecond):
	}
	mu.Lock()
	failure = ipc.NewError(ipc.CodeForbidden, "登录会话已失效")
	mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection survived the expiry of its login session")
	}
}

func TestVNCSetPasswordFlow(t *testing.T) {
	f := newFixture(t)
	host, port := listen(t, func(conn net.Conn) {
		rfbServerStart(t, conn, rfbSecVNCAuth)
		mustRead(t, conn, 1)
		challenge := []byte("0123456789abcdef")
		_, _ = conn.Write(challenge)
		response := mustRead(t, conn, 16)
		want, _ := vncChallengeResponse(challenge, "typed-pw")
		result(conn, bytes.Equal(response, want), "wrong typed password")
		_, _ = io.ReadFull(conn, make([]byte, 1))
	})
	f.resolver.set(Asset{ID: "v1", OwnerID: "u1", Kind: KindVNC, Host: host, Port: port, HasPassword: true, OptionsJSON: `{"promptPassword":true}`})
	f.resolver.passwords["v1"] = "stored-pw-must-not-be-used"
	f.resolver.set(Asset{ID: "v2", OwnerID: "u1", Kind: KindVNC, Host: host, Port: port})
	ctx := userCtx("u1", "")

	plain, _ := f.svc.Prepare(ctx, "v2")
	if err := f.svc.SetPassword(ctx, plain.Ticket, "x"); err == nil {
		t.Fatal("password bound to a non-prompt asset")
	}
	prepared, _ := f.svc.Prepare(ctx, "v1")
	if !prepared.PromptPassword || prepared.HasPassword {
		t.Fatalf("prepared = %+v", prepared)
	}
	if err := f.svc.SetPassword(userCtx("u2", ""), prepared.Ticket, "x"); err == nil {
		t.Fatal("another user bound a password")
	}
	if err := f.svc.SetPassword(ctx, prepared.Ticket, ""); err == nil {
		t.Fatal("empty password accepted")
	}
	if err := f.svc.SetPassword(ctx, prepared.Ticket, "typed-pw"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetPassword(ctx, prepared.Ticket, "again"); err == nil {
		t.Fatal("password rebound")
	}
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1"}, prepared.Ticket) }()
	noVNC := &frameStream{fc: client}
	mustRead(t, noVNC, 12)
	_, _ = noVNC.Write([]byte("RFB 003.008\n"))
	if got := mustRead(t, noVNC, 2); !bytes.Equal(got, []byte{1, rfbSecNone}) {
		t.Fatalf("security types = %v", got)
	}
	_, _ = noVNC.Write([]byte{rfbSecNone})
	if got := mustRead(t, noVNC, 4); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Fatalf("SecurityResult = %v (typed password was not used)", got)
	}
	client.Close()
	<-done

	// Without a typed password the viewer gets a Chinese prompt reason and nothing is dialed.
	missing, _ := f.svc.Prepare(ctx, "v1")
	client2, server2 := newMemPair(8)
	go func() { _ = f.svc.Serve(background(), server2, Identity{UserID: "u1"}, missing.Ticket) }()
	noVNC2 := &frameStream{fc: client2}
	mustRead(t, noVNC2, 12)
	_, _ = noVNC2.Write([]byte("RFB 003.008\n"))
	if zero := mustRead(t, noVNC2, 1); zero[0] != 0 {
		t.Fatalf("expected a rejection, got %d", zero[0])
	}
	client2.Close()
}

func TestFirstFrameAndHandshakeTimeouts(t *testing.T) {
	f := newFixture(t, WithHandshakeTimeout(100*time.Millisecond))
	_, server := newMemPair(8)
	start := time.Now()
	if err := f.svc.Serve(background(), server, Identity{}, ""); err == nil {
		t.Fatal("silent client kept the connection")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("first frame read was not bounded")
	}

	host, port := listen(t, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) }) // never sends a banner
	f.resolver.set(Asset{ID: "v1", Kind: KindVNC, Host: host, Port: port})
	prepared, _ := f.svc.Prepare(background(), "v1")
	client, server2 := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server2, Identity{}, prepared.Ticket) }()
	noVNC := &frameStream{fc: client}
	mustRead(t, noVNC, 12)
	_, _ = noVNC.Write([]byte("RFB 003.008\n"))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled handshake succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream handshake has no deadline")
	}
}

func TestViewerNeverSeesCauseText(t *testing.T) {
	if got := userMessage(userErrWrap("VNC TLS 握手失败", errors.New("dial tcp 10.1.2.3:5900: secret detail"))); got != "VNC TLS 握手失败" {
		t.Fatalf("message = %q", got)
	}
	f := newFixture(t)
	f.resolver.set(Asset{ID: "v1", Kind: KindVNC, Host: "127.0.0.1", Port: 1})
	prepared, _ := f.svc.Prepare(background(), "v1")
	client, server := newMemPair(8)
	go func() { _ = f.svc.Serve(background(), server, Identity{}, prepared.Ticket) }()
	noVNC := &frameStream{fc: client}
	mustRead(t, noVNC, 12)
	_, _ = noVNC.Write([]byte("RFB 003.008\n"))
	if zero := mustRead(t, noVNC, 1); zero[0] != 0 {
		t.Fatalf("expected rejection, got %d", zero[0])
	}
	length := binary.BigEndian.Uint32(mustRead(t, noVNC, 4))
	reason := string(mustRead(t, noVNC, int(length)))
	if strings.Contains(reason, "127.0.0.1") || strings.Contains(reason, "refused") {
		t.Fatalf("viewer saw connection details: %q", reason)
	}
}

func TestVNCPinnedHostRefusesPlainSecurity(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	credentialsRead := false
	handlerDone := make(chan struct{}, 2)
	host, port := listen(t, func(conn net.Conn) {
		defer func() { handlerDone <- struct{}{} }()
		_, _ = conn.Write([]byte("RFB 003.008\n"))
		_, _ = io.ReadFull(conn, make([]byte, 12))
		_, _ = conn.Write([]byte{1, rfbSecVNCAuth})
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			return
		}
		_, _ = conn.Write(make([]byte, 16))
		if _, err := io.ReadFull(conn, make([]byte, 16)); err == nil {
			mu.Lock()
			credentialsRead = true
			mu.Unlock()
			result(conn, true, "")
		}
	})
	f.resolver.set(Asset{ID: "v1", Kind: KindVNC, Host: host, Port: port, HasPassword: true})
	f.resolver.passwords["v1"] = "pw"
	connect := func() string {
		prepared, _ := f.svc.Prepare(background(), "v1")
		client, server := newMemPair(8)
		go func() { _ = f.svc.Serve(background(), server, Identity{}, prepared.Ticket) }()
		noVNC := &frameStream{fc: client}
		mustRead(t, noVNC, 12)
		_, _ = noVNC.Write([]byte("RFB 003.008\n"))
		first := mustRead(t, noVNC, 1)
		defer client.Close()
		if first[0] != 0 {
			return ""
		}
		length := binary.BigEndian.Uint32(mustRead(t, noVNC, 4))
		return string(mustRead(t, noVNC, int(length)))
	}
	// No pin yet: a default TigerVNC style VncAuth server keeps working.
	if reason := connect(); reason != "" {
		t.Fatalf("unpinned host rejected: %q", reason)
	}
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeVNC] = "SHA256:pinned"
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first upstream handler did not finish")
	}
	mu.Lock()
	credentialsRead = false
	mu.Unlock()
	if reason := connect(); !strings.Contains(reason, "不再提供 TLS") {
		t.Fatalf("pinned host reason = %q", reason)
	}
	// The handler returns once the service drops the upstream connection, so
	// by then it would already have read credentials if any had been sent.
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pinned upstream connection was not dropped")
	}
	mu.Lock()
	defer mu.Unlock()
	if credentialsRead {
		t.Fatal("credentials were sent to a pinned host that stopped offering TLS")
	}
}

func TestCloseAccountUserWithNothingSparedClosesConnectionsWithoutLoginSession(t *testing.T) {
	f := newFixture(t)
	cert, der := newTestCertificate(t, "rdp")
	host, port := rdpServer(t, func() tls.Certificate { return cert }, protoTLS, nil)
	f.resolver.set(Asset{ID: "r1", OwnerID: "u1", Kind: KindRDP, Host: host, Port: port})
	f.certs.pinned[host+":"+strconv.Itoa(port)+"/"+certKeyTypeRDP] = fingerprintOf(der)
	prepared, err := f.svc.Prepare(userCtx("u1", ""), "r1")
	if err != nil {
		t.Fatal(err)
	}
	client, server := newMemPair(8)
	done := make(chan error, 1)
	go func() { done <- f.svc.Serve(background(), server, Identity{UserID: "u1"}, "") }()
	_ = client.WriteFrame(rdpClientRequest(prepared.Ticket, ""))
	if _, err := client.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	leftover, _ := f.svc.Prepare(userCtx("u1", ""), "r1")
	f.svc.CloseAccountUser("u1", "")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection without a login session survived a full revocation")
	}
	if _, err := f.svc.tickets.consume(leftover.Ticket, "u1", ""); err == nil {
		t.Fatal("ticket without a login session survived a full revocation")
	}
	client.Close()
}
