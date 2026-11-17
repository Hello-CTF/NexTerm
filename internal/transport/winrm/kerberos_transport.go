package winrm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/jcmturner/gokrb5/v8/client"
	"github.com/jcmturner/gokrb5/v8/config"
	"github.com/jcmturner/gokrb5/v8/iana/etypeID"
)

// kerberosTicketSource obtains service tickets for the WinRM SPN.
type kerberosTicketSource interface {
	serviceTicket(ctx context.Context) (kerberosTicket, error)
	close()
}

// gokrb5TicketSource gets tickets from the realm's KDC with the asset's password. The krb5 configuration is built in
// memory, so no krb5.conf, keytab or credential cache of the process is read.
type gokrb5TicketSource struct {
	mu            sync.Mutex
	client        *client.Client
	spn           string
	kdcConfigured bool
	loggedIn      bool
}

func newGokrb5TicketSource(c Config) *gokrb5TicketSource {
	cfg := config.New()
	cfg.LibDefaults.DefaultRealm = c.Kerberos.Realm
	cfg.LibDefaults.DNSLookupKDC = len(c.Kerberos.KDCs) == 0
	cfg.LibDefaults.DNSLookupRealm = false
	cfg.LibDefaults.DNSCanonicalizeHostname = false
	cfg.LibDefaults.RDNS = false
	// Tickets with an Active Directory PAC rarely fit a UDP datagram.
	cfg.LibDefaults.UDPPreferenceLimit = 1
	aes := []string{"aes256-cts-hmac-sha1-96", "aes128-cts-hmac-sha1-96"}
	aesIDs := []int32{etypeID.AES256_CTS_HMAC_SHA1_96, etypeID.AES128_CTS_HMAC_SHA1_96}
	cfg.LibDefaults.DefaultTktEnctypes, cfg.LibDefaults.DefaultTktEnctypeIDs = aes, aesIDs
	cfg.LibDefaults.DefaultTGSEnctypes, cfg.LibDefaults.DefaultTGSEnctypeIDs = aes, aesIDs
	cfg.LibDefaults.PermittedEnctypes, cfg.LibDefaults.PermittedEnctypeIDs = aes, aesIDs
	if len(c.Kerberos.KDCs) != 0 {
		cfg.Realms = []config.Realm{{Realm: c.Kerberos.Realm, KDC: append([]string(nil), c.Kerberos.KDCs...)}}
	}
	// Active Directory does not offer FAST armoring for password logins.
	cl := client.NewWithPassword(c.Kerberos.username, c.Kerberos.Realm, c.Password, cfg, client.DisablePAFXFAST(true))
	return &gokrb5TicketSource{client: cl, spn: c.Kerberos.SPN, kdcConfigured: len(c.Kerberos.KDCs) != 0}
}

func (s *gokrb5TicketSource) serviceTicket(ctx context.Context) (kerberosTicket, error) {
	type result struct {
		ticket kerberosTicket
		err    error
	}
	// gokrb5 has no context support; its KDC exchanges time out on their own, so a canceled connect returns
	// without waiting for them.
	done := make(chan result, 1)
	go func() {
		ticket, err := s.fetch()
		done <- result{ticket, err}
	}()
	select {
	case r := <-done:
		return r.ticket, r.err
	case <-ctx.Done():
		return kerberosTicket{}, ctx.Err()
	}
}

func (s *gokrb5TicketSource) fetch() (kerberosTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loggedIn {
		if err := s.client.Login(); err != nil {
			return kerberosTicket{}, classifyKerberosClientError(err, s.kdcConfigured)
		}
		s.loggedIn = true
	}
	ticket, key, err := s.client.GetServiceTicket(s.spn)
	if err != nil {
		return kerberosTicket{}, classifyKerberosClientError(err, s.kdcConfigured)
	}
	return kerberosTicket{
		ticket:     ticket,
		sessionKey: key,
		realm:      s.client.Credentials.Domain(),
		cname:      s.client.Credentials.CName(),
	}, nil
}

func (s *gokrb5TicketSource) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client.Destroy()
}

// kerberosSealingTransport authenticates with HTTP Negotiate (SPNEGO/Kerberos) and wraps every SOAP message with
// the context's keys, as WinRM requires on HTTP and accepts on HTTPS. Like NTLM, the context is bound to the
// connection, so the inner transport keeps a single connection per host.
type kerberosSealingTransport struct {
	inner             http.RoundTripper
	tickets           kerberosTicketSource
	mu                sync.Mutex
	messageEncryption *winRMMessageEncryption
}

func newKerberosSealingTransport(inner http.RoundTripper, tickets kerberosTicketSource) *kerberosSealingTransport {
	return &kerberosSealingTransport{inner: inner, tickets: tickets}
}

func (t *kerberosSealingTransport) Close() error {
	t.tickets.close()
	return nil
}

func (t *kerberosSealingTransport) RoundTrip(req *http.Request) (resp *http.Response, returnErr error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	defer func() {
		if returnErr != nil {
			t.messageEncryption = nil
		}
	}()
	if t.messageEncryption == nil {
		if err := t.handshake(req); err != nil {
			return nil, err
		}
	}
	resp, err := sendEncryptedRequest(t.inner, t.messageEncryption, "kerberos", req, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.messageEncryption = nil
		if err := t.handshake(req); err != nil {
			return nil, err
		}
		return sendEncryptedRequest(t.inner, t.messageEncryption, "kerberos", req, body)
	}
	return resp, nil
}

func (t *kerberosSealingTransport) handshake(req *http.Request) error {
	ticket, err := t.tickets.serviceTicket(req.Context())
	if err != nil {
		return err
	}
	initiator, token, err := newKerberosInitiator(ticket)
	if err != nil {
		return err
	}
	authReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), nil)
	if err != nil {
		return err
	}
	authReq.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(token))
	resp, err := t.inner.RoundTrip(authReq)
	if err != nil {
		return fmt.Errorf("kerberos: authenticate: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("kerberos: read authentication response: %w", err)
	}
	responseToken, tokenErr := negotiateResponseToken(resp.Header.Values("WWW-Authenticate"))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		// A KRB-ERROR in the challenge names the reason (clock skew, a ticket the host cannot decrypt).
		if tokenErr == nil && len(responseToken) != 0 {
			if _, err := initiator.complete(responseToken); err != nil {
				var classified *kerberosError
				if errors.As(err, &classified) && classified.failure != kerberosFailureUnknown {
					return err
				}
			}
		}
		return newKerberosError(kerberosFailureRejected, fmt.Errorf("kerberos: authentication returned HTTP %d", resp.StatusCode))
	default:
		return fmt.Errorf("kerberos: authentication returned HTTP %d", resp.StatusCode)
	}
	if tokenErr != nil {
		return newKerberosError(kerberosFailureNoMutualAuth, tokenErr)
	}
	protector, err := initiator.complete(responseToken)
	if err != nil {
		return err
	}
	t.messageEncryption, err = newWinRMMessageEncryption("kerberos", protector)
	return err
}
