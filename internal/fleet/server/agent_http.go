package fleetserver

import (
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
)

// serveAgentSync 处理 POST /agent/sync: 设备凭证鉴权 + 指标落库 + 服务状态回写,
// 响应携带服务端期望配置 (desired_autostart / metrics_interval_ms / terminal_enabled)。
// 凭证或设备已吊销一律 403, agent 按合同视同吊销停止。
func (s *Service) serveAgentSync(w http.ResponseWriter, r *http.Request) {
	var request agent.SyncRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if _, err := s.AuthenticateAgent(r.Context(), request.DeviceID, request.Secret); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if request.Sample != nil {
		if err := s.RecordMetrics(r.Context(), request.DeviceID, MetricsSample{
			TS:        request.Sample.TS,
			CPUPct:    request.Sample.CPUPct,
			MemUsed:   request.Sample.MemUsed,
			MemTotal:  request.Sample.MemTotal,
			DiskUsed:  request.Sample.DiskUsed,
			DiskTotal: request.Sample.DiskTotal,
			UptimeS:   request.Sample.UptimeS,
		}); err != nil {
			writeFleetFailure(w, err)
			return
		}
	}
	if request.ServiceState != nil {
		if err := s.UpdateServiceState(r.Context(), request.DeviceID, ServiceState{
			Installed:       request.ServiceState.Installed,
			Enabled:         request.ServiceState.Enabled,
			Active:          request.ServiceState.Active,
			LastReconcileAt: request.ServiceState.LastReconcileAt,
			LastError:       request.ServiceState.LastError,
		}); err != nil {
			writeFleetFailure(w, err)
			return
		}
	}
	desired, err := s.AgentDesiredConfig(r.Context(), request.DeviceID)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, agent.SyncResponse{
		DesiredAutostart:  desired.DesiredAutostart,
		MetricsIntervalMS: desired.MetricsIntervalMS,
		TerminalEnabled:   desired.TerminalEnabled,
	})
}

type agentCurrentURLRequest struct {
	DeviceID string `json:"device_id"`
	Secret   string `json:"secret"`
	URL      string `json:"url"`
	Reason   string `json:"reason"`
}

// serveAgentCurrentURL 处理 POST /agent/current-url: 记录 agent 切换后的当前
// 接入地址并写 failover 审计。
func (s *Service) serveAgentCurrentURL(w http.ResponseWriter, r *http.Request) {
	var request agentCurrentURLRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if _, err := s.AuthenticateAgent(r.Context(), request.DeviceID, request.Secret); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if err := s.SetCurrentURL(r.Context(), request.DeviceID, request.URL, request.Reason); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
