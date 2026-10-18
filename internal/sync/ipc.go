package sync

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	CommandLinkGet           = "sync_link_get"
	CommandLinkSet           = "sync_link_set"
	CommandLinkMFA           = "sync_link_mfa"
	CommandLinkResetPassword = "sync_link_reset_password"
	CommandStatus            = "sync_status"
	CommandWake              = "sync_wake"
	CommandBundleRead        = "sync_bundle_read"
	CommandBundleWrite       = "sync_bundle_write"

	CommandDigest = "sync_digest"
	CommandExport = "sync_export"
	CommandImport = "sync_import"

	CommandTranscriptSyncOptIn = "transcript_sync_opt_in"

	CommandKindOptInGet   = "sync_kind_opt_in_get"
	CommandKindOptInSet   = "sync_kind_opt_in_set"
	CommandPreferencesGet = "sync_preferences_get"
	CommandPreferencesSet = "sync_preferences_set"
)

type TranscriptSyncOptInRequest struct {
	ID    string `json:"id"`
	OptIn bool   `json:"optIn"`
}

type LinkMFARequest struct {
	Code string `json:"code"`
}

type LinkResetPasswordRequest struct {
	NewPassword string `json:"newPassword"`
}

// KindOptIn is the per-account-user isolated sync opt-in view: known_host (host trust), AI model profiles, AI permissions,
// AI grants (device authorization and permission modes), and appearance shortcuts are enabled by default.
type KindOptIn struct {
	KnownHost    bool `json:"knownHost"`
	AIProfile    bool `json:"aiProfile"`
	AIPermission bool `json:"aiPermission"`
	AIGrant      bool `json:"aiGrant"`
	Preferences  bool `json:"preferences"`
}

type KindOptInPatch struct {
	KnownHost    *bool `json:"knownHost,omitempty"`
	AIProfile    *bool `json:"aiProfile,omitempty"`
	AIPermission *bool `json:"aiPermission,omitempty"`
	AIGrant      *bool `json:"aiGrant,omitempty"`
	Preferences  *bool `json:"preferences,omitempty"`
}

// kindOptInUserID resolves the owning user of the opt-in: the /rpc session-injected identity takes precedence; desktop anonymous
// direct connections fall back to the logged-in account session held by the engine (same fallback as preferenceUserID).
func (s *Service) kindOptInUserID(ctx context.Context) (string, bool) {
	if userID, ok := UserIDFromContext(ctx); ok && userID != "" {
		return userID, true
	}
	s.engine.sessionMu.Lock()
	defer s.engine.sessionMu.Unlock()
	if s.engine.session != nil && s.engine.session.userID != "" {
		return s.engine.session.userID, true
	}
	return "", false
}

// KindOptInGet returns the owning user's opt-in; with no identity (anonymous and desktop engine not logged in) it returns all false (default off) without error.
func (s *Service) KindOptInGet(ctx context.Context) (KindOptIn, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.kindOptInGetLocked(ctx)
}

func (s *Service) kindOptInGetLocked(ctx context.Context) (KindOptIn, error) {
	userID, ok := s.kindOptInUserID(ctx)
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
	aiPermission, err := s.store.AIPermissionSyncOptIn(ctx, userID)
	if err != nil {
		return KindOptIn{}, err
	}
	aiGrant, err := s.store.AIGrantSyncOptIn(ctx, userID)
	if err != nil {
		return KindOptIn{}, err
	}
	preferences, err := s.store.PreferencesSyncOptIn(ctx, userID)
	if err != nil {
		return KindOptIn{}, err
	}
	return KindOptIn{KnownHost: knownHost, AIProfile: aiProfile, AIPermission: aiPermission, AIGrant: aiGrant, Preferences: preferences}, nil
}

// KindOptInSet only writes the owning user's own opt-in; with no identity (anonymous and desktop engine not logged in) it is always forbidden, and client-reported userIDs are not accepted (cross-user writes cannot happen).
func (s *Service) KindOptInSet(ctx context.Context, patch KindOptInPatch) (KindOptIn, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	userID, ok := s.kindOptInUserID(ctx)
	if !ok {
		return KindOptIn{}, ipc.NewError(ipc.CodeForbidden, "需要账号会话才能修改同步内容开关")
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
	if patch.AIPermission != nil {
		if err := s.store.SetAIPermissionSyncOptIn(ctx, userID, *patch.AIPermission); err != nil {
			return KindOptIn{}, err
		}
	}
	if patch.AIGrant != nil {
		if err := s.store.SetAIGrantSyncOptIn(ctx, userID, *patch.AIGrant); err != nil {
			return KindOptIn{}, err
		}
	}
	if patch.Preferences != nil {
		if err := s.store.SetPreferencesSyncOptIn(ctx, userID, *patch.Preferences); err != nil {
			return KindOptIn{}, err
		}
	}
	return s.kindOptInGetLocked(ctx)
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
			return ipc.RegisterNested(dispatcher, CommandLinkSet, func(ctx context.Context, _ *ipc.Call, input LinkPatch) (LinkResult, error) {
				if err := s.requireDesktop(); err != nil {
					return LinkResult{}, err
				}
				return s.LinkSet(ctx, input)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandLinkMFA, func(ctx context.Context, _ *ipc.Call, input LinkMFARequest) (LinkResult, error) {
				if err := s.requireDesktop(); err != nil {
					return LinkResult{}, err
				}
				return s.LinkMFA(ctx, input.Code)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandLinkResetPassword, func(ctx context.Context, _ *ipc.Call, input LinkResetPasswordRequest) (LinkResult, error) {
				if err := s.requireDesktop(); err != nil {
					return LinkResult{}, err
				}
				return s.LinkResetPassword(ctx, input.NewPassword)
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
			return ipc.Register(dispatcher, CommandWake, func(_ context.Context, _ *ipc.Call, _ struct{}) (struct{}, error) {
				s.wake()
				return struct{}{}, nil
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
		// Asset bundle digest/export/import: one implementation shared by desktop and server, operating on the current backend's asset store;
		// session and CSRF are enforced by the /rpc transport layer in account mode; offline shared workspaces (no account) stay anonymously usable.
		func() error {
			return s.registerBundleCommands(dispatcher)
		},
		// Browser collect reads: offline shared workspaces (no account) stay anonymously usable; session and CSRF are enforced by the /rpc transport layer in account mode.
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
		func() error {
			return ipc.Register(dispatcher, CommandCollectAIPermission, func(ctx context.Context, _ *ipc.Call, _ struct{}) (CollectAIPermission, error) {
				return s.CollectAIPermission(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandCollectAIGrants, func(ctx context.Context, _ *ipc.Call, _ struct{}) (CollectAIGrants, error) {
				return s.CollectAIGrants(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, CommandCollectPreferences, func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]CollectPreference, error) {
				return s.CollectPreferences(ctx)
			})
		},
		// Sync opt-in read/write: the user identity is injected by the /rpc session (UserIDFromContext); desktop anonymous direct connections
		// fall back to the engine's logged-in session (kindOptInUserID); with neither identity, get returns all false and set is forbidden.
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
		func() error {
			return ipc.Register(dispatcher, CommandPreferencesGet, func(ctx context.Context, _ *ipc.Call, _ struct{}) (PreferenceScopeView, error) {
				return s.PreferenceView(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandPreferencesSet, func(ctx context.Context, _ *ipc.Call, input PreferenceUpdate) (PreferenceScopeView, error) {
				return s.PreferenceUpdate(ctx, input)
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

// registerBundleCommands registers the three asset bundle commands; the main command surface and the peer command surface share the same handlers.
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

// PeerDispatcher returns the sync peer command surface (the three asset bundle commands) for surface probe enumeration and sync-only deployment mounting.
func (s *Service) PeerDispatcher() (*ipc.Dispatcher, error) {
	dispatcher := ipc.NewDispatcher()
	if err := s.registerBundleCommands(dispatcher); err != nil {
		return nil, err
	}
	return dispatcher, nil
}
