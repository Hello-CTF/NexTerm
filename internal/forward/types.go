package forward

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const (
	KindLocal = "local"
	KindSOCKS = "socks"
)

var ErrClosed = errors.New("forward service closed")

type DialerProvider interface {
	CurrentDialer(context.Context, string) (base.Dialer, error)
}

type DialerProviderFunc func(context.Context, string) (base.Dialer, error)

func (f DialerProviderFunc) CurrentDialer(ctx context.Context, sessionID string) (base.Dialer, error) {
	return f(ctx, sessionID)
}

type Policy struct {
	Desktop  bool
	Platform string
}

type Environment struct {
	Available  bool   `json:"available"`
	Platform   string `json:"platform"`
	ListenHost string `json:"listenHost"`
	Reason     string `json:"reason,omitempty"`
}

func (p Policy) Environment() Environment {
	if p.Desktop {
		return Environment{Available: true, Platform: "other", ListenHost: "127.0.0.1"}
	}
	if strings.EqualFold(strings.TrimSpace(p.Platform), "lazycat") {
		return Environment{
			Available:  false,
			Platform:   "lazycat",
			ListenHost: "0.0.0.0",
			Reason:     "端口转发在懒猫微服上暂不可用：请使用平台转发能力或 SSH 隧道",
		}
	}
	return Environment{Available: true, Platform: "other", ListenHost: "0.0.0.0"}
}

func (e Environment) exposed() bool {
	ip := net.ParseIP(e.ListenHost)
	return e.ListenHost != "localhost" && (ip == nil || !ip.IsLoopback())
}

type ReconnectPolicy struct {
	Attempts int
	Backoff  time.Duration
}

type Config struct {
	Provider         DialerProvider
	Policy           Policy
	Reconnect        ReconnectPolicy
	DialTimeout      time.Duration
	HandshakeTimeout time.Duration
	Now              func() time.Time
	OnError          func(error)
}

type Spec struct {
	ID         string  `json:"id"`
	SessionID  string  `json:"sessionId"`
	ListenHost string  `json:"listenHost"`
	ListenPort uint16  `json:"listenPort"`
	TargetHost *string `json:"targetHost"`
	TargetPort *uint16 `json:"targetPort"`
	Kind       string  `json:"kind"`
	CreatedAt  int64   `json:"createdAt"`
}

type CreateLocalArgs struct {
	SessionID  string `json:"sessionId"`
	ListenPort uint16 `json:"listenPort"`
	TargetHost string `json:"targetHost"`
	TargetPort uint16 `json:"targetPort"`
}

type CreateSocksArgs struct {
	SessionID       string `json:"sessionId"`
	ListenPort      uint16 `json:"listenPort"`
	AcknowledgeRisk bool   `json:"acknowledgeRisk,omitempty"`
}

type ExposureRisk struct {
	Risk           string `json:"risk"`
	ListenHost     string `json:"listenHost"`
	Authentication string `json:"authentication"`
}
