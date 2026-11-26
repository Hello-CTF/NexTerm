package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

const (
	// relayUnavailableRetry is how long an agent whose server offers no relay (404, disabled or an incompatible
	// version) waits before it asks again; bridges keep working per operation meanwhile.
	relayUnavailableRetry = 10 * time.Minute
	// relayDialTimeout bounds fetching the relay config and dialing the link.
	relayDialTimeout = 30 * time.Second
	// relayStableLink is how long a link must have lived for its loss to restart the reconnect backoff.
	relayStableLink = time.Minute
)

// errRelayUnavailable means the server offers no usable relay.
var errRelayUnavailable = errors.New("relay not offered by the server")

// RelayConfig fetches GET /relay/config with the device credential headers.
func (c *EndpointClient) RelayConfig(ctx context.Context, deviceID, secret string) (*relaymux.Config, error) {
	headers := http.Header{}
	headers.Set(HeaderDeviceID, deviceID)
	headers.Set("Authorization", "Bearer "+secret)
	var config relaymux.Config
	if err := c.doJSONWithHeaders(ctx, http.MethodGet, relaymux.ConfigPath, headers, nil, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// proxied reports whether requests to the endpoint go through a proxy: a manual one or one from the
// environment. A QUIC carrier would bypass it, so the relay then uses only the websocket carrier.
func (c *EndpointClient) proxied() bool {
	if c.entry.Proxy.Enabled() {
		return true
	}
	parsed, err := url.Parse(c.base)
	if err != nil {
		return false
	}
	proxy, err := http.ProxyFromEnvironment(&http.Request{URL: parsed})
	return err == nil && proxy != nil
}

// relayLink is an established relay link and the TCP connection its websocket carrier was dialed on (nil
// for QUIC), from which terminals get their SSH session variables as on a bridge.
type relayLink struct {
	*relaymux.Link
	tcp net.Conn
}

// relayServerError turns a relay refusal (a Hello refusal or a goaway) into the ServerError the control channel
// reports for the same cause, so isForbidden and isOwnerUnavailable classify both alike.
func relayServerError(err error) error {
	reply, refused := relaymux.Refused(err)
	if !refused {
		return err
	}
	return &ServerError{Status: reply.Status, Code: reply.Code, Message: reply.Message, Reason: reply.Reason, ServerVersion: reply.ServerVersion}
}

// relayLoop keeps one relay-mux link to the current endpoint while the server offers the relay, and serves the
// device streams the server opens on it. It reconnects with its own backoff, independent of the control
// channel; bridges requested over the control channel keep working whether or not a link is up. A revocation
// stops the agent as on the control channel, owner_unavailable waits until the control channel recovers, and a
// version mismatch leaves the relay off for this run (the control channel drives any upgrade).
func (r *Runtime) relayLoop(ctx context.Context) {
	backoff := retrySleep
	for ctx.Err() == nil {
		if r.ownerActive() {
			if !sleepContext(ctx, r.jittered(retrySleep)) {
				return
			}
			continue
		}
		client, err := r.selectEndpoint(ctx)
		if err != nil {
			return
		}
		link, err := r.dialRelay(ctx, client)
		if err == nil {
			started := r.now()
			err = r.serveRelayLink(ctx, link)
			if ctx.Err() != nil {
				return
			}
			if r.now().Sub(started) >= relayStableLink {
				backoff = retrySleep
			}
		}
		if ctx.Err() != nil {
			return
		}
		delay := r.jittered(backoff)
		err = relayServerError(err)
		var serverError *ServerError
		switch {
		case errors.Is(err, errRelayUnavailable):
			r.logger.Debug("server offers no relay; device streams use bridges", "url", client.BaseURL(), "error", err)
			delay = r.jittered(relayUnavailableRetry)
		case isOwnerUnavailable(err):
			if !r.ownerActive() {
				r.enterOwnerWait()
			}
			continue
		case isForbidden(err):
			r.revoke()
			return
		case errors.As(err, &serverError) && serverError.Code == "version_mismatch":
			r.logger.Warn("relay protocol mismatch; device streams use bridges", "url", client.BaseURL(), "server_version", serverError.ServerVersion)
			return
		default:
			r.logger.Debug("relay link lost; reconnecting", "url", client.BaseURL(), "error", err)
			backoff = nextBackoff(backoff)
		}
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

// ownerActive reports whether the agent waits for its owner account to become usable again.
func (r *Runtime) ownerActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owner.active
}

// dialRelay fetches the relay config and dials a link with the endpoint's TLS and proxy policy. QUIC is used
// only over verified HTTPS without a proxy; the websocket carrier uses the endpoint's own transport.
func (r *Runtime) dialRelay(ctx context.Context, client *EndpointClient) (*relayLink, error) {
	ctx, cancel := context.WithTimeout(ctx, relayDialTimeout)
	defer cancel()
	config, err := client.RelayConfig(ctx, r.deviceID, r.secret)
	if err != nil {
		var serverError *ServerError
		if errors.As(err, &serverError) && serverError.Status == http.StatusNotFound {
			return nil, errRelayUnavailable
		}
		return nil, err
	}
	var (
		mu       sync.Mutex
		observed net.Conn
	)
	httpClient, err := observedHTTPClient(client.DialPolicy(), func(conn net.Conn) {
		mu.Lock()
		observed = conn
		mu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	dialConfig, err := config.DialConfig(client.BaseURL(), relaymux.DialOptions{
		Hello: relaymux.Hello{
			Role: relaymux.RoleAgent, DeviceID: r.deviceID, Secret: r.secret,
			Protocol: ProtocolVersion, AppVersion: r.appVersion,
		},
		HTTPClient:    httpClient,
		UnverifiedTLS: client.Insecure(),
	})
	if err != nil {
		return nil, errors.Join(errRelayUnavailable, err)
	}
	if client.proxied() {
		dialConfig.QUICAddrs = nil
	}
	if r.relayDialHook != nil {
		r.relayDialHook(&dialConfig)
	}
	link, err := relaymux.Dial(ctx, dialConfig)
	if err != nil {
		return nil, err
	}
	result := &relayLink{Link: link}
	if link.Kind() == relaymux.KindWS {
		mu.Lock()
		result.tcp = observed
		mu.Unlock()
	}
	return result, nil
}

// serveRelayLink accepts the streams of link until it ends and returns why it ended. Each stream is served on the
// runtime's context, so an rstream stream outlives the link and resumes on the next one.
func (r *Runtime) serveRelayLink(ctx context.Context, link *relayLink) error {
	defer func() { _ = link.Close() }()
	r.logger.Info("relay link up", "carrier", link.Kind())
	for {
		incoming, err := link.AcceptHeader(ctx)
		if err != nil {
			if goaway := link.Err(); goaway != nil && !errors.Is(goaway, relaymux.ErrClosed) {
				return goaway
			}
			return err
		}
		env := link.sessionEnv(incoming.Conn())
		r.loopWg.Add(1)
		go func() {
			defer r.loopWg.Done()
			r.serveRelayStream(incoming, env)
		}()
	}
}

// sessionEnv states the relay connection the way a bridge states its own: the server is the SSH client and this
// host the server. The websocket carrier reports the TCP connection it was dialed on; a QUIC stream reports the
// UDP addresses of its session, given as the same address and port.
func (l *relayLink) sessionEnv(stream net.Conn) []string {
	if l.tcp != nil {
		return supervisor.SSHConnectionEnv(l.tcp.RemoteAddr(), l.tcp.LocalAddr())
	}
	return supervisor.SSHConnectionEnv(udpAsTCP(stream.RemoteAddr()), udpAsTCP(stream.LocalAddr()))
}

func udpAsTCP(addr net.Addr) net.Addr {
	if udp, ok := addr.(*net.UDPAddr); ok {
		return &net.TCPAddr{IP: udp.IP, Port: udp.Port, Zone: udp.Zone}
	}
	return addr
}

// serveRelayStream serves one device stream like a bridge of the same purpose: terminal streams go to the helper
// with the SSH session variables, other purposes to their handler; with stream=1 the stream is an rstream carrier
// (an OPEN or a RESUME) served through the stream server.
func (r *Runtime) serveRelayStream(incoming *relaymux.IncomingStream, env []string) {
	ctx := r.runCtx
	header := incoming.Header
	purpose := header.Purpose
	if !r.TerminalEnabled() {
		incoming.Refuse(relaymux.Reply{Status: http.StatusForbidden, Code: string(ipc.CodeForbidden), Message: "设备已关闭远程访问"})
		return
	}
	var handler PurposeHandler
	if purpose != "" && purpose != PurposeTerminal {
		r.mu.Lock()
		handler = r.purposeHandlers[purpose]
		r.mu.Unlock()
		if handler == nil {
			incoming.Refuse(relaymux.Reply{Status: http.StatusForbidden, Code: string(ipc.CodeForbidden), Message: "设备代理不提供该通道"})
			return
		}
	}
	// The claim key is local: a request id is chosen by the client and two streams could share one.
	bridgeID := "relay-" + ids.New()
	if header.Params[relaymux.ParamStream] == "1" {
		conn, err := incoming.Accept(map[string]string{relaymux.ParamStream: "1"})
		if err != nil {
			return
		}
		if err := r.serveCarrier(ctx, conn, bridgeID, purpose, env, handler); err != nil {
			r.logger.Warn("relay stream refused", "request_id", header.RequestID, "purpose", purpose, "error", err)
		}
		return
	}
	conn, err := incoming.Accept(nil)
	if err != nil {
		return
	}
	if handler == nil {
		r.serveTerminalStream(ctx, bridgeID, conn, env)
		return
	}
	r.servePurposeConn(ctx, conn, bridgeID, purpose, handler)
}
