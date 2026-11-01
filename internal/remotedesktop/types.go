// Package remotedesktop relays RDP and VNC desktops to the WebView frontends. It is an independent service
// (not a session transport): the frontend obtains a single-use ticket for a stored asset and then opens a binary
// frame transport that the service terminates itself.
package remotedesktop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	KindRDP = "rdp"
	KindVNC = "vnc"

	DefaultRDPPort = 3389
	DefaultVNCPort = 5900

	// StreamName is the Wails GoStream name used on desktop and mobile.
	StreamName = "remotedesktop"
	// TicketPreamble prefixes a first frame that carries only the ticket (VNC, or RDP without proxy_auth).
	TicketPreamble = "NXRD1:"

	// FirstFrameLimit bounds the first client frame (ticket preamble or RDCleanPath request).
	FirstFrameLimit = 64 * 1024

	certKeyTypeRDP = "rdp-tls"
	certKeyTypeVNC = "vnc-tls"
)

// Asset is the connect-time view of a stored rdp or vnc asset.
type Asset struct {
	ID          string
	OwnerID     string
	Kind        string
	Name        string
	Host        string
	Port        int
	Username    string
	OptionsJSON string
	HasPassword bool
	// AuthNone is the asset's explicit "use no password" choice; no group default and no prompt applies.
	AuthNone bool
}

// Options are the keys read from the asset OptionsJSON.
type Options struct {
	ViaAssetID     string `json:"viaAssetId"`
	PromptPassword bool   `json:"promptPassword"`
	Domain         string `json:"domain"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
}

func ParseOptions(raw string) (Options, error) {
	var options Options
	if strings.TrimSpace(raw) == "" {
		return options, nil
	}
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return Options{}, ipc.NewError(ipc.CodeBadParam, "资产选项格式无效")
	}
	options.ViaAssetID = strings.TrimSpace(options.ViaAssetID)
	options.Domain = strings.TrimSpace(options.Domain)
	return options, nil
}

// AssetResolver reads assets and secrets with the owner scope carried by ctx.
type AssetResolver interface {
	ResolveAsset(ctx context.Context, id string) (Asset, error)
	// ResolvePassword decrypts the asset credential. Callers must never log the result.
	ResolvePassword(ctx context.Context, id string) (string, error)
}

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DialerFactory returns a dialer routed through the live session of viaAssetID. The returned function resolves the
// current session on every call and must not retain the factory context beyond identity.
type DialerFactory func(ctx context.Context, viaAssetID string) (DialContextFunc, error)

// CertificateStore pins certificate fingerprints (trust on first use).
type CertificateStore interface {
	Get(ctx context.Context, host string, port int, keyType string) (fingerprint string, found bool, err error)
	Accept(ctx context.Context, host string, port int, keyType, fingerprint string) error
}

// AuditEvent never carries secrets.
type AuditEvent struct {
	Kind    string
	AssetID string
	Payload map[string]any
}

type Auditor interface {
	Audit(ctx context.Context, event AuditEvent) error
}

// Audit kinds written to the audit log.
const (
	AuditConnect       = "remotedesktop_connect"
	AuditDisconnect    = "remotedesktop_disconnect"
	AuditFailure       = "remotedesktop_failure"
	AuditSecretRelease = "remotedesktop_secret_release"
	AuditCertTrust     = "remotedesktop_cert_trust"
)

// FrameConn is the one bidirectional binary message interface both transports (websocket, Wails stream) adapt to.
// WriteFrame blocks while the peer is slow (backpressure) and never drops.
type FrameConn interface {
	ReadFrame() ([]byte, error)
	WriteFrame([]byte) error
	Close() error
}

// Identity is the authenticated caller of a transport. Both fields are empty on open-access desktop deployments.
type Identity struct {
	UserID         string
	LoginSessionID string
}

type RemoteDesktopPrepareResult struct {
	Ticket   string `json:"ticket"`
	Protocol string `json:"protocol"`
	AssetID  string `json:"assetId"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Domain   string `json:"domain"`
	// PromptPassword is true when the frontend must ask the user; nothing is stored or released then.
	PromptPassword bool `json:"promptPassword"`
	HasPassword    bool `json:"hasPassword"`
	// NoPassword is true for an asset explicitly set to use no password: nothing is released and nothing is asked.
	NoPassword  bool  `json:"noPassword,omitempty"`
	Width       int   `json:"width,omitempty"`
	Height      int   `json:"height,omitempty"`
	ExpiresAtMs int64 `json:"expiresAtMs"`
}

// RemoteDesktopSecretResult is deliberately opaque to logging.
type RemoteDesktopSecretResult struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Password string `json:"password"`
}

func (RemoteDesktopSecretResult) String() string       { return "RemoteDesktopSecretResult{redacted}" }
func (RemoteDesktopSecretResult) GoString() string     { return "RemoteDesktopSecretResult{redacted}" }
func (RemoteDesktopSecretResult) LogValue() slog.Value { return slog.StringValue("redacted") }
func (RemoteDesktopSecretResult) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte("RemoteDesktopSecretResult{redacted}"))
}

// RemoteDesktopCertificateStatus describes a certificate observed by a connection attempt.
type RemoteDesktopCertificateStatus struct {
	AssetID             string `json:"assetId"`
	Protocol            string `json:"protocol"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Status              string `json:"status"` // "new", "changed", "pinned" or "none"
	Fingerprint         string `json:"fingerprint"`
	PreviousFingerprint string `json:"previousFingerprint,omitempty"`
	Subject             string `json:"subject,omitempty"`
	NotAfterMs          int64  `json:"notAfterMs,omitempty"`
	ObservedAtMs        int64  `json:"observedAtMs"`
}

const (
	CertNew     = "new"
	CertChanged = "changed"
	CertPinned  = "pinned"
	CertNone    = "none"
)

type RemoteDesktopConnectionInfo struct {
	ID        string `json:"id"`
	AssetID   string `json:"assetId"`
	Protocol  string `json:"protocol"`
	StartedMs int64  `json:"startedAtMs"`
}

func kindPort(kind string) int {
	if kind == KindRDP {
		return DefaultRDPPort
	}
	return DefaultVNCPort
}

func certKeyType(kind string) string {
	if kind == KindRDP {
		return certKeyTypeRDP
	}
	return certKeyTypeVNC
}

func nowMS() int64 { return time.Now().UnixMilli() }

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// SetPasswordResult is empty; the password never comes back.
