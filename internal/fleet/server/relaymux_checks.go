package fleetserver

import (
	"context"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/relaymux"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
)

// relayStreamPlan is an authorized device stream: the canonical purpose and whether the agent will read the
// stream as an rstream carrier (and, for resume, re-attach one).
type relayStreamPlan struct {
	purpose string
	stream  bool
	resume  bool
}

// checkRelayStream runs the checks of serveDeviceBridgeRelay, in its order and with its refusals, for a relay
// stream header: purpose, owner authorization with the open audit (allow and deny), the device's remote access
// switch, the agent being online, the agent advertising the purpose, and a resume needing an rstream agent.
// The login session recheck follows in the caller, after the stream is registered under its session.
// TestRelayStreamRefusalsMatchTheBridgeRelay keeps the two in step.
func (s *Service) checkRelayStream(ctx context.Context, identity *account.Identity, header relaymux.StreamHeader) (relayStreamPlan, *relaymux.Reply) {
	deviceID := header.DeviceID
	purpose := header.Purpose
	action := "terminal_open"
	switch purpose {
	case "", agent.PurposeTerminal:
		purpose = agent.PurposeTerminal
	case deviceproto.PurposeFile, deviceproto.PurposeFileMux:
		action = "file_open"
	case "forward", "listen", "accept":
		action = "forward_open"
	case agent.PurposeP2PSignal:
		action = "p2p_signal_open"
	default:
		refusal := relayReply(http.StatusBadRequest, ipc.CodeBadParam, "未知的桥接用途")
		return relayStreamPlan{}, &refusal
	}
	resume := header.Params[relaymux.ParamResume] == "1"
	capabilities, _ := s.registry.Capabilities(deviceID)
	stream := (resume || header.Params[relaymux.ParamStream] == "1") && hasCapability(capabilities, rstream.Capability)
	if _, err := s.authorize(ctx, identity, deviceID, action, auditKindDeviceTerminal, func(payload *auditPayload) {
		payload.Stream, payload.Resume = stream, resume
	}); err != nil {
		refusal := relayRefusal(err)
		return relayStreamPlan{}, &refusal
	}
	desired, err := s.AgentDesiredConfig(ctx, deviceID)
	if err != nil {
		refusal := relayRefusal(err)
		return relayStreamPlan{}, &refusal
	}
	if !desired.TerminalEnabled {
		refusal := relayReply(http.StatusForbidden, ipc.CodeForbidden, "设备已关闭远程访问")
		return relayStreamPlan{}, &refusal
	}
	if !s.registry.AgentOnline(deviceID) {
		refusal := relayReply(http.StatusServiceUnavailable, ipc.CodeDisconnected, errDeviceOffline.Error())
		return relayStreamPlan{}, &refusal
	}
	capabilities, _ = s.registry.Capabilities(deviceID)
	if !servesPurpose(capabilities, purpose) {
		refusal := relayReply(http.StatusForbidden, ipc.CodeForbidden, purposeUnsupportedMessage(purpose))
		return relayStreamPlan{}, &refusal
	}
	stream = stream && hasCapability(capabilities, rstream.Capability)
	if resume && !stream {
		refusal := relayReply(http.StatusConflict, ipc.CodeUnsupported, "设备代理不支持续传通道")
		return relayStreamPlan{}, &refusal
	}
	return relayStreamPlan{purpose: purpose, stream: stream, resume: resume}, nil
}

// fleetFailureStatus is the HTTP status and normalized error writeFleetFailure answers for err.
func fleetFailureStatus(err error) (int, *ipc.Error) {
	normalized := ipc.NormalizeError(err)
	status := http.StatusInternalServerError
	switch normalized.Code {
	case ipc.CodeBadParam:
		status = http.StatusBadRequest
	case ipc.CodeForbidden, codeOwnerUnavailable:
		// owner_unavailable stays 403 so agents that predate it keep treating the response as a denial.
		status = http.StatusForbidden
	case ipc.CodeNotFound:
		status = http.StatusNotFound
	case ipc.CodeDisconnected:
		status = http.StatusServiceUnavailable
	}
	return status, normalized
}
