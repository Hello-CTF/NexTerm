// Ported from AstrLink privacy-worker (Apache-2.0, commit 24ddf0d823785fe9b3ed7ee23b3a0fc517313e93)
// src/manifest.rs.
// Modified for NexTerm; see ../third_party/ASTRLINK_NOTICE.md.

package inference

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxRequestTokens = 128 * 1024
	modelWindow      = 512
	modelStride      = 128
	// Token ids index the embedding table; no tokenizer comes close to this.
	maxPadTokenID = (1 << 24) - 1
)

// modelManifest is the subset of AstrLink's model manifest that a NexTerm
// installation can produce: an HF token-classification model with BIO tags,
// the fixed Guard label mapping and an optional static FP16 input shape.
type modelManifest struct {
	revision          string
	precision         string
	modelPath         string
	externalDataPaths []string
	tokenizerPath     string
	configPath        string
	files             map[string]int64
	staticSequence    int // 0 for dynamic input shapes
	padTokenID        int64
	labelMapping      map[string]*string
	inputIDs, mask    string
	tokenTypeIDs      string
	outputName        string
	window, stride    int
	maxRequestTokens  int
}

var errInvalidManifest = errors.New("invalid_model_manifest")

// loadInstallationManifest adapts the verified download manifest written by
// privacy.Catalog to the model manifest in memory, without modifying files.
func loadInstallationManifest(directory string) (*modelManifest, error) {
	path := filepath.Join(directory, "installation.json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errInvalidManifest
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	file.Close()
	if err != nil {
		return nil, err
	}
	var installation struct {
		Version   *string `json:"version"`
		Revision  *string `json:"revision"`
		Precision *string `json:"precision"`
		Files     map[string]struct {
			Size   *int64  `json:"size"`
			SHA256 *string `json:"sha256"`
		} `json:"files"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&installation); err != nil || decoder.More() || installation.Version == nil || *installation.Version == "" ||
		installation.Revision == nil || installation.Precision == nil || installation.Files == nil {
		return nil, errInvalidManifest
	}
	m := &modelManifest{
		revision: *installation.Revision, precision: *installation.Precision,
		tokenizerPath: "tokenizer.json", configPath: "config.json",
		files: map[string]int64{}, labelMapping: map[string]*string{},
		inputIDs: "input_ids", mask: "attention_mask", tokenTypeIDs: "token_type_ids", outputName: "logits",
		window: modelWindow, stride: modelStride, maxRequestTokens: maxRequestTokens,
	}
	switch m.precision {
	case "int8":
		m.modelPath = "model_int8.onnx"
	case "fp32":
		m.modelPath = "model.onnx"
	case "fp16":
		m.modelPath = "model_fp16_static512.onnx"
		m.staticSequence, m.padTokenID = modelWindow, 1
	default:
		return nil, errors.New("unsupported_precision")
	}
	if m.precision != "fp16" {
		m.externalDataPaths = []string{m.modelPath + "_data"}
	}
	for _, kind := range guardKinds {
		m.labelMapping[kind] = &kind
	}
	for name, file := range installation.Files {
		if file.Size == nil || file.SHA256 == nil || !validRelativePath(name) || *file.Size <= 0 || !validLowerHex(*file.SHA256, 64) {
			return nil, errInvalidManifest
		}
		m.files[name] = *file.Size
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *modelManifest) validate() error {
	if !validLowerHex(m.revision, 40) || len(m.files) == 0 || len(m.files) > 128 || len(m.labelMapping) > 256 {
		return errInvalidManifest
	}
	if m.window == 0 || m.stride >= m.window || m.window > m.maxRequestTokens || m.maxRequestTokens > maxRequestTokens {
		return errInvalidManifest
	}
	if m.staticSequence != 0 && (m.staticSequence != m.window || m.padTokenID < 0 || m.padTokenID > maxPadTokenID) {
		return errInvalidManifest
	}
	required := append([]string{m.modelPath, m.tokenizerPath, m.configPath}, m.externalDataPaths...)
	for _, path := range required {
		if _, ok := m.files[path]; !ok || !validRelativePath(path) {
			return errInvalidManifest
		}
	}
	for _, kind := range m.labelMapping {
		if kind != nil && !canonicalKind(*kind) {
			return errInvalidManifest
		}
	}
	return nil
}

// validateFiles checks that every declared file is a regular, non-symlink
// file of the declared size. Content hashes are verified by the installer.
func (m *modelManifest) validateFiles(directory string) error {
	for name, size := range m.files {
		info, err := os.Lstat(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			return errors.New("model_not_installed")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != size {
			return errors.New("invalid_model_installation")
		}
	}
	return nil
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !(value[i] >= '0' && value[i] <= '9') && !(value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.ContainsAny(value, `\:`) {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
