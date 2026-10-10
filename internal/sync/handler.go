package sync

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
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
	Protocol  int    `json:"protocol"`
	KnownHead string `json:"known_head,omitempty"`
}

type IDsResponse struct {
	Protocol  int       `json:"protocol"`
	Entries   []IDEntry `json:"entries"`
	Head      string    `json:"head"`
	MaxSeq    int64     `json:"max_seq"`
	Unchanged bool      `json:"unchanged,omitempty"`
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
	w.Header().Add("Vary", "Accept-Encoding")
	buffered := newSyncResponseWriter(w, acceptsGzip(r))
	defer buffered.Close()
	w = buffered
	userID, ok := UserIDFromContext(r.Context())
	if !ok || userID == "" {
		writeSyncError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录"))
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
	head, err := h.store.currentHead(r.Context(), userID)
	if err != nil {
		writeSyncError(w, syncErrorStatus(err), err)
		return
	}
	maxSeq, err := h.store.maxSeq(r.Context(), userID)
	if err != nil {
		writeSyncError(w, syncErrorStatus(err), err)
		return
	}
	if request.KnownHead != "" && request.KnownHead == head {
		writeSyncJSON(w, http.StatusOK, IDsResponse{Protocol: ProtocolVersion, Head: head, MaxSeq: maxSeq, Unchanged: true})
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
	body := r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		compressed, err := gzip.NewReader(body)
		if err != nil {
			writeSyncError(w, http.StatusBadRequest, ipc.BadParam(fmt.Errorf("请求体不是合法的 gzip 数据: %w", err)))
			return false
		}
		defer compressed.Close()
		body = compressed
	}
	r.Body = http.MaxBytesReader(w, body, maxSyncObjectBytes*2)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeSyncError(w, http.StatusBadRequest, ipc.BadParam(fmt.Errorf("请求体不是合法 JSON: %w", err)))
		return false
	}
	return true
}

func acceptsGzip(r *http.Request) bool {
	for _, value := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		parts := strings.Split(value, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		accepted := true
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) != 2 || !strings.EqualFold(keyValue[0], "q") {
				continue
			}
			quality, err := strconv.ParseFloat(keyValue[1], 64)
			if err != nil || quality <= 0 {
				accepted = false
			}
		}
		if accepted {
			return true
		}
	}
	return false
}

type syncResponseWriter struct {
	parent     http.ResponseWriter
	acceptGzip bool
	status     int
	body       bytes.Buffer
}

func newSyncResponseWriter(parent http.ResponseWriter, acceptGzip bool) *syncResponseWriter {
	return &syncResponseWriter{parent: parent, acceptGzip: acceptGzip, status: http.StatusOK}
}

func (w *syncResponseWriter) Header() http.Header {
	return w.parent.Header()
}

func (w *syncResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *syncResponseWriter) Write(payload []byte) (int, error) {
	return w.body.Write(payload)
}

func (w *syncResponseWriter) Close() error {
	raw := w.body.Bytes()
	if w.acceptGzip && len(raw) >= 1024 {
		var compressed bytes.Buffer
		compressor := gzip.NewWriter(&compressed)
		if _, err := compressor.Write(raw); err != nil {
			_ = compressor.Close()
			return err
		}
		if err := compressor.Close(); err != nil {
			return err
		}
		if compressed.Len() < len(raw) {
			w.parent.Header().Set("Content-Encoding", "gzip")
			w.parent.WriteHeader(w.status)
			_, err := w.parent.Write(compressed.Bytes())
			return err
		}
	}
	w.parent.WriteHeader(w.status)
	_, err := w.parent.Write(raw)
	return err
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
