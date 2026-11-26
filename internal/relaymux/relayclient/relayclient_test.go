package relayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/relaymux"
)

// fakeRelay serves /relay/config and the websocket carrier: a Hello is accepted for the current token, and
// each stream echoes after an accepting reply, or is refused with refusal when set.
type fakeRelay struct {
	server  *httptest.Server
	config  atomic.Value // string body; "" answers 404
	token   atomic.Value // string
	configs atomic.Int32
	hellos  atomic.Int32

	mu      sync.Mutex
	refusal *relaymux.Reply
	links   []*relaymux.Link
}

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	relay := &fakeRelay{}
	relay.config.Store(`{"enabled":true,"version":1,"ws_path":"/relay/ws","quic":[{"host":"relay.example.com","port":3478}],"alpn":"nexterm-relay/1","spki_sha256":"pin"}`)
	relay.token.Store("token-1")
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+relaymux.ConfigPath, func(w http.ResponseWriter, r *http.Request) {
		relay.configs.Add(1)
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value != relay.token.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body := relay.config.Load().(string)
		if body == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("GET "+relaymux.WSPath, func(w http.ResponseWriter, r *http.Request) {
		session, err := relaymux.AcceptWS(w, r)
		if err != nil {
			return
		}
		pending, err := relaymux.AcceptHandshake(context.Background(), session)
		if err != nil {
			return
		}
		relay.hellos.Add(1)
		if pending.Hello.Token != relay.token.Load().(string) {
			pending.Refuse(relaymux.Reply{Status: http.StatusUnauthorized, Code: "forbidden", Message: "relogin"})
			return
		}
		link, err := pending.Accept(relaymux.Reply{})
		if err != nil {
			return
		}
		relay.mu.Lock()
		relay.links = append(relay.links, link)
		relay.mu.Unlock()
		_ = link.ServeStreams(context.Background(), func(_ context.Context, incoming *relaymux.IncomingStream) {
			relay.mu.Lock()
			refusal := relay.refusal
			relay.mu.Unlock()
			if refusal != nil {
				incoming.Refuse(*refusal)
				return
			}
			conn, err := incoming.Accept(incoming.Header.Params)
			if err != nil {
				return
			}
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
		})
	})
	relay.server = httptest.NewServer(mux)
	t.Cleanup(relay.server.Close)
	return relay
}

func (r *fakeRelay) session() Session {
	return Session{BaseURL: r.server.URL, Transport: http.DefaultTransport, Token: r.token.Load().(string)}
}

func (r *fakeRelay) goaway(reply relaymux.Reply) {
	r.mu.Lock()
	links := r.links
	r.links = nil
	r.mu.Unlock()
	for _, link := range links {
		link.Goaway(reply)
	}
}

type legacyCounter struct{ calls atomic.Int32 }

func (l *legacyCounter) dial(context.Context) (net.Conn, bool, error) {
	l.calls.Add(1)
	return nil, false, errors.New("legacy bridge")
}

func newTestDialer(now func() time.Time) *Dialer {
	return New(Options{Now: now, Backoff: relaymux.NewQUICBackoff(time.Minute, nil)})
}

func echo(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo = %q, %v", buffer, err)
	}
}

func TestDialDeviceUsesTheRelayLink(t *testing.T) {
	relay := newFakeRelay(t)
	dialer := newTestDialer(nil)
	defer dialer.Close()
	legacy := &legacyCounter{}
	for range 3 {
		conn, stream, err := dialer.DialDevice(context.Background(), relay.session(), "dev", "terminal", true, false, legacy.dial)
		if err != nil {
			t.Fatal(err)
		}
		if !stream {
			t.Fatal("the reply's stream param was not reported")
		}
		echo(t, conn)
		_ = conn.Close()
	}
	if legacy.calls.Load() != 0 || relay.hellos.Load() != 1 {
		t.Fatalf("legacy dials %d, hellos %d; want 0 and one shared link", legacy.calls.Load(), relay.hellos.Load())
	}
	if kind := dialer.Kind(relay.server.URL); kind != relaymux.KindWS {
		t.Fatalf("kind = %q", kind)
	}
}

// An old server (404) and a disabled relay send operations to the bridge, and the answer is remembered so later
// operations do not ask again until the period ends.
func TestDialDeviceFallsBackWhenTheRelayIsNotOffered(t *testing.T) {
	for _, body := range []string{"", `{"enabled":false,"version":1}`, `{"enabled":true,"version":99}`} {
		relay := newFakeRelay(t)
		relay.config.Store(body)
		var clock atomic.Int64
		now := func() time.Time { return time.Unix(clock.Load(), 0) }
		dialer := newTestDialer(now)
		legacy := &legacyCounter{}
		for range 3 {
			if _, _, err := dialer.DialDevice(context.Background(), relay.session(), "dev", "", true, false, legacy.dial); err == nil || err.Error() != "legacy bridge" {
				t.Fatalf("config %q: dial = %v, want the legacy bridge", body, err)
			}
		}
		if legacy.calls.Load() != 3 || relay.configs.Load() != 1 {
			t.Fatalf("config %q: legacy %d, config requests %d; want 3 and 1", body, legacy.calls.Load(), relay.configs.Load())
		}
		clock.Store(int64(DefaultNotOfferedFor/time.Second) + 1)
		_, _, _ = dialer.DialDevice(context.Background(), relay.session(), "dev", "", true, false, legacy.dial)
		if relay.configs.Load() != 2 {
			t.Fatalf("config %q: the relay was not asked again after the period", body)
		}
		dialer.Close()
	}
}

// A carrier that cannot be dialed falls back too, for the shorter period.
func TestDialDeviceFallsBackWhenTheCarrierFails(t *testing.T) {
	relay := newFakeRelay(t)
	relay.config.Store(`{"enabled":true,"version":1,"ws_path":"/missing"}`)
	dialer := newTestDialer(nil)
	defer dialer.Close()
	legacy := &legacyCounter{}
	for range 2 {
		_, _, _ = dialer.DialDevice(context.Background(), relay.session(), "dev", "", true, false, legacy.dial)
	}
	if legacy.calls.Load() != 2 || relay.configs.Load() != 1 {
		t.Fatalf("legacy %d, config requests %d; want 2 and 1", legacy.calls.Load(), relay.configs.Load())
	}
}

// A refusal is final: it is returned with the server's reply and never retried over the bridge.
func TestDialDeviceReturnsRefusals(t *testing.T) {
	relay := newFakeRelay(t)
	refusal := relaymux.Reply{Status: http.StatusForbidden, Code: "forbidden", Message: "设备已关闭远程访问"}
	relay.mu.Lock()
	relay.refusal = &refusal
	relay.mu.Unlock()
	dialer := newTestDialer(nil)
	defer dialer.Close()
	legacy := &legacyCounter{}
	_, _, err := dialer.DialDevice(context.Background(), relay.session(), "dev", "", true, false, legacy.dial)
	reply, refused := relaymux.Refused(err)
	if !refused || reply.Status != refusal.Status || reply.Code != refusal.Code || reply.Message != refusal.Message {
		t.Fatalf("dial = %v, want the refusal", err)
	}
	if legacy.calls.Load() != 0 {
		t.Fatal("a refusal fell back to the bridge")
	}
}

// A rejected login (401 on the config, the Hello or a goaway) reports ErrSessionExpired without a bridge; the next
// operation with the new login dials a new link.
func TestDialDeviceReportsAnExpiredLogin(t *testing.T) {
	relay := newFakeRelay(t)
	dialer := newTestDialer(nil)
	defer dialer.Close()
	legacy := &legacyCounter{}
	stale := relay.session()
	conn, _, err := dialer.DialDevice(context.Background(), stale, "dev", "", true, false, legacy.dial)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	relay.token.Store("token-2")
	relay.goaway(relaymux.Reply{Status: http.StatusUnauthorized, Code: "forbidden", Message: "relogin"})
	deadline := time.Now().Add(10 * time.Second)
	for dialer.Kind(relay.server.URL) != "" {
		if time.Now().After(deadline) {
			t.Fatal("the goaway did not end the link")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, _, err := dialer.DialDevice(context.Background(), stale, "dev", "", true, false, legacy.dial); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("dial with the revoked login = %v, want ErrSessionExpired", err)
	}
	conn, _, err = dialer.DialDevice(context.Background(), relay.session(), "dev", "", true, false, legacy.dial)
	if err != nil {
		t.Fatalf("dial with the new login: %v", err)
	}
	echo(t, conn)
	_ = conn.Close()
	if legacy.calls.Load() != 0 {
		t.Fatal("an expired login fell back to the bridge")
	}
}

// QUIC is offered only to a session whose HTTPS is verified and not proxied; the websocket carrier always.
func TestDialConfigLeavesQUICOutForUnverifiedOrProxiedSessions(t *testing.T) {
	config := relaymux.Config{Enabled: true, Version: relaymux.Version, WSPath: relaymux.WSPath, ALPN: relaymux.ALPN, Pin: "pin", QUIC: []relaymux.Endpoint{{Host: "relay.example.com", Port: 3478}}}
	encoded, _ := json.Marshal(config)
	for _, tc := range []struct {
		name     string
		session  Session
		wantQUIC bool
	}{
		{name: "verified", session: Session{BaseURL: "https://relay.example.com"}, wantQUIC: true},
		{name: "insecure", session: Session{BaseURL: "https://relay.example.com", Insecure: true}},
		{name: "proxied", session: Session{BaseURL: "https://relay.example.com", Proxied: true}},
		{name: "plain http", session: Session{BaseURL: "http://relay.example.com"}},
	} {
		var got relaymux.DialConfig
		dialer := New(Options{Configure: func(dial *relaymux.DialConfig) {
			got = *dial
			dial.QUICAddrs, dial.WSURL = nil, ""
		}})
		tc.session.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(encoded)), Header: http.Header{}}, nil
		})
		_, _ = dialer.dial(context.Background(), tc.session)
		if (len(got.QUICAddrs) > 0) != tc.wantQUIC || got.WSURL == "" {
			t.Fatalf("%s: QUIC %v, websocket %q", tc.name, got.QUICAddrs, got.WSURL)
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
