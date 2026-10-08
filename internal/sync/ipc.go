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

	CommandDigest = "sync_digest"
	CommandExport = "sync_export"
	CommandImport = "sync_import"

	CommandTranscriptSyncOptIn = "transcript_sync_opt_in"

	CommandKindOptInGet = "sync_kind_opt_in_get"
	CommandKindOptInSet = "sync_kind_opt_in_set"
)

type TranscriptSyncOptInRequest struct {
	ID    string `json:"id"`
	OptIn bool   `json:"optIn"`
}

// KindOptIn 是按账号用户隔离的同步 opt-in 视图: known_host(主机信任)与 ai_profile(AI 模型档案)默认关。
type KindOptIn struct {
	KnownHost bool `json:"knownHost"`
	AIProfile bool `json:"aiProfile"`
}

type KindOptInPatch struct {
	KnownHost *bool `json:"knownHost,omitempty"`
	AIProfile *bool `json:"aiProfile,omitempty"`
}

// KindOptInGet 返回会话用户的 opt-in; 无身份(匿名/桌面直连)返回全 false(默认关), 不报错。
func (s *Service) KindOptInGet(ctx context.Context) (KindOptIn, error) {
	userID, ok := UserIDFromContext(ctx)
	if !ok {
		return KindOptIn{}, nil
	}
	knownHost, err := s.store.KnownHostSyncOptIn(ctx, userID)
	if err != nil {
		return KindOptIn{}, err
	}
	aiProfile, err := s.store.AIProfileSyncOptIn(ctx, userID)
	if err != nil {
		return KindOptIn{}, err
	}
	return KindOptIn{KnownHost: knownHost, AIProfile: aiProfile}, nil
}

// KindOptInSet 只写会话用户自己的 opt-in; 无身份一律 forbidden, 不接受客户端上报 userID(跨用户写入无从发生)。
func (s *Service) KindOptInSet(ctx context.Context, patch KindOptInPatch) (KindOptIn, error) {
	userID, ok := UserIDFromContext(ctx)
	if !ok {
		return KindOptIn{}, ipc.NewError(ipc.CodeForbidden, "需要账号会话才能修改同步 opt-in")
	}
	if patch.KnownHost != nil {
		if err := s.store.SetKnownHostSyncOptIn(ctx, userID, *patch.KnownHost); err != nil {
			return KindOptIn{}, err
		}
	}
	if patch.AIProfile != nil {
		if err := s.store.SetAIProfileSyncOptIn(ctx, userID, *patch.AIProfile); err != nil {
			return KindOptIn{}, err
		}
	}
	return s.KindOptInGet(ctx)
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
			return ipc.Register(dispatcher, CommandTranscriptSyncOptIn, func(ctx context.Context, _ *ipc.Call, input TranscriptSyncOptInRequest) (struct{}, error) {
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
			return ipc.RegisterNested(dispatcher, CommandBundleRead, func(ctx context.Context, _ *ipc.Call, input BundleFileRequest) (string, error) {
				return s.ReadBundleFileWithOptions(ctx, input.Path, BundleReadOptions{Password: input.Password})
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandBundleWrite, func(ctx context.Context, _ *ipc.Call, input BundleFileRequest) (*BundleWriteResult, error) {
				return s.WriteBundleFileWithOptions(ctx, input.Path, input.Content, BundleWriteOptions{Password: input.Password})
			})
		},
		// 资产包摘要/导出/导入: 桌面端与服务器端同一实现, 操作当前后端资产库;
		// 会话与 CSRF 由 /rpc 传输层在账号模式下把关, 离线共享工作区(无账号)保持匿名可用。
		func() error {
			return s.registerBundleCommands(dispatcher)
		},
		// 浏览器收集读取: 离线共享工作区(无账号)保持匿名可用, 会话与 CSRF 由 /rpc 传输层在账号模式下把关。
		func() error {
			return ipc.RegisterNested(dispatcher, CommandCollectAssets, func(ctx context.Context, _ *ipc.Call, input CollectAssetsRequest) (CollectAssetsResult, error) {
				return s.CollectAssets(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandCollectTombstones, func(ctx context.Context, _ *ipc.Call, input CollectTombstonesRequest) (CollectTombstonesResult, error) {
				return s.CollectTombstones(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandCollectCredentials, func(ctx context.Context, _ *ipc.Call, input CollectCredentialsRequest) (CollectCredentialsResult, error) {
				return s.CollectCredentials(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandCollectKnownHosts, func(ctx context.Context, _ *ipc.Call, input CollectKnownHostsRequest) (CollectKnownHostsResult, error) {
				return s.CollectKnownHosts(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandCollectAIProfiles, func(ctx context.Context, _ *ipc.Call, input CollectAIProfilesRequest) (CollectAIProfilesResult, error) {
				return s.CollectAIProfiles(ctx, input)
			})
		},
		// 同步 opt-in 读写: 用户身份由 /rpc 会话注入(UserIDFromContext), 匿名 get 全 false、set forbidden。
		func() error {
			return ipc.Register(dispatcher, CommandKindOptInGet, func(ctx context.Context, _ *ipc.Call, _ struct{}) (KindOptIn, error) {
				return s.KindOptInGet(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandKindOptInSet, func(ctx context.Context, _ *ipc.Call, input KindOptInPatch) (KindOptIn, error) {
				return s.KindOptInSet(ctx, input)
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

// registerBundleCommands 注册资产包三命令; 主命令面与对端命令面共用同一组处理器。
func (s *Service) registerBundleCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, CommandDigest, func(ctx context.Context, _ *ipc.Call, _ struct{}) (SyncDigest, error) {
				return s.Digest(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandExport, func(ctx context.Context, _ *ipc.Call, input ExportBundleRequest) (SyncBundle, error) {
				return s.ExportBundle(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandImport, func(ctx context.Context, _ *ipc.Call, input ImportBundleRequest) (ImportReport, error) {
				return s.ImportBundle(ctx, input)
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

// PeerDispatcher 返回同步对端命令面(资产包三命令), 供 surface probe 枚举与同步专用部署挂载。
func (s *Service) PeerDispatcher() (*ipc.Dispatcher, error) {
	dispatcher := ipc.NewDispatcher()
	if err := s.registerBundleCommands(dispatcher); err != nil {
		return nil, err
	}
	return dispatcher, nil
}
