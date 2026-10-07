package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

// WithUserID/UserIDFromContext 委托 ipc 包的同一实现: /rpc(账号会话)与 /sync/v2 两条链路共享同一身份 key。
func WithUserID(ctx context.Context, userID string) context.Context {
	return ipc.WithUserID(ctx, userID)
}

func UserIDFromContext(ctx context.Context) (string, bool) {
	return ipc.UserIDFromContext(ctx)
}

type PushRequest struct {
	Protocol  int          `json:"protocol"`
	KnownHead string       `json:"known_head"`
	Objects   []WireObject `json:"objects"`
}

type PushResponse struct {
	Protocol int    `json:"protocol"`
	Head     string `json:"head"`
	MaxSeq   int64  `json:"max_seq"`
	Applied  int    `json:"applied"`
	Skipped  int    `json:"skipped"`
}

type PullRequest struct {
	Protocol int      `json:"protocol"`
	SinceSeq int64    `json:"since_seq"`
	IDs      []string `json:"ids,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

type PullResponse struct {
	Protocol   int             `json:"protocol"`
	Objects    []WireObjectSeq `json:"objects"`
	Head       string          `json:"head"`
	MaxSeq     int64           `json:"max_seq"`
	NextSeq    int64           `json:"next_seq"`
	CursorDone bool            `json:"cursor_done"`
}

type IDsRequest struct {
	Protocol int `json:"protocol"`
}

type IDsResponse struct {
	Protocol int       `json:"protocol"`
	Entries  []IDEntry `json:"entries"`
	Head     string    `json:"head"`
	MaxSeq   int64     `json:"max_seq"`
}

// ObjectHandler 是服务端盲存储的 HTTP 入口: 只认会话身份, 不解读对象内容, 不记录对象明细。
type ObjectHandler struct {
	store *objectStore
}

func NewObjectHandler(db *sql.DB, backend store.Backend) *ObjectHandler {
	return &ObjectHandler{store: &objectStore{db: db, backend: backend}}
}

func (h *ObjectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	userID, ok := UserIDFromContext(r.Context())
	if !ok || userID == "" {
		writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失"))
		return
	}
	switch r.URL.Path {
	case "/sync/v2/push":
		h.servePush(w, r, userID)
	case "/sync/v2/pull":
		h.servePull(w, r, userID)
	case "/sync/v2/ids":
		h.serveIDs(w, r, userID)
	default:
		http.NotFound(w, r)
	}
}

func (h *ObjectHandler) servePush(w http.ResponseWriter, r *http.Request, userID string) {
	var request PushRequest
	if !decodeSyncRequest(w, r, &request) {
		return
	}
	if request.Protocol != ProtocolVersion {
		writeSyncError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeUnsupported,
			fmt.Sprintf("不支持的同步协议版本 %d（当前支持 %d）", request.Protocol, ProtocolVersion)))
		return
	}
	applied, skipped, head, maxSeq, err := h.store.push(r.Context(), userID, request.KnownHead, request.Objects)
	if errors.Is(err, errHeadMismatch) {
		writeSyncError(w, http.StatusConflict, ipc.NewError(ipc.CodeForbidden,
			"同步头不一致：远端已被其他设备更新或发生回滚，请先拉取合并后再推送"))
		return
	}
	if err != nil {
		writeSyncError(w, syncErrorStatus(err), err)
		return
	}
	writeSyncJSON(w, http.StatusOK, PushResponse{
		Protocol: ProtocolVersion, Head: head, MaxSeq: maxSeq, Applied: applied, Skipped: skipped,
	})
}

func (h *ObjectHandler) servePull(w http.ResponseWriter, r *http.Request, userID string) {
	var request PullRequest
	if !decodeSyncRequest(w, r, &request) {
		return
	}
	if request.Protocol != ProtocolVersion {
		writeSyncError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeUnsupported,
			fmt.Sprintf("不支持的同步协议版本 %d（当前支持 %d）", request.Protocol, ProtocolVersion)))
		return
	}
	objects, head, maxSeq, nextSeq, cursorDone, err := h.store.pull(r.Context(), userID, request.SinceSeq, request.IDs, request.MaxBytes)
	if err != nil {
		writeSyncError(w, syncErrorStatus(err), err)
		return
	}
	writeSyncJSON(w, http.StatusOK, PullResponse{
		Protocol: ProtocolVersion, Objects: objects, Head: head, MaxSeq: maxSeq, NextSeq: nextSeq, CursorDone: cursorDone,
	})
}

func (h *ObjectHandler) serveIDs(w http.ResponseWriter, r *http.Request, userID string) {
	var request IDsRequest
	if !decodeSyncRequest(w, r, &request) {
		return
	}
	if request.Protocol != ProtocolVersion {
		writeSyncError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeUnsupported,
			fmt.Sprintf("不支持的同步协议版本 %d（当前支持 %d）", request.Protocol, ProtocolVersion)))
		return
	}
	entries, head, maxSeq, err := h.store.idList(r.Context(), userID)
	if err != nil {
		writeSyncError(w, syncErrorStatus(err), err)
		return
	}
	writeSyncJSON(w, http.StatusOK, IDsResponse{
		Protocol: ProtocolVersion, Entries: entries, Head: head, MaxSeq: maxSeq,
	})
}

func decodeSyncRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSyncObjectBytes*2)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(fmt.Errorf("请求体不是合法 JSON: %w", err)))
		return false
	}
	return true
}

func syncErrorStatus(err error) int {
	normalized := ipc.NormalizeError(err)
	switch normalized.Code {
	case ipc.CodeBadParam, ipc.CodeUnsupported:
		return http.StatusBadRequest
	case ipc.CodeForbidden:
		return http.StatusForbidden
	case ipc.CodeNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func writeSyncJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSyncError(w http.ResponseWriter, status int, err error) {
	writeSyncJSON(w, status, ipc.Failure(ipc.NormalizeError(err)))
}
