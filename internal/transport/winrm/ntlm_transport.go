package winrm

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type ntlmSealingTransport struct {
	inner             http.RoundTripper
	mu                sync.Mutex
	messageEncryption *winRMMessageEncryption
}

func newNTLMSealingTransport(inner http.RoundTripper) *ntlmSealingTransport {
	return &ntlmSealingTransport{inner: inner}
}

func (t *ntlmSealingTransport) RoundTrip(req *http.Request) (resp *http.Response, returnErr error) {
	username, password, ok := req.BasicAuth()
	if !ok {
		return nil, errors.New("ntlm: request has no credentials to negotiate with")
	}
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
		if err := t.handshake(req, username, password); err != nil {
			return nil, err
		}
	}
	resp, err := t.sendSealed(req, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.messageEncryption = nil
		if err := t.handshake(req, username, password); err != nil {
			return nil, err
		}
		return t.sendSealed(req, body)
	}
	return resp, nil
}

func (t *ntlmSealingTransport) handshake(req *http.Request, username, password string) error {
	negotiateChallenge := func(negotiateToken []byte) ([]byte, error) {
		negReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), nil)
		if err != nil {
			return nil, err
		}
		negReq.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(negotiateToken))
		resp, err := t.inner.RoundTrip(negReq)
		if err != nil {
			return nil, fmt.Errorf("ntlm: negotiate message encryption: %w", err)
		}
		defer resp.Body.Close()
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return nil, fmt.Errorf("ntlm: read challenge response: %w", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			return nil, fmt.Errorf("ntlm: negotiation expected an HTTP 401 challenge, got %d", resp.StatusCode)
		}
		challengeToken, err := negotiateResponseToken(resp.Header.Values("WWW-Authenticate"))
		if err != nil {
			return nil, fmt.Errorf("ntlm: read challenge: %w", err)
		}
		return challengeToken, nil
	}
	authenticateToken, sessionKey, negotiateFlags, err := negotiateAzureNTLMSessionKey(username, password, negotiateChallenge)
	if err != nil {
		return fmt.Errorf("ntlm: negotiate message encryption: %w", err)
	}
	authReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), nil)
	if err != nil {
		return err
	}
	authReq.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(authenticateToken))
	resp, err := t.inner.RoundTrip(authReq)
	if err != nil {
		return fmt.Errorf("ntlm: complete message encryption negotiation: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("ntlm: read negotiation response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ntlm: negotiation returned HTTP %d", resp.StatusCode)
	}
	session, err := newAzureNTLMSecuritySession(sessionKey, negotiateFlags, true)
	if err != nil {
		return fmt.Errorf("ntlm: establish security session: %w", err)
	}
	t.messageEncryption, err = newWinRMMessageEncryption("ntlm", azureNTLMMessageProtector{session: session})
	return err
}

func (t *ntlmSealingTransport) sendSealed(req *http.Request, body []byte) (*http.Response, error) {
	encrypted, err := t.messageEncryption.encrypt(body)
	if err != nil {
		return nil, fmt.Errorf("ntlm: seal request body: %w", err)
	}
	sealedReq, err := http.NewRequestWithContext(req.Context(), req.Method, req.URL.String(), bytes.NewReader(encrypted))
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		if k == "Authorization" || k == "Content-Type" || k == "Content-Length" {
			continue
		}
		sealedReq.Header[k] = v
	}
	sealedReq.Header.Set("Content-Type", t.messageEncryption.contentType())
	sealedReq.ContentLength = int64(len(encrypted))
	resp, err := t.inner.RoundTrip(sealedReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp, nil
	}
	plaintext, err := t.messageEncryption.decryptResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("ntlm: unseal response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(plaintext))
	resp.ContentLength = int64(len(plaintext))
	resp.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
	return resp, nil
}

func negotiateResponseToken(headers []string) ([]byte, error) {
	for _, header := range headers {
		for _, challenge := range strings.Split(header, ",") {
			fields := strings.Fields(strings.TrimSpace(challenge))
			if len(fields) != 2 || (!strings.EqualFold(fields[0], "Negotiate") && !strings.EqualFold(fields[0], "NTLM")) {
				continue
			}
			token, err := base64.StdEncoding.DecodeString(fields[1])
			if err != nil {
				return nil, fmt.Errorf("decode NTLM negotiate response: %w", err)
			}
			return token, nil
		}
	}
	return nil, errors.New("negotiation response did not include a Negotiate or NTLM token")
}
