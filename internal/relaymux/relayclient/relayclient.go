// Package relayclient is the client core's device stream dial: it keeps one relay-mux link per signed-in
// server login and opens every device operation as a stream of it, and tells the caller to dial today's
// per-operation bridge when the server offers no usable relay (an older server answering 404, a relay that is
// disabled or of another version, or a carrier that cannot be dialed).
package relayclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/relaymux"
)

const (
	// DefaultUnavailableFor is how long a server whose relay could not be used is reached by bridges only.
	DefaultUnavailableFor = time.Minute
	// DefaultNotOfferedFor is that period for a server that answered it offers no compatible relay.
	DefaultNotOfferedFor = 10 * time.Minute
	// DefaultLinkWait bounds how long an operation waits for a link before it uses a bridge.
	DefaultLinkWait = 10 * time.Second
)

var (
	// ErrUnavailable means the relay cannot carry this operation; the caller dials the per-operation bridge.
	ErrUnavailable = errors.New("relayclient: relay unavailable")
	// ErrSessionExpired means the server rejected the login session (HTTP 401 on the config route, the Hello,
	// a stream or a goaway); the caller re-logs in as it does for a bridge 401.
	ErrSessionExpired = errors.New("relayclient: login session expired")
)

// Session is one signed-in login on a server, as the client's HTTPS API reaches it.
type Session struct {
	// BaseURL is the server's HTTP(S) base without a trailing slash.
	BaseURL string
	// Transport carries the TLS and proxy settings of the HTTPS API; the config request and the websocket
	// carrier use it.
	Transport http.RoundTripper
	// Insecure marks a base whose TLS certificate is not verified: QUIC is never used then.
	Insecure bool
	// Proxied marks a base reached through a proxy: QUIC, a direct UDP flow, would bypass it.
	Proxied bool
	// Token is the account session token (the session cookie value).
	Token      string
	AppVersion string
}

// Options tune a Dialer.
type Options struct {
	UnavailableFor time.Duration
	NotOfferedFor  time.Duration
	LinkWait       time.Duration
	// Configure adjusts every dial configuration before the dial (tests force a carrier).
	Configure func(*relaymux.DialConfig)
	// Backoff is the QUIC failure memory (nil: relaymux.DefaultBackoff).
	Backoff *relaymux.QUICBackoff
	Now     func() time.Time
}

// Dialer holds the relay state per server.
type Dialer struct {
	options Options

	mu      sync.Mutex
	servers map[string]*server
	closed  bool
}

// server is one base URL: the client of the current login and the clients of earlier logins, closed once their
// links ended.
type server struct {
	identity         identity
	client           *relaymux.Client
	retired          []*relaymux.Client
	unavailableUntil time.Time
}

type identity struct {
	token    string
	insecure bool
	proxied  bool
}

// New returns a Dialer.
func New(options Options) *Dialer {
	if options.UnavailableFor <= 0 {
		options.UnavailableFor = DefaultUnavailableFor
	}
	if options.NotOfferedFor <= 0 {
		options.NotOfferedFor = DefaultNotOfferedFor
	}
	if options.LinkWait <= 0 {
		options.LinkWait = DefaultLinkWait
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Dialer{options: options, servers: make(map[string]*server)}
}

// notOfferedError marks a dial that found no compatible relay on the server.
type notOfferedError struct{ err error }

func (e *notOfferedError) Error() string { return e.err.Error() }
func (e *notOfferedError) Unwrap() error { return e.err }

// client returns the relay client of session, creating it, or ErrUnavailable while the server is in backoff.
func (d *Dialer) client(session Session) (*relaymux.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrUnavailable
	}
	state := d.servers[session.BaseURL]
	if state == nil {
		state = &server{}
		d.servers[session.BaseURL] = state
	}
	retired := state.retired[:0]
	for _, old := range state.retired {
		if _, err := old.Current(); err != nil {
			_ = old.Close()
			continue
		}
		retired = append(retired, old)
	}
	state.retired = retired
	if d.options.Now().Before(state.unavailableUntil) {
		return nil, ErrUnavailable
	}
	id := identity{token: session.Token, insecure: session.Insecure, proxied: session.Proxied}
	if state.client != nil && state.identity != id {
		// Streams of an earlier login keep their link until it ends; new streams use the current login.
		state.retired = append(state.retired, state.client)
		state.client = nil
	}
	if state.client == nil {
		state.identity = id
		state.client = relaymux.NewClient(func(ctx context.Context) (*relaymux.Link, error) {
			return d.dial(ctx, session)
		})
	}
	return state.client, nil
}

// markUnavailable sends the server's operations to bridges for period, unless client is no longer current.
func (d *Dialer) markUnavailable(session Session, client *relaymux.Client, period time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if state := d.servers[session.BaseURL]; state != nil && state.client == client {
		state.unavailableUntil = d.options.Now().Add(period)
	}
}

// drop closes client after its login was rejected, so the next operation dials with the new login.
func (d *Dialer) drop(session Session, client *relaymux.Client) {
	d.mu.Lock()
	if state := d.servers[session.BaseURL]; state != nil && state.client == client {
		state.client = nil
	}
	d.mu.Unlock()
	_ = client.Close()
}

// Open opens a device stream through the session's relay link. It returns ErrUnavailable when the caller should
// use the bridge, ErrSessionExpired on a rejected login, and a *relaymux.RefusedError when the server or the
// device refused the stream; a refusal is final and must not be retried over a bridge.
//
// A stream whose link breaks before the reply (the server already closed a link this client has not noticed yet)
// is opened again on a new link, once; only a link that cannot be dialed sends the operation to the bridge. A
// reply that does not arrive in time is returned as an error without a bridge, since the server may still be
// reaching the device, and a second attempt would authorize and audit the operation twice.
func (d *Dialer) Open(ctx context.Context, session Session, header relaymux.StreamHeader) (net.Conn, relaymux.Reply, error) {
	client, err := d.client(session)
	if err != nil {
		return nil, relaymux.Reply{}, err
	}
	for attempt := 0; ; attempt++ {
		linkCtx, cancel := context.WithTimeout(ctx, d.options.LinkWait)
		link, err := client.Link(linkCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, relaymux.Reply{}, ctx.Err()
			}
			return nil, relaymux.Reply{}, d.linkFailed(session, client, err)
		}
		conn, reply, err := link.OpenDeviceStream(ctx, header)
		if err == nil {
			return conn, reply, nil
		}
		if ctx.Err() != nil {
			return nil, relaymux.Reply{}, ctx.Err()
		}
		refusal, refused := relaymux.Refused(err)
		if refused && refusal.Status == http.StatusUnauthorized {
			d.drop(session, client)
			return nil, refusal, ErrSessionExpired
		}
		if refused && link.Err() == nil {
			return nil, refusal, err
		}
		var timeout net.Error
		if !refused && errors.As(err, &timeout) && timeout.Timeout() {
			return nil, relaymux.Reply{}, err
		}
		// The link is gone (a goaway, or a reset under the stream): redial it once.
		_ = link.Close()
		if attempt > 0 {
			return nil, relaymux.Reply{}, d.linkFailed(session, client, err)
		}
	}
}

// linkFailed classifies a failure to get a link.
func (d *Dialer) linkFailed(session Session, client *relaymux.Client, err error) error {
	if refusal, refused := relaymux.Refused(err); refused {
		if refusal.Status == http.StatusUnauthorized {
			d.drop(session, client)
			return ErrSessionExpired
		}
		// Refused for a reason a bridge reports itself (a version mismatch, device management off).
		d.markUnavailable(session, client, d.options.NotOfferedFor)
		return errors.Join(ErrUnavailable, err)
	}
	var notOffered *notOfferedError
	if errors.As(err, &notOffered) {
		d.markUnavailable(session, client, d.options.NotOfferedFor)
	} else {
		d.markUnavailable(session, client, d.options.UnavailableFor)
	}
	return errors.Join(ErrUnavailable, err)
}

// DialDevice opens a stream to deviceID for purpose through the relay, asking for an rstream carrier with stream
// (and resume), and falls back to legacy when the relay is unavailable. carriesStream reports whether the device
// reads the stream as an rstream carrier.
func (d *Dialer) DialDevice(ctx context.Context, session Session, deviceID, purpose string, stream, resume bool, legacy func(context.Context) (net.Conn, bool, error)) (net.Conn, bool, error) {
	params := map[string]string{}
	if stream || resume {
		params[relaymux.ParamStream] = "1"
	}
	if resume {
		params[relaymux.ParamResume] = "1"
	}
	conn, reply, err := d.Open(ctx, session, relaymux.StreamHeader{Purpose: purpose, DeviceID: deviceID, Params: params})
	if errors.Is(err, ErrUnavailable) {
		return legacy(ctx)
	}
	if err != nil {
		return nil, false, err
	}
	return conn, reply.Params[relaymux.ParamStream] == "1", nil
}

// Kind reports the carrier of baseURL's live link, or "" when none is up.
func (d *Dialer) Kind(baseURL string) string {
	d.mu.Lock()
	state := d.servers[baseURL]
	var client *relaymux.Client
	if state != nil {
		client = state.client
	}
	d.mu.Unlock()
	if client == nil {
		return ""
	}
	link, err := client.Current()
	if err != nil {
		return ""
	}
	return link.Kind()
}

// Reset closes every link (a sign-out or a changed server); the next operation dials afresh.
func (d *Dialer) Reset() {
	d.mu.Lock()
	servers := d.servers
	d.servers = make(map[string]*server)
	d.mu.Unlock()
	for _, state := range servers {
		if state.client != nil {
			_ = state.client.Close()
		}
		for _, old := range state.retired {
			_ = old.Close()
		}
	}
}

// Close resets the dialer and makes it answer ErrUnavailable from then on.
func (d *Dialer) Close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.Reset()
}

// dial fetches the relay config over the HTTPS API and dials a link with the session's TLS and proxy settings.
func (d *Dialer) dial(ctx context.Context, session Session) (*relaymux.Link, error) {
	httpClient := &http.Client{
		Transport:     session.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	config, err := fetchConfig(ctx, httpClient, session)
	if err != nil {
		return nil, err
	}
	dialConfig, err := config.DialConfig(session.BaseURL, relaymux.DialOptions{
		Hello:         relaymux.Hello{Role: relaymux.RoleClient, Token: session.Token, AppVersion: session.AppVersion},
		HTTPClient:    httpClient,
		UnverifiedTLS: session.Insecure,
	})
	if err != nil {
		return nil, &notOfferedError{err}
	}
	if session.Proxied {
		dialConfig.QUICAddrs = nil
	}
	dialConfig.BackoffKey = session.BaseURL
	dialConfig.Backoff = d.options.Backoff
	if d.options.Configure != nil {
		d.options.Configure(&dialConfig)
	}
	return relaymux.Dial(ctx, dialConfig)
}

const sessionCookieName = "nexterm_session"

func fetchConfig(ctx context.Context, client *http.Client, session Session) (*relaymux.Config, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(session.BaseURL, "/")+relaymux.ConfigPath, nil)
	if err != nil {
		return nil, &notOfferedError{err}
	}
	request.Header.Set("Accept", "application/json")
	if session.Token != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, &relaymux.RefusedError{Reply: relaymux.Reply{Status: http.StatusUnauthorized}}
	case http.StatusNotFound:
		return nil, &notOfferedError{errors.New("relayclient: the server has no relay")}
	default:
		return nil, fmt.Errorf("relayclient: relay config: HTTP %d", response.StatusCode)
	}
	var config relaymux.Config
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&config); err != nil {
		return nil, &notOfferedError{err}
	}
	return &config, nil
}
