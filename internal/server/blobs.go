package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	DefaultBlobTTL                  = 2 * time.Hour
	DefaultBlobSweepInterval        = 10 * time.Minute
	DefaultBlobMaxBytes       int64 = 256 << 20
	DefaultBlobPersistQuota   int64 = 1 << 30
	DefaultBlobDiskMaxPercent       = 90.0
	blobCopyBuffer                  = 256 << 10
	blobTooLargeMessage             = "文件超出大小限制，请压缩或拆分后重试"
	blobMetaName                    = ".meta.json"
)

var (
	errBlobPersistQuota  = errors.New("持久化存储配额已用尽，请清理空间后重试")
	errBlobDiskThreshold = errors.New("磁盘使用率超过阈值，无法继续持久化存储")
	errStagedNotFound    = errors.New("暂存文件不存在或已过期")
)

type BlobStore struct {
	dataDir        string
	ttl            time.Duration
	sweepEvery     time.Duration
	maxBytes       int64
	persistQuota   int64
	diskMaxPercent float64
	diskUsage      func(string) (float64, error)
	newID          func() string
	now            func() time.Time
	logger         *slog.Logger
}

type stagedBlob struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Bytes *int64 `json:"bytes,omitempty"`
}

type blobMeta struct {
	OwnerID string `json:"owner_id"`
}

func NewBlobStore(dataDir string, logger *slog.Logger) *BlobStore {
	if logger == nil {
		logger = slog.Default()
	}
	store := &BlobStore{
		dataDir: dataDir, ttl: DefaultBlobTTL, sweepEvery: DefaultBlobSweepInterval,
		newID: ids.New, now: time.Now, logger: logger, diskUsage: diskUsageRate,
	}
	if maxBytes, err := ParseBlobMaxBytesEnv(os.Getenv); err != nil {
		logger.Warn("ignoring invalid blob limit override", "error", err)
	} else {
		store.maxBytes = maxBytes
	}
	if quota, err := ParseBlobPersistQuotaEnv(os.Getenv); err != nil {
		logger.Warn("ignoring invalid persist quota override", "error", err)
	} else {
		store.persistQuota = quota
	}
	if percent, err := ParseBlobDiskMaxPercentEnv(os.Getenv); err != nil {
		logger.Warn("ignoring invalid disk threshold override", "error", err)
	} else {
		store.diskMaxPercent = percent
	}
	return store
}

func ParseBlobMaxBytesEnv(getenv func(string) string) (int64, error) {
	return parseBlobByteEnv(getenv, "NEXTERM_BLOB_MAX_BYTES")
}

func ParseBlobPersistQuotaEnv(getenv func(string) string) (int64, error) {
	return parseBlobByteEnv(getenv, "NEXTERM_BLOB_PERSIST_MAX_BYTES")
}

func ParseBlobDiskMaxPercentEnv(getenv func(string) string) (float64, error) {
	raw := getenv("NEXTERM_BLOB_DISK_MAX_PERCENT")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 || value > 100 {
		return 0, fmt.Errorf("NEXTERM_BLOB_DISK_MAX_PERCENT must be a number in (0, 100], got %q", raw)
	}
	return value, nil
}

func parseBlobByteEnv(getenv func(string) string, key string) (int64, error) {
	raw := getenv(key)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, raw)
	}
	return value, nil
}

func (b *BlobStore) maxBlobBytes() int64 {
	if b.maxBytes > 0 {
		return b.maxBytes
	}
	return DefaultBlobMaxBytes
}

func (b *BlobStore) persistQuotaBytes() int64 {
	if b.persistQuota > 0 {
		return b.persistQuota
	}
	return DefaultBlobPersistQuota
}

func (b *BlobStore) diskThresholdPercent() float64 {
	if b.diskMaxPercent > 0 {
		return b.diskMaxPercent
	}
	return DefaultBlobDiskMaxPercent
}

func (b *BlobStore) stageRoot() string { return filepath.Join(b.dataDir, "blobs") }
func (b *BlobStore) keepRoot() string  { return filepath.Join(b.dataDir, "files") }

func (b *BlobStore) Stage(w http.ResponseWriter, r *http.Request) {
	persist, err := optionalBool(r, "persist")
	if err != nil {
		http.Error(w, "persist 参数不是合法的布尔值", http.StatusBadRequest)
		return
	}
	limit := b.maxBlobBytes()
	if r.ContentLength > limit {
		http.Error(w, blobTooLargeMessage, http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	root := b.stageRoot()
	if persist {
		root = b.keepRoot()
		if err := b.checkPersistQuota(r.ContentLength); err != nil {
			if errors.Is(err, errBlobPersistQuota) {
				http.Error(w, errBlobPersistQuota.Error(), http.StatusInsufficientStorage)
				return
			}
			http.Error(w, "持久化存储用量获取失败，请稍后重试", http.StatusInternalServerError)
			return
		}
		if err := b.checkDiskThreshold(); err != nil {
			if errors.Is(err, errBlobDiskThreshold) {
				http.Error(w, errBlobDiskThreshold.Error(), http.StatusInsufficientStorage)
				return
			}
			http.Error(w, "磁盘用量获取失败，请稍后重试", http.StatusInternalServerError)
			return
		}
	}
	dir, id, err := b.createItemDir(root)
	if err != nil {
		http.Error(w, "暂存目录不可用，请稍后重试", http.StatusInternalServerError)
		return
	}
	name := SafeBlobName(r.URL.Query().Get("name"))
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "暂存文件创建失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	written, copyErr := io.CopyBuffer(file, r.Body, make([]byte, blobCopyBuffer))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		var maxBytesErr *http.MaxBytesError
		if errors.As(copyErr, &maxBytesErr) {
			http.Error(w, blobTooLargeMessage, http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "暂存写入失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	if err := b.writeOwnerMeta(dir, r); err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "暂存写入失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	writeBlobJSON(w, stagedBlob{ID: id, Path: id + "/" + name, Bytes: &written})
}

func (b *BlobStore) Reserve(w http.ResponseWriter, r *http.Request) {
	limit := b.maxBlobBytes()
	if r.ContentLength > limit {
		http.Error(w, blobTooLargeMessage, http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dir, id, err := b.createItemDir(b.stageRoot())
	if err != nil {
		http.Error(w, "暂存目录不可用，请稍后重试", http.StatusInternalServerError)
		return
	}
	name := SafeBlobName(r.URL.Query().Get("name"))
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "文件预留失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "文件预留失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	if err := b.writeOwnerMeta(dir, r); err != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "文件预留失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	writeBlobJSON(w, stagedBlob{ID: id, Path: id + "/" + name})
}

func (b *BlobStore) Download(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !ids.Valid(id) {
		http.Error(w, "id 参数无效", http.StatusBadRequest)
		return
	}
	path, err := findBlob(b.stageRoot(), id)
	if err != nil {
		http.Error(w, "暂存文件不存在或已过期", http.StatusNotFound)
		return
	}
	if !b.canAccess(r, filepath.Dir(path)) {
		http.Error(w, "暂存文件不存在或已过期", http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "暂存文件不可读", http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "暂存文件不可读", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", blobContentDisposition(filepath.Base(path)))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func (b *BlobStore) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !ids.Valid(id) {
		http.Error(w, "id 参数无效", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(b.stageRoot(), id)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "暂存文件不存在或已过期", http.StatusNotFound)
		return
	}
	if err != nil || !info.IsDir() {
		http.Error(w, "暂存文件不存在或已过期", http.StatusNotFound)
		return
	}
	if !b.canAccess(r, dir) {
		http.Error(w, "暂存文件不存在或已过期", http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, "删除失败，请稍后重试", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *BlobStore) writeOwnerMeta(dir string, r *http.Request) error {
	identity := accountIdentityFrom(r.Context())
	if identity == nil {
		return nil
	}
	encoded, err := json.Marshal(blobMeta{OwnerID: identity.UserID})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, blobMetaName), encoded, 0o600)
}

// canAccess 校验请求身份是暂存条目属主或超管；无属主元数据的存量条目
// (未启用账号或本改动前的暂存) 保持放行。拒绝与不存在同样回 404, 不泄露存在性。
func (b *BlobStore) canAccess(r *http.Request, dir string) bool {
	identity := accountIdentityFrom(r.Context())
	userID := ""
	superadmin := false
	if identity != nil {
		userID = identity.UserID
		superadmin = identity.Role == account.RoleSuperadmin
	}
	return b.canAccessDir(dir, userID, superadmin)
}

// canAccessDir 是 canAccess 的按身份判等拆分, 供 HTTP 面与 IPC 面共用;
// userID 空串表示无身份 (静态令牌/网关/auth=off)。
func (b *BlobStore) canAccessDir(dir, userID string, superadmin bool) bool {
	raw, err := os.ReadFile(filepath.Join(dir, blobMetaName))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	var meta blobMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	if meta.OwnerID == "" {
		return true
	}
	return userID != "" && (userID == meta.OwnerID || superadmin)
}

// ResolveStagedPath 把 Stage/Reserve 响应里的相对 path (id/name) 解析为暂存文件的服务器
// 绝对路径, 供服务端 IPC (fs_upload/fs_download/terminal_record_start 等) 定位浏览器暂存
// 文件; 响应本身不含绝对路径, 解析只在服务端发生。userID 是 ipc 会话注入的操作用户,
// 属主口径与 HTTP 面一致 (IPC 面无角色信息, 超管不能代解析他人暂存)。persist 条目不在
// 暂存根, 不能经此解析。name 必须等于 SafeBlobName(name) (即暂存落盘名), 拒绝大小写、
// 控制字符、前导点等变体, 如 .META.JSON 在大小写不敏感文件系统上会与属主元数据碰撞。
func (b *BlobStore) ResolveStagedPath(rel, userID string) (string, error) {
	id, name, found := strings.Cut(rel, "/")
	if !found || !ids.Valid(id) || name == "" || name != SafeBlobName(name) {
		return "", errStagedNotFound
	}
	dir := filepath.Join(b.stageRoot(), id)
	if !b.canAccessDir(dir, userID, false) {
		return "", errStagedNotFound
	}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errStagedNotFound
	}
	return path, nil
}

func (b *BlobStore) createItemDir(root string) (string, string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	for range 3 {
		id := b.newID()
		if !ids.Valid(id) {
			return "", "", fmt.Errorf("invalid generated blob id")
		}
		dir := filepath.Join(root, id)
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return dir, id, err
	}
	return "", "", fmt.Errorf("blob id collision")
}

func (b *BlobStore) checkPersistQuota(incoming int64) error {
	usage, err := b.persistUsage()
	if err != nil {
		return err
	}
	quota := b.persistQuotaBytes()
	if usage >= quota || incoming > 0 && usage+incoming > quota {
		return errBlobPersistQuota
	}
	return nil
}

func (b *BlobStore) checkDiskThreshold() error {
	if err := os.MkdirAll(b.dataDir, 0o700); err != nil {
		return err
	}
	usage, err := b.diskUsage(b.dataDir)
	if err != nil {
		return err
	}
	if usage*100 >= b.diskThresholdPercent() {
		return errBlobDiskThreshold
	}
	return nil
}

func (b *BlobStore) persistUsage() (int64, error) {
	var usage int64
	err := filepath.WalkDir(b.keepRoot(), func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() || entry.Name() == blobMetaName {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		usage += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return usage, err
}

func SafeBlobName(raw string) string {
	if index := strings.LastIndexAny(raw, `/\`); index >= 0 {
		raw = raw[index+1:]
	}
	raw = strings.TrimSpace(raw)
	cleaned := strings.Map(func(char rune) rune {
		if char == 0 || unicode.IsControl(char) {
			return '_'
		}
		return char
	}, raw)
	cleaned = strings.TrimSpace(strings.TrimLeft(cleaned, "."))
	if cleaned == "" {
		return "blob"
	}
	runes := []rune(cleaned)
	if len(runes) > 120 {
		runes = runes[:120]
	}
	for len(string(runes)) > 240 {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func findBlob(root, id string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, id))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() == blobMetaName {
			continue
		}
		if entry.Type().IsRegular() {
			return filepath.Join(root, id, entry.Name()), nil
		}
	}
	return "", os.ErrNotExist
}

func (b *BlobStore) SweepOnce(ctx context.Context) (int, error) {
	root := b.stageRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	now := b.now()
	removed := 0
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		age := now.Sub(info.ModTime())
		if age < 0 || age <= b.ttl {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func (b *BlobStore) RunSweeper(ctx context.Context) {
	interval := b.sweepEvery
	if interval <= 0 {
		interval = DefaultBlobSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, err := b.SweepOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				b.logger.Warn("blob sweep failed", "error", err)
			}
			if removed > 0 {
				b.logger.Info("expired staged blobs removed", "count", removed)
			}
		}
	}
}

func optionalBool(r *http.Request, name string) (bool, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func writeBlobJSON(w http.ResponseWriter, blob stagedBlob) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		OK   bool       `json:"ok"`
		Data stagedBlob `json:"data"`
	}{OK: true, Data: blob})
}

func blobContentDisposition(name string) string {
	var encoded strings.Builder
	for _, value := range []byte(name) {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value)) {
			encoded.WriteByte(value)
		} else {
			_, _ = fmt.Fprintf(&encoded, "%%%02X", value)
		}
	}
	return "attachment; filename*=UTF-8''" + encoded.String()
}
