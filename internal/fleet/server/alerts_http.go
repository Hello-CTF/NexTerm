package fleetserver

import (
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/fleet/alerts"
)

type alertAckRequest struct {
	IDs []string `json:"ids"`
}

func (s *Service) serveAlertList(w http.ResponseWriter, r *http.Request) {
	result, err := s.ListAlerts(r.Context(), identityFrom(r))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, result)
}

func (s *Service) serveAlertAck(w http.ResponseWriter, r *http.Request) {
	var request alertAckRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	acked, err := s.AckAlerts(r.Context(), identityFrom(r), request.IDs)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]any{"ok": true, "acked": acked})
}

func (s *Service) serveAlertSettingsGet(w http.ResponseWriter, r *http.Request) {
	result, err := s.DeviceAlertSettings(r.Context(), identityFrom(r), r.PathValue("id"))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, result)
}

func (s *Service) serveAlertSettingsPut(w http.ResponseWriter, r *http.Request) {
	var request alerts.SettingsInput
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	result, err := s.PutDeviceAlertSettings(r.Context(), identityFrom(r), r.PathValue("id"), request)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, result)
}
