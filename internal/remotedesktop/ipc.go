package remotedesktop

import (
	"context"
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type RemoteDesktopPrepareRequest struct {
	AssetID string `json:"assetId"`
}

type RemoteDesktopReleaseSecretRequest struct {
	Ticket string `json:"ticket"`
}

type RemoteDesktopCertificateRequest struct {
	AssetID string `json:"assetId"`
}

type RemoteDesktopTrustCertificateRequest struct {
	AssetID     string `json:"assetId"`
	Fingerprint string `json:"fingerprint"`
}

// RemoteDesktopSetPasswordRequest carries a typed password to Go for a promptPassword VNC asset.
type RemoteDesktopSetPasswordRequest struct {
	Ticket   string `json:"ticket"`
	Password string `json:"password"`
}

func (RemoteDesktopSetPasswordRequest) String() string {
	return "RemoteDesktopSetPasswordRequest{redacted}"
}
func (RemoteDesktopSetPasswordRequest) GoString() string {
	return "RemoteDesktopSetPasswordRequest{redacted}"
}
func (RemoteDesktopSetPasswordRequest) LogValue() slog.Value { return slog.StringValue("redacted") }

type RemoteDesktopCloseRequest struct {
	ConnID string `json:"connId"`
}

// RegisterCommands registers the remotedesktop_* IPC commands.
func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "remotedesktop_prepare", func(ctx context.Context, _ *ipc.Call, input RemoteDesktopPrepareRequest) (RemoteDesktopPrepareResult, error) {
		return s.Prepare(ctx, input.AssetID)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "remotedesktop_release_secret", func(ctx context.Context, _ *ipc.Call, input RemoteDesktopReleaseSecretRequest) (RemoteDesktopSecretResult, error) {
		return s.ReleaseSecret(ctx, input.Ticket)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "remotedesktop_set_password", func(ctx context.Context, _ *ipc.Call, input RemoteDesktopSetPasswordRequest) (any, error) {
		return nil, s.SetPassword(ctx, input.Ticket, input.Password)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "remotedesktop_certificate", func(ctx context.Context, _ *ipc.Call, input RemoteDesktopCertificateRequest) (RemoteDesktopCertificateStatus, error) {
		return s.CertificateStatus(ctx, input.AssetID)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "remotedesktop_trust_certificate", func(ctx context.Context, _ *ipc.Call, input RemoteDesktopTrustCertificateRequest) (RemoteDesktopCertificateStatus, error) {
		return s.TrustCertificate(ctx, input.AssetID, input.Fingerprint)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "remotedesktop_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]RemoteDesktopConnectionInfo, error) {
		return s.List(ctx), nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "remotedesktop_close", func(ctx context.Context, _ *ipc.Call, input RemoteDesktopCloseRequest) (any, error) {
		return nil, s.CloseConnection(ctx, input.ConnID)
	})
}
