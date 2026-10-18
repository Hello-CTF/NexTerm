package sync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const CommandRepairUndecryptable = "sync_repair_undecryptable"

type RepairUndecryptableRequest struct {
	ID               string `json:"id"`
	Action           string `json:"action"`
	UserID           string `json:"userId"`
	CompareToken     string `json:"compareToken"`
	RemoteSeq        int64  `json:"remoteSeq"`
	RemoteBlobHash   string `json:"remoteBlobHash"`
	LocalKind        string `json:"localKind,omitempty"`
	LocalPayloadHash string `json:"localPayloadHash,omitempty"`
}

type RepairUndecryptableResult struct {
	Repaired bool `json:"repaired"`
}

func syncCompareToken(session *remoteSession, optIn kindOptIn) string {
	mac := hmac.New(sha256.New, session.dek)
	_, _ = mac.Write([]byte("nexterm/sync/compare/v1\x00" + session.key + "\x00" + session.userID + "\x00" + session.credentialHash))
	flags := []bool{optIn.knownHost, optIn.aiProfile, optIn.aiPermission, optIn.aiGrant, optIn.preferences}
	for _, enabled := range flags {
		if enabled {
			_, _ = mac.Write([]byte{1})
		} else {
			_, _ = mac.Write([]byte{0})
		}
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func repairStale() error {
	return ipc.NewError(ipc.CodeForbidden, "本机或云端数据已变化，请刷新对比结果后重新确认")
}

// RepairUndecryptable is called only after a desktop user confirms one compare row. It rechecks
// the current account, DEK, opt-in settings, local copy and cloud ciphertext before a CAS write.
func (s *Service) RepairUndecryptable(ctx context.Context, request RepairUndecryptableRequest) (RepairUndecryptableResult, error) {
	if err := s.requireDesktop(); err != nil {
		return RepairUndecryptableResult{}, err
	}
	if !validObjectID(request.ID) || request.RemoteSeq <= 0 || len(request.RemoteBlobHash) != 64 ||
		(request.Action != "restore-local" && request.Action != "clear-cloud") {
		return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeBadParam, "修复参数不合法，请刷新对比结果")
	}
	if request.Action == "restore-local" && (request.LocalKind == "" || len(request.LocalPayloadHash) != 64) {
		return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeBadParam, "本机副本校验信息不完整")
	}
	if request.Action == "clear-cloud" && (request.LocalKind != "" || request.LocalPayloadHash != "") {
		return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeBadParam, "清除云端副本时不能提供本机副本")
	}

	// Service.Sync acquires settingsMu before engine.opMu; keep that order so a
	// background round or a link change cannot race this repair.
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.engine.opMu.Lock()
	defer s.engine.opMu.Unlock()

	secret, err := s.linkSecret(ctx)
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	if secret.URL == "" || secret.Username == "" || secret.Password == "" {
		return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeBadParam, "同步链接需要同时配置 URL、用户名和口令")
	}
	session, err := s.engine.ensureSession(ctx, remoteConfigFromSecret(secret))
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	currentUserID, err := session.client.me(ctx)
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	if currentUserID != session.userID {
		return RepairUndecryptableResult{}, repairStale()
	}
	if err := s.engine.verifySessionDEK(ctx, remoteConfigFromSecret(secret), session); err != nil {
		return RepairUndecryptableResult{}, err
	}
	optIn, err := s.engine.loadKindOptIn(ctx, session.userID)
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	expectedToken := syncCompareToken(session, optIn)
	if session.userID != request.UserID || subtle.ConstantTimeCompare([]byte(expectedToken), []byte(request.CompareToken)) != 1 {
		return RepairUndecryptableResult{}, repairStale()
	}
	idsResponse, err := session.client.ids(ctx)
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	var entry IDEntry
	found := false
	for _, candidate := range idsResponse.Entries {
		if candidate.ID == request.ID {
			entry = candidate
			found = true
			break
		}
	}
	if !found || entry.Seq != request.RemoteSeq || entry.BlobHash != request.RemoteBlobHash {
		return RepairUndecryptableResult{}, repairStale()
	}
	pullResponse, err := session.client.pull(ctx, idsResponse.MaxSeq, []string{request.ID}, maxPullBytes)
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	if pullResponse.Head != idsResponse.Head || len(pullResponse.Objects) != 1 ||
		pullResponse.Objects[0].ID != request.ID || pullResponse.Objects[0].Seq != entry.Seq ||
		hashBlobHex(pullResponse.Objects[0].Blob) != entry.BlobHash {
		return RepairUndecryptableResult{}, repairStale()
	}
	if _, _, err := tryOpenObject(session.dek, pullResponse.Objects[0].Blob, request.ID); err == nil {
		return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeForbidden, "云端对象现在可以读取，请刷新对比结果")
	}

	// Read local data after checking the cloud. Clearing must search every
	// optional kind, including kinds hidden from the current sync comparison.
	collectionOptIn := optIn
	if request.Action == "clear-cloud" {
		collectionOptIn = kindOptIn{knownHost: true, aiProfile: true, aiPermission: true, aiGrant: true, preferences: true}
	}
	localReport := &SyncReport{}
	objects, err := s.engine.collectLocalObjects(ctx, localReport, collectionOptIn, session.userID)
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	local, localFound := objects[request.ID]
	if request.Action == "restore-local" {
		if !localFound || local.kind != request.LocalKind || objectPayloadHash(local.plaintext) != request.LocalPayloadHash {
			return RepairUndecryptableResult{}, repairStale()
		}
		blob, err := sealObject(session.dek, local.plaintext, request.ID, local.kind)
		if err != nil {
			return RepairUndecryptableResult{}, err
		}
		if err := s.engine.verifySessionDEK(ctx, remoteConfigFromSecret(secret), session); err != nil {
			return RepairUndecryptableResult{}, err
		}
		response, err := session.client.push(ctx, idsResponse.Head, []WireObject{{ID: request.ID, Blob: blob}})
		if errors.Is(err, errHeadMismatch) {
			return RepairUndecryptableResult{}, repairStale()
		}
		if err != nil {
			return RepairUndecryptableResult{}, err
		}
		if response.Applied != 1 {
			return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeForbidden, "云端未确认修复，请刷新对比结果")
		}
		return RepairUndecryptableResult{Repaired: true}, nil
	}
	if localFound || len(localReport.Warnings) != 0 {
		return RepairUndecryptableResult{}, repairStale()
	}
	// Built-in assets and recordings with per-record sync disabled are omitted by
	// collectLocalObjects but still count as local same-ID copies for a delete.
	if _, err := s.store.AssetGet(ctx, request.ID); err == nil {
		return RepairUndecryptableResult{}, repairStale()
	} else if !isNotFound(err) {
		return RepairUndecryptableResult{}, err
	}
	if _, err := s.store.TranscriptGet(ctx, request.ID); err == nil {
		return RepairUndecryptableResult{}, repairStale()
	} else if !isNotFound(err) {
		return RepairUndecryptableResult{}, err
	}
	if err := s.engine.verifySessionDEK(ctx, remoteConfigFromSecret(secret), session); err != nil {
		return RepairUndecryptableResult{}, err
	}
	response, err := session.client.deleteObject(ctx, idsResponse.Head, request.ID, entry.BlobHash)
	if errors.Is(err, errHeadMismatch) {
		return RepairUndecryptableResult{}, repairStale()
	}
	if err != nil {
		return RepairUndecryptableResult{}, err
	}
	if !response.Deleted {
		return RepairUndecryptableResult{}, ipc.NewError(ipc.CodeForbidden, "云端未确认清除，请刷新对比结果")
	}
	return RepairUndecryptableResult{Repaired: true}, nil
}
