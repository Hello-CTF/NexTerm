package sync

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	CommandLinkGet     = "sync_link_get"
	CommandLinkSet     = "sync_link_set"
	CommandStatus      = "sync_status"
	CommandSyncNow     = "sync_now"
	CommandBundleRead  = "sync_bundle_read"
	CommandBundleWrite = "sync_bundle_write"

	CommandTranscriptSyncOptIn  = "transcript_sync_opt_in"
	CommandTranscriptSyncOptIns = "transcript_sync_opt_ins"
)

type TranscriptSyncOptInRequest struct {
	ID    string `json:"id"`
	OptIn bool   `json:"optIn"`
}

type TranscriptSyncOptInsRequest struct {
	AssetID string `json:"assetId"`
}

type TranscriptSyncOptInsResponse struct {
	IDs []string `json:"ids"`
}

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, CommandLinkGet, func(ctx context.Context, _ *ipc.Call, _ struct{}) (Link, error) {
				if err := s.requireDesktop(); err != nil {
					return Link{}, err
				}
				return s.LinkGet(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandLinkSet, func(ctx context.Context, _ *ipc.Call, input LinkPatch) (Link, error) {
				if err := s.requireDesktop(); err != nil {
					return Link{}, err
				}
				return s.LinkSet(ctx, input)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandStatus, func(ctx context.Context, _ *ipc.Call, _ struct{}) (Status, error) {
				if err := s.requireDesktop(); err != nil {
					return Status{}, err
				}
				return s.Status(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandSyncNow, func(ctx context.Context, _ *ipc.Call, _ struct{}) (SyncReport, error) {
				if err := s.requireDesktop(); err != nil {
					return SyncReport{}, err
				}
				return s.Sync(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandTranscriptSyncOptIn, func(ctx context.Context, _ *ipc.Call, input TranscriptSyncOptInRequest) (struct{}, error) {
				if err := s.requireDesktop(); err != nil {
					return struct{}{}, err
				}
				if input.ID == "" {
					return struct{}{}, ipc.NewError(ipc.CodeBadParam, "会话记录 ID 不能为空")
				}
				return struct{}{}, s.store.TranscriptSetSyncOptIn(ctx, input.ID, input.OptIn)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandTranscriptSyncOptIns, func(ctx context.Context, _ *ipc.Call, input TranscriptSyncOptInsRequest) (TranscriptSyncOptInsResponse, error) {
				if err := s.requireDesktop(); err != nil {
					return TranscriptSyncOptInsResponse{}, err
				}
				rows, err := s.store.TranscriptListOptedIn(ctx)
				if err != nil {
					return TranscriptSyncOptInsResponse{}, err
				}
				response := TranscriptSyncOptInsResponse{IDs: []string{}}
				for _, row := range rows {
					if row.AssetID == input.AssetID {
						response.IDs = append(response.IDs, row.ID)
					}
				}
				return response, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandBundleRead, func(ctx context.Context, _ *ipc.Call, input BundleFileRequest) (string, error) {
				return s.ReadBundleFileWithOptions(ctx, input.Path, BundleReadOptions{Password: input.Password})
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandBundleWrite, func(ctx context.Context, _ *ipc.Call, input BundleFileRequest) (*BundleWriteResult, error) {
				return s.WriteBundleFileWithOptions(ctx, input.Path, input.Content, BundleWriteOptions{Password: input.Password})
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

func (s *Service) requireDesktop() error {
	if !s.desktop {
		return ipc.NewError(ipc.CodeUnsupported, "该同步操作只在桌面端可用")
	}
	return nil
}
