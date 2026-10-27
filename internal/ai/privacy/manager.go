package privacy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
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
	Error     string           `json:"error,omitempty"`
}

type Status struct {
	Config    Config         `json:"config"`
	Installed []Installation `json:"installed"`
	Download  DownloadState  `json:"download"`
	Latest    *ModelRelease  `json:"latest,omitempty"`
}

type Manager struct {
	settings     Settings
	protector    store.SecretProtector
	root         string
	shared       bool
	catalog      *Catalog
	load         EngineLoader
	mu           sync.Mutex
	downloads    map[string]DownloadState
	cancels      map[string]context.CancelFunc
	latest       *ModelRelease
	closed       bool
	stopped      atomic.Bool
	workers      sync.WaitGroup
	engineMu     sync.Mutex
	engine       Engine
	engineID     string
	lifetime     context.Context
	stop         context.CancelFunc
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

func NewManager(settings Settings, root string, shared bool, load EngineLoader) *Manager {
	lifetime, stop := context.WithCancel(context.Background())
	m := &Manager{lifetime: lifetime, stop: stop, shutdownDone: make(chan struct{}), settings: settings, root: root, shared: shared, catalog: NewCatalog(), load: load, downloads: map[string]DownloadState{}, cancels: map[string]context.CancelFunc{}}
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

func (m *Manager) Status(ctx context.Context) (Status, error) {
	id, err := owner.ID(ctx, m.shared)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.read(ctx, id)
	if err != nil {
		return Status{}, err
	}
	status := Status{Config: state.Config, Installed: []Installation{}, Download: m.downloads[id]}
	if m.latest != nil {
		copy := *m.latest
		status.Latest = &copy
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
	}
	sort.Slice(status.Installed, func(i, j int) bool {
		return status.Installed[i].Revision+string(status.Installed[i].Precision) < status.Installed[j].Revision+string(status.Installed[j].Precision)
	})
	return status, nil
}

func (m *Manager) CheckUpdates(ctx context.Context) (ModelRelease, error) {
	if _, err := owner.ID(ctx, m.shared); err != nil {
		return ModelRelease{}, err
	}
	release, err := m.catalog.Latest(ctx)
	if err != nil {
		return release, err
	}
	m.mu.Lock()
	m.latest = &release
	m.mu.Unlock()
	return release, nil
}

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
	state, err := m.read(ctx, id)
	if err == nil {
		next := Config{Enabled: enabled, Precision: precision, Revision: revision}
		if !enabled && revision == state.Revision && precision == state.Precision {
			next.Version = state.Version
		} else if revision != "" {
			var installation Installation
			installation, err = ReadInstallation(m.modelDir(next))
			if errors.Is(err, os.ErrNotExist) {
				err = userError("本地隐私模型文件缺失，请重新下载模型")
			}
			if err == nil && (installation.Revision != revision || installation.Precision != precision) {
				err = userError("隐私模型选择与安装文件不匹配")
			}
			if err == nil {
				next.Version = installation.Version
			}
		}
		if err == nil && enabled {
			if revision == "" {
				err = userError("请先下载并选择本地隐私模型")
			} else {
				err = m.validateEngine(ctx, next)
			}
			if err == nil {
				_, err = m.seed(ctx, &state)
			}
		}
		if err == nil {
			state.Config = next
			err = m.save(ctx, id, state)
		}
	}
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
	m.engineMu.Lock()
	defer m.engineMu.Unlock()
	if err := m.ensureEngine(ctx, d.config); err != nil {
		return nil, err
	}
	return m.engine.Detect(ctx, text)
}
func (m *Manager) validateEngine(ctx context.Context, config Config) error {
	m.engineMu.Lock()
	defer m.engineMu.Unlock()
	return m.ensureEngine(ctx, config)
}
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
	if m.engine != nil {
		if err := m.engine.Close(); err != nil {
			return err
		}
		m.engine = nil
		m.engineID = ""
	}
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
	if !precision.Valid() {
		return Status{}, userError("无效的模型精度")
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
	err := func() error {
		release, err := m.catalog.Latest(ctx)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.latest = &release
		m.mu.Unlock()
		installation, err := m.catalog.Manifest(ctx, release, precision)
		if err != nil {
			return err
		}
		_, err = m.catalog.Download(ctx, m.root, installation, func(progress DownloadProgress) {
			m.mu.Lock()
			state := m.downloads[id]
			state.Progress = progress
			m.downloads[id] = state
			m.mu.Unlock()
		})
		if err != nil {
			return err
		}
		if err := ipc.CheckAccountSession(ctx); err != nil {
			return err
		}
		// Downloading does not activate protection or change an in-use selection.
		return nil
	}()
	m.mu.Lock()
	state := m.downloads[id]
	state.Running = false
	if err != nil {
		if errors.Is(err, context.Canceled) {
			state.Error = "下载已取消"
		} else {
			state.Error = fmt.Sprintf("模型下载失败：%v", err)
		}
	}
	m.downloads[id] = state
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
	}
	delete(m.cancels, id)
	m.mu.Unlock()
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
func (m *Manager) Start(context.Context) error { return nil }
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
			m.engineMu.Lock()
			defer m.engineMu.Unlock()
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
