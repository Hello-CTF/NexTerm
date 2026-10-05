package sync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const (
	PurposeSync = "sync"

	adminTokenID  = "admin"
	adminClientID = "admin"

	auditSourceSync = "sync"
	auditKindToken  = "sync_token"
)

type Token struct {
	ID         string `json:"id"`
	ClientID   string `json:"clientId"`
	Purpose    string `json:"purpose"`
	CreatedAt  int64  `json:"createdAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	RevokedAt  *int64 `json:"revokedAt"`
	LastUsedAt *int64 `json:"lastUsedAt"`
}

type TokenIssueRequest struct {
	ClientID string `json:"clientId"`
	Purpose  string `json:"purpose"`
	TTLMs    int64  `json:"ttlMs"`
}

type TokenIssueResult struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret"`
}

type TokenRotateRequest struct {
	ID string `json:"id"`
}

type TokenRevokeRequest struct {
	ID string `json:"id"`
}

func generateTokenSecret() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "无法生成同步令牌", err)
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func tokenSecretHash(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func validTokenClientID(clientID string) bool {
	if clientID == "" || len(clientID) > 64 || strings.TrimSpace(clientID) != clientID {
		return false
	}
	for _, r := range clientID {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validTokenPurpose(purpose string) bool {
	if len(purpose) == 0 || len(purpose) > 32 {
		return false
	}
	for i, r := range purpose {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r == '-' || r == '_' || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

func scanToken(row *sql.Row) (Token, error) {
	var token Token
	var revokedAt, lastUsedAt sql.NullInt64
	if err := row.Scan(&token.ID, &token.ClientID, &token.Purpose, &token.CreatedAt, &token.ExpiresAt, &revokedAt, &lastUsedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, err
		}
		return Token{}, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Int64
	}
	if lastUsedAt.Valid {
		token.LastUsedAt = &lastUsedAt.Int64
	}
	return token, nil
}

const tokenColumns = "id, client_id, purpose, created_at, expires_at, revoked_at, last_used_at"

func (s *Service) tokenByID(ctx context.Context, id string) (Token, error) {
	token, err := scanToken(s.store.DB().QueryRowContext(ctx, "SELECT "+tokenColumns+" FROM sync_tokens WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ipc.NewError(ipc.CodeNotFound, "未找到同步令牌")
	}
	return token, err
}

func (s *Service) TokenList(ctx context.Context) ([]Token, error) {
	rows, err := s.store.DB().QueryContext(ctx, "SELECT "+tokenColumns+" FROM sync_tokens ORDER BY created_at, id")
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer rows.Close()
	tokens := []Token{}
	for rows.Next() {
		var token Token
		var revokedAt, lastUsedAt sql.NullInt64
		if err := rows.Scan(&token.ID, &token.ClientID, &token.Purpose, &token.CreatedAt, &token.ExpiresAt, &revokedAt, &lastUsedAt); err != nil {
			return nil, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		if revokedAt.Valid {
			token.RevokedAt = &revokedAt.Int64
		}
		if lastUsedAt.Valid {
			token.LastUsedAt = &lastUsedAt.Int64
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Service) TokenIssue(ctx context.Context, request TokenIssueRequest) (TokenIssueResult, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if !validTokenClientID(request.ClientID) {
		return TokenIssueResult{}, ipc.NewError(ipc.CodeBadParam, "客户端标识不合法")
	}
	purpose := request.Purpose
	if purpose == "" {
		purpose = PurposeSync
	}
	if !validTokenPurpose(purpose) {
		return TokenIssueResult{}, ipc.NewError(ipc.CodeBadParam, "令牌用途不合法")
	}
	if request.TTLMs < 0 {
		return TokenIssueResult{}, ipc.NewError(ipc.CodeBadParam, "令牌有效期不能为负数")
	}
	secret, err := generateTokenSecret()
	if err != nil {
		return TokenIssueResult{}, err
	}
	now := ids.NowMS()
	token := Token{ID: ids.New(), ClientID: request.ClientID, Purpose: purpose, CreatedAt: now}
	if request.TTLMs > 0 {
		token.ExpiresAt = now + request.TTLMs
	}
	if _, err := s.store.DB().ExecContext(ctx, `INSERT INTO sync_tokens(id, client_id, purpose, secret_hash, created_at, expires_at)
VALUES(?,?,?,?,?,?)`, token.ID, token.ClientID, token.Purpose, tokenSecretHash(secret), token.CreatedAt, token.ExpiresAt); err != nil {
		return TokenIssueResult{}, ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	s.auditToken(ctx, "issue", token)
	return TokenIssueResult{Token: token, Secret: secret}, nil
}

func (s *Service) TokenRotate(ctx context.Context, id string) (string, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.rotateTokenByIDLocked(ctx, id)
}

func (s *Service) rotateTokenByIDLocked(ctx context.Context, id string) (string, error) {
	token, err := s.tokenByID(ctx, id)
	if err != nil {
		return "", err
	}
	if token.RevokedAt != nil {
		return "", ipc.NewError(ipc.CodeBadParam, "令牌已吊销，不能轮换")
	}
	secret, err := generateTokenSecret()
	if err != nil {
		return "", err
	}
	hash := tokenSecretHash(secret)
	if id == adminTokenID {
		err = s.withTx(ctx, func(tx *sql.Tx) error {
			if err := txUpdateTokenHash(ctx, tx, id, hash); err != nil {
				return err
			}
			return s.persistAdminTokenTx(ctx, tx, secret)
		})
	} else {
		_, err = s.store.DB().ExecContext(ctx, "UPDATE sync_tokens SET secret_hash = ? WHERE id = ?", hash, id)
		if err != nil {
			err = ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
	}
	if err != nil {
		return "", err
	}
	s.auditToken(ctx, "rotate", token)
	return secret, nil
}

func txUpdateTokenHash(ctx context.Context, tx *sql.Tx, id, hash string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE sync_tokens SET secret_hash = ? WHERE id = ?", hash, id); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func (s *Service) TokenRevoke(ctx context.Context, id string) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	token, err := s.tokenByID(ctx, id)
	if err != nil {
		return err
	}
	if token.RevokedAt != nil {
		return nil
	}
	now := ids.NowMS()
	if _, err := s.store.DB().ExecContext(ctx, "UPDATE sync_tokens SET revoked_at = ? WHERE id = ?", now, id); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	token.RevokedAt = &now
	s.auditToken(ctx, "revoke", token)
	return nil
}

func (s *Service) VerifyToken(ctx context.Context, presented string) (bool, error) {
	return s.verifyToken(ctx, presented, PurposeSync)
}

func (s *Service) verifyToken(ctx context.Context, presented, purpose string) (bool, error) {
	if presented == "" {
		return false, nil
	}
	valid, err := s.matchToken(ctx, presented, purpose)
	if err != nil || valid {
		return valid, err
	}
	migrated, err := s.migrateLegacyToken(ctx)
	if err != nil || !migrated {
		return false, err
	}
	return s.matchToken(ctx, presented, purpose)
}

func (s *Service) matchToken(ctx context.Context, presented, purpose string) (bool, error) {
	token, err := scanToken(s.store.DB().QueryRowContext(ctx, "SELECT "+tokenColumns+" FROM sync_tokens WHERE secret_hash = ?", tokenSecretHash(presented)))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if token.RevokedAt != nil || (token.ExpiresAt > 0 && ids.NowMS() > token.ExpiresAt) || token.Purpose != purpose {
		return false, nil
	}
	_, _ = s.store.DB().ExecContext(ctx, "UPDATE sync_tokens SET last_used_at = ? WHERE id = ?", ids.NowMS(), token.ID)
	return true, nil
}

func (s *Service) migrateLegacyToken(ctx context.Context) (bool, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.migrateLegacyTokenLocked(ctx)
}

func (s *Service) migrateLegacyTokenLocked(ctx context.Context) (bool, error) {
	if _, err := s.tokenByID(ctx, adminTokenID); err == nil {
		return false, nil
	} else if !isNotFound(err) {
		return false, err
	}
	stored, found, err := s.store.SettingGet(ctx, settingToken)
	if err != nil {
		return false, err
	}
	if !found || stored == "" {
		return false, nil
	}
	plaintext, err := s.adminTokenPlaintextFromStored(ctx, stored)
	if err != nil {
		return false, err
	}
	now := ids.NowMS()
	token := Token{ID: adminTokenID, ClientID: adminClientID, Purpose: PurposeSync, CreatedAt: now}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_tokens(id, client_id, purpose, secret_hash, created_at, expires_at)
VALUES(?,?,?,?,?,0)`, token.ID, token.ClientID, token.Purpose, tokenSecretHash(plaintext), token.CreatedAt); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		return s.persistAdminTokenTx(ctx, tx, plaintext)
	})
	if err != nil {
		return false, err
	}
	s.auditToken(ctx, "migrate", token)
	return true, nil
}

func (s *Service) ensureAdminRowLocked(ctx context.Context) error {
	if _, err := s.tokenByID(ctx, adminTokenID); err == nil {
		return nil
	} else if !isNotFound(err) {
		return err
	}
	if migrated, err := s.migrateLegacyTokenLocked(ctx); err != nil || migrated {
		return err
	}
	secret, err := generateTokenSecret()
	if err != nil {
		return err
	}
	token := Token{ID: adminTokenID, ClientID: adminClientID, Purpose: PurposeSync, CreatedAt: ids.NowMS()}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sync_tokens(id, client_id, purpose, secret_hash, created_at, expires_at)
VALUES(?,?,?,?,?,0)`, token.ID, token.ClientID, token.Purpose, tokenSecretHash(secret), token.CreatedAt); err != nil {
			return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
		}
		return s.persistAdminTokenTx(ctx, tx, secret)
	})
	if err != nil {
		return err
	}
	s.auditToken(ctx, "issue", token)
	return nil
}

func (s *Service) adminTokenPlaintextFromStored(ctx context.Context, stored string) (string, error) {
	plaintext, err := s.revealSettingSecret(ctx, stored)
	if err == nil {
		return plaintext, nil
	}
	if !isVaultLockedError(err) {
		return "", err
	}
	backup, found, backupErr := s.store.SettingGet(ctx, settingTokenBackup)
	if backupErr != nil {
		return "", backupErr
	}
	if found && backup != "" {
		return backup, nil
	}
	return "", err
}

func (s *Service) adminTokenPlaintextLocked(ctx context.Context) (string, error) {
	stored, found, err := s.store.SettingGet(ctx, settingToken)
	if err != nil {
		return "", err
	}
	if !found || stored == "" {
		return "", ipc.NewError(ipc.CodeInternal, "同步令牌设置丢失，请轮换令牌")
	}
	return s.adminTokenPlaintextFromStored(ctx, stored)
}

func (s *Service) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
	}
	return nil
}

func (s *Service) auditToken(ctx context.Context, action string, token Token) {
	payload, err := json.Marshal(map[string]string{
		"action": action, "tokenId": token.ID, "clientId": token.ClientID, "purpose": token.Purpose,
	})
	if err != nil {
		return
	}
	_ = s.store.AuditInsert(ctx, store.AuditInput{Source: auditSourceSync, Kind: auditKindToken, Payload: json.RawMessage(payload)})
}

func isVaultLockedError(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeVaultLocked
}
