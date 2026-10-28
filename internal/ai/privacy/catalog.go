package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
)

const modelRepository = "QuantumNous/astrlink-guard"
const maxModelFile = 512 << 20

// maxMetadataFile bounds the small metadata files of a release; only the weights may approach maxModelFile.
func maxMetadataFile(name string) int64 {
	switch name {
	case "config.json", "labels.json":
		return 4 << 20
	case "tokenizer.json":
		return 64 << 20
	default:
		return maxModelFile
	}
}

const (
	responseHeaderTimeout = 30 * time.Second
	defaultIdleTimeout    = 60 * time.Second
)

// ErrIntegrity marks downloaded bytes that do not match the published manifest.
var ErrIntegrity = errors.New("model integrity check failed")

// ErrStalled marks a transfer that made no progress within the idle timeout.
var ErrStalled = errors.New("model download stalled")

// HTTPStatusError reports an unexpected response from the model host.
type HTTPStatusError struct{ Code int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("model download returned HTTP %d", e.Code)
}

type Precision string

const (
	INT8 Precision = "int8"
	FP32 Precision = "fp32"
	FP16 Precision = "fp16"
)

func (p Precision) Valid() bool { return p == INT8 || p == FP32 || p == FP16 }

type ModelRelease struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

type ModelFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Installation struct {
	ModelRelease
	Precision Precision            `json:"precision"`
	Files     map[string]ModelFile `json:"files"`
}

type DownloadProgress struct {
	File       string `json:"file"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
}

type Catalog struct {
	client  *http.Client
	baseURL string
	// idleTimeout aborts a response body that delivers no bytes for this long.
	idleTimeout time.Duration
	// validate decides from a release's metadata files whether this runtime can
	// load it; nil uses the inference package's load-time checks.
	validate func(config, tokenizer []byte) error
}

// NewCatalog bounds the wait for response headers and for each stretch without
// progress instead of the whole transfer, so slow but steady links complete.
func NewCatalog() *Catalog {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &Catalog{client: &http.Client{Transport: transport}, baseURL: "https://huggingface.co", idleTimeout: defaultIdleTimeout}
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *Catalog) get(ctx context.Context, path string) (io.ReadCloser, error) {
	idle := c.idleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	ctx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	timer := time.AfterFunc(idle, func() { cancel(ErrStalled) })
	resp, err := c.client.Do(req)
	if err != nil {
		timer.Stop()
		cancel(nil)
		if errors.Is(context.Cause(ctx), ErrStalled) {
			return nil, ErrStalled
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		timer.Stop()
		resp.Body.Close()
		cancel(nil)
		return nil, &HTTPStatusError{Code: resp.StatusCode}
	}
	timer.Reset(idle)
	return &idleBody{ctx: ctx, body: resp.Body, timer: timer, idle: idle, cancel: cancel}, nil
}

// idleBody cancels its request when no bytes arrive within idle and reports
// that as ErrStalled; caller cancellation still surfaces as the context error.
type idleBody struct {
	ctx    context.Context
	body   io.ReadCloser
	timer  *time.Timer
	idle   time.Duration
	cancel context.CancelCauseFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && err != io.EOF && errors.Is(context.Cause(b.ctx), ErrStalled) {
		err = ErrStalled
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.body.Close()
	b.cancel(nil)
	return err
}

func (c *Catalog) readJSON(ctx context.Context, path string, value any) error {
	body, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 2<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

// Resolution is the outcome of looking for the newest usable release.
type Resolution struct {
	// Release is the newest release this app version can load.
	Release ModelRelease
	// NewerIncompatible is a newer stable release that cannot be loaded, if any.
	NewerIncompatible *ModelRelease
}

// Latest returns the newest compatible release.
func (c *Catalog) Latest(ctx context.Context) (ModelRelease, error) {
	resolution, err := c.Resolve(ctx)
	return resolution.Release, err
}

// Resolve picks the highest stable semver tag whose content this runtime can
// load, resolved to an immutable commit before any weights are fetched. Only
// content decides compatibility, never the version number.
func (c *Catalog) Resolve(ctx context.Context) (Resolution, error) {
	var refs struct {
		Tags []struct {
			Name   string `json:"name"`
			Commit string `json:"targetCommit"`
		} `json:"tags"`
	}
	if err := c.readJSON(ctx, "/api/models/"+modelRepository+"/refs", &refs); err != nil {
		return Resolution{}, err
	}
	var releases []ModelRelease
	for _, tag := range refs.Tags {
		if semver.IsValid(tag.Name) && semver.Prerelease(tag.Name) == "" && revisionPattern.MatchString(tag.Commit) {
			releases = append(releases, ModelRelease{Version: tag.Name, Revision: tag.Commit})
		}
	}
	slices.SortFunc(releases, func(a, b ModelRelease) int { return semver.Compare(b.Version, a.Version) })
	var resolution Resolution
	for _, release := range releases {
		ok, err := c.compatible(ctx, release)
		if err != nil {
			return Resolution{}, err
		}
		if ok {
			resolution.Release = release
			return resolution, nil
		}
		if resolution.NewerIncompatible == nil {
			skipped := release
			resolution.NewerIncompatible = &skipped
		}
	}
	return Resolution{NewerIncompatible: resolution.NewerIncompatible}, errors.New("no supported model release is available")
}

// compatible reports whether any precision of release passes the metadata
// checks. Only transport failures are returned as errors; content or layout
// the runtime cannot use makes the release incompatible.
func (c *Catalog) compatible(ctx context.Context, release ModelRelease) (bool, error) {
	for _, precision := range []Precision{INT8, FP32, FP16} {
		manifest, err := c.Manifest(ctx, release, precision)
		if err == nil {
			err = c.checkMetadata(ctx, manifest)
		}
		if err == nil {
			return true, nil
		}
		if ctx.Err() != nil || !isContentError(err) {
			return false, err
		}
	}
	return false, nil
}

var errIncompatibleRelease = errors.New("incompatible model release")

// errManifestEntry marks a manifest that lacks or malforms a required file.
var errManifestEntry = errors.New("invalid model manifest entry")

func isContentError(err error) bool {
	var status *HTTPStatusError
	var syntax *json.SyntaxError
	var typed *json.UnmarshalTypeError
	if errors.As(err, &status) {
		return status.Code >= 400 && status.Code < 500
	}
	return errors.As(err, &syntax) || errors.As(err, &typed) || errors.Is(err, errIncompatibleRelease) || errors.Is(err, errManifestEntry)
}

// checkMetadata fetches config.json and tokenizer.json of the pinned revision,
// confirms they match the manifest and runs the runtime's load-time checks.
func (c *Catalog) checkMetadata(ctx context.Context, installation Installation) error {
	fetch := func(name string, limit int64) ([]byte, error) {
		body, err := c.get(ctx, "/"+modelRepository+"/resolve/"+installation.Revision+"/"+string(installation.Precision)+"/"+name)
		if err != nil {
			return nil, err
		}
		defer body.Close()
		data, err := io.ReadAll(io.LimitReader(body, limit+1))
		if err != nil {
			return nil, err
		}
		entry := installation.Files[name]
		sum := sha256.Sum256(data)
		if int64(len(data)) > limit || int64(len(data)) != entry.Size || hex.EncodeToString(sum[:]) != entry.SHA256 {
			return nil, errIncompatibleRelease
		}
		return data, nil
	}
	config, err := fetch("config.json", 4<<20)
	if err != nil {
		return err
	}
	tokenizer, err := fetch("tokenizer.json", 64<<20)
	if err != nil {
		return err
	}
	return c.validateMetadata(config, tokenizer)
}

func (c *Catalog) validateMetadata(config, tokenizer []byte) error {
	validate := c.validate
	if validate == nil {
		validate = inference.ValidateMetadata
	}
	if err := validate(config, tokenizer); err != nil {
		return fmt.Errorf("%w: %w", errIncompatibleRelease, err)
	}
	return nil
}

func modelFileName(p Precision) string {
	switch p {
	case INT8:
		return "model_int8.onnx"
	case FP32:
		return "model.onnx"
	case FP16:
		return "model_fp16_static512.onnx"
	default:
		return ""
	}
}

func requiredModelFiles(p Precision) []string {
	files := []string{"config.json", "tokenizer.json", "labels.json", modelFileName(p)}
	if p != FP16 {
		files = append(files, modelFileName(p)+"_data")
	}
	return files
}

func (c *Catalog) Manifest(ctx context.Context, release ModelRelease, p Precision) (Installation, error) {
	installation := Installation{ModelRelease: release, Precision: p}
	if !p.Valid() || !revisionPattern.MatchString(release.Revision) {
		return installation, errors.New("invalid model release or precision")
	}
	path := "/" + modelRepository + "/resolve/" + release.Revision + "/" + string(p) + "/CHECKSUMS.json"
	if err := c.readJSON(ctx, path, &installation.Files); err != nil {
		return installation, err
	}
	for _, name := range requiredModelFiles(p) {
		file, ok := installation.Files[name]
		if !ok || !digestPattern.MatchString(file.SHA256) || file.Size <= 0 || file.Size > maxMetadataFile(name) {
			return installation, fmt.Errorf("%w: %s", errManifestEntry, name)
		}
	}
	// Only known files are downloaded; remote names never become local paths.
	for name := range installation.Files {
		if !slices.Contains(requiredModelFiles(p), name) {
			delete(installation.Files, name)
		}
	}
	return installation, nil
}

// Size is the total number of bytes the installation downloads.
func (i Installation) Size() int64 {
	var total int64
	for _, file := range i.Files {
		total += file.Size
	}
	return total
}

// installedMatches reports whether directory already holds this exact manifest
// and its files verify.
func installedMatches(ctx context.Context, directory string, installation Installation) bool {
	existing, err := ReadInstallation(directory)
	if err != nil || existing.Revision != installation.Revision || existing.Precision != installation.Precision || !maps.Equal(existing.Files, installation.Files) {
		return false
	}
	return VerifyInstallation(ctx, directory, existing) == nil
}

// Download writes a new installation directory and only publishes it after all
// hashes match. An identical verified installation is reused without network
// access. release is called before an existing directory is replaced so the
// caller can close anything that still uses its files.
func (c *Catalog) Download(ctx context.Context, root string, installation Installation, progress func(DownloadProgress), release func(directory string) error) (string, error) {
	if !installation.Precision.Valid() || !revisionPattern.MatchString(installation.Revision) {
		return "", errors.New("invalid installation")
	}
	destination := filepath.Join(root, installation.Revision+"-"+string(installation.Precision))
	if installedMatches(ctx, destination, installation) {
		return destination, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(root, ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	var total, downloaded int64
	for _, name := range requiredModelFiles(installation.Precision) {
		file := installation.Files[name]
		if file.Size <= 0 || file.Size > maxMetadataFile(name) || !digestPattern.MatchString(file.SHA256) {
			return "", errors.New("invalid installation manifest")
		}
		total += file.Size
	}
	for _, name := range requiredModelFiles(installation.Precision) {
		file := installation.Files[name]
		path := "/" + modelRepository + "/resolve/" + installation.Revision + "/" + string(installation.Precision) + "/" + name
		body, err := c.get(ctx, path)
		if err != nil {
			return "", err
		}
		destination, err := os.OpenFile(filepath.Join(temporary, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			body.Close()
			return "", err
		}
		hash := sha256.New()
		writer := &progressWriter{writer: io.MultiWriter(destination, hash), onWrite: func(n int) {
			downloaded += int64(n)
			if progress != nil {
				progress(DownloadProgress{File: name, Downloaded: downloaded, Total: total})
			}
		}}
		n, copyErr := io.Copy(writer, io.LimitReader(body, file.Size+1))
		body.Close()
		syncErr := destination.Sync()
		closeErr := destination.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return "", err
		}
		if n != file.Size || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return "", fmt.Errorf("%w: %s", ErrIntegrity, name)
		}
	}
	// The bytes about to be published must also be loadable by this runtime, not only match the manifest.
	config, err := os.ReadFile(filepath.Join(temporary, "config.json"))
	if err != nil {
		return "", err
	}
	tokenizer, err := os.ReadFile(filepath.Join(temporary, "tokenizer.json"))
	if err != nil {
		return "", err
	}
	if err := c.validateMetadata(config, tokenizer); err != nil {
		return "", err
	}
	data, err := json.Marshal(installation)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporary, "installation.json"), data, 0600); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backup := ""
	if _, err := os.Lstat(destination); err == nil {
		if release != nil {
			if err := release(destination); err != nil {
				return "", err
			}
		}
		backup, err = os.MkdirTemp(root, ".replaced-")
		if err != nil {
			return "", err
		}
		if err := os.Remove(backup); err != nil {
			return "", err
		}
		if err := os.Rename(destination, backup); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		if backup != "" {
			err = errors.Join(err, os.Rename(backup, destination))
		}
		return "", fmt.Errorf("install model: %w", err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return destination, nil
}

type progressWriter struct {
	writer  io.Writer
	onWrite func(int)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.onWrite(n)
	return n, err
}

func ReadInstallation(directory string) (Installation, error) {
	var installation Installation
	file, err := os.Open(filepath.Join(directory, "installation.json"))
	if err != nil {
		return installation, err
	}
	defer file.Close()
	err = json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&installation)
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntaxError) || errors.As(err, &typeError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return installation, userError("本地隐私模型清单损坏，请重新下载模型")
	}
	if err == nil && (!installation.Precision.Valid() || !revisionPattern.MatchString(installation.Revision)) {
		err = userError("本地隐私模型清单损坏，请重新下载模型")
	}
	return installation, err
}

func VerifyInstallation(ctx context.Context, directory string, installation Installation) error {
	if !installation.Precision.Valid() {
		return userError("本地隐私模型精度无效，请重新下载模型")
	}
	for _, name := range requiredModelFiles(installation.Precision) {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, ok := installation.Files[name]
		if !ok || entry.Size <= 0 || entry.Size > maxModelFile || !digestPattern.MatchString(entry.SHA256) {
			return userError("本地隐私模型校验信息无效，请重新下载模型")
		}
		file, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(file, entry.Size+1))
		file.Close()
		if err != nil {
			return err
		}
		if n != entry.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), entry.SHA256) {
			return userError("本地隐私模型校验失败，请重新下载模型")
		}
	}
	return nil
}

// sweepStaleDirectories removes temporary directories that an interrupted
// download or replacement left behind. Call it only while no download runs.
func sweepStaleDirectories(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".download-") || strings.HasPrefix(name, ".replaced-") {
			if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// checkInstallation inspects a selected installation without hashing it: the
// manifest must parse and match, and every file must exist with its size.
func checkInstallation(directory string, config Config) (Installation, error) {
	installation, err := ReadInstallation(directory)
	if errors.Is(err, os.ErrNotExist) {
		return installation, userError("本地隐私模型文件缺失，请重新下载模型")
	}
	if err != nil {
		var safe *UserError
		if errors.As(err, &safe) {
			return installation, err
		}
		return installation, userError("无法读取本地隐私模型，请检查文件权限或重新下载")
	}
	if installation.Revision != config.Revision || installation.Precision != config.Precision {
		return installation, userError("隐私模型清单不匹配，请重新下载模型")
	}
	for _, name := range requiredModelFiles(installation.Precision) {
		entry, ok := installation.Files[name]
		info, err := os.Stat(filepath.Join(directory, name))
		if !ok || err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			return installation, userError("本地隐私模型文件缺失或不完整，请重新下载模型")
		}
	}
	return installation, nil
}
