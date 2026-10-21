package agent

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// RosterListEntry is one roster row enriched with live runtime evidence: the
// probe-classified presence and the real target asset resolved from
// server-side session state. An empty AssetID means the asset is honestly
// unknown — never a client-claimed identity.
type RosterListEntry struct {
	Entry    RosterEntry      `json:"entry"`
	Presence RosterResolution `json:"presence"`
	AssetID  string           `json:"assetId"`
}

// RosterAttachmentDTO is the attach outcome plus the real target-asset
// evidence resolved from the runtime the handle bound to.
type RosterAttachmentDTO struct {
	Entry              RosterEntry      `json:"entry"`
	Resolution         RosterResolution `json:"resolution"`
	CurrentIncarnation string           `json:"currentIncarnation,omitempty"`
	AssetID            string           `json:"assetId"`
}

// rosterAssetResolver is the optional probe capability behind asset evidence.
type rosterAssetResolver interface {
	ResolveRosterAsset(ctx context.Context, handle string) (string, error)
}

// RegisterRosterCommands registers the roster IPC surface backed by the
// runner's configured Roster. Every handler is owner-scoped through the
// roster store; a runner without a roster answers unsupported rather than
// inventing an empty one.
func (r *Runner) RegisterRosterCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "ai_roster_list", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
			}) ([]RosterListEntry, error) {
				if r.config.Roster == nil {
					return nil, ipc.NewError(ipc.CodeUnsupported, "AI roster 未配置")
				}
				entries, err := r.config.Roster.List(ctx, args.ConversationID)
				if err != nil {
					return nil, err
				}
				listed := make([]RosterListEntry, 0, len(entries))
				for _, entry := range entries {
					listed = append(listed, RosterListEntry{
						Entry:    entry,
						Presence: r.rosterPresence(ctx, entry),
						AssetID:  r.rosterAssetEvidence(ctx, entry.Handle),
					})
				}
				return listed, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_roster_attach", func(ctx context.Context, _ *ipc.Call, args struct {
				Handle string `json:"handle"`
				JobID  string `json:"jobId"`
			}) (RosterAttachmentDTO, error) {
				if r.config.Roster == nil {
					return RosterAttachmentDTO{}, ipc.NewError(ipc.CodeUnsupported, "AI roster 未配置")
				}
				if strings.TrimSpace(args.Handle) == "" {
					return RosterAttachmentDTO{}, ipc.NewError(ipc.CodeBadParam, "roster handle 不能为空")
				}
				attachment, err := r.config.Roster.Attach(ctx, args.Handle, args.JobID)
				if err != nil {
					return RosterAttachmentDTO{}, err
				}
				return RosterAttachmentDTO{
					Entry:              attachment.Entry,
					Resolution:         attachment.Resolution,
					CurrentIncarnation: attachment.CurrentIncarnation,
					AssetID:            r.rosterAssetEvidence(ctx, args.Handle),
				}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_roster_release", func(ctx context.Context, _ *ipc.Call, args struct {
				Handle      string `json:"handle"`
				CreatedAt   int64  `json:"createdAt"`
				Incarnation string `json:"incarnation"`
			}) (RosterEntry, error) {
				if r.config.Roster == nil {
					return RosterEntry{}, ipc.NewError(ipc.CodeUnsupported, "AI roster 未配置")
				}
				if strings.TrimSpace(args.Handle) == "" {
					return RosterEntry{}, ipc.NewError(ipc.CodeBadParam, "roster handle 不能为空")
				}
				return r.config.Roster.RequestRelease(ctx, args.Handle, RosterIdentity{CreatedAt: args.CreatedAt, Incarnation: args.Incarnation})
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_roster_reconcile", func(ctx context.Context, _ *ipc.Call, args struct {
				ConversationID string `json:"conversationId"`
			}) (RosterReconcileReport, error) {
				if r.config.Roster == nil {
					return RosterReconcileReport{}, ipc.NewError(ipc.CodeUnsupported, "AI roster 未配置")
				}
				return r.config.Roster.Reconcile(ctx, args.ConversationID)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// rosterPresence classifies one entry without writing: terminal states
// resolve authoritatively from the persisted state; live states take the
// probe's answer, and a missing or failing probe degrades to unverifiable
// rather than inventing presence.
func (r *Runner) rosterPresence(ctx context.Context, entry RosterEntry) RosterResolution {
	switch entry.State {
	case RosterStateGone, RosterStateReleased:
		return RosterResolutionGone
	case RosterStateExited:
		return RosterResolutionExited
	case RosterStateReplaced:
		return RosterResolutionReplaced
	}
	probe := r.config.Roster.Probe()
	if probe == nil {
		return RosterResolutionUnverifiable
	}
	status, err := probe.ProbeRosterRuntime(ctx, entry.Handle, entry.Incarnation)
	if err != nil {
		return RosterResolutionUnverifiable
	}
	switch status.Kind {
	case RosterRuntimeConnected:
		return RosterResolutionConnected
	case RosterRuntimeDisconnected:
		return RosterResolutionDisconnected
	case RosterRuntimeGone:
		return RosterResolutionGone
	case RosterRuntimeReplaced:
		return RosterResolutionReplaced
	case RosterRuntimeExited:
		return RosterResolutionExited
	default:
		return RosterResolutionUnverifiable
	}
}

// rosterAssetEvidence resolves the real target asset through the probe's
// optional resolver; an unresolvable runtime stays unknown ("") instead of
// substituting a client-claimed identity.
func (r *Runner) rosterAssetEvidence(ctx context.Context, handle string) string {
	resolver, ok := r.config.Roster.Probe().(rosterAssetResolver)
	if !ok {
		return ""
	}
	assetID, err := resolver.ResolveRosterAsset(ctx, handle)
	if err != nil {
		return ""
	}
	return assetID
}
