package sync

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	CommandDigest       = "sync_digest"
	CommandOrigin       = "sync_origin"
	CommandExport       = "sync_export"
	CommandImport       = "sync_import"
	CommandLinkGet      = "sync_link_get"
	CommandLinkSet      = "sync_link_set"
	CommandRemoteDigest = "sync_remote_digest"
	CommandPush         = "sync_push"
	CommandPull         = "sync_pull"
	CommandToken        = "sync_token"
	CommandTokenRotate  = "sync_token_rotate"
	CommandTokenList    = "sync_token_list"
	CommandTokenIssue   = "sync_token_issue"
	CommandTokenRevoke  = "sync_token_revoke"
	CommandBundleRead   = "sync_bundle_read"
	CommandBundleWrite  = "sync_bundle_write"
)

func (s *Service) RegisterCommands(dispatcher *ipc.Dispatcher) error {
	if err := s.registerPeerCommands(dispatcher); err != nil {
		return err
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, CommandOrigin, func(ctx context.Context, _ *ipc.Call, _ struct{}) (string, error) {
				return s.Origin(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandLinkGet, func(ctx context.Context, _ *ipc.Call, _ struct{}) (Link, error) {
				return s.LinkGet(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandLinkSet, func(ctx context.Context, _ *ipc.Call, input LinkPatch) (Link, error) {
				return s.LinkSet(ctx, input)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandRemoteDigest, func(ctx context.Context, _ *ipc.Call, _ struct{}) (Digest, error) {
				if err := s.requireDesktop(); err != nil {
					return Digest{}, err
				}
				return NewClient(s).RemoteDigest(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandPush, func(ctx context.Context, _ *ipc.Call, input PushRequest) (ImportReport, error) {
				if err := s.requireDesktop(); err != nil {
					return ImportReport{}, err
				}
				return NewClient(s).Push(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandPull, func(ctx context.Context, _ *ipc.Call, input PullRequest) (ImportReport, error) {
				if err := s.requireDesktop(); err != nil {
					return ImportReport{}, err
				}
				return NewClient(s).Pull(ctx, input)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandToken, func(ctx context.Context, _ *ipc.Call, _ struct{}) (*string, error) {
				if s.desktop {
					return nil, nil
				}
				if err := s.requireTokenAdmin(ctx); err != nil {
					return nil, err
				}
				token, err := s.Token(ctx)
				if err != nil {
					return nil, err
				}
				return &token, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandTokenRotate, func(ctx context.Context, _ *ipc.Call, input TokenRotateRequest) (*string, error) {
				if s.desktop {
					if input.ID != "" {
						return nil, ipc.NewError(ipc.CodeUnsupported, "该同步操作只在服务端可用")
					}
					return nil, nil
				}
				if err := s.requireTokenAdmin(ctx); err != nil {
					return nil, err
				}
				var (
					token string
					err   error
				)
				if input.ID == "" {
					token, err = s.RotateToken(ctx)
				} else {
					token, err = s.TokenRotate(ctx, input.ID)
				}
				if err != nil {
					return nil, err
				}
				return &token, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandTokenList, func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]Token, error) {
				if s.desktop {
					return nil, nil
				}
				if err := s.requireTokenAdmin(ctx); err != nil {
					return nil, err
				}
				return s.TokenList(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandTokenIssue, func(ctx context.Context, _ *ipc.Call, input TokenIssueRequest) (*TokenIssueResult, error) {
				if s.desktop {
					return nil, ipc.NewError(ipc.CodeUnsupported, "该同步操作只在服务端可用")
				}
				if err := s.requireTokenAdmin(ctx); err != nil {
					return nil, err
				}
				result, err := s.TokenIssue(ctx, input)
				if err != nil {
					return nil, err
				}
				return &result, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandTokenRevoke, func(ctx context.Context, _ *ipc.Call, input TokenRevokeRequest) (struct{}, error) {
				if s.desktop {
					return struct{}{}, ipc.NewError(ipc.CodeUnsupported, "该同步操作只在服务端可用")
				}
				if err := s.requireTokenAdmin(ctx); err != nil {
					return struct{}{}, err
				}
				if input.ID == "" {
					return struct{}{}, ipc.NewError(ipc.CodeBadParam, "令牌 ID 不能为空")
				}
				return struct{}{}, s.TokenRevoke(ctx, input.ID)
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

func (s *Service) PeerDispatcher() (*ipc.Dispatcher, error) {
	dispatcher := ipc.NewDispatcher()
	if err := s.registerPeerCommands(dispatcher); err != nil {
		return nil, err
	}
	return dispatcher, nil
}

func (s *Service) registerPeerCommands(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, CommandDigest, func(ctx context.Context, _ *ipc.Call, _ struct{}) (Digest, error) {
		return s.Digest(ctx)
	}); err != nil {
		return err
	}
	if err := ipc.RegisterNested(dispatcher, CommandExport, func(ctx context.Context, _ *ipc.Call, input ExportRequest) (Bundle, error) {
		return s.Export(ctx, input)
	}); err != nil {
		return err
	}
	return ipc.RegisterNested(dispatcher, CommandImport, func(ctx context.Context, _ *ipc.Call, input ImportRequest) (ImportReport, error) {
		return s.Import(ctx, input)
	})
}

func (s *Service) requireDesktop() error {
	if !s.desktop {
		return ipc.NewError(ipc.CodeUnsupported, "该同步操作只在桌面端可用")
	}
	return nil
}
