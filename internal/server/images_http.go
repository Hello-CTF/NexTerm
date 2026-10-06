package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const imageAuditSource = "files"

// imageIdentity 解析可选的会话 cookie 身份, 不强制。
// 账号功能关闭(--auth=off)时不会话解析, 直接进入共享工作区语义。
func (s *Server) imageIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.accounts != nil {
			if token, ok := sessionCookie(r); ok {
				if identity, err := s.accounts.ValidateSession(r.Context(), token); err == nil {
					r = r.WithContext(withAccountIdentity(r.Context(), identity))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireImageWrite 图片写守卫: 有会话身份时要求 CSRF 头并拒绝 reset_required;
// 无会话身份时仅在访问控制不强制(回环豁免或 --auth=off)时放行, 归属共享工作区。
// --auth=off 不会因此获得任何账号或超管能力。
func (s *Server) requireImageWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := accountIdentityFrom(r.Context())
		if identity == nil {
			if s.authRequired {
				writeAccountError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "会话无效或缺失"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if identity.State == account.StateResetRequired {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "必须先完成密码重置"))
			return
		}
		if !accountCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "CSRF 校验失败"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func imageOwnerFrom(ctx context.Context) string {
	if identity := accountIdentityFrom(ctx); identity != nil {
		return identity.UserID
	}
	return ImageSharedOwner
}

type imageUploadView struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	MIME      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *Server) serveImageUpload(w http.ResponseWriter, r *http.Request) {
	owner := imageOwnerFrom(r.Context())
	name := r.URL.Query().Get("name")
	if name != "" {
		name = SafeBlobName(name)
	}
	meta, err := s.images.Put(owner, r.Body, r.ContentLength, name)
	if err != nil {
		switch {
		case errors.Is(err, errImageTooLarge):
			http.Error(w, errImageTooLarge.Error(), http.StatusRequestEntityTooLarge)
		case errors.Is(err, errImageQuota):
			http.Error(w, errImageQuota.Error(), http.StatusInsufficientStorage)
		case errors.Is(err, errImageMIME):
			http.Error(w, errImageMIME.Error(), http.StatusUnsupportedMediaType)
		default:
			s.logger.Warn("image upload failed", "error", err)
			http.Error(w, "image upload failed", http.StatusInternalServerError)
		}
		return
	}
	s.auditImage(r.Context(), "image_upload", meta, owner)
	writeAccountJSON(w, http.StatusOK, imageUploadView{
		ID: meta.ID, URL: s.imagePublicURL(r.Context(), meta.ID), MIME: meta.MIME,
		Bytes: meta.Bytes, CreatedAt: meta.CreatedAt, ExpiresAt: meta.ExpiresAt,
	})
}

func (s *Server) serveImageDownload(w http.ResponseWriter, r *http.Request) {
	meta, file, err := s.images.Open(r.PathValue("id"))
	if err != nil {
		http.Error(w, "image not found or expired", http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "image not found or expired", http.StatusNotFound)
		return
	}
	s.auditImage(r.Context(), "image_download", meta, "anonymous")
	w.Header().Set("Content-Type", meta.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", imageInlineDisposition(meta.Name))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, meta.Name, info.ModTime(), file)
}

func (s *Server) serveImageDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	meta, err := s.images.readMeta(id)
	if err != nil {
		http.Error(w, "image not found or expired", http.StatusNotFound)
		return
	}
	identity := accountIdentityFrom(r.Context())
	by := ImageSharedOwner
	allowed := meta.Owner == ImageSharedOwner
	if identity != nil {
		by = identity.UserID
		allowed = identity.UserID == meta.Owner || identity.Role == account.RoleSuperadmin
	}
	if !allowed {
		http.Error(w, "image not found or expired", http.StatusNotFound)
		return
	}
	removed, err := s.images.Remove(id)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "image not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Warn("image delete failed", "error", err)
		http.Error(w, "image delete failed", http.StatusInternalServerError)
		return
	}
	s.auditImage(r.Context(), "image_delete", removed, by)
	w.WriteHeader(http.StatusNoContent)
}

// imageInlineDisposition 生成 inline 展示用的安全文件名。
func imageInlineDisposition(name string) string {
	if name == "" {
		return "inline"
	}
	var encoded strings.Builder
	for _, value := range []byte(name) {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value)) {
			encoded.WriteByte(value)
		} else {
			_, _ = fmt.Fprintf(&encoded, "%%%02X", value)
		}
	}
	return "inline; filename*=UTF-8''" + encoded.String()
}

// publicBaseURL 返回生效的文件访问基础 URL: 持久化设置优先, 其次 CLI/env 默认值;
// 都未设置时为空串, 由调用方生成同源相对链接。
func (s *Server) publicBaseURL(ctx context.Context) string {
	if s.settings != nil {
		if value, ok, err := s.settings.SettingGet(ctx, core.FilePublicBaseURLSettingKey); err == nil && ok && value != "" {
			return value
		}
	}
	return s.options.PublicBaseURL
}

func (s *Server) imagePublicURL(ctx context.Context, id string) string {
	base := s.publicBaseURL(ctx)
	if base == "" {
		return "/files/image/" + id
	}
	return strings.TrimRight(base, "/") + "/files/image/" + id
}

// auditImage 写图片审计; 审计不含任何令牌或秘密, 注入失败只记日志。
func (s *Server) auditImage(ctx context.Context, kind string, meta *ImageMeta, by string) {
	if s.audit == nil {
		return
	}
	payload := map[string]any{
		"id": meta.ID, "owner": meta.Owner, "mime": meta.MIME, "bytes": meta.Bytes, "by": by,
	}
	if err := s.audit(ctx, imageAuditSource, kind, payload); err != nil {
		s.logger.Warn("image audit write failed", "kind", kind, "error", err)
	}
}
