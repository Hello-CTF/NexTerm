package sync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	stdsync "sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

const (
	settingOrigin = "sync.origin"
	settingToken  = "sync.token"
	settingLink   = "sync.link"
)

type Option func(*Service)

func WithMetadata(appVersion string, desktop bool) Option {
	return func(s *Service) {
		s.appVersion = appVersion
		s.desktop = desktop
	}
}

type Service struct {
	store      *store.Store
	vault      *vault.Vault
	appVersion string
	desktop    bool

	settingsMu stdsync.Mutex
}

func New(db *store.Store, credentialVault *vault.Vault, options ...Option) *Service {
	s := &Service{store: db, vault: credentialVault}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) Start(ctx context.Context) error {
	if s.desktop {
		return nil
	}
	_, err := s.Token(ctx)
	return err
}

func (s *Service) Shutdown(context.Context) error {
	return nil
}

func (s *Service) Origin(ctx context.Context) (string, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.originLocked(ctx)
}

func (s *Service) originLocked(ctx context.Context) (string, error) {
	origin, found, err := s.store.SettingGet(ctx, settingOrigin)
	if err != nil {
		return "", err
	}
	if found && origin != "" {
		return origin, nil
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "无法生成同步实例标识", err)
	}
	origin = hex.EncodeToString(random[:])
	if err := s.store.SettingSet(ctx, settingOrigin, origin); err != nil {
		return "", err
	}
	return origin, nil
}

func (s *Service) Token(ctx context.Context) (string, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.tokenLocked(ctx)
}

func (s *Service) tokenLocked(ctx context.Context) (string, error) {
	token, found, err := s.store.SettingGet(ctx, settingToken)
	if err != nil {
		return "", err
	}
	if found && token != "" {
		return token, nil
	}
	return s.rotateTokenLocked(ctx)
}

func (s *Service) RotateToken(ctx context.Context) (string, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.rotateTokenLocked(ctx)
}

func (s *Service) SyncToken(ctx context.Context) (string, error) {
	return s.Token(ctx)
}

func (s *Service) RotateSyncToken(ctx context.Context) (string, error) {
	return s.RotateToken(ctx)
}

func (s *Service) rotateTokenLocked(ctx context.Context) (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "无法生成同步令牌", err)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	if err := s.store.SettingSet(ctx, settingToken, token); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) VerifyToken(ctx context.Context, presented string) (bool, error) {
	expected, found, err := s.store.SettingGet(ctx, settingToken)
	if err != nil {
		return false, err
	}
	if !found || expected == "" || presented == "" {
		return false, nil
	}
	expectedDigest := sha256.Sum256([]byte(expected))
	presentedDigest := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(expectedDigest[:], presentedDigest[:]) == 1, nil
}

func (s *Service) LinkGet(ctx context.Context) (Link, error) {
	value, found, err := s.store.SettingGet(ctx, settingLink)
	if err != nil {
		return Link{}, err
	}
	link := Link{TokenKind: TokenKindServer}
	if !found || strings.TrimSpace(value) == "" {
		return link, nil
	}
	if err := json.Unmarshal([]byte(value), &link); err != nil {
		return Link{}, ipc.WrapError(ipc.CodeInternal, "同步链接设置损坏", err)
	}
	link.URL = strings.TrimSpace(link.URL)
	if link.TokenKind != TokenKindBox {
		link.TokenKind = TokenKindServer
	}
	return link, nil
}

func (s *Service) LinkSet(ctx context.Context, patch LinkPatch) (Link, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	link, err := s.LinkGet(ctx)
	if err != nil {
		return Link{}, err
	}
	link.URL = strings.TrimSpace(patch.URL)
	if patch.TokenKind == TokenKindBox {
		link.TokenKind = TokenKindBox
	} else {
		link.TokenKind = TokenKindServer
	}
	if patch.Token != nil {
		link.Token = strings.TrimSpace(*patch.Token)
	}
	if patch.Insecure != nil {
		link.Insecure = *patch.Insecure
	}
	if err := s.saveLink(ctx, link); err != nil {
		return Link{}, err
	}
	return link, nil
}

func (s *Service) saveLink(ctx context.Context, link Link) error {
	encoded, err := json.Marshal(link)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "无法编码同步链接设置", err)
	}
	return s.store.SettingSet(ctx, settingLink, string(encoded))
}

func (s *Service) recordProbe(ctx context.Context, probeErr error) {
	link, err := s.LinkGet(ctx)
	if err != nil {
		return
	}
	if probeErr != nil {
		link.LastError = probeErr.Error()
	} else {
		link.VerifiedAt = ids.NowMS()
		link.LastError = ""
	}
	_ = s.saveLink(ctx, link)
}

func (s *Service) Digest(ctx context.Context) (Digest, error) {
	origin, err := s.Origin(ctx)
	if err != nil {
		return Digest{}, err
	}
	assets, err := s.store.AssetList(ctx, true)
	if err != nil {
		return Digest{}, err
	}
	digest := Digest{
		Origin: origin, Protocol: ProtocolVersion, AppVersion: s.appVersion,
		Desktop: s.desktop, Assets: []DigestEntry{},
	}
	for _, asset := range assets {
		if asset.Builtin || asset.ID == store.BuiltinLocalAssetID {
			continue
		}
		digest.Assets = append(digest.Assets, DigestEntry{
			ID: asset.ID, Name: asset.Name, Kind: asset.Kind, Host: asset.Host,
			Username: asset.Username, UpdatedAt: asset.UpdatedAt, DeletedAt: asset.DeletedAt,
			HasCred: asset.CredID != nil, GroupID: asset.GroupID,
		})
	}
	sort.Slice(digest.Assets, func(i, j int) bool { return digest.Assets[i].ID < digest.Assets[j].ID })
	return digest, nil
}
