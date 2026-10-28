package privacy

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type Settings interface {
	SettingGet(context.Context, string) (string, bool, error)
	SettingSet(context.Context, string, string) error
}

type Engine interface {
	Detector
	Close() error
}
type EngineLoader func(context.Context, string, Installation) (Engine, error)

type Config struct {
	Enabled   bool      `json:"enabled"`
	Precision Precision `json:"precision"`
	Revision  string    `json:"revision"`
	Version   string    `json:"version"`
}

type savedConfig struct {
	Config
	Seed string `json:"seed,omitempty"`
}

type DownloadState struct {
	Running   bool             `json:"running"`
	Precision Precision        `json:"precision"`
	Progress  DownloadProgress `json:"progress"`
	// Error is a fixed summary; Detail carries the raw cause as secondary text.
	Error    string `json:"error,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
	// Completed names the release installed by the last finished download.
	Completed *ModelRelease `json:"completed,omitempty"`
}

// RuntimeStatus reports whether this device can run the local engine at all.
type RuntimeStatus struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

// SelectionStatus reports the configured model's health without hashing it.
type SelectionStatus struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type Status struct {
	Config    Config         `json:"config"`
	Installed []Installation `json:"installed"`
	Download  DownloadState  `json:"download"`
	Latest    *ModelRelease  `json:"latest,omitempty"`
	// LatestSizes is the download size of each precision of Latest, when known.
	LatestSizes map[Precision]int64 `json:"latestSizes,omitempty"`
	Runtime     RuntimeStatus       `json:"runtime"`
	Selection   *SelectionStatus    `json:"selection,omitempty"`
	DiskUsage   int64               `json:"diskUsage"`
	// CanManage is false for server accounts that may not download or delete
	// the instance-wide model files.
	CanManage bool `json:"canManage"`
}

// RuntimeDirectory resolves the packaged inference libraries, honoring the
// NEXTERM_AI_RUNTIME_DIR override.
func RuntimeDirectory() string {
	if directory := os.Getenv("NEXTERM_AI_RUNTIME_DIR"); directory != "" {
		return directory
	}
	return inference.DefaultRuntimeDirectory()
}

func defaultRuntimeCheck() error { return inference.Available(RuntimeDirectory()) }

type Manager struct {
	settings    Settings
	protector   store.SecretProtector
	root        string
	shared      bool
	catalog     *Catalog
	load        EngineLoader
	runtime     func() error
	mu          sync.Mutex
	downloads   map[string]DownloadState
	cancels     map[string]context.CancelFunc
	latest      *ModelRelease
	latestSizes map[Precision]int64
	closed      bool
	stopped     atomic.Bool
	workers     sync.WaitGroup
	// engineSlot serializes engine use; waiting on it honors the caller's
	// context. Never acquire mu while holding the slot.
	engineSlot   chan struct{}
	engine       Engine
	engineID     string
	failureMu    sync.Mutex
	failures     map[string]string
	lifetime     context.Context
	stop         context.CancelFunc
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

func NewManager(settings Settings, root string, shared bool, load EngineLoader) *Manager {
	lifetime, stop := context.WithCancel(context.Background())
	m := &Manager{
		lifetime: lifetime, stop: stop, shutdownDone: make(chan struct{}),
		settings: settings, root: root, shared: shared, catalog: NewCatalog(), load: load, runtime: defaultRuntimeCheck,
		downloads: map[string]DownloadState{}, cancels: map[string]context.CancelFunc{},
		engineSlot: make(chan struct{}, 1), failures: map[string]string{},
	}
	if source, ok := settings.(interface{ SecretProtector() store.SecretProtector }); ok {
		m.protector = source.SecretProtector()
	}
	return m
}

func configKey(id string) string {
	if id == "" {
		return "ai.privacy"
	}
	return "user.ai.privacy." + id
}
func (m *Manager) read(ctx context.Context, id string) (savedConfig, error) {
	state := savedConfig{Config: Config{Precision: INT8}}
	raw, found, err := m.settings.SettingGet(ctx, configKey(id))
	if err != nil || !found {
		return state, err
	}
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return state, userError("AI 隐私设置损坏，请重新配置")
	}
	if !state.Precision.Valid() {
		return state, userError("AI 隐私模型精度无效")
	}
	if state.Revision != "" && !revisionPattern.MatchString(state.Revision) {
		return state, userError("AI 隐私模型版本无效")
	}
	return state, nil
}
func (m *Manager) save(ctx context.Context, id string, state savedConfig) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return m.settings.SettingSet(ctx, configKey(id), string(data))
}
func (m *Manager) modelDir(config Config) string {
	return filepath.Join(m.root, config.Revision+"-"+string(config.Precision))
}

// canManage limits instance-wide model files to the desktop user, an open
// deployment, or a server superadmin.
func (m *Manager) canManage(ctx context.Context) bool {
	if m.shared || owner.OpenAccess(ctx) {
		return true
	}
	role, ok := ipc.RoleFromContext(ctx)
	return ok && role == "superadmin"
}
func (m *Manager) authorizeManage(ctx context.Context) error {
	if !m.canManage(ctx) {
		return ipc.NewError(ipc.CodeForbidden, "仅管理员可以下载、删除或检查本地隐私模型")
	}
	return nil
}

func (m *Manager) runtimeError() error {
	if m.runtime == nil {
		return nil
	}
	return m.runtime()
}

// safeMessage returns UI-safe text for an error; other diagnostics are hidden.
func safeMessage(err error, fallback string) string {
	var safe *UserError
	var platform *inference.PlatformError
	switch {
	case errors.As(err, &safe):
		return safe.Message
	case errors.As(err, &platform):
		return platform.Message
	case errors.Is(err, inference.ErrUnavailable):
		return "未找到可用的本地推理运行时，请安装支持本设备的完整 NexTerm 版本"
	default:
		return fallback
	}
}

// clearNoticeLocked drops a finished download's result once the owner acts again.
func (m *Manager) clearNoticeLocked(id string) {
	if state, ok := m.downloads[id]; ok && !state.Running {
		delete(m.downloads, id)
	}
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	state, err := m.read(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return Status{}, err
	}
	status := Status{Config: state.Config, Installed: []Installation{}, Download: m.downloads[id], CanManage: m.canManage(ctx)}
	if m.latest != nil {
		latest := *m.latest
		status.Latest = &latest
		if len(m.latestSizes) > 0 {
			status.LatestSizes = make(map[Precision]int64, len(m.latestSizes))
			for precision, size := range m.latestSizes {
				status.LatestSizes[precision] = size
			}
		}
	}
	m.mu.Unlock()
	status.Runtime.Supported = true
	if err := m.runtimeError(); err != nil {
		status.Runtime = RuntimeStatus{Reason: safeMessage(err, "本地推理运行时不可用")}
	}
	if state.Revision != "" {
		directory := m.modelDir(state.Config)
		selection := SelectionStatus{Available: true}
		if _, err := checkInstallation(directory, state.Config); err != nil {
			selection = SelectionStatus{Error: safeMessage(err, "本地隐私模型不可用")}
		} else if failure := m.failure(directory); failure != "" {
			selection = SelectionStatus{Error: failure}
		}
		status.Selection = &selection
	}
	entries, err := os.ReadDir(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return Status{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		installation, err := ReadInstallation(filepath.Join(m.root, entry.Name()))
		if err != nil {
			continue
		}
		if entry.Name() != installation.Revision+"-"+string(installation.Precision) {
			continue
		}
		status.Installed = append(status.Installed, installation)
		status.DiskUsage += installation.Size()
	}
	sort.Slice(status.Installed, func(i, j int) bool {
		return status.Installed[i].Revision+string(status.Installed[i].Precision) < status.Installed[j].Revision+string(status.Installed[j].Precision)
	})
	return status, nil
}

// CheckUpdates resolves the newest supported release and the download size of
// each precision so the UI can show them before anything is downloaded.
func (m *Manager) CheckUpdates(ctx context.Context) (ModelRelease, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return ModelRelease{}, err
	}
	if err := m.authorizeManage(ctx); err != nil {
		return ModelRelease{}, err
	}
	m.mu.Lock()
	m.clearNoticeLocked(id)
	m.mu.Unlock()
	release, err := m.catalog.Latest(ctx)
	if err != nil {
		return release, err
	}
	sizes := map[Precision]int64{}
	var firstErr error
	for _, precision := range []Precision{INT8, FP32, FP16} {
		manifest, err := m.catalog.Manifest(ctx, release, precision)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			continue
		}
		sizes[precision] = manifest.Size()
	}
	if len(sizes) == 0 {
		return release, firstErr
	}
	m.mu.Lock()
	m.latest = &release
	m.latestSizes = sizes
	m.mu.Unlock()
	return release, nil
}

// Configure validates and loads the engine without holding mu, so status
// reads and requests continue meanwhile, then saves only if no other change
// landed in between.
func (m *Manager) Configure(ctx context.Context, enabled bool, precision Precision, revision string) (Status, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Status{}, err
	}
	if !precision.Valid() || (revision != "" && !revisionPattern.MatchString(revision)) {
		return Status{}, userError("无效的隐私模型选择")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, userError("隐私服务已关闭")
	}
	m.clearNoticeLocked(id)
	state, err := m.read(ctx, id)
	m.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	next := Config{Enabled: enabled, Precision: precision, Revision: revision}
	if !enabled && revision == state.Revision && precision == state.Precision {
		next.Version = state.Version
	} else if revision != "" {
		installation, err := checkInstallation(m.modelDir(next), next)
		if err != nil {
			return Status{}, err
		}
		next.Version = installation.Version
	}
	if enabled {
		if revision == "" {
			return Status{}, userError("请先下载并选择本地隐私模型")
		}
		if err := m.runtimeError(); err != nil {
			return Status{}, err
		}
		if err := m.validateEngine(ctx, next); err != nil {
			return Status{}, err
		}
	}
	m.mu.Lock()
	err = func() error {
		if m.closed {
			return userError("隐私服务已关闭")
		}
		current, err := m.read(ctx, id)
		if err != nil {
			return err
		}
		if current.Config != state.Config {
			return userError("隐私设置已被其他操作修改，请刷新后重试")
		}
		if enabled {
			if _, err := m.seed(ctx, &current); err != nil {
				return err
			}
		}
		current.Config = next
		return m.save(ctx, id, current)
	}()
	m.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return m.Status(ctx)
}

func (m *Manager) seed(ctx context.Context, state *savedConfig) ([32]byte, error) {
	var seed [32]byte
	if m.protector == nil {
		return seed, userError("凭据保护服务不可用")
	}
	if state.Seed == "" {
		if _, err := rand.Read(seed[:]); err != nil {
			return seed, err
		}
		encrypted, err := m.protector.EncryptSecret(ctx, hex.EncodeToString(seed[:]))
		if err != nil {
			return [32]byte{}, err
		}
		state.Seed = encrypted
		return seed, nil
	}
	value, err := m.protector.DecryptSecret(ctx, state.Seed)
	if err != nil {
		return seed, err
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(seed) {
		return seed, userError("隐私会话密钥无效")
	}
	copy(seed[:], decoded)
	return seed, nil
}

func (m *Manager) Exchange(ctx context.Context) (*Exchange, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, userError("隐私服务已关闭")
	}
	state, err := m.read(ctx, id)
	if err != nil || !state.Enabled {
		m.mu.Unlock()
		return nil, err
	}
	previous := state.Seed
	seed, err := m.seed(ctx, &state)
	if err == nil && state.Seed != previous {
		err = m.save(ctx, id, state)
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	detector := managedDetector{manager: m, config: state.Config}
	conversation := ConversationID(ctx)
	if conversation == "" {
		return NewExchange(detector)
	}
	return NewScopedExchange(detector, deriveScopeKey(seed, id, conversation))
}

type managedDetector struct {
	manager *Manager
	config  Config
}

func (d managedDetector) Detect(ctx context.Context, text string) ([]Span, error) {
	m := d.manager
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.lifetime, cancel)
	defer stop()
	if err := m.acquireEngine(ctx); err != nil {
		return nil, err
	}
	defer m.releaseEngine()
	if err := m.ensureEngine(ctx, d.config); err != nil {
		return nil, err
	}
	spans, err := m.engine.Detect(ctx, text)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, inference.ErrTokenLimit) {
		// The native engine may be in an undefined state; the next request reloads it.
		m.dropEngine()
	}
	return spans, err
}

func (m *Manager) acquireEngine(ctx context.Context) error {
	select {
	case m.engineSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) releaseEngine() { <-m.engineSlot }

// dropEngine closes the cached engine; the caller holds the engine slot.
func (m *Manager) dropEngine() {
	if m.engine != nil {
		_ = m.engine.Close()
		m.engine = nil
		m.engineID = ""
	}
}

// releaseDirectory closes the cached engine if it uses directory, so its files
// can be replaced or removed.
func (m *Manager) releaseDirectory(ctx context.Context, directory string) error {
	if err := m.acquireEngine(ctx); err != nil {
		return err
	}
	defer m.releaseEngine()
	if m.engineID == directory {
		m.dropEngine()
	}
	m.setFailure(directory, "")
	return nil
}

func (m *Manager) failure(directory string) string {
	m.failureMu.Lock()
	defer m.failureMu.Unlock()
	return m.failures[directory]
}
func (m *Manager) setFailure(directory, message string) {
	m.failureMu.Lock()
	defer m.failureMu.Unlock()
	if message == "" {
		delete(m.failures, directory)
	} else {
		m.failures[directory] = message
	}
}

func (m *Manager) validateEngine(ctx context.Context, config Config) error {
	if err := m.acquireEngine(ctx); err != nil {
		return err
	}
	defer m.releaseEngine()
	return m.ensureEngine(ctx, config)
}

// ensureEngine loads config's engine; the caller holds the engine slot. A
// failure other than cancellation is remembered for Status until a load succeeds.
func (m *Manager) ensureEngine(ctx context.Context, config Config) error {
	if m.stopped.Load() {
		return userError("隐私服务已关闭")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if config.Revision == "" {
		return userError("本地隐私模型尚未下载")
	}
	directory := m.modelDir(config)
	if m.engine != nil && m.engineID == directory {
		return nil
	}
	err := m.loadEngine(ctx, directory, config)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		m.setFailure(directory, safeMessage(err, "本地隐私模型加载失败"))
	} else if err == nil {
		m.setFailure(directory, "")
	}
	return err
}
func (m *Manager) loadEngine(ctx context.Context, directory string, config Config) error {
	if m.load == nil {
		return userError("本地隐私推理运行时不可用")
	}
	installation, err := ReadInstallation(directory)
	if errors.Is(err, os.ErrNotExist) {
		err = userError("本地隐私模型文件缺失，请重新下载模型")
	}
	if err != nil {
		return err
	}
	if installation.Revision != config.Revision || installation.Precision != config.Precision {
		return userError("隐私模型清单不匹配")
	}
	if err := VerifyInstallation(ctx, directory, installation); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return userError("本地隐私模型文件缺失，请重新下载模型")
		}
		return err
	}
	m.dropEngine()
	engine, err := m.load(ctx, directory, installation)
	if err != nil {
		return err
	}
	m.engine = engine
	m.engineID = directory
	return nil
}

func (m *Manager) StartDownload(ctx context.Context, precision Precision) (Status, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Status{}, err
	}
	if err := m.authorizeManage(ctx); err != nil {
		return Status{}, err
	}
	if !precision.Valid() {
		return Status{}, userError("无效的模型精度")
	}
	if err := m.runtimeError(); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, userError("隐私服务已关闭")
	}
	// Public weights are shared, but progress and cancellation belong to the initiator.
	if len(m.cancels) > 0 {
		m.mu.Unlock()
		return Status{}, userError("当前实例已有隐私模型下载任务，请稍后重试")
	}
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.cancels[id] = cancel
	m.downloads[id] = DownloadState{Running: true, Precision: precision}
	m.workers.Add(1)
	m.mu.Unlock()
	go m.download(jobCtx, id, precision)
	return m.Status(ctx)
}
func (m *Manager) download(ctx context.Context, id string, precision Precision) {
	defer m.workers.Done()
	var installed ModelRelease
	err := func() error {
		release, err := m.catalog.Latest(ctx)
		if err != nil {
			return err
		}
		installation, err := m.catalog.Manifest(ctx, release, precision)
		if err != nil {
			return err
		}
		m.mu.Lock()
		if m.latest == nil || m.latest.Revision != release.Revision {
			m.latestSizes = map[Precision]int64{}
		}
		m.latest = &release
		m.latestSizes[precision] = installation.Size()
		m.mu.Unlock()
		_, err = m.catalog.Download(ctx, m.root, installation, func(progress DownloadProgress) {
			m.mu.Lock()
			state := m.downloads[id]
			state.Progress = progress
			m.downloads[id] = state
			m.mu.Unlock()
		}, func(directory string) error { return m.releaseDirectory(ctx, directory) })
		if err != nil {
			return err
		}
		if err := ipc.CheckAccountSession(ctx); err != nil {
			return err
		}
		installed = release
		return nil
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.downloads[id]
	state.Running = false
	switch {
	case err == nil:
		state.Completed = &installed
		// Select the first model so it can be enabled, but never enable it.
		if current, readErr := m.read(ctx, id); readErr == nil && current.Revision == "" && !current.Enabled {
			current.Config = Config{Precision: precision, Revision: installed.Revision, Version: installed.Version}
			_ = m.save(ctx, id, current)
		}
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		state.Error, state.Canceled = "下载已取消", true
	default:
		state.Error, state.Detail = describeDownloadError(err)
	}
	m.downloads[id] = state
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
	}
	delete(m.cancels, id)
}
func (m *Manager) CancelDownload(ctx context.Context) error {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
	}
	return nil
}

// Remove deletes an installed model. The selection that protection currently
// uses cannot be removed while enabled; a disabled selection is cleared.
func (m *Manager) Remove(ctx context.Context, revision string, precision Precision) (Status, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Status{}, err
	}
	if err := m.authorizeManage(ctx); err != nil {
		return Status{}, err
	}
	if !precision.Valid() || !revisionPattern.MatchString(revision) {
		return Status{}, userError("无效的隐私模型选择")
	}
	target := Config{Precision: precision, Revision: revision}
	directory := m.modelDir(target)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, userError("隐私服务已关闭")
	}
	m.clearNoticeLocked(id)
	state, err := m.read(ctx, id)
	m.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	if state.Enabled && state.Revision == revision && state.Precision == precision {
		return Status{}, userError("该模型正在用于脱敏，请先关闭脱敏或切换到其他模型后再删除")
	}
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		return Status{}, userError("模型不存在或已被删除")
	} else if err != nil {
		return Status{}, err
	}
	if err := m.releaseDirectory(ctx, directory); err != nil {
		return Status{}, err
	}
	if err := os.RemoveAll(directory); err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	if current, err := m.read(ctx, id); err == nil && !current.Enabled && current.Revision == revision && current.Precision == precision {
		current.Revision, current.Version = "", ""
		err = m.save(ctx, id, current)
		if err != nil {
			m.mu.Unlock()
			return Status{}, err
		}
	}
	m.mu.Unlock()
	return m.Status(ctx)
}

// Start removes leftovers of interrupted downloads and, when the shared
// configuration is enabled, loads its engine in the background so the first
// request does not pay for hashing and loading.
func (m *Manager) Start(ctx context.Context) error {
	_ = sweepStaleDirectories(m.root)
	if !m.shared {
		return nil
	}
	state, err := m.read(ctx, "")
	if err != nil || !state.Enabled || m.runtimeError() != nil {
		return nil
	}
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		if err := m.acquireEngine(m.lifetime); err != nil {
			return
		}
		defer m.releaseEngine()
		_ = m.ensureEngine(m.lifetime, state.Config)
	}()
	return nil
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.shutdownOnce.Do(func() {
		m.stopped.Store(true)
		m.stop()
		go func() {
			defer close(m.shutdownDone)
			m.mu.Lock()
			m.closed = true
			for _, cancel := range m.cancels {
				cancel()
			}
			m.mu.Unlock()
			m.workers.Wait()
			m.engineSlot <- struct{}{}
			defer m.releaseEngine()
			if m.engine != nil {
				m.shutdownErr = m.engine.Close()
				m.engine = nil
				m.engineID = ""
			}
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.shutdownDone:
		return m.shutdownErr
	}
}
