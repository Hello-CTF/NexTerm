package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
)

const (
	// PurposeP2PSignal is the bridge purpose that carries direct-connection signaling between a client and
	// this agent. Advertising it (by registering its purpose handler) is also how an agent declares that it
	// understands p2p_revoke control frames; the server sends those frames and issues grants only to agents
	// that advertise it.
	PurposeP2PSignal = "p2p-signal"
	// CapabilityP2P names the hello capability the server requires before it issues a grant or sends p2p_revoke.
	CapabilityP2P = PurposeP2PSignal

	PathP2PKey    = "/agent/p2p/key"
	PathP2PConfig = "/agent/p2p/config"
	PathP2PAudit  = "/agent/p2p/audit"

	// HeaderDeviceID carries the device id of a GET agent route; the secret travels as an Authorization
	// bearer token, since a GET has no body to carry the usual device_id/secret pair.
	HeaderDeviceID = "X-NexTerm-Device-ID"

	// P2P device key states reported by the server.
	P2PKeyStateNone    = "none"
	P2PKeyStateBound   = "bound"
	P2PKeyStatePending = "pending"

	p2pKeyFile    = "p2p_key"
	p2pKeyTimeout = 15 * time.Second
)

// ErrP2PKeyInsecureEndpoint reports that a device key was not registered because the endpoint skips TLS
// verification, so trust on first use would not be meaningful.
var ErrP2PKeyInsecureEndpoint = errors.New("p2p device key is not registered over an endpoint without TLS verification")

// P2PKeyRequest is the POST /agent/p2p/key body.
type P2PKeyRequest struct {
	DeviceID  string `json:"device_id"`
	Secret    string `json:"secret"`
	PublicKey string `json:"public_key"`
}

// P2PKeyResponse reports how the server holds this device's key: bound means it is the trusted key; pending
// means the server keeps another trusted key and direct connections stay off until the owner confirms.
type P2PKeyResponse struct {
	State     string `json:"state"`
	PublicKey string `json:"public_key"`
}

// P2PEndpoint is a STUN or TURN server address.
type P2PEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// P2PTimeouts are the connection establishment budgets clients and agents use.
type P2PTimeouts struct {
	GatherMS int64 `json:"gather_ms"`
	CheckMS  int64 `json:"check_ms"`
}

// P2PConfig is the GET /fleet/p2p/config and GET /agent/p2p/config body. TURN is always empty for now.
type P2PConfig struct {
	Enabled   bool              `json:"enabled"`
	STUN      []P2PEndpoint     `json:"stun"`
	TURN      []P2PEndpoint     `json:"turn"`
	GrantKeys []grant.PublicKey `json:"grant_keys"`
	Timeouts  P2PTimeouts       `json:"timeouts"`
	// KeyState is the calling device's key state; only the agent route sets it.
	KeyState string `json:"key_state,omitempty"`
}

// P2PAuditRequest is the POST /agent/p2p/audit body: the grants the records refer to, and one record per
// direct stream the agent opened or refused.
type P2PAuditRequest struct {
	DeviceID string           `json:"device_id"`
	Secret   string           `json:"secret"`
	Grants   []string         `json:"grants"`
	Records  []P2PAuditRecord `json:"records"`
}

// P2PAuditRecord describes one direct stream. Times are Unix milliseconds; Outcome is "allow" (the default)
// or "deny" with a Reason.
type P2PAuditRecord struct {
	GrantID   string `json:"grant_id"`
	Purpose   string `json:"purpose"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at"`
	BytesIn   int64  `json:"bytes_in"`
	BytesOut  int64  `json:"bytes_out"`
	Outcome   string `json:"outcome,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// P2PAuditRejection names a record the server did not store and why.
type P2PAuditRejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// P2PAuditResponse reports how many records were stored; every other record is listed in Rejected.
type P2PAuditResponse struct {
	Accepted int                 `json:"accepted"`
	Rejected []P2PAuditRejection `json:"rejected"`
}

// P2PKeyPath is where an agent with data directory dataDir keeps its device identity key, next to agent.json.
func P2PKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "fleet", p2pKeyFile)
}

// LoadOrCreateP2PKey returns the device identity key at path, generating and saving one (mode 0600) when the
// file does not exist. A file that exists but does not hold a key is an error, never silently replaced: a new key
// would disable direct connections until the owner confirms it.
func LoadOrCreateP2PKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		seed, decodeErr := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("p2p device key %s is not a valid key", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read p2p device key: %w", err)
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create p2p key directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, p2pKeyFile+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("write p2p device key: %w", err)
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("restrict p2p device key: %w", err)
	}
	if _, err := temporary.WriteString(base64.RawURLEncoding.EncodeToString(seed) + "\n"); err != nil {
		_ = temporary.Close()
		return nil, fmt.Errorf("write p2p device key: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, fmt.Errorf("write p2p device key: %w", err)
	}
	// Link instead of rename so two agents racing on first start keep one key: the loser reads the winner's.
	if err := os.Link(temporary.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return LoadOrCreateP2PKey(path)
		}
		return nil, fmt.Errorf("save p2p device key: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// RegisterP2PKey registers the device public key with the server. It refuses endpoints without TLS
// verification (ErrP2PKeyInsecureEndpoint).
func (c *EndpointClient) RegisterP2PKey(ctx context.Context, deviceID, secret string, public ed25519.PublicKey) (*P2PKeyResponse, error) {
	if c.Insecure() {
		return nil, ErrP2PKeyInsecureEndpoint
	}
	var response P2PKeyResponse
	request := P2PKeyRequest{DeviceID: deviceID, Secret: secret, PublicKey: grant.Key(public).String()}
	if err := c.doJSON(ctx, http.MethodPost, PathP2PKey, request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// P2PConfig fetches the direct-connection configuration for this device.
func (c *EndpointClient) P2PConfig(ctx context.Context, deviceID, secret string) (*P2PConfig, error) {
	headers := http.Header{}
	headers.Set(HeaderDeviceID, deviceID)
	headers.Set("Authorization", "Bearer "+secret)
	var response P2PConfig
	if err := c.doJSONWithHeaders(ctx, http.MethodGet, PathP2PConfig, headers, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// UploadP2PAudit submits after-the-fact audit records of direct streams.
func (c *EndpointClient) UploadP2PAudit(ctx context.Context, request P2PAuditRequest) (*P2PAuditResponse, error) {
	var response P2PAuditResponse
	if err := c.doJSON(ctx, http.MethodPost, PathP2PAudit, request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// P2PKey returns this agent's device identity key, creating it on first use.
func (r *Runtime) P2PKey() (ed25519.PrivateKey, error) {
	return LoadOrCreateP2PKey(P2PKeyPath(r.dataDir))
}

// SetP2PRevokeHandler installs the receiver of p2p_revoke control frames: the direct-connection layer closes
// the session of grantID. Frames that arrive without a handler are ignored.
func (r *Runtime) SetP2PRevokeHandler(handler func(grantID, reason string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.p2pRevoke = handler
}

func (r *Runtime) handleP2PRevoke(grantID, reason string) {
	r.mu.Lock()
	handler := r.p2pRevoke
	r.mu.Unlock()
	if handler == nil {
		r.logger.Debug("p2p_revoke ignored: direct connections are not wired", "grant_id", grantID)
		return
	}
	handler(grantID, reason)
}

func (r *Runtime) servesP2P() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.purposeHandlers[PurposeP2PSignal]
	return ok
}

// RegisterP2PKey registers the device key through the current endpoint.
func (r *Runtime) RegisterP2PKey(ctx context.Context) (*P2PKeyResponse, error) {
	client := r.prober.Current()
	if client == nil {
		return nil, ErrNoEndpoint
	}
	key, err := r.P2PKey()
	if err != nil {
		return nil, err
	}
	registerCtx, cancel := context.WithTimeout(ctx, p2pKeyTimeout)
	defer cancel()
	return client.RegisterP2PKey(registerCtx, r.deviceID, r.secret, key.Public().(ed25519.PublicKey))
}

// registerP2PKeyAfterConnect registers the device key once a control channel is up, only for an agent that
// serves direct connections. Failure leaves direct connections off and is logged; the relay is unaffected.
func (r *Runtime) registerP2PKeyAfterConnect(ctx context.Context, client *EndpointClient) {
	if !r.servesP2P() {
		return
	}
	if client.Insecure() {
		r.logger.Warn("direct connections stay off: the endpoint skips TLS verification", "url", client.BaseURL())
		return
	}
	r.loopWg.Add(1)
	go func() {
		defer r.loopWg.Done()
		response, err := r.RegisterP2PKey(ctx)
		switch {
		case err != nil:
			r.logger.Warn("p2p device key registration failed; direct connections stay off", "error", err)
		case response.State == P2PKeyStatePending:
			r.logger.Warn("p2p device key changed; direct connections stay off until the device owner confirms the new key")
		}
	}()
}
