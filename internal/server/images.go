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
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	DefaultImageMaxBytes      int64 = 20 << 20
	DefaultImageOwnerQuota    int64 = 200 << 20
	DefaultImageTTL                 = 24 * time.Hour
	MaxImageTTL                     = 7 * 24 * time.Hour
	DefaultImageSweepInterval       = 10 * time.Minute
	imageCopyBuffer                 = 256 << 10
	imageSniffBytes                 = 512
)

// ImageSharedOwner 是无账号模式(--auth=off 或回环豁免)下图片的共享工作区归属。
// 它只是配额与删除授权的归组标识, 不携带任何账号或超管能力。
const ImageSharedOwner = "shared"

var (
	errImageTooLarge = errors.New("image exceeds the maximum allowed size")
	errImageQuota    = errors.New("image storage quota exceeded for this owner")
	errImageMIME     = errors.New("unsupported image type")
)

var imageAllowedMIME = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

type ImageMeta struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	MIME      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Name      string `json:"name,omitempty"`
}

type ImageStore struct {
	dataDir    string
	ttl        time.Duration
	sweepEvery time.Duration
	maxBytes   int64
	ownerQuota int64
	newID      func() string
	now        func() time.Time
	logger     *slog.Logger
	onExpire   func(ctx context.Context, meta *ImageMeta)
	mu         sync.Mutex

	stagingMu     sync.Mutex
	activeStaging map[string]struct{}
}

func NewImageStore(dataDir string, logger *slog.Logger) *ImageStore {
	if logger == nil {
		logger = slog.Default()
	}
	store := &ImageStore{
		dataDir: dataDir, ttl: DefaultImageTTL, sweepEvery: DefaultImageSweepInterval,
		maxBytes: DefaultImageMaxBytes, ownerQuota: DefaultImageOwnerQuota,
		newID: ids.New, now: time.Now, logger: logger,
	}
	if maxBytes, err := ParseImageMaxBytesEnv(os.Getenv); err != nil {
		logger.Warn("ignoring invalid image limit override", "error", err)
	} else if maxBytes > 0 {
		store.maxBytes = maxBytes
	}
	if quota, err := ParseImageOwnerQuotaEnv(os.Getenv); err != nil {
		logger.Warn("ignoring invalid image quota override", "error", err)
	} else if quota > 0 {
		store.ownerQuota = quota
	}
	if ttl, err := ParseImageTTLEnv(os.Getenv); err != nil {
		logger.Warn("ignoring invalid image TTL override", "error", err)
	} else if ttl > 0 {
		store.ttl = ttl
	}
	return store
}

func ParseImageMaxBytesEnv(getenv func(string) string) (int64, error) {
	return parseBlobByteEnv(getenv, "NEXTERM_IMAGE_MAX_BYTES")
}

func ParseImageOwnerQuotaEnv(getenv func(string) string) (int64, error) {
	return parseBlobByteEnv(getenv, "NEXTERM_IMAGE_OWNER_MAX_BYTES")
}

func ParseImageTTLEnv(getenv func(string) string) (time.Duration, error) {
	raw := getenv("NEXTERM_IMAGE_TTL")
	if raw == "" {
		return 0, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("NEXTERM_IMAGE_TTL must be a positive duration, got %q", raw)
	}
	if value > MaxImageTTL {
		value = MaxImageTTL
	}
	return value, nil
}

func (s *ImageStore) imageMaxBytes() int64 {
	if s.maxBytes > 0 {
		return s.maxBytes
	}
	return DefaultImageMaxBytes
}

func (s *ImageStore) ownerQuotaBytes() int64 {
	if s.ownerQuota > 0 {
		return s.ownerQuota
	}
	return DefaultImageOwnerQuota
}

func (s *ImageStore) ttlDuration() time.Duration {
	if s.ttl > 0 {
		if s.ttl > MaxImageTTL {
			return MaxImageTTL
		}
		return s.ttl
	}
	return DefaultImageTTL
}

func (s *ImageStore) root() string { return filepath.Join(s.dataDir, "images") }

const (
	imageDataName = "content"
	imageMetaName = "meta.json"
	// imageStagingName 是在途上传的暂存目录, 不参与公开读取;
	// 提交以 rename 原子发布, 清扫只回收超龄暂存。
	imageStagingName = ".staging"
	stagingMaxAge    = time.Hour
)

// Put 校验并落盘一张图片: 大小上限、按 owner 配额、内容嗅探 MIME 白名单。
// 请求体在锁外写入暂存目录(慢客户端不阻塞其他写操作), 提交时在锁内
// 以实际字节重新校验配额, 再原子发布; 任何失败都会清理暂存。
func (s *ImageStore) Put(owner string, src io.Reader, declared int64, name string) (*ImageMeta, error) {
	limit := s.imageMaxBytes()
	if declared > limit {
		return nil, errImageTooLarge
	}
	if declared > 0 {
		if usage, err := s.ownerUsage(owner); err == nil && usage+declared > s.ownerQuotaBytes() {
			return nil, errImageQuota
		}
	}
	dir, id, err := s.createStagingDir()
	if err != nil {
		return nil, err
	}
	untrack := s.trackStaging(id)
	defer untrack()
	discard := func() { _ = os.RemoveAll(dir) }
	file, err := os.OpenFile(filepath.Join(dir, imageDataName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		discard()
		return nil, err
	}
	written, mime, copyErr := s.writeSniffed(file, src, limit)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		discard()
		if errors.Is(copyErr, errImageTooLarge) {
			return nil, errImageTooLarge
		}
		return nil, errors.Join(copyErr, closeErr)
	}
	if _, allowed := imageAllowedMIME[mime]; !allowed {
		discard()
		return nil, errImageMIME
	}
	now := s.now()
	if name == "" {
		name = "image." + imageAllowedMIME[mime]
	}
	meta := &ImageMeta{
		ID: id, Owner: owner, MIME: mime, Bytes: written,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(s.ttlDuration()).UnixMilli(), Name: name,
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		discard()
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, imageMetaName), encoded, 0o600); err != nil {
		discard()
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	usage, err := s.ownerUsage(owner)
	if err != nil {
		discard()
		return nil, err
	}
	if usage+written > s.ownerQuotaBytes() {
		discard()
		return nil, errImageQuota
	}
	if err := os.Rename(dir, filepath.Join(s.root(), id)); err != nil {
		discard()
		return nil, err
	}
	return meta, nil
}

// writeSniffed 先嗅探前 512 字节判定 MIME, 再限流写入剩余内容。
func (s *ImageStore) writeSniffed(file *os.File, src io.Reader, limit int64) (int64, string, error) {
	sniff := make([]byte, imageSniffBytes)
	n, readErr := io.ReadFull(src, sniff)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return 0, "", readErr
	}
	mime := http.DetectContentType(sniff[:n])
	written := int64(n)
	if _, err := file.Write(sniff[:n]); err != nil {
		return 0, "", err
	}
	if written > limit {
		return written, mime, errImageTooLarge
	}
	rest, err := io.CopyBuffer(file, io.LimitReader(src, limit-written+1), make([]byte, imageCopyBuffer))
	written += rest
	if err != nil {
		return written, mime, err
	}
	if written > limit {
		return written, mime, errImageTooLarge
	}
	return written, mime, nil
}

// Open 返回未过期图片的 meta 与内容读取器; 过期视为不存在, 删除并写过期审计。
func (s *ImageStore) Open(ctx context.Context, id string) (*ImageMeta, *os.File, error) {
	if !ids.Valid(id) {
		return nil, nil, os.ErrNotExist
	}
	meta, err := s.readMeta(id)
	if err != nil {
		return nil, nil, err
	}
	if s.now().UnixMilli() >= meta.ExpiresAt {
		if err := os.RemoveAll(filepath.Join(s.root(), id)); err == nil {
			s.auditExpire(ctx, meta)
		}
		return nil, nil, os.ErrNotExist
	}
	if _, allowed := imageAllowedMIME[meta.MIME]; !allowed {
		return nil, nil, os.ErrNotExist
	}
	file, err := os.Open(filepath.Join(s.root(), id, imageDataName))
	if err != nil {
		return nil, nil, os.ErrNotExist
	}
	return meta, file, nil
}

// auditExpire 上报过期清理审计(不含 URL/令牌); 未注入时跳过。
func (s *ImageStore) auditExpire(ctx context.Context, meta *ImageMeta) {
	if s.onExpire == nil {
		return
	}
	s.onExpire(ctx, meta)
}

// Remove 删除图片并返回原 meta(供删除授权与审计使用); 不存在返回 os.ErrNotExist。
func (s *ImageStore) Remove(id string) (*ImageMeta, error) {
	if !ids.Valid(id) {
		return nil, os.ErrNotExist
	}
	meta, err := s.readMeta(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(s.root(), id)); err != nil {
		return nil, err
	}
	return meta, nil
}

func (s *ImageStore) readMeta(id string) (*ImageMeta, error) {
	raw, err := os.ReadFile(filepath.Join(s.root(), id, imageMetaName))
	if err != nil {
		return nil, os.ErrNotExist
	}
	var meta ImageMeta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ID != id || meta.Owner == "" {
		return nil, os.ErrNotExist
	}
	return &meta, nil
}

func (s *ImageStore) ownerUsage(owner string) (int64, error) {
	var usage int64
	root := s.root()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := s.readMeta(entry.Name())
		if err != nil || meta.Owner != owner {
			continue
		}
		if s.now().UnixMilli() >= meta.ExpiresAt {
			continue
		}
		usage += meta.Bytes
	}
	return usage, nil
}

func (s *ImageStore) createStagingDir() (string, string, error) {
	if err := os.MkdirAll(filepath.Join(s.root(), imageStagingName), 0o700); err != nil {
		return "", "", err
	}
	for range 3 {
		id := s.newID()
		if !ids.Valid(id) {
			return "", "", fmt.Errorf("invalid generated image id")
		}
		dir := filepath.Join(s.root(), imageStagingName, id)
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return dir, id, err
	}
	return "", "", fmt.Errorf("image id collision")
}

// trackStaging 登记在途上传; 返回的注销函数覆盖完成/失败/panic 路径。
// 进程重启后残留的暂存没有登记, 只能按年龄被 sweepStaging 回收。
func (s *ImageStore) trackStaging(id string) func() {
	s.stagingMu.Lock()
	if s.activeStaging == nil {
		s.activeStaging = map[string]struct{}{}
	}
	s.activeStaging[id] = struct{}{}
	s.stagingMu.Unlock()
	return func() {
		s.stagingMu.Lock()
		delete(s.activeStaging, id)
		s.stagingMu.Unlock()
	}
}

func (s *ImageStore) isStagingActive(id string) bool {
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	_, ok := s.activeStaging[id]
	return ok
}

// SweepOnce 清理所有过期图片目录, 返回清理数量。
// 在途上传的暂存目录不参与过期判定, 只回收超过 stagingMaxAge 的孤儿暂存。
func (s *ImageStore) SweepOnce(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(s.root())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	now := s.now()
	removed := 0
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == imageStagingName {
			stagingRemoved, err := s.sweepStaging(ctx, now)
			removed += stagingRemoved
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !ids.Valid(entry.Name()) {
			continue
		}
		meta, err := s.readMeta(entry.Name())
		if err != nil {
			if err := os.RemoveAll(filepath.Join(s.root(), entry.Name())); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
			continue
		}
		if now.UnixMilli() < meta.ExpiresAt {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.root(), entry.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
		s.auditExpire(ctx, meta)
	}
	return removed, errors.Join(errs...)
}

func (s *ImageStore) sweepStaging(ctx context.Context, now time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.root(), imageStagingName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !entry.IsDir() {
			continue
		}
		if s.isStagingActive(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if now.Sub(info.ModTime()) <= stagingMaxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.root(), imageStagingName, entry.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func (s *ImageStore) RunSweeper(ctx context.Context) {
	interval := s.sweepEvery
	if interval <= 0 {
		interval = DefaultImageSweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, err := s.SweepOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("image sweep failed", "error", err)
			}
			if removed > 0 {
				s.logger.Info("expired images removed", "count", removed)
			}
		}
	}
}
