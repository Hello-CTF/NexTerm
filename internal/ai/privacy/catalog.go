package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const modelRepository = "QuantumNous/astrlink-guard"
const maxModelFile = 512 << 20

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
}

func NewCatalog() *Catalog {
	return &Catalog{client: &http.Client{Timeout: 15 * time.Minute}, baseURL: "https://huggingface.co"}
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *Catalog) get(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("model download returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
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

// Latest resolves a released version to an immutable commit before fetching any
// files. Moving tags or main updates cannot mix weights from different releases.
func (c *Catalog) Latest(ctx context.Context) (ModelRelease, error) {
	var refs struct {
		Tags []struct {
			Name   string `json:"name"`
			Commit string `json:"targetCommit"`
		} `json:"tags"`
	}
	if err := c.readJSON(ctx, "/api/models/"+modelRepository+"/refs", &refs); err != nil {
		return ModelRelease{}, err
	}
	var release ModelRelease
	for _, tag := range refs.Tags {
		if !semver.IsValid(tag.Name) || semver.Prerelease(tag.Name) != "" || !revisionPattern.MatchString(tag.Commit) {
			continue
		}
		if release.Version == "" || semver.Compare(tag.Name, release.Version) > 0 {
			release = ModelRelease{Version: tag.Name, Revision: tag.Commit}
		}
	}
	if release.Version == "" {
		return release, errors.New("no supported model release is available")
	}
	return release, nil
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
		if !ok || !digestPattern.MatchString(file.SHA256) || file.Size <= 0 || file.Size > maxModelFile {
			return installation, fmt.Errorf("invalid model manifest entry: %s", name)
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

// Download writes a new installation directory and only publishes it after all
// hashes match. The caller switches the active selection after loading succeeds.
func (c *Catalog) Download(ctx context.Context, root string, installation Installation, progress func(DownloadProgress)) (string, error) {
	if !installation.Precision.Valid() || !revisionPattern.MatchString(installation.Revision) {
		return "", errors.New("invalid installation")
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
		if file.Size <= 0 || file.Size > maxModelFile || !digestPattern.MatchString(file.SHA256) {
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
			return "", fmt.Errorf("model integrity check failed: %s", name)
		}
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
	destination := filepath.Join(root, installation.Revision+"-"+string(installation.Precision))
	if existing, err := ReadInstallation(destination); err == nil && existing.Revision == installation.Revision && existing.Precision == installation.Precision {
		if err := VerifyInstallation(ctx, destination, existing); err == nil {
			return destination, nil
		}
	}
	backup := ""
	if _, err := os.Lstat(destination); err == nil {
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
