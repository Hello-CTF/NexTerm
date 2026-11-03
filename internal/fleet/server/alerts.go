package fleetserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/alerts"
)

func (s *Service) alertService() *alerts.Service {
	return alerts.NewService(s.db, DefaultMetricsIntervalMS*time.Millisecond, func() time.Time { return time.UnixMilli(s.now()) })
}

// NewAlertEvaluator builds the device alert evaluator. Presence is the control-channel registration,
// and the interval is the sync interval the server asks agents for.
func NewAlertEvaluator(s *Service, logger *slog.Logger) (*alerts.Evaluator, error) {
	return alerts.NewEvaluator(alerts.Config{
		DB:       s.db,
		Interval: DefaultMetricsIntervalMS * time.Millisecond,
		Now:      func() time.Time { return time.UnixMilli(s.now()) },
		Presence: s.registry.AgentOnline,
		Logger:   logger,
	})
}

// ListAlerts returns the caller's own firing alerts and unacknowledged events.
func (s *Service) ListAlerts(ctx context.Context, identity *account.Identity) (*alerts.Overview, error) {
	return s.alertService().List(ctx, identity.UserID)
}

// AckAlerts acknowledges the caller's own events; ids of other owners are ignored.
func (s *Service) AckAlerts(ctx context.Context, identity *account.Identity, ids []string) (int64, error) {
	return s.alertService().Ack(ctx, identity.UserID, ids)
}

// DeviceAlertSettings returns the effective alert settings; only the device owner may read them.
func (s *Service) DeviceAlertSettings(ctx context.Context, identity *account.Identity, deviceID string) (*alerts.EffectiveSettings, error) {
	grant, err := s.canAccess(ctx, identity, deviceID)
	if err != nil {
		return nil, err
	}
	return s.alertService().GetSettings(ctx, deviceID, grant.kind)
}

// PutDeviceAlertSettings replaces the alert settings of a device the caller owns.
func (s *Service) PutDeviceAlertSettings(ctx context.Context, identity *account.Identity, deviceID string, in alerts.SettingsInput) (*alerts.EffectiveSettings, error) {
	grant, err := s.canAccess(ctx, identity, deviceID)
	if err != nil {
		return nil, err
	}
	return s.alertService().PutSettings(ctx, grant.userID, deviceID, grant.kind, in)
}
